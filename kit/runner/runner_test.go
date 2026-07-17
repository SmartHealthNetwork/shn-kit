// runner_test.go — hermetic tests for the scenario runner.
// Drives Run/Start against a FAKE gateway httptest server (standing in for
// the child's provider-data /scenario/* routes and Da Vinci ingress) and a
// fake audit server (a fixture in this same shape) — never a real gateway/child.
// The REAL child is exercised by the integration gate.
package runner

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/auditread"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/relay"
)

func fixedClock() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }

// readSSE GETs url, scans "data: " lines off the response body, and
// unmarshals each into an event.Event. It returns after collecting n
// events or a 5s deadline, whichever comes first. Mirrors kit/event's and
// kit/relay's helper of the same name/shape.
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

// busEventCount reads the bus's /health event counter — a cheap way to
// assert "zero events emitted" without opening a (potentially indefinitely
// blocking) SSE connection.
func busEventCount(t *testing.T, busURL string) int {
	t.Helper()
	resp, err := http.Get(busURL + "/health")
	if err != nil {
		t.Fatalf("GET %s/health: %v", busURL, err)
	}
	defer resp.Body.Close()
	var h struct {
		Events int `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	return h.Events
}

// newFakeAudit starts a fake Audit Plane server whose GET /auditor answers
// callSeqs[callIndex] on the callIndex'th call (clamped to the last entry
// once exhausted) — lets a test script exactly what HighWater/After sees
// pre- vs post-row (the fixture shape: a bare JSON array of records).
func newFakeAudit(t *testing.T, callSeqs [][]int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auditor", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		idx := calls
		if idx >= len(callSeqs) {
			idx = len(callSeqs) - 1
		}
		calls++
		mu.Unlock()
		var recs []auditread.Record
		for _, seq := range callSeqs[idx] {
			recs = append(recs, auditread.Record{Seq: seq, TransactionType: "pas-claim", Outcome: "approved"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recs)
	})
	return httptest.NewServer(mux)
}

// waitIdle polls the sequential lock until it is free (TryLock succeeds),
// or fails the test after 5s. White-box (same package) — used only to
// synchronize on Start's async completion in tests, never in production code.
func waitIdle(t *testing.T, rn *Runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rn.mu.TryLock() {
			rn.mu.Unlock()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("runner: did not become idle within 5s")
}

// ---- Row 1: ehr/uc01 covered ----------------------------------------------

func TestRun_EHRUC01Covered(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"branch":"covered"}` {
			t.Errorf("request body = %s, want {\"branch\":\"covered\"}", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	audit := newFakeAudit(t, [][]int{{1, 2, 3}, {1, 2, 3, 4, 5}}) // grows by 2 between pre/post
	defer audit.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rn := New(Config{
		Driver:   scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:      bus,
		AuditURL: audit.URL,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want %q (Detail=%q)", res.State, StatePassed, res.Detail)
	}
	if res.RunID == "" || res.Lane != "ehr" || res.UC != "uc01" || res.Branch != "covered" {
		t.Fatalf("Result = %+v, unexpected identity fields", res)
	}

	events := readSSE(t, busSrv.URL+"/events", 4) // started, audit, audit, finished
	if events[0].Type != event.TypeRunStarted || events[0].RunID != res.RunID || events[0].Lane != "ehr" || events[0].UC != "uc01" {
		t.Fatalf("events[0] = %+v, want run.started stamped with the run", events[0])
	}
	for i := 1; i <= 2; i++ {
		if events[i].Type != event.TypeAudit || events[i].RunID != res.RunID {
			t.Fatalf("events[%d] = %+v, want an audit event stamped with the run", i, events[i])
		}
		var rec auditread.Record
		if err := json.Unmarshal(events[i].Audit, &rec); err != nil {
			t.Fatalf("events[%d].Audit does not decode as auditread.Record: %v", i, err)
		}
	}
	if events[3].Type != event.TypeRunFinished || events[3].RunID != res.RunID {
		t.Fatalf("events[3] = %+v, want run.finished stamped with the run", events[3])
	}
}

// TestRun_UC07HCPCS_DegradesWhenPatientSurfaceUnreadable proves UC-07's patient-surface
// read-back is SKIPPED gracefully (the row still PASSES on the PA) when the hosted
// patient-surface reads are not externally reachable — the desktop failure. It also proves
// the read-back is not even attempted (the PCI resolver must not run), so a future reachable
// read-back path (Option 3) can be added without this masking it.
func TestRun_UC07HCPCS_DegradesWhenPatientSurfaceUnreadable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc07hcpcs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"paRequired":true,"authNumber":"PA-UC07","validUntil":"2026-12-31"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver:                 scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:                    bus,
		PatientSurfaceReadable: false, // HOSTED: the patient-surface reads are internal/patient-only
		UC07PCI: func() (string, error) {
			t.Error("UC07PCI resolver ran; the read-back must be skipped when the patient-surface is unreadable")
			return "", fmt.Errorf("must not be called")
		},
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc07", Branch: "hcpcs"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want %q (Detail=%q)", res.State, StatePassed, res.Detail)
	}
	if !strings.Contains(res.Detail, "PA-UC07") || !strings.Contains(res.Detail, "skipped") {
		t.Fatalf("Detail = %q, want it to note the auth AND the skipped read-back", res.Detail)
	}
}

// ---- Row 2: sequential lock -----------------------------------------------

func TestRun_SequentialLock(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	var wg sync.WaitGroup
	wg.Add(1)
	var firstRes Result
	var firstErr error
	go func() {
		defer wg.Done()
		firstRes, firstErr = rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	}()

	<-started // the first run holds the lock, blocked inside the row

	if _, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "notcovered"}); !errors.Is(err, ErrRunInFlight) {
		t.Fatalf("concurrent Run: err = %v, want ErrRunInFlight", err)
	}

	close(release)
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("first Run: %v", firstErr)
	}
	if firstRes.State != StatePassed {
		t.Fatalf("first Run: state = %q, want passed (Detail=%q)", firstRes.State, firstRes.Detail)
	}

	// Both rows are "present" per the busy contract: the successful run is
	// recorded; the busy attempt never got far enough to be appended at all.
	results := rn.Results()
	if len(results) != 1 {
		t.Fatalf("Results() = %d rows, want 1 (the busy attempt must never be appended): %+v", len(results), results)
	}
	if results[0].RunID != firstRes.RunID || results[0].State != StatePassed {
		t.Fatalf("Results()[0] = %+v, want the passed first run", results[0])
	}
}

// ---- Row 3: row failure ----------------------------------------------------

func TestRun_RowFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom: downstream unavailable"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run returned a Go error (want a failed Result instead): %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("Result.State = %q, want %q", res.State, StateFailed)
	}
	if !strings.Contains(res.Detail, "500") {
		t.Errorf("Result.Detail = %q, want it to contain the status code 500", res.Detail)
	}
	if !strings.Contains(res.Detail, "boom: downstream unavailable") {
		t.Errorf("Result.Detail = %q, want it to contain a body excerpt", res.Detail)
	}

	events := readSSE(t, busSrv.URL+"/events", 3) // started, audit.unavailable (AuditURL unset), failed
	last := events[len(events)-1]
	if last.Type != event.TypeRunFailed || last.RunID != res.RunID {
		t.Fatalf("last event = %+v, want run.failed stamped with the run", last)
	}
	if last.Detail != res.Detail {
		t.Fatalf("run.failed Detail = %q, want it to match Result.Detail %q", last.Detail, res.Detail)
	}
}

// ---- Row 4: AuditURL empty --------------------------------------------------

func TestRun_AuditURLEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
		// AuditURL deliberately left "".
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}

	events := readSSE(t, busSrv.URL+"/events", 3) // started, audit.unavailable, finished
	n := 0
	for _, e := range events {
		switch e.Type {
		case event.TypeAuditUnavailable:
			n++
			if e.Detail != "no readable Audit Plane configured (hosted reads are internal-only)" {
				t.Errorf("audit.unavailable Detail = %q, unexpected", e.Detail)
			}
		case event.TypeAudit:
			t.Errorf("unexpected audit event when AuditURL is empty: %+v", e)
		}
	}
	if n != 1 {
		t.Fatalf("audit.unavailable count = %d, want exactly 1", n)
	}
}

// ---- Row 5: conformant/uc02 -------------------------------------------------

func TestRun_ConformantUC02(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
		// Transport fake: does NOT verify the minted bearer — real
		// verification is the (later) live gate's job, not this test's.
		if r.Header.Get("Authorization") == "" {
			t.Errorf("request carries no Authorization header — the driver should have minted a bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{
			IngressURL:  srv.URL,
			IngressBase: srv.URL,
			ClientID:    "kit-runner-test",
			Key:         key,
		}),
		Bus: bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc02", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}
	if !strings.Contains(res.Detail, "No prior authorization required") {
		t.Errorf("Result.Detail = %q, want it to contain the card summary", res.Detail)
	}
}

// TestRun_ConformantUC02_MemberNotOnConnectedEHR is a deliberate
// rejection row: the CRD ingress answers the byte-real subject-bind-miss
// shape (status 400, body {"error":"unknown member"} — pinned live at
// gateway/engine/ingress_crd.go:104's ingressCRDSubjectPCI, written via
// gateway/engine/gateway.go:524's writeJSON; see
// ConformantMemberNotOnConnectedEHRSentence's doc comment for the full
// evidence trail) — the run must fail with the named sentence, never the
// raw 400 relayed verbatim.
func TestRun_ConformantUC02_MemberNotOnConnectedEHR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"unknown member"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{
			IngressURL:  srv.URL,
			IngressBase: srv.URL,
			ClientID:    "kit-runner-test",
			Key:         key,
		}),
		Bus: bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc02", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("Result.State = %q, want failed", res.State)
	}
	if !strings.Contains(res.Detail, ConformantMemberNotOnConnectedEHRSentence) {
		t.Errorf("Result.Detail = %q, want it to contain the named sentence %q", res.Detail, ConformantMemberNotOnConnectedEHRSentence)
	}
}

// TestRun_ConformantUC02_OtherIngressFailure_NotRelabeled is the pass-through
// regression row: a DIFFERENT ingress failure (same 400 status, a wholly
// different body) must keep its raw detail — status alone is not the
// discriminator, and isConformantIngressUnknownMember must not over-match.
func TestRun_ConformantUC02_OtherIngressFailure_NotRelabeled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"missing context.patientId"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{
			IngressURL:  srv.URL,
			IngressBase: srv.URL,
			ClientID:    "kit-runner-test",
			Key:         key,
		}),
		Bus: bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc02", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("Result.State = %q, want failed", res.State)
	}
	if !strings.Contains(res.Detail, "missing context.patientId") {
		t.Errorf("Result.Detail = %q, want the raw detail passed through unchanged", res.Detail)
	}
	if strings.Contains(res.Detail, ConformantMemberNotOnConnectedEHRSentence) {
		t.Errorf("Result.Detail = %q, must NOT contain the member-not-on-EHR sentence for a non-matching failure", res.Detail)
	}
}

// ---- Row 5b: CRD-prong BFF branch -------------------------------------------

// TestRun_ConformantUC02_BFFOrigination proves that with Config.BFFURL set
// (the Java trio present), uc02's CRD leg originates through br-provider's
// real BFF (scenariodriver.OriginateThroughBRProvider) — the fake BFF server
// asserts the hit landed on ITS endpoint (POST /api/cds-services/
// order-select-crd), the ingress server is NEVER hit directly, and the row
// detail carries the br-provider provenance line.
func TestRun_ConformantUC02_BFFOrigination(t *testing.T) {
	var bffHit bool
	bffMux := http.NewServeMux()
	bffMux.HandleFunc("POST /api/cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
		bffHit = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`))
	})
	bffSrv := httptest.NewServer(bffMux)
	defer bffSrv.Close()

	ingressMux := http.NewServeMux()
	ingressMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("ingress %s %s was hit directly — the BFF branch must route through br-provider, never PostCRD", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	ingressSrv := httptest.NewServer(ingressMux)
	defer ingressSrv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{
			IngressURL:  ingressSrv.URL,
			IngressBase: ingressSrv.URL,
			ClientID:    "kit-runner-test",
			Key:         key,
			BFFURL:      bffSrv.URL,
		}),
		Bus:    bus,
		BFFURL: bffSrv.URL,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc02", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}
	if !bffHit {
		t.Error("br-provider BFF endpoint was never hit")
	}
	if !strings.Contains(res.Detail, brProviderOriginatedPrefix) {
		t.Errorf("Result.Detail = %q, want it to contain the br-provider provenance line %q", res.Detail, brProviderOriginatedPrefix)
	}
}

// TestRun_ConformantUC02_NoBFF_PostCRDUnchanged is the no-trio regression
// pin: with Config.BFFURL left empty, uc02 drives PostCRD exactly as before
// the BFF branch existed — byte-for-byte TestRun_ConformantUC02's own assertions,
// duplicated here under the BFF-branch's name so the two rows read as a
// deliberate pair.
func TestRun_ConformantUC02_NoBFF_PostCRDUnchanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{
			IngressURL:  srv.URL,
			IngressBase: srv.URL,
			ClientID:    "kit-runner-test",
			Key:         key,
		}),
		Bus: bus,
		// BFFURL deliberately left "" — the no-trio posture.
	})

	res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc02", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}
	if strings.Contains(res.Detail, brProviderOriginatedPrefix) {
		t.Errorf("Result.Detail = %q, must NOT contain the br-provider provenance line when BFFURL is empty", res.Detail)
	}
	if !strings.Contains(res.Detail, "No prior authorization required") {
		t.Errorf("Result.Detail = %q, want it to contain the card summary", res.Detail)
	}
}

// TestRun_ConformantUC03_UnderBFF_StillDriverMinted proves the
// no-over-capture condition: conformantBRPScenario carries no entry for
// "uc03", so even with Config.BFFURL set (the Java trio present), uc03's CRD
// leg still drives the direct-mint PostCRD ingress path — never
// br-provider's BFF. The fake BFF server is cribbed from
// TestRun_ConformantUC02_BFFOrigination's fixture, but here it asserts it was
// NEVER hit; the fake ingress fixture serves all three conformant uc03 legs
// (CRD, DTR $questionnaire-package, PAS $submit) so the row runs to
// completion driver-minted, exactly as it does with BFFURL empty.
func TestRun_ConformantUC03_UnderBFF_StillDriverMinted(t *testing.T) {
	var bffHit bool
	bffMux := http.NewServeMux()
	bffMux.HandleFunc("POST /api/cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
		bffHit = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`))
	})
	bffSrv := httptest.NewServer(bffMux)
	defer bffSrv.Close()

	var crdHit bool
	ingressMux := http.NewServeMux()
	ingressMux.HandleFunc("POST /cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
		crdHit = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"covered","paNeeded":"auth-needed","questionnaires":["` + shnsdk.QuestionnaireCanonicalLumbarMRI + `"]}}]}`))
	})
	ingressMux.HandleFunc("POST /Questionnaire/$questionnaire-package", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Questionnaire","id":"pa-lumbar-mri"}}]}`))
	})
	ingressMux.HandleFunc("POST /Claim/$submit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-UC03-NO-OVER-CAPTURE"}`))
	})
	ingressSrv := httptest.NewServer(ingressMux)
	defer ingressSrv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{
			IngressURL:  ingressSrv.URL,
			IngressBase: ingressSrv.URL,
			ClientID:    "kit-runner-test",
			Key:         key,
			BFFURL:      bffSrv.URL,
		}),
		Bus:    bus,
		BFFURL: bffSrv.URL,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc03", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}
	if bffHit {
		t.Error("br-provider BFF endpoint was hit for uc03 — conformantBRPScenario must have NO entry for uc03 (no over-capture)")
	}
	if !crdHit {
		t.Error("ingress CRD endpoint was never hit — uc03 must still drive the direct-mint PostCRD path under the trio")
	}
	if strings.Contains(res.Detail, brProviderOriginatedPrefix) {
		t.Errorf("Result.Detail = %q, must NOT contain the br-provider provenance line for uc03 (provenance stays uc02-only)", res.Detail)
	}
}

// ---- Row 6: unknown row -----------------------------------------------------

func TestRun_UnknownRow(t *testing.T) {
	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rn := New(Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus})

	if _, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc99", Branch: ""}); err == nil {
		t.Fatal("Run(ehr, uc99, \"\"): want an error for an unknown UC")
	}
	if n := busEventCount(t, busSrv.URL); n != 0 {
		t.Fatalf("bus events = %d, want 0 (an unknown row must not create a run)", n)
	}
	if len(rn.Results()) != 0 {
		t.Fatalf("Results() = %d, want 0", len(rn.Results()))
	}
}

// ---- Start: 400/409-shaped behavior ----------------------------------------

func TestStart_UnknownRow(t *testing.T) {
	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rn := New(Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus})

	runID, err := rn.Start(context.Background(), Req{Lane: "conformant", UC: "uc00", Branch: ""})
	if err == nil {
		t.Fatal("Start(conformant, uc00, \"\"): want an error for an unknown UC")
	}
	if runID != "" {
		t.Fatalf("Start: runID = %q, want empty on a validation error", runID)
	}
	if n := busEventCount(t, busSrv.URL); n != 0 {
		t.Fatalf("bus events = %d, want 0 (an unknown row must not create a run)", n)
	}
}

// Start must detach the spawned run from the CALLER's context: the
// daemon handler calls Start(r.Context(), ...) and returns immediately, and
// net/http cancels that ctx as the handler returns — the async run's audit
// fetches (load-bearing when AuditURL is set) must survive that. RED against
// a Start that hands the caller ctx to the row goroutine.
func TestStart_DetachedFromCallerContext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	audit := newFakeAudit(t, [][]int{{1}, {1, 2}}) // grows by 1 between pre/post
	defer audit.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rn := New(Config{
		Driver:   scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:      bus,
		AuditURL: audit.URL,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ALREADY canceled before Start — the handler-returned shape

	runID, err := rn.Start(ctx, Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitIdle(t, rn)

	results := rn.Results()
	if len(results) != 1 || results[0].RunID != runID {
		t.Fatalf("Results() = %+v, want exactly 1 row with RunID %q", results, runID)
	}
	if results[0].State != StatePassed {
		t.Fatalf("Result.State = %q, want passed — the async run must not inherit the caller's canceled ctx (Detail=%q)", results[0].State, results[0].Detail)
	}

	events := readSSE(t, busSrv.URL+"/events", 3) // started, audit, finished
	if events[1].Type != event.TypeAudit || events[1].RunID != runID {
		t.Fatalf("events[1] = %+v, want an audit event stamped with the run (the merge must have run)", events[1])
	}
}

// A panicking row must fail its run legibly and leave the runner usable —
// never wedge the sequential lock (every later call ErrRunInFlight). The
// panicking row is injected into the ehr row table for the test's duration
// (white-box; the tables are package vars, and tests in this package do not
// run in parallel).
func TestRun_RowPanicDoesNotWedgeRunner(t *testing.T) {
	const panicUC = "uc98"
	ehrRows[panicUC] = func(rn *Runner, branch string) (string, error) {
		panic("row exploded")
	}
	t.Cleanup(func() { delete(ehrRows, panicUC) })

	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: panicUC, Branch: ""})
	if err != nil {
		t.Fatalf("Run(panicking row): returned a Go error, want a failed Result: %v", err)
	}
	if res.State != StateFailed || !strings.Contains(res.Detail, "row exploded") {
		t.Fatalf("Result = %+v, want failed with the panic detail", res)
	}

	// The lock must have been released: a normal run works afterwards.
	res2, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run after panic: %v (runner wedged?)", err)
	}
	if res2.State != StatePassed {
		t.Fatalf("Run after panic: state = %q, want passed (Detail=%q)", res2.State, res2.Detail)
	}
	if got := len(rn.Results()); got != 2 {
		t.Fatalf("Results() = %d rows, want 2 (the panicked run must be recorded too)", got)
	}
}

func TestStart_Busy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	runID, err := rn.Start(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if runID == "" {
		t.Fatal("Start: empty runID")
	}

	<-started // the spawned row holds the lock, blocked inside the handler

	if _, err := rn.Start(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "notcovered"}); !errors.Is(err, ErrRunInFlight) {
		t.Fatalf("busy Start: err = %v, want ErrRunInFlight", err)
	}

	close(release)
	waitIdle(t, rn)

	results := rn.Results()
	if len(results) != 1 || results[0].RunID != runID || results[0].State != StatePassed {
		t.Fatalf("Results() = %+v, want exactly 1 passed row with RunID %q", results, runID)
	}
}

// ---- Result wire tags + lane-aware branch validation ----------------------

// TestResult_JSONShape pins Result's wire shape (lowercase JSON tags) the
// UI's GET /api/runs poll consumes.
func TestResult_JSONShape(t *testing.T) {
	res := Result{RunID: "run-1", Lane: "ehr", UC: "uc01", Branch: "covered", State: StatePassed, Detail: "d"}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"runId":"run-1"`) {
		t.Fatalf("Marshal(Result) = %s, want it to contain \"runId\":\"run-1\"", s)
	}
	if strings.Contains(s, `"RunID"`) {
		t.Fatalf("Marshal(Result) = %s, want no capitalized \"RunID\" key", s)
	}
}

// TestValidateRow_LaneAwareBranch pins: uc05/uc07 take a branch
// only on the "ehr" lane; the "conformant" lane (the Da Vinci CRD/DTR/PAS
// ingress path — no branch-selecting query param exists there) takes none.
func TestValidateRow_LaneAwareBranch(t *testing.T) {
	if _, err := validateRow(Req{Lane: "conformant", UC: "uc05", Branch: "consent"}); err == nil {
		t.Fatal("validateRow(conformant, uc05, consent): want an error naming the lane")
	} else if !strings.Contains(err.Error(), "conformant uc05 takes no branch") {
		t.Fatalf("validateRow(conformant, uc05, consent) error = %v, want it to name the lane", err)
	}
	if _, err := validateRow(Req{Lane: "conformant", UC: "uc07", Branch: "hcpcs"}); err == nil {
		t.Fatal("validateRow(conformant, uc07, hcpcs): want an error naming the lane")
	} else if !strings.Contains(err.Error(), "conformant uc07 takes no branch") {
		t.Fatalf("validateRow(conformant, uc07, hcpcs) error = %v, want it to name the lane", err)
	}
	if _, err := validateRow(Req{Lane: "ehr", UC: "uc05", Branch: "consent"}); err != nil {
		t.Fatalf("validateRow(ehr, uc05, consent): unexpected error: %v", err)
	}
	if _, err := validateRow(Req{Lane: "ehr", UC: "uc07", Branch: "hcpcs"}); err != nil {
		t.Fatalf("validateRow(ehr, uc07, hcpcs): unexpected error: %v", err)
	}
	if _, err := validateRow(Req{Lane: "conformant", UC: "uc01", Branch: "covered"}); err != nil {
		t.Fatalf("validateRow(conformant, uc01, covered): unexpected error: %v", err)
	}
}

// ---- Relay drain barrier ---------------------------------------------------

// TestRun_TailFrameStampedBeforeTerminal (drain point (a)):
// a real relay.Relay is wired into the Runner against a fake observer
// fixture that withholds its ONE frame until GET /health has been fetched
// at least once — i.e. until execute's drain has genuinely begun polling,
// after the row has already completed. Once Run returns, the observer event
// must be on the bus, stamped with the run's identity, and its Seq must be
// LESS than run.finished's Seq: the tail frame precedes the terminal event.
func TestRun_TailFrameStampedBeforeTerminal(t *testing.T) {
	healthPolled := make(chan struct{})
	var healthOnce sync.Once

	obsMux := http.NewServeMux()
	obsMux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		<-healthPolled // withhold the tail frame until drain has started polling
		fmt.Fprintf(w, "id: 1\ndata: %s\n\n", `{"seq":1,"kind":"leg.originated"}`)
		fl.Flush()
		<-r.Context().Done()
	})
	obsMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthOnce.Do(func() { close(healthPolled) })
		fmt.Fprint(w, `{"events":1}`)
	})
	obsSrv := httptest.NewServer(obsMux)
	defer obsSrv.Close()

	scenarioMux := http.NewServeMux()
	scenarioMux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	scenarioSrv := httptest.NewServer(scenarioMux)
	defer scenarioSrv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rly := relay.New(obsSrv.URL+"/events", obsSrv.URL+"/health", bus, func(string, ...any) {})
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go rly.Run(relayCtx)

	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: scenarioSrv.URL}),
		Bus:    bus,
		Relay:  rly,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}

	// started, audit.unavailable (AuditURL unset), observer (the drained tail
	// frame), finished.
	events := readSSE(t, busSrv.URL+"/events", 4)
	var obsEvt, finEvt *event.Event
	for i := range events {
		switch events[i].Type {
		case event.TypeObserver:
			obsEvt = &events[i]
		case event.TypeRunFinished:
			finEvt = &events[i]
		}
	}
	if obsEvt == nil {
		t.Fatalf("no observer event found on the bus: %+v", events)
	}
	if finEvt == nil {
		t.Fatalf("no run.finished event found on the bus: %+v", events)
	}
	if obsEvt.RunID != res.RunID || obsEvt.Lane != "ehr" || obsEvt.UC != "uc01" {
		t.Errorf("observer event stamp = RunID:%q Lane:%q UC:%q, want %s/ehr/uc01", obsEvt.RunID, obsEvt.Lane, obsEvt.UC, res.RunID)
	}
	if obsEvt.Seq >= finEvt.Seq {
		t.Errorf("observer event Seq %d >= run.finished Seq %d, want the tail frame to precede the terminal event (drain point (a))", obsEvt.Seq, finEvt.Seq)
	}
}

// TestRun_DrainTimeoutDoesNotFailRun: a drain that never catches up (the
// fixture's health reports 99 events but never sends a single frame) must
// NOT fail the run — the observer stream is diagnostic, never load-bearing.
// The row's own ctx carries a 300ms deadline (drainRelay's
// context.WithTimeout(ctx, drainTimeout) takes the SHORTER of that and the
// production 5s drainTimeout), so this test never waits the full 5s.
func TestRun_DrainTimeoutDoesNotFailRun(t *testing.T) {
	obsMux := http.NewServeMux()
	obsMux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		<-r.Context().Done() // never sends a frame
	})
	obsMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"events":99}`)
	})
	obsSrv := httptest.NewServer(obsMux)
	defer obsSrv.Close()

	scenarioMux := http.NewServeMux()
	scenarioMux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	scenarioSrv := httptest.NewServer(scenarioMux)
	defer scenarioSrv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rly := relay.New(obsSrv.URL+"/events", obsSrv.URL+"/health", bus, func(string, ...any) {})
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go rly.Run(relayCtx)

	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: scenarioSrv.URL}),
		Bus:    bus,
		Relay:  rly,
	})

	runCtx, runCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer runCancel()

	res, err := rn.Run(runCtx, Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want %q (a drain timeout must never fail the run; Detail=%q)", res.State, StatePassed, res.Detail)
	}
	if !strings.Contains(res.Detail, "observer drain incomplete") {
		t.Fatalf("Result.Detail = %q, want it to contain %q", res.Detail, "observer drain incomplete")
	}

	events := readSSE(t, busSrv.URL+"/events", 3) // started, audit.unavailable, finished
	last := events[len(events)-1]
	if last.Type != event.TypeRunFinished {
		t.Fatalf("last event type = %q, want run.finished", last.Type)
	}
	if !strings.Contains(last.Detail, "observer drain incomplete") {
		t.Fatalf("run.finished Detail = %q, want it to contain %q", last.Detail, "observer drain incomplete")
	}
}

// TestRun_RowPanicWithRelayStillDrainsAndClears: the
// panicking-row posture (TestRun_RowPanicDoesNotWedgeRunner) and the relay
// drain barrier posture (TestRun_TailFrameStampedBeforeTerminal) combined —
// a real relay.Relay is wired in AND the row panics. execute's drain-then-
// clear defer (runner.go's "on EVERY exit path" comment) must still run
// during the panic's unwind before runLocked's recover converts it to a
// failed Result: the withheld tail frame must still land on the bus,
// stamped with the panicking run's identity, strictly before run.failed —
// and the runner must still be usable afterward.
func TestRun_RowPanicWithRelayStillDrainsAndClears(t *testing.T) {
	const panicUC = "uc96"
	ehrRows[panicUC] = func(rn *Runner, branch string) (string, error) {
		panic("row exploded")
	}
	t.Cleanup(func() { delete(ehrRows, panicUC) })

	healthPolled := make(chan struct{})
	var healthOnce sync.Once

	obsMux := http.NewServeMux()
	obsMux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		<-healthPolled // withhold the tail frame until drain has started polling
		fmt.Fprintf(w, "id: 1\ndata: %s\n\n", `{"seq":1,"kind":"leg.originated"}`)
		fl.Flush()
		<-r.Context().Done()
	})
	obsMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthOnce.Do(func() { close(healthPolled) })
		fmt.Fprint(w, `{"events":1}`)
	})
	obsSrv := httptest.NewServer(obsMux)
	defer obsSrv.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rly := relay.New(obsSrv.URL+"/events", obsSrv.URL+"/health", bus, func(string, ...any) {})
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go rly.Run(relayCtx)

	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
		Relay:  rly,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: panicUC, Branch: ""})
	if err != nil {
		t.Fatalf("Run(panicking row): returned a Go error, want a failed Result: %v", err)
	}
	if res.State != StateFailed || !strings.Contains(res.Detail, "row exploded") {
		t.Fatalf("Result = %+v, want failed with the panic detail", res)
	}

	// started, audit.unavailable (AuditURL unset), observer (the drained tail
	// frame — proves the panic's unwind still ran execute's drain-then-clear
	// defer), failed.
	events := readSSE(t, busSrv.URL+"/events", 4)
	var obsEvt, failEvt *event.Event
	for i := range events {
		switch events[i].Type {
		case event.TypeObserver:
			obsEvt = &events[i]
		case event.TypeRunFailed:
			failEvt = &events[i]
		}
	}
	if obsEvt == nil {
		t.Fatalf("no observer event found on the bus: %+v", events)
	}
	if failEvt == nil {
		t.Fatalf("no run.failed event found on the bus: %+v", events)
	}
	if obsEvt.RunID != res.RunID || obsEvt.Lane != "ehr" || obsEvt.UC != panicUC {
		t.Errorf("observer event stamp = RunID:%q Lane:%q UC:%q, want %s/ehr/%s", obsEvt.RunID, obsEvt.Lane, obsEvt.UC, res.RunID, panicUC)
	}
	if obsEvt.Seq >= failEvt.Seq {
		t.Errorf("observer event Seq %d >= run.failed Seq %d, want the tail frame to precede the terminal event even on the panic path", obsEvt.Seq, failEvt.Seq)
	}

	// The lock must have been released and the stamp cleared: a normal run
	// works afterwards (mirrors TestRun_RowPanicDoesNotWedgeRunner).
	res2, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run after panic: %v (runner wedged?)", err)
	}
	if res2.State != StatePassed {
		t.Fatalf("Run after panic: state = %q, want passed (Detail=%q)", res2.State, res2.Detail)
	}
}

// ---- Unique run ids + History Sink -----------------------------------------

var runIDPattern = regexp.MustCompile(`^run-\d+-\d+$`)

// fakeSink is a recording runner.Sink: it appends every Result it sees, and
// (per row 2's ordering pin) captures bus.Since(0) AT CALL TIME so the test
// can assert the run's terminal bus event was already present when the Sink
// fired.
type fakeSink struct {
	bus *event.Bus

	mu          sync.Mutex
	results     []Result
	sinceAtCall [][]event.Event
}

func (f *fakeSink) RunCompleted(res Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, res)
	f.sinceAtCall = append(f.sinceAtCall, f.bus.Since(0))
}

// TestNextRunID_UniquePerRunAndAcrossRestarts pins the default NewRunID shape
// "run-<millis>-<seq>": two runs on the same Runner (fixed clock)
// get distinct ids matching the pattern, and a SECOND Runner instance
// (simulating a daemon restart) with a LATER injected clock never collides
// with the first Runner's ids — run history keys on RunID across restarts.
func TestNextRunID_UniquePerRunAndAcrossRestarts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
		Now:    fixedClock,
	})

	res1, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	res2, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if !runIDPattern.MatchString(res1.RunID) {
		t.Fatalf("run id %q does not match %s", res1.RunID, runIDPattern)
	}
	if !runIDPattern.MatchString(res2.RunID) {
		t.Fatalf("run id %q does not match %s", res2.RunID, runIDPattern)
	}
	if res1.RunID == res2.RunID {
		t.Fatalf("two runs on the same Runner produced the same id %q", res1.RunID)
	}

	laterClock := func() time.Time { return fixedClock().Add(time.Hour) }
	bus2 := event.NewBus(laterClock)
	rn2 := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus2,
		Now:    laterClock,
	})
	res3, err := rn2.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run 3 (second Runner instance): %v", err)
	}
	if res3.RunID == res1.RunID || res3.RunID == res2.RunID {
		t.Fatalf("a second Runner instance (restart) produced a colliding run id %q", res3.RunID)
	}
}

// TestRun_SinkCalledAfterTerminalWithFinalResult: a passing row's Sink call
// carries the final Result, and — at the moment the Sink fires — the run's
// run.finished event is already on the bus (the "story complete before
// capture" ordering the Recorder depends on).
func TestRun_SinkCalledAfterTerminalWithFinalResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	sink := &fakeSink{bus: bus}
	rn := New(Config{
		Driver:  scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:     bus,
		History: sink,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.results) != 1 {
		t.Fatalf("Sink saw %d Results, want exactly 1: %+v", len(sink.results), sink.results)
	}
	if sink.results[0] != res {
		t.Fatalf("Sink's Result = %+v, want the exact final Result %+v", sink.results[0], res)
	}

	found := false
	for _, e := range sink.sinceAtCall[0] {
		if e.Type == event.TypeRunFinished && e.RunID == res.RunID {
			found = true
		}
	}
	if !found {
		t.Fatalf("run.finished was not yet on the bus when the Sink fired: %+v", sink.sinceAtCall[0])
	}
}

// TestRun_SinkCalledOnFailureAndPanic: the Sink fires on a failed row and on
// a panicking row (State failed both times), and the runner remains usable
// afterward — a successful Run following the panic still reaches the Sink.
func TestRun_SinkCalledOnFailureAndPanic(t *testing.T) {
	const panicUC = "uc97"
	ehrRows[panicUC] = func(rn *Runner, branch string) (string, error) {
		panic("row exploded")
	}
	t.Cleanup(func() { delete(ehrRows, panicUC) })

	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"boom: downstream unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	sink := &fakeSink{bus: bus}
	rn := New(Config{
		Driver:  scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:     bus,
		History: sink,
	})

	res1, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run (failing row): %v", err)
	}
	if res1.State != StateFailed {
		t.Fatalf("Result.State = %q, want failed", res1.State)
	}

	res2, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: panicUC, Branch: ""})
	if err != nil {
		t.Fatalf("Run (panicking row): %v", err)
	}
	if res2.State != StateFailed {
		t.Fatalf("Result.State = %q, want failed", res2.State)
	}

	res3, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run after panic: %v (runner wedged?)", err)
	}
	if res3.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res3.State, res3.Detail)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.results) != 3 {
		t.Fatalf("Sink saw %d Results, want 3: %+v", len(sink.results), sink.results)
	}
	if sink.results[0].State != StateFailed || sink.results[0].RunID != res1.RunID {
		t.Fatalf("Sink's first Result = %+v, want failed/%s", sink.results[0], res1.RunID)
	}
	if sink.results[1].State != StateFailed || sink.results[1].RunID != res2.RunID {
		t.Fatalf("Sink's second Result = %+v, want failed/%s", sink.results[1], res2.RunID)
	}
	if sink.results[2].State != StatePassed || sink.results[2].RunID != res3.RunID {
		t.Fatalf("Sink's third Result = %+v, want passed/%s", sink.results[2], res3.RunID)
	}
}

// panicSink is a runner.Sink whose RunCompleted always panics — standing in
// for a misconfigured runhistory.Recorder (e.g. nil logf called after a
// failed Save) to prove the runner isolates a panicking Sink the same way it
// isolates a panicking row.
type panicSink struct{}

func (panicSink) RunCompleted(Result) { panic("sink exploded") }

// TestRun_PanickingSinkDoesNotWedgeRunner: a Sink whose RunCompleted panics
// must not take runLocked's defer down with it — Run still returns the
// row's own (correct, passed) Result, and the sequential lock is still
// released so a SECOND Run immediately after succeeds. RED against HEAD
// d733bf0: runLocked's defer calls r.cfg.History.RunCompleted(res) with no
// recover of its own, above the plain r.mu.Unlock() statement — a panicking
// Sink propagates out of the defer, skipping Unlock, and every later Run
// permanently sees ErrRunInFlight.
func TestRun_PanickingSinkDoesNotWedgeRunner(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver:  scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:     bus,
		History: panicSink{},
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run (panicking Sink): returned a Go error, want the row's own Result: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed despite the panicking Sink (Detail=%q)", res.State, res.Detail)
	}

	// The lock must have been released: a second run works immediately
	// after, rather than permanently reading ErrRunInFlight.
	res2, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run after panicking Sink: %v (runner wedged?)", err)
	}
	if res2.State != StatePassed {
		t.Fatalf("Run after panicking Sink: state = %q, want passed (Detail=%q)", res2.State, res2.Detail)
	}
}

// ---- Req + freeform row + watch session ------------------------------------

// controllableObs is a test fixture standing in for a gateway's loopback
// observer stream (kit/relay's client target): the test pushes frames (with
// explicit seq ids) onto its stream at will via pushFrame, and /health
// reports whatever count pushFrame last set — letting a test control
// exactly when relay.Drain considers itself caught up.
type controllableObs struct {
	srv    *httptest.Server
	frames chan string
	health atomic.Uint64
}

func newControllableObs(t *testing.T) *controllableObs {
	t.Helper()
	o := &controllableObs{frames: make(chan string, 32)}
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		for {
			select {
			case frame := <-o.frames:
				fmt.Fprint(w, frame)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"events":%d}`, o.health.Load())
	})
	o.srv = httptest.NewServer(mux)
	return o
}

// pushFrame sends one observer frame carrying seq and advances the
// fixture's health count to match, so a caller's Drain() sees itself caught
// up once the frame is actually relayed onto the bus.
func (o *controllableObs) pushFrame(seq uint64) {
	o.health.Store(seq)
	o.frames <- fmt.Sprintf("id: %d\ndata: {\"seq\":%d,\"kind\":\"leg.originated\"}\n\n", seq, seq)
}

func (o *controllableObs) close() { o.srv.Close() }

// newTestRelay wires a relay.Relay against obs and starts its reconnect loop
// under ctx, returning the relay. Mirrors the relay drain-fixture pattern
// (TestRun_TailFrameStampedBeforeTerminal) factored out for the watch rows.
func newTestRelay(ctx context.Context, obs *controllableObs, bus *event.Bus) *relay.Relay {
	rly := relay.New(obs.srv.URL+"/events", obs.srv.URL+"/health", bus, func(string, ...any) {})
	go rly.Run(ctx)
	return rly
}

// ---- Row 1: freeform row plumbing ------------------------------------------

// TestRun_Freeform: the "freeform" row POSTs /scenario/dispatch with the
// caller-named member's body, and decodes the SAME uc03Resp wire shape the
// real gateway's handleDispatch answers with (row-truth verified against
// gateway/engine/originate_dispatch_test.go — paRequired/authNumber, not a
// generic {"ok":true}).
func TestRun_Freeform(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/dispatch", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"member":"MBR-X"}` {
			t.Errorf("request body = %s, want {\"member\":\"MBR-X\"}", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"paRequired":true,"authNumber":"AUTH-FREEFORM-1","validUntil":"2027-01-01"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "freeform", Member: "MBR-X"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}
	if !strings.Contains(res.Detail, "AUTH-FREEFORM-1") {
		t.Errorf("Result.Detail = %q, want it to contain the auth number", res.Detail)
	}
}

// TestRun_FreeformFailure: a non-200 /scenario/dispatch response fails the
// row, carrying the body's detail in the error (crib of ehrUC03's response
// handling: ehrScenario already does this status/excerpt check).
func TestRun_FreeformFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/dispatch", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":"no coverage on file for member"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "freeform", Member: "MBR-X"})
	if err != nil {
		t.Fatalf("Run returned a Go error (want a failed Result instead): %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("Result.State = %q, want failed", res.State)
	}
	if !strings.Contains(res.Detail, "no coverage on file for member") {
		t.Errorf("Result.Detail = %q, want it to contain the response body's detail", res.Detail)
	}
}

// TestRun_FreeformMemberUnknown_ProviderSide: a freeform dispatch whose
// PROVIDER-side gateway rejects the caller-named member outright — status
// 400, body {"error":"unknown member"} — is gateway/engine/
// originate_homeoxygen.go's own ResolvePatient guard (pinned live by
// gateway/engine/originate_dispatch_test.go's TestHandleDispatch_UnknownMember,
// row 4: {"member":"MBR-NOPE"} → 400 "unknown member"). The
// failure Detail must name the member-coverage constraint in plain
// language, never relay this bare 400.
func TestRun_FreeformMemberUnknown_ProviderSide(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/dispatch", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"unknown member"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "freeform", Member: "MBR-NOT-COVERED"})
	if err != nil {
		t.Fatalf("Run returned a Go error (want a failed Result instead): %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("Result.State = %q, want failed", res.State)
	}
	if !strings.Contains(res.Detail, freeformProviderUnknownMemberSentence) {
		t.Errorf("Result.Detail = %q, want it to contain the named provider-side sentence %q", res.Detail, freeformProviderUnknownMemberSentence)
	}
}

// TestRun_FreeformPayerSide_RelayedVerbatim: since shn-gateway v0.28.0 the
// payer's real application answer is relayed verbatim through the sealed message
// frame, so a freeform dispatch whose PAYER counterparty rejects the caller-named
// member surfaces the payer's genuine status + OperationOutcome body — the runner
// no longer synthesizes a likely-cause payer-routing sentence (that relabel was a
// workaround for the payload-blind Hub discarding the payer's reason, now retired
// with the frame). The provider-side sentence must NOT be applied here (this is a
// payer answer, not the operator's own connected-system guard).
func TestRun_FreeformPayerSide_RelayedVerbatim(t *testing.T) {
	payerBody := `{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"not-found","diagnostics":"member not found"}]}`
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/dispatch", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(payerBody))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "freeform", Member: "MBR-NOT-COVERED"})
	if err != nil {
		t.Fatalf("Run returned a Go error (want a failed Result instead): %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("Result.State = %q, want failed", res.State)
	}
	if !strings.Contains(res.Detail, "member not found") {
		t.Errorf("Result.Detail = %q, want the payer's verbatim OperationOutcome body passed through", res.Detail)
	}
	if strings.Contains(res.Detail, freeformProviderUnknownMemberSentence) {
		t.Errorf("Result.Detail = %q, must NOT apply the provider-side sentence to a payer answer", res.Detail)
	}
}

// TestRun_FreeformPolicyDenial_NotRelabeled is the policy-denial
// regression row: a genuine policy denial — gateway/engine/pas_tail.go and
// originate.go both answer 502 {"error":"preauthorization not approved"}
// for an adjudicated deny — shares freeform's payer-side status code (502)
// with the unknown-member shape above, but MUST NOT be relabeled as the
// member-coverage constraint (it is a real, distinct substrate outcome, not
// an unrecognized member). Pins the mapping's conservativeness: matching on
// status 502 alone is not enough — the "hub routing failed" body text is
// the discriminating token.
func TestRun_FreeformPolicyDenial_NotRelabeled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/dispatch", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":"preauthorization not approved"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "freeform", Member: "MBR-X"})
	if err != nil {
		t.Fatalf("Run returned a Go error (want a failed Result instead): %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("Result.State = %q, want failed", res.State)
	}
	if !strings.Contains(res.Detail, "preauthorization not approved") {
		t.Errorf("Result.Detail = %q, want the raw policy-denial detail passed through unchanged", res.Detail)
	}
	if strings.Contains(res.Detail, freeformProviderUnknownMemberSentence) {
		t.Errorf("Result.Detail = %q, must NOT contain the provider-side unknown-member sentence for a non-member failure", res.Detail)
	}
}

// ---- Row 2: validateRow extensions (freeform/member/external) -------------

func TestValidateRow_FreeformMemberAndExternal(t *testing.T) {
	cases := []struct {
		name      string
		req       Req
		wantValid bool
		wantErr   string // substring required in the error when wantValid is false; "" = any error
	}{
		{"freeform valid", Req{Lane: "ehr", UC: "freeform", Branch: "", Member: "MBR-X"}, true, ""},
		{"freeform empty member", Req{Lane: "ehr", UC: "freeform", Branch: "", Member: ""}, false, "member is required"},
		{"freeform wrong lane", Req{Lane: "conformant", UC: "freeform", Branch: "", Member: "MBR-X"}, false, ""},
		{"freeform with a branch", Req{Lane: "ehr", UC: "freeform", Branch: "covered", Member: "MBR-X"}, false, ""},
		{"member on non-freeform uc03", Req{Lane: "ehr", UC: "uc03", Branch: "", Member: "MBR-X"}, false, "member is only valid for freeform"},
		{"external rejected (watch-only)", Req{Lane: "conformant", UC: "external", Branch: "", Member: ""}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row, err := validateRow(tc.req)
			if tc.wantValid {
				if err != nil {
					t.Fatalf("validateRow(%+v): unexpected error: %v", tc.req, err)
				}
				if row == nil {
					t.Fatalf("validateRow(%+v): nil row on success", tc.req)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateRow(%+v): want an error", tc.req)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateRow(%+v) error = %v, want it to contain %q", tc.req, err, tc.wantErr)
			}
		})
	}
}

// ---- Row 3: watch lifecycle ------------------------------------------------

// TestWatch_Lifecycle: StartWatch mints a run.started stamped external/
// conformant, the audit.unavailable parity emit fires (no AuditURL
// configured), a partner-originated frame relays stamped with the watch's
// identity while it is open, StopWatch produces run.finished + a Result
// appended to Results() + exactly one History Sink call, and a frame after
// StopWatch relays unstamped (the stamp was cleared).
func TestWatch_Lifecycle(t *testing.T) {
	obs := newControllableObs(t)
	defer obs.close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	rly := newTestRelay(relayCtx, obs, bus)

	sink := &fakeSink{bus: bus}
	rn := New(Config{
		Driver:  scenariodriver.New(scenariodriver.Config{}),
		Bus:     bus,
		Relay:   rly,
		History: sink,
	})

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	runID, err := rn.StartWatch(watchCtx)
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	if !runIDPattern.MatchString(runID) {
		t.Fatalf("StartWatch runID = %q, does not match %s", runID, runIDPattern)
	}

	startEvents := readSSE(t, busSrv.URL+"/events", 2) // started, audit.unavailable (AuditURL unset)
	if startEvents[0].Type != event.TypeRunStarted || startEvents[0].RunID != runID || startEvents[0].Lane != watchLane || startEvents[0].UC != watchUC {
		t.Fatalf("events[0] = %+v, want run.started stamped %s/%s/%s", startEvents[0], runID, watchLane, watchUC)
	}
	if startEvents[1].Type != event.TypeAuditUnavailable || startEvents[1].RunID != runID {
		t.Fatalf("events[1] = %+v, want audit.unavailable stamped with the watch (parity with a run)", startEvents[1])
	}

	// A partner-originated frame arrives while the watch is open: it must
	// relay stamped with the watch's identity.
	obs.pushFrame(1)
	obsEvents := readSSE(t, busSrv.URL+"/events", 3)
	obsEvt := obsEvents[2]
	if obsEvt.Type != event.TypeObserver || obsEvt.RunID != runID || obsEvt.Lane != watchLane || obsEvt.UC != watchUC {
		t.Fatalf("observer event = %+v, want stamped with the watch %s/%s/%s", obsEvt, runID, watchLane, watchUC)
	}

	res, err := rn.StopWatch()
	if err != nil {
		t.Fatalf("StopWatch: %v", err)
	}
	if res.RunID != runID || res.State != StatePassed || res.Lane != watchLane || res.UC != watchUC {
		t.Fatalf("StopWatch Result = %+v, unexpected", res)
	}
	if res.Detail != "external activity window closed" {
		t.Fatalf("StopWatch Result.Detail = %q, want the fixed detail", res.Detail)
	}

	results := rn.Results()
	if len(results) != 1 || results[0].RunID != runID {
		t.Fatalf("Results() = %+v, want exactly 1 row with RunID %q", results, runID)
	}

	finishedEvents := readSSE(t, busSrv.URL+"/events", 4)
	last := finishedEvents[3]
	if last.Type != event.TypeRunFinished || last.RunID != runID {
		t.Fatalf("last event = %+v, want run.finished stamped with the watch", last)
	}

	sink.mu.Lock()
	if len(sink.results) != 1 || sink.results[0].RunID != runID {
		t.Fatalf("History Sink saw %+v, want exactly 1 call with RunID %q", sink.results, runID)
	}
	sink.mu.Unlock()

	// A post-stop frame relays unstamped: ClearStamp already ran.
	obs.pushFrame(2)
	afterEvents := readSSE(t, busSrv.URL+"/events", 5)
	post := afterEvents[4]
	if post.Type != event.TypeObserver || post.RunID != "" {
		t.Fatalf("post-stop observer event = %+v, want unstamped (empty RunID)", post)
	}
}

// ---- Row 4: drain-before-terminal parity for a watch ----------------------

// TestWatch_DrainBeforeTerminal reuses the relay drain-fixture trick
// (TestRun_TailFrameStampedBeforeTerminal) for a watch: a tail frame that
// lands only once StopWatch's drain has genuinely begun polling must relay
// BEFORE run.finished — its Seq on the bus must be less than run.finished's.
func TestWatch_DrainBeforeTerminal(t *testing.T) {
	healthPolled := make(chan struct{})
	var healthOnce sync.Once

	obsMux := http.NewServeMux()
	obsMux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		<-healthPolled // withhold the tail frame until drain has started polling
		fmt.Fprintf(w, "id: 1\ndata: %s\n\n", `{"seq":1,"kind":"leg.originated"}`)
		fl.Flush()
		<-r.Context().Done()
	})
	obsMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthOnce.Do(func() { close(healthPolled) })
		fmt.Fprint(w, `{"events":1}`)
	})
	obsSrv := httptest.NewServer(obsMux)
	defer obsSrv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rly := relay.New(obsSrv.URL+"/events", obsSrv.URL+"/health", bus, func(string, ...any) {})
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go rly.Run(relayCtx)

	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{}),
		Bus:    bus,
		Relay:  rly,
	})

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	runID, err := rn.StartWatch(watchCtx)
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}

	res, err := rn.StopWatch()
	if err != nil {
		t.Fatalf("StopWatch: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("StopWatch Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}

	events := readSSE(t, busSrv.URL+"/events", 4) // started, audit.unavailable, observer (drained tail), finished
	var obsEvt, finEvt *event.Event
	for i := range events {
		switch events[i].Type {
		case event.TypeObserver:
			obsEvt = &events[i]
		case event.TypeRunFinished:
			finEvt = &events[i]
		}
	}
	if obsEvt == nil {
		t.Fatalf("no observer event found on the bus: %+v", events)
	}
	if finEvt == nil {
		t.Fatalf("no run.finished event found on the bus: %+v", events)
	}
	if obsEvt.RunID != runID || obsEvt.Lane != watchLane || obsEvt.UC != watchUC {
		t.Errorf("observer event stamp = RunID:%q Lane:%q UC:%q, want %s/%s/%s", obsEvt.RunID, obsEvt.Lane, obsEvt.UC, runID, watchLane, watchUC)
	}
	if obsEvt.Seq >= finEvt.Seq {
		t.Errorf("observer event Seq %d >= run.finished Seq %d, want the tail frame to precede the terminal event (drain point (a))", obsEvt.Seq, finEvt.Seq)
	}
}

// ---- Row 5: mutual exclusion ------------------------------------------------

func TestWatch_MutualExclusion(t *testing.T) {
	t.Run("watch then run", func(t *testing.T) {
		bus := event.NewBus(fixedClock)
		rn := New(Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus})
		watchCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if _, err := rn.StartWatch(watchCtx); err != nil {
			t.Fatalf("StartWatch: %v", err)
		}
		if _, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"}); !errors.Is(err, ErrRunInFlight) {
			t.Fatalf("Run while watching: err = %v, want ErrRunInFlight", err)
		}
		if _, err := rn.StopWatch(); err != nil {
			t.Fatalf("StopWatch: %v", err)
		}
	})

	t.Run("run then watch", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		mux := http.NewServeMux()
		mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		bus := event.NewBus(fixedClock)
		rn := New(Config{
			Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
			Bus:    bus,
		})

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
		}()
		<-started // the run holds the lock, blocked inside the row

		if _, err := rn.StartWatch(context.Background()); !errors.Is(err, ErrRunInFlight) {
			t.Fatalf("StartWatch while a run is in flight: err = %v, want ErrRunInFlight", err)
		}

		close(release)
		wg.Wait()
	})

	t.Run("watch twice", func(t *testing.T) {
		bus := event.NewBus(fixedClock)
		rn := New(Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus})
		watchCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if _, err := rn.StartWatch(watchCtx); err != nil {
			t.Fatalf("StartWatch: %v", err)
		}
		if _, err := rn.StartWatch(context.Background()); !errors.Is(err, ErrRunInFlight) {
			t.Fatalf("second StartWatch: err = %v, want ErrRunInFlight", err)
		}
		if _, err := rn.StopWatch(); err != nil {
			t.Fatalf("StopWatch: %v", err)
		}
	})

	t.Run("stop without a watch", func(t *testing.T) {
		bus := event.NewBus(fixedClock)
		rn := New(Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus})
		if _, err := rn.StopWatch(); !errors.Is(err, ErrNoWatch) {
			t.Fatalf("StopWatch with no watch open: err = %v, want ErrNoWatch", err)
		}
	})

	// Two concurrent StopWatch callers can both read the same non-nil
	// r.watch before finishWatch nils it: both
	// proceed past the nil check and both block on <-w.done. Without the
	// fix, the finalize goroutine sent its one Result to the cap-1 done
	// channel and never closed it — the loser of the race blocked forever
	// (a leaked DELETE /api/watch handler). Bound the whole race with a
	// watchdog: exactly one caller gets the Result (err nil), the other gets
	// ErrNoWatch, and neither ever hangs.
	t.Run("two concurrent StopWatch callers race one watch: exactly one gets the Result, the other ErrNoWatch, neither hangs", func(t *testing.T) {
		bus := event.NewBus(fixedClock)
		rn := New(Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus})
		watchCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if _, err := rn.StartWatch(watchCtx); err != nil {
			t.Fatalf("StartWatch: %v", err)
		}

		type outcome struct {
			res Result
			err error
		}
		results := make(chan outcome, 2)
		for i := 0; i < 2; i++ {
			go func() {
				res, err := rn.StopWatch()
				results <- outcome{res, err}
			}()
		}

		var got []outcome
		for i := 0; i < 2; i++ {
			select {
			case o := <-results:
				got = append(got, o)
			case <-time.After(5 * time.Second):
				t.Fatal("a racing StopWatch call did not return — runner wedged (double-send/never-close on w.done?)")
			}
		}

		var oks, noWatches int
		for _, o := range got {
			switch {
			case o.err == nil:
				oks++
				if o.res.RunID == "" || o.res.State != StatePassed {
					t.Errorf("winning StopWatch Result = %+v, want a passed Result with a RunID", o.res)
				}
			case errors.Is(o.err, ErrNoWatch):
				noWatches++
			default:
				t.Errorf("racing StopWatch returned unexpected error: %v", o.err)
			}
		}
		if oks != 1 || noWatches != 1 {
			t.Fatalf("got %d nil-error outcomes and %d ErrNoWatch outcomes, want exactly 1 of each", oks, noWatches)
		}
	})
}

// ---- Row 6: ctx-driven self-finalize ---------------------------------------

// TestWatch_CtxCancelSelfFinalizes: canceling the lifetime ctx passed to
// StartWatch self-finalizes the watch (run.finished emitted, lock released —
// a subsequent Run succeeds) exactly like an explicit StopWatch, EXCEPT a
// StopWatch call afterward answers ErrNoWatch (the finalize
// path itself nils the watch slot). The cancel-path tail still drains under
// the fresh short-timeout ctx: the withheld frame must still be
// on the bus, stamped.
func TestWatch_CtxCancelSelfFinalizes(t *testing.T) {
	scenarioMux := http.NewServeMux()
	scenarioMux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	scenarioSrv := httptest.NewServer(scenarioMux)
	defer scenarioSrv.Close()

	healthPolled := make(chan struct{})
	var healthOnce sync.Once
	obsMux := http.NewServeMux()
	obsMux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		<-healthPolled
		fmt.Fprintf(w, "id: 1\ndata: %s\n\n", `{"seq":1,"kind":"leg.originated"}`)
		fl.Flush()
		<-r.Context().Done()
	})
	obsMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthOnce.Do(func() { close(healthPolled) })
		fmt.Fprint(w, `{"events":1}`)
	})
	obsSrv := httptest.NewServer(obsMux)
	defer obsSrv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rly := relay.New(obsSrv.URL+"/events", obsSrv.URL+"/health", bus, func(string, ...any) {})
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go rly.Run(relayCtx)

	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: scenarioSrv.URL}),
		Bus:    bus,
		Relay:  rly,
	})

	watchCtx, watchCancel := context.WithCancel(context.Background())
	runID, err := rn.StartWatch(watchCtx)
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}

	watchCancel() // simulate daemon shutdown / lifetime ctx death

	waitIdle(t, rn) // the finalize goroutine must release the lock

	results := rn.Results()
	if len(results) != 1 || results[0].RunID != runID || results[0].State != StatePassed {
		t.Fatalf("Results() after ctx-cancel finalize = %+v, want exactly 1 passed row with RunID %q", results, runID)
	}

	events := readSSE(t, busSrv.URL+"/events", 4) // started, audit.unavailable, observer, finished
	var obsEvt *event.Event
	for i := range events {
		if events[i].Type == event.TypeObserver {
			obsEvt = &events[i]
		}
	}
	if obsEvt == nil || obsEvt.RunID != runID {
		t.Fatalf("observer event = %+v, want it stamped with %q (the cancel-path tail must still drain)", obsEvt, runID)
	}

	// The finalize path itself nils the watch slot: StopWatch afterward must
	// answer ErrNoWatch, never the stale buffered Result.
	if _, err := rn.StopWatch(); !errors.Is(err, ErrNoWatch) {
		t.Fatalf("StopWatch after ctx-cancel finalize: err = %v, want ErrNoWatch", err)
	}

	// A subsequent Run succeeds: the lock was released.
	res2, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run after ctx-cancel finalize: %v (runner wedged?)", err)
	}
	if res2.State != StatePassed {
		t.Fatalf("Run after ctx-cancel finalize: state = %q, want passed (Detail=%q)", res2.State, res2.Detail)
	}
}

// ---- Row 7: audit merge parity + load-bearing pre-fetch --------------------

// TestWatch_AuditMergeParity: a watch window brackets audit events exactly
// as a run does — pre-fetch high-water mark, then post-fetch emitting one
// TypeAudit event per new record, all stamped with the watch's identity.
func TestWatch_AuditMergeParity(t *testing.T) {
	audit := newFakeAudit(t, [][]int{{1, 2}, {1, 2, 3, 4}}) // grows by 2 between pre/post
	defer audit.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rn := New(Config{
		Driver:   scenariodriver.New(scenariodriver.Config{}),
		Bus:      bus,
		AuditURL: audit.URL,
	})

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	runID, err := rn.StartWatch(watchCtx)
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}

	res, err := rn.StopWatch()
	if err != nil {
		t.Fatalf("StopWatch: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("StopWatch Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}

	events := readSSE(t, busSrv.URL+"/events", 4) // started, audit, audit, finished
	if events[0].Type != event.TypeRunStarted || events[0].RunID != runID {
		t.Fatalf("events[0] = %+v, want run.started for the watch", events[0])
	}
	for i := 1; i <= 2; i++ {
		if events[i].Type != event.TypeAudit || events[i].RunID != runID {
			t.Fatalf("events[%d] = %+v, want an audit event stamped with the watch", i, events[i])
		}
	}
	if events[3].Type != event.TypeRunFinished || events[3].RunID != runID {
		t.Fatalf("events[3] = %+v, want run.finished for the watch", events[3])
	}
}

// TestWatch_AuditPreFetchFailureNoWatchStarted (load-bearing):
// a pre-fetch failure (broken Audit Plane) makes StartWatch return the
// error, with NOTHING emitted (no run.started, no stamp) and the lock
// released — a subsequent Run succeeds.
func TestWatch_AuditPreFetchFailureNoWatchStarted(t *testing.T) {
	badAudit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badAudit.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rn := New(Config{
		Driver:   scenariodriver.New(scenariodriver.Config{}),
		Bus:      bus,
		AuditURL: badAudit.URL,
	})

	if _, err := rn.StartWatch(context.Background()); err == nil {
		t.Fatal("StartWatch: want an error when the audit pre-fetch fails")
	}

	if n := busEventCount(t, busSrv.URL); n != 0 {
		t.Fatalf("bus events = %d, want 0 (a failed pre-fetch must emit nothing — no run.started, no stamp)", n)
	}

	// The lock must have been released: a subsequent Run succeeds. Point the
	// Runner at a working scenario stub and a clean (unset) AuditURL for
	// this follow-up call — white-box mutation of cfg, mirroring this file's
	// existing waitIdle helper's same-package access.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	rn.cfg.Driver = scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL})
	rn.cfg.AuditURL = ""

	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run after failed StartWatch: %v (runner wedged?)", err)
	}
	if res.State != StatePassed {
		t.Fatalf("Run after failed StartWatch: state = %q, want passed (Detail=%q)", res.State, res.Detail)
	}
}

// TestWatch_AuditPostFetchFailure (a deviation from the execute path that
// needs its own pin here):
// unlike execute's row (whose own failure is known before the post-fetch
// ever runs), a watch's post-fetch lives entirely inside finishWatch's tail
// — by the time it can fail, run.started (and possibly observer frames)
// are already on the bus, stamped. The window still finalizes cleanly: a
// failed Result, run.failed emitted, the lock released (a subsequent Run
// succeeds), and the History Sink still called exactly once — a failed
// window is still a COMPLETED window, and best-effort history capture
// applies to it exactly as to any other terminal Result.
func TestWatch_AuditPostFetchFailure(t *testing.T) {
	var calls atomic.Int32
	audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 { // pre-fetch (StartWatch): succeed
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]auditread.Record{})
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // post-fetch (finishWatch): fail
	}))
	defer audit.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	sink := &fakeSink{bus: bus}
	rn := New(Config{
		Driver:   scenariodriver.New(scenariodriver.Config{}),
		Bus:      bus,
		AuditURL: audit.URL,
		History:  sink,
	})

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	runID, err := rn.StartWatch(watchCtx)
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}

	res, err := rn.StopWatch()
	if err != nil {
		t.Fatalf("StopWatch: %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("StopWatch Result.State = %q, want %q (audit post-fetch failed)", res.State, StateFailed)
	}
	if !strings.Contains(res.Detail, "audit read failed") {
		t.Fatalf("StopWatch Result.Detail = %q, want it to mention the audit read failure", res.Detail)
	}

	events := readSSE(t, busSrv.URL+"/events", 2) // started, failed (no audit.unavailable — AuditURL is set; no run.finished)
	last := events[len(events)-1]
	if last.Type != event.TypeRunFailed || last.RunID != runID {
		t.Fatalf("last event = %+v, want run.failed stamped with the watch", last)
	}

	sink.mu.Lock()
	if len(sink.results) != 1 || sink.results[0].RunID != runID || sink.results[0].State != StateFailed {
		t.Fatalf("History Sink saw %+v, want exactly 1 failed call with RunID %q", sink.results, runID)
	}
	sink.mu.Unlock()

	// The lock must have been released: a subsequent Run succeeds. Point the
	// Runner at a working scenario stub and a clean (unset) AuditURL for
	// this follow-up call — white-box mutation of cfg, mirroring
	// TestWatch_AuditPreFetchFailureNoWatchStarted's same pattern.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	rn.cfg.Driver = scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL})
	rn.cfg.AuditURL = ""

	res2, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run after failed watch post-fetch: %v (runner wedged?)", err)
	}
	if res2.State != StatePassed {
		t.Fatalf("Run after failed watch post-fetch: state = %q, want passed (Detail=%q)", res2.State, res2.Detail)
	}
}

// ---- Row 8: post-terminal frame drops as ambient --------------------------

// TestWatch_PostTerminalFrameDropsAsAmbient: a frame is held back on the
// observer connection until AFTER StopWatch's run.finished has already been
// observed on the bus. The health fixture never reports it as emitted, so
// Drain does not wait for it — StopWatch returns promptly. Releasing the
// frame afterward proves ClearStamp already ran: the frame relays UNSTAMPED,
// never landing inside the watch's now-closed history Record.
func TestWatch_PostTerminalFrameDropsAsAmbient(t *testing.T) {
	release := make(chan struct{})

	obsMux := http.NewServeMux()
	obsMux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		<-release // withhold the frame until the test explicitly releases it
		fmt.Fprintf(w, "id: 1\ndata: %s\n\n", `{"seq":1,"kind":"leg.originated"}`)
		fl.Flush()
		<-r.Context().Done()
	})
	obsMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"events":0}`) // never reports the held-back frame — Drain must not wait for it
	})
	obsSrv := httptest.NewServer(obsMux)
	defer obsSrv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	rly := relay.New(obsSrv.URL+"/events", obsSrv.URL+"/health", bus, func(string, ...any) {})
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go rly.Run(relayCtx)

	sink := &fakeSink{bus: bus}
	rn := New(Config{
		Driver:  scenariodriver.New(scenariodriver.Config{}),
		Bus:     bus,
		Relay:   rly,
		History: sink,
	})

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	runID, err := rn.StartWatch(watchCtx)
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}

	res, err := rn.StopWatch()
	if err != nil {
		t.Fatalf("StopWatch: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("StopWatch Result.State = %q, want passed (Detail=%q)", res.State, res.Detail)
	}

	// run.finished is already observed on the bus (StopWatch returned).
	events := readSSE(t, busSrv.URL+"/events", 3) // started, audit.unavailable, finished
	last := events[2]
	if last.Type != event.TypeRunFinished || last.RunID != runID {
		t.Fatalf("last event = %+v, want run.finished for the watch", last)
	}

	// NOW release the held-back frame — strictly after the terminal event,
	// strictly after ClearStamp (finishWatch's tail order guarantees this).
	close(release)

	late := readSSE(t, busSrv.URL+"/events", 4)
	lateEvt := late[3]
	if lateEvt.Type != event.TypeObserver {
		t.Fatalf("late event = %+v, want observer", lateEvt)
	}
	if lateEvt.RunID != "" {
		t.Fatalf("late observer event RunID = %q, want empty (unstamped — the watch had already terminated)", lateEvt.RunID)
	}

	// The history Record captured by finishWatch's Sink call must not have
	// been reopened by the late frame — exactly one Sink call, for the
	// terminal Result.
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.results) != 1 || sink.results[0].RunID != runID {
		t.Fatalf("History Sink saw %+v, want exactly 1 call with RunID %q", sink.results, runID)
	}
}

// ---- Row 9: finishWatch panic-safety --------------------------------------

// TestWatch_PanickingSinkDoesNotWedgeRunner: a Sink whose RunCompleted panics
// during a watch's finalize must not wedge the runner — mirrors
// TestRun_PanickingSinkDoesNotWedgeRunner, but for the StartWatch/finishWatch/
// StopWatch path rather than Run/runLocked. StopWatch must still return
// promptly with the watch's (passed) Result, the watch slot must be cleared
// (a second StopWatch reads ErrNoWatch, never a stale buffered Result), and
// the sequential lock must be released (a Run immediately after succeeds).
//
// NOTE on this test's pre-fix status: at HEAD, finishWatch's Sink call
// already has its OWN inner recover, so this exact scenario (a panicking
// Sink) already passes before the fix below — it is not "red" on its own.
// The concern is architectural, not scenario-specific: everything
// AROUND that inner recover (drainRelay, auditPostFetchAndEmit, the
// terminal Emit, ClearStamp, the watch-slot nil, appendResult, r.mu.Unlock)
// had no top-level recover of finishWatch's own, so the SAME class of panic
// at any of those other sites (not reachable through the public Config
// surface, so not independently testable here) would propagate out of the
// goroutine StartWatch spawned, skipping `w.done <- res` entirely (StopWatch
// hangs forever on <-w.done) and leaving r.mu held forever (permanent
// ErrRunInFlight). This test locks in the externally-observable contract —
// no hang, a usable Result, the watch slot cleared, the lock released — for
// the one panic site reachable from a test (the History Sink), and the
// fix's top-level defer generalizes the same guarantee to the rest of the
// tail by construction.
func TestWatch_PanickingSinkDoesNotWedgeRunner(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver:  scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:     bus,
		History: panicSink{},
	})

	watchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := rn.StartWatch(watchCtx); err != nil {
		t.Fatalf("StartWatch: %v", err)
	}

	done := make(chan struct{})
	var res Result
	var stopErr error
	go func() {
		res, stopErr = rn.StopWatch()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StopWatch (panicking Sink) did not return — runner wedged")
	}
	if stopErr != nil {
		t.Fatalf("StopWatch (panicking Sink): returned a Go error, want the watch's own Result: %v", stopErr)
	}
	if res.State != StatePassed {
		t.Fatalf("StopWatch Result.State = %q, want passed despite the panicking Sink (Detail=%q)", res.State, res.Detail)
	}

	// The watch slot must have been nil'd: a second StopWatch reads
	// ErrNoWatch, never a stale buffered Result.
	if _, err := rn.StopWatch(); !errors.Is(err, ErrNoWatch) {
		t.Fatalf("second StopWatch after panicking Sink: err = %v, want ErrNoWatch (watch slot not cleared?)", err)
	}

	// The sequential lock must have been released: a Run works immediately
	// after, rather than permanently reading ErrRunInFlight.
	res2, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatalf("Run after panicking watch Sink: %v (runner wedged?)", err)
	}
	if res2.State != StatePassed {
		t.Fatalf("Run after panicking watch Sink: state = %q, want passed (Detail=%q)", res2.State, res2.Detail)
	}
}

// ---- Runner.InFlight -------------------------------------------------------

// TestRunner_InFlight proves the atomic InFlight() flag mirrors mu's hold
// across BOTH acquisition points — a running row (Start) and an open watch
// session (StartWatch) — false before either ever runs, true for exactly the
// duration the sequential lock is actually held, and false again once it is
// released. kit/kitd's restart handler reads this as a
// best-effort admission gate; a TryLock-based probe would itself steal the
// lock, which is exactly what this flag avoids.
func TestRunner_InFlight(t *testing.T) {
	if rn := New(Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: event.NewBus(fixedClock)}); rn.InFlight() {
		t.Fatal("InFlight() = true on a fresh Runner, want false")
	}

	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    bus,
	})

	if _, err := rn.Start(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	if !rn.InFlight() {
		t.Fatal("InFlight() = false while a run is executing, want true")
	}
	close(release)
	waitIdle(t, rn)
	if rn.InFlight() {
		t.Fatal("InFlight() = true after the run completed, want false")
	}

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	if _, err := rn.StartWatch(watchCtx); err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	if !rn.InFlight() {
		t.Fatal("InFlight() = false while a watch is open, want true")
	}
	if _, err := rn.StopWatch(); err != nil {
		t.Fatalf("StopWatch: %v", err)
	}
	if rn.InFlight() {
		t.Fatal("InFlight() = true after StopWatch, want false")
	}
}
