// rows_ehr_test.go — the Plain-EHR lane's rows against a FAKE gateway child
// (canned /scenario/* bodies in the shapes the live reference-payer gate
// reads): one passing case per UC, then the mutation table — each passing
// body with ONE fact removed or swapped must fail naming it. The lane's one
// bridging row (uc03 bridge-refuse) runs on the MAIN child instead, which
// TestEHRLane_DriverPerRow pins.
package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// pdServer serves canned /scenario/* bodies keyed by path, standing in for
// the Plain-EHR lane's gateway child.
func pdServer(t *testing.T, bodies map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range bodies {
		body := body
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// pdRunner builds a Runner whose MAIN-child driver points nowhere (every
// Plain-EHR row but bridge-refuse must never touch it) and whose Plain-EHR
// driver points at srv — or is absent when withPD is false (the no-trio Kit).
func pdRunner(t *testing.T, srv *httptest.Server, withPD bool) *Runner {
	t.Helper()
	cfg := Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: "http://127.0.0.1:1"}),
		Bus:    event.NewBus(fixedClock),
	}
	if withPD {
		cfg.ProviderDataDriver = scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL})
	}
	return New(cfg)
}

const (
	pdUC04OK = `{"paRequired":true,"authNumber":"AUTH-1011","validUntil":"2026-12-01","qrAnswers":{"1.1":"91251008","3.1":"Cerebral infarction","3.2":"Right-sided weakness, needs assistance with transfers","3.3":"Independent transfers within 6 weeks"}}`
	pdUC06OK = `{"paRequired":true,"authNumber":"AUTH-1012","amendmentCorr":"corr-amend-1","attested":true,"qrAnswers":{"1.1":"91251008","3.1":"Muscle weakness (generalized)"}}`
	pdUC07OK = `{"paRequired":true,"authNumber":"AUTH-1013","amendmentCorr":"corr-amend-2","attested":true,"qrAnswers":{"1.1":"91251008"}}`
	pdUC02OK = `{"covered":"covered","paRequired":false,"needsDTR":false,"cardSummary":"Document medical necessity"}`
	pdUC03OK = `{"paRequired":true,"authNumber":"AUTH-1014","qrAnswers":{"2.2":"87","2.3":"53"}}`
	pdUC05OK = `{"paRequired":true,"authNumber":"AUTH-1015","facilityId":"metro-spine"}`
	pdUC05NC = `{"paRequired":true,"pended":true,"consentDenied":true}`
	pdUC08OK = `{"denied":true,"rationale":"Not Certified: excluded service"}`
)

func runPD(t *testing.T, bodies map[string]string, uc, branch string) Result {
	t.Helper()
	srv := pdServer(t, bodies)
	rn := pdRunner(t, srv, true)
	res, err := rn.Run(t.Context(), Req{Lane: "ehr", UC: uc, Branch: branch})
	if err != nil {
		t.Fatalf("Run(ehr/%s/%s): %v", uc, branch, err)
	}
	return res
}

func TestEHRRows_Pass(t *testing.T) {
	for _, tc := range []struct{ uc, branch, path, body, wantDetail string }{
		{"uc01", "covered", "/scenario/uc01", `{"covered":true,"reason":"active"}`, "covered=true"},
		{"uc01", "notcovered", "/scenario/uc01", `{"covered":false,"reason":"coverage-terminated"}`, "coverage-terminated"},
		{"uc02", "", "/scenario/uc02", pdUC02OK, "no prior authorization"},
		{"uc03", "", "/scenario/uc03", pdUC03OK, "AUTH-1014"},
		{"uc04", "", "/scenario/uc04", pdUC04OK, "AUTH-1011"},
		{"uc05", "", "/scenario/uc05", pdUC05OK, "metro-spine"},
		{"uc05", "consent", "/scenario/uc05", pdUC05OK, "metro-spine"},
		{"uc05", "noconsent", "/scenario/uc05", pdUC05NC, "consent denied"},
		{"uc06", "", "/scenario/uc06", pdUC06OK, "AUTH-1012"},
		{"uc07", "", "/scenario/uc07", pdUC07OK, "AUTH-1013"},
		{"uc08", "", "/scenario/uc08", pdUC08OK, "denied"},
	} {
		res := runPD(t, map[string]string{tc.path: tc.body}, tc.uc, tc.branch)
		if res.State != StatePassed || !strings.Contains(res.Detail, tc.wantDetail) {
			t.Errorf("%s/%s: state=%s detail=%q, want passed containing %q", tc.uc, tc.branch, res.State, res.Detail, tc.wantDetail)
		}
		if res.Lane != "ehr" {
			t.Errorf("%s/%s: Result.Lane = %q, want ehr", tc.uc, tc.branch, res.Lane)
		}
	}
}

// TestEHRRows_Reject is the mutation table.
func TestEHRRows_Reject(t *testing.T) {
	mut := func(base string, f func(m map[string]any)) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(base), &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		b, _ := json.Marshal(m)
		return string(b)
	}
	qr := func(m map[string]any) map[string]any { return m["qrAnswers"].(map[string]any) }
	for _, tc := range []struct{ name, uc, branch, path, body, wantErr string }{
		{"uc04 non-reference verdict prefix", "uc04", "", "/scenario/uc04", mut(pdUC04OK, func(m map[string]any) { m["authNumber"] = "PA-deadbeef" }), "AUTH-"},
		{"uc04 not PA-required", "uc04", "", "/scenario/uc04", mut(pdUC04OK, func(m map[string]any) { m["paRequired"] = false }), "paRequired"},
		{"uc04 group-3 3.2 missing", "uc04", "", "/scenario/uc04", mut(pdUC04OK, func(m map[string]any) { delete(qr(m), "3.2") }), "3.2"},
		{"uc04 group-3 3.3 missing", "uc04", "", "/scenario/uc04", mut(pdUC04OK, func(m map[string]any) { delete(qr(m), "3.3") }), "3.3"},
		{"uc04 1.1 not the PT category", "uc04", "", "/scenario/uc04", mut(pdUC04OK, func(m map[string]any) { qr(m)["1.1"] = "72148" }), "1.1"},
		{"uc04 3.1 not the seeded dx", "uc04", "", "/scenario/uc04", mut(pdUC04OK, func(m map[string]any) { qr(m)["3.1"] = "Low back pain" }), "3.1"},
		{"uc04 scripted pend shape", "uc04", "", "/scenario/uc04", `{"paRequired":true,"authNumber":"PA-1","pendedItems":["operative-report"]}`, "1.1"},
		{"uc06 not attested", "uc06", "", "/scenario/uc06", mut(pdUC06OK, func(m map[string]any) { m["attested"] = false }), "attested"},
		{"uc06 no amendment leg", "uc06", "", "/scenario/uc06", mut(pdUC06OK, func(m map[string]any) { delete(m, "amendmentCorr") }), "amendmentCorr"},
		{"uc06 3.1 not the seeded dx", "uc06", "", "/scenario/uc06", mut(pdUC06OK, func(m map[string]any) { qr(m)["3.1"] = "Cerebral infarction" }), "3.1"},
		{"uc06 non-reference verdict", "uc06", "", "/scenario/uc06", mut(pdUC06OK, func(m map[string]any) { m["authNumber"] = "PA-1" }), "AUTH-"},
		{"uc07 no amendment leg", "uc07", "", "/scenario/uc07", mut(pdUC07OK, func(m map[string]any) { delete(m, "amendmentCorr") }), "amendmentCorr"},
		{"uc07 not attested", "uc07", "", "/scenario/uc07", mut(pdUC07OK, func(m map[string]any) { m["attested"] = false }), "attested"},
		{"uc07 non-reference verdict", "uc07", "", "/scenario/uc07", mut(pdUC07OK, func(m map[string]any) { m["authNumber"] = "PA-1" }), "AUTH-"},
		{"uc02 questionnaire demanded", "uc02", "", "/scenario/uc02", mut(pdUC02OK, func(m map[string]any) { m["needsDTR"] = true }), "needsDTR"},
		{"uc02 PA demanded", "uc02", "", "/scenario/uc02", mut(pdUC02OK, func(m map[string]any) { m["paRequired"] = true }), "paRequired"},
		{"uc02 not covered", "uc02", "", "/scenario/uc02", mut(pdUC02OK, func(m map[string]any) { m["covered"] = "not-covered" }), "covered"},
		{"uc03 auto-fill 2.2 not from the seeded obs", "uc03", "", "/scenario/uc03", mut(pdUC03OK, func(m map[string]any) { qr(m)["2.2"] = "" }), "2.2"},
		{"uc03 auto-fill 2.3 another persona's value", "uc03", "", "/scenario/uc03", mut(pdUC03OK, func(m map[string]any) { qr(m)["2.3"] = "54" }), "2.3"},
		{"uc03 non-reference verdict", "uc03", "", "/scenario/uc03", mut(pdUC03OK, func(m map[string]any) { m["authNumber"] = "PA-1" }), "AUTH-"},
		{"uc05 wrong facility", "uc05", "consent", "/scenario/uc05", mut(pdUC05OK, func(m map[string]any) { m["facilityId"] = "other" }), "metro-spine"},
		{"uc05 non-reference verdict", "uc05", "consent", "/scenario/uc05", mut(pdUC05OK, func(m map[string]any) { m["authNumber"] = "PA-1" }), "AUTH-"},
		{"uc05 noconsent issued an auth", "uc05", "noconsent", "/scenario/uc05", `{"consentDenied":true,"authNumber":"AUTH-9"}`, "authNumber"},
		{"uc05 noconsent not denied", "uc05", "noconsent", "/scenario/uc05", `{"consentDenied":false}`, "consentDenied"},
		{"uc08 approved", "uc08", "", "/scenario/uc08", `{"denied":false,"authNumber":"AUTH-9","rationale":""}`, "denied"},
		{"uc08 denied with an auth", "uc08", "", "/scenario/uc08", `{"denied":true,"authNumber":"AUTH-9","rationale":"x"}`, "authNumber"},
		{"uc08 no rationale", "uc08", "", "/scenario/uc08", `{"denied":true,"rationale":""}`, "rationale"},
		{"uc01 covered branch not covered", "uc01", "covered", "/scenario/uc01", `{"covered":false,"reason":"x"}`, "covered"},
		{"uc01 notcovered without the seeded reason", "uc01", "notcovered", "/scenario/uc01", `{"covered":false,"reason":"unknown"}`, "coverage-terminated"},
		{"non-200", "uc04", "", "/scenario/uc99", pdUC04OK, "status 404"},
	} {
		res := runPD(t, map[string]string{tc.path: tc.body}, tc.uc, tc.branch)
		if res.State != StateFailed || !strings.Contains(res.Detail, tc.wantErr) {
			t.Errorf("%s: state=%s detail=%q, want failed naming %q", tc.name, res.State, res.Detail, tc.wantErr)
		}
	}
}

// TestEHRLane_RowShape pins the lane's row shape and the no-trio refusal:
// without a ProviderDataDriver the lane is refused with the trio sentence
// (the run is never created) — EXCEPT the bridge-refuse row, which runs on
// the main child and needs no trio; the conformant-only branches (uc03
// bridge-demo) are refused here, uc07 takes no branch on either lane, uc01
// requires a branch, uc05 takes ""|consent|noconsent only, and
// "provider-data" is no longer a lane at all.
func TestEHRLane_RowShape(t *testing.T) {
	srv := pdServer(t, nil)
	noPD := pdRunner(t, srv, false)
	_, err := noPD.Run(t.Context(), Req{Lane: "ehr", UC: "uc04"})
	if err == nil || !strings.Contains(err.Error(), ehrLaneUnavailable) {
		t.Fatalf("no ProviderDataDriver: err=%v, want the trio sentence", err)
	}
	if _, err := noPD.Start(t.Context(), Req{Lane: "ehr", UC: "uc04"}); err == nil || !strings.Contains(err.Error(), ehrLaneUnavailable) {
		t.Fatalf("no ProviderDataDriver (Start): err=%v, want the trio sentence", err)
	}
	if got := len(noPD.Results()); got != 0 {
		t.Fatalf("a refused run was recorded: %d results", got)
	}
	// bridge-refuse is the exception: it originates on the MAIN child, so a
	// no-trio Kit still admits it (the run is created and fails on the wire
	// against the unroutable main base, never refused up front).
	if _, err := noPD.Run(t.Context(), Req{Lane: "ehr", UC: "uc03", Branch: "bridge-refuse"}); err != nil {
		t.Fatalf("no ProviderDataDriver, ehr/uc03/bridge-refuse: err=%v, want the run admitted (main child)", err)
	}
	// Row shape is validateRow's alone — no runner needed for the rest.
	for _, bad := range []Req{
		{Lane: "ehr", UC: "uc07", Branch: "hcpcs"},
		{Lane: "ehr", UC: "uc03", Branch: "bridge-demo"},
		{Lane: "ehr", UC: "uc01", Branch: ""},
		{Lane: "ehr", UC: "uc05", Branch: "x"},
		{Lane: "ehr", UC: "uc02", Branch: "x"},
		{Lane: "ehr", UC: "external"},
		{Lane: "ehr", UC: "uc09"},
		{Lane: "provider-data", UC: "uc04", Branch: ""},
		{Lane: "conformant", UC: "uc03", Branch: "bridge-refuse"},
	} {
		if _, err := validateRow(bad); err == nil {
			t.Errorf("validateRow(%+v) accepted, want rejected", bad)
		}
	}
	// "provider-data" is not a lane any more — the error must name the two
	// that are.
	if _, err := validateRow(Req{Lane: "provider-data", UC: "uc04"}); err == nil || !strings.Contains(err.Error(), "want ehr|conformant") {
		t.Errorf("validateRow(provider-data/uc04) error = %v, want it to name ehr|conformant", err)
	}
	for _, ok := range []Req{
		{Lane: "ehr", UC: "uc01", Branch: "covered"},
		{Lane: "ehr", UC: "uc05", Branch: "noconsent"},
		{Lane: "ehr", UC: "uc05", Branch: ""},
		{Lane: "ehr", UC: "uc07", Branch: ""},
		{Lane: "ehr", UC: "uc03", Branch: ""},
		{Lane: "ehr", UC: "uc03", Branch: "bridge-refuse"},
		{Lane: "ehr", UC: "freeform", Member: "MBR-OX"},
	} {
		if _, err := validateRow(ok); err != nil {
			t.Errorf("validateRow(%+v) = %v, want accepted", ok, err)
		}
	}
}

// runMain builds a Runner whose MAIN-child driver serves body for every
// request (bridge-refuse originates on the main child, never the
// provider-data child) and runs ehr/uc03/bridge-refuse against it.
func runMain(t *testing.T, body string) Result {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	rn := New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL}),
		Bus:    event.NewBus(fixedClock),
	})
	res, err := rn.Run(t.Context(), Req{Lane: "ehr", UC: "uc03", Branch: "bridge-refuse"})
	if err != nil {
		t.Fatalf("Run(ehr/uc03/bridge-refuse): %v", err)
	}
	return res
}

// Bridge refusal bodies mirror the engine's structured 200
// (gateway/engine/originate.go's writeBridgeRefusal): the two designed
// refusal grammars, one per contract-version boundary check.
const (
	bridgeRefuseSemantic  = `{"refused":true,"refusedAt":"pas-claim","refusal":"semantic-change refusal: pa.pas 2.0->2.1 (up direction): lanes 2.0,2.1 knob SHN_DEMO_EGRESS_NATIVE_LINES"}`
	bridgeRefuseSelection = `{"refused":true,"refusedAt":"pas-claim","refusal":"no shared contract line for pa.pas (leg pas-claim): recipient advertises 2.1 only"}`
)

// TestEHRUC03BridgeRefuse_Pass pins the two designed-refusal grammars the row
// must accept: the semantic-change and the no-shared-contract-line shape.
func TestEHRUC03BridgeRefuse_Pass(t *testing.T) {
	for _, tc := range []struct{ name, body, wantDetail string }{
		{"semantic-change grammar", bridgeRefuseSemantic, "semantic-change refusal: pa.pas"},
		{"no-shared-contract-line grammar", bridgeRefuseSelection, "no shared contract line for pa.pas"},
	} {
		res := runMain(t, tc.body)
		if res.State != StatePassed || !strings.Contains(res.Detail, tc.wantDetail) {
			t.Errorf("%s: state=%s detail=%q, want passed containing %q", tc.name, res.State, res.Detail, tc.wantDetail)
		}
	}
}

// TestEHRUC03BridgeRefuse_Impostors pins the fence against three ways a wire
// body can look plausible while missing the version-boundary contract this
// exhibit demonstrates: the old approval shape, a refusal at the wrong seam,
// and a refusal at the right seam with an unrecognized grammar.
func TestEHRUC03BridgeRefuse_Impostors(t *testing.T) {
	for _, tc := range []struct{ name, body, wantErr string }{
		{"old approval shape", `{"paRequired":true,"authNumber":"AUTH-0199","validUntil":"2026-12-01"}`, "did not refuse"},
		{"refused at the wrong seam", `{"refused":true,"refusedAt":"crd-order-select","refusal":"semantic-change refusal: pa.pas x"}`, "crd-order-select"},
		{"unrecognized refusal grammar", `{"refused":true,"refusedAt":"pas-claim","refusal":"hub routing failed"}`, "hub routing failed"},
	} {
		res := runMain(t, tc.body)
		if res.State != StateFailed || !strings.Contains(res.Detail, tc.wantErr) {
			t.Errorf("%s: state=%s detail=%q, want failed naming %q", tc.name, res.State, res.Detail, tc.wantErr)
		}
	}
}

// The bridge-refuse branch is the one Plain-EHR row that runs on the MAIN child
// (origination of the lumbar order to the bridging demo payer); every other ehr
// row runs on the provider-data child — and never the other way round.
func TestEHRLane_DriverPerRow(t *testing.T) {
	mainHits, pdHits := 0, 0
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mainHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"paRequired":true,"authNumber":"PA-1"}`))
	}))
	t.Cleanup(mainSrv.Close)
	pdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pdHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"paRequired":true,"authNumber":"AUTH-1"}`))
	}))
	t.Cleanup(pdSrv.Close)
	rn := New(Config{
		Driver:             scenariodriver.New(scenariodriver.Config{ProviderDataURL: mainSrv.URL}),
		ProviderDataDriver: scenariodriver.New(scenariodriver.Config{ProviderDataURL: pdSrv.URL}),
		Bus:                event.NewBus(fixedClock),
	})
	if _, err := rn.Run(t.Context(), Req{Lane: "ehr", UC: "uc03", Branch: "bridge-refuse"}); err != nil {
		t.Fatal(err)
	}
	if mainHits != 1 || pdHits != 0 {
		t.Fatalf("bridge-refuse: main=%d pd=%d, want the main child only", mainHits, pdHits)
	}
	if _, err := rn.Run(t.Context(), Req{Lane: "ehr", UC: "freeform", Member: "MBR-OX"}); err != nil {
		t.Fatal(err)
	}
	if mainHits != 1 || pdHits != 1 {
		t.Fatalf("freeform: main=%d pd=%d, want the provider-data child", mainHits, pdHits)
	}
}
