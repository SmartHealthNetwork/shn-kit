package bootstrap

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// --- fixtures ------------------------------------------------------------

// fakeDiscoverySrv serves a shnsdk.Discovery descriptor pointing Endpoints.
// Registrar at registrarURL (mirrors test/kitlive/substrate_test.go:144-156,
// hand-built here with shnsdk types — no test-code import across the kit
// boundary fence).
func fakeDiscoverySrv(t *testing.T, registrarURL string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(shnsdk.Discovery{
			Endpoints: shnsdk.DiscoveryEndpoints{
				Registrar: registrarURL,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFetchDiscovery_ResolvesPHG proves shnkitd can read the patient-surface (PHG)
// endpoint off discovery — the resolution cmd/shnkitd uses to wire the scenario driver's
// UC-07 read-back (without it the driver hits "" + /personas → the desktop UC-07 failure).
func TestFetchDiscovery_ResolvesPHG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(shnsdk.Discovery{
			Endpoints: shnsdk.DiscoveryEndpoints{
				Registrar: "https://registrar.example",
				PHG:       "https://phg.example",
				Consent:   "https://consent.example",
			},
		})
	}))
	t.Cleanup(srv.Close)

	disc, err := FetchDiscovery(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatalf("FetchDiscovery: %v", err)
	}
	if disc.Endpoints.PHG != "https://phg.example" {
		t.Fatalf("Endpoints.PHG = %q, want https://phg.example", disc.Endpoints.PHG)
	}
}

// TestFetchDiscovery_Errors proves the fetch surfaces transport/status failures (so
// shnkitd's best-effort resolution logs and falls back rather than panicking).
func TestFetchDiscovery_Errors(t *testing.T) {
	// Non-200.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	if _, err := FetchDiscovery(context.Background(), nil, bad.URL); err == nil {
		t.Fatal("FetchDiscovery(500): want error, got nil")
	}
	// Unreachable host.
	if _, err := FetchDiscovery(context.Background(), nil, "http://127.0.0.1:0/discovery"); err == nil {
		t.Fatal("FetchDiscovery(unreachable): want error, got nil")
	}
}

// TestPatientSurfaceReadable proves the boot probe that decides whether UC-07's read-back
// runs or degrades: 200 + JSON array ⇒ readable; the HOSTED notify-only edge (404 "no
// route") ⇒ not readable; a non-array body or an unreachable host ⇒ not readable.
func TestPatientSurfaceReadable(t *testing.T) {
	// Readable: a real patient-surface render (200 + JSON array — even empty).
	readable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/personas" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"label":"Nakamura","pci":"pci:x"}]`))
	}))
	t.Cleanup(readable.Close)
	if !PatientSurfaceReadable(context.Background(), nil, readable.URL) {
		t.Fatal("PatientSurfaceReadable(real render) = false, want true")
	}

	// HOSTED notify-only edge: /personas is not routed → 404 "no route".
	notifyOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notify" {
			w.WriteHeader(http.StatusMethodNotAllowed) // routed, POST-only
			return
		}
		http.Error(w, "no route", http.StatusNotFound)
	}))
	t.Cleanup(notifyOnly.Close)
	if PatientSurfaceReadable(context.Background(), nil, notifyOnly.URL) {
		t.Fatal("PatientSurfaceReadable(notify-only edge) = true, want false (the desktop UC-07 failure)")
	}

	// A 200 that is NOT a JSON array (e.g. an HTML error page) ⇒ not readable.
	notArray := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>oops</html>`))
	}))
	t.Cleanup(notArray.Close)
	if PatientSurfaceReadable(context.Background(), nil, notArray.URL) {
		t.Fatal("PatientSurfaceReadable(non-array body) = true, want false")
	}

	// Empty URL and an unreachable host ⇒ not readable, no panic.
	if PatientSurfaceReadable(context.Background(), nil, "") {
		t.Fatal("PatientSurfaceReadable(\"\") = true, want false")
	}
	if PatientSurfaceReadable(context.Background(), nil, "http://127.0.0.1:0") {
		t.Fatal("PatientSurfaceReadable(unreachable) = true, want false")
	}
}

// fakeRegistrarSrv serves GET /holders with the fixed holders fixture
// (mirrors the same FeedPayerRouter precondition used by the substrate's own
// integration fixtures).
func fakeRegistrarSrv(t *testing.T, holders []shnsdk.Holder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/holders" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(holders)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readVerifyEvents GETs an SSE url and collects exactly n "data:" events (as
// event.Event), or fails the test after a 5s deadline. Mirrors
// bootstrap_test.go's readEvents (unexported to that file, so re-declared
// here for this file's own use).
func readVerifyEvents(t *testing.T, url string, n int) []event.Event {
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
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
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

func probeByName(probes []Probe, name string) (Probe, bool) {
	for _, p := range probes {
		if p.Name == name {
			return p, true
		}
	}
	return Probe{}, false
}

// --- Row 1: all green, hc == nil pins the nil-default ----------------------

func TestVerify_AllGreen(t *testing.T) {
	holders := []shnsdk.Holder{
		{ID: "kit-h1", Role: "provider"},
		{ID: ReferencePayerHolderID, Role: "payer"},
	}
	reg := fakeRegistrarSrv(t, holders)
	disc := fakeDiscoverySrv(t, reg.URL)

	now := func() time.Time { return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC) }
	bus := event.NewBus(now)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()
	resultCh := make(chan []event.Event, 1)
	go func() { resultCh <- readVerifyEvents(t, busSrv.URL+"/events", 3) }()

	// hc deliberately nil — pins Verify's internal http.DefaultClient default
	// (shnsdk.FetchHolders has no nil-guard of its own). BridgeProbes{} zero
	// value: both bridge probes absent (unconfigured).
	probes := Verify(context.Background(), nil, disc.URL, "kit-h1", BridgeProbes{}, bus)

	if len(probes) != 3 {
		t.Fatalf("len(probes) = %d, want 3: %+v", len(probes), probes)
	}
	for _, name := range []string{"discovery", "registration", "reference-payer"} {
		p, ok := probeByName(probes, name)
		if !ok {
			t.Fatalf("missing probe %q in %+v", name, probes)
		}
		if !p.OK {
			t.Errorf("probe %q OK = false, want true (detail %q)", name, p.Detail)
		}
	}

	select {
	case events := <-resultCh:
		if len(events) != 3 {
			t.Fatalf("got %d verify events, want 3", len(events))
		}
		for _, e := range events {
			if e.Type != event.TypeVerify {
				t.Errorf("event.Type = %q, want %q", e.Type, event.TypeVerify)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for verify events")
	}
}

// --- Row 2: holder missing from the feed ---------------------------------

func TestVerify_HolderMissingFromFeed(t *testing.T) {
	holders := []shnsdk.Holder{
		{ID: "someone-else", Role: "provider"},
		{ID: ReferencePayerHolderID, Role: "payer"},
	}
	reg := fakeRegistrarSrv(t, holders)
	disc := fakeDiscoverySrv(t, reg.URL)

	probes := Verify(context.Background(), http.DefaultClient, disc.URL, "kit-h1", BridgeProbes{}, nil)

	discP, _ := probeByName(probes, "discovery")
	if !discP.OK {
		t.Errorf("discovery probe OK = false, want true: %+v", discP)
	}
	regP, ok := probeByName(probes, "registration")
	if !ok {
		t.Fatalf("missing registration probe: %+v", probes)
	}
	if regP.OK {
		t.Error("registration probe OK = true, want false")
	}
	if !strings.Contains(regP.Detail, "kit-h1") {
		t.Errorf("registration probe Detail = %q, want it to name the holder id kit-h1", regP.Detail)
	}
	payerP, _ := probeByName(probes, "reference-payer")
	if !payerP.OK {
		t.Errorf("reference-payer probe OK = false, want true (unaffected by the missing holder): %+v", payerP)
	}
}

// --- Row 3: the reference payer is not on the feed ---------------------------
func TestVerify_ReferencePayerMissing(t *testing.T) {
	holders := []shnsdk.Holder{
		{ID: "kit-h1", Role: "provider"},
		// A payer holder that publishes the routable payer identity but is NOT the
		// reference payer holder: claiming the identity is not the same as BEING the
		// holder the probe names, and the probe must not accept the substitution.
		{ID: "some-other-payer", Role: "payer", PayerIDs: []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity}},
	}
	reg := fakeRegistrarSrv(t, holders)
	disc := fakeDiscoverySrv(t, reg.URL)
	probes := Verify(context.Background(), http.DefaultClient, disc.URL, "kit-h1", BridgeProbes{}, nil)
	p, ok := probeByName(probes, "reference-payer")
	if !ok {
		t.Fatalf("missing reference-payer probe: %+v", probes)
	}
	if p.OK {
		t.Error("reference-payer probe OK = true, want false (a payer publishing PayerIDs is not the reference payer)")
	}
	if !strings.Contains(p.Detail, ReferencePayerHolderID) {
		t.Errorf("Detail = %q, want it to name %s", p.Detail, ReferencePayerHolderID)
	}
}

func TestVerify_ReferencePayerWrongRole(t *testing.T) {
	holders := []shnsdk.Holder{
		{ID: "kit-h1", Role: "provider"},
		{ID: ReferencePayerHolderID, Role: "provider"},
	}
	reg := fakeRegistrarSrv(t, holders)
	disc := fakeDiscoverySrv(t, reg.URL)
	probes := Verify(context.Background(), http.DefaultClient, disc.URL, "kit-h1", BridgeProbes{}, nil)
	if p, _ := probeByName(probes, "reference-payer"); p.OK {
		t.Error("reference-payer probe OK = true for a non-payer holder, want false")
	}
}

// --- Row 4: discovery unreachable ------------------------------------------

func TestVerify_DiscoveryUnreachable(t *testing.T) {
	closedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closedSrv.URL
	closedSrv.Close() // now unreachable

	probes := Verify(context.Background(), http.DefaultClient, closedURL, "kit-h1", BridgeProbes{}, nil)

	if len(probes) != 3 {
		t.Fatalf("len(probes) = %d, want 3: %+v", len(probes), probes)
	}
	discP, _ := probeByName(probes, "discovery")
	if discP.OK {
		t.Error("discovery probe OK = true, want false")
	}
	for _, name := range []string{"registration", "reference-payer"} {
		p, ok := probeByName(probes, name)
		if !ok {
			t.Fatalf("missing probe %q: %+v", name, probes)
		}
		if p.OK {
			t.Errorf("probe %q OK = true, want false (dependent on failed discovery)", name)
		}
		if p.Detail != "skipped: discovery failed" {
			t.Errorf("probe %q Detail = %q, want %q", name, p.Detail, "skipped: discovery failed")
		}
	}
}

// --- Row 5: FetchHolders itself fails ---------------------------------------

// TestVerify_FetchHoldersFails: discovery succeeds and names a registrar
// endpoint, but that endpoint is itself unreachable — exercising verify.go's
// already-implemented FetchHolders error branch (lines 61-71), which had no
// dedicated test until now.
func TestVerify_FetchHoldersFails(t *testing.T) {
	// Allocate a port and immediately close the listener, so the registrar
	// endpoint discovery names is provably dead (connection refused) rather
	// than merely slow or 404ing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	registrarURL := "http://" + ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("ln.Close: %v", err)
	}

	disc := fakeDiscoverySrv(t, registrarURL)

	probes := Verify(context.Background(), http.DefaultClient, disc.URL, "kit-h1", BridgeProbes{}, nil)

	if len(probes) != 3 {
		t.Fatalf("len(probes) = %d, want 3: %+v", len(probes), probes)
	}
	discP, _ := probeByName(probes, "discovery")
	if !discP.OK {
		t.Errorf("discovery probe OK = false, want true: %+v", discP)
	}
	for _, name := range []string{"registration", "reference-payer"} {
		p, ok := probeByName(probes, name)
		if !ok {
			t.Fatalf("missing probe %q: %+v", name, probes)
		}
		if p.OK {
			t.Errorf("probe %q OK = true, want false (dependent on the failed holder feed fetch)", name)
		}
		if !strings.Contains(p.Detail, "skipped: fetch holder feed failed") {
			t.Errorf("probe %q Detail = %q, want containing %q", name, p.Detail, "skipped: fetch holder feed failed")
		}
	}
}

// --- Row 6: bridge-demo probes ---------------------------------------------

// demoHolder/refuseHolder are the fixture holder ids the bridge-probe rows below use.
const (
	demoHolder   = "bridge-demo"
	refuseHolder = "bridge-demo-refuse"
)

func TestVerify_BridgeProbes(t *testing.T) {
	tests := []struct {
		name         string
		holders      []shnsdk.Holder
		bp           BridgeProbes
		wantProbes   []string // probe names expected present, in any order
		wantPayerOK  *bool
		wantRefuseOK *bool
		wantDetail   map[string]string // probe name -> substring expected in Detail
	}{
		{
			name: "both unconfigured: neither probe present",
			holders: []shnsdk.Holder{
				{ID: "kit-h1", Role: "provider"},
			},
			bp:         BridgeProbes{},
			wantProbes: nil,
		},
		{
			name: "green: demo payer exists with PayerIDs and a non-2.0 line",
			holders: []shnsdk.Holder{
				{ID: "kit-h1", Role: "provider"},
				{
					ID:               demoHolder,
					Role:             "payer",
					PayerIDs:         []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity},
					ContractVersions: []string{"pa.crd@2.1"},
				},
			},
			bp:          BridgeProbes{DemoHolder: demoHolder},
			wantProbes:  []string{"bridge-demo-payer"},
			wantPayerOK: boolPtr(true),
		},
		{
			name: "absent: demo holder not on the feed",
			holders: []shnsdk.Holder{
				{ID: "kit-h1", Role: "provider"},
			},
			bp:          BridgeProbes{DemoHolder: demoHolder},
			wantProbes:  []string{"bridge-demo-payer"},
			wantPayerOK: boolPtr(false),
			wantDetail:  map[string]string{"bridge-demo-payer": "not found in registrar feed"},
		},
		{
			name: "red: demo holder is not a payer",
			holders: []shnsdk.Holder{
				{ID: demoHolder, Role: "provider", ContractVersions: []string{"pa.crd@2.1"}},
			},
			bp:          BridgeProbes{DemoHolder: demoHolder},
			wantProbes:  []string{"bridge-demo-payer"},
			wantPayerOK: boolPtr(false),
			wantDetail:  map[string]string{"bridge-demo-payer": "is not a payer"},
		},
		{
			name: "red: demo holder publishes no PayerIDs",
			holders: []shnsdk.Holder{
				{ID: demoHolder, Role: "payer", ContractVersions: []string{"pa.crd@2.1"}},
			},
			bp:          BridgeProbes{DemoHolder: demoHolder},
			wantProbes:  []string{"bridge-demo-payer"},
			wantPayerOK: boolPtr(false),
			wantDetail:  map[string]string{"bridge-demo-payer": "publishes no payer identity"},
		},
		{
			name: "red: demo holder stuck at the 2.0 baseline",
			holders: []shnsdk.Holder{
				{
					ID:               demoHolder,
					Role:             "payer",
					PayerIDs:         []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity},
					ContractVersions: []string{"pa.crd@2.0", "pa.pas@2.0"},
				},
			},
			bp:          BridgeProbes{DemoHolder: demoHolder},
			wantProbes:  []string{"bridge-demo-payer"},
			wantPayerOK: boolPtr(false),
			wantDetail:  map[string]string{"bridge-demo-payer": "no CRD or DTR contract-version line beyond the 2.0 baseline"},
		},
		{
			// A pa.pas@2.1-only holder must NOT read
			// GREEN while the demo's bridged CRD/DTR legs would show no
			// transform at all — the probe must demand a non-2.0 CRD or DTR
			// line specifically, matching the refuse probe's hardening.
			name: "red: demo holder skewed only on PAS (no CRD/DTR line beyond 2.0)",
			holders: []shnsdk.Holder{
				{
					ID:               demoHolder,
					Role:             "payer",
					PayerIDs:         []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity},
					ContractVersions: []string{"pa.pas@2.1"},
				},
			},
			bp:          BridgeProbes{DemoHolder: demoHolder},
			wantProbes:  []string{"bridge-demo-payer"},
			wantPayerOK: boolPtr(false),
			wantDetail:  map[string]string{"bridge-demo-payer": "no CRD or DTR contract-version line beyond the 2.0 baseline"},
		},
		{
			name: "green: refuse holder shares a CRD/DTR line at pa.pas non-2.0",
			holders: []shnsdk.Holder{
				{
					ID:               refuseHolder,
					Role:             "payer",
					PayerIDs:         []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity},
					ContractVersions: []string{"pa.pas@2.1", "pa.crd@2.0"},
				},
			},
			bp:           BridgeProbes{RefuseHolder: refuseHolder},
			wantProbes:   []string{"bridge-demo-refuse"},
			wantRefuseOK: boolPtr(true),
		},
		{
			name: "red: refuse holder declares pa.pas only — no shared CRD/DTR line (the exhibit precondition)",
			holders: []shnsdk.Holder{
				{
					ID:               refuseHolder,
					Role:             "payer",
					PayerIDs:         []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity},
					ContractVersions: []string{"pa.pas@2.1"},
				},
			},
			bp:           BridgeProbes{RefuseHolder: refuseHolder},
			wantProbes:   []string{"bridge-demo-refuse"},
			wantRefuseOK: boolPtr(false),
			wantDetail:   map[string]string{"bridge-demo-refuse": "shares no CRD/DTR line"},
		},
		{
			name: "red: refuse holder has no non-2.0 pa.pas line",
			holders: []shnsdk.Holder{
				{
					ID:               refuseHolder,
					Role:             "payer",
					PayerIDs:         []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity},
					ContractVersions: []string{"pa.pas@2.0", "pa.crd@2.0"},
				},
			},
			bp:           BridgeProbes{RefuseHolder: refuseHolder},
			wantProbes:   []string{"bridge-demo-refuse"},
			wantRefuseOK: boolPtr(false),
			wantDetail:   map[string]string{"bridge-demo-refuse": "no pa.pas line beyond the 2.0 baseline"},
		},
		{
			name: "red: refuse holder publishes no PayerIDs",
			holders: []shnsdk.Holder{
				{ID: refuseHolder, Role: "payer", ContractVersions: []string{"pa.pas@2.1", "pa.crd@2.0"}},
			},
			bp:           BridgeProbes{RefuseHolder: refuseHolder},
			wantProbes:   []string{"bridge-demo-refuse"},
			wantRefuseOK: boolPtr(false),
			wantDetail:   map[string]string{"bridge-demo-refuse": "publishes no payer identity"},
		},
		{
			name: "both configured: both probes present independently",
			holders: []shnsdk.Holder{
				{
					ID:               demoHolder,
					Role:             "payer",
					PayerIDs:         []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity},
					ContractVersions: []string{"pa.crd@2.1"},
				},
				// refuseHolder deliberately absent from the feed.
			},
			bp:           BridgeProbes{DemoHolder: demoHolder, RefuseHolder: refuseHolder},
			wantProbes:   []string{"bridge-demo-payer", "bridge-demo-refuse"},
			wantPayerOK:  boolPtr(true),
			wantRefuseOK: boolPtr(false),
			wantDetail:   map[string]string{"bridge-demo-refuse": "not found in registrar feed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := fakeRegistrarSrv(t, tt.holders)
			disc := fakeDiscoverySrv(t, reg.URL)

			probes := Verify(context.Background(), http.DefaultClient, disc.URL, "kit-h1", tt.bp, nil)

			if _, ok := probeByName(probes, "bridge-demo-payer"); ok != contains(tt.wantProbes, "bridge-demo-payer") {
				t.Fatalf("bridge-demo-payer presence = %v, want %v (bp=%+v probes=%+v)", ok, contains(tt.wantProbes, "bridge-demo-payer"), tt.bp, probes)
			}
			if _, ok := probeByName(probes, "bridge-demo-refuse"); ok != contains(tt.wantProbes, "bridge-demo-refuse") {
				t.Fatalf("bridge-demo-refuse presence = %v, want %v (bp=%+v probes=%+v)", ok, contains(tt.wantProbes, "bridge-demo-refuse"), tt.bp, probes)
			}

			if tt.wantPayerOK != nil {
				p, ok := probeByName(probes, "bridge-demo-payer")
				if !ok {
					t.Fatalf("missing bridge-demo-payer probe: %+v", probes)
				}
				if p.OK != *tt.wantPayerOK {
					t.Errorf("bridge-demo-payer OK = %v, want %v (detail %q)", p.OK, *tt.wantPayerOK, p.Detail)
				}
			}
			if tt.wantRefuseOK != nil {
				p, ok := probeByName(probes, "bridge-demo-refuse")
				if !ok {
					t.Fatalf("missing bridge-demo-refuse probe: %+v", probes)
				}
				if p.OK != *tt.wantRefuseOK {
					t.Errorf("bridge-demo-refuse OK = %v, want %v (detail %q)", p.OK, *tt.wantRefuseOK, p.Detail)
				}
			}
			for name, want := range tt.wantDetail {
				p, ok := probeByName(probes, name)
				if !ok {
					t.Fatalf("missing probe %q: %+v", name, probes)
				}
				if !strings.Contains(p.Detail, want) {
					t.Errorf("probe %q Detail = %q, want containing %q", name, p.Detail, want)
				}
			}
		})
	}
}

// TestVerify_BridgeProbes_SkippedWithDiscoveryFailure proves the bridge
// probes ride the SAME "skipped: discovery failed" / "skipped: fetch holder
// feed failed" branches as registration/reference-payer — present (not
// silently dropped) whenever configured, absent whenever not, regardless of
// which branch Verify takes.
func TestVerify_BridgeProbes_SkippedWithDiscoveryFailure(t *testing.T) {
	closedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closedSrv.URL
	closedSrv.Close()

	probes := Verify(context.Background(), http.DefaultClient, closedURL, "kit-h1",
		BridgeProbes{DemoHolder: demoHolder, RefuseHolder: refuseHolder}, nil)

	if len(probes) != 5 {
		t.Fatalf("len(probes) = %d, want 5: %+v", len(probes), probes)
	}
	for _, name := range []string{"bridge-demo-payer", "bridge-demo-refuse"} {
		p, ok := probeByName(probes, name)
		if !ok {
			t.Fatalf("missing probe %q: %+v", name, probes)
		}
		if p.OK {
			t.Errorf("probe %q OK = true, want false (dependent on failed discovery)", name)
		}
		if p.Detail != "skipped: discovery failed" {
			t.Errorf("probe %q Detail = %q, want %q", name, p.Detail, "skipped: discovery failed")
		}
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }
