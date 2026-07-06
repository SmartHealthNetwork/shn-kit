// Package kitd is the SHN Kit daemon: a loopback-only, session-token-gated
// HTTP API that fronts the supervisor's child status, the scenario runner's
// async run dispatch, and the event bus's SSE run timeline. It is the
// surface the Electron shell and the integration gate drive — never a
// network service (the daemon
// carries edge payloads and a bearer token; see the loopback-only posture
// mirrored from gateway's OBSERVER_ADDR, app/app.go).
package kitd

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/bootstrap"
	"github.com/SmartHealthNetwork/shn-kit/byo"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/runhistory"
	"github.com/SmartHealthNetwork/shn-kit/runner"
	"github.com/SmartHealthNetwork/shn-kit/supervisor"
	"github.com/SmartHealthNetwork/shn-kit/update"
)

// Config wires a Daemon to the pieces it fronts.
type Config struct {
	APIAddr  string // EXPLICIT loopback host required, e.g. "127.0.0.1:0" (port 0 = ephemeral)
	StateDir string
	Token    string // "" ⇒ generate 128-bit hex
	Bus      *event.Bus
	Sup      *supervisor.Supervisor
	Runner   *runner.Runner     // OPTIONAL at construction: nil ⇒ /api/runs routes 503 until SetRunner (daemon-first: kitd serves before the stack — and hence the Runner — exists).
	Boot     *bootstrap.Machine // nil ⇒ /api/bootstrap routes 404 (unit tests that don't exercise bootstrap embed a Config without it)

	// History is the run-history Store /api/history serves. nil ⇒
	// /api/history routes 404 (unit tests that don't exercise history keep
	// working unchanged, mirroring the Boot nil-Config-field pattern above).
	History *runhistory.Store

	// BYO is the persisted bring-your-own systems swap-config store backing
	// the /api/byo* routes. nil ⇒ every
	// /api/byo* route 404s "byo not configured" (unit tests that don't
	// exercise BYO keep working
	// unchanged, mirroring the Boot/History nil-Config-field pattern above).
	BYO *byo.Store

	// PatientAppURL is the Smart Health account patient app's URL.
	// "" ⇒ omitted from GET /api/status entirely, rather than serialized as
	// an empty string, so a client can treat key-presence as "configured".
	PatientAppURL string

	// UIDir is the dir of the built renderer assets served UNGATED at
	// GET /ui/ ("" ⇒ no route) — ungated by design, since the
	// renderer's assets carry no secrets; everything the UI *does* still
	// authenticates per-call with the session token. The renderer build
	// lands here via --ui-dir.
	UIDir string

	// Restarter is the seam POST /api/children/{name}/restart dispatches a
	// deliberate per-child restart through. In
	// production main wires an adapter over the real *supervisor.Supervisor's
	// Restart method; unit tests inject a fake so a restart round-trip can be
	// asserted without spawning a real child process. Kept as an interface
	// (not *supervisor.Supervisor directly, even though Sup above already IS
	// one) purely for that test seam.
	Restarter Restarter

	// TokenStorage optionally reports which bootstrap.TokenStore backend is
	// actually in effect for this Kit: "keychain", or
	// "file (keychain unavailable: <reason>)" once a fail-visible fallback
	// has occurred — the same Detail() seam bootstrap.NewKeyringTokenStore's
	// concrete type implements. nil (the plain file-backed dev default, and
	// every unit-test Config embedding that doesn't set it)
	// omits "tokenStorage" from GET /api/bootstrap entirely, mirroring
	// PatientAppURL's key-presence contract above.
	TokenStorage TokenStorage

	// ManifestPath is the package-time versions.json manifest's absolute path
	// (tools/kitassets/manifest.sh) — main's
	// --manifest flag. GET /api/about serves this file's bytes VERBATIM (no
	// decode/re-encode). "" (a dev checkout with no packaged manifest, or a
	// path that turns out to be unreadable at request time) answers a
	// 404-with-body — the same honest-absence contract as PatientAppURL/
	// TokenStorage, just via a status code rather than an omitted JSON key,
	// since /api/about's body IS the manifest bytes rather than an envelope
	// that could carry a null.
	ManifestPath string
}

// TokenStorage is the Detail() seam Config.TokenStorage satisfies — kitd
// only needs this one method, never the full bootstrap.TokenStore interface
// (kit/boundary_test.go's monorepo-import posture is unaffected either way,
// since bootstrap already lives in this module).
type TokenStorage interface {
	Detail() string
}

// Restarter is the child-restart seam Config.Restarter satisfies —
// RestartChild, not Restart, so it reads as kitd's OWN contract rather than
// a re-export of supervisor.Supervisor's method name (main.go adapts
// *supervisor.Supervisor.Restart to it with a one-line func wrapper).
type Restarter interface {
	RestartChild(ctx context.Context, name string) error
}

// StackInfo is the boot-resolved facts GET /api/status widens with once
// SetStackInfo has been called: Validator is
// "stand-in" or "packaged" — the SAME posture --fake-validator resolved to
// for the gateway child (via its own flag.Visit derivation) — and
// BRProviderURL is the Java trio's br-provider base ("" when no trio is
// configured, kitd.Stack.BRProviderURL's own contract). Before the first
// SetStackInfo call (daemon-first: kitd serves before the stack exists) both
// fields read as their zero value and are omitted from GET /api/status
// entirely — the same key-presence contract as PatientAppURL — and the
// per-child restart route answers 503 (pre-boot), since Validator == "" is
// this Daemon's only signal that boot has reached that point.
type StackInfo struct {
	Validator     string // "stand-in" | "packaged"
	BRProviderURL string // "" ⇒ no Java trio configured
}

// sessionFile is the session.json contract main and the integration gate
// consume: {"api":"http://127.0.0.1:<port>","token":"<hex>"}, 0600, in
// StateDir.
type sessionFile struct {
	API   string `json:"api"`
	Token string `json:"token"`
}

// Daemon is the Kit's loopback session-token-gated HTTP API. Zero value is
// not usable; construct with New.
type Daemon struct {
	cfg     Config
	token   string
	addr    string
	ready   chan struct{}
	baseCtx context.Context // set in Serve, right after net.Listen — before session.json is written, so no request can ever observe it nil

	// mu guards runner/verify/verifyFn — the daemon-first state that starts
	// nil/empty at construction and is swapped in later, safely, once the
	// substrate stack (main's boot sequence) has actually started.
	mu        sync.RWMutex
	runner    *runner.Runner
	verify    []bootstrap.Probe
	verifyFn  func(context.Context) []bootstrap.Probe
	byo       BYORuntime
	stackInfo StackInfo // zero value (Validator == "") until the first SetStackInfo call

	// update/updateSet back GET /api/status's "update" field.
	// Unlike StackInfo/PatientAppURL, update.Info's own zero
	// value ({Available:false}) is a GENUINE "no update available" result —
	// it cannot double as "never checked yet" the way an empty string/""
	// Validator can — so presence is tracked with an explicit bool rather
	// than inferred from the value (see SetUpdate/getUpdate).
	update    update.Info
	updateSet bool

	// verifyBusy single-flights POST /api/verify: only one re-probe runs at
	// a time, the same posture as the runs routes' single
	// in-flight run.
	verifyBusy atomic.Bool
}

// verifyTimeout bounds every POST /api/verify re-probe: probe funcs make
// live network calls (bootstrap.Verify's discovery GET), which must never be
// able to hang the daemon indefinitely.
const verifyTimeout = 15 * time.Second

// New validates cfg.APIAddr (must be an explicit loopback host — a bare
// ":0" is REJECTED, since SplitHostPort yields host "" and an empty host
// would make net.Listen bind all interfaces) and constructs a Daemon,
// generating a random 128-bit hex token when cfg.Token is empty. It does
// not bind a socket — call Serve for that.
func New(cfg Config) (*Daemon, error) {
	host, _, err := net.SplitHostPort(cfg.APIAddr)
	if err != nil {
		return nil, fmt.Errorf("kitd: APIAddr %q: %w", cfg.APIAddr, err)
	}
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return nil, fmt.Errorf("kitd: APIAddr %q is not loopback; the daemon binds loopback only", cfg.APIAddr)
	}

	token := cfg.Token
	if token == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("kitd: generate session token: %w", err)
		}
		token = hex.EncodeToString(b)
	}

	return &Daemon{
		cfg:    cfg,
		token:  token,
		ready:  make(chan struct{}),
		runner: cfg.Runner,
		verify: []bootstrap.Probe{}, // non-nil so GET /api/bootstrap serializes "verify":[] before the first SetVerify, never null
	}, nil
}

// Addr returns the bound "host:port". Valid only after Ready() has closed.
func (d *Daemon) Addr() string { return d.addr }

// Ready returns a channel closed once session.json has been written to
// StateDir and the daemon is about to start serving.
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// SetRunner wires in the scenario Runner once the substrate stack has
// started (main constructs the Runner only after the gateway child
// is ready — the daemon itself must be reachable before then, hence the
// nil-at-construction/daemon-first gate on /api/runs). Safe to call any
// time after Serve begins running; concurrent /api/runs requests either see
// the old value (503) or the new one (never a torn read).
func (d *Daemon) SetRunner(r *runner.Runner) {
	d.mu.Lock()
	d.runner = r
	d.mu.Unlock()
}

// SetVerify publishes the "hello substrate" bootstrap.Verify results
// (bootstrap/verify.go) so they appear in the next GET /api/bootstrap. Safe
// to call any time after Serve begins running.
func (d *Daemon) SetVerify(probes []bootstrap.Probe) {
	d.mu.Lock()
	d.verify = probes
	d.mu.Unlock()
}

// SetVerifyFunc installs the re-probe closure once boot has the facts it
// needs (discovery URL, holder id, applied BYO endpoints). Until then
// POST /api/verify answers 503 — the same daemon-first posture as the runs
// routes. Deliberately NOT set on the degraded
// reset-raced-boot path: degraded-until-restart means the restart is the
// recovery action, and a re-probe against a cleared bundle would lie.
func (d *Daemon) SetVerifyFunc(fn func(ctx context.Context) []bootstrap.Probe) {
	d.mu.Lock()
	d.verifyFn = fn
	d.mu.Unlock()
}

// BYORuntime is the boot-applied bring-your-own systems state:
// the byo.json Config actually applied to this boot
// (Applied), a ready-to-use browse client over the swapped EHR when one was
// configured (Browser, nil otherwise), the stack's GatewayURL (the /api/byo
// routes need it to point BYO-driven runs at the right ingress), and
// LoadError — non-empty when byo.json existed but was unreadable/corrupt, in
// which case Applied is the fail-safe zero Config the boot fell back to,
// not a lie about what's actually configured.
//
// This is a minimal stub: the stack + boot wiring introduces the type
// and setter so main.go's boot goroutine has somewhere to publish the
// applied BYO state; the GET /api/byo (and related) routes
// read it back via getBYO.
type BYORuntime struct {
	Applied    byo.Config
	Browser    *byo.Browser
	GatewayURL string
	LoadError  string
}

// SetBYO publishes the boot-applied bring-your-own systems state (mirrors
// SetRunner/SetVerifyFunc: the daemon itself must be reachable before boot
// has these facts, since they depend on the stack having started). Safe to
// call any time after Serve begins running.
func (d *Daemon) SetBYO(b BYORuntime) {
	d.mu.Lock()
	d.byo = b
	d.mu.Unlock()
}

// getBYO returns the last-published BYORuntime (the zero value before the
// first SetBYO). Unexported: the route(s) that expose it live in this package.
func (d *Daemon) getBYO() BYORuntime {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.byo
}

// SetStackInfo publishes the boot-resolved StackInfo
// so it appears in the next GET /api/status and unlocks
// POST /api/children/{name}/restart (both gated on Validator != "" — see
// StackInfo's doc). Safe to call any time after Serve begins running,
// mirroring SetRunner/SetBYO/SetVerifyFunc's daemon-first pattern: main
// calls this once, after BuildStack has resolved the facts it carries.
func (d *Daemon) SetStackInfo(info StackInfo) {
	d.mu.Lock()
	d.stackInfo = info
	d.mu.Unlock()
}

// getStackInfo returns the last-published StackInfo (the zero value before
// the first SetStackInfo call).
func (d *Daemon) getStackInfo() StackInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.stackInfo
}

// SetUpdate publishes the launch-time update-check result
// so it appears in the next GET /api/status as "update". Safe
// to call any time after Serve begins running, mirroring SetStackInfo/
// SetBYO's daemon-first pattern: main's boot goroutine fires
// update.Check asynchronously post-Ready and calls this exactly once, ONLY
// on a successful check (a failed check is logged by main and simply never
// calls this — GET /api/status omits "update" entirely in that case, same as
// it never having been checked, since a failed check carries no fact worth
// publishing).
func (d *Daemon) SetUpdate(u update.Info) {
	d.mu.Lock()
	d.update = u
	d.updateSet = true
	d.mu.Unlock()
}

// getUpdate returns the last-published update.Info and whether SetUpdate has
// ever been called — the explicit ok bool is load-bearing (see the Daemon
// struct's update/updateSet doc): update.Info{} is itself a legitimate "no
// update available" result, so it cannot serve as its own "unset" sentinel.
func (d *Daemon) getUpdate() (update.Info, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.update, d.updateSet
}

// Serve binds APIAddr, writes session.json into StateDir, closes Ready(),
// and serves the API until ctx is done, at which point it shuts down
// gracefully via http.Server.Shutdown.
func (d *Daemon) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", d.cfg.APIAddr)
	if err != nil {
		return fmt.Errorf("kitd: listen %q: %w", d.cfg.APIAddr, err)
	}
	d.addr = ln.Addr().String()
	// Set baseCtx before session.json is written (and long before the
	// server starts accepting): the bootstrap signin handler starts a
	// goroutine that must outlive its POST, so it runs under this
	// Serve-lifetime context, never r.Context(). A client racing
	// session.json's appearance on disk must never be able to reach a
	// handler before baseCtx is set.
	d.baseCtx = ctx

	session := sessionFile{API: fmt.Sprintf("http://%s", d.addr), Token: d.token}
	b, err := json.Marshal(session)
	if err != nil {
		ln.Close()
		return fmt.Errorf("kitd: marshal session.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(d.cfg.StateDir, "session.json"), b, 0600); err != nil {
		ln.Close()
		return fmt.Errorf("kitd: write session.json: %w", err)
	}

	close(d.ready)

	srv := &http.Server{
		Handler: d.handler(),
		// BaseContext derives every request's context from the daemon ctx:
		// the bus's SSE handler (handleEvents) blocks on <-r.Context().Done(),
		// which srv.Shutdown alone never fires (Shutdown waits for connections
		// to go idle; it never interrupts active ones) — without this, Serve
		// would hang forever on shutdown while any /events subscriber is
		// connected, the daemon's normal steady state once a UI attaches.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		// Bounded Shutdown, belt-and-braces against any other long-lived
		// request that BaseContext cancellation alone doesn't unstick; on
		// timeout fall back to a hard Close.
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			_ = srv.Close()
		}
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// handler builds the daemon's mux: /health ungated, everything else
// (/api/* and /events) behind the token gate.
func (d *Daemon) handler() http.Handler {
	gated := http.NewServeMux()
	gated.HandleFunc("GET /api/status", d.handleStatus)
	gated.HandleFunc("POST /api/runs", d.handleRunsPost)
	gated.HandleFunc("GET /api/runs", d.handleRunsGet)
	gated.HandleFunc("POST /api/watch", d.handleWatchPost)
	gated.HandleFunc("DELETE /api/watch", d.handleWatchDelete)
	gated.HandleFunc("GET /api/bootstrap", d.handleBootstrapGet)
	gated.HandleFunc("POST /api/verify", d.handleVerifyPost)
	gated.HandleFunc("POST /api/bootstrap/signin", d.handleBootstrapSignInPost)
	gated.HandleFunc("POST /api/bootstrap/reset", d.handleBootstrapResetPost)
	gated.HandleFunc("GET /api/history", d.handleHistoryList)
	gated.HandleFunc("GET /api/history/{runId}", d.handleHistoryGet)
	gated.HandleFunc("GET /api/byo", d.handleBYOGet)
	gated.HandleFunc("PUT /api/byo/ehr", d.handleBYOEHRPut)
	gated.HandleFunc("DELETE /api/byo/ehr", d.handleBYOEHRDelete)
	gated.HandleFunc("PUT /api/byo/davinci", d.handleBYODaVinciPut)
	gated.HandleFunc("DELETE /api/byo/davinci", d.handleBYODaVinciDelete)
	gated.HandleFunc("GET /api/byo/patients", d.handleBYOPatientsGet)
	gated.HandleFunc("GET /api/byo/patients/{fhirId}/context", d.handleBYOPatientContextGet)
	gated.HandleFunc("POST /api/children/{name}/restart", d.handleChildRestart)
	gated.HandleFunc("GET /api/about", d.handleAbout)
	gated.HandleFunc("GET /api/support-bundle", d.handleSupportBundle)
	// Only GET /events is forwarded to the bus's handler; its internal
	// GET /health is intentionally unreachable through kitd's routing —
	// the daemon's own ungated /health below is the health surface.
	gated.Handle("GET /events", d.cfg.Bus.Handler())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	if d.cfg.UIDir != "" {
		// Ungated by design: the built renderer's assets are public
		// bytes; everything the UI *does* still authenticates per-call with the
		// session token. http.FileServer path-cleans, so StateDir (session.json,
		// tokens) is unreachable — pinned by the traversal test rows. Directory
		// listings are disabled: a bare directory
		// URL answers 404, not an index of assets/.
		mux.Handle("GET /ui/", http.StripPrefix("/ui/", noDirListing(http.Dir(d.cfg.UIDir))))
	}
	mux.Handle("/", d.authMiddleware(gated))
	return mux
}

// noDirListing serves files from root but answers 404 for directory paths
// other than "/" (which serves index.html) — a bare "GET /ui/assets/" must
// not enumerate the bundle.
func noDirListing(root http.FileSystem) http.Handler {
	fs := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := strings.TrimSuffix(r.URL.Path, "/"); p != "" && p != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// authMiddleware accepts `Authorization: Bearer <token>` or `?token=<token>`
// (EventSource cannot set headers, hence the query fallback), comparing
// with crypto/subtle.ConstantTimeCompare — else 401 JSON.
func (d *Daemon) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(d.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (d *Daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{"children": d.cfg.Sup.Status()}
	if d.cfg.PatientAppURL != "" {
		resp["patientAppUrl"] = d.cfg.PatientAppURL
	}
	// Both fields are omitted entirely
	// (never serialized as an empty string) until SetStackInfo has actually
	// been called — key-presence semantics matching patientAppUrl above.
	if si := d.getStackInfo(); si.Validator != "" {
		resp["validator"] = si.Validator
		if si.BRProviderURL != "" {
			resp["brProviderUrl"] = si.BRProviderURL
		}
	}
	// "update" is omitted until
	// SetUpdate has actually been called — see getUpdate's doc for why that
	// can't be inferred from update.Info's zero value the way Validator's
	// empty string can.
	if u, ok := d.getUpdate(); ok {
		resp["update"] = u
	}
	writeJSON(w, http.StatusOK, resp)
}

// runRequest is POST /api/runs's body: {"lane","uc","branch","member"}.
// Member is optional and UC-gated by runner.validateRow: required (non-
// empty) for uc "freeform", rejected (must be "") for every other UC — see
// runner.Req's doc.
type runRequest struct {
	Lane   string `json:"lane"`
	UC     string `json:"uc"`
	Branch string `json:"branch"`
	Member string `json:"member"`
}

// getRunner returns the currently-wired Runner, or nil before SetRunner has
// ever been called (daemon-first: the daemon itself is reachable before the
// substrate stack — and hence the Runner — exists).
func (d *Daemon) getRunner() *runner.Runner {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.runner
}

func (d *Daemon) handleRunsPost(w http.ResponseWriter, r *http.Request) {
	rn := d.getRunner()
	if rn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stack not started"})
		return
	}
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode request body: %v", err)})
		return
	}
	// r.Context() is safe here ONLY because Start ignores its ctx argument
	// entirely — the spawned row runs under r.baseCtx (runner.go's Start doc).
	// Contrast handleWatchPost below, which hands StartWatch d.baseCtx: a
	// watch's ctx IS its lifetime, so a request-scoped ctx there would
	// finalize the watch the instant this handler's POST response is
	// written. Do not "fix" one call site to match the other.
	runID, err := rn.Start(r.Context(), runner.Req{Lane: req.Lane, UC: req.UC, Branch: req.Branch, Member: req.Member})
	if err != nil {
		if errors.Is(err, runner.ErrRunInFlight) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "run in flight"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID})
}

// handleWatchPost serves POST /api/watch: opens a
// watch session that attributes externally-originated (partner-driven)
// gateway traffic to a run. 409 when a run or another watch already holds
// the sequential lock; 503 before SetRunner.
//
// d.baseCtx, NEVER r.Context(): the watch's ctx IS its
// lifetime — StartWatch parks until this ctx is done or StopWatch closes it,
// potentially long after this POST's response has been written. A
// request-scoped ctx would self-finalize the watch the moment net/http
// cancels it as this handler returns, defeating the entire feature. Contrast
// handleRunsPost above, whose r.Context() is safe only because Start ignores
// its ctx argument — do not "fix" this call to match that one.
func (d *Daemon) handleWatchPost(w http.ResponseWriter, _ *http.Request) {
	rn := d.getRunner()
	if rn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stack not started"})
		return
	}
	runID, err := rn.StartWatch(d.baseCtx)
	if err != nil {
		if errors.Is(err, runner.ErrRunInFlight) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "run in flight"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID})
}

// handleWatchDelete serves DELETE /api/watch: closes the open watch session
// and returns its final runner.Result. 404 when no watch is open (either
// none was ever started, or it already self-finalized via ctx cancellation);
// 503 before SetRunner.
func (d *Daemon) handleWatchDelete(w http.ResponseWriter, _ *http.Request) {
	rn := d.getRunner()
	if rn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stack not started"})
		return
	}
	res, err := rn.StopWatch()
	if err != nil {
		if errors.Is(err, runner.ErrNoWatch) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no watch in progress"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleRunsGet(w http.ResponseWriter, _ *http.Request) {
	rn := d.getRunner()
	if rn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stack not started"})
		return
	}
	writeJSON(w, http.StatusOK, rn.Results())
}

// gatewayRestartRefused is POST /api/children/gateway/restart's 403 body:
// unlike the Java trio's children, the gateway
// child owns its allocated port, its driver keypair, and the runner's
// wiring against it — all of which are re-derived only by a full shnkitd
// respawn, never a bare process bounce. The per-child restart seam this
// route otherwise opens is for the Java children (validator/data-server/
// br-provider); a gateway restart stays the existing full-Kit "Restart the
// Kit" action.
const gatewayRestartRefused = "restarting gateway would invalidate its port, driver keypair, and runner wiring, which only a full Kit restart re-derives; the per-child restart seam is for the Java trio's children, not gateway"

// handleChildRestart serves POST /api/children/{name}/restart: a deliberate
// stop-then-respawn of one supervised child,
// distinct from the existing "Restart the Kit" full-process action.
//
//   - 503 before SetRunner has ever been called — the SAME daemon-first gate
//     every sibling route in this file uses (getRunner() == nil), not
//     StackInfo.Validator == "". SetStackInfo
//     fires right after BuildStack, BEFORE CopyPrewarmedH2 and the
//     sequential child-start loop that ends in SetRunner: gating on
//     Validator alone would leave the whole boot window answering the Restarter's
//     own 404 ("unknown child") for a restart request instead of 503 ("not
//     started") — a misleading code, not a microsecond race.
//   - 403 for "gateway" (gatewayRestartRefused) — never forwarded
//     to Restarter at all.
//   - 409 when a run or watch session is in flight (Runner.InFlight(), a
//     best-effort gate): a plain atomic read, not a TryLock probe,
//     so it never itself steals the sequential lock — but
//     for exactly that reason a run/watch CAN start in the window between
//     this check and the Restarter call below. That residual race is
//     accepted: the supervisor's own hardened Stop→respawn arc (generation
//     fencing) makes a restart landing moments into a fresh run no worse
//     than any other unexpected child bounce a run already has to tolerate.
//   - 404 when Restarter reports an error (supervisor.Restart's own
//     contract: an unknown/unregistered child name is its only failure mode
//     short of the child's own respawn failing, which the operator would
//     see surfaced via the status panel's child state either way).
//   - 200 {"restarted": "<name>"} on success.
func (d *Daemon) handleChildRestart(w http.ResponseWriter, r *http.Request) {
	rn := d.getRunner()
	if rn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stack not started"})
		return
	}
	name := r.PathValue("name")
	if name == gatewayChildName {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": gatewayRestartRefused})
		return
	}
	if rn.InFlight() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a run or watch is in flight"})
		return
	}
	if err := d.cfg.Restarter.RestartChild(r.Context(), name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"restarted": name})
}

// handleAbout serves GET /api/about: the package-time versions.json
// manifest's bytes, VERBATIM — no
// decode/re-marshal, so the served bytes are byte-identical to what
// tools/kitassets/manifest.sh wrote. 404-with-body (an honest JSON error,
// never a bare empty 404) when Config.ManifestPath is "" (a dev checkout
// with no packaged manifest) OR the path is set but unreadable — both read
// as "not available" to a caller, not a server error.
func (d *Daemon) handleAbout(w http.ResponseWriter, _ *http.Request) {
	if d.cfg.ManifestPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "manifest not available (dev build)"})
		return
	}
	b, err := os.ReadFile(d.cfg.ManifestPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("manifest not available: %v", err)})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// bootstrapResponse flattens bootstrap.Status (embedding) alongside the
// latest bootstrap.Verify results, the wire shape GET /api/bootstrap backs
// the UI's poll with.
type bootstrapResponse struct {
	bootstrap.Status
	Verify []bootstrap.Probe `json:"verify"`

	// TokenStorage mirrors Config.TokenStorage's Detail(): "" (omitted,
	// key-presence semantics matching patientAppUrl)
	// when Config.TokenStorage is nil.
	TokenStorage string `json:"tokenStorage,omitempty"`
}

func (d *Daemon) handleBootstrapGet(w http.ResponseWriter, _ *http.Request) {
	if d.cfg.Boot == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bootstrap not configured"})
		return
	}
	d.mu.RLock()
	verify := d.verify
	d.mu.RUnlock()
	resp := bootstrapResponse{Status: d.cfg.Boot.Status(), Verify: verify}
	if d.cfg.TokenStorage != nil {
		resp.TokenStorage = d.cfg.TokenStorage.Detail()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleVerifyPost serves POST /api/verify: re-runs the same
// probe closure boot installed via SetVerifyFunc, publishes the fresh
// result via SetVerify (so a following GET /api/bootstrap reflects it too),
// and returns it. 503 before boot has called SetVerifyFunc; 409 if a
// re-probe is already in flight (single-flight, mirroring /api/runs).
func (d *Daemon) handleVerifyPost(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	fn := d.verifyFn
	d.mu.RUnlock()
	if fn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "verify not available until boot completes"})
		return
	}
	if !d.verifyBusy.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "verify already in flight"})
		return
	}
	defer d.verifyBusy.Store(false)
	ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	defer cancel()
	probes := fn(ctx)
	d.SetVerify(probes)
	writeJSON(w, http.StatusOK, probes)
}

func (d *Daemon) handleBootstrapSignInPost(w http.ResponseWriter, _ *http.Request) {
	if d.cfg.Boot == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bootstrap not configured"})
		return
	}
	// d.baseCtx, NEVER r.Context(): SignIn's PKCE/provisioning goroutine
	// must outlive this POST, which net/http would otherwise cancel the
	// instant this handler returns (the runner makes the same
	// async-vs-request split for /api/runs).
	authorizeURL, err := d.cfg.Boot.SignIn(d.baseCtx)
	if err != nil {
		if errors.Is(err, bootstrap.ErrSignInInProgress) || errors.Is(err, bootstrap.ErrAlreadyProvisioned) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorizeUrl": authorizeURL})
}

// handleBootstrapResetPost clears the bootstrap Machine's persisted tokens
// and secrets bundle, returning it to signin-required. Between this call
// returning and an operator restarting shnkitd, the running stack is
// degraded BY DESIGN: the secrets bundle is gone, so a gateway
// child that crashes in that window respawns into a failing child (it has
// no identity to re-materialize) until shnkitd itself is restarted — which
// alone hands out a fresh bootstrap.Machine (Machine.Reset does not reopen
// Provisioned()). Callers must restart promptly; {"restartRequired":true}
// is the signal the UI surfaces to the operator for exactly that reason.
func (d *Daemon) handleBootstrapResetPost(w http.ResponseWriter, _ *http.Request) {
	if d.cfg.Boot == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bootstrap not configured"})
		return
	}
	if err := d.cfg.Boot.Reset(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"restartRequired": true})
}

// handleHistoryList serves GET /api/history: every saved run's Summary,
// newest first, never null (the UI client consumes this).
func (d *Daemon) handleHistoryList(w http.ResponseWriter, _ *http.Request) {
	if d.cfg.History == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run history not configured"})
		return
	}
	sums, err := d.cfg.History.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if sums == nil {
		sums = []runhistory.Summary{}
	}
	writeJSON(w, http.StatusOK, sums)
}

// handleHistoryGet serves GET /api/history/{runId}: the full Record (Summary
// plus its Events slice), or 404 if no run with that id has been saved.
func (d *Daemon) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	if d.cfg.History == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run history not configured"})
		return
	}
	rec, err := d.cfg.History.Get(r.PathValue("runId"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// ---- /api/byo config + browse routes ---------------------------------------

// byoIngressEndpoints is the Da Vinci inbound ingress surface a
// bring-your-own gateway boots with once an EHR/DaVinci swap takes effect —
// mirrors gateway/engine.Handler()'s ingress block, the SOURCE OF TRUTH this
// list must track (update it there first, then here).
var byoIngressEndpoints = []string{
	"/cds-services",
	"/cds-services/{id}",
	"/Questionnaire/$questionnaire-package",
	"/Claim/$submit",
}

// byoEHRResponse is GET /api/byo's "ehr" lane wire shape. clientKeyPem is
// deliberately absent: key material is write-only and never echoed;
// HasClientKey reports presence without ever carrying the bytes.
//
// DemoPersonas is a *bool tri-state: true/false when a
// live sentinel check against the applied swap's connected server
// succeeds, nil (JSON null) when there is nothing to check or the check
// itself errors — see demoPersonasState's doc. It is a pointer (not
// `bool` + omitempty) deliberately: the wire contract wants an EXPLICIT
// null, not an omitted key, so the panel can render "unknown" rather than
// defaulting to a guessed false.
type byoEHRResponse struct {
	DataURL      string `json:"dataUrl"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	Alg          string `json:"alg,omitempty"`
	Scope        string `json:"scope,omitempty"`
	KID          string `json:"kid,omitempty"`
	HasClientKey bool   `json:"hasClientKey"`
	Applied      bool   `json:"applied"`
	DemoPersonas *bool  `json:"demoPersonas"`
}

// conformantLaneSentinelMember is the conformant lane's canonical member —
// MBR-COVERED, the persona conformantUC02..conformantUC05 all hardcode
// (kit/runner/rows_conformant.go) — used as GET /api/byo's "does your
// connected server carry the demo personas" sentinel check. One check
// stands in for the whole kit/seed/demo-personas-
// conformant.json bundle because that bundle loads all-or-nothing (a single
// manual transaction POST): if the partner server resolves MBR-COVERED it
// carries the bundle; if it doesn't, this member is as good a sentinel as
// any of the other four.
const conformantLaneSentinelMember = "MBR-COVERED"

// demoPersonasState answers GET /api/byo's "ehr.demoPersonas" tri-state:
// true/false from a live sentinel check
// (byo.Browser.HasPersona) against the APPLIED swap's connected server, nil
// when there is nothing meaningful to check (no EHR swap applied THIS boot,
// or applied but this boot never got as far as building a Browser) OR when
// the sentinel check itself errors — shown, never assumed: a
// transient probe failure must render as "we don't know," never as a false
// "not loaded."
func demoPersonasState(ctx context.Context, applied bool, browser *byo.Browser) *bool {
	if !applied || browser == nil {
		return nil
	}
	ok, err := browser.HasPersona(ctx, conformantLaneSentinelMember)
	if err != nil {
		return nil
	}
	return &ok
}

// byoDaVinciResponse is GET /api/byo's "davinci" lane wire shape. Unlike the
// EHR lane, PublicKeyPEM is public material (a Da Vinci ingress client
// registration) — echoing it back is not a key-hygiene concern.
type byoDaVinciResponse struct {
	ClientID     string `json:"clientId"`
	Alg          string `json:"alg"`
	PublicKeyPEM string `json:"publicKeyPem"`
	Applied      bool   `json:"applied"`
}

// byoIngressResponse is GET /api/byo's "ingress" block: null until this
// process has actually booted a gateway (BYORuntime.GatewayURL == "").
type byoIngressResponse struct {
	BaseURL        string   `json:"baseUrl"`
	TokenURL       string   `json:"tokenUrl"`
	SmartConfigURL string   `json:"smartConfigUrl"`
	Endpoints      []string `json:"endpoints"`
}

// byoGetResponse is GET /api/byo's full body.
type byoGetResponse struct {
	EHR       *byoEHRResponse     `json:"ehr"`
	DaVinci   *byoDaVinciResponse `json:"davinci"`
	Ingress   *byoIngressResponse `json:"ingress"`
	LoadError string              `json:"loadError,omitempty"`
}

// handleBYOGet serves GET /api/byo: the currently SAVED byo.json config (not
// necessarily what this process booted with — a saved-but-unrestarted edit
// renders with applied:false, the honest "restart to apply" state). Each
// lane's applied bool is a deep-equal of the saved lane against
// BYORuntime.Applied (the boot-published state); a lane absent from the
// saved config renders as null, never applied:false. The ingress block is
// null pre-boot (GatewayURL == ""); loadError is omitted when clean.
func (d *Daemon) handleBYOGet(w http.ResponseWriter, r *http.Request) {
	if d.cfg.BYO == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "byo not configured"})
		return
	}
	saved, err := d.cfg.BYO.Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rt := d.getBYO()

	resp := byoGetResponse{LoadError: rt.LoadError}
	if saved.EHR != nil {
		_, statErr := os.Stat(d.cfg.BYO.EHRKeyPath())
		applied := rt.Applied.EHR != nil && reflect.DeepEqual(*saved.EHR, *rt.Applied.EHR)
		resp.EHR = &byoEHRResponse{
			DataURL:      saved.EHR.DataURL,
			TokenURL:     saved.EHR.TokenURL,
			ClientID:     saved.EHR.ClientID,
			Alg:          saved.EHR.Alg,
			Scope:        saved.EHR.Scope,
			KID:          saved.EHR.KID,
			HasClientKey: statErr == nil,
			Applied:      applied,
			DemoPersonas: func() *bool {
				// Bounded exactly like PUT /api/byo/ehr's own live probe
				// (kitd.go's verifyTimeout pattern): the
				// sentinel check is a live network call against a
				// bring-your-own operator's own connected server, which must
				// never be able to hang this GET indefinitely.
				ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
				defer cancel()
				return demoPersonasState(ctx, applied, rt.Browser)
			}(),
		}
	}
	if saved.DaVinci != nil {
		resp.DaVinci = &byoDaVinciResponse{
			ClientID:     saved.DaVinci.ClientID,
			Alg:          saved.DaVinci.Alg,
			PublicKeyPEM: saved.DaVinci.PublicKeyPEM,
			Applied:      rt.Applied.DaVinci != nil && reflect.DeepEqual(*saved.DaVinci, *rt.Applied.DaVinci),
		}
	}
	if rt.GatewayURL != "" {
		resp.Ingress = &byoIngressResponse{
			BaseURL:        rt.GatewayURL,
			TokenURL:       rt.GatewayURL + "/oauth/token",
			SmartConfigURL: rt.GatewayURL + "/.well-known/smart-configuration",
			Endpoints:      byoIngressEndpoints,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// putEHRRequest is PUT /api/byo/ehr's body. ClientKeyPEM is write-only:
// accepted here, persisted to the Store's 0600 key file, and never echoed by
// any GET.
type putEHRRequest struct {
	DataURL      string `json:"dataUrl"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientKeyPEM string `json:"clientKeyPem,omitempty"`
	Alg          string `json:"alg,omitempty"`
	Scope        string `json:"scope,omitempty"`
	KID          string `json:"kid,omitempty"`
}

// handleBYOEHRPut serves PUT /api/byo/ehr: decode, validate
// with gateway-boot parity (byo.ValidateEHR), build a transient client off
// the SUBMITTED config and live-probe it under a verifyTimeout-bounded ctx
// (the Kit refuses a swap the gateway couldn't itself boot on),
// and only then persist via Store.SetEHR. Any failure (decode, validation,
// or probe) leaves byo.json untouched.
func (d *Daemon) handleBYOEHRPut(w http.ResponseWriter, r *http.Request) {
	if d.cfg.BYO == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "byo not configured"})
		return
	}
	var req putEHRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode request body: %v", err)})
		return
	}
	e := byo.EHR{
		DataURL:  req.DataURL,
		TokenURL: req.TokenURL,
		ClientID: req.ClientID,
		Alg:      req.Alg,
		Scope:    req.Scope,
		KID:      req.KID,
	}
	keyPEM := []byte(req.ClientKeyPEM)

	if err := byo.ValidateEHR(e, keyPEM); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	hc, err := byo.EHRHTTPClient(&e, keyPEM)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	defer cancel()
	probe := byo.ProbeEHR(ctx, hc, e.DataURL)
	if !probe.OK {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": probe.Detail})
		return
	}

	if err := d.cfg.BYO.SetEHR(e, keyPEM); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"restartRequired": true})
}

// handleBYOEHRDelete serves DELETE /api/byo/ehr: clears the saved EHR lane
// (and its key file, if any) — takes effect on the next restart.
func (d *Daemon) handleBYOEHRDelete(w http.ResponseWriter, _ *http.Request) {
	if d.cfg.BYO == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "byo not configured"})
		return
	}
	if err := d.cfg.BYO.ClearEHR(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"restartRequired": true})
}

// putDaVinciRequest is PUT /api/byo/davinci's body.
type putDaVinciRequest struct {
	ClientID     string `json:"clientId"`
	Alg          string `json:"alg"`
	PublicKeyPEM string `json:"publicKeyPem"`
}

// handleBYODaVinciPut serves PUT /api/byo/davinci: validated with
// gateway-boot parity (byo.ValidateDaVinci, run inside Store.SetDaVinci) and
// persisted — takes effect on the next restart. No live probe here: unlike
// the EHR lane, there is nothing to reach yet (this registers an INBOUND
// ingress client; the gateway is the one being called, not the caller).
func (d *Daemon) handleBYODaVinciPut(w http.ResponseWriter, r *http.Request) {
	if d.cfg.BYO == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "byo not configured"})
		return
	}
	var req putDaVinciRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode request body: %v", err)})
		return
	}
	dv := byo.DaVinci{ClientID: req.ClientID, Alg: req.Alg, PublicKeyPEM: req.PublicKeyPEM}
	if err := d.cfg.BYO.SetDaVinci(dv); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"restartRequired": true})
}

// handleBYODaVinciDelete serves DELETE /api/byo/davinci.
func (d *Daemon) handleBYODaVinciDelete(w http.ResponseWriter, _ *http.Request) {
	if d.cfg.BYO == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "byo not configured"})
		return
	}
	if err := d.cfg.BYO.ClearDaVinci(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"restartRequired": true})
}

// handleBYOPatientsGet serves GET /api/byo/patients: proxies to the
// boot-applied Browser (BYORuntime.Browser). 409 when no EHR swap has been
// applied THIS boot (Browser nil — a saved-but-unrestarted swap does not
// count); 502 with the partner server's own error string (kept human-usable
// for the browse panel) on a browse failure — the byo package owns the deep
// query behavior, this handler only proxies.
func (d *Daemon) handleBYOPatientsGet(w http.ResponseWriter, r *http.Request) {
	if d.cfg.BYO == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "byo not configured"})
		return
	}
	rt := d.getBYO()
	if rt.Browser == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "connect your EHR and restart the Kit first"})
		return
	}
	patients, err := rt.Browser.Patients(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if patients == nil {
		patients = []byo.PatientSummary{}
	}
	writeJSON(w, http.StatusOK, patients)
}

// handleBYOPatientContextGet serves GET /api/byo/patients/{fhirId}/context:
// same gating/proxy posture as handleBYOPatientsGet.
func (d *Daemon) handleBYOPatientContextGet(w http.ResponseWriter, r *http.Request) {
	if d.cfg.BYO == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "byo not configured"})
		return
	}
	rt := d.getBYO()
	if rt.Browser == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "connect your EHR and restart the Kit first"})
		return
	}
	pc, err := rt.Browser.Context(r.Context(), r.PathValue("fhirId"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pc)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
