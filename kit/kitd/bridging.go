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
	"net/http"
	"strings"
	"time"
)

// bridgingCarryInput is the embedded golden QR (2.2, with a hand-injected
// item.answer.extension:itemWeight) the "carry" exhibit runs down (2.2->2.1,
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
// no outbound call in this package is ever unbounded.
var bridgingHTTPClient = &http.Client{Timeout: bridgingExhibitTimeout}

// bridgingResponseBytesLimit bounds how much of a /demo/transform response
// body this proxy will read — mirrors shnsdk.MaxRequestBytes (8 MiB), the
// same bound demo_endpoint.go applies to the inbound side of that same
// exchange. The Kit doesn't import shnsdk here for one constant; the value
// is cited, not re-derived.
const bridgingResponseBytesLimit = 8 << 20

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

// demoTransformWireResponse is POST /demo/transform's 200 body, verbatim.
type demoTransformWireResponse struct {
	Output      json.RawMessage      `json:"output"`
	LossReports []bridgingLossReport `json:"lossReports"`
}

// demoTransformWireRefusal is POST /demo/transform's 422 body, verbatim.
type demoTransformWireRefusal struct {
	Refusal        string `json:"refusal"`
	SemanticChange bool   `json:"semanticChange"`
}

// --- POST /api/bridging/exhibit wire shapes ---

// bridgingExhibitRequest is POST /api/bridging/exhibit's body.
type bridgingExhibitRequest struct {
	Kind string `json:"kind"` // "carry" | "refusal"
}

// bridgingExhibitCarryResponse is the "carry" kind's 200 body.
type bridgingExhibitCarryResponse struct {
	Kind        string               `json:"kind"` // "carry"
	LossReports []bridgingLossReport `json:"lossReports"`
	Restored    bool                 `json:"restored"`
}

// bridgingExhibitRefusalResponse is the "refusal" kind's 200 body — the
// EXHIBIT's response is 200 even though the child's own leg was refused
// (422): the exhibit successfully demonstrated a refusal, which is its whole
// point, so the outer HTTP status reports the exhibit's own success.
type bridgingExhibitRefusalResponse struct {
	Kind           string `json:"kind"` // "refusal"
	Refusal        string `json:"refusal"`
	SemanticChange bool   `json:"semanticChange"`
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
		handleBridgingCarry(ctx, w, demoURL)
	case "refusal":
		handleBridgingRefusal(ctx, w, demoURL)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown kind %q (want \"carry\" or \"refusal\")", req.Kind)})
	}
}

// handleBridgingCarry runs the embedded itemWeight-bearing 2.2 QR down
// (pa.dtr 2.2->2.1, the direction whose carry genuinely fires) then up
// (pa.dtr 2.1->2.2, restore) and reports whether the round trip was
// byte-identical to the embedded input.
func handleBridgingCarry(ctx context.Context, w http.ResponseWriter, demoURL string) {
	downOut, downReports, downRefusal, err := callDemoTransform(ctx, demoURL, "pa.dtr", "2.2", "2.1", bridgingCarryInput)
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

	upOut, upReports, upRefusal, err := callDemoTransform(ctx, demoURL, "pa.dtr", "2.1", "2.2", downOut)
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

	if !bytes.Equal([]byte(upOut), bridgingCarryInput) {
		// Never fake it: a divergent round trip is reported loudly, not
		// papered over with Restored:true.
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "carry exhibit: round trip was not byte-identical to the embedded input",
		})
		return
	}

	// Walk order: the down leg's reports first, then the up leg's — mirroring the
	// two calls above (2.2->2.1 then 2.1->2.2), not sorted or otherwise reordered.
	reports := make([]bridgingLossReport, 0, len(downReports)+len(upReports))
	reports = append(reports, downReports...)
	reports = append(reports, upReports...)
	writeJSON(w, http.StatusOK, bridgingExhibitCarryResponse{Kind: "carry", LossReports: reports, Restored: true})
}

// handleBridgingRefusal runs the embedded crafted multi-coverage 2.1 QR up
// (pa.dtr 2.1->2.2) and expects — and surfaces — the child's typed
// semantic-change refusal.
func handleBridgingRefusal(ctx context.Context, w http.ResponseWriter, demoURL string) {
	_, _, refusal, err := callDemoTransform(ctx, demoURL, "pa.dtr", "2.1", "2.2", bridgingRefusalInput)
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
	writeJSON(w, http.StatusOK, bridgingExhibitRefusalResponse{Kind: "refusal", Refusal: refusal.Refusal, SemanticChange: refusal.SemanticChange})
}

// demoTransformURL derives the gateway child's POST /demo/transform URL from
// its ObserverURL ("http://host:port/events" — kitd.Stack.ObserverURL's own
// doc): both live on the same loopback listener
// (gateway/app/demo_endpoint.go's composeObserverHandler serves both on one
// mux).
func demoTransformURL(observerURL string) string {
	return strings.TrimSuffix(observerURL, "/events") + "/demo/transform"
}

// callDemoTransform posts one contract@from->to request to the gateway
// child's POST /demo/transform and classifies the response into exactly one
// of:
//   - (output, reports, nil, nil) on 200
//   - (nil, nil, refusal, nil) on 422
//   - (nil, nil, nil, err) for anything else — network failure, a
//     non-200/422 status, or a response body that doesn't decode as the
//     status code's expected shape. Callers map a non-nil err to 502: it
//     means the child was unreachable or answered something this proxy
//     doesn't understand, never a transform result to report on.
func callDemoTransform(ctx context.Context, demoURL, contract, from, to string, payload []byte) (json.RawMessage, []bridgingLossReport, *demoTransformWireRefusal, error) {
	reqBody, err := json.Marshal(demoTransformWireRequest{Contract: contract, From: from, To: to, Payload: json.RawMessage(payload)})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal /demo/transform request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, demoURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build /demo/transform request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := bridgingHTTPClient.Do(httpReq)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("gateway demo endpoint unreachable at %s: %w", demoURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, bridgingResponseBytesLimit))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read /demo/transform response from %s: %w", demoURL, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var wr demoTransformWireResponse
		if err := json.Unmarshal(body, &wr); err != nil {
			return nil, nil, nil, fmt.Errorf("decode /demo/transform 200 response from %s: %w", demoURL, err)
		}
		if wr.LossReports == nil {
			wr.LossReports = []bridgingLossReport{}
		}
		return wr.Output, wr.LossReports, nil, nil
	case http.StatusUnprocessableEntity:
		var wref demoTransformWireRefusal
		if err := json.Unmarshal(body, &wref); err != nil {
			return nil, nil, nil, fmt.Errorf("decode /demo/transform 422 response from %s: %w", demoURL, err)
		}
		return nil, nil, &wref, nil
	default:
		return nil, nil, nil, fmt.Errorf("gateway demo endpoint %s: unexpected status %d: %s", demoURL, resp.StatusCode, body)
	}
}
