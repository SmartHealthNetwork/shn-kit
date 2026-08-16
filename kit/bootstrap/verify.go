package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// Probe is one "hello substrate" fact Verify checks, serializable as-is for
// the daemon's GET /api/verify response.
type Probe struct {
	Name   string `json:"name"` // "discovery" | "registration" | "hosted-payer" | "bridge-demo-payer" | "bridge-demo-refuse"
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// BridgeProbes configures the two OPTIONAL demo-holder "hello bridging"
// checks Verify runs alongside the baseline three: DemoHolder
// names the holder id the "bridge-demo-payer" probe expects to find, and
// RefuseHolder names the holder id "bridge-demo-refuse" expects. Either
// field left "" makes its probe ABSENT from the returned slice entirely —
// not reported red — since a Kit not configured with a peer's holder id has
// nothing to check (the cross-version bridged-exchange exhibit's holders
// exist on the feed only once a peer publishes them; an empty BridgeProbes
// is the ordinary case and every such Verify call passes).
type BridgeProbes struct {
	DemoHolder   string
	RefuseHolder string
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
// FetchHolders call, then derives the baseline three probes (plus whichever
// bridge-demo probes bp configures, BridgeProbes doc) from those two
// results. If discovery fails, the registration and hosted-payer probes are
// reported not-attempted (OK false, Detail "skipped: discovery failed")
// rather than silently omitted, so a caller can always expect the baseline
// 3 probes back (plus a skip row for each configured bridge probe).
//
// When bus is non-nil, Verify emits one event.TypeVerify event per probe
// ("<name>: ok" or "<name>: <failure detail>").
//
// bp configures the two OPTIONAL bridge-demo probes (BridgeProbes doc):
// a "" field makes that probe absent from every returned slice, including
// the discovery-failed and fetch-failed skip branches below — a Kit not
// configured with a peer's holder id gets no probe row for it, at any stage.
func Verify(ctx context.Context, hc *http.Client, discoveryURL, holderID string, bp BridgeProbes, bus *event.Bus) []Probe {
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
		probes = append(probes, skippedBridgeProbes(bp, "skipped: discovery failed")...)
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
		probes = append(probes, skippedBridgeProbes(bp, detail)...)
		emitProbes(bus, probes)
		return probes
	}

	probes := []Probe{discProbe, probeRegistration(holders, holderID), probeHostedPayer(holders)}
	if bp.DemoHolder != "" {
		probes = append(probes, probeBridgeDemoPayer(holders, bp.DemoHolder))
	}
	if bp.RefuseHolder != "" {
		probes = append(probes, probeBridgeDemoRefuse(holders, bp.RefuseHolder))
	}
	emitProbes(bus, probes)
	return probes
}

// skippedBridgeProbes returns the skip-detail rows for whichever bridge
// probes bp actually configures (empty fields stay absent, per BridgeProbes'
// doc) — the shared tail appended to both of Verify's early-skip branches.
func skippedBridgeProbes(bp BridgeProbes, detail string) []Probe {
	var out []Probe
	if bp.DemoHolder != "" {
		out = append(out, Probe{Name: "bridge-demo-payer", OK: false, Detail: detail})
	}
	if bp.RefuseHolder != "" {
		out = append(out, Probe{Name: "bridge-demo-refuse", OK: false, Detail: detail})
	}
	return out
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

// PatientSurfaceReadable reports whether the hosted patient-surface render is reachable by
// a machine client: GET <phgURL>/personas must return 200 with a decodable JSON array. In
// the HOSTED topology the discovery-advertised phg endpoint is the machine /notify edge
// only — the patient-surface reads (/personas, /authorizations) are internal/patient-only
// (Cognito-gated at app.<apex>), so /personas is not routed (404 "no route"). shnkitd uses
// this to decide whether UC-07's patient-surface read-back runs or degrades gracefully
// (runner.Config.PatientSurfaceReadable). A nil hc uses http.DefaultClient; any error /
// non-200 / non-array body ⇒ false (degrade the read-back, never hard-fail the whole run).
func PatientSurfaceReadable(ctx context.Context, hc *http.Client, phgURL string) bool {
	if phgURL == "" {
		return false
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, phgURL+"/personas", nil)
	if err != nil {
		return false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var personas []json.RawMessage
	return json.NewDecoder(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes)).Decode(&personas) == nil
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

// probeBridgeDemoPayer reports whether demoHolder exists on the feed as a
// payer publishing a routable payer identity (PayerIDs) AND declaring a
// non-2.0 CRD or DTR contract-version line specifically — the
// bridged-exchange exhibit's precondition: the exhibit's bridged legs are
// CRD and DTR, so a holder skewed only on another contract (e.g. pa.pas@2.1)
// would read green while the demo shows no transform at all, and a holder
// stuck entirely at 2.0 has nothing to bridge to.
func probeBridgeDemoPayer(holders []shnsdk.Holder, demoHolder string) Probe {
	const name = "bridge-demo-payer"
	for _, h := range holders {
		if h.ID != demoHolder {
			continue
		}
		if h.Role != "payer" {
			return Probe{Name: name, OK: false, Detail: fmt.Sprintf("holder %q is not a payer (role %q)", demoHolder, h.Role)}
		}
		if len(h.PayerIDs) == 0 {
			return Probe{Name: name, OK: false, Detail: fmt.Sprintf("holder %q publishes no payer identity (PayerIDs)", demoHolder)}
		}
		// A non-2.0 CRD or DTR line specifically (not just ANY non-2.0 line —
		// a pa.pas-only skew would read green while the demo's bridged
		// CRD/DTR legs show no transform — matching the refuse probe's
		// hardening).
		line, ok := nonBaselineLine(h.ContractVersions, "pa.crd")
		if !ok {
			line, ok = nonBaselineLine(h.ContractVersions, "pa.dtr")
		}
		if ok {
			return Probe{Name: name, OK: true, Detail: fmt.Sprintf("%s publishes a payer identity and declares %s", demoHolder, line)}
		}
		return Probe{Name: name, OK: false, Detail: fmt.Sprintf("holder %q declares no CRD or DTR contract-version line beyond the 2.0 baseline", demoHolder)}
	}
	return Probe{Name: name, OK: false, Detail: fmt.Sprintf("holder %q not found in registrar feed", demoHolder)}
}

// probeBridgeDemoRefuse reports whether refuseHolder exists on the feed,
// publishes a routable payer identity (PayerIDs), declares pa.pas at a line
// other than 2.0 (so the exhibit has a non-baseline PAS response to refuse
// across), AND shares a CRD or DTR line with this build's own declared set
// (shnsdk.SupportedContractVersions()) — the refuse exhibit issues its
// CRD/DTR leg at OUR declared line, so without that overlap the exhibit
// can't run at all. A holder that only declares pa.pas (no shared CRD/DTR
// line) reads as a red probe naming the missing precondition, never a probe
// that silently vanishes or a dead run discovered only at exhibit time.
func probeBridgeDemoRefuse(holders []shnsdk.Holder, refuseHolder string) Probe {
	const name = "bridge-demo-refuse"
	for _, h := range holders {
		if h.ID != refuseHolder {
			continue
		}
		if len(h.PayerIDs) == 0 {
			return Probe{Name: name, OK: false, Detail: fmt.Sprintf("holder %q publishes no payer identity (PayerIDs)", refuseHolder)}
		}
		pasLine, ok := nonBaselineLine(h.ContractVersions, "pa.pas")
		if !ok {
			return Probe{Name: name, OK: false, Detail: fmt.Sprintf("holder %q declares no pa.pas line beyond the 2.0 baseline", refuseHolder)}
		}
		shared, ok := sharedCRDOrDTRLine(h.ContractVersions)
		if !ok {
			return Probe{Name: name, OK: false, Detail: fmt.Sprintf(
				"holder %q declares %s but shares no CRD/DTR line with this build's declared set (%s) — the bridged-refuse exhibit precondition",
				refuseHolder, pasLine, strings.Join(shnsdk.SupportedContractVersions(), ","),
			)}
		}
		return Probe{Name: name, OK: true, Detail: fmt.Sprintf("%s declares %s and shares %s", refuseHolder, pasLine, shared)}
	}
	return Probe{Name: name, OK: false, Detail: fmt.Sprintf("holder %q not found in registrar feed", refuseHolder)}
}

// nonBaselineLine returns the first token in versions declared at a line
// other than "2.0", if any. contract == "" matches any contract family
// ("<any>@<non-2.0-line>"); otherwise only tokens for that contract family
// ("<contract>@<non-2.0-line>") are considered.
func nonBaselineLine(versions []string, contract string) (string, bool) {
	prefix := contract + "@"
	for _, v := range versions {
		c, line, ok := strings.Cut(v, "@")
		if !ok || line == "2.0" {
			continue
		}
		if contract != "" && c+"@" != prefix {
			continue
		}
		return v, true
	}
	return "", false
}

// sharedCRDOrDTRLine reports whether versions shares a pa.crd@ or pa.dtr@
// token with this build's own declared set (shnsdk.SupportedContractVersions()).
func sharedCRDOrDTRLine(versions []string) (string, bool) {
	supported := map[string]bool{}
	for _, v := range shnsdk.SupportedContractVersions() {
		if strings.HasPrefix(v, "pa.crd@") || strings.HasPrefix(v, "pa.dtr@") {
			supported[v] = true
		}
	}
	for _, v := range versions {
		if supported[v] {
			return v, true
		}
	}
	return "", false
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
