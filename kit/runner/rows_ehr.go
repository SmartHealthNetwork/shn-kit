package runner

import (
	"encoding/json"
	"fmt"
	"strings"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
)

// ehrLaneUnavailable is the sentence Run/Start answer when the Plain EHR lane
// is requested on a Kit without the Java trio: the lane's gateway child
// originates off the bundled FHIR data server (its clinical-logic engine fills
// the questionnaires), so without that server there is no lane. The one
// exception is the uc03 bridge-refuse row, which originates on the main child
// (see ehrUC03).
const ehrLaneUnavailable = "the Plain EHR lane needs the packaged Java trio (its gateway originates off the bundled FHIR data server)"

// ehrRows is the "ehr" lane's row table — the Plain EHR lane: the
// provider-data origination profile on the Kit's SECOND gateway child. Every
// scenario's order and clinical evidence is READ from the bundled FHIR data
// server (nothing is scripted), and the payer legs are routed by a static
// directory to the hosted Da Vinci reference payer. Each row asserts the bar
// the live reference-payer gate sets — the attested/auto-filled answers TRACE
// TO THE SEEDED DATA, and the verdict is the reference payer's AUTH-NNNN
// (requireReferenceAuth is the anti-fallback fence: any other verdict prefix
// means the run never reached the reference payer).
//
// Response contracts mirror the /scenario/* decode structs the live gate reads
// (copied, never imported: the kit's publish boundary forbids importing the
// private repository).
var ehrRows = map[string]rowFunc{
	"uc01": ehrUC01,
	"uc02": ehrUC02,
	"uc03": ehrUC03,
	"uc04": ehrUC04,
	"uc05": ehrUC05,
	"uc06": ehrUC06,
	"uc07": ehrUC07,
	"uc08": ehrUC08,
}

// ehrScenario POSTs body (marshaled to JSON) to path on the Plain-EHR gateway
// child's /scenario/* base (Config.ProviderDataDriver) and decodes a 200
// response into out (out may be nil to discard the body). A non-200 status is
// an error carrying an excerpt of the body.
func ehrScenario(rn *Runner, path string, body any, out any) error {
	return postScenario(rn.cfg.ProviderDataDriver, path, body, out)
}

// ehrScenarioMain POSTs to the MAIN child's /scenario/* base (Config.Driver) —
// used by the two rows that are not provider-data originations: the Da Vinci
// lane's eligibility row (rows_conformant.go) and the bridge-refuse bridging
// demo below.
func ehrScenarioMain(rn *Runner, path string, body any, out any) error {
	return postScenario(rn.cfg.Driver, path, body, out)
}

// postScenario is ehrScenario/ehrScenarioMain's shared body — the child to
// talk to is the only thing that differs.
func postScenario(drv *scenariodriver.Driver, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("runner: marshal %s request: %w", path, err)
	}
	res, err := drv.RunProviderDataScenario(path, string(b))
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

// pdVerdict is the CRD→DTR→PAS response shape the Plain-EHR rows read.
// QRAnswers is the attested/auto-filled questionnaire answer VALUES keyed by
// linkId — the traces-to-seed evidence each row checks.
type pdVerdict struct {
	PARequired    bool              `json:"paRequired"`
	AuthNumber    string            `json:"authNumber"`
	ValidUntil    string            `json:"validUntil"`
	AmendmentCorr string            `json:"amendmentCorr"`
	Attested      bool              `json:"attested"`
	QRAnswers     map[string]string `json:"qrAnswers"`
}

// uc03Resp is the older CRD→DTR→PAS response shape the two rows that do NOT
// read the chart decode (the bridging demo and the free-form dispatch). A
// local decode struct, not a cross-import (kit's publish boundary forbids
// importing the private substrate module).
type uc03Resp struct {
	PARequired bool   `json:"paRequired"`
	AuthNumber string `json:"authNumber"`
	ValidUntil string `json:"validUntil"`
	QRItems    []struct {
		LinkID    string `json:"linkId"`
		Answer    string `json:"answer"`
		Origin    string `json:"origin"`
		SourceRef string `json:"sourceRef"`
	} `json:"qrItems"`
	PendedItems []string `json:"pendedItems"`
}

// requireReferenceAuth is the anti-fallback fence: the reference payer resolves
// a held request to an AUTH-NNNN authorization, so a run that silently fell
// back to some other payer can never pass.
func requireReferenceAuth(uc string, v pdVerdict) error {
	if !v.PARequired {
		return fmt.Errorf("runner: ehr/%s: paRequired=false, want true", uc)
	}
	if !strings.HasPrefix(v.AuthNumber, "AUTH-") {
		return fmt.Errorf("runner: ehr/%s: authNumber=%q, want the hosted Da Vinci reference payer's AUTH-NNNN — this run did not reach the reference payer", uc, v.AuthNumber)
	}
	return nil
}

// requireAnswer asserts the questionnaire answer at linkID is present and, when
// wantContains is non-empty, carries the seeded value.
func requireAnswer(uc string, v pdVerdict, linkID, wantContains string) error {
	got := v.QRAnswers[linkID]
	if got == "" {
		return fmt.Errorf("runner: ehr/%s: questionnaire answer %s is empty — the attestation did not trace to the seeded record", uc, linkID)
	}
	if wantContains != "" && !strings.Contains(got, wantContains) {
		return fmt.Errorf("runner: ehr/%s: questionnaire answer %s = %q, want it to carry the seeded value %q", uc, linkID, got, wantContains)
	}
	return nil
}

// ehrUC01: eligibility off the member's OWN seeded Coverage — covered ⇒ the
// active Coverage; notcovered ⇒ the seeded terminated Coverage, a reason-bearing
// negative that traces to a seeded artifact rather than an absence.
func ehrUC01(rn *Runner, branch string) (string, error) {
	var out struct {
		Covered bool   `json:"covered"`
		Reason  string `json:"reason"`
	}
	if err := ehrScenario(rn, "/scenario/uc01", map[string]string{"branch": branch}, &out); err != nil {
		return "", err
	}
	want := branch == "covered"
	if out.Covered != want {
		return "", fmt.Errorf("runner: ehr/uc01(%s): covered=%v, want %v (reason=%q)", branch, out.Covered, want, out.Reason)
	}
	if !want && !strings.Contains(out.Reason, "coverage-terminated") {
		return "", fmt.Errorf("runner: ehr/uc01(notcovered): reason=%q, want coverage-terminated (the seeded terminated Coverage)", out.Reason)
	}
	return fmt.Sprintf("covered=%v off the seeded Coverage: %s", out.Covered, out.Reason), nil
}

// ehrUC02: a seeded hospital-bed order (with its reason) drives the coverage
// check to the reference payer — covered, no prior authorization, no
// questionnaire. All three facts are asserted: the questionnaire axis is the one
// the order's reason code flips, so "no PA" alone would pass for the wrong reason.
func ehrUC02(rn *Runner, branch string) (string, error) {
	var out struct {
		Covered     string `json:"covered"`
		PARequired  bool   `json:"paRequired"`
		NeedsDTR    bool   `json:"needsDTR"`
		CardSummary string `json:"cardSummary"`
	}
	if err := ehrScenario(rn, "/scenario/uc02", map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.Covered != "covered" {
		return "", fmt.Errorf("runner: ehr/uc02: covered=%q, want covered", out.Covered)
	}
	if out.PARequired {
		return "", fmt.Errorf("runner: ehr/uc02: paRequired=true, want false")
	}
	if out.NeedsDTR {
		return "", fmt.Errorf("runner: ehr/uc02: needsDTR=true, want false (the seeded order's reason satisfies the payer's documentation rule)")
	}
	return fmt.Sprintf("covered, no prior authorization, no questionnaire (%s)", out.CardSummary), nil
}

// ehrUC03 dispatches on branch: "" is the chart-driven home-oxygen row
// (ehrUC03Order); "bridge-refuse" is the bridging demo — the SAME prior-authorization
// order every other lane originates, sent on the MAIN child's default profile to the
// bridging demo payer whose declared contract lines force a refusal (its persona's
// Coverage names that payer; the directory routes it). Bridging-only: it is the one
// Plain-EHR row that does not read the chart, and the one that needs no Java trio.
func ehrUC03(rn *Runner, branch string) (string, error) {
	if branch == "bridge-refuse" {
		// A req.Branch switch inside handleUC03 carries the member
		// selection (gateway/engine/originate.go): "bridge-refuse" selects
		// the demo refuse persona MBR-BRIDGE-REFUSE — expected to fail at
		// the PAS leg when run live, which is the run failing as designed,
		// not a bug.
		var out uc03Resp
		if err := ehrScenarioMain(rn, "/scenario/uc03", map[string]string{"branch": branch}, &out); err != nil {
			return "", err
		}
		if !out.PARequired {
			return "", fmt.Errorf("runner: ehr/uc03: paRequired=false, want true")
		}
		if out.AuthNumber == "" {
			return "", fmt.Errorf("runner: ehr/uc03: empty authNumber")
		}
		// NO per-item assertion here, and the reason is a fact about the
		// questionnaire rather than about this Kit. This row's order is the
		// payer's prior-authorization family, and that payer's questionnaire
		// for it asks for no computed values at all — it is a group, a
		// display line and one boolean for a clinician to answer. So a
		// populate of it yields no filled items on ANY posture: not with the
		// packaged clinical-reasoning engine, which correctly returns an
		// empty in-progress response, and not without it. An item-count
		// assertion here would demand something no engine can produce.
		//
		// What IS this row's contract is asserted above — the payer required
		// prior authorization and issued an authorization number — plus the
		// exhibit itself, which is the refusal this run reaches at its PAS
		// leg when the recipient's contract lines force one.
		return fmt.Sprintf("bridging demo: approved, auth %s, %d QR items", out.AuthNumber, len(out.QRItems)), nil
	}
	return ehrUC03Order(rn)
}

// ehrUC03Order: a seeded home-oxygen order; the questionnaire's
// oxygen-saturation and blood-gas items are COMPUTED by the clinical-logic
// engine from the member's own seeded observations (87 / 53 — distinct from
// every other persona's values), then approved by the reference payer.
func ehrUC03Order(rn *Runner) (string, error) {
	var out pdVerdict
	if err := ehrScenario(rn, "/scenario/uc03", map[string]any{}, &out); err != nil {
		return "", err
	}
	if got := out.QRAnswers["2.2"]; got != "87" {
		return "", fmt.Errorf("runner: ehr/uc03: questionnaire answer 2.2 (oxygen saturation) = %q, want 87 — the auto-fill did not compute it from the seeded observation", got)
	}
	if got := out.QRAnswers["2.3"]; got != "53" {
		return "", fmt.Errorf("runner: ehr/uc03: questionnaire answer 2.3 (blood gas) = %q, want 53 — the auto-fill did not compute it from the seeded observation", got)
	}
	if err := requireReferenceAuth("uc03", out); err != nil {
		return "", err
	}
	return fmt.Sprintf("auto-filled from the chart (2.2=%s, 2.3=%s), approved %s", out.QRAnswers["2.2"], out.QRAnswers["2.3"], out.AuthNumber), nil
}

// ehrUC04: a seeded home-health therapy order; the adaptive questionnaire is
// driven group by group and attested from the chart — the service category
// (1.1) from the order's code, the diagnosis (3.1) from its reason, the
// functional limitations (3.2) and treatment goals (3.3) from the assessment and
// goal it references — then approved in one submission by the reference payer.
func ehrUC04(rn *Runner, branch string) (string, error) {
	var out pdVerdict
	if err := ehrScenario(rn, "/scenario/uc04", map[string]any{}, &out); err != nil {
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

// ehrUC05: the seeded home-health order is held by the reference payer; the
// member's own operative report at the other facility is retrieved under
// consent and carried on the amended re-submit. noconsent: no permit ⇒ the
// federated query is refused and no authorization is issued.
func ehrUC05(rn *Runner, branch string) (string, error) {
	var out struct {
		pdVerdict
		FacilityID    string `json:"facilityId"`
		ConsentDenied bool   `json:"consentDenied"`
	}
	if err := ehrScenario(rn, "/scenario/uc05", map[string]string{"branch": branch}, &out); err != nil {
		return "", err
	}
	if branch == "noconsent" {
		if !out.ConsentDenied {
			return "", fmt.Errorf("runner: ehr/uc05(noconsent): consentDenied=false, want true")
		}
		if out.AuthNumber != "" {
			return "", fmt.Errorf("runner: ehr/uc05(noconsent): authNumber=%q, want empty", out.AuthNumber)
		}
		return "consent denied: federated query refused, no authorization issued", nil
	}
	if err := requireReferenceAuth("uc05", out.pdVerdict); err != nil {
		return "", err
	}
	if out.FacilityID != "metro-spine" {
		return "", fmt.Errorf("runner: ehr/uc05(%s): facilityId=%q, want metro-spine (the member's own facility record)", branch, out.FacilityID)
	}
	return fmt.Sprintf("approved via federated evidence from facility %s, %s", out.FacilityID, out.AuthNumber), nil
}

// ehrUC06: the seeded home-health order is held; the clinician's functional-status
// attestation supersedes the chart's value inside the questionnaire group on the
// amended re-submit, which the reference payer approves.
func ehrUC06(rn *Runner, branch string) (string, error) {
	var out pdVerdict
	if err := ehrScenario(rn, "/scenario/uc06", map[string]any{}, &out); err != nil {
		return "", err
	}
	if err := requireAnswer("uc06", out, "1.1", "91251008"); err != nil {
		return "", err
	}
	if err := requireAnswer("uc06", out, "3.1", "Muscle weakness"); err != nil {
		return "", err
	}
	if !out.Attested {
		return "", fmt.Errorf("runner: ehr/uc06: attested=false, want true (the clinician's functional-status item was not applied)")
	}
	if out.AmendmentCorr == "" {
		return "", fmt.Errorf("runner: ehr/uc06: amendmentCorr empty, want the amended re-submit's correlation id")
	}
	if err := requireReferenceAuth("uc06", out); err != nil {
		return "", err
	}
	return fmt.Sprintf("held, clinician attestation amended (%s), approved %s", out.AmendmentCorr, out.AuthNumber), nil
}

// ehrUC07: the seeded home-health order is held; the patient's attestation (via
// the patient surface) supersedes the chart's value inside the questionnaire
// group on the amended re-submit, which the reference payer approves. No
// separate patient read-back on this lane.
func ehrUC07(rn *Runner, branch string) (string, error) {
	var out pdVerdict
	if err := ehrScenario(rn, "/scenario/uc07", map[string]string{"answer": ""}, &out); err != nil {
		return "", err
	}
	if err := requireAnswer("uc07", out, "1.1", "91251008"); err != nil {
		return "", err
	}
	if !out.Attested {
		return "", fmt.Errorf("runner: ehr/uc07: attested=false, want true (the patient's functional-status item was not applied)")
	}
	if out.AmendmentCorr == "" {
		return "", fmt.Errorf("runner: ehr/uc07: amendmentCorr empty, want the amended re-submit's correlation id")
	}
	if err := requireReferenceAuth("uc07", out); err != nil {
		return "", err
	}
	return fmt.Sprintf("held, patient attestation amended (%s), approved %s", out.AmendmentCorr, out.AuthNumber), nil
}

// ehrUC08: a seeded order for an excluded service; the reference payer's
// not-covered verdict is carried to a formal denial with its reason.
func ehrUC08(rn *Runner, branch string) (string, error) {
	var out struct {
		Denied     bool   `json:"denied"`
		AuthNumber string `json:"authNumber"`
		Rationale  string `json:"rationale"`
	}
	if err := ehrScenario(rn, "/scenario/uc08", map[string]any{}, &out); err != nil {
		return "", err
	}
	if !out.Denied {
		return "", fmt.Errorf("runner: ehr/uc08: denied=false, want true")
	}
	if out.AuthNumber != "" {
		return "", fmt.Errorf("runner: ehr/uc08: authNumber=%q, want empty — a denial never yields an authorization", out.AuthNumber)
	}
	if out.Rationale == "" {
		return "", fmt.Errorf("runner: ehr/uc08: rationale is empty — the payer's reason did not travel back")
	}
	return "denied: " + out.Rationale, nil
}

// ehrFreeform drives the "freeform" row: a caller-named member dispatched
// against THEIR OWN provider data via the Plain-EHR child's POST
// /scenario/dispatch — no answer book, no lane/uc03 baked assumptions
// (gateway/engine/originate_homeoxygen.go's handleDispatch). Its response
// shares uc03Resp's wire shape byte-for-byte (paRequired/authNumber/
// validUntil/qrItems/pendedItems — the extra fields that route carries,
// amendmentCorr/attested/qrAnswers, decode as zero values here and are
// unused).
//
// When ehrScenario's error is the recognized PROVIDER-side member-unknown wire
// shape — a definitive fact about the operator's OWN connected system — the row
// names the constraint in plain language (freeformProviderUnknownMemberSentence)
// instead of relaying the raw status/body. A PAYER-side failure is NOT relabeled:
// since shn-gateway v0.28.0 the payer's real application answer is relayed
// verbatim through the sealed message frame (its real status + OperationOutcome),
// so surfacing that genuine body beats the old likely-cause sentence the runner
// used to synthesize when the payload-blind Hub discarded the payer's reason.
func ehrFreeform(rn *Runner, member string) (string, error) {
	var out uc03Resp
	if err := ehrScenario(rn, "/scenario/dispatch", map[string]string{"member": member}, &out); err != nil {
		if isFreeformProviderUnknownMember(err) {
			return "", fmt.Errorf("runner: ehr/freeform(%s): %s (%v)", member, freeformProviderUnknownMemberSentence, err)
		}
		return "", err
	}
	if out.AuthNumber == "" {
		return "", fmt.Errorf("runner: ehr/freeform(%s): empty authNumber", member)
	}
	return fmt.Sprintf("dispatched for member %s: approved, auth %s", member, out.AuthNumber), nil
}

// freeformProviderUnknownMemberSentence is the plain-language sentence for
// the PROVIDER-side wire shape: status 400, body {"error":"unknown member"}
// (gateway/engine/originate_homeoxygen.go's originateDispatch, its own
// ResolvePatient guard against the gateway's OWN connected system; pinned
// live by gateway/engine/originate_dispatch_test.go's
// TestHandleDispatch_UnknownMember, row 4: {"member":"MBR-NOPE"} → 400
// "unknown member"). This is NOT a payer concern — the member is simply
// missing from the partner's own connected system's data (a browse-race or
// a typo'd id typed straight into the free-form panel), so the sentence
// states that fact plainly rather than reaching for the payer-coverage
// framing the payer-side shape below actually needs.
const freeformProviderUnknownMemberSentence = "this member id isn't in the connected system's data — check the id or refresh the patient list (a browsed patient always has one)"

// isFreeformProviderUnknownMember recognizes the PROVIDER-side wire shape
// (status 400, body {"error":"unknown member"}) — see
// freeformProviderUnknownMemberSentence's doc for the origin. Every other
// failure keeps its raw detail unchanged: a genuine policy denial
// ("preauthorization not approved") and — since shn-gateway v0.28.0 relays the
// payer's application answer verbatim through the sealed message frame — the
// payer's own real status + OperationOutcome both pass through as-is (the runner
// no longer synthesizes a likely-cause payer-routing sentence). See
// TestRun_FreeformPolicyDenial_NotRelabeled.
func isFreeformProviderUnknownMember(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 400") && strings.Contains(msg, `"unknown member"`)
}

// uc07PatientSurfaceReadBack resolves the UC-07 HCPCS PCI (Config.UC07PCI)
// and reads back the patient-surface /authorizations render, returning the
// count of approved rows and the total row count. Used by the conformant
// lane's uc07 row too: the read-back is lane-agnostic — it always reads the
// same patient-surface projection.
func uc07PatientSurfaceReadBack(rn *Runner) (approved, total int, err error) {
	if rn.cfg.UC07PCI == nil {
		return 0, 0, fmt.Errorf("no UC07PCI resolver configured")
	}
	pci, err := rn.cfg.UC07PCI()
	if err != nil {
		return 0, 0, fmt.Errorf("resolve PCI: %w", err)
	}
	views, err := rn.cfg.Driver.GetAuthorizations(pci)
	if err != nil {
		return 0, 0, fmt.Errorf("read-back authorizations: %w", err)
	}
	for _, v := range views {
		if v.Status == "approved" {
			approved++
		}
	}
	return approved, len(views), nil
}
