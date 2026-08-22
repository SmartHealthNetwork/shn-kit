package runner

import (
	"encoding/json"
	"fmt"
	"strings"
)

// providerDataLaneUnavailable is the sentence Run/Start answer when the
// provider-data lane is requested on a Kit without the Java trio: the lane's
// gateway child originates off the bundled FHIR data server (its clinical-logic
// engine fills the questionnaires), so without that server there is no lane.
const providerDataLaneUnavailable = "the provider-data lane needs the packaged Java trio (its gateway originates off the bundled FHIR data server)"

// providerDataRows is the "provider-data" lane's row table. A SECOND gateway
// child runs the provider-data origination profile: every scenario's order and
// clinical evidence is READ from the bundled FHIR data server (nothing is
// scripted), and the payer legs are routed by a static directory to the hosted
// Da Vinci reference payer rather than the built-in sandbox payer. Each row
// asserts the bar the live reference-payer gate sets — the attested/auto-filled
// answers TRACE TO THE SEEDED DATA, and the verdict is the reference payer's
// AUTH-NNNN. The sandbox payer mints PA-<hex>, so a sandbox answer can never
// pass a row here (requireReferenceAuth).
//
// Response contracts mirror the provider-data /scenario/* decode structs the
// live gate reads (copied, never imported: the kit's publish boundary forbids
// importing the private repository).
var providerDataRows = map[string]rowFunc{
	"uc01": pdUC01,
	"uc02": pdUC02,
	"uc03": pdUC03,
	"uc04": pdUC04,
	"uc05": pdUC05,
	"uc06": pdUC06,
	"uc07": pdUC07,
	"uc08": pdUC08,
}

// pdScenario POSTs body to path on the provider-data gateway child's
// /scenario/* base (Config.ProviderDataDriver) and decodes a 200 into out.
func pdScenario(rn *Runner, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("runner: marshal %s request: %w", path, err)
	}
	res, err := rn.cfg.ProviderDataDriver.RunProviderDataScenario(path, string(b))
	if err != nil {
		return fmt.Errorf("runner: POST %s: %w", path, err)
	}
	if res.Status != 200 {
		return fmt.Errorf("runner: POST %s: status %d: %s", path, res.Status, excerpt(res.Body))
	}
	if out != nil {
		if err := json.Unmarshal(res.Body, out); err != nil {
			return fmt.Errorf("runner: decode %s response: %w", path, err)
		}
	}
	return nil
}

// pdVerdict is the provider-data CRD→DTR→PAS response shape the reference-payer
// rows read. QRAnswers is the attested/auto-filled questionnaire answer VALUES
// keyed by linkId — the traces-to-seed evidence each row checks.
type pdVerdict struct {
	PARequired    bool              `json:"paRequired"`
	AuthNumber    string            `json:"authNumber"`
	ValidUntil    string            `json:"validUntil"`
	AmendmentCorr string            `json:"amendmentCorr"`
	Attested      bool              `json:"attested"`
	QRAnswers     map[string]string `json:"qrAnswers"`
}

// requireReferenceAuth is the anti-fallback fence: the reference payer resolves
// a held request to an AUTH-NNNN authorization; the built-in sandbox payer mints
// PA-<hex>, so a run that silently fell back to the sandbox can never pass.
func requireReferenceAuth(uc string, v pdVerdict) error {
	if !v.PARequired {
		return fmt.Errorf("runner: provider-data/%s: paRequired=false, want true", uc)
	}
	if !strings.HasPrefix(v.AuthNumber, "AUTH-") {
		return fmt.Errorf("runner: provider-data/%s: authNumber=%q, want the reference payer's AUTH-NNNN (the sandbox payer mints PA-<hex> — this run did not reach the reference payer)", uc, v.AuthNumber)
	}
	return nil
}

// requireAnswer asserts the questionnaire answer at linkID is present and, when
// wantContains is non-empty, carries the seeded value.
func requireAnswer(uc string, v pdVerdict, linkID, wantContains string) error {
	got := v.QRAnswers[linkID]
	if got == "" {
		return fmt.Errorf("runner: provider-data/%s: questionnaire answer %s is empty — the attestation did not trace to the seeded record", uc, linkID)
	}
	if wantContains != "" && !strings.Contains(got, wantContains) {
		return fmt.Errorf("runner: provider-data/%s: questionnaire answer %s = %q, want it to carry the seeded value %q", uc, linkID, got, wantContains)
	}
	return nil
}

// pdUC01: eligibility off the member's OWN seeded Coverage — covered ⇒ the
// active Coverage; notcovered ⇒ the seeded terminated Coverage, a reason-bearing
// negative that traces to a seeded artifact rather than an absence.
func pdUC01(rn *Runner, branch string) (string, error) {
	var out struct {
		Covered bool   `json:"covered"`
		Reason  string `json:"reason"`
	}
	if err := pdScenario(rn, "/scenario/uc01", map[string]string{"branch": branch}, &out); err != nil {
		return "", err
	}
	want := branch == "covered"
	if out.Covered != want {
		return "", fmt.Errorf("runner: provider-data/uc01(%s): covered=%v, want %v (reason=%q)", branch, out.Covered, want, out.Reason)
	}
	if !want && !strings.Contains(out.Reason, "coverage-terminated") {
		return "", fmt.Errorf("runner: provider-data/uc01(notcovered): reason=%q, want coverage-terminated (the seeded terminated Coverage)", out.Reason)
	}
	return fmt.Sprintf("covered=%v off the seeded Coverage: %s", out.Covered, out.Reason), nil
}

// pdUC02: a seeded hospital-bed order (with its reason) drives the coverage
// check to the reference payer — covered, no prior authorization, no
// questionnaire. All three facts are asserted: the questionnaire axis is the one
// the order's reason code flips, so "no PA" alone would pass for the wrong reason.
func pdUC02(rn *Runner, branch string) (string, error) {
	var out struct {
		Covered     string `json:"covered"`
		PARequired  bool   `json:"paRequired"`
		NeedsDTR    bool   `json:"needsDTR"`
		CardSummary string `json:"cardSummary"`
	}
	if err := pdScenario(rn, "/scenario/uc02", map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.Covered != "covered" {
		return "", fmt.Errorf("runner: provider-data/uc02: covered=%q, want covered", out.Covered)
	}
	if out.PARequired {
		return "", fmt.Errorf("runner: provider-data/uc02: paRequired=true, want false")
	}
	if out.NeedsDTR {
		return "", fmt.Errorf("runner: provider-data/uc02: needsDTR=true, want false (the seeded order's reason satisfies the payer's documentation rule)")
	}
	return fmt.Sprintf("covered, no prior authorization, no questionnaire (%s)", out.CardSummary), nil
}

// pdUC03: a seeded home-oxygen order; the questionnaire's oxygen-saturation and
// blood-gas items are COMPUTED by the clinical-logic engine from the member's
// own seeded observations (87 / 53 — distinct from every other persona's
// values), then approved by the reference payer.
func pdUC03(rn *Runner, branch string) (string, error) {
	var out pdVerdict
	if err := pdScenario(rn, "/scenario/uc03", map[string]any{}, &out); err != nil {
		return "", err
	}
	if got := out.QRAnswers["2.2"]; got != "87" {
		return "", fmt.Errorf("runner: provider-data/uc03: questionnaire answer 2.2 (oxygen saturation) = %q, want 87 — the auto-fill did not compute it from the seeded observation", got)
	}
	if got := out.QRAnswers["2.3"]; got != "53" {
		return "", fmt.Errorf("runner: provider-data/uc03: questionnaire answer 2.3 (blood gas) = %q, want 53 — the auto-fill did not compute it from the seeded observation", got)
	}
	if err := requireReferenceAuth("uc03", out); err != nil {
		return "", err
	}
	return fmt.Sprintf("auto-filled from the chart (2.2=%s, 2.3=%s), approved %s", out.QRAnswers["2.2"], out.QRAnswers["2.3"], out.AuthNumber), nil
}

// pdUC04: a seeded home-health therapy order; the adaptive questionnaire is
// driven group by group and attested from the chart — the service category
// (1.1) from the order's code, the diagnosis (3.1) from its reason, the
// functional limitations (3.2) and treatment goals (3.3) from the assessment and
// goal it references — then approved in one submission by the reference payer.
func pdUC04(rn *Runner, branch string) (string, error) {
	var out pdVerdict
	if err := pdScenario(rn, "/scenario/uc04", map[string]any{}, &out); err != nil {
		return "", err
	}
	for _, want := range []struct{ linkID, contains string }{
		{"1.1", "91251008"},
		{"3.1", "Cerebral infarction"},
		{"3.2", ""},
		{"3.3", ""},
	} {
		if err := requireAnswer("uc04", out, want.linkID, want.contains); err != nil {
			return "", err
		}
	}
	if err := requireReferenceAuth("uc04", out); err != nil {
		return "", err
	}
	return fmt.Sprintf("home-health therapy order attested from the chart (1.1 %s; group 3: %s / %s / %s), approved %s",
		out.QRAnswers["1.1"], out.QRAnswers["3.1"], out.QRAnswers["3.2"], out.QRAnswers["3.3"], out.AuthNumber), nil
}

// pdUC05: the seeded home-health order is held by the reference payer; the
// member's own operative report at the other facility is retrieved under
// consent and carried on the amended re-submit. noconsent: no permit ⇒ the
// federated query is refused and no authorization is issued.
func pdUC05(rn *Runner, branch string) (string, error) {
	var out struct {
		pdVerdict
		FacilityID    string `json:"facilityId"`
		ConsentDenied bool   `json:"consentDenied"`
	}
	if err := pdScenario(rn, "/scenario/uc05", map[string]string{"branch": branch}, &out); err != nil {
		return "", err
	}
	if branch == "noconsent" {
		if !out.ConsentDenied {
			return "", fmt.Errorf("runner: provider-data/uc05(noconsent): consentDenied=false, want true")
		}
		if out.AuthNumber != "" {
			return "", fmt.Errorf("runner: provider-data/uc05(noconsent): authNumber=%q, want empty", out.AuthNumber)
		}
		return "consent denied: federated query refused, no authorization issued", nil
	}
	if err := requireReferenceAuth("uc05", out.pdVerdict); err != nil {
		return "", err
	}
	if out.FacilityID != "metro-spine" {
		return "", fmt.Errorf("runner: provider-data/uc05(%s): facilityId=%q, want metro-spine (the member's own facility record)", branch, out.FacilityID)
	}
	return fmt.Sprintf("approved via federated evidence from facility %s, %s", out.FacilityID, out.AuthNumber), nil
}

// pdUC06: the seeded home-health order is held; the clinician's functional-status
// attestation supersedes the chart's value inside the questionnaire group on the
// amended re-submit, which the reference payer approves.
func pdUC06(rn *Runner, branch string) (string, error) {
	var out pdVerdict
	if err := pdScenario(rn, "/scenario/uc06", map[string]any{}, &out); err != nil {
		return "", err
	}
	if err := requireAnswer("uc06", out, "1.1", "91251008"); err != nil {
		return "", err
	}
	if err := requireAnswer("uc06", out, "3.1", "Muscle weakness"); err != nil {
		return "", err
	}
	if !out.Attested {
		return "", fmt.Errorf("runner: provider-data/uc06: attested=false, want true (the clinician's functional-status item was not applied)")
	}
	if out.AmendmentCorr == "" {
		return "", fmt.Errorf("runner: provider-data/uc06: amendmentCorr empty, want the amended re-submit's correlation id")
	}
	if err := requireReferenceAuth("uc06", out); err != nil {
		return "", err
	}
	return fmt.Sprintf("held, clinician attestation amended (%s), approved %s", out.AmendmentCorr, out.AuthNumber), nil
}

// pdUC07: the seeded home-health order is held; the patient's attestation (via
// the patient surface) supersedes the chart's value inside the questionnaire
// group on the amended re-submit, which the reference payer approves. No
// separate patient read-back on this lane.
func pdUC07(rn *Runner, branch string) (string, error) {
	var out pdVerdict
	if err := pdScenario(rn, "/scenario/uc07", map[string]string{"answer": ""}, &out); err != nil {
		return "", err
	}
	if err := requireAnswer("uc07", out, "1.1", "91251008"); err != nil {
		return "", err
	}
	if !out.Attested {
		return "", fmt.Errorf("runner: provider-data/uc07: attested=false, want true (the patient's functional-status item was not applied)")
	}
	if out.AmendmentCorr == "" {
		return "", fmt.Errorf("runner: provider-data/uc07: amendmentCorr empty, want the amended re-submit's correlation id")
	}
	if err := requireReferenceAuth("uc07", out); err != nil {
		return "", err
	}
	return fmt.Sprintf("held, patient attestation amended (%s), approved %s", out.AmendmentCorr, out.AuthNumber), nil
}

// pdUC08: a seeded order for an excluded service; the reference payer's
// not-covered verdict is carried to a formal denial with its reason.
func pdUC08(rn *Runner, branch string) (string, error) {
	var out struct {
		Denied     bool   `json:"denied"`
		AuthNumber string `json:"authNumber"`
		Rationale  string `json:"rationale"`
	}
	if err := pdScenario(rn, "/scenario/uc08", map[string]any{}, &out); err != nil {
		return "", err
	}
	if !out.Denied {
		return "", fmt.Errorf("runner: provider-data/uc08: denied=false, want true")
	}
	if out.AuthNumber != "" {
		return "", fmt.Errorf("runner: provider-data/uc08: authNumber=%q, want empty — a denial never yields an authorization", out.AuthNumber)
	}
	if out.Rationale == "" {
		return "", fmt.Errorf("runner: provider-data/uc08: rationale is empty — the payer's reason did not travel back")
	}
	return "denied: " + out.Rationale, nil
}
