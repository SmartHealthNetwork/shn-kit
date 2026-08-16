// bridging_test.go — POST /api/bridging/exhibit: a fake
// POST /demo/transform child stands in for the gateway, so the proxy logic
// (contract/from/to/payload forwarding, 200/422 classification, the carry
// round trip's byte-identity proof, and the daemon-first/auth/kind gates) is
// tested hermetically without a real gateway child.
package kitd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/runner"
	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

// demoChildCall is one decoded POST /demo/transform request the fake child
// observed.
type demoChildCall struct {
	Contract string
	From     string
	To       string
	Payload  json.RawMessage
}

// newFakeDemoChild starts an httptest server serving POST /demo/transform
// per responder, and returns it (closed automatically at test cleanup).
// responder gets the decoded request and returns the HTTP status + body to
// encode as JSON.
func newFakeDemoChild(t *testing.T, responder func(call demoChildCall) (int, any)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /demo/transform", func(w http.ResponseWriter, r *http.Request) {
		var req demoTransformWireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("fake demo child: decode request: %v", err)
		}
		status, body := responder(demoChildCall{Contract: req.Contract, From: req.From, To: req.To, Payload: req.Payload})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("fake demo child: encode response: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// bridgingExhibitTestConfig returns a minimal Config sufficient to serve
// POST /api/bridging/exhibit (the route needs no Runner/BridgingDemo seam —
// only StackInfo.ObserverURL, set by the caller after startDaemon).
func bridgingExhibitTestConfig(token string) Config {
	bus := event.NewBus(fixedClock)
	return Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: "",
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Runner:   runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
	}
}

func TestBridgingExhibit_PreBoot503(t *testing.T) {
	const token = "bridging-exhibit-preboot-token"
	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	_, apiBase := startDaemon(t, cfg)
	// StackInfo intentionally left at its zero value: ObserverURL == "".

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/bridging/exhibit pre-boot = %d, want 503 (body=%s)", status, body)
	}
}

func TestBridgingExhibit_TokenGated(t *testing.T) {
	const token = "bridging-exhibit-gate-token"
	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	_, apiBase := startDaemon(t, cfg)

	status, _ := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", "", map[string]string{"kind": "carry"})
	if status != http.StatusUnauthorized {
		t.Fatalf("POST /api/bridging/exhibit without token = %d, want 401", status)
	}
}

func TestBridgingExhibit_UnknownKind400(t *testing.T) {
	const token = "bridging-exhibit-kind-token"
	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: "http://127.0.0.1:1/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "bogus"})
	if status != http.StatusBadRequest {
		t.Fatalf("POST /api/bridging/exhibit kind=bogus = %d, want 400 (body=%s)", status, body)
	}
}

func TestBridgingExhibit_BadBody400(t *testing.T) {
	const token = "bridging-exhibit-badbody-token"
	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: "http://127.0.0.1:1/events"})

	req, err := http.NewRequest(http.MethodPost, apiBase+"/api/bridging/exhibit", strings.NewReader("{not json"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/bridging/exhibit with malformed body = %d, want 400", resp.StatusCode)
	}
}

func TestBridgingExhibit_ChildUnreachable502(t *testing.T) {
	const token = "bridging-exhibit-unreachable-token"
	// A server started then immediately closed: its address refuses
	// connections, standing in for "gateway child not running".
	dead := httptest.NewServer(http.NewServeMux())
	dead.Close()

	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: dead.URL + "/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
	if status != http.StatusBadGateway {
		t.Fatalf("POST /api/bridging/exhibit carry, unreachable child = %d, want 502 (body=%s)", status, body)
	}

	status, body = doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "refusal"})
	if status != http.StatusBadGateway {
		t.Fatalf("POST /api/bridging/exhibit refusal, unreachable child = %d, want 502 (body=%s)", status, body)
	}
}

// TestBridgingExhibit_CarryHappyPath proves the carry kind's two-call
// sequence: down (2.2->2.1) then up (2.1->2.2) fed the down leg's own
// output, the combined lossReports surfacing the down leg's real Carried
// path, and restored:true when the up leg's output is byte-identical to the
// embedded input.
func TestBridgingExhibit_CarryHappyPath(t *testing.T) {
	const token = "bridging-exhibit-carry-happy-token"
	var calls []demoChildCall
	srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		calls = append(calls, call)
		switch {
		case call.From == "2.2" && call.To == "2.1":
			return http.StatusOK, demoTransformWireResponse{
				Output: json.RawMessage(`{"down":"output"}`),
				LossReports: []bridgingLossReport{{
					Module: "pa.dtr 2.2->2.1", Source: "2.2", Target: "2.1",
					Carried: []bridgingLossEntry{{
						Path:   "QuestionnaireResponse.item.answer.extension:itemWeight",
						Detail: "carried; source line 2.2 (no 2.1 slot)",
					}},
				}},
			}
		case call.From == "2.1" && call.To == "2.2":
			return http.StatusOK, demoTransformWireResponse{
				Output:      json.RawMessage(bridgingCarryInput),
				LossReports: []bridgingLossReport{{Module: "pa.dtr 2.1->2.2", Source: "2.1", Target: "2.2"}},
			}
		default:
			t.Fatalf("unexpected fake-child call: %+v", call)
			return 0, nil
		}
	})

	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
	if status != http.StatusOK {
		t.Fatalf("POST /api/bridging/exhibit carry = %d, want 200 (body=%s)", status, body)
	}
	var resp bridgingExhibitCarryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, body)
	}
	if resp.Kind != "carry" {
		t.Fatalf("resp.Kind = %q, want carry", resp.Kind)
	}
	if !resp.Restored {
		t.Fatalf("resp.Restored = false, want true (up leg's output was byte-identical to the embedded input)")
	}
	if len(resp.LossReports) != 2 {
		t.Fatalf("lossReports = %+v, want 2 entries (down leg + up leg)", resp.LossReports)
	}
	if len(resp.LossReports[0].Carried) != 1 || resp.LossReports[0].Carried[0].Path != "QuestionnaireResponse.item.answer.extension:itemWeight" {
		t.Fatalf("down leg report Carried = %+v, want exactly the itemWeight path", resp.LossReports[0].Carried)
	}

	if len(calls) != 2 {
		t.Fatalf("fake child called %d times, want 2 (down then up)", len(calls))
	}
	if calls[0].Contract != "pa.dtr" || calls[0].From != "2.2" || calls[0].To != "2.1" {
		t.Fatalf("down call = %+v, want contract=pa.dtr from=2.2 to=2.1", calls[0])
	}
	if string(calls[0].Payload) != string(bridgingCarryInput) {
		t.Fatalf("down call payload = %s, want the embedded carry input verbatim", calls[0].Payload)
	}
	if calls[1].Contract != "pa.dtr" || calls[1].From != "2.1" || calls[1].To != "2.2" {
		t.Fatalf("up call = %+v, want contract=pa.dtr from=2.1 to=2.2", calls[1])
	}
	if string(calls[1].Payload) != `{"down":"output"}` {
		t.Fatalf("up call payload = %s, want the down leg's own output fed forward", calls[1].Payload)
	}
}

// TestBridgingExhibit_CarryRoundTripMismatch500 proves the "never fake it"
// contract: if the up leg's output does NOT byte-match the embedded input,
// the handler answers 500 rather than reporting restored:true.
func TestBridgingExhibit_CarryRoundTripMismatch500(t *testing.T) {
	const token = "bridging-exhibit-carry-mismatch-token"
	srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		if call.From == "2.2" {
			return http.StatusOK, demoTransformWireResponse{Output: json.RawMessage(`{"down":"output"}`)}
		}
		return http.StatusOK, demoTransformWireResponse{Output: json.RawMessage(`{"not":"the same bytes"}`)}
	})

	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
	if status != http.StatusInternalServerError {
		t.Fatalf("POST /api/bridging/exhibit carry, mismatched round trip = %d, want 500 (body=%s)", status, body)
	}
	if !strings.Contains(string(body), "byte-identical") {
		t.Fatalf("500 body = %s, want it to name the byte-identity failure", body)
	}
}

// TestBridgingExhibit_CarryUnexpectedDownRefusal500 proves an unexpected
// 422 on the down leg is surfaced loudly (500) and the up leg is never
// called.
func TestBridgingExhibit_CarryUnexpectedDownRefusal500(t *testing.T) {
	const token = "bridging-exhibit-carry-downrefused-token"
	calls := 0
	srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		calls++
		return http.StatusUnprocessableEntity, demoTransformWireRefusal{Refusal: "unexpected", SemanticChange: true}
	})

	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
	if status != http.StatusInternalServerError {
		t.Fatalf("POST /api/bridging/exhibit carry, down leg refused = %d, want 500 (body=%s)", status, body)
	}
	if calls != 1 {
		t.Fatalf("fake child called %d times, want exactly 1 (up leg must never run after an unexpected down refusal)", calls)
	}
}

// TestBridgingExhibit_CarryUnexpectedUpRefusal500 proves an unexpected 422
// on the up leg (after a successful down leg) is also surfaced as 500.
func TestBridgingExhibit_CarryUnexpectedUpRefusal500(t *testing.T) {
	const token = "bridging-exhibit-carry-uprefused-token"
	srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		if call.From == "2.2" {
			return http.StatusOK, demoTransformWireResponse{Output: json.RawMessage(`{"down":"output"}`)}
		}
		return http.StatusUnprocessableEntity, demoTransformWireRefusal{Refusal: "unexpected", SemanticChange: true}
	})

	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
	if status != http.StatusInternalServerError {
		t.Fatalf("POST /api/bridging/exhibit carry, up leg refused = %d, want 500 (body=%s)", status, body)
	}
}

// TestBridgingExhibit_RefusalHappyPath proves the refusal kind surfaces the
// child's 422 as a 200 exhibit result carrying semanticChange:true and the
// typed refusal text.
func TestBridgingExhibit_RefusalHappyPath(t *testing.T) {
	const token = "bridging-exhibit-refusal-happy-token"
	const refusalText = "shn: semantic-change refusal: pa.dtr 2.1->2.2 (up direction): no honest byte-level source for QuestionnaireResponse.extension:qr-coverage (ambiguous: 2 Coverage-referencing qr-context entries, multi-coverage source)"
	var calls []demoChildCall
	srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		calls = append(calls, call)
		return http.StatusUnprocessableEntity, demoTransformWireRefusal{Refusal: refusalText, SemanticChange: true}
	})

	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "refusal"})
	if status != http.StatusOK {
		t.Fatalf("POST /api/bridging/exhibit refusal = %d, want 200 (body=%s)", status, body)
	}
	var resp bridgingExhibitRefusalResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, body)
	}
	if resp.Kind != "refusal" || !resp.SemanticChange || resp.Refusal != refusalText {
		t.Fatalf("resp = %+v, want kind=refusal semanticChange=true refusal=%q", resp, refusalText)
	}

	if len(calls) != 1 {
		t.Fatalf("fake child called %d times, want exactly 1", len(calls))
	}
	if calls[0].Contract != "pa.dtr" || calls[0].From != "2.1" || calls[0].To != "2.2" {
		t.Fatalf("call = %+v, want contract=pa.dtr from=2.1 to=2.2", calls[0])
	}
	if string(calls[0].Payload) != string(bridgingRefusalInput) {
		t.Fatalf("call payload = %s, want the embedded refusal input verbatim", calls[0].Payload)
	}
}

// TestBridgingExhibit_RefusalUnexpectedAccept500 proves the mirror of the
// carry mismatch case: if the crafted multi-coverage QR is unexpectedly
// ACCEPTED (200) rather than refused, the handler answers 500 rather than
// fabricating a refusal.
func TestBridgingExhibit_RefusalUnexpectedAccept500(t *testing.T) {
	const token = "bridging-exhibit-refusal-accepted-token"
	srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		return http.StatusOK, demoTransformWireResponse{Output: json.RawMessage(bridgingRefusalInput)}
	})

	cfg := bridgingExhibitTestConfig(token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "refusal"})
	if status != http.StatusInternalServerError {
		t.Fatalf("POST /api/bridging/exhibit refusal, unexpectedly accepted = %d, want 500 (body=%s)", status, body)
	}
}

// TestBridgingCarryInputAsset_ParsesAndHasItemWeight is the embedded-asset
// sanity test: the carry fixture genuinely parses as a QuestionnaireResponse
// and genuinely carries the itemWeight extension the exhibit exists to
// demonstrate — a drifted or malformed embed fails loudly here rather than
// surfacing only as an opaque proxy failure.
func TestBridgingCarryInputAsset_ParsesAndHasItemWeight(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(bridgingCarryInput, &doc); err != nil {
		t.Fatalf("carry input asset does not parse as JSON: %v", err)
	}
	if doc["resourceType"] != "QuestionnaireResponse" {
		t.Fatalf("carry input asset resourceType = %v, want QuestionnaireResponse", doc["resourceType"])
	}
	items, _ := doc["item"].([]any)
	found := false
	for _, it := range items {
		im, ok := it.(map[string]any)
		if !ok || im["linkId"] != "conservative-therapy-weeks" {
			continue
		}
		answers, _ := im["answer"].([]any)
		if len(answers) == 0 {
			continue
		}
		am, ok := answers[0].(map[string]any)
		if !ok {
			continue
		}
		exts, _ := am["extension"].([]any)
		for _, e := range exts {
			em, ok := e.(map[string]any)
			// Literal, not imported: this package deliberately does not depend on
			// gateway/engine (kit's go.mod boundary — see bridging.go's header).
			// Mirrors engine/transform_dtr.go's dtrItemWeightExt constant with no
			// compile-time link; if that constant's value ever changes, this
			// string must be updated by hand or this test silently stops proving
			// anything about the real extension.
			if ok && em["url"] == "http://hl7.org/fhir/StructureDefinition/itemWeight" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("carry input asset has no item.answer.extension:itemWeight on conservative-therapy-weeks")
	}
}

// TestBridgingRefusalInputAsset_ParsesAndHasMultiCoverage is the refusal
// fixture's own sanity test: it must genuinely carry >=2 Coverage-
// referencing qr-context entries — the exact shape dtrRelocateCoverageUp
// refuses.
func TestBridgingRefusalInputAsset_ParsesAndHasMultiCoverage(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(bridgingRefusalInput, &doc); err != nil {
		t.Fatalf("refusal input asset does not parse as JSON: %v", err)
	}
	if doc["resourceType"] != "QuestionnaireResponse" {
		t.Fatalf("refusal input asset resourceType = %v, want QuestionnaireResponse", doc["resourceType"])
	}
	extAny, _ := doc["extension"].([]any)
	count := 0
	for _, e := range extAny {
		em, ok := e.(map[string]any)
		// Literal, not imported (same cross-module drift note as
		// TestBridgingCarryInputAsset_ParsesAndHasItemWeight above): mirrors
		// engine/transform_dtr.go's dtrQRContextExt constant with no compile-time
		// link across the kit/gateway module boundary.
		if !ok || em["url"] != "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/qr-context" {
			continue
		}
		ref, _ := em["valueReference"].(map[string]any)
		r, _ := ref["reference"].(string)
		if strings.HasPrefix(r, "Coverage/") {
			count++
		}
	}
	if count < 2 {
		t.Fatalf("refusal input asset has %d Coverage-referencing qr-context entries, want >= 2", count)
	}
}

func TestDemoTransformURL(t *testing.T) {
	got := demoTransformURL("http://127.0.0.1:54321/events")
	want := "http://127.0.0.1:54321/demo/transform"
	if got != want {
		t.Fatalf("demoTransformURL = %q, want %q", got, want)
	}
}
