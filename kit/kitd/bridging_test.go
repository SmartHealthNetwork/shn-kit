// bridging_test.go — POST /api/bridging/exhibit: a fake
// POST /demo/transform child stands in for the gateway, so the proxy logic
// (contract/from/to/payload forwarding, 200/422 classification, the carry
// round trip's byte-identity proof, and the daemon-first/auth/kind gates) is
// tested hermetically without a real gateway child.
package kitd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/runhistory"
	"github.com/SmartHealthNetwork/shn-kit/runner"
	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

// newFakeCaptureChild starts an httptest server serving GET
// /demo/capture/{id} per responder, standing in for the gateway's own
// GET /demo/capture/{correlationId} (gateway/app/demo_endpoint.go) — a
// separate fake from newFakeDemoChild above (which only ever fakes
// POST /demo/transform): the capture proxy's passthrough tests need a
// child that answers a raw status+body pair verbatim, not a decoded/
// re-encoded wire struct, so the byte-identity assertions below prove
// something about the proxy, not about json.Encoder's own round trip.
func newFakeCaptureChild(t *testing.T, responder func(id string) (int, []byte)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo/capture/{id}", func(w http.ResponseWriter, r *http.Request) {
		status, body := responder(r.PathValue("id"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// demoRunIDPattern pins the demonstration run id shape (bridging.go's
// emitDemoRun): "demo-<unixMilli>-<n>", namespace-disjoint from the
// runner's own "run-<unixMilli>-<n>" ids.
var demoRunIDPattern = regexp.MustCompile(`^demo-\d+-\d+$`)

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
// POST /api/bridging/exhibit (the route needs no BridgingDemo seam — only
// StackInfo.ObserverURL, set by the caller after startDaemon). It wires a
// real temp-dir runhistory.Store (not nil) so the demonstration-run history
// assertions have somewhere to read from, and the same fixedClock the bus
// uses (Config.Clock) so a demo run's minted id and its bus-stamped
// Event.Time are drawn from one source, mirroring production's
// event.NewBus(time.Now)/Config.Clock: time.Now pairing.
func bridgingExhibitTestConfig(t *testing.T, token string) Config {
	t.Helper()
	bus := event.NewBus(fixedClock)
	return Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: "",
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		Runner:   runner.New(runner.Config{Driver: scenariodriver.New(scenariodriver.Config{}), Bus: bus}),
		History:  runhistory.NewStore(t.TempDir(), 50),
		Clock:    fixedClock,
	}
}

func TestBridgingExhibit_PreBoot503(t *testing.T) {
	const token = "bridging-exhibit-preboot-token"
	cfg := bridgingExhibitTestConfig(t, token)
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
	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	_, apiBase := startDaemon(t, cfg)

	status, _ := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", "", map[string]string{"kind": "carry"})
	if status != http.StatusUnauthorized {
		t.Fatalf("POST /api/bridging/exhibit without token = %d, want 401", status)
	}
}

func TestBridgingExhibit_UnknownKind400(t *testing.T) {
	const token = "bridging-exhibit-kind-token"
	cfg := bridgingExhibitTestConfig(t, token)
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
	cfg := bridgingExhibitTestConfig(t, token)
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

	cfg := bridgingExhibitTestConfig(t, token)
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
						Path:   "QuestionnaireResponse.item.answer.value.extension:itemWeight",
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

	cfg := bridgingExhibitTestConfig(t, token)
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
	if len(resp.LossReports[0].Carried) != 1 || resp.LossReports[0].Carried[0].Path != "QuestionnaireResponse.item.answer.value.extension:itemWeight" {
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

	cfg := bridgingExhibitTestConfig(t, token)
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

	cfg := bridgingExhibitTestConfig(t, token)
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

	cfg := bridgingExhibitTestConfig(t, token)
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

	cfg := bridgingExhibitTestConfig(t, token)
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

	cfg := bridgingExhibitTestConfig(t, token)
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
	// The demo questionnaire groups its leaves, so the weeks item sits inside
	// clinical-history — walk every depth (both QR nesting axes), not the top level.
	var flatten func(items []any) []any
	flatten = func(items []any) []any {
		var out []any
		for _, it := range items {
			im, ok := it.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, im)
			sub, _ := im["item"].([]any)
			out = append(out, flatten(sub)...)
			answers, _ := im["answer"].([]any)
			for _, a := range answers {
				if am, ok := a.(map[string]any); ok {
					asub, _ := am["item"].([]any)
					out = append(out, flatten(asub)...)
				}
			}
		}
		return out
	}
	items, _ := doc["item"].([]any)
	found := false
	for _, it := range flatten(items) {
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
		// The itemWeight extension must sit on the answer's VALUE, not on the
		// answer. The extension's SD contexts it to
		// QuestionnaireResponse.item.answer.value (and Coding); the answer-level
		// slice DTR 2.2.0 declares is unsatisfiable on the wire, and the engine
		// reads the SD. This answer is valueInteger, a primitive, so its
		// extensions live on the sibling "_valueInteger" object.
		//
		// Literal, not imported: this package deliberately does not depend on
		// gateway/engine (kit's go.mod boundary — see bridging.go's header).
		// Mirrors engine/transform_dtr.go's dtrItemWeightExt constant with no
		// compile-time link; if that constant's value ever changes, this string
		// must be updated by hand or this test silently stops proving anything.
		under, _ := am["_valueInteger"].(map[string]any)
		if under == nil {
			t.Fatalf("carry input asset answer has no _valueInteger container — the itemWeight cannot be at its context-legal locus")
		}
		underExts, _ := under["extension"].([]any)
		for _, e := range underExts {
			em, ok := e.(map[string]any)
			if ok && em["url"] == "http://hl7.org/fhir/StructureDefinition/itemWeight" {
				found = true
			}
		}
		// Negative half: the old, context-illegal locus must be empty of
		// itemWeight, or the exhibit would demonstrate a shape no conformant
		// peer can send and the engine no longer carries.
		answerExts, _ := am["extension"].([]any)
		for _, e := range answerExts {
			em, ok := e.(map[string]any)
			if ok && em["url"] == "http://hl7.org/fhir/StructureDefinition/itemWeight" {
				t.Fatal("carry input asset still carries itemWeight at answer.extension — that locus is context-illegal")
			}
		}
	}
	if !found {
		t.Fatalf("carry input asset has no item.answer.value.extension:itemWeight on conservative-therapy-weeks")
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

// demoEventsForRun filters the bus's full replay to the events stamped with
// runID, oldest first.
func demoEventsForRun(t *testing.T, bus *event.Bus, runID string) []event.Event {
	t.Helper()
	var out []event.Event
	for _, e := range bus.Since(0) {
		if e.RunID == runID {
			out = append(out, e)
		}
	}
	return out
}

// TestBridgingExhibit_RefusalProducesDemonstrationRun proves the refusal
// kind's success path orchestrates a full local-demonstration run: a
// demo-<unixMilli>-<n> run id on the response, exactly three demo.* bus
// events stamped lane=demo/uc=refusal-engine, a demo.exhibit record
// carrying the chain/input/refusal/semanticChange the fake child reported,
// and a passed run-history Record with the pinned verdict line.
func TestBridgingExhibit_RefusalProducesDemonstrationRun(t *testing.T) {
	const token = "bridging-exhibit-refusal-run-token"
	const refusalText = "shn: semantic-change refusal: pa.dtr 2.1->2.2 (up direction): no honest byte-level source for QuestionnaireResponse.extension:qr-coverage (ambiguous: 2 Coverage-referencing qr-context entries, multi-coverage source)"
	wantChain := []bridgingChainStep{{Module: "pa.dtr 2.1->2.2", From: "2.1", To: "2.2", Class: "full"}}
	srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		return http.StatusUnprocessableEntity, demoTransformWireRefusal{Refusal: refusalText, SemanticChange: true, Chain: wantChain}
	})

	cfg := bridgingExhibitTestConfig(t, token)
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
	if !demoRunIDPattern.MatchString(resp.RunID) {
		t.Fatalf("resp.RunID = %q, want to match %s", resp.RunID, demoRunIDPattern)
	}

	evs := demoEventsForRun(t, cfg.Bus, resp.RunID)
	if len(evs) != 3 {
		t.Fatalf("bus events for run %s = %d, want exactly 3 (started/exhibit/finished); got %+v", resp.RunID, len(evs), evs)
	}
	if evs[0].Type != event.TypeDemoStarted || evs[1].Type != event.TypeDemoExhibit || evs[2].Type != event.TypeDemoFinished {
		t.Fatalf("event types = %s/%s/%s, want demo.started/demo.exhibit/demo.finished", evs[0].Type, evs[1].Type, evs[2].Type)
	}
	for i, e := range evs {
		if e.Lane != "demo" || e.UC != "refusal-engine" {
			t.Fatalf("event[%d] lane=%q uc=%q, want lane=demo uc=refusal-engine", i, e.Lane, e.UC)
		}
	}
	if evs[2].Detail != demoVerdictRefusalExpected {
		t.Fatalf("demo.finished detail = %q, want %q", evs[2].Detail, demoVerdictRefusalExpected)
	}

	var rec demoRecord
	if err := json.Unmarshal(evs[1].Demo, &rec); err != nil {
		t.Fatalf("decode demo.exhibit record: %v (raw=%s)", err, evs[1].Demo)
	}
	if rec.Kind != "refusal-engine" {
		t.Fatalf("record.Kind = %q, want refusal-engine", rec.Kind)
	}
	if len(rec.Chain) != 1 || rec.Chain[0] != wantChain[0] {
		t.Fatalf("record.Chain = %+v, want %+v", rec.Chain, wantChain)
	}
	if string(rec.Input) != string(bridgingRefusalInput) {
		t.Fatalf("record.Input = %s, want the embedded refusal fixture verbatim", rec.Input)
	}
	if rec.Refusal != refusalText || !rec.SemanticChange {
		t.Fatalf("record refusal=%q semanticChange=%v, want %q / true", rec.Refusal, rec.SemanticChange, refusalText)
	}

	histRec, err := cfg.History.Get(resp.RunID)
	if err != nil {
		t.Fatalf("History.Get(%s): %v", resp.RunID, err)
	}
	if histRec == nil {
		t.Fatalf("History.Get(%s) = nil, want a saved record", resp.RunID)
	}
	if histRec.State != "passed" {
		t.Fatalf("history record state = %q, want passed", histRec.State)
	}
	if histRec.Detail != demoVerdictRefusalExpected {
		t.Fatalf("history record detail = %q, want %q", histRec.Detail, demoVerdictRefusalExpected)
	}
	if len(histRec.Events) != 3 {
		t.Fatalf("history record events = %d, want 3", len(histRec.Events))
	}
}

// TestBridgingExhibit_CarryProducesDemonstrationRun is the carry kind's
// mirror of the refusal test above: input/intermediate/output/restored/
// lossReports/chain in the demo.exhibit record, and the same three-event +
// history-record shape.
func TestBridgingExhibit_CarryProducesDemonstrationRun(t *testing.T) {
	const token = "bridging-exhibit-carry-run-token"
	downChain := []bridgingChainStep{{Module: "pa.dtr 2.2->2.1", From: "2.2", To: "2.1", Class: "carry"}}
	upChain := []bridgingChainStep{{Module: "pa.dtr 2.1->2.2", From: "2.1", To: "2.2", Class: "carry"}}
	srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		switch {
		case call.From == "2.2" && call.To == "2.1":
			return http.StatusOK, demoTransformWireResponse{
				Output: json.RawMessage(`{"down":"output"}`),
				LossReports: []bridgingLossReport{{
					Module: "pa.dtr 2.2->2.1", Source: "2.2", Target: "2.1",
					Carried: []bridgingLossEntry{{
						Path:   "QuestionnaireResponse.item.answer.value.extension:itemWeight",
						Detail: "carried; source line 2.2 (no 2.1 slot)",
					}},
				}},
				Chain: downChain,
			}
		case call.From == "2.1" && call.To == "2.2":
			return http.StatusOK, demoTransformWireResponse{
				Output:      json.RawMessage(bridgingCarryInput),
				LossReports: []bridgingLossReport{{Module: "pa.dtr 2.1->2.2", Source: "2.1", Target: "2.2"}},
				Chain:       upChain,
			}
		default:
			t.Fatalf("unexpected fake-child call: %+v", call)
			return 0, nil
		}
	})

	cfg := bridgingExhibitTestConfig(t, token)
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
	if !demoRunIDPattern.MatchString(resp.RunID) {
		t.Fatalf("resp.RunID = %q, want to match %s", resp.RunID, demoRunIDPattern)
	}

	evs := demoEventsForRun(t, cfg.Bus, resp.RunID)
	if len(evs) != 3 {
		t.Fatalf("bus events for run %s = %d, want exactly 3 (started/exhibit/finished); got %+v", resp.RunID, len(evs), evs)
	}
	if evs[0].Type != event.TypeDemoStarted || evs[1].Type != event.TypeDemoExhibit || evs[2].Type != event.TypeDemoFinished {
		t.Fatalf("event types = %s/%s/%s, want demo.started/demo.exhibit/demo.finished", evs[0].Type, evs[1].Type, evs[2].Type)
	}
	for i, e := range evs {
		if e.Lane != "demo" || e.UC != "carry-engine" {
			t.Fatalf("event[%d] lane=%q uc=%q, want lane=demo uc=carry-engine", i, e.Lane, e.UC)
		}
	}
	if evs[2].Detail != demoVerdictCarryRestored {
		t.Fatalf("demo.finished detail = %q, want %q", evs[2].Detail, demoVerdictCarryRestored)
	}

	var rec demoRecord
	if err := json.Unmarshal(evs[1].Demo, &rec); err != nil {
		t.Fatalf("decode demo.exhibit record: %v (raw=%s)", err, evs[1].Demo)
	}
	if rec.Kind != "carry-engine" {
		t.Fatalf("record.Kind = %q, want carry-engine", rec.Kind)
	}
	if string(rec.Input) != string(bridgingCarryInput) {
		t.Fatalf("record.Input = %s, want the embedded carry fixture verbatim", rec.Input)
	}
	if string(rec.Intermediate) != `{"down":"output"}` {
		t.Fatalf("record.Intermediate = %s, want the fake down-leg output", rec.Intermediate)
	}
	if string(rec.Output) != string(bridgingCarryInput) {
		t.Fatalf("record.Output = %s, want the fake up-leg output (byte-identical to the embedded input)", rec.Output)
	}
	if !rec.Restored {
		t.Fatalf("record.Restored = false, want true")
	}
	if len(rec.LossReports) != 2 {
		t.Fatalf("record.LossReports = %+v, want 2 entries (down leg + up leg)", rec.LossReports)
	}
	wantChain := append(append([]bridgingChainStep{}, downChain...), upChain...)
	if len(rec.Chain) != 2 || rec.Chain[0] != wantChain[0] || rec.Chain[1] != wantChain[1] {
		t.Fatalf("record.Chain = %+v, want %+v (down hop then up hop)", rec.Chain, wantChain)
	}

	histRec, err := cfg.History.Get(resp.RunID)
	if err != nil {
		t.Fatalf("History.Get(%s): %v", resp.RunID, err)
	}
	if histRec == nil {
		t.Fatalf("History.Get(%s) = nil, want a saved record", resp.RunID)
	}
	if histRec.State != "passed" || histRec.Detail != demoVerdictCarryRestored || len(histRec.Events) != 3 {
		t.Fatalf("history record = %+v, want state=passed detail=%q events len 3", histRec, demoVerdictCarryRestored)
	}
}

// TestBridgingExhibit_FailureEmitsNothing proves the 500-never-200 contract
// carries no side effect across EVERY one of handleBridgingExhibit's failure
// branches, not just the fixture-misbehavior one: pre-boot (503), a
// malformed body (400), an unreachable child (502), and a fixture that
// doesn't behave as its embedded content promises (500, "never fake it" —
// here, the refusal leg's crafted QR unexpectedly accepted). Each subtest
// leaves the bus's sequence counter and run history exactly as they were —
// zero events, zero history records, no phantom half-run.
func TestBridgingExhibit_FailureEmitsNothing(t *testing.T) {
	cases := []struct {
		name       string
		wantStatus int
		// setup configures StackInfo (and any fake child) for this case;
		// startDaemon has already run when it's called.
		setup func(t *testing.T, d *Daemon)
		// request drives the failing call and returns its status/body.
		request func(t *testing.T, apiBase, token string) (int, []byte)
	}{
		{
			name:       "preboot-503",
			wantStatus: http.StatusServiceUnavailable,
			setup:      func(t *testing.T, d *Daemon) {}, // StackInfo intentionally left at its zero value
			request: func(t *testing.T, apiBase, token string) (int, []byte) {
				return doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
			},
		},
		{
			name:       "badbody-400",
			wantStatus: http.StatusBadRequest,
			setup: func(t *testing.T, d *Daemon) {
				d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: "http://127.0.0.1:1/events"})
			},
			request: func(t *testing.T, apiBase, token string) (int, []byte) {
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
				b, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, b
			},
		},
		{
			name:       "childdown-502",
			wantStatus: http.StatusBadGateway,
			setup: func(t *testing.T, d *Daemon) {
				dead := httptest.NewServer(http.NewServeMux())
				dead.Close()
				d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: dead.URL + "/events"})
			},
			request: func(t *testing.T, apiBase, token string) (int, []byte) {
				return doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
			},
		},
		{
			name:       "fixture-misbehavior-500",
			wantStatus: http.StatusInternalServerError,
			setup: func(t *testing.T, d *Daemon) {
				srv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
					return http.StatusOK, demoTransformWireResponse{Output: json.RawMessage(bridgingRefusalInput)}
				})
				d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})
			},
			request: func(t *testing.T, apiBase, token string) (int, []byte) {
				return doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "refusal"})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := "bridging-exhibit-failure-nothing-" + tc.name + "-token"
			cfg := bridgingExhibitTestConfig(t, token)
			cfg.StateDir = t.TempDir()
			d, apiBase := startDaemon(t, cfg)
			tc.setup(t, d)

			beforeSeq := cfg.Bus.Seq()
			sumsBefore, err := cfg.History.List()
			if err != nil {
				t.Fatalf("History.List() before: %v", err)
			}

			status, body := tc.request(t, apiBase, token)
			if status != tc.wantStatus {
				t.Fatalf("POST /api/bridging/exhibit (%s) = %d, want %d (body=%s)", tc.name, status, tc.wantStatus, body)
			}

			if got := cfg.Bus.Seq(); got != beforeSeq {
				t.Fatalf("bus Seq() after a failed exhibit (%s) = %d, want unchanged %d (a failed exhibit must emit nothing)", tc.name, got, beforeSeq)
			}
			sumsAfter, err := cfg.History.List()
			if err != nil {
				t.Fatalf("History.List() after: %v", err)
			}
			if len(sumsAfter) != len(sumsBefore) {
				t.Fatalf("History.List() after a failed exhibit (%s) = %d entries, want unchanged %d", tc.name, len(sumsAfter), len(sumsBefore))
			}
		})
	}
}

// TestBridgingExhibit_NoWireShapedEvents is the honesty invariant pin: a
// demonstration run's events must never carry a wire-shaped Type — no
// "observer", "run.started", "run.finished", or "run.failed" ever appears
// anywhere on the bus after an exhibit runs, across both exhibit kinds. This
// asserts over the WHOLE bus (not filtered to the two demo run ids) because
// nothing else produces onto this daemon's bus in this test — so the
// stronger, unfiltered assertion also catches the failure mode a
// run-id-filtered check would miss: an observer frame emitted with an empty
// or borrowed run id. The exact event count (6 = 3 per exhibit x 2 exhibits)
// pins that nothing beyond the demo.started/demo.exhibit/demo.finished
// triple was emitted either.
func TestBridgingExhibit_NoWireShapedEvents(t *testing.T) {
	const token = "bridging-exhibit-no-wire-events-token"
	refusalSrv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		return http.StatusUnprocessableEntity, demoTransformWireRefusal{Refusal: "refused", SemanticChange: true}
	})
	carrySrv := newFakeDemoChild(t, func(call demoChildCall) (int, any) {
		if call.From == "2.2" {
			return http.StatusOK, demoTransformWireResponse{Output: json.RawMessage(`{"down":"output"}`)}
		}
		return http.StatusOK, demoTransformWireResponse{Output: json.RawMessage(bridgingCarryInput)}
	})

	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)

	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: refusalSrv.URL + "/events"})
	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "refusal"})
	if status != http.StatusOK {
		t.Fatalf("refusal exhibit = %d, want 200 (body=%s)", status, body)
	}
	var refusalResp bridgingExhibitRefusalResponse
	if err := json.Unmarshal(body, &refusalResp); err != nil {
		t.Fatalf("decode refusal response: %v (body=%s)", err, body)
	}

	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: carrySrv.URL + "/events"})
	status, body = doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
	if status != http.StatusOK {
		t.Fatalf("carry exhibit = %d, want 200 (body=%s)", status, body)
	}
	var carryResp bridgingExhibitCarryResponse
	if err := json.Unmarshal(body, &carryResp); err != nil {
		t.Fatalf("decode carry response: %v (body=%s)", err, body)
	}
	if !demoRunIDPattern.MatchString(refusalResp.RunID) || !demoRunIDPattern.MatchString(carryResp.RunID) {
		t.Fatalf("run ids = %q / %q, want both to match %s", refusalResp.RunID, carryResp.RunID, demoRunIDPattern)
	}
	if refusalResp.RunID == carryResp.RunID {
		t.Fatalf("refusal runId %q == carry runId %q, want distinct ids across two exhibits", refusalResp.RunID, carryResp.RunID)
	}

	forbidden := map[string]bool{
		event.TypeObserver:    true,
		event.TypeRunStarted:  true,
		event.TypeRunFinished: true,
		event.TypeRunFailed:   true,
	}
	all := cfg.Bus.Since(0)
	for _, e := range all {
		if forbidden[e.Type] {
			t.Fatalf("event %+v has a wire-shaped Type %q — the honesty invariant forbids it on the demonstration path", e, e.Type)
		}
	}
	if len(all) != 6 {
		t.Fatalf("bus event count after both exhibits = %d, want exactly 6 (3 per exhibit: demo.started/demo.exhibit/demo.finished)", len(all))
	}
}

// TestBridgingCapture_TokenGated proves GET /api/bridging/capture/{id} rides
// the SAME token gate every other /api/bridging/* route does — the check
// this test asserts comes entirely from authMiddleware wrapping the gated
// mux, not from anything inside handleBridgingCapture itself.
func TestBridgingCapture_TokenGated(t *testing.T) {
	const token = "bridging-capture-gate-token"
	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	_, apiBase := startDaemon(t, cfg)

	status, _ := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/corr-1", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("GET /api/bridging/capture/corr-1 without token = %d, want 401", status)
	}
}

// TestBridgingCapture_PreBoot503 proves the same daemon-first gate
// handleBridgingExhibit uses: StackInfo.ObserverURL reads "" until the
// first SetStackInfo call, so this proxy has nowhere to forward to yet.
func TestBridgingCapture_PreBoot503(t *testing.T) {
	const token = "bridging-capture-preboot-token"
	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	_, apiBase := startDaemon(t, cfg)
	// StackInfo intentionally left at its zero value: ObserverURL == "".

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/corr-1", token, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/bridging/capture/corr-1 pre-boot = %d, want 503 (body=%s)", status, body)
	}
}

// TestBridgingCapture_BadID400 proves the path-escape guard rejects an
// empty id BEFORE this proxy ever builds a child URL. Exercised directly
// against the handler (bypassing the daemon's real mux routing) because a
// genuinely empty final path segment is UNREACHABLE through a real routed
// request: it doesn't match the {correlationId} wildcard at all —
// net/http's ServeMux falls through to its own plain-text 404 instead, the
// same gateway-side gap gateway/app/demo_endpoint_test.go's
// TestDemoCaptureEndpoint_EmptyID404 documents. Handing "" to the handler
// directly via r.SetPathValue is the only way to exercise the guard's empty
// branch at all. The slash-carrying rows this guard also rejects ARE
// reachable through a real routed request (a percent-encoded slash decodes
// into PathValue without matching as a path separator) — those are proven
// end-to-end below in TestBridgingCapture_BadID400Routed instead.
func TestBridgingCapture_BadID400(t *testing.T) {
	const token = "bridging-capture-badid-token"
	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, _ := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: "http://127.0.0.1:1/events"})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/bridging/capture/x", nil)
	req.SetPathValue("correlationId", "")
	d.handleBridgingCapture(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("id=%q status = %d, want 400 (body=%s)", "", rr.Code, rr.Body.String())
	}
}

// TestBridgingCapture_BadID400Routed proves the routing-to-guard
// integration end to end: a real GET against the daemon's actual mux, with
// a percent-encoded slash inside the single {correlationId} path segment,
// decodes to a slash-carrying id (PathValue unescapes %2F to a literal '/'
// without ServeMux ever treating it as a path separator) and still comes
// back 400 — proving the guard genuinely fires on the routed path, not just
// when handed a slash-carrying id directly.
func TestBridgingCapture_BadID400Routed(t *testing.T) {
	const token = "bridging-capture-badid-routed-token"
	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: "http://127.0.0.1:1/events"})

	for _, rawSegment := range []string{"..%2Fx", "a%2Fb", "%2Fleading"} {
		status, body := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/"+rawSegment, token, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("GET /api/bridging/capture/%s = %d, want 400 (body=%s)", rawSegment, status, body)
		}
	}
}

// TestBridgingCapture_BareDotSegmentRouted400 proves the SLASH-FREE
// dot-segment guard: "%2E%2E" and "%2E" decode (via PathValue) to the
// literal single-segment ids ".." and "." — neither carries a '/', so
// invalidBridgingCaptureID's slash check alone would let them through and
// build a child URL from a bare dot-segment, which a downstream HTTP
// server's own path-cleaning could resolve to an entirely different path
// than the one this proxy's contract promises. Routed end to end (not
// handed to the handler directly) so this proves the guard fires on the
// real mux path, mirroring TestBridgingCapture_BadID400Routed's posture for
// the slash-carrying cases.
func TestBridgingCapture_BareDotSegmentRouted400(t *testing.T) {
	const token = "bridging-capture-dotsegment-routed-token"
	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: "http://127.0.0.1:1/events"})

	for _, rawSegment := range []string{"%2E%2E", "%2E"} {
		status, body := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/"+rawSegment, token, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("GET /api/bridging/capture/%s = %d, want 400 (body=%s)", rawSegment, status, body)
		}
	}
}

// TestBridgingCapture_ChildRedirect502 proves this proxy never follows a
// redirect from the gateway child: a fake child that answers 301/302
// instead of 200/404 must come back as this proxy's own 502, never the
// followed-redirect target's body — a redirect is not one of the two
// shapes handleDemoCapture's wire contract promises (200 or 404 only), and
// silently following it would break the verbatim-passthrough contract this
// proxy exists to uphold.
func TestBridgingCapture_ChildRedirect502(t *testing.T) {
	const token = "bridging-capture-redirect-token"
	const secretBody = `{"secret":"never returned by the proxy"}`

	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo/capture/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("GET /elsewhere", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(secretBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/corr-1", token, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("GET /api/bridging/capture/corr-1, child answers 302 = %d, want 502 (body=%s)", status, body)
	}
	if strings.Contains(string(body), "secret") {
		t.Fatalf("body = %s, want it to NEVER contain the redirect target's body", body)
	}
}

// TestBridgingCapture_ResponseTooLarge502 proves a body one byte past
// bridgingResponseBytesLimit is reported as an error, never silently
// truncated to the cap and passed through as if it were the whole thing.
func TestBridgingCapture_ResponseTooLarge502(t *testing.T) {
	const token = "bridging-capture-toolarge-token"
	oversized := bytes.Repeat([]byte("a"), bridgingResponseBytesLimit+1)
	srv := newFakeCaptureChild(t, func(id string) (int, []byte) {
		return http.StatusOK, oversized
	})

	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/corr-1", token, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("GET /api/bridging/capture/corr-1, oversized body = %d, want 502", status)
	}
	if strings.Contains(string(body), strings.Repeat("a", 100)) {
		t.Fatalf("body echoes the oversized payload, want it to NEVER be passed through")
	}
}

// TestBridgingCapture_ChildDown502 proves an unreachable gateway child
// answers 502, mirroring TestBridgingExhibit_ChildUnreachable502's
// dead-server trick.
func TestBridgingCapture_ChildDown502(t *testing.T) {
	const token = "bridging-capture-unreachable-token"
	dead := httptest.NewServer(http.NewServeMux())
	dead.Close()

	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: dead.URL + "/events"})

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/corr-1", token, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("GET /api/bridging/capture/corr-1, unreachable child = %d, want 502 (body=%s)", status, body)
	}
}

// TestBridgingCapture_PassthroughBodies proves the gateway's own capture
// wire contract (gateway/app/demo_endpoint.go's GET
// /demo/capture/{correlationId}) survives this proxy byte-for-byte: the
// 200 body and both distinct 404 bodies (flag off vs. flag on but
// missing) arrive at the caller exactly as the child sent them — no
// decode/re-marshal round trip to drift them.
func TestBridgingCapture_PassthroughBodies(t *testing.T) {
	const token = "bridging-capture-passthrough-token"
	const okBody = `{"correlationId":"corr-1","legType":"pas-submit","contract":"pa.pas","from":"2.1","to":"2.2","chain":[{"module":"pa.pas 2.1->2.2","from":"2.1","to":"2.2","class":"full"}],"lossReports":[],"before":{"a":1},"after":{"a":2},"capturedAt":"2026-08-16T00:00:00Z"}`
	const notEnabledBody = `{"error":"edge capture is not enabled"}`
	const noCaptureBody = `{"error":"no capture for this leg"}`

	tests := []struct {
		name   string
		id     string
		status int
		body   string
	}{
		{"ok", "corr-1", http.StatusOK, okBody},
		{"flag-off", "corr-2", http.StatusNotFound, notEnabledBody},
		{"missing", "corr-3", http.StatusNotFound, noCaptureBody},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeCaptureChild(t, func(id string) (int, []byte) {
				if id != tc.id {
					t.Fatalf("fake capture child called with id %q, want %q", id, tc.id)
				}
				return tc.status, []byte(tc.body)
			})

			cfg := bridgingExhibitTestConfig(t, token)
			cfg.StateDir = t.TempDir()
			d, apiBase := startDaemon(t, cfg)
			d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

			status, body := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/"+tc.id, token, nil)
			if status != tc.status {
				t.Fatalf("GET /api/bridging/capture/%s = %d, want %d (body=%s)", tc.id, status, tc.status, body)
			}
			if string(body) != tc.body {
				t.Fatalf("body = %s, want byte-identical %s", body, tc.body)
			}
		})
	}
}

// TestBridgingCapture_ChildUnexpectedStatus502 proves a status code the
// child's own two-endpoint contract never produces (200 or 404 only — see
// handleDemoCapture's doc comment) is not passed through verbatim: the proxy
// wraps it into its own 502, naming the unexpected code, rather than
// forwarding a shape a caller has no contract for.
func TestBridgingCapture_ChildUnexpectedStatus502(t *testing.T) {
	const token = "bridging-capture-unexpected-status-token"
	srv := newFakeCaptureChild(t, func(id string) (int, []byte) {
		return http.StatusInternalServerError, []byte(`{"error":"boom"}`)
	})

	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodGet, apiBase+"/api/bridging/capture/corr-1", token, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%s)", status, body)
	}
	if !strings.Contains(string(body), "unexpected status 500") {
		t.Fatalf("body = %s, want it to name the unexpected status", body)
	}
}

// TestBridgingExhibit_ChildResponseTooLarge502 proves callDemoTransform gets
// the same truncation-detection readBridgingResponseBody gives the capture
// proxy (TestBridgingCapture_ResponseTooLarge502): a /demo/transform
// response one byte past bridgingResponseBytesLimit is reported as a 502,
// never silently truncated to the cap and decoded as if it were the
// complete response.
func TestBridgingExhibit_ChildResponseTooLarge502(t *testing.T) {
	const token = "bridging-exhibit-toolarge-token"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /demo/transform", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("a"), bridgingResponseBytesLimit+1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := bridgingExhibitTestConfig(t, token)
	cfg.StateDir = t.TempDir()
	d, apiBase := startDaemon(t, cfg)
	d.SetStackInfo(StackInfo{Validator: "stand-in", ObserverURL: srv.URL + "/events"})

	status, body := doJSON(t, http.MethodPost, apiBase+"/api/bridging/exhibit", token, map[string]string{"kind": "carry"})
	if status != http.StatusBadGateway {
		t.Fatalf("POST /api/bridging/exhibit carry, oversized child response = %d, want 502 (body=%s)", status, body)
	}
}
