package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// Probe is one "hello substrate" fact Verify checks, serializable as-is for
// the daemon's GET /api/verify response.
type Probe struct {
	Name   string `json:"name"` // "discovery" | "registration" | "hosted-payer"
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Verify runs the "hello substrate" checks a freshly provisioned
// Kit needs before it can drive a scenario: can it reach the network's
// discovery descriptor, is its own holder id visible in the registrar feed,
// and does the feed publish at least one payer with a routable payer
// identity (the FeedPayerRouter precondition an origination needs to route
// at all). A fourth "hello substrate" fact — "the gateway federates" — is NOT probed
// here: it is the supervisor's child-ready probe (child reaching the "ready"
// state), already surfaced via that mechanism, so Verify does not duplicate
// it.
//
// hc == nil defaults to http.DefaultClient — load-bearing, because
// shnsdk.FetchHolders (sdk/holders.go) has no nil-guard of its own, unlike
// most SDK client-accepting funcs.
//
// Verify makes exactly one discovery GET and, if that succeeds, exactly one
// FetchHolders call, then derives all three probes from those two results.
// If discovery fails, the registration and hosted-payer probes are reported
// not-attempted (OK false, Detail "skipped: discovery failed") rather than
// silently omitted, so a caller can always expect exactly 3 probes back.
//
// When bus is non-nil, Verify emits one event.TypeVerify event per probe
// ("<name>: ok" or "<name>: <failure detail>").
func Verify(ctx context.Context, hc *http.Client, discoveryURL, holderID string, bus *event.Bus) []Probe {
	if hc == nil {
		hc = http.DefaultClient
	}

	discProbe, disc, ok := probeDiscovery(ctx, hc, discoveryURL)
	if !ok {
		probes := []Probe{
			discProbe,
			{Name: "registration", OK: false, Detail: "skipped: discovery failed"},
			{Name: "hosted-payer", OK: false, Detail: "skipped: discovery failed"},
		}
		emitProbes(bus, probes)
		return probes
	}

	holders, err := shnsdk.FetchHolders(ctx, hc, disc.Endpoints.Registrar)
	if err != nil {
		detail := fmt.Sprintf("skipped: fetch holder feed failed: %v", err)
		probes := []Probe{
			discProbe,
			{Name: "registration", OK: false, Detail: detail},
			{Name: "hosted-payer", OK: false, Detail: detail},
		}
		emitProbes(bus, probes)
		return probes
	}

	probes := []Probe{discProbe, probeRegistration(holders, holderID), probeHostedPayer(holders)}
	emitProbes(bus, probes)
	return probes
}

// FetchDiscovery GETs discoveryURL and decodes the shnsdk.Discovery descriptor. It is the
// shared fetch used by the boot Verify probe (probeDiscovery) AND by shnkitd's endpoint
// resolution (cmd/shnkitd resolves endpoints.PHG for the scenario driver's UC-07
// patient-surface read-back, matching how the gateway resolves its own endpoints from
// discovery). A nil hc uses http.DefaultClient.
func FetchDiscovery(ctx context.Context, hc *http.Client, discoveryURL string) (shnsdk.Discovery, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return shnsdk.Discovery{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return shnsdk.Discovery{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return shnsdk.Discovery{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var disc shnsdk.Discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes)).Decode(&disc); err != nil {
		return shnsdk.Discovery{}, err
	}
	return disc, nil
}

// probeDiscovery GETs discoveryURL and decodes a shnsdk.Discovery. ok is
// false whenever the "discovery" probe itself failed — a nil Discovery is
// meaningless to callers in that case, so they must not use it.
func probeDiscovery(ctx context.Context, hc *http.Client, discoveryURL string) (probe Probe, disc shnsdk.Discovery, ok bool) {
	fail := func(detail string) (Probe, shnsdk.Discovery, bool) {
		return Probe{Name: "discovery", OK: false, Detail: "discovery: " + detail}, shnsdk.Discovery{}, false
	}

	disc, err := FetchDiscovery(ctx, hc, discoveryURL)
	if err != nil {
		return fail(err.Error())
	}
	if disc.Endpoints.Registrar == "" {
		return fail("no registrar endpoint published")
	}
	return Probe{Name: "discovery", OK: true, Detail: "reachable"}, disc, true
}

// probeRegistration reports whether holderID appears in the registrar feed.
func probeRegistration(holders []shnsdk.Holder, holderID string) Probe {
	for _, h := range holders {
		if h.ID == holderID {
			return Probe{Name: "registration", OK: true, Detail: "found in registrar feed"}
		}
	}
	return Probe{Name: "registration", OK: false, Detail: fmt.Sprintf("holder %q not found in registrar feed", holderID)}
}

// probeHostedPayer reports whether the feed publishes at least one payer
// holder with a routable payer identity (PayerIDs) — the FeedPayerRouter
// precondition an origination needs to route at all (FR-G41).
func probeHostedPayer(holders []shnsdk.Holder) Probe {
	for _, h := range holders {
		if h.Role == "payer" && len(h.PayerIDs) > 0 {
			return Probe{Name: "hosted-payer", OK: true, Detail: fmt.Sprintf("%s publishes a routable payer identity", h.ID)}
		}
	}
	return Probe{Name: "hosted-payer", OK: false, Detail: "no payer holder publishes a routable payer identity (PayerIDs) — originations have no route"}
}

// emitProbes is a nil-safe bus wrapper: one event.TypeVerify per probe.
func emitProbes(bus *event.Bus, probes []Probe) {
	if bus == nil {
		return
	}
	for _, p := range probes {
		detail := p.Name + ": ok"
		if !p.OK {
			detail = p.Name + ": " + p.Detail
		}
		bus.Emit(event.Event{Type: event.TypeVerify, Detail: detail})
	}
}
