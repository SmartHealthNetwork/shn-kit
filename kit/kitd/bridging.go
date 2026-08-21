// bridging.go — POST /api/bridging/exhibit: proxies the gateway
// child's loopback POST /demo/transform (gateway/app/demo_endpoint.go —
// engine.RunTransformChain exported for exactly this) over embedded
// reference content, so the Kit's UI can show a real cross-version transform
// round trip (the carry mechanism) and a real semantic-change refusal
// without touching a live scenario run or a running gateway child's
// observable state — engine.RunTransformChain never appears on the observer
// SSE stream either (demo_endpoint.go's own doc), so this exhibit is its own
// record, same as the endpoint it calls.
//
// This package does NOT import gateway/engine for the wire types: kit's
// go.mod pins a published shn-gateway release (workspace mode/go.work is a
// local-dev convenience, not what a built kit binary resolves against), and
// the real coupling this endpoint has to the gateway is the wire contract
// POST /demo/transform speaks over HTTP — so the types below mirror that
// wire shape byte-for-byte (same JSON tags as gateway/app/demo_endpoint.go's
// demoTransformRequest/Response/Refusal and gateway/engine/transform.go's
// LossReport/LossEntry) rather than depending on engine's Go types.
package kitd

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/runhistory"
)

// bridgingCarryInput is the embedded golden QR (2.2, with a hand-injected
// item.answer.value.extension:itemWeight — the locus the extension's own SD
// contexts it to) the "carry" exhibit runs down (2.2->2.1,
// carries itemWeight into shn-carried-content) then up (2.1->2.2, restores
// it) — see bridgingassets/README.md for full provenance and the
// regeneration recipe.
//
//go:embed bridgingassets/carry-input-2.2.json
var bridgingCarryInput []byte

// bridgingRefusalInput is the embedded crafted QR (2.1 golden plus a second
// Coverage-referencing qr-context entry) the "refusal" exhibit runs up
// (2.1->2.2) — dtrRelocateCoverageUp's ambiguous-source case, a genuine
// *engine.SemanticChangeError. See bridgingassets/README.md.
//
//go:embed bridgingassets/refusal-input-2.1.json
var bridgingRefusalInput []byte

// bridgingExhibitTimeout bounds POST /api/bridging/exhibit end to end,
// including the carry kind's TWO sequential /demo/transform calls. Both
// calls are loopback, in-process compute over a small fixed payload (no
// network hop, no I/O beyond JSON marshal/unmarshal) — this ceiling is
// generous headroom against a wedged child, not a budget either call is
// expected to approach (same reasoning as kitd.go's verifyTimeout for
// bootstrap.Verify's re-probe).
const bridgingExhibitTimeout = 15 * time.Second

// bridgingHTTPClient posts to the gateway child's own loopback listener —
// same machine, no real network hop — but still time-bounded defensively;
// no outbound call in this package is ever unbounded. CheckRedirect refuses
// every redirect: both callers below (callDemoTransform and
// handleBridgingCapture) exist to relay THIS child's own answer verbatim —
// a redirect (a mux quirk, e.g. a trailing-slash rewrite, or a hijacked/
// misbehaving child) points this client at a response that never came from
// the endpoint it asked, and forwarding that body as though it did would
// break the verbatim-passthrough contract both callers depend on. Returning
// a non-nil error here makes net/http report the previous (redirect)
// response with its body already closed, plus this error — the caller's own
// "err != nil" branch (already present at every call site) turns it into a
// 502, never a followed-redirect body.
var bridgingHTTPClient = &http.Client{
	Timeout: bridgingExhibitTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return fmt.Errorf("refusing to follow redirect to %s", req.URL)
	},
}

// bridgingResponseBytesLimit bounds how much of a /demo/transform response
// body this proxy will read — mirrors shnsdk.MaxRequestBytes (8 MiB), the
// same bound demo_endpoint.go applies to the inbound side of that same
// exchange. The Kit doesn't import shnsdk here for one constant; the value
// is cited, not re-derived.
const bridgingResponseBytesLimit = 8 << 20

// errBridgingResponseTooLarge is readBridgingResponseBody's sentinel: the
// child's response body exceeded bridgingResponseBytesLimit (a body of
// exactly the limit is not an error — the check is strict >).
var errBridgingResponseTooLarge = fmt.Errorf("response body exceeds the %d byte limit", bridgingResponseBytesLimit)

// readBridgingResponseBody reads body up to bridgingResponseBytesLimit,
// detecting truncation instead of silently returning a truncated body: it
// reads ONE byte past the cap, so a body that is exactly at the cap (len ==
// bridgingResponseBytesLimit) and a body that is larger (len >
// bridgingResponseBytesLimit, read stops at cap+1) are distinguishable —
// the latter errors out rather than passing through as if it were the
// complete body. Both callers below (callDemoTransform's decode path and
// handleBridgingCapture's verbatim passthrough) share this: a silently
// truncated response is never something either proxy should report on.
func readBridgingResponseBody(body io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(body, bridgingResponseBytesLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > bridgingResponseBytesLimit {
		return nil, errBridgingResponseTooLarge
	}
	return b, nil
}

// --- wire shapes mirroring gateway/app/demo_endpoint.go + gateway/engine/transform.go ---

// demoTransformWireRequest is POST /demo/transform's request body, verbatim.
type demoTransformWireRequest struct {
	Contract string          `json:"contract"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	Payload  json.RawMessage `json:"payload"`
}

// bridgingLossEntry mirrors engine.LossEntry's JSON shape.
type bridgingLossEntry struct {
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

// bridgingLossReport mirrors engine.LossReport's JSON shape.
type bridgingLossReport struct {
	Module      string              `json:"module"`
	Source      string              `json:"source"`
	Target      string              `json:"target"`
	Carried     []bridgingLossEntry `json:"carried,omitempty"`
	Synthesized []bridgingLossEntry `json:"synthesized,omitempty"`
}

// bridgingChainStep mirrors engine.ChainStep's JSON shape
// (gateway/engine/observer.go) — one hop of the compatibility chain a
// /demo/transform call walked (or attempted), same Module rendering
// ("pa.dtr 2.1->2.2") as LossReport.Module.
type bridgingChainStep struct {
	Module string `json:"module"`
	From   string `json:"from"`
	To     string `json:"to"`
	Class  string `json:"class"` // "full" | "carry" | "gated"
}

// demoTransformWireResponse is POST /demo/transform's 200 body, verbatim.
type demoTransformWireResponse struct {
	Output      json.RawMessage      `json:"output"`
	LossReports []bridgingLossReport `json:"lossReports"`
	Chain       []bridgingChainStep  `json:"chain,omitempty"`
}

// demoTransformWireRefusal is POST /demo/transform's 422 body, verbatim.
type demoTransformWireRefusal struct {
	Refusal        string              `json:"refusal"`
	SemanticChange bool                `json:"semanticChange"`
	Chain          []bridgingChainStep `json:"chain,omitempty"`
}

// --- POST /api/bridging/exhibit wire shapes ---

// bridgingExhibitRequest is POST /api/bridging/exhibit's body.
type bridgingExhibitRequest struct {
	Kind string `json:"kind"` // "carry" | "refusal"
}

// bridgingExhibitCarryResponse is the "carry" kind's 200 body. RunID
// identifies the local-demonstration run this exhibit orchestrated
// (emitDemoRun) — the panel's link into the inspector.
type bridgingExhibitCarryResponse struct {
	Kind        string               `json:"kind"` // "carry"
	LossReports []bridgingLossReport `json:"lossReports"`
	Restored    bool                 `json:"restored"`
	RunID       string               `json:"runId"`
}

// bridgingExhibitRefusalResponse is the "refusal" kind's 200 body — the
// EXHIBIT's response is 200 even though the child's own leg was refused
// (422): the exhibit successfully demonstrated a refusal, which is its whole
// point, so the outer HTTP status reports the exhibit's own success. RunID
// identifies the local-demonstration run this exhibit orchestrated
// (emitDemoRun) — the panel's link into the inspector.
type bridgingExhibitRefusalResponse struct {
	Kind           string `json:"kind"` // "refusal"
	Refusal        string `json:"refusal"`
	SemanticChange bool   `json:"semanticChange"`
	RunID          string `json:"runId"`
}

// demoVerdictRefusalExpected and demoVerdictCarryRestored are the
// demonstration record's pinned one-line verdicts — PINNED WIRE CONTRACT:
// these exact strings ride the demo.finished event's Detail into the saved
// run-history Record's Summary.Detail, and the Kit UI renders/asserts these
// same literals verbatim. Never reword without updating every consumer in
// lockstep.
const (
	demoVerdictRefusalExpected = "refused as expected"
	demoVerdictCarryRestored   = "restored exactly"
)

// demoRecord is the demonstration record the demo.exhibit event carries —
// the inspector's data source for a local demonstration run. Payload fields
// are raw JSON (the embedded fixtures / child responses, byte-faithful).
//
// SemanticChange deliberately has NO omitempty, unlike this struct's other
// refusal-only field (Refusal): the wire's own demoTransformWireRefusal.
// SemanticChange can genuinely be false (a refusal that ISN'T a typed
// semantic-change error — see gateway/app/demo_endpoint.go's
// errors.As(err, &scErr)), so omitting it on false would make "false" and
// "absent because this is a carry-engine record" indistinguishable on the
// wire. Matches bridgingExhibitRefusalResponse's sibling field, which has
// the same non-omitempty tag for the same reason.
type demoRecord struct {
	Kind           string               `json:"kind"` // "refusal-engine" | "carry-engine"
	Contract       string               `json:"contract"`
	Chain          []bridgingChainStep  `json:"chain"`
	Input          json.RawMessage      `json:"input"`
	Refusal        string               `json:"refusal,omitempty"`
	SemanticChange bool                 `json:"semanticChange"`
	Intermediate   json.RawMessage      `json:"intermediate,omitempty"`
	Output         json.RawMessage      `json:"output,omitempty"`
	Restored       bool                 `json:"restored,omitempty"`
	LossReports    []bridgingLossReport `json:"lossReports,omitempty"`
}

// handleBridgingExhibit serves POST /api/bridging/exhibit: body
// {"kind":"carry"|"refusal"} runs the matching embedded fixture through the
// gateway child's real POST /demo/transform.
//
//   - 503 before the gateway child's ObserverURL is known (daemon-first:
//     StackInfo.ObserverURL reads "" until the first SetStackInfo call —
//     same pre-boot gate as the per-child restart route and the bridging
//     demo toggle).
//   - 400 on an undecodable body or an unrecognized kind.
//   - 502 when the gateway child's observer listener is unreachable, or
//     answers something this proxy doesn't understand (never a transform
//     result to report on).
//   - 500 when the exhibit's own fixtures don't behave as the embedded
//     content promises — an unexpected refusal on the carry legs, an
//     unexpected acceptance on the refusal leg, or a carry round trip that
//     isn't byte-identical to its input. These indicate the embedded assets
//     and the live transform chain have drifted (bridgingassets/README.md's
//     staleness caveat) — never faked as a false-positive 200.
//   - 200 with the kind-specific body on success.
func (d *Daemon) handleBridgingExhibit(w http.ResponseWriter, r *http.Request) {
	observerURL := d.getStackInfo().ObserverURL
	if observerURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stack not started"})
		return
	}

	var req bridgingExhibitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode request body: %v", err)})
		return
	}

	demoURL := demoTransformURL(observerURL)
	// d.baseCtx is deliberately NOT used here (contrast handleBridgingDemo's
	// gateway-restart call): this is a plain read-only proxy request whose
	// entire lifetime is the HTTP request itself, so r.Context() (bounded
	// further by bridgingExhibitTimeout below) is the right ctx — a client
	// disconnect should cancel the in-flight child calls, not outlive them.
	ctx, cancel := context.WithTimeout(r.Context(), bridgingExhibitTimeout)
	defer cancel()

	switch req.Kind {
	case "carry":
		d.handleBridgingCarry(ctx, w, demoURL)
	case "refusal":
		d.handleBridgingRefusal(ctx, w, demoURL)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown kind %q (want \"carry\" or \"refusal\")", req.Kind)})
	}
}

// handleBridgingCarry runs the embedded itemWeight-bearing 2.2 QR down
// (pa.dtr 2.2->2.1, the direction whose carry genuinely fires) then up
// (pa.dtr 2.1->2.2, restore), reports whether the round trip was
// byte-identical to the embedded input, and — only once that "never fake
// it" check has genuinely passed — orchestrates the local-demonstration run
// (emitDemoRun) that the response's runId identifies. A *Daemon method (not
// a package-level func) so it can reach d.cfg.Bus/d.cfg.History/d.now/
// d.demoSeq; the caller's switch in handleBridgingExhibit passes d through.
func (d *Daemon) handleBridgingCarry(ctx context.Context, w http.ResponseWriter, demoURL string) {
	down, downRefusal, err := callDemoTransform(ctx, demoURL, "pa.dtr", "2.2", "2.1", bridgingCarryInput)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if downRefusal != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "carry exhibit: unexpected refusal on the down leg (pa.dtr 2.2->2.1): " + downRefusal.Refusal,
		})
		return
	}

	up, upRefusal, err := callDemoTransform(ctx, demoURL, "pa.dtr", "2.1", "2.2", down.Output)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if upRefusal != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "carry exhibit: unexpected refusal on the up leg (pa.dtr 2.1->2.2): " + upRefusal.Refusal,
		})
		return
	}

	if !bytes.Equal([]byte(up.Output), bridgingCarryInput) {
		// Never fake it: a divergent round trip is reported loudly, not
		// papered over with Restored:true — and no demonstration run is
		// orchestrated below, so the bus/history stay untouched too.
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "carry exhibit: round trip was not byte-identical to the embedded input",
		})
		return
	}

	// Walk order: the down leg's reports first, then the up leg's — mirroring the
	// two calls above (2.2->2.1 then 2.1->2.2), not sorted or otherwise reordered.
	reports := make([]bridgingLossReport, 0, len(down.LossReports)+len(up.LossReports))
	reports = append(reports, down.LossReports...)
	reports = append(reports, up.LossReports...)
	chain := make([]bridgingChainStep, 0, len(down.Chain)+len(up.Chain))
	chain = append(chain, down.Chain...)
	chain = append(chain, up.Chain...)

	runID, err := d.emitDemoRun("carry-engine", demoRecord{
		Kind:         "carry-engine",
		Contract:     "pa.dtr",
		Chain:        chain,
		Input:        json.RawMessage(bridgingCarryInput),
		Intermediate: down.Output,
		Output:       up.Output,
		Restored:     true,
		LossReports:  reports,
	}, demoVerdictCarryRestored)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "carry exhibit: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, bridgingExhibitCarryResponse{Kind: "carry", LossReports: reports, Restored: true, RunID: runID})
}

// handleBridgingRefusal runs the embedded crafted multi-coverage 2.1 QR up
// (pa.dtr 2.1->2.2), expects — and surfaces — the child's typed
// semantic-change refusal, and orchestrates the local-demonstration run the
// response's runId identifies (see handleBridgingCarry's doc for the
// *Daemon-method rationale).
func (d *Daemon) handleBridgingRefusal(ctx context.Context, w http.ResponseWriter, demoURL string) {
	_, refusal, err := callDemoTransform(ctx, demoURL, "pa.dtr", "2.1", "2.2", bridgingRefusalInput)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if refusal == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "refusal exhibit: expected the crafted multi-coverage QR to be refused, but the gateway accepted it",
		})
		return
	}

	runID, err := d.emitDemoRun("refusal-engine", demoRecord{
		Kind:           "refusal-engine",
		Contract:       "pa.dtr",
		Chain:          refusal.Chain,
		Input:          json.RawMessage(bridgingRefusalInput),
		Refusal:        refusal.Refusal,
		SemanticChange: refusal.SemanticChange,
	}, demoVerdictRefusalExpected)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "refusal exhibit: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, bridgingExhibitRefusalResponse{Kind: "refusal", Refusal: refusal.Refusal, SemanticChange: refusal.SemanticChange, RunID: runID})
}

// emitDemoRun mints a fresh local-demonstration run id
// (demo-<unixMilli>-<n>, the runner's own nextRunID idiom —
// kit/runner/runner.go:206-216 — clock-prefixed so a kitd restart never
// re-mints a previous session's id against the disk-persisted history),
// emits the demo.started -> demo.exhibit -> demo.finished triple for it
// (never any observer/run.* type — the honesty invariant a local
// demonstration must never be mistaken for a genuine wire exchange), saves
// the resulting Record to run history when one is configured, and returns
// the run id for the caller's HTTP response body. Called from the success
// path ONLY — a failed exhibit (the 500-never-200 "never fake it" branches
// above) never reaches here, so it emits nothing and saves nothing.
//
// rec is marshaled BEFORE anything touches the bus: rec is built entirely
// from this package's own struct/[]byte fields (the embedded fixtures and
// the child's own decoded JSON), so a marshal failure here is near
// impossible in practice — but if it ever did happen, the demonstration has
// not demonstrably succeeded, so the honest branch is the same "never fake
// it" contract every other guard in this file follows: return an error and
// emit/save NOTHING, rather than shipping a demo.exhibit event with a
// silently-empty payload. Marshaling first (not after demo.started is
// already on the bus) is what makes "emit nothing" true on this path too.
func (d *Daemon) emitDemoRun(uc string, rec demoRecord, verdict string) (string, error) {
	demoJSON, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("marshal demonstration record: %w", err)
	}

	cursor := d.cfg.Bus.Seq()
	runID := fmt.Sprintf("demo-%d-%d", d.now().UnixMilli(), d.demoSeq.Add(1))

	d.cfg.Bus.Emit(event.Event{Type: event.TypeDemoStarted, RunID: runID, Lane: "demo", UC: uc})
	d.cfg.Bus.Emit(event.Event{Type: event.TypeDemoExhibit, RunID: runID, Lane: "demo", UC: uc, Demo: demoJSON})
	d.cfg.Bus.Emit(event.Event{Type: event.TypeDemoFinished, RunID: runID, Lane: "demo", UC: uc, Detail: verdict})

	events := d.cfg.Bus.SinceRun(cursor, runID)
	if d.cfg.History != nil {
		// t falls back to the daemon's injected clock when events comes back
		// empty — SinceRun reads the bus's bounded ring (bufSize 5000), which
		// can in principle have evicted everything between cursor and now
		// under enough concurrent traffic; events[0] would panic in that
		// case. Same fallback shape as runhistory.Recorder.RunCompleted
		// (runhistory.go's t := rc.now(); if len(matched) > 0 { ... }).
		t := d.now()
		if len(events) > 0 {
			t = events[0].Time
		}
		histRec := runhistory.Record{
			Summary: runhistory.Summary{
				RunID:      runID,
				Lane:       "demo",
				UC:         uc,
				Branch:     "",
				State:      "passed",
				Detail:     verdict,
				Time:       t,
				EventCount: len(events),
			},
			Events: events,
		}
		// nil History already skips this whole block above (the
		// nil-Config-field contract); a Save failure here is logged, never
		// surfaced as a response failure — the exhibit itself already
		// succeeded and its HTTP response has already been decided by the
		// caller.
		if err := d.cfg.History.Save(histRec); err != nil {
			log.Printf("kitd: save local-demonstration run %s to history: %v", runID, err)
		}
	}
	return runID, nil
}

// demoTransformURL derives the gateway child's POST /demo/transform URL from
// its ObserverURL ("http://host:port/events" — kitd.Stack.ObserverURL's own
// doc): both live on the same loopback listener
// (gateway/app/demo_endpoint.go's composeObserverHandler serves both on one
// mux).
func demoTransformURL(observerURL string) string {
	return strings.TrimSuffix(observerURL, "/events") + "/demo/transform"
}

// demoCaptureURL derives the gateway child's GET /demo/capture/{id} URL
// from its ObserverURL, the same base demoTransformURL derives
// POST /demo/transform's URL from — both demo endpoints live on the SAME
// observer loopback mux (gateway/app/demo_endpoint.go's
// composeObserverHandler). id is escaped as a URL path segment; callers
// must already have rejected any id carrying a literal '/'
// (invalidBridgingCaptureID) before reaching here — this function does not
// re-check that.
func demoCaptureURL(observerURL, id string) string {
	return strings.TrimSuffix(observerURL, "/events") + "/demo/capture/" + url.PathEscape(id)
}

// invalidBridgingCaptureID reports whether id is unsafe to build a child
// URL from: empty, carrying a literal '/' (which could smuggle an extra
// path segment, or a path-escape attempt like "../x", past this route's
// single-segment {correlationId} wildcard), or equal to "." or ".." outright
// — a bare dot-segment carries no slash at all, so the '/'-rejection above
// does not catch it, but url.PathEscape passes it through unchanged and a
// downstream HTTP server's own path-cleaning could still resolve it to a
// different, unintended path. Any id containing a slash is already rejected
// by the check above, so a single equality check against the two bare
// dot-segment forms is enough — no id both carries a slash-free "." or ".."
// AND needs the '/' check too. Checked BEFORE demoCaptureURL is ever
// called, so a rejected id never reaches URL construction at all.
func invalidBridgingCaptureID(id string) bool {
	return id == "" || id == "." || id == ".." || strings.Contains(id, "/")
}

// handleBridgingCapture serves GET /api/bridging/capture/{correlationId}: a
// read-only proxy to this participant's own gateway child's loopback
// GET /demo/capture/{correlationId} (gateway/app/demo_endpoint.go's
// handleDemoCapture) — the participant's own pre-seal before/after payload
// pair for a leg it already transformed, read back by correlation id for
// the bridging inspector to render. Mirrors handleBridgingExhibit's proxy
// structure (same demoCaptureURL/demoTransformURL-derived base, same
// bridgingHTTPClient/bridgingExhibitTimeout, same
// bridgingResponseBytesLimit cap) but, unlike that exhibit endpoint, this
// proxy never decodes the child's body into a typed shape: the 200 and 404
// responses are relayed byte-for-byte, verbatim. This proxy never caches
// and never rewrites what the participant's own gateway captured — it only
// ever reads that gateway's own store back to that gateway's own operator.
//
//   - 503 before the gateway child's ObserverURL is known (same pre-boot
//     gate as POST /api/bridging/exhibit: StackInfo.ObserverURL reads ""
//     until the first SetStackInfo call).
//   - 400 when correlationId is empty or carries a literal '/'
//     (invalidBridgingCaptureID) — rejected before any child URL is built.
//   - 502 when the gateway child's observer listener is unreachable, or
//     answers a status other than 200/404 — the wire contract promises
//     only those two, so anything else is a response shape this proxy
//     doesn't recognize, never forwarded.
//   - 200/404 with the child's own body, verbatim, size-capped at
//     bridgingResponseBytesLimit.
func (d *Daemon) handleBridgingCapture(w http.ResponseWriter, r *http.Request) {
	observerURL := d.getStackInfo().ObserverURL
	if observerURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stack not started"})
		return
	}

	id := r.PathValue("correlationId")
	if invalidBridgingCaptureID(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid correlation id %q", id)})
		return
	}

	captureURL := demoCaptureURL(observerURL, id)
	// Same read-only, request-lifetime ctx posture as handleBridgingExhibit
	// above: this is a plain proxy GET, not an orchestrated multi-call
	// sequence, so r.Context() (bounded further by bridgingExhibitTimeout)
	// is the right ctx — a client disconnect cancels the in-flight child
	// call rather than outliving it.
	ctx, cancel := context.WithTimeout(r.Context(), bridgingExhibitTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, captureURL, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("build capture request: %v", err)})
		return
	}
	resp, err := bridgingHTTPClient.Do(httpReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("gateway demo capture endpoint unreachable at %s: %v", captureURL, err)})
		return
	}
	defer resp.Body.Close()

	body, err := readBridgingResponseBody(resp.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("read capture response from %s: %v", captureURL, err)})
		return
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		// Verbatim passthrough: this proxy never decodes or re-marshals the
		// child's own body — the bytes it read above are exactly the bytes
		// it writes back, unchanged.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body) // headers already sent; nothing left to do on a write failure
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("gateway demo capture endpoint %s: unexpected status %d", captureURL, resp.StatusCode)})
	}
}

// callDemoTransform posts one contract@from->to request to the gateway
// child's POST /demo/transform and classifies the response into exactly one
// of:
//   - (response, nil, nil) on 200 — response.Output/.LossReports/.Chain are
//     the leg's real output, its loss reports, and the hop(s) it walked.
//   - (nil, refusal, nil) on 422 — refusal.Chain is the hop(s) it attempted
//     before refusing.
//   - (nil, nil, err) for anything else — network failure, a
//     non-200/422 status, or a response body that doesn't decode as the
//     status code's expected shape. Callers map a non-nil err to 502: it
//     means the child was unreachable or answered something this proxy
//     doesn't understand, never a transform result to report on.
func callDemoTransform(ctx context.Context, demoURL, contract, from, to string, payload []byte) (*demoTransformWireResponse, *demoTransformWireRefusal, error) {
	reqBody, err := json.Marshal(demoTransformWireRequest{Contract: contract, From: from, To: to, Payload: json.RawMessage(payload)})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal /demo/transform request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, demoURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, fmt.Errorf("build /demo/transform request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := bridgingHTTPClient.Do(httpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway demo endpoint unreachable at %s: %w", demoURL, err)
	}
	defer resp.Body.Close()

	body, err := readBridgingResponseBody(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read /demo/transform response from %s: %w", demoURL, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var wr demoTransformWireResponse
		if err := json.Unmarshal(body, &wr); err != nil {
			return nil, nil, fmt.Errorf("decode /demo/transform 200 response from %s: %w", demoURL, err)
		}
		if wr.LossReports == nil {
			wr.LossReports = []bridgingLossReport{}
		}
		return &wr, nil, nil
	case http.StatusUnprocessableEntity:
		var wref demoTransformWireRefusal
		if err := json.Unmarshal(body, &wref); err != nil {
			return nil, nil, fmt.Errorf("decode /demo/transform 422 response from %s: %w", demoURL, err)
		}
		return nil, &wref, nil
	default:
		return nil, nil, fmt.Errorf("gateway demo endpoint %s: unexpected status %d: %s", demoURL, resp.StatusCode, body)
	}
}
