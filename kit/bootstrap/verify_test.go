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
		{ID: "payer-1", Role: "payer", PayerIDs: []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity}},
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
	// (shnsdk.FetchHolders has no nil-guard of its own).
	probes := Verify(context.Background(), nil, disc.URL, "kit-h1", bus)

	if len(probes) != 3 {
		t.Fatalf("len(probes) = %d, want 3: %+v", len(probes), probes)
	}
	for _, name := range []string{"discovery", "registration", "hosted-payer"} {
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
		{ID: "payer-1", Role: "payer", PayerIDs: []shnsdk.PayerIdentifier{shnsdk.CMSPayerIdentity}},
	}
	reg := fakeRegistrarSrv(t, holders)
	disc := fakeDiscoverySrv(t, reg.URL)

	probes := Verify(context.Background(), http.DefaultClient, disc.URL, "kit-h1", nil)

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
	payerP, _ := probeByName(probes, "hosted-payer")
	if !payerP.OK {
		t.Errorf("hosted-payer probe OK = false, want true (unaffected by the missing holder): %+v", payerP)
	}
}

// --- Row 3: no payer with PayerIDs ----------------------------------------

func TestVerify_NoRoutablePayer(t *testing.T) {
	holders := []shnsdk.Holder{
		{ID: "kit-h1", Role: "provider"},
		{ID: "payer-1", Role: "payer"}, // no PayerIDs published
	}
	reg := fakeRegistrarSrv(t, holders)
	disc := fakeDiscoverySrv(t, reg.URL)

	probes := Verify(context.Background(), http.DefaultClient, disc.URL, "kit-h1", nil)

	regP, _ := probeByName(probes, "registration")
	if !regP.OK {
		t.Errorf("registration probe OK = false, want true: %+v", regP)
	}
	payerP, ok := probeByName(probes, "hosted-payer")
	if !ok {
		t.Fatalf("missing hosted-payer probe: %+v", probes)
	}
	if payerP.OK {
		t.Error("hosted-payer probe OK = true, want false")
	}
	if payerP.Detail == "" {
		t.Error("hosted-payer probe Detail is empty, want an explanation that no routable payer identity is published")
	}
}

// --- Row 4: discovery unreachable ------------------------------------------

func TestVerify_DiscoveryUnreachable(t *testing.T) {
	closedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closedSrv.URL
	closedSrv.Close() // now unreachable

	probes := Verify(context.Background(), http.DefaultClient, closedURL, "kit-h1", nil)

	if len(probes) != 3 {
		t.Fatalf("len(probes) = %d, want 3: %+v", len(probes), probes)
	}
	discP, _ := probeByName(probes, "discovery")
	if discP.OK {
		t.Error("discovery probe OK = true, want false")
	}
	for _, name := range []string{"registration", "hosted-payer"} {
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

	probes := Verify(context.Background(), http.DefaultClient, disc.URL, "kit-h1", nil)

	if len(probes) != 3 {
		t.Fatalf("len(probes) = %d, want 3: %+v", len(probes), probes)
	}
	discP, _ := probeByName(probes, "discovery")
	if !discP.OK {
		t.Errorf("discovery probe OK = false, want true: %+v", discP)
	}
	for _, name := range []string{"registration", "hosted-payer"} {
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
