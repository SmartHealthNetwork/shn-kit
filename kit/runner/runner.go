// Package runner drives one lane+UC+branch scenario against the Kit's local
// gateway child through the public gateway/scenariodriver surface, brackets
// it with run-lifecycle events on the Kit event bus (kit/event), stamps the
// observer relay (kit/relay) so its frames attribute to the run, and merges
// the substrate Audit Plane's chain records into the run's timeline via a
// seq-window (kit/auditread) — sound only because Kit runs are
// sequential-only in v1 (see the auditread package doc).
//
// Two lanes exercise the same eight Prior Authorization scenarios (UC-01…08)
// two different ways: "ehr" — the Plain EHR lane — drives the SECOND gateway
// child's /scenario/* routes on the provider-data origination profile (every
// scenario's order and evidence read off the bundled FHIR data server, the
// payer legs routed to the hosted Da Vinci reference payer), available only
// with the Java trio; "conformant" drives the Da Vinci ingress directly
// (CRD/DTR/PAS, UDAP B2B direct bearer). Both lanes answer to the same
// reference payer. The row tables live in rows_ehr.go / rows_conformant.go.
//
// Two rows are the exception to the per-lane child: the Plain-EHR uc03
// bridge-refuse bridging demo and the conformant lane's eligibility row (no
// Da Vinci ingress operation exists for eligibility) both originate on the
// MAIN child (rows_ehr.go's ehrScenarioMain).
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"

	"github.com/SmartHealthNetwork/shn-kit/auditread"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/relay"
)

// Result state values.
const (
	StatePassed = "passed"
	StateFailed = "failed"
)

// ErrRunInFlight is returned by Run/Start/StartWatch when a run or watch is
// already in progress (sequential-only v1: at most one row goroutine OR
// watch session runs at a time — they share the same lock, the soundness
// condition the attribution windows below depend on).
var ErrRunInFlight = errors.New("runner: a run is already in flight")

// ErrNoWatch is returned by StopWatch when there is no watch session open —
// either none was ever started, or the watch has already self-finalized
// (ctx cancellation: the finalize path nils the watch slot
// itself, so a Stop racing or following that finalize sees this, never a
// stale buffered Result).
var ErrNoWatch = errors.New("kit/runner: no watch in progress")

// watchLane/watchUC are the identity StartWatch stamps its window with:
// "external" is a WATCH-ONLY UC — validateRow rejects it from
// Run/Start so a client can never POST a run claiming to be one (only
// StartWatch, which holds the watch's full lifecycle, may mint it).
const watchLane, watchUC = "conformant", "external"

// auditUnavailableDetail is the audit.unavailable event's Detail — shared by
// execute's and StartWatch/finishWatch's audit brackets so an unconfigured
// Audit Plane reads identically for a run and a watch.
const auditUnavailableDetail = "no readable Audit Plane configured (hosted reads are internal-only)"

// drainTimeout bounds how long execute waits for the observer relay to catch
// up (relay.Relay.Drain) before giving up and surfacing the
// incompleteness honestly rather than failing the run.
const drainTimeout = 5 * time.Second

// Sink receives each run or watch's outcome synchronously after its terminal
// event, while admission is still held and before Results publishes it. The
// kit/runhistory Recorder implements this to capture the completed story.
// Capture is best-effort: a returned callback or recovered panic completes
// the attempt, without promising durable storage or changing the row outcome.
// The runner package sees only this interface; it never imports runhistory
// (import direction is one-way: runhistory → runner + event).
type Sink interface{ RunCompleted(res Result) }

// Config wires a Runner to the pieces it brackets a row with.
type Config struct {
	Driver *scenariodriver.Driver // the child's transport core (required)
	// ProviderDataDriver is the Plain-EHR lane's child: the SECOND gateway
	// child's /scenario/* base (kitd.Stack.ProviderDataDriver) running the
	// provider-data origination profile. nil ⇒ the lane is refused
	// (bridge-refuse excepted — that row originates on Driver, the main
	// child) with ehrLaneUnavailable before any run is created.
	ProviderDataDriver *scenariodriver.Driver
	Bus                *event.Bus             // the Kit run-timeline bus (required)
	Relay              relay.Stamper          // nil ok — unit tests without a live observer; a relay.Multi when several children emit
	AuditURL           string                 // "" ⇒ merge skipped, one audit.unavailable event per run
	HTTP               *http.Client           // nil → http.DefaultClient
	Now                func() time.Time       // nil → time.Now
	NewRunID           func() string          // nil → monotonic "run-N"
	UC07PCI            func() (string, error) // patient-surface PCI resolver
	// PatientSurfaceReadable reports whether the hosted patient-surface reads
	// (/personas, /authorizations) are reachable by this (machine) client. shnkitd
	// sets it from a boot-time probe: in the HOSTED topology the discovery-advertised
	// phg endpoint is the machine /notify edge only — the patient-surface reads are
	// internal/patient-only (Cognito-gated at app.<apex>), so /personas is not routed
	// (404 "no route"). When false, UC-07's patient-surface read-back is SKIPPED
	// gracefully (the same "hosted reads are internal-only" principle as AuditURL=="";
	// the PA itself still succeeds and asserts). This is a REACHABILITY GATE, not a
	// removal: a future conformant read-back path (PDex Patient Access, reachable by a
	// machine) simply won't degrade — nothing here forecloses it.
	PatientSurfaceReadable bool
	History                Sink // nil ok — no run-history capture without one
	// BFFURL is the Java trio's br-provider base (kitd.Stack.BRProviderURL) —
	// "" when no trio is configured. When set, the conformant lane's CRD
	// prong originates through br-provider's real BFF
	// (scenariodriver.OriginateThroughBRProvider) instead of the driver's
	// own direct-mint PostCRD (rows_conformant.go).
	BFFURL string
	// Dispatch is shared by all owned gateway/BFF driver clients for the daemon lifetime.
	Dispatch *DispatchObserver
}

// Result is one completed run's outcome, as returned by Run and accumulated
// in Results().
type Result struct {
	RunID  string `json:"runId"`
	Lane   string `json:"lane"`
	UC     string `json:"uc"`
	Branch string `json:"branch"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// rowFunc drives one lane's UC — branch is "" for UCs that take none. It
// returns a one-sentence human detail on success, or an error whose message
// becomes Result.Detail on failure.
type rowFunc func(rn *Runner, branch string) (detail string, err error)

// Req is one row's parameters, replacing the former bare (lane, uc, branch)
// triple: Member is populated ONLY for the
// "freeform" UC (a caller-named member dispatched against their own
// provider data, no answer book) and rejected for every other UC
// (validateRow) — see rows_ehr.go's ehrFreeform.
type Req struct {
	Lane   string
	UC     string
	Branch string
	Member string
}

// watch is one open external-activity attribution window (StartWatch /
// StopWatch). once guards ONLY the stop-channel close (StopWatch
// may be called more than once, or race a ctx-driven self-finalize); done
// buffers exactly the one Result finishWatch ever produces for this window.
type watch struct {
	runID       string
	stop        chan struct{}
	done        chan Result
	once        sync.Once
	mergeAudit  bool
	preHW       int
	observation *observationWindow
}

// Runner drives scenario rows sequentially (v1: at most one Run/Start at a
// time — TryLock, never queued) and accumulates their Results. Zero value is
// not usable; construct with New.
type Runner struct {
	cfg Config

	// baseCtx is the Runner-LIFETIME context async (Start-spawned) runs
	// execute under. Start's goroutine must NOT inherit the caller's ctx:
	// the daemon handler calls Start(r.Context(), ...) and returns
	// immediately, and net/http cancels that ctx as the handler returns —
	// which would fail every async run's (load-bearing) audit fetch with
	// "context canceled". A Close/shutdown lifetime ctx is future
	// territory; until then this is simply context.Background().
	baseCtx context.Context

	mu sync.Mutex // held for the duration of exactly one row's execution OR one watch session

	resMu   sync.Mutex
	results []Result

	idSeq atomic.Uint64

	// watchMu guards watch — the currently-open watch session (nil when
	// none). Separate from mu: mu is held for the ENTIRE watch lifetime (the
	// sequential-lock invariant), while watchMu is a short-hold accessor lock
	// so StopWatch (running on the caller's goroutine) and the finalize
	// goroutine (running on its own) can safely hand off the same *watch
	// without contending on mu itself.
	watchMu sync.Mutex
	watch   *watch

	// inFlight mirrors mu's hold, set true the instant a TryLock succeeds
	// (Run/Start/StartWatch — a run OR a watch) and cleared at every one of
	// mu's release points (completeLocked, and
	// StartWatch's own early-unlock on a pre-fetch failure). It is a plain
	// atomic flag, NOT a TryLock probe: InFlight()
	// is read by kitd's restart handler as a best-effort admission gate, and
	// a TryLock-based probe would itself momentarily steal the sequential
	// lock out from under a real run — an atomic read never
	// contends with mu at all.
	inFlight atomic.Bool
}

// New constructs a Runner, defaulting HTTP/Now/NewRunID when unset.
func New(cfg Config) *Runner {
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Runner{cfg: cfg, baseCtx: context.Background()}
	if r.cfg.NewRunID == nil {
		r.cfg.NewRunID = r.nextRunID
	}
	return r
}

// nextRunID is the default NewRunID: collision-resistant under normal
// restart latency across daemon restarts (run history keys on it)
// — clock-prefixed off the injected Now, per-process counter suffix for
// same-millisecond runs. Not a formal uniqueness guarantee: a restart inside
// the same millisecond as the prior process's last-issued id (the counter
// resets to 0 on restart) could in theory collide — a window under 1ms,
// never observed in practice, and additive to close later if it matters
// (e.g. a persisted counter or a restart epoch component).
func (r *Runner) nextRunID() string {
	return fmt.Sprintf("run-%d-%d", r.cfg.Now().UnixMilli(), r.idSeq.Add(1))
}

// now returns the Runner's injected clock (house clock rule).
func (r *Runner) now() time.Time { return r.cfg.Now() }

// InFlight reports whether a run or watch session currently holds the
// sequential lock. It is a plain atomic read (kit/kitd's restart handler
// consumes it as a best-effort admission gate) — never a TryLock
// probe, which would itself momentarily steal the lock out from under a
// real run. Because it is read-then-act rather than
// lock-then-act, a run/watch can start in the window between an InFlight()
// read and whatever the caller does next; callers that need that window
// closed must document the residual, not paper over it.
func (r *Runner) InFlight() bool { return r.inFlight.Load() }

// Run validates req, acquires the sequential lock (ErrRunInFlight if busy —
// including a watch session holding it), and blocks through history capture
// and admission release before returning its Result. The caller's ctx governs
// the run's HTTP work (Run blocks for its duration, so caller cancellation is
// meaningful here — unlike Start).
func (r *Runner) Run(ctx context.Context, req Req) (Result, error) {
	row, err := r.validate(req)
	if err != nil {
		return Result{}, err
	}
	if !r.mu.TryLock() {
		return Result{}, ErrRunInFlight
	}
	r.inFlight.Store(true)
	return r.runLocked(ctx, r.cfg.NewRunID(), req.Lane, req.UC, req.Branch, row), nil
}

// Start validates req, acquires the sequential lock (ErrRunInFlight if
// busy — including a watch session holding it), and spawns the row in a
// goroutine, returning the pre-allocated run id immediately (the daemon's
// contract: a caller polls Results() for finalized completion; the event bus
// reports the outcome before history capture and admission release). Run and
// Start delegate to the same runLocked internals — Run is simply Start's
// blocking form.
//
// The caller's ctx is used ONLY for the synchronous validate/lock phase; the
// spawned run executes under r.baseCtx (see the field doc: the caller's ctx
// is typically an HTTP request context that dies as the handler returns).
func (r *Runner) Start(_ context.Context, req Req) (string, error) {
	row, err := r.validate(req)
	if err != nil {
		return "", err
	}
	if !r.mu.TryLock() {
		return "", ErrRunInFlight
	}
	r.inFlight.Store(true)
	runID := r.cfg.NewRunID()
	go r.runLocked(r.baseCtx, runID, req.Lane, req.UC, req.Branch, row)
	return runID, nil
}

// StartWatch opens an external-activity attribution window: it
// holds the SAME sequential lock as Run/Start — the same
// soundness condition — stamps the relay, and parks until StopWatch or ctx
// cancellation. Frames from a partner-originated flow relay stamped and
// become a normal run on the bus: inspector, history, and export need no
// special cases.
//
// ctx is the watch's LIFETIME, not a request scope: kitd hands
// it d.baseCtx. A request-scoped ctx would finalize the watch the moment the
// POST response that opened it is written.
//
// The audit pre-fetch (when Config.AuditURL is set) is load-bearing exactly
// as in a normal run's execute, but ordered DIFFERENTLY: here
// it runs BEFORE the stamp/run.started emit (execute's row runs first and
// only fails visibly via run.failed) so that a pre-fetch failure leaves
// NOTHING emitted or stamped — the lock is simply released and the error
// returned, exactly like a normal Run/Start validation failure.
func (r *Runner) StartWatch(ctx context.Context) (string, error) {
	if !r.mu.TryLock() {
		return "", ErrRunInFlight
	}
	r.inFlight.Store(true)
	mergeAudit := r.cfg.AuditURL != ""
	var preHW int
	if mergeAudit {
		var err error
		preHW, err = r.auditPreFetch(ctx)
		if err != nil {
			r.inFlight.Store(false)
			r.mu.Unlock()
			return "", err
		}
	}

	runID := r.nextRunID()
	w := &watch{runID: runID, stop: make(chan struct{}), done: make(chan Result, 1), mergeAudit: mergeAudit, preHW: preHW}
	r.watchMu.Lock()
	r.watch = w
	r.watchMu.Unlock()

	w.observation = r.beginObservation(ctx, relay.Stamp{RunID: runID, Lane: watchLane, UC: watchUC}, "")
	if !mergeAudit {
		r.cfg.Bus.Emit(event.Event{
			Type: event.TypeAuditUnavailable, RunID: runID, Lane: watchLane, UC: watchUC,
			Detail: auditUnavailableDetail,
		})
	}

	go func() {
		canceled := false
		select {
		case <-w.stop:
		case <-ctx.Done():
			canceled = true
		}
		// On the cancel path the lifetime ctx is already dead — the tail
		// (drain + audit post-fetch) runs under a FRESH short-timeout ctx,
		// deliberately: shutdown closes the record as completely
		// as the drain bound allows rather than instantly degrading it.
		tctx := ctx
		if canceled {
			var cancel context.CancelFunc
			tctx, cancel = context.WithTimeout(context.Background(), drainTimeout+time.Second)
			defer cancel()
		}
		res := r.finishWatch(tctx, w)
		w.done <- res
		close(w.done)
	}()
	return runID, nil
}

// StopWatch closes the window opened by StartWatch and returns its Result
// after history capture and admission release.
// The finalize path (finishWatch, run either from StopWatch's own close of
// w.stop or from a ctx-cancel self-finalize) nils r.watch itself,
// so a Stop after that finalize — or a second Stop — answers
// ErrNoWatch, never a stale buffered Result.
//
// Two concurrent StopWatch callers can both read the same non-nil r.watch
// before either sees the finalize goroutine's nil:
// both proceed past the nil check and both block on <-w.done. The finalize
// goroutine sends its ONE Result then closes w.done, so exactly one of the
// two racing receives gets (res, true) and the other — reading from a
// closed, now-empty channel — immediately gets the zero Result with ok
// false. That second caller answers ErrNoWatch (there is, by the time it
// would report a Result, no longer a watch to report one for) instead of
// blocking on a done channel nobody will ever send to again.
func (r *Runner) StopWatch() (Result, error) {
	r.watchMu.Lock()
	w := r.watch
	r.watchMu.Unlock()
	if w == nil {
		return Result{}, ErrNoWatch
	}
	w.once.Do(func() { close(w.stop) })
	res, ok := <-w.done // finishWatch buffers exactly one Result, then closes
	if !ok {
		return Result{}, ErrNoWatch
	}
	return res, nil
}

// finishWatch closes observation before history capture and admission release.
func (r *Runner) finishWatch(tctx context.Context, w *watch) (res Result) {
	var clinicalErr error
	defer func() {
		if p := recover(); p != nil {
			clinicalErr = fmt.Errorf("finishWatch panicked: %v", p)
		}
		res = w.observation.end("external activity window closed", clinicalErr, false)
		r.watchMu.Lock()
		r.watch = nil
		r.watchMu.Unlock()
		r.completeLocked(res)
	}()
	w.observation.drain(tctx)
	clinicalErr = r.auditPostFetchAndEmit(tctx, w.runID, watchLane, watchUC, w.mergeAudit, w.preHW, nil)
	return
}

// runLocked holds admission through preparation, terminal publication and history.
func (r *Runner) runLocked(ctx context.Context, runID, lane, uc, branch string, row rowFunc) (res Result) {
	w := r.beginObservation(ctx, relay.Stamp{RunID: runID, Lane: lane, UC: uc}, branch)
	var detail string
	var clinicalErr error
	mergeAudit := r.cfg.AuditURL != ""
	preHW := 0
	auditReady := false
	defer func() {
		panicked := false
		if p := recover(); p != nil {
			clinicalErr = fmt.Errorf("row panicked: %v", p)
			panicked = true
		}
		w.drain(ctx)
		func() {
			defer func() {
				if p := recover(); p != nil {
					clinicalErr = errors.Join(clinicalErr, fmt.Errorf("audit panicked: %v", p))
				}
			}()
			if auditReady {
				clinicalErr = r.auditPostFetchAndEmit(ctx, runID, lane, uc, mergeAudit, preHW, clinicalErr)
			}
		}()
		// An observed fully completed response (including refusal) is safe. A
		// panic without transport completion evidence cannot prove dispatch ended.
		uncertain := panicked && r.cfg.Dispatch == nil
		res = w.end(detail, clinicalErr, uncertain)
		r.completeLocked(res)
	}()
	if mergeAudit {
		preHW, clinicalErr = r.auditPreFetch(ctx)
		if clinicalErr != nil {
			return
		}
	} else {
		r.cfg.Bus.Emit(event.Event{Type: event.TypeAuditUnavailable, RunID: runID, Lane: lane, UC: uc, Detail: auditUnavailableDetail})
	}
	auditReady = true
	detail, clinicalErr = row(r, branch)
	return
}

// Results returns finalized run and watch outcomes, oldest first. Each entry
// is visible only after its history capture attempt and admission release.
// Observing an entry does not reserve admission: another caller may start a
// new run or watch before the reader does.
func (r *Runner) Results() []Result {
	r.resMu.Lock()
	defer r.resMu.Unlock()
	out := make([]Result, len(r.results))
	copy(out, r.results)
	return out
}

// completeLocked finishes a run or watch while mu is held. History may read
// Results, so capture runs outside resMu. Keeping resMu held until mu is
// released makes result visibility the finalization boundary and preserves
// result order even when the next run starts immediately.
func (r *Runner) completeLocked(res Result) {
	if r.cfg.History != nil {
		func() {
			// A sink panic must not change the outcome or strand admission.
			defer func() { _ = recover() }()
			r.cfg.History.RunCompleted(res)
		}()
	}
	r.resMu.Lock()
	r.results = append(r.results, res)
	r.inFlight.Store(false)
	r.mu.Unlock()
	r.resMu.Unlock()
}

// auditPreFetch performs the load-bearing audit-merge pre-fetch: fetch the
// Audit Plane's current content and return its high-water mark, or a
// wrapped error on failure. Callers must have already checked
// Config.AuditURL != "". Regular rows report pre-fetch failure inside their
// already-started window; StartWatch refuses before publishing any lifecycle.
func (r *Runner) auditPreFetch(ctx context.Context) (int, error) {
	pre, err := auditread.Fetch(ctx, r.cfg.HTTP, r.cfg.AuditURL)
	if err != nil {
		return 0, fmt.Errorf("audit read failed: %w", err)
	}
	return auditread.HighWater(pre), nil
}

// auditPostFetchAndEmit performs the audit-merge post-fetch (skipped
// entirely when !mergeAudit, e.g. an unconfigured Audit Plane) and emits one
// TypeAudit event per record newer than preHW — the second half of the
// audit bracket, shared by runLocked and finishWatch so a run
// and a watch merge audit events identically. priorErr (the row/window's own
// outcome so far, possibly nil) is folded into the returned error via
// errors.Join on a post-fetch failure, so a post-fetch failure never
// silently discards an already-failed row/window; when the post-fetch
// succeeds (or merge is off), priorErr passes through unchanged.
func (r *Runner) auditPostFetchAndEmit(ctx context.Context, runID, lane, uc string, mergeAudit bool, preHW int, priorErr error) error {
	if !mergeAudit {
		return priorErr
	}
	post, err := auditread.Fetch(ctx, r.cfg.HTTP, r.cfg.AuditURL)
	if err != nil {
		return errors.Join(priorErr, fmt.Errorf("audit read failed: %w", err))
	}
	for _, rec := range auditread.After(post, preHW) {
		b, err := json.Marshal(rec)
		if err != nil {
			continue // a Record's fields are all plain strings/ints — cannot fail in practice
		}
		r.cfg.Bus.Emit(event.Event{Type: event.TypeAudit, RunID: runID, Lane: lane, UC: uc, Audit: b})
	}
	return priorErr
}

// validate is Run/Start's shared pre-flight: the lane must be AVAILABLE on
// this Kit (the Plain EHR lane needs its own gateway child, i.e. the Java
// trio — refused with ehrLaneUnavailable, no run created), then the row shape
// (validateRow). The uc03 bridge-refuse row is the one exception: it
// originates on the MAIN child, so it stays available on a no-trio Kit.
func (r *Runner) validate(req Req) (rowFunc, error) {
	bridgeRefuse := req.UC == "uc03" && req.Branch == "bridge-refuse"
	if req.Lane == "ehr" && r.cfg.ProviderDataDriver == nil && !bridgeRefuse {
		return nil, fmt.Errorf("runner: %s", ehrLaneUnavailable)
	}
	return validateRow(req)
}

// validateRow checks req against this package's row shape (package doc;
// branches: uc01 covered|notcovered (both lanes); uc05 lane "conformant"
// takes no branch, lane "ehr" takes ""|consent|noconsent; uc07 takes no
// branch on either lane; uc03 lane "conformant" takes ""|bridge-demo, lane
// "ehr" takes ""|bridge-refuse (the two bridge personas are lane-exclusive);
// every other UC takes no branch) and returns the row func to execute. An
// unknown lane/UC/branch is an error and the run is never created (no lock
// taken, no bus events emitted) — the "unknown row" contract.
//
// uc "freeform" is NOT a table row — it is a
// closure built here over req.Member (rows_ehr.go's ehrFreeform), requiring
// lane "ehr", no branch, and a non-empty Member. Every OTHER uc rejects a
// non-empty Member (it is freeform-only). uc "external" is rejected outright
// for ANY lane: external is minted by StartWatch only — a
// POSTable "external" would let a client stamp arbitrary partner-originated
// traffic as a run without ever holding the watch session's lifecycle (the
// sequential lock + its finalize tail) StartWatch/StopWatch provide.
func validateRow(req Req) (rowFunc, error) {
	lane, uc, branch, member := req.Lane, req.UC, req.Branch, req.Member

	if uc == watchUC {
		return nil, fmt.Errorf("runner: uc %q is watch-only (minted by StartWatch, never Run/Start)", uc)
	}

	if uc == "freeform" {
		if lane != "ehr" {
			return nil, fmt.Errorf("runner: freeform requires lane \"ehr\", got %q", lane)
		}
		if branch != "" {
			return nil, fmt.Errorf("runner: freeform takes no branch, got %q", branch)
		}
		if strings.TrimSpace(member) == "" {
			return nil, fmt.Errorf("runner: freeform: member is required")
		}
		return func(rn *Runner, _ string) (string, error) { return ehrFreeform(rn, member) }, nil
	}
	if member != "" {
		return nil, fmt.Errorf("runner: member is only valid for freeform, got uc %q", uc)
	}

	var table map[string]rowFunc
	switch lane {
	case "ehr":
		table = ehrRows
	case "conformant":
		table = conformantRows
	default:
		return nil, fmt.Errorf("runner: unknown lane %q (want ehr|conformant)", lane)
	}
	row, ok := table[uc]
	if !ok {
		return nil, fmt.Errorf("runner: unknown UC %q for lane %q", uc, lane)
	}
	switch uc {
	case "uc01":
		if branch != "covered" && branch != "notcovered" {
			return nil, fmt.Errorf("runner: uc01 branch must be covered|notcovered, got %q", branch)
		}
	case "uc05":
		if lane == "conformant" {
			if branch != "" {
				return nil, fmt.Errorf("runner: conformant uc05 takes no branch, got %q", branch)
			}
		} else if branch != "" && branch != "consent" && branch != "noconsent" { // lane "ehr"
			return nil, fmt.Errorf("runner: uc05 branch must be \"\"|consent|noconsent, got %q", branch)
		}
	case "uc07":
		// Neither lane branches on uc07: the conformant row IS the HCPCS
		// persona (with the patient-surface read-back), and the Plain-EHR
		// row is the chart-driven patient-attestation flow.
		if branch != "" {
			return nil, fmt.Errorf("runner: %s uc07 takes no branch, got %q", lane, branch)
		}
	case "uc03":
		// The two bridge personas are lane-exclusive: the bridge-demo
		// persona is conformant-lane-only (its PAS leg rides the ingress
		// fallback, a route refusal, a different species than the gated
		// refusal); the bridge-refuse persona is ehr-lane-only (its
		// promoted runCRDThenDTROrder sites fire the gated chain + the typed
		// refusal). Neither lane takes the other's branch.
		if lane == "conformant" {
			if branch != "" && branch != "bridge-demo" {
				return nil, fmt.Errorf("runner: conformant uc03 branch must be \"\"|bridge-demo, got %q", branch)
			}
		} else if branch != "" && branch != "bridge-refuse" {
			return nil, fmt.Errorf("runner: ehr uc03 branch must be \"\"|bridge-refuse, got %q", branch)
		}
	default:
		if branch != "" {
			return nil, fmt.Errorf("runner: uc %q takes no branch, got %q", uc, branch)
		}
	}
	return row, nil
}

// excerpt truncates b to a short diagnostic prefix for Result.Detail /
// wrapped errors — never the full body (which may be a multi-KB FHIR Bundle).
func excerpt(b []byte) string {
	const max = 200
	s := string(b)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
