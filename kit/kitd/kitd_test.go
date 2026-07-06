// kitd_test.go — hermetic tests for the Kit daemon. White-box (package
// kitd) so tests can read the generated
// token/addr directly, mirroring kit/runner's test style.
package kitd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/bootstrap"
	"github.com/SmartHealthNetwork/shn-kit/byo"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/relay"
	"github.com/SmartHealthNetwork/shn-kit/runhistory"
	"github.com/SmartHealthNetwork/shn-kit/runner"
	"github.com/SmartHealthNetwork/shn-kit/supervisor"
	"github.com/SmartHealthNetwork/shn-kit/update"
)

func fixedClock() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }

// startDaemon constructs and Serves a Daemon in a goroutine, waits for
// Ready() (failing the test after 5s), and registers cleanup that cancels
// the Serve context and asserts Serve returned cleanly.
func startDaemon(t *testing.T, cfg Config) (*Daemon, string) {
	t.Helper()
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = d.Serve(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if serveErr != nil {
			t.Errorf("Serve: %v", serveErr)
		}
	})
	select {
	case <-d.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not become ready within 5s")
	}
	return d, "http://" + d.Addr()
}

// doJSON issues an HTTP request with an optional bearer token and JSON
// body, returning the status code and raw response body.
func doJSON(t *testing.T, method, url, token string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, b
}

// readSSE GETs url, scans "data: " lines off the response body, and
// unmarshals each into an event.Event, returning after n events or a 5s
// deadline (mirrors kit/event's and kit/runner's helper of the same name).
func readSSE(t *testing.T, url string, n int) []event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	var out []event.Event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() && len(out) < n {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e event.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
			t.Fatalf("unmarshal SSE data %q: %v", line, err)
		}
		out = append(out, e)
	}
	if len(out) != n {
		t.Fatalf("read %d SSE events, want %d: %+v", len(out), n, out)
	}
	return out
}

// ---- Row 1: loopback fail-closed -------------------------------------------

func TestNew_LoopbackFailClosed(t *testing.T) {
	bus := event.NewBus(fixedClock)
	baseCfg := Config{StateDir: t.TempDir(), Bus: bus, Sup: supervisor.New(nil), Runner: runner.New(runner.Config{
		Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus,
	})}

	bad := []string{
		"0.0.0.0:0",        // all-interfaces
		":0",               // bare port — SplitHostPort yields host "", must NOT be special-cased to loopback
		"example.com:0",    // non-loopback host
		"192.168.1.5:0",    // LAN address
		"not-a-valid-addr", // fails SplitHostPort entirely
	}
	for _, addr := range bad {
		t.Run(addr, func(t *testing.T) {
			cfg := baseCfg
			cfg.APIAddr = addr
			if _, err := New(cfg); err == nil {
				t.Fatalf("New(APIAddr:%q): want error, got nil", addr)
			}
		})
	}

	good := []string{"127.0.0.1:0", "localhost:0", "[::1]:0"}
	for _, addr := range good {
		t.Run(addr, func(t *testing.T) {
			cfg := baseCfg
			cfg.APIAddr = addr
			if _, err := New(cfg); err != nil {
				t.Fatalf("New(APIAddr:%q): unexpected error: %v", addr, err)
			}
		})
	}
}

// ---- Row 2: session handshake ----------------------------------------------

func TestServe_SessionHandshake(t *testing.T) {
	bus := event.NewBus(fixedClock)
	stateDir := t.TempDir()
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: stateDir,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Runner:   runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
	}
	d, apiBase := startDaemon(t, cfg)

	sessionPath := filepath.Join(stateDir, "session.json")
	fi, err := os.Stat(sessionPath)
	if err != nil {
		t.Fatalf("stat session.json: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Fatalf("session.json perms = %v, want 0600", perm)
	}

	raw, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session.json: %v", err)
	}
	var session sessionFile
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatalf("unmarshal session.json: %v", err)
	}
	if session.API != apiBase {
		t.Fatalf("session.json api = %q, want %q", session.API, apiBase)
	}
	if session.Token == "" || session.Token != d.token {
		t.Fatalf("session.json token = %q, want the daemon's generated token %q", session.Token, d.token)
	}
	if len(session.Token) != 32 { // 16 bytes crypto/rand hex-encoded
		t.Fatalf("session.json token length = %d, want 32 (16-byte hex)", len(session.Token))
	}

	// /health is ungated: no token required.
	status, body := doJSON(t, http.MethodGet, apiBase+"/health", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200 (body=%s)", status, body)
	}
	var health struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &health); err != nil || !health.OK {
		t.Fatalf("GET /health body = %s, want {\"ok\":true}", body)
	}
}

// ---- Row 3: token gate -------------------------------------------------------

func TestTokenGate(t *testing.T) {
	const token = "row3-fixed-token-for-determinism"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil), // real Supervisor, no children — Status() must be []
		Runner:   runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
	}
	_, apiBase := startDaemon(t, cfg)

	// No token at all.
	if status, _ := doJSON(t, http.MethodGet, apiBase+"/api/status", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /api/status without token = %d, want 401", status)
	}
	// Wrong token.
	if status, _ := doJSON(t, http.MethodGet, apiBase+"/api/status", "wrong-token", nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /api/status with wrong token = %d, want 401", status)
	}
	// Correct token via Authorization header.
	status, body := doJSON(t, http.MethodGet, apiBase+"/api/status", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/status with valid Bearer token = %d, want 200 (body=%s)", status, body)
	}
	var statusBody struct {
		Children []supervisor.ChildStatus `json:"children"`
	}
	if err := json.Unmarshal(body, &statusBody); err != nil {
		t.Fatalf("unmarshal /api/status body: %v", err)
	}
	if statusBody.Children == nil || len(statusBody.Children) != 0 {
		t.Fatalf("/api/status children = %+v, want a non-nil empty slice", statusBody.Children)
	}
	if !strings.Contains(string(body), `"children":[]`) {
		t.Fatalf("/api/status body = %s, want a JSON array (not null) for children", body)
	}

	// Correct token via query param (EventSource can't set headers).
	status, body = doJSON(t, http.MethodGet, apiBase+"/api/status?token="+token, "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/status?token=... = %d, want 200 (body=%s)", status, body)
	}

	// /events wrong token → 401, no stream.
	if status, _ := doJSON(t, http.MethodGet, apiBase+"/events?token=wrong-token", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /events with wrong token = %d, want 401", status)
	}

	// /events streams: emit before connecting so it lands in the replay
	// buffer (Bus.subscribe replays everything with seq > 0 for a fresh
	// subscriber), avoiding a live-delivery race.
	bus.Emit(event.Event{Type: event.TypeChild, Child: "gateway", Detail: "ready"})
	events := readSSE(t, apiBase+"/events?token="+token, 1)
	if events[0].Type != event.TypeChild || events[0].Child != "gateway" {
		t.Fatalf("streamed event = %+v, want the emitted child event", events[0])
	}
}

// ---- Fix round 1: shutdown with a live SSE subscriber ------------------------

// TestServe_ShutdownWithLiveSSE: Serve(ctx) must return promptly after ctx
// is canceled even while an /events SSE subscriber is connected — the
// daemon's NORMAL steady state once a UI attaches. RED against a Serve
// whose srv.Shutdown(context.Background()) waits forever for the SSE
// connection to go idle (Shutdown never interrupts active connections, and
// the bus's handleEvents loop only exits when the REQUEST context is
// canceled — which Shutdown alone never does).
func TestServe_ShutdownWithLiveSSE(t *testing.T) {
	const token = "shutdown-sse-fixed-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Runner:   runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()
	select {
	case <-d.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not become ready within 5s")
	}
	apiBase := "http://" + d.Addr()

	// Open a REAL streaming SSE connection and prove it is live by reading
	// one emitted event off it. The connection stays open (body not closed)
	// across the daemon-ctx cancel below.
	req, err := http.NewRequest(http.MethodGet, apiBase+"/events?token="+token, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events = %d, want 200", resp.StatusCode)
	}
	bus.Emit(event.Event{Type: event.TypeChild, Child: "gateway", Detail: "ready"})
	sc := bufio.NewScanner(resp.Body)
	gotEvent := false
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			gotEvent = true
			break
		}
	}
	if !gotEvent {
		t.Fatalf("SSE stream not live: never received a data line (scan err: %v)", sc.Err())
	}

	// Cancel the daemon ctx with the SSE subscriber still connected: Serve
	// must return within a bounded window (RED evidence bounds the hang —
	// select vs time.After, never a stuck test).
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return within 10s of ctx cancel while an SSE subscriber was connected")
	}
}

// ---- Row 4: run dispatch -----------------------------------------------------

func TestRunDispatch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	gwSrv := httptest.NewServer(mux)
	defer gwSrv.Close()

	const token = "row4-fixed-token-for-determinism"
	bus := event.NewBus(fixedClock)
	rn := runner.New(runner.Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: gwSrv.URL}),
		Bus:    bus,
	})
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Runner:   rn,
	}
	_, apiBase := startDaemon(t, cfg)

	// Unknown row → 400, synchronously, no run created.
	status, body := doJSON(t, http.MethodPost, apiBase+"/api/runs", token,
		map[string]string{"lane": "ehr", "uc": "uc99", "branch": ""})
	if status != http.StatusBadRequest {
		t.Fatalf("POST /api/runs (unknown uc) = %d, want 400 (body=%s)", status, body)
	}

	// Dispatch a real row: 202 with a runId, running async.
	status, body = doJSON(t, http.MethodPost, apiBase+"/api/runs", token,
		map[string]string{"lane": "ehr", "uc": "uc01", "branch": "covered"})
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/runs = %d, want 202 (body=%s)", status, body)
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || accepted.RunID == "" {
		t.Fatalf("POST /api/runs body = %s, want {\"runId\":\"...\"}", body)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fake gateway never received the /scenario/uc01 request")
	}

	// A second run while the first is blocked in-flight → 409.
	status, body = doJSON(t, http.MethodPost, apiBase+"/api/runs", token,
		map[string]string{"lane": "ehr", "uc": "uc01", "branch": "notcovered"})
	if status != http.StatusConflict {
		t.Fatalf("POST /api/runs (busy) = %d, want 409 (body=%s)", status, body)
	}
	var conflictBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &conflictBody)
	if conflictBody.Error != "run in flight" {
		t.Fatalf("409 body = %s, want {\"error\":\"run in flight\"}", body)
	}

	close(release)

	// Poll GET /api/runs until the run shows passed.
	deadline := time.Now().Add(5 * time.Second)
	var results []runner.Result
	for time.Now().Before(deadline) {
		status, body = doJSON(t, http.MethodGet, apiBase+"/api/runs", token, nil)
		if status != http.StatusOK {
			t.Fatalf("GET /api/runs = %d, want 200 (body=%s)", status, body)
		}
		if err := json.Unmarshal(body, &results); err != nil {
			t.Fatalf("unmarshal /api/runs body: %v", err)
		}
		if len(results) == 1 && results[0].State == runner.StatePassed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(results) != 1 {
		t.Fatalf("GET /api/runs = %+v, want exactly 1 result", results)
	}
	if results[0].RunID != accepted.RunID || results[0].State != runner.StatePassed {
		t.Fatalf("GET /api/runs result = %+v, want RunID %q State %q", results[0], accepted.RunID, runner.StatePassed)
	}
}

// ---- Bootstrap API routes + daemon-first run gating ------------------------

// ---- Row 1: run routes 503 before SetRunner, 202/200 after ----------------

// TestRunRoutes_DaemonFirstGating proves kitd is reachable (and its
// /api/bootstrap routes usable) BEFORE the substrate stack — and hence a
// Runner — exists: Config.Runner is nil at construction, /api/runs 503s
// until main calls SetRunner once the gateway child is ready. It
// also proves the mirror-image gate: with no Boot configured (nil), the
// Config embedding this test otherwise uses still works —
// /api/bootstrap 404s instead of panicking on a nil Machine.
func TestRunRoutes_DaemonFirstGating(t *testing.T) {
	const token = "row1-daemon-first-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		// Runner and Boot both intentionally left nil.
	}
	d, apiBase := startDaemon(t, cfg)

	if status, _ := doJSON(t, http.MethodGet, apiBase+"/api/bootstrap", token, nil); status != http.StatusNotFound {
		t.Fatalf("GET /api/bootstrap with no Boot configured = %d, want 404", status)
	}

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/runs", token, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/runs before SetRunner = %d, want 503 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error != "stack not started" {
		t.Fatalf("503 body = %s, want {\"error\":\"stack not started\"}", body)
	}

	status, body = doJSON(t, http.MethodPost, apiBase+"/api/runs", token,
		map[string]string{"lane": "ehr", "uc": "uc01", "branch": "covered"})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/runs before SetRunner = %d, want 503 (body=%s)", status, body)
	}

	// Wire in a real runner (mirrors row 4's fixture) and confirm the gate lifts.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	gwSrv := httptest.NewServer(mux)
	defer gwSrv.Close()

	rn := runner.New(runner.Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: gwSrv.URL}),
		Bus:    bus,
	})
	d.SetRunner(rn)

	status, body = doJSON(t, http.MethodPost, apiBase+"/api/runs", token,
		map[string]string{"lane": "ehr", "uc": "uc01", "branch": "covered"})
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/runs after SetRunner = %d, want 202 (body=%s)", status, body)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/runs", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/runs after SetRunner = %d, want 200 (body=%s)", status, body)
	}
}

// ---- Row 2: GET /api/bootstrap auth + shape --------------------------------

func TestBootstrapGet_Unauthed401_SigninRequired(t *testing.T) {
	const token = "row2-bootstrap-get-token"
	bus := event.NewBus(fixedClock)
	boot := bootstrap.New(bootstrap.Config{SecretsDir: filepath.Join(t.TempDir(), "secrets")})
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Boot:     boot,
	}
	_, apiBase := startDaemon(t, cfg)

	if status, _ := doJSON(t, http.MethodGet, apiBase+"/api/bootstrap", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /api/bootstrap unauthenticated = %d, want 401", status)
	}

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/bootstrap", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/bootstrap = %d, want 200 (body=%s)", status, body)
	}
	var resp struct {
		State  string            `json:"state"`
		Verify []bootstrap.Probe `json:"verify"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal /api/bootstrap body: %v", err)
	}
	if resp.State != string(bootstrap.StateSignInRequired) {
		t.Fatalf("state = %q, want %q", resp.State, bootstrap.StateSignInRequired)
	}
	if resp.Verify == nil || len(resp.Verify) != 0 {
		t.Fatalf("verify = %+v, want an empty (non-nil) slice", resp.Verify)
	}
	if !strings.Contains(string(body), `"verify":[]`) {
		t.Fatalf("body = %s, want a JSON array (not null) for verify", body)
	}
}

// ---- Rows 3-5: signin drives a real PKCE arc; already-provisioned → 409; --
// ---- reset returns to signin-required --------------------------------------

func TestBootstrapSignin_ArcThenConflictThenReset(t *testing.T) {
	const token = "row345-bootstrap-signin-token"
	bus := event.NewBus(fixedClock)

	cognito := newFakeCognito(t)
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec)

	boot := bootstrap.New(bootstrap.Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      filepath.Join(t.TempDir(), "secrets"),
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Bus:             bus,
		Ports:           []int{freePort(t)},
	})

	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Boot:     boot,
	}
	_, apiBase := startDaemon(t, cfg)

	// Row 3: POST /api/bootstrap/signin → 200 with a non-empty authorizeUrl.
	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bootstrap/signin", token, nil)
	if status != http.StatusOK {
		t.Fatalf("POST /api/bootstrap/signin = %d, want 200 (body=%s)", status, body)
	}
	var signinResp struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(body, &signinResp); err != nil || signinResp.AuthorizeURL == "" {
		t.Fatalf("signin body = %s, want a non-empty authorizeUrl", body)
	}

	// Driving that URL flips a later GET /api/bootstrap to provisioned.
	go func() { _, _ = http.Get(signinResp.AuthorizeURL) }()

	deadline := time.Now().Add(2 * time.Second)
	var lastState string
	for time.Now().Before(deadline) {
		_, body = doJSON(t, http.MethodGet, apiBase+"/api/bootstrap", token, nil)
		var resp struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(body, &resp)
		lastState = resp.State
		if lastState == string(bootstrap.StateProvisioned) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lastState != string(bootstrap.StateProvisioned) {
		t.Fatalf("GET /api/bootstrap state after driving the authorize URL (last=%q) never reached %q within 2s", lastState, bootstrap.StateProvisioned)
	}
	if rec.Creates() != 1 {
		t.Fatalf("accounts /clients creates = %d, want 1", rec.Creates())
	}

	// Row 4: signin when already provisioned → 409.
	status, body = doJSON(t, http.MethodPost, apiBase+"/api/bootstrap/signin", token, nil)
	if status != http.StatusConflict {
		t.Fatalf("POST /api/bootstrap/signin when provisioned = %d, want 409 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &errBody)
	if !strings.Contains(errBody.Error, "reset first") && !strings.Contains(errBody.Error, "already provisioned") {
		t.Fatalf("409 body = %s, want mentioning already provisioned / reset first", body)
	}

	// Row 5: reset → {"restartRequired":true}; a following GET /api/bootstrap
	// shows signin-required.
	status, body = doJSON(t, http.MethodPost, apiBase+"/api/bootstrap/reset", token, nil)
	if status != http.StatusOK {
		t.Fatalf("POST /api/bootstrap/reset = %d, want 200 (body=%s)", status, body)
	}
	var resetResp struct {
		RestartRequired bool `json:"restartRequired"`
	}
	if err := json.Unmarshal(body, &resetResp); err != nil || !resetResp.RestartRequired {
		t.Fatalf("reset body = %s, want {\"restartRequired\":true}", body)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/bootstrap", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/bootstrap after reset = %d, want 200 (body=%s)", status, body)
	}
	var afterReset struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(body, &afterReset)
	if afterReset.State != string(bootstrap.StateSignInRequired) {
		t.Fatalf("state after reset = %q, want %q", afterReset.State, bootstrap.StateSignInRequired)
	}
}

// ---- Row 6: SetVerify results appear in GET /api/bootstrap; patientAppUrl -
// ---- appears in GET /api/status when configured, absent otherwise --------

func TestBootstrapVerify_And_PatientAppURL(t *testing.T) {
	const token = "row6-verify-patient-token"
	bus := event.NewBus(fixedClock)
	boot := bootstrap.New(bootstrap.Config{SecretsDir: filepath.Join(t.TempDir(), "secrets")})

	cfg := Config{
		APIAddr:       "127.0.0.1:0",
		StateDir:      t.TempDir(),
		Token:         token,
		Bus:           bus,
		Sup:           supervisor.New(nil),
		Boot:          boot,
		PatientAppURL: "http://127.0.0.1:8084",
	}
	d, apiBase := startDaemon(t, cfg)

	probes := []bootstrap.Probe{
		{Name: "discovery", OK: true, Detail: "reachable"},
		{Name: "registration", OK: false, Detail: "holder not found"},
	}
	d.SetVerify(probes)

	_, body := doJSON(t, http.MethodGet, apiBase+"/api/bootstrap", token, nil)
	var resp struct {
		Verify []bootstrap.Probe `json:"verify"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal /api/bootstrap body: %v", err)
	}
	if len(resp.Verify) != 2 || resp.Verify[0] != probes[0] || resp.Verify[1] != probes[1] {
		t.Fatalf("verify = %+v, want %+v", resp.Verify, probes)
	}

	_, body = doJSON(t, http.MethodGet, apiBase+"/api/status", token, nil)
	var statusResp struct {
		PatientAppURL string `json:"patientAppUrl"`
	}
	if err := json.Unmarshal(body, &statusResp); err != nil {
		t.Fatalf("unmarshal /api/status body: %v", err)
	}
	if statusResp.PatientAppURL != "http://127.0.0.1:8084" {
		t.Fatalf("patientAppUrl = %q, want http://127.0.0.1:8084", statusResp.PatientAppURL)
	}

	// A second daemon with PatientAppURL unset must omit the key entirely.
	cfg2 := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      event.NewBus(fixedClock),
		Sup:      supervisor.New(nil),
	}
	_, apiBase2 := startDaemon(t, cfg2)
	_, body = doJSON(t, http.MethodGet, apiBase2+"/api/status", token, nil)
	if strings.Contains(string(body), "patientAppUrl") {
		t.Fatalf("/api/status body = %s, want no patientAppUrl key when not configured", body)
	}
}

// fakeTokenStorage is a minimal interface{ Detail() string } stand-in for
// GET /api/bootstrap's tokenStorage row — kitd only
// needs the Detail() seam, never a real bootstrap.TokenStore/keychain.
type fakeTokenStorage string

func (f fakeTokenStorage) Detail() string { return string(f) }

// TestBootstrapGet_TokenStorage: GET /api/bootstrap widens with tokenStorage
// once Config.TokenStorage is set — omitted
// entirely (never an empty string) when it isn't, the same key-presence
// contract as patientAppUrl/validator above.
func TestBootstrapGet_TokenStorage(t *testing.T) {
	const token = "row-tokenstorage-token"
	boot := bootstrap.New(bootstrap.Config{SecretsDir: filepath.Join(t.TempDir(), "secrets")})
	cfg := Config{
		APIAddr:      "127.0.0.1:0",
		StateDir:     t.TempDir(),
		Token:        token,
		Bus:          event.NewBus(fixedClock),
		Sup:          supervisor.New(nil),
		Boot:         boot,
		TokenStorage: fakeTokenStorage("file (keychain unavailable: keychain locked)"),
	}
	_, apiBase := startDaemon(t, cfg)

	_, body := doJSON(t, http.MethodGet, apiBase+"/api/bootstrap", token, nil)
	var resp struct {
		TokenStorage string `json:"tokenStorage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal /api/bootstrap body: %v", err)
	}
	if resp.TokenStorage != "file (keychain unavailable: keychain locked)" {
		t.Fatalf("tokenStorage = %q, want the fake store's Detail()", resp.TokenStorage)
	}

	// A second daemon with TokenStorage unset must omit the key entirely.
	boot2 := bootstrap.New(bootstrap.Config{SecretsDir: filepath.Join(t.TempDir(), "secrets")})
	cfg2 := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      event.NewBus(fixedClock),
		Sup:      supervisor.New(nil),
		Boot:     boot2,
	}
	_, apiBase2 := startDaemon(t, cfg2)
	_, body = doJSON(t, http.MethodGet, apiBase2+"/api/bootstrap", token, nil)
	if strings.Contains(string(body), "tokenStorage") {
		t.Fatalf("/api/bootstrap body = %s, want no tokenStorage key when not configured", body)
	}
}

// ---- /ui static route, empty-UIDir no-op, errors.Is 409 --------------------

// TestUIDir_ServedUngated: with Config.UIDir set to a temp dir of built
// renderer assets, GET /ui/* is served with NO token required —
// the assets carry no secrets; everything the UI does still authenticates
// per-call with the session token, proven here by the untouched /api/status
// gate. Directory listings are disabled (a bare "GET /ui/assets/" 404s
// rather than enumerating the bundle). The two traversal rows are asserted
// via BODY content, not a pinned status code: the
// "GET /ui/../session.json" variant is mux-cleaned to "/session.json",
// redirected, and lands on the token gate (401, no token); the
// "GET /ui/..%2fsession.json" variant survives mux cleaning (the escaped
// path's "%2f" is opaque to path.Clean), reaches StripPrefix, and is
// answered by http.Dir.Open's own root-clamp (404) — either way, the real
// session.json bytes Serve wrote into StateDir must never appear in the
// response body.
func TestUIDir_ServedUngated(t *testing.T) {
	uiDir := t.TempDir()
	const indexBody = "<html><head><title>SHN Kit</title></head><body></body></html>"
	if err := os.WriteFile(filepath.Join(uiDir, "index.html"), []byte(indexBody), 0644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.Mkdir(filepath.Join(uiDir, "assets"), 0755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "assets", "app.js"), []byte("console.log('app')"), 0644); err != nil {
		t.Fatalf("write assets/app.js: %v", err)
	}

	const token = "ui-dir-fixed-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		UIDir:    uiDir,
	}
	// startDaemon's underlying Serve writes a REAL session.json into StateDir
	// before this test ever issues a request, so the traversal rows below
	// prove isolation from an actually-present secret, not mere absence.
	d, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodGet, apiBase+"/ui/", "", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "<title>SHN Kit</title>") {
		t.Fatalf("GET /ui/ = %d (body=%s), want 200 containing the index body", status, body)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/ui/assets/app.js", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /ui/assets/app.js = %d (body=%s), want 200", status, body)
	}

	status, _ = doJSON(t, http.MethodGet, apiBase+"/ui/missing.js", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("GET /ui/missing.js = %d, want 404", status)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/ui/assets/", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("GET /ui/assets/ = %d (body=%s), want 404 (directory listings disabled)", status, body)
	}
	if strings.Contains(string(body), "app.js") {
		t.Fatalf("GET /ui/assets/ body = %s, want no directory listing naming app.js", body)
	}

	// The token gate on /api/* is untouched by the /ui/ route's existence.
	if status, _ := doJSON(t, http.MethodGet, apiBase+"/api/status", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /api/status without token = %d, want 401 (the gate stays)", status)
	}

	for _, p := range []string{"/ui/../session.json", "/ui/..%2fsession.json"} {
		status, body := doJSON(t, http.MethodGet, apiBase+p, "", nil)
		if status == http.StatusOK {
			t.Fatalf("GET %s = 200 (body=%s), want NOT 200 (must never serve session.json)", p, body)
		}
		if strings.Contains(string(body), d.token) {
			t.Fatalf("GET %s body = %s, want it to NOT contain the real session token", p, body)
		}
		if strings.Contains(string(body), `"api"`) {
			t.Fatalf("GET %s body = %s, want it to NOT contain session.json's \"api\" key", p, body)
		}
	}
}

// TestUIDir_EmptyNoRoute: Config.UIDir left "" (the existing daemon fixture
// shape) must change nothing — GET /ui/ falls through to the gated
// catch-all mux and 401s without a token, exactly like any other unmounted
// path.
func TestUIDir_EmptyNoRoute(t *testing.T) {
	const token = "ui-dir-empty-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		// UIDir intentionally left "".
	}
	_, apiBase := startDaemon(t, cfg)

	status, _ := doJSON(t, http.MethodGet, apiBase+"/ui/", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("GET /ui/ with UIDir unset = %d, want 401 (falls through to the token gate; \"\" changes nothing)", status)
	}
}

// TestBootstrapSignin_NonSentinelError_Maps500 pins the errors.Is split: a
// SignIn error that is NEITHER bootstrap.ErrSignInInProgress NOR
// bootstrap.ErrAlreadyProvisioned (here, a transport failure against an
// unreachable Accounts URL) must map to 500, never 409 — the pre-existing
// string-match fallback this replaces would have false-positived on any
// transport error whose body happened to echo "already in progress".
func TestBootstrapSignin_NonSentinelError_Maps500(t *testing.T) {
	const token = "row-nonsentinel-signin-token"
	bus := event.NewBus(fixedClock)
	boot := bootstrap.New(bootstrap.Config{
		AccountsURL: fmt.Sprintf("http://127.0.0.1:%d", freePort(t)), // nothing listening: connection refused
		SecretsDir:  filepath.Join(t.TempDir(), "secrets"),
		ClientName:  "SHN Kit",
		Role:        "provider",
		Bus:         bus,
		Ports:       []int{freePort(t)},
	})
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Boot:     boot,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bootstrap/signin", token, nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("POST /api/bootstrap/signin (unreachable accounts URL) = %d (body=%s), want 500 — a non-sentinel SignIn error must not map to 409", status, body)
	}
}

// ---- /api/history routes ----------------------------------------------------

// TestHistoryList_TwoRecordsNewestFirst_LowercaseKeys pre-seeds a temp-dir
// runhistory.Store with two Save'd records, then proves GET /api/history
// returns both summaries newest first with the lowercase wire keys
// (runId, eventCount) the UI client pins.
func TestHistoryList_TwoRecordsNewestFirst_LowercaseKeys(t *testing.T) {
	histDir := t.TempDir()
	store := runhistory.NewStore(histDir, 200)
	older := runhistory.Record{Summary: runhistory.Summary{
		RunID: "run-older", Lane: "ehr", UC: "uc01", Branch: "covered", State: "passed",
		Time: time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC), EventCount: 2,
	}, Events: []event.Event{{Seq: 1}, {Seq: 2}}}
	newer := runhistory.Record{Summary: runhistory.Summary{
		RunID: "run-newer", Lane: "ehr", UC: "uc07", Branch: "approve", State: "passed",
		Time: time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC), EventCount: 3,
	}, Events: []event.Event{{Seq: 1}, {Seq: 2}, {Seq: 3}}}
	if err := store.Save(older); err != nil {
		t.Fatalf("Save(older): %v", err)
	}
	if err := store.Save(newer); err != nil {
		t.Fatalf("Save(newer): %v", err)
	}

	const token = "history-list-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		History:  store,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/history", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/history = %d, want 200 (body=%s)", status, body)
	}
	// Raw-body key check: pin lowercase
	// runId/eventCount before decoding into Go types, so a struct-tag drift
	// can't hide the wire shape from this test.
	if !strings.Contains(string(body), `"runId"`) || !strings.Contains(string(body), `"eventCount"`) {
		t.Fatalf("GET /api/history body = %s, want lowercase \"runId\"/\"eventCount\" keys", body)
	}

	var sums []runhistory.Summary
	if err := json.Unmarshal(body, &sums); err != nil {
		t.Fatalf("unmarshal /api/history body: %v", err)
	}
	if len(sums) != 2 {
		t.Fatalf("GET /api/history = %d summaries, want 2: %+v", len(sums), sums)
	}
	if sums[0].RunID != "run-newer" || sums[1].RunID != "run-older" {
		t.Fatalf("GET /api/history order = [%s, %s], want [run-newer, run-older] (newest first)", sums[0].RunID, sums[1].RunID)
	}

	// Without a token → 401, same gate as every other /api/* route.
	status, _ = doJSON(t, http.MethodGet, apiBase+"/api/history", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("GET /api/history without token = %d, want 401", status)
	}
}

// TestHistoryGet_KnownUnknownAndNilHistory covers GET /api/history/{runId}:
// a known run returns 200 with its full Events slice, an unknown id 404s,
// and a daemon with Config.History nil 404s BOTH routes with "not configured"
// (mirroring the existing nil-Config-field pattern for Boot/Runner).
func TestHistoryGet_KnownUnknownAndNilHistory(t *testing.T) {
	histDir := t.TempDir()
	store := runhistory.NewStore(histDir, 200)
	rec := runhistory.Record{Summary: runhistory.Summary{
		RunID: "run-get", Lane: "ehr", UC: "uc01", Branch: "covered", State: "passed",
		Time: time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC), EventCount: 3,
	}, Events: []event.Event{{Seq: 1}, {Seq: 2}, {Seq: 3}}}
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const token = "history-get-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		History:  store,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/history/run-get", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/history/run-get = %d, want 200 (body=%s)", status, body)
	}
	var got runhistory.Record
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal /api/history/run-get body: %v", err)
	}
	if len(got.Events) != 3 {
		t.Fatalf("GET /api/history/run-get events = %d, want 3: %+v", len(got.Events), got.Events)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/history/nope", token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("GET /api/history/nope = %d, want 404 (body=%s)", status, body)
	}
	var notFoundBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &notFoundBody); err != nil || notFoundBody.Error != "run not found" {
		t.Fatalf("GET /api/history/nope body = %s, want {\"error\":\"run not found\"}", body)
	}

	// A daemon with Config.History nil (the S3-shaped fixture shape every
	// other test in this file uses) 404s BOTH routes with "not configured".
	cfg2 := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      event.NewBus(fixedClock),
		Sup:      supervisor.New(nil),
		// History intentionally left nil.
	}
	_, apiBase2 := startDaemon(t, cfg2)

	for _, path := range []string{"/api/history", "/api/history/run-get"} {
		status, body = doJSON(t, http.MethodGet, apiBase2+path, token, nil)
		if status != http.StatusNotFound {
			t.Fatalf("GET %s (nil History) = %d, want 404 (body=%s)", path, status, body)
		}
		var errBody struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errBody); err != nil || !strings.Contains(errBody.Error, "not configured") {
			t.Fatalf("GET %s (nil History) body = %s, want an \"error\" containing \"not configured\"", path, body)
		}
	}
}

// TestHistoryList_EmptyStoreReturnsEmptyArrayNotNull proves GET /api/history
// against a Store whose dir has never been written (no run ever Saved) reads
// as 200 [] — never a bare "null" a naive JS client would choke iterating.
func TestHistoryList_EmptyStoreReturnsEmptyArrayNotNull(t *testing.T) {
	histDir := filepath.Join(t.TempDir(), "never-written")
	store := runhistory.NewStore(histDir, 200)

	const token = "history-empty-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		History:  store,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/history", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/history (empty store) = %d, want 200 (body=%s)", status, body)
	}
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "[") {
		t.Fatalf("GET /api/history (empty store) body = %s, want it to start with \"[\" (never bare null)", body)
	}
	var sums []runhistory.Summary
	if err := json.Unmarshal(body, &sums); err != nil {
		t.Fatalf("unmarshal /api/history (empty store) body: %v", err)
	}
	if len(sums) != 0 {
		t.Fatalf("GET /api/history (empty store) = %+v, want empty", sums)
	}
}

// ---- POST /api/verify re-probe ---------------------------------------------

// TestVerifyPost_503BeforeSetVerifyFunc proves the daemon-first posture:
// before SetVerifyFunc has ever been called (mirroring the /api/runs
// pre-SetRunner gate), POST /api/verify 503s rather than panicking on a nil
// closure.
func TestVerifyPost_503BeforeSetVerifyFunc(t *testing.T) {
	const token = "verify-503-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		// Runner/Boot/verifyFn all intentionally left unset.
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/verify", token, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/verify before SetVerifyFunc = %d, want 503 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || !strings.Contains(errBody.Error, "boot") {
		t.Fatalf("503 body = %s, want an \"error\" mentioning boot", body)
	}
}

// TestVerifyPost_ReprobeHappyPath wires a SetVerifyFunc whose Detail encodes
// an invocation counter, proving: POST /api/verify returns 200 with the
// fresh probes; a following GET /api/bootstrap serves the SAME probes (proof
// SetVerify was called, per the brief's "also stored via SetVerify" line); a
// second POST returns the counter incremented (proof of re-invocation, not a
// cached result).
func TestVerifyPost_ReprobeHappyPath(t *testing.T) {
	const token = "verify-happy-token"
	bus := event.NewBus(fixedClock)
	boot := bootstrap.New(bootstrap.Config{SecretsDir: filepath.Join(t.TempDir(), "secrets")})
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Boot:     boot,
	}
	d, apiBase := startDaemon(t, cfg)

	var calls int
	d.SetVerifyFunc(func(ctx context.Context) []bootstrap.Probe {
		calls++
		return []bootstrap.Probe{{Name: "discovery", OK: true, Detail: fmt.Sprintf("call %d", calls)}}
	})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/verify", token, nil)
	if status != http.StatusOK {
		t.Fatalf("POST /api/verify = %d, want 200 (body=%s)", status, body)
	}
	var probes []bootstrap.Probe
	if err := json.Unmarshal(body, &probes); err != nil {
		t.Fatalf("unmarshal POST /api/verify body: %v", err)
	}
	if len(probes) != 1 || probes[0].Detail != "call 1" {
		t.Fatalf("POST /api/verify probes = %+v, want a single probe with Detail \"call 1\"", probes)
	}

	// GET /api/bootstrap must serve the SAME fresh probes: SetVerify happened.
	_, body = doJSON(t, http.MethodGet, apiBase+"/api/bootstrap", token, nil)
	var bootResp struct {
		Verify []bootstrap.Probe `json:"verify"`
	}
	if err := json.Unmarshal(body, &bootResp); err != nil {
		t.Fatalf("unmarshal /api/bootstrap body: %v", err)
	}
	if len(bootResp.Verify) != 1 || bootResp.Verify[0].Detail != "call 1" {
		t.Fatalf("GET /api/bootstrap verify = %+v, want the same call-1 probe POST /api/verify just produced", bootResp.Verify)
	}

	// A second POST re-invokes fn — the counter increments, proving it isn't
	// a cached result.
	status, body = doJSON(t, http.MethodPost, apiBase+"/api/verify", token, nil)
	if status != http.StatusOK {
		t.Fatalf("second POST /api/verify = %d, want 200 (body=%s)", status, body)
	}
	if err := json.Unmarshal(body, &probes); err != nil {
		t.Fatalf("unmarshal second POST /api/verify body: %v", err)
	}
	if len(probes) != 1 || probes[0].Detail != "call 2" {
		t.Fatalf("second POST /api/verify probes = %+v, want Detail \"call 2\"", probes)
	}
}

// TestVerifyPost_SingleFlight409 proves a re-probe already in flight answers
// a concurrent POST with 409 rather than running two probe sets at once
// (mirroring the /api/runs busy-409 posture).
func TestVerifyPost_SingleFlight409(t *testing.T) {
	const token = "verify-409-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
	}
	d, apiBase := startDaemon(t, cfg)

	inFlight := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d.SetVerifyFunc(func(ctx context.Context) []bootstrap.Probe {
		once.Do(func() { close(inFlight) })
		<-release
		return []bootstrap.Probe{{Name: "discovery", OK: true, Detail: "released"}}
	})

	firstDone := make(chan struct {
		status int
		body   []byte
	}, 1)
	go func() {
		status, body := doJSON(t, http.MethodPost, apiBase+"/api/verify", token, nil)
		firstDone <- struct {
			status int
			body   []byte
		}{status, body}
	}()

	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("first POST /api/verify never entered the probe closure")
	}

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/verify", token, nil)
	if status != http.StatusConflict {
		t.Fatalf("POST /api/verify while busy = %d, want 409 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error != "verify already in flight" {
		t.Fatalf("409 body = %s, want {\"error\":\"verify already in flight\"}", body)
	}

	close(release)

	select {
	case first := <-firstDone:
		if first.status != http.StatusOK {
			t.Fatalf("first POST /api/verify final status = %d, want 200 (body=%s)", first.status, first.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first POST /api/verify never completed after release")
	}
}

// TestVerifyPost_TokenGated pins the 401 row for POST /api/verify explicitly
// (it rides the shared authMiddleware, but every other gated route gets its
// own explicit row too).
func TestVerifyPost_TokenGated(t *testing.T) {
	const token = "verify-token-gate-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
	}
	_, apiBase := startDaemon(t, cfg)

	status, _ := doJSON(t, http.MethodPost, apiBase+"/api/verify", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("POST /api/verify without token = %d, want 401", status)
	}
}

// TestVerifyPost_BoundedContext proves the ctx handed to the verify closure
// carries a deadline no more than verifyTimeout from now — probe funcs must
// not be able to hang the daemon indefinitely (the same bound §a live network
// call like bootstrap.Verify's discovery GET relies on).
func TestVerifyPost_BoundedContext(t *testing.T) {
	const token = "verify-bounded-ctx-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
	}
	d, apiBase := startDaemon(t, cfg)

	deadlineOK := make(chan bool, 1)
	d.SetVerifyFunc(func(ctx context.Context) []bootstrap.Probe {
		dl, ok := ctx.Deadline()
		deadlineOK <- ok && !dl.After(time.Now().Add(verifyTimeout+time.Second))
		return []bootstrap.Probe{}
	})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/verify", token, nil)
	if status != http.StatusOK {
		t.Fatalf("POST /api/verify = %d, want 200 (body=%s)", status, body)
	}
	select {
	case ok := <-deadlineOK:
		if !ok {
			t.Fatal("verify closure's ctx had no deadline, or one further out than verifyTimeout")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("verify closure was never invoked")
	}
}

// TestSetBYO_StoresAndReads is the smoke row for the BYORuntime stub
// (the routes that read this back are built on top of it): before any
// SetBYO call getBYO returns the zero value, and after a call it returns exactly what
// was set, under concurrent-safe access.
func TestSetBYO_StoresAndReads(t *testing.T) {
	bus := event.NewBus(fixedClock)
	d, err := New(Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Bus:      bus,
		Sup:      supervisor.New(nil),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := d.getBYO(); got.GatewayURL != "" || got.LoadError != "" || got.Applied.EHR != nil || got.Applied.DaVinci != nil || got.Browser != nil {
		t.Fatalf("getBYO before any SetBYO = %+v, want the zero value", got)
	}

	want := BYORuntime{
		Applied:    byo.Config{EHR: &byo.EHR{DataURL: "https://ehr.example.org/fhir"}},
		GatewayURL: "http://127.0.0.1:12345",
		LoadError:  "kit/byo: parse byo.json: unexpected EOF",
	}
	d.SetBYO(want)
	if got := d.getBYO(); got.GatewayURL != want.GatewayURL || got.LoadError != want.LoadError ||
		got.Applied.EHR == nil || got.Applied.EHR.DataURL != want.Applied.EHR.DataURL {
		t.Errorf("getBYO after SetBYO = %+v, want %+v", got, want)
	}
}

// ---- /api/byo config + browse routes ---------------------------------------

// genRSAPublicKeyPEM generates a throwaway RSA public key PEM for the
// DaVinci lane's PUT tests. Package kitd (white-box tests here) has no access
// to kit/byo's own private key-gen test helpers (package byo) — same
// generation shape, ported locally.
func genRSAPublicKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("x509.MarshalPKIXPublicKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// capabilityStatementServer returns an httptest.Server whose GET /metadata
// answers a minimal CapabilityStatement — what PUT /api/byo/ehr's live probe
// (byo.ProbeEHR) dials before persisting a swap.
func capabilityStatementServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		w.Write([]byte(`{"resourceType":"CapabilityStatement"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestBYORoutes_NilStore404s proves every /api/byo* route 404s
// {"error":"byo not configured"} when Config.BYO is nil — the S3..S6-shaped
// Config embedding every other test in this file uses, mirroring the
// Boot/History/Runner nil-Config-field pattern.
func TestBYORoutes_NilStore404s(t *testing.T) {
	const token = "byo-nil-store-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		// BYO intentionally left nil.
	}
	_, apiBase := startDaemon(t, cfg)

	rows := []struct{ method, path string }{
		{http.MethodGet, "/api/byo"},
		{http.MethodPut, "/api/byo/ehr"},
		{http.MethodDelete, "/api/byo/ehr"},
		{http.MethodPut, "/api/byo/davinci"},
		{http.MethodDelete, "/api/byo/davinci"},
		{http.MethodGet, "/api/byo/patients"},
		{http.MethodGet, "/api/byo/patients/pat-1/context"},
	}
	for _, row := range rows {
		t.Run(row.method+" "+row.path, func(t *testing.T) {
			status, body := doJSON(t, row.method, apiBase+row.path, token, nil)
			if status != http.StatusNotFound {
				t.Fatalf("%s %s (nil BYO) = %d, want 404 (body=%s)", row.method, row.path, status, body)
			}
			var errBody struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error != "byo not configured" {
				t.Fatalf("%s %s (nil BYO) body = %s, want {\"error\":\"byo not configured\"}", row.method, row.path, body)
			}
		})
	}
}

// TestBYOGet_ZeroState proves GET /api/byo against a fresh Store (nothing
// ever saved, SetBYO never called) reads as {"ehr":null,"davinci":null,
// "ingress":null} with no loadError key.
func TestBYOGet_ZeroState(t *testing.T) {
	store := byo.NewStore(t.TempDir())
	const token = "byo-zero-state-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	if strings.Contains(string(body), "loadError") {
		t.Fatalf("GET /api/byo body = %s, want no loadError key when clean", body)
	}
	var resp struct {
		EHR     json.RawMessage `json:"ehr"`
		DaVinci json.RawMessage `json:"davinci"`
		Ingress json.RawMessage `json:"ingress"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal GET /api/byo body: %v", err)
	}
	if string(resp.EHR) != "null" || string(resp.DaVinci) != "null" || string(resp.Ingress) != "null" {
		t.Fatalf("GET /api/byo body = %s, want ehr/davinci/ingress all null", body)
	}
}

// TestBYOEHRPut_HappyUnauthenticated_RestartRequired proves PUT /api/byo/ehr
// with an unauthenticated (no tokenUrl) EHR against a live CapabilityStatement
// stub: 200 restartRequired, byo.json written, and a following GET shows the
// ehr lane with applied:false (SetBYO was never re-run — restart pending is
// the honest state until an operator actually restarts).
func TestBYOEHRPut_HappyUnauthenticated_RestartRequired(t *testing.T) {
	ehrSrv := capabilityStatementServer(t)
	store := byo.NewStore(t.TempDir())
	const token = "byo-ehr-put-happy-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{
		"dataUrl": ehrSrv.URL,
	})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr = %d, want 200 (body=%s)", status, body)
	}
	var putResp struct {
		RestartRequired bool `json:"restartRequired"`
	}
	if err := json.Unmarshal(body, &putResp); err != nil || !putResp.RestartRequired {
		t.Fatalf("PUT /api/byo/ehr body = %s, want {\"restartRequired\":true}", body)
	}

	saved, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.EHR == nil || saved.EHR.DataURL != ehrSrv.URL {
		t.Fatalf("saved config = %+v, want EHR.DataURL %q", saved, ehrSrv.URL)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	var getResp struct {
		EHR *struct {
			DataURL string `json:"dataUrl"`
			Applied bool   `json:"applied"`
		} `json:"ehr"`
	}
	if err := json.Unmarshal(body, &getResp); err != nil {
		t.Fatalf("unmarshal GET /api/byo body: %v", err)
	}
	if getResp.EHR == nil || getResp.EHR.DataURL != ehrSrv.URL {
		t.Fatalf("GET /api/byo ehr = %+v, want DataURL %q", getResp.EHR, ehrSrv.URL)
	}
	if getResp.EHR.Applied {
		t.Fatalf("GET /api/byo ehr.applied = true, want false (SetBYO was never re-run; restart pending)")
	}
}

// TestBYOEHRPut_ProbeFailure_422NothingSaved proves a PUT whose submitted EHR
// validates but whose live probe fails (dataUrl answering 500) 422s naming
// the status, and byo.json is left untouched.
func TestBYOEHRPut_ProbeFailure_422NothingSaved(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ehrSrv := httptest.NewServer(mux)
	defer ehrSrv.Close()

	store := byo.NewStore(t.TempDir())
	const token = "byo-ehr-put-probe-fail-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{
		"dataUrl": ehrSrv.URL,
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("PUT /api/byo/ehr (probe 500) = %d, want 422 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || !strings.Contains(errBody.Error, "500") {
		t.Fatalf("422 body = %s, want an error naming status 500", body)
	}

	saved, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.EHR != nil {
		t.Fatalf("saved config = %+v, want EHR nil (nothing saved on probe failure)", saved)
	}
}

// TestBYOEHRPut_ValidationFailure_NoProbeNothingSaved proves a tokenUrl set
// without a clientId (the all-or-nothing guard) 422s fast — proving no probe
// was attempted (a probe against the unreachable example.org host would take
// noticeably longer) — and leaves byo.json untouched.
func TestBYOEHRPut_ValidationFailure_NoProbeNothingSaved(t *testing.T) {
	store := byo.NewStore(t.TempDir())
	const token = "byo-ehr-put-validation-fail-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	_, apiBase := startDaemon(t, cfg)

	start := time.Now()
	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{
		"dataUrl":  "https://ehr.example.org/fhir",
		"tokenUrl": "https://ehr.example.org/token", // no clientId/clientKeyPem: all-or-nothing violation
	})
	elapsed := time.Since(start)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("PUT /api/byo/ehr (validation failure) = %d, want 422 (body=%s)", status, body)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("PUT /api/byo/ehr (validation failure) took %v, want fast — no probe should have been attempted", elapsed)
	}

	saved, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.EHR != nil {
		t.Fatalf("saved config = %+v, want EHR nil (validation rejects before any probe/save)", saved)
	}
}

// TestBYOEHRPut_KeyNeverEchoed proves key material is write-only: a PUT
// carrying a clientKeyPem
// results in a GET body that never contains the key bytes, while
// hasClientKey correctly reports true (the key file was written).
func TestBYOEHRPut_KeyNeverEchoed(t *testing.T) {
	ehrSrv := capabilityStatementServer(t)
	store := byo.NewStore(t.TempDir())
	const token = "byo-ehr-put-key-hygiene-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	_, apiBase := startDaemon(t, cfg)

	const fakeKeyPEM = "-----BEGIN PRIVATE KEY-----\nfakefakefakefakefakefakefake\n-----END PRIVATE KEY-----"
	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{
		"dataUrl":      ehrSrv.URL,
		"clientKeyPem": fakeKeyPEM,
	})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr (with clientKeyPem, unauthenticated) = %d, want 200 (body=%s)", status, body)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	if strings.Contains(string(body), "PRIVATE KEY") {
		t.Fatalf("GET /api/byo body = %s, want NO key material", body)
	}
	var resp struct {
		EHR *struct {
			HasClientKey bool `json:"hasClientKey"`
		} `json:"ehr"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal GET /api/byo body: %v", err)
	}
	if resp.EHR == nil || !resp.EHR.HasClientKey {
		t.Fatalf("GET /api/byo ehr = %+v, want hasClientKey:true", resp.EHR)
	}
}

// TestBYODaVinci_PutGetDelete proves a valid RS384 DaVinci entry PUTs 200
// restartRequired, GET echoes the PUBLIC material (clientId/alg/publicKeyPem
// — not a key-hygiene concern, unlike the EHR lane), and DELETE clears it
// back to null.
func TestBYODaVinci_PutGetDelete(t *testing.T) {
	store := byo.NewStore(t.TempDir())
	const token = "byo-davinci-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	_, apiBase := startDaemon(t, cfg)

	pubPEM := genRSAPublicKeyPEM(t)
	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/davinci", token, map[string]string{
		"clientId":     "partner-client-1",
		"alg":          "RS384",
		"publicKeyPem": pubPEM,
	})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/davinci = %d, want 200 (body=%s)", status, body)
	}
	var putResp struct {
		RestartRequired bool `json:"restartRequired"`
	}
	if err := json.Unmarshal(body, &putResp); err != nil || !putResp.RestartRequired {
		t.Fatalf("PUT /api/byo/davinci body = %s, want {\"restartRequired\":true}", body)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	var getResp struct {
		DaVinci *struct {
			ClientID     string `json:"clientId"`
			Alg          string `json:"alg"`
			PublicKeyPEM string `json:"publicKeyPem"`
		} `json:"davinci"`
	}
	if err := json.Unmarshal(body, &getResp); err != nil {
		t.Fatalf("unmarshal GET /api/byo body: %v", err)
	}
	if getResp.DaVinci == nil || getResp.DaVinci.ClientID != "partner-client-1" || getResp.DaVinci.Alg != "RS384" || getResp.DaVinci.PublicKeyPEM != pubPEM {
		t.Fatalf("GET /api/byo davinci = %+v, want the echoed public material", getResp.DaVinci)
	}

	status, body = doJSON(t, http.MethodDelete, apiBase+"/api/byo/davinci", token, nil)
	if status != http.StatusOK {
		t.Fatalf("DELETE /api/byo/davinci = %d, want 200 (body=%s)", status, body)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo after delete = %d, want 200 (body=%s)", status, body)
	}
	if err := json.Unmarshal(body, &getResp); err != nil {
		t.Fatalf("unmarshal GET /api/byo body after delete: %v", err)
	}
	if getResp.DaVinci != nil {
		t.Fatalf("GET /api/byo davinci after DELETE = %+v, want null", getResp.DaVinci)
	}
}

// TestBYOGet_AppliedSemantics proves the applied bool is a deep-equal of the
// saved lane against BYORuntime.Applied: right after SetBYO carries the SAME
// ehr this process saved, GET shows applied:true and a non-null ingress block
// derived from GatewayURL; PUTting a DIFFERENT ehr (SetBYO not re-run) flips
// applied back to false — a saved-but-unrestarted edit is honestly "pending".
func TestBYOGet_AppliedSemantics(t *testing.T) {
	ehrSrv := capabilityStatementServer(t)
	store := byo.NewStore(t.TempDir())
	const token = "byo-applied-semantics-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	d, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{"dataUrl": ehrSrv.URL})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr = %d, want 200 (body=%s)", status, body)
	}
	saved, err := store.Load()
	if err != nil || saved.EHR == nil {
		t.Fatalf("Load after PUT: %+v, %v", saved, err)
	}

	d.SetBYO(BYORuntime{
		Applied:    byo.Config{EHR: saved.EHR},
		GatewayURL: "http://127.0.0.1:1234",
	})

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	var resp struct {
		EHR *struct {
			Applied bool `json:"applied"`
		} `json:"ehr"`
		Ingress *struct {
			BaseURL        string   `json:"baseUrl"`
			TokenURL       string   `json:"tokenUrl"`
			SmartConfigURL string   `json:"smartConfigUrl"`
			Endpoints      []string `json:"endpoints"`
		} `json:"ingress"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal GET /api/byo body: %v", err)
	}
	if resp.EHR == nil || !resp.EHR.Applied {
		t.Fatalf("GET /api/byo ehr = %+v, want applied:true", resp.EHR)
	}
	if resp.Ingress == nil {
		t.Fatalf("GET /api/byo ingress = nil, want non-nil once GatewayURL is set")
	}
	if resp.Ingress.BaseURL != "http://127.0.0.1:1234" {
		t.Fatalf("ingress.baseUrl = %q, want http://127.0.0.1:1234", resp.Ingress.BaseURL)
	}
	if resp.Ingress.TokenURL != "http://127.0.0.1:1234/oauth/token" {
		t.Fatalf("ingress.tokenUrl = %q, want .../oauth/token", resp.Ingress.TokenURL)
	}
	if resp.Ingress.SmartConfigURL != "http://127.0.0.1:1234/.well-known/smart-configuration" {
		t.Fatalf("ingress.smartConfigUrl = %q, want .../.well-known/smart-configuration", resp.Ingress.SmartConfigURL)
	}
	for _, want := range []string{"/cds-services", "/Questionnaire/$questionnaire-package", "/Claim/$submit"} {
		found := false
		for _, got := range resp.Ingress.Endpoints {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ingress.endpoints = %v, want it to contain %q", resp.Ingress.Endpoints, want)
		}
	}

	// PUT a DIFFERENT ehr (SetBYO not re-run) -> applied:false again.
	ehrSrv2 := capabilityStatementServer(t)
	status, body = doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{"dataUrl": ehrSrv2.URL})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr (different) = %d, want 200 (body=%s)", status, body)
	}
	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal GET /api/byo body: %v", err)
	}
	if resp.EHR == nil || resp.EHR.Applied {
		t.Fatalf("GET /api/byo ehr after swap = %+v, want applied:false", resp.EHR)
	}
}

// demoPersonasGetResponse decodes just the field TestBYOGet_DemoPersonas*
// needs — a *bool to distinguish true/false/null.
type demoPersonasGetResponse struct {
	EHR *struct {
		Applied      bool  `json:"applied"`
		DemoPersonas *bool `json:"demoPersonas"`
	} `json:"ehr"`
}

// sentinelPatientServer answers Patient?identifier=urn:shn:member|... with a
// searchset carrying (or not carrying) one entry, standing in for a
// bring-your-own operator's connected FHIR server for the demo-personas
// sentinel check.
func sentinelPatientServer(t *testing.T, found bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/Patient", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		if found {
			w.Write([]byte(`{"resourceType":"Bundle","type":"searchset","entry":[{"resource":{"resourceType":"Patient","id":"pat-1","identifier":[{"system":"` + shnsdk.MemberSystem + `","value":"MBR-COVERED"}]}}]}`))
			return
		}
		w.Write([]byte(`{"resourceType":"Bundle","type":"searchset","entry":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestBYOGet_DemoPersonas_NotApplied_Null proves demoPersonas renders null
// when the saved EHR config exists but this boot never applied it (no
// SetBYO call — the "saved but unrestarted" case; Browser is nil, matching
// the browse routes' own gating) — nothing meaningful to check yet.
func TestBYOGet_DemoPersonas_NotApplied_Null(t *testing.T) {
	ehrSrv := capabilityStatementServer(t)
	store := byo.NewStore(t.TempDir())
	const token = "byo-demopersonas-notapplied-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	_, apiBase := startDaemon(t, cfg) // deliberately no SetBYO call — the saved-but-unrestarted state

	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{"dataUrl": ehrSrv.URL})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr = %d, want 200 (body=%s)", status, body)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	var resp demoPersonasGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.EHR == nil || resp.EHR.Applied {
		t.Fatalf("GET /api/byo ehr = %+v, want applied:false (unrestarted)", resp.EHR)
	}
	if resp.EHR.DemoPersonas != nil {
		t.Fatalf("demoPersonas = %v, want null (nothing applied yet)", *resp.EHR.DemoPersonas)
	}
}

// TestBYOGet_DemoPersonas_True proves demoPersonas renders true when the
// swap is applied and the sentinel member resolves on the connected server.
func TestBYOGet_DemoPersonas_True(t *testing.T) {
	ehrSrv := capabilityStatementServer(t)
	sentinelSrv := sentinelPatientServer(t, true)
	store := byo.NewStore(t.TempDir())
	const token = "byo-demopersonas-true-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	d, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{"dataUrl": ehrSrv.URL})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr = %d, want 200 (body=%s)", status, body)
	}
	saved, err := store.Load()
	if err != nil || saved.EHR == nil {
		t.Fatalf("Load after PUT: %+v, %v", saved, err)
	}
	d.SetBYO(BYORuntime{
		Applied: byo.Config{EHR: saved.EHR},
		Browser: byo.NewBrowser(sentinelSrv.URL, nil),
	})

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	var resp demoPersonasGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.EHR == nil || !resp.EHR.Applied {
		t.Fatalf("GET /api/byo ehr = %+v, want applied:true", resp.EHR)
	}
	if resp.EHR.DemoPersonas == nil || !*resp.EHR.DemoPersonas {
		t.Fatalf("demoPersonas = %v, want true", resp.EHR.DemoPersonas)
	}
}

// TestBYOGet_DemoPersonas_False proves demoPersonas renders false when the
// swap is applied but the sentinel member does NOT resolve on the connected
// server (a well-formed empty searchset — not an error).
func TestBYOGet_DemoPersonas_False(t *testing.T) {
	ehrSrv := capabilityStatementServer(t)
	sentinelSrv := sentinelPatientServer(t, false)
	store := byo.NewStore(t.TempDir())
	const token = "byo-demopersonas-false-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	d, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{"dataUrl": ehrSrv.URL})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr = %d, want 200 (body=%s)", status, body)
	}
	saved, err := store.Load()
	if err != nil || saved.EHR == nil {
		t.Fatalf("Load after PUT: %+v, %v", saved, err)
	}
	d.SetBYO(BYORuntime{
		Applied: byo.Config{EHR: saved.EHR},
		Browser: byo.NewBrowser(sentinelSrv.URL, nil),
	})

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	var resp demoPersonasGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.EHR == nil || !resp.EHR.Applied {
		t.Fatalf("GET /api/byo ehr = %+v, want applied:true", resp.EHR)
	}
	if resp.EHR.DemoPersonas == nil || *resp.EHR.DemoPersonas {
		t.Fatalf("demoPersonas = %v, want false", resp.EHR.DemoPersonas)
	}
}

// TestBYOGet_DemoPersonas_SentinelError_Null proves a sentinel-check ERROR
// (the connected server 500s) renders demoPersonas as null, never a guessed
// false — shown, never assumed.
func TestBYOGet_DemoPersonas_SentinelError_Null(t *testing.T) {
	ehrSrv := capabilityStatementServer(t)
	brokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer brokenSrv.Close()
	store := byo.NewStore(t.TempDir())
	const token = "byo-demopersonas-error-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	d, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{"dataUrl": ehrSrv.URL})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr = %d, want 200 (body=%s)", status, body)
	}
	saved, err := store.Load()
	if err != nil || saved.EHR == nil {
		t.Fatalf("Load after PUT: %+v, %v", saved, err)
	}
	d.SetBYO(BYORuntime{
		Applied: byo.Config{EHR: saved.EHR},
		Browser: byo.NewBrowser(brokenSrv.URL, nil),
	})

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	var resp demoPersonasGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.EHR == nil || !resp.EHR.Applied {
		t.Fatalf("GET /api/byo ehr = %+v, want applied:true", resp.EHR)
	}
	if resp.EHR.DemoPersonas != nil {
		t.Fatalf("demoPersonas = %v, want null on a sentinel-check error", *resp.EHR.DemoPersonas)
	}
}

// TestBYOBrowse_GatingThenProxy proves: with no applied EHR (Browser nil,
// the SetBYO-never-called or pre-restart state), both browse routes 409 with
// the operator-facing message; once SetBYO carries a Browser wired against a
// stub partner FHIR server, the routes proxy through — the byo package owns
// the deep query behavior (fhirsor-mirroring searches), this is only proving
// the handler wiring.
func TestBYOBrowse_GatingThenProxy(t *testing.T) {
	store := byo.NewStore(t.TempDir())
	const token = "byo-browse-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	d, apiBase := startDaemon(t, cfg)

	const wantGateError = "connect your EHR and restart the Kit first"
	status, body := doJSON(t, http.MethodGet, apiBase+"/api/byo/patients", token, nil)
	if status != http.StatusConflict {
		t.Fatalf("GET /api/byo/patients (no Browser) = %d, want 409 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error != wantGateError {
		t.Fatalf("409 body = %s, want %q", body, wantGateError)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo/patients/pat-1/context", token, nil)
	if status != http.StatusConflict {
		t.Fatalf("GET /api/byo/patients/{fhirId}/context (no Browser) = %d, want 409 (body=%s)", status, body)
	}
	if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error != wantGateError {
		t.Fatalf("409 body = %s, want %q", body, wantGateError)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/Patient", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		w.Write([]byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Patient","id":"pat-1","identifier":[{"system":"` + shnsdk.MemberSystem + `","value":"member-1"}],"name":[{"text":"Test Patient"}],"birthDate":"1990-01-01"}}]}`))
	})
	mux.HandleFunc("/DeviceRequest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		w.Write([]byte(`{"resourceType":"Bundle","entry":[]}`))
	})
	mux.HandleFunc("/ServiceRequest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		w.Write([]byte(`{"resourceType":"Bundle","entry":[]}`))
	})
	mux.HandleFunc("/Coverage", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		w.Write([]byte(`{"resourceType":"Bundle","entry":[]}`))
	})
	stubSrv := httptest.NewServer(mux)
	defer stubSrv.Close()

	d.SetBYO(BYORuntime{Browser: byo.NewBrowser(stubSrv.URL, nil)})

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo/patients", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo/patients (with Browser) = %d, want 200 (body=%s)", status, body)
	}
	var patients []byo.PatientSummary
	if err := json.Unmarshal(body, &patients); err != nil {
		t.Fatalf("unmarshal GET /api/byo/patients body: %v", err)
	}
	if len(patients) != 1 || patients[0].FHIRID != "pat-1" {
		t.Fatalf("GET /api/byo/patients = %+v, want one entry with fhirId pat-1", patients)
	}

	status, body = doJSON(t, http.MethodGet, apiBase+"/api/byo/patients/pat-1/context", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo/patients/pat-1/context (with Browser) = %d, want 200 (body=%s)", status, body)
	}
	var pc byo.PatientContext
	if err := json.Unmarshal(body, &pc); err != nil {
		t.Fatalf("unmarshal GET /api/byo/patients/pat-1/context body: %v", err)
	}
	if pc.OrderSummary == "" {
		t.Fatalf("GET /api/byo/patients/pat-1/context = %+v, want a non-empty orderSummary", pc)
	}
}

// TestBYOBrowse_PartnerServerError502 proves a partner FHIR server error
// surfaces as 502 with its own (human-usable) error string, not a generic
// failure — the browse panel renders this directly.
func TestBYOBrowse_PartnerServerError502(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/Patient", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	stubSrv := httptest.NewServer(mux)
	defer stubSrv.Close()

	store := byo.NewStore(t.TempDir())
	const token = "byo-browse-502-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	d, apiBase := startDaemon(t, cfg)
	d.SetBYO(BYORuntime{Browser: byo.NewBrowser(stubSrv.URL, nil)})

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/byo/patients", token, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("GET /api/byo/patients (partner 500) = %d, want 502 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error == "" {
		t.Fatalf("502 body = %s, want a non-empty human-usable error", body)
	}
}

// TestBYOGet_LoadErrorSurfaces proves a non-empty BYORuntime.LoadError (the
// byo.json load fail-safe main.go's boot goroutine falls back on) appears in
// GET /api/byo's loadError key.
func TestBYOGet_LoadErrorSurfaces(t *testing.T) {
	store := byo.NewStore(t.TempDir())
	const token = "byo-load-error-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	d, apiBase := startDaemon(t, cfg)

	const wantErr = "kit/byo: parse byo.json: unexpected EOF"
	d.SetBYO(BYORuntime{LoadError: wantErr})

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/byo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 (body=%s)", status, body)
	}
	var resp struct {
		LoadError string `json:"loadError"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal GET /api/byo body: %v", err)
	}
	if resp.LoadError != wantErr {
		t.Fatalf("GET /api/byo loadError = %q, want %q", resp.LoadError, wantErr)
	}
}

// TestBYORoutes_TokenGated pins the 401 row for every new /api/byo* route
// (each rides the shared authMiddleware, but every other gated route in this
// file gets its own explicit row too).
func TestBYORoutes_TokenGated(t *testing.T) {
	store := byo.NewStore(t.TempDir())
	const token = "byo-token-gate-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	_, apiBase := startDaemon(t, cfg)

	rows := []struct{ method, path string }{
		{http.MethodGet, "/api/byo"},
		{http.MethodPut, "/api/byo/ehr"},
		{http.MethodDelete, "/api/byo/ehr"},
		{http.MethodPut, "/api/byo/davinci"},
		{http.MethodDelete, "/api/byo/davinci"},
		{http.MethodGet, "/api/byo/patients"},
		{http.MethodGet, "/api/byo/patients/pat-1/context"},
	}
	for _, row := range rows {
		t.Run(row.method+" "+row.path, func(t *testing.T) {
			status, _ := doJSON(t, row.method, apiBase+row.path, "", nil)
			if status != http.StatusUnauthorized {
				t.Fatalf("%s %s without token = %d, want 401", row.method, row.path, status)
			}
		})
	}
}

// TestWatchRoutes_TokenGated pins the 401 row for both /api/watch routes
// (the other gated routes each get their own
// explicit row in this file's convention — POST/DELETE /api/watch had none).
// Both ride the same shared authMiddleware as every other gated route
// (kitd.go's `gated` mux group), so a missing token 401s before ever
// reaching handleWatchPost/handleWatchDelete — proven here rather than
// assumed from the middleware's other pinned routes.
func TestWatchRoutes_TokenGated(t *testing.T) {
	const token = "watch-token-gate-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Runner:   runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
	}
	_, apiBase := startDaemon(t, cfg)

	rows := []struct{ method, path string }{
		{http.MethodPost, "/api/watch"},
		{http.MethodDelete, "/api/watch"},
	}
	for _, row := range rows {
		t.Run(row.method+" "+row.path, func(t *testing.T) {
			status, _ := doJSON(t, row.method, apiBase+row.path, "", nil)
			if status != http.StatusUnauthorized {
				t.Fatalf("%s %s without token = %d, want 401", row.method, row.path, status)
			}
		})
	}
}

// ---- runs "member" field + /api/watch routes -------------------------------

// ---- Row 9: POST /api/runs member validation + freeform happy path --------

// TestRunsPost_MemberValidationAndFreeform pins the runs body's
// optional "member" field: non-freeform UCs reject
// a non-empty member with 400 (validateRow's detail surfaces verbatim), and
// the freeform row itself dispatches normally (202).
func TestRunsPost_MemberValidationAndFreeform(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/dispatch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"paRequired":true,"authNumber":"AUTH-KITD-1"}`))
	})
	gwSrv := httptest.NewServer(mux)
	defer gwSrv.Close()

	const token = "row9-member-token"
	bus := event.NewBus(fixedClock)
	rn := runner.New(runner.Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: gwSrv.URL}),
		Bus:    bus,
	})
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Runner:   rn,
	}
	_, apiBase := startDaemon(t, cfg)

	// member set on a non-freeform UC → 400, validateRow's detail surfaces.
	status, body := doJSON(t, http.MethodPost, apiBase+"/api/runs", token,
		map[string]string{"lane": "ehr", "uc": "uc03", "branch": "", "member": "MBR-X"})
	if status != http.StatusBadRequest {
		t.Fatalf("POST /api/runs (member on uc03) = %d, want 400 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || !strings.Contains(errBody.Error, "member is only valid for freeform") {
		t.Fatalf("400 body = %s, want an error naming freeform", body)
	}

	// freeform happy path → 202.
	status, body = doJSON(t, http.MethodPost, apiBase+"/api/runs", token,
		map[string]string{"lane": "ehr", "uc": "freeform", "member": "MBR-X"})
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/runs (freeform) = %d, want 202 (body=%s)", status, body)
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || accepted.RunID == "" {
		t.Fatalf("POST /api/runs (freeform) body = %s, want {\"runId\":\"...\"}", body)
	}
}

// ---- Row 10: /api/watch lifecycle + daemon-first gating -------------------

// TestWatchRoutes_LifecycleAndGating: POST /api/watch 202s with a runId,
// 409s while already open, DELETE closes it 200-with-Result (UC "external"),
// a second DELETE 404s, and both routes 503 before SetRunner.
func TestWatchRoutes_LifecycleAndGating(t *testing.T) {
	const token = "row10-watch-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		// Runner intentionally left nil for the pre-SetRunner gate below.
	}
	d, apiBase := startDaemon(t, cfg)

	status, _ := doJSON(t, http.MethodPost, apiBase+"/api/watch", token, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/watch before SetRunner = %d, want 503", status)
	}
	status, _ = doJSON(t, http.MethodDelete, apiBase+"/api/watch", token, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("DELETE /api/watch before SetRunner = %d, want 503", status)
	}

	rn := runner.New(runner.Config{
		Driver: scenariodriver.New(scenariodriver.Config{}),
		Bus:    bus,
	})
	d.SetRunner(rn)

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/watch", token, nil)
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/watch = %d, want 202 (body=%s)", status, body)
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || accepted.RunID == "" {
		t.Fatalf("POST /api/watch body = %s, want {\"runId\":\"...\"}", body)
	}

	status, body = doJSON(t, http.MethodPost, apiBase+"/api/watch", token, nil)
	if status != http.StatusConflict {
		t.Fatalf("second POST /api/watch (already open) = %d, want 409 (body=%s)", status, body)
	}

	status, body = doJSON(t, http.MethodDelete, apiBase+"/api/watch", token, nil)
	if status != http.StatusOK {
		t.Fatalf("DELETE /api/watch = %d, want 200 (body=%s)", status, body)
	}
	var res runner.Result
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("unmarshal DELETE /api/watch body: %v", err)
	}
	if res.RunID != accepted.RunID || res.UC != "external" {
		t.Fatalf("DELETE /api/watch Result = %+v, want RunID %q UC external", res, accepted.RunID)
	}

	status, body = doJSON(t, http.MethodDelete, apiBase+"/api/watch", token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("second DELETE /api/watch (already closed) = %d, want 404 (body=%s)", status, body)
	}
}

// ---- Row 11: watch survives its own POST's request lifetime ---------------

// TestWatchPost_SurvivesItsOwnRequest is the deterministic pin that
// handleWatchPost hands StartWatch d.baseCtx, never r.Context(): after the
// POST /api/watch response has fully completed, POST /api/watch is polled
// repeatedly and must answer 409 CONSISTENTLY across the window (never a
// stray 202, which would mean the watch had already self-finalized because
// its ctx died with the request) — and a frame driven through the observer
// fixture during that same window must still land on the bus stamped with
// the watch's runId, proving the watch (and its relay stamp) are still
// alive well after the request that opened it returned.
func TestWatchPost_SurvivesItsOwnRequest(t *testing.T) {
	frames := make(chan string, 4)
	var health atomic.Uint64

	obsMux := http.NewServeMux()
	obsMux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		for {
			select {
			case frame := <-frames:
				fmt.Fprint(w, frame)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	obsMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"events":%d}`, health.Load())
	})
	obsSrv := httptest.NewServer(obsMux)
	defer obsSrv.Close()

	bus := event.NewBus(fixedClock)
	rly := relay.New(obsSrv.URL+"/events", obsSrv.URL+"/health", bus, func(string, ...any) {})
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go rly.Run(relayCtx)

	rn := runner.New(runner.Config{
		Driver: scenariodriver.New(scenariodriver.Config{}),
		Bus:    bus,
		Relay:  rly,
	})

	const token = "row11-watch-survives-token"
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Runner:   rn,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/watch", token, nil)
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/watch = %d, want 202 (body=%s)", status, body)
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || accepted.RunID == "" {
		t.Fatalf("POST /api/watch body = %s, want {\"runId\":\"...\"}", body)
	}

	// The above POST's response has now fully completed (doJSON reads the
	// whole body before returning). If handleWatchPost had passed
	// r.Context() to StartWatch, that request ctx would already be dead —
	// the watch would have self-finalized and released the lock, so a
	// following POST would succeed (202 with a NEW runId) instead of 409.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		status, body = doJSON(t, http.MethodPost, apiBase+"/api/watch", token, nil)
		if status != http.StatusConflict {
			t.Fatalf("POST /api/watch while the first watch is open = %d, want 409 (body=%s) — the watch did not survive its own request", status, body)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Drive one frame through the observer fixture: it must land on the bus
	// stamped with the watch's runId, proving the watch is STILL open (and
	// the relay's stamp still set) after the whole poll window above.
	health.Store(1)
	frames <- "id: 1\ndata: {\"seq\":1,\"kind\":\"leg.originated\"}\n\n"

	events := readSSE(t, apiBase+"/events?token="+token, 3) // started, audit.unavailable, observer
	var obsEvt *event.Event
	for i := range events {
		if events[i].Type == event.TypeObserver {
			obsEvt = &events[i]
		}
	}
	if obsEvt == nil {
		t.Fatalf("no observer event found on the bus: %+v", events)
	}
	if obsEvt.RunID != accepted.RunID {
		t.Fatalf("observer event RunID = %q, want %q (the watch must have survived its own request)", obsEvt.RunID, accepted.RunID)
	}
}

// ---- Status widening, POST /api/children/{name}/restart -------------------

// fakeRestarter is Config.Restarter's test double: records every call and
// answers callErr (nil ⇒ success) — never touches a real supervisor/child
// process.
type fakeRestarter struct {
	mu      sync.Mutex
	calls   []string
	callErr error
}

func (f *fakeRestarter) RestartChild(_ context.Context, name string) error {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	return f.callErr
}

func (f *fakeRestarter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestStatus_ValidatorPostureAndBRProviderURL proves GET /api/status's
// "validator"/"brProviderUrl" fields follow the SAME key-presence contract
// as patientAppUrl: both entirely absent before the first
// SetStackInfo call, "validator" alone present once set with an empty
// BRProviderURL (no Java trio), and both present once a trio's br-provider
// base is set too.
func TestStatus_ValidatorPostureAndBRProviderURL(t *testing.T) {
	const token = "status-validator-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
	}
	d, apiBase := startDaemon(t, cfg)

	_, body := doJSON(t, http.MethodGet, apiBase+"/api/status", token, nil)
	if strings.Contains(string(body), "validator") || strings.Contains(string(body), "brProviderUrl") {
		t.Fatalf("/api/status body = %s, want neither key before SetStackInfo", body)
	}

	d.SetStackInfo(StackInfo{Validator: "stand-in"})
	_, body = doJSON(t, http.MethodGet, apiBase+"/api/status", token, nil)
	var resp struct {
		Validator     string `json:"validator"`
		BRProviderURL string `json:"brProviderUrl"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal /api/status body: %v", err)
	}
	if resp.Validator != "stand-in" {
		t.Fatalf("validator = %q, want stand-in", resp.Validator)
	}
	if strings.Contains(string(body), "brProviderUrl") {
		t.Fatalf("/api/status body = %s, want no brProviderUrl key when unset", body)
	}

	d.SetStackInfo(StackInfo{Validator: "packaged", BRProviderURL: "http://127.0.0.1:9091"})
	_, body = doJSON(t, http.MethodGet, apiBase+"/api/status", token, nil)
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal /api/status body: %v", err)
	}
	if resp.Validator != "packaged" || resp.BRProviderURL != "http://127.0.0.1:9091" {
		t.Fatalf("validator/brProviderUrl = %+v, want packaged / http://127.0.0.1:9091", resp)
	}
}

// TestChildRestart_PreBoot503 proves the restart route answers 503 before
// SetRunner has ever been called, regardless of child name — the SAME
// daemon-first posture as /api/runs before SetRunner. This covers BOTH the
// window before the first SetStackInfo call AND the window after it (boot
// calls SetStackInfo right after BuildStack, before CopyPrewarmedH2 and the
// sequential child-start loop that ends in SetRunner): gating on
// Validator == "" alone would leak a restart request for
// a not-yet-registered child through to the Restarter during that window,
// misreporting 404 ("unknown child") instead of 503 ("not started"). An
// unknown child name is deliberately included: the point is that 503 must
// win regardless of what the Restarter would have said.
func TestChildRestart_PreBoot503(t *testing.T) {
	const token = "restart-preboot-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:   "127.0.0.1:0",
		StateDir:  t.TempDir(),
		Token:     token,
		Bus:       bus,
		Sup:       supervisor.New(nil),
		Restarter: &fakeRestarter{},
	}
	d, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/children/validator/restart", token, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("POST .../validator/restart pre-boot (no SetStackInfo yet) = %d, want 503 (body=%s)", status, body)
	}

	// The post-SetStackInfo/pre-SetRunner window: StackInfo is published (as
	// BuildStack resolves it) but the Runner is not yet wired (SetRunner
	// hasn't run). Any child name — including one the Restarter has never
	// heard of — must still 503, never fall through to the Restarter's own
	// 404.
	d.SetStackInfo(StackInfo{Validator: "stand-in"})
	status, body = doJSON(t, http.MethodPost, apiBase+"/api/children/unknown-child/restart", token, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("POST .../unknown-child/restart post-SetStackInfo/pre-SetRunner = %d, want 503 (body=%s)", status, body)
	}
}

// TestChildRestart_GatewayRefused403 proves "gateway" is refused with the
// port/keypair/wiring rationale, WITHOUT ever reaching the injected Restarter
// (the gateway restart stays the existing full-Kit action).
func TestChildRestart_GatewayRefused403(t *testing.T) {
	const token = "restart-gateway-refused-token"
	bus := event.NewBus(fixedClock)
	restarter := &fakeRestarter{}
	cfg := Config{
		APIAddr:   "127.0.0.1:0",
		StateDir:  t.TempDir(),
		Token:     token,
		Bus:       bus,
		Sup:       supervisor.New(nil),
		Runner:    runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
		Restarter: restarter,
	}
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/children/gateway/restart", token, nil)
	if status != http.StatusForbidden {
		t.Fatalf("POST .../gateway/restart = %d, want 403 (body=%s)", status, body)
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(errResp.Error, "only a full Kit restart re-derives") {
		t.Fatalf("403 error body = %q, want it to cite the port/keypair/wiring rationale", errResp.Error)
	}
	if restarter.callCount() != 0 {
		t.Fatalf("Restarter called %d times, want 0 — gateway must be refused before ever reaching it", restarter.callCount())
	}
}

// TestChildRestart_HappyPath proves a non-gateway name reaches the injected
// Restarter and 200s with {"restarted": "<name>"}.
func TestChildRestart_HappyPath(t *testing.T) {
	const token = "restart-happy-token"
	bus := event.NewBus(fixedClock)
	restarter := &fakeRestarter{}
	cfg := Config{
		APIAddr:   "127.0.0.1:0",
		StateDir:  t.TempDir(),
		Token:     token,
		Bus:       bus,
		Sup:       supervisor.New(nil),
		Runner:    runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
		Restarter: restarter,
	}
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "packaged"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/children/validator/restart", token, nil)
	if status != http.StatusOK {
		t.Fatalf("POST .../validator/restart = %d, want 200 (body=%s)", status, body)
	}
	var resp struct {
		Restarted string `json:"restarted"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Restarted != "validator" {
		t.Fatalf("restarted = %q, want validator", resp.Restarted)
	}
	if restarter.callCount() != 1 || restarter.calls[0] != "validator" {
		t.Fatalf("Restarter calls = %+v, want exactly one call with name validator", restarter.calls)
	}
}

// TestChildRestart_Unknown404 proves an error from the injected Restarter
// (standing in for supervisor.Restart's "unknown child" contract) maps to
// 404, surfacing the Restarter's own error text.
func TestChildRestart_Unknown404(t *testing.T) {
	const token = "restart-unknown-token"
	bus := event.NewBus(fixedClock)
	restarter := &fakeRestarter{callErr: fmt.Errorf("supervisor: unknown child %q", "bogus")}
	cfg := Config{
		APIAddr:   "127.0.0.1:0",
		StateDir:  t.TempDir(),
		Token:     token,
		Bus:       bus,
		Sup:       supervisor.New(nil),
		Runner:    runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
		Restarter: restarter,
	}
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/children/bogus/restart", token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("POST .../bogus/restart = %d, want 404 (body=%s)", status, body)
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(errResp.Error, "unknown child") {
		t.Fatalf("404 error body = %q, want the Restarter's own unknown-child message", errResp.Error)
	}
}

// TestChildRestart_InFlight409 proves a run in flight blocks a restart
// attempt (Runner.InFlight()'s best-effort gate) — and that the
// Restarter is never reached while blocked.
func TestChildRestart_InFlight409(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	gwSrv := httptest.NewServer(mux)
	defer gwSrv.Close()

	const token = "restart-inflight-token"
	bus := event.NewBus(fixedClock)
	rn := runner.New(runner.Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: gwSrv.URL}),
		Bus:    bus,
	})
	restarter := &fakeRestarter{}
	cfg := Config{
		APIAddr:   "127.0.0.1:0",
		StateDir:  t.TempDir(),
		Token:     token,
		Bus:       bus,
		Sup:       supervisor.New(nil),
		Runner:    rn,
		Restarter: restarter,
	}
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/runs", token,
		map[string]string{"lane": "ehr", "uc": "uc01", "branch": "covered"})
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/runs = %d, want 202 (body=%s)", status, body)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fake gateway never received the /scenario/uc01 request")
	}

	status, body = doJSON(t, http.MethodPost, apiBase+"/api/children/validator/restart", token, nil)
	if status != http.StatusConflict {
		t.Fatalf("POST .../validator/restart while a run is in flight = %d, want 409 (body=%s)", status, body)
	}
	if restarter.callCount() != 0 {
		t.Fatalf("Restarter called %d times, want 0 while a run is in flight", restarter.callCount())
	}

	close(release)
	waitRunnerIdle(t, rn)
}

// waitRunnerIdle polls Runner.InFlight() until it reads false, or fails the
// test after 5s — used only to let an async run drain before a test ends.
func waitRunnerIdle(t *testing.T, rn *runner.Runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !rn.InFlight() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("runner: did not become idle within 5s")
}

// TestChildRestart_TokenGated mirrors TestWatchRoutes_TokenGated: the
// restart route is behind the same session-token gate as every other
// /api/* route.
func TestChildRestart_TokenGated(t *testing.T) {
	const token = "restart-token-gate-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:   "127.0.0.1:0",
		StateDir:  t.TempDir(),
		Token:     token,
		Bus:       bus,
		Sup:       supervisor.New(nil),
		Restarter: &fakeRestarter{},
	}
	_, apiBase := startDaemon(t, cfg)

	status, _ := doJSON(t, http.MethodPost, apiBase+"/api/children/validator/restart", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("POST .../validator/restart without token = %d, want 401", status)
	}
}

// ---- byo-GET timeout, seed cross-pin ---------------------------------------

// TestBYOGet_DemoPersonas_HangingSentinelBounded proves the bound at
// handleBYOGet's demoPersonasState call (mirroring PUT /api/byo/ehr's own
// verifyTimeout-bounded probe, kitd.go:797's pattern): a sentinel server
// that never answers no longer hangs GET /api/byo past verifyTimeout — the
// request still completes (200, demoPersonas rendered null — an errored/
// timed-out probe, per demoPersonasState's own "shown, never assumed"
// contract), it just takes up to verifyTimeout to do so.
func TestBYOGet_DemoPersonas_HangingSentinelBounded(t *testing.T) {
	ehrSrv := capabilityStatementServer(t)
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never answers; unblocks only once the CLIENT (kitd's bounded probe) gives up
	}))
	defer hangSrv.Close()

	store := byo.NewStore(t.TempDir())
	const token = "byo-hanging-sentinel-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		BYO:      store,
	}
	d, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodPut, apiBase+"/api/byo/ehr", token, map[string]string{"dataUrl": ehrSrv.URL})
	if status != http.StatusOK {
		t.Fatalf("PUT /api/byo/ehr = %d, want 200 (body=%s)", status, body)
	}
	saved, err := store.Load()
	if err != nil || saved.EHR == nil {
		t.Fatalf("Load after PUT: %+v, %v", saved, err)
	}
	d.SetBYO(BYORuntime{
		Applied: byo.Config{EHR: saved.EHR},
		Browser: byo.NewBrowser(hangSrv.URL, nil),
	})

	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout+5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/api/byo", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/byo did not return within verifyTimeout+5s — the hanging sentinel is not bounded: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)
	if elapsed > verifyTimeout+3*time.Second {
		t.Fatalf("GET /api/byo took %v, want bounded near verifyTimeout (%v)", elapsed, verifyTimeout)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/byo = %d, want 200 despite the bounded-out sentinel probe", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	var got demoPersonasGetResponse
	if err := json.Unmarshal(respBody, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EHR == nil || got.EHR.DemoPersonas != nil {
		t.Fatalf("GET /api/byo ehr = %+v, want demoPersonas null (bounded-out probe treated as an error)", got.EHR)
	}
}

// TestConformantLaneSentinelMember_PinnedInSeedArtifact cross-pins
// conformantLaneSentinelMember against the COMMITTED
// kit/seed/demo-personas-conformant.json bytes: kit
// never imports the root module's internal/fhirseed census list (the
// boundary fence, kit/seed/doc.go) — the seed artifact itself is the
// kit-local ground truth this sentinel must track. Fails loudly if a future
// regen of the seed artifact ever drops MBR-COVERED.
func TestConformantLaneSentinelMember_PinnedInSeedArtifact(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "seed", "demo-personas-conformant.json"))
	if err != nil {
		t.Fatalf("read kit/seed/demo-personas-conformant.json: %v", err)
	}
	var bundle struct {
		Entry []struct {
			Resource struct {
				Identifier []struct {
					Value string `json:"value"`
				} `json:"identifier"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(b, &bundle); err != nil {
		t.Fatalf("unmarshal seed artifact: %v", err)
	}
	found := false
	for _, e := range bundle.Entry {
		for _, id := range e.Resource.Identifier {
			if id.Value == conformantLaneSentinelMember {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("conformantLaneSentinelMember %q not found as a member identifier in kit/seed/demo-personas-conformant.json", conformantLaneSentinelMember)
	}
}

// ---- SetUpdate / GET /api/status "update" ----------------------------------

// TestSetUpdate_StatusFieldKeyPresence mirrors TestStatus_ValidatorPostureAndBRProviderURL's
// key-presence contract: "update" is absent from GET /api/status until
// SetUpdate has been called at least once — deliberately NOT keyed off the
// zero value (update.Info{} IS a legitimate "no update available" result, so
// it cannot double as the "never checked" sentinel the way StackInfo.Validator
// == "" or PatientAppURL == "" can).
func TestSetUpdate_StatusFieldKeyPresence(t *testing.T) {
	const token = "status-update-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
	}
	d, apiBase := startDaemon(t, cfg)

	_, body := doJSON(t, http.MethodGet, apiBase+"/api/status", token, nil)
	if strings.Contains(string(body), `"update"`) {
		t.Fatalf(`/api/status body = %s, want no "update" key before SetUpdate`, body)
	}

	// A genuine "no update available" result is itself the zero value — must
	// still surface the key once SetUpdate has actually been called.
	d.SetUpdate(update.Info{Available: false})
	_, body = doJSON(t, http.MethodGet, apiBase+"/api/status", token, nil)
	var resp struct {
		Update *update.Info `json:"update"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal /api/status body: %v", err)
	}
	if resp.Update == nil {
		t.Fatalf(`/api/status body = %s, want an "update" key once SetUpdate has been called, even with Available:false`, body)
	}
	if resp.Update.Available {
		t.Fatalf("update.Available = true, want false")
	}

	d.SetUpdate(update.Info{Available: true, Latest: "v9.9.9", URL: "https://example.org/releases/v9.9.9"})
	_, body = doJSON(t, http.MethodGet, apiBase+"/api/status", token, nil)
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal /api/status body: %v", err)
	}
	if resp.Update == nil || !resp.Update.Available || resp.Update.Latest != "v9.9.9" || resp.Update.URL != "https://example.org/releases/v9.9.9" {
		t.Fatalf("update = %+v, want {available:true latest:v9.9.9 url:https://example.org/releases/v9.9.9}", resp.Update)
	}
}

// ---- GET /api/about ---------------------------------------------------------

// TestAbout_ManifestServedVerbatim proves GET /api/about serves the
// --manifest file's bytes byte-for-byte (no re-marshal, no envelope) —
// tools/kitassets/manifest.sh's versions.json contract.
func TestAbout_ManifestServedVerbatim(t *testing.T) {
	const token = "about-manifest-token"
	manifestPath := filepath.Join(t.TempDir(), "versions.json")
	// Deliberately unusual formatting/whitespace: proves the handler serves
	// the file's ACTUAL bytes, not a decode-then-re-encode of them.
	manifestBytes := []byte("{\n  \"kit\":   \"1.2.3\",\n  \"gateway\": \"0.20.1\"\n}\n")
	if err := os.WriteFile(manifestPath, manifestBytes, 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:      "127.0.0.1:0",
		StateDir:     t.TempDir(),
		Token:        token,
		Bus:          bus,
		Sup:          supervisor.New(nil),
		ManifestPath: manifestPath,
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/about", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/about = %d, want 200 (body=%s)", status, body)
	}
	if !bytes.Equal(body, manifestBytes) {
		t.Fatalf("GET /api/about body = %q, want verbatim manifest bytes %q", body, manifestBytes)
	}

	// Without a token → 401, the same gate as every other /api/* route.
	status, _ = doJSON(t, http.MethodGet, apiBase+"/api/about", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("GET /api/about without token = %d, want 401", status)
	}
}

// TestAbout_AbsentManifest404sWithBody proves the dev posture (no --manifest
// flag, ManifestPath == "") answers a HONEST 404 — a JSON error body, not a
// bare empty 404 — and that an explicitly-set-but-unreadable path 404s the
// same way rather than 500ing.
func TestAbout_AbsentManifest404sWithBody(t *testing.T) {
	const token = "about-absent-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		// ManifestPath intentionally left "" (dev build, no packaged manifest).
	}
	_, apiBase := startDaemon(t, cfg)

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/about", token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("GET /api/about (no manifest) = %d, want 404 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error == "" {
		t.Fatalf("GET /api/about (no manifest) body = %s, want a non-empty \"error\"", body)
	}

	// A path that's set but points at nothing readable — 404, not 500 (the
	// same honest "not available" as leaving it unset entirely).
	cfg2 := cfg
	cfg2.ManifestPath = filepath.Join(t.TempDir(), "does-not-exist.json")
	_, apiBase2 := startDaemon(t, cfg2)
	status, body = doJSON(t, http.MethodGet, apiBase2+"/api/about", token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("GET /api/about (unreadable manifest path) = %d, want 404 (body=%s)", status, body)
	}
}
