package runner

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// conformantRows is the "conformant" lane's row table: each row drives the
// child's Da Vinci ingress directly (CRD order-select, DTR
// $questionnaire-package, PAS $submit — all UDAP B2B direct-bearer-authed)
// via scenariodriver.Driver. Sandbox-payer verdict sources: the sandbox
// adjudicator (gateway/engine/adjudicator.go, OrderSelect / PriorAuth) and
// test/ingressconformance (read at design time; this package never imports
// it — the shapes below were cross-checked against it and, for the PAS
// submit rows, EMPIRICALLY VERIFIED against a live harness.StartWithIngress
// run before this file was written, which caught and corrected several rows
// against that ground truth).
var conformantRows = map[string]rowFunc{
	"uc01": conformantUC01,
	"uc02": conformantUC02,
	"uc03": conformantUC03,
	"uc04": conformantUC04,
	"uc05": conformantUC05,
	"uc06": conformantUC06,
	"uc07": conformantUC07,
	"uc08": conformantUC08,
}

// ConformantMemberNotOnConnectedEHRSentence is the plain-language sentence a
// conformant-lane row's failure Detail names when the gateway's OWN ingress
// subject-bind rejects the row's hardcoded member — status 400, body
// {"error":"unknown member"}. This is a deliberate rejection: the other lane
// keeps running seeded when the swap target carries the seeded members, but
// under an applied EHR swap this shape means the member a conformant row hardcodes
// (MBR-COVERED, MBR-NOTCOVERED, MBR-UC06, MBR-UC07HCPCS, MBR-UC08) is not a
// Patient on the partner's connected FHIR server — the remedy is loading
// the demo persona bundle (kit/seed/demo-personas-conformant.json, manual
// transaction-POST) onto it, or restoring demo data.
//
// Pinned live at gateway/engine/ingress_crd.go:104 (ingressCRDSubjectPCI's
// g.cfg.SoR.ResolvePatient miss — hit by every PostCRD-driven row) and
// gateway/engine/pas_native.go:171 (ingressPASNativeSubjectPCI's identical
// miss — hit by every SubmitPAS-driven row), both via
// gateway/engine/gateway.go:524's writeJSON(w, http.StatusBadRequest,
// map[string]string{"error": "unknown member"}) — the EXACT SAME byte shape
// kit/runner/rows_ehr.go's freeformProviderUnknownMemberSentence recognizes
// for the SoR's own free-form-side unknown-member guard
// (originate_homeoxygen.go:61); this is that same wire shape's
// conformant-ingress twin (gateway/engine/ingress_crd_test.go's
// TestIngressSubjectPCI_UnknownReferenceFailsClosed pins the fail-closed
// contract (status != 0) and test/ingressconformance/crd_adversarial_test.go's
// TestCRDIngress_UnknownMember_RejectedNoLeg pins ≥400 end-to-end; the exact
// byte shape (400 + the literal "unknown member" body) is confirmed by
// reading ingress_crd.go/pas_native.go's writeJSON calls directly, the same
// evidence-first method rows_ehr.go's own analogous constant documents for
// its guard).
//
// The conformant lane's sentence differs from the ehr/free-form lane's
// (rows_ehr.go's freeformProviderUnknownMemberSentence, "check the id or
// refresh the patient list") because the two lanes' members come from
// DIFFERENT places: a free-form member is caller-typed (so "check the id"
// is the right remedy), while every conformant-lane member is a HARDCODED
// seeded persona the row itself chose (never caller input) — so the honest
// remedy here is "load the demo personas," not "check the id."
//
// Safe to map UNCONDITIONALLY, with no swap-state check: in un-swapped demo
// mode every conformant row's member is a persona the memstub
// (engine.NewStubHolderData, gateway/engine/holderdata.go) always resolves,
// so this shape cannot occur there — it is reachable ONLY once an EHR swap
// has repointed the gateway's SoR at a partner server missing the persona.
//
// EXPORTED so the live kit gate's both-states rows
// (test/kitlive/byo_test.go) assert against the constant itself rather than
// retyping the sentence — copy that a test retypes can drift from the copy
// the product actually renders.
const ConformantMemberNotOnConnectedEHRSentence = "this member isn't on your connected EHR — load the demo personas or restore demo data"

// isConformantIngressUnknownMember recognizes the byte-real ingress
// subject-bind-miss shape (status 400, body containing
// {"error":"unknown member"}) — a conservative, exact-shape match. It does
// NOT fire on the DTR ingress's own DIFFERENT unresolvable-patient guard
// (gateway/engine/ingress.go's handleDTRIngress: status 403, body
// {"error":"carried coverage patient does not resolve"}) — a different
// status AND a different body, left with its raw detail unchanged (see
// TestRun_ConformantUC02_OtherIngressFailure_NotRelabeled's regression row).
func isConformantIngressUnknownMember(status int, body []byte) bool {
	return status == http.StatusBadRequest && strings.Contains(string(body), `"unknown member"`)
}

// conformantIngressErr builds a conformant row's failure error for a
// non-200 ingress response at step (a short "ucNN: CRD"/"ucNN: submit"-style
// label matching the row's existing wording, so "runner: conformant/"+step
// reads identically to the pre-mapping error text). It recognizes the
// unknown-member shape and substitutes the named sentence
// (ConformantMemberNotOnConnectedEHRSentence) ahead of the raw status/body;
// every other shape keeps its raw "status %d: %s" detail unchanged.
func conformantIngressErr(step string, status int, body []byte) error {
	if isConformantIngressUnknownMember(status, body) {
		return fmt.Errorf("runner: conformant/%s: %s (status %d: %s)", step, ConformantMemberNotOnConnectedEHRSentence, status, excerpt(body))
	}
	return fmt.Errorf("runner: conformant/%s status %d: %s", step, status, excerpt(body))
}

// Terminology systems mirrored from sdk/order.go's unexported constants
// (systemICD10) and the US Core profile BuildServiceRequest pins — needed
// here only because buildOrderServiceRequest (below) must vary the PROCEDURE
// coding system (CPT vs HCPCS), which shnsdk.BuildServiceRequest hardcodes
// to CPT. Values are byte-identical to their sdk originals (test/sdkparity
// pins the sdk side); duplicated, never diverged.
const (
	icd10System                 = "http://hl7.org/fhir/sid/icd-10-cm"
	usCoreServiceRequestProfile = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-servicerequest"
)

// buildOrderServiceRequest is shnsdk.BuildServiceRequest with an explicit
// procedure coding system — needed for the uc07 HCPCS-approve persona
// (L8000), which shnsdk.BuildServiceRequest cannot express (it hardcodes
// CPT). Same shape (US Core us-core-servicerequest profile, draft/order,
// ICD-10-CM reasonCode), so a real payer sees the same conformant order
// either way.
func buildOrderServiceRequest(system, code, display, dxCode, patientRef string) ([]byte, error) {
	sr := map[string]any{
		"resourceType": "ServiceRequest",
		"meta":         map[string]any{"profile": []string{usCoreServiceRequestProfile}},
		"status":       "draft",
		"intent":       "order",
		"code": map[string]any{"coding": []any{map[string]any{
			"system": system, "code": code, "display": display,
		}}},
		"reasonCode": []any{map[string]any{"coding": []any{map[string]any{
			"system": icd10System, "code": dxCode,
		}}}},
		"subject": map[string]any{"reference": patientRef},
	}
	b, err := json.Marshal(sr)
	if err != nil {
		return nil, fmt.Errorf("runner: build order ServiceRequest: %w", err)
	}
	return b, nil
}

// fillLumbarQR fills the sandbox lumbar-MRI DTR questionnaire for member
// under cc, authored at now. The QR is what SandboxAdjudicate reads (via the
// PAS ingress's parseConformantPASSubjects) to decide approve/pend/deny —
// the ANSWERS drive the outcome (FR-35), never the member id.
func fillLumbarQR(member string, cc shnsdk.ClinicalContext, now time.Time) ([]byte, error) {
	ref := "Patient/" + member
	qr, err := shnsdk.FillQuestionnaire(shnsdk.SandboxLumbarQuestionnaire(), cc, shnsdk.QRContext{
		PatientRef: ref, CoverageRef: "Coverage/" + member, OrderRef: "ServiceRequest/sr1", Authored: now,
	})
	if err != nil {
		return nil, fmt.Errorf("runner: fill DTR questionnaire: %w", err)
	}
	return qr, nil
}

// conformantSubmitBundle assembles a CONFORMANT PAS Claim Bundle (Patient +
// Coverage + the order ServiceRequest + Claim + the answered
// QuestionnaireResponse) — mirroring test/ingressconformance/pas_test.go's
// pasConformantBundle/pasConformantPendBundle shape, the SANDBOX-PAYER
// contract (empirically verified against a live harness run): unlike
// scenariodriver.BuildPASBundle(golden,...), which loads a two-RI golden
// carrying NO QuestionnaireResponse (fine for a real Da Vinci payer, but a
// 400 "conformant PAS bundle missing QuestionnaireResponse" — wrapped 502 by
// the Hub — against SHN's OWN sandbox ingress, which is what the Kit's local
// gateway child runs), this bundle always carries an answered QR so the
// sandbox adjudicator has something to read.
func conformantSubmitBundle(member string, srJSON, qrJSON []byte) ([]byte, error) {
	ref := "Patient/" + member
	entries := []map[string]any{
		{"resource": map[string]any{"resourceType": "Patient", "id": member}},
		{"resource": map[string]any{
			"resourceType": "Coverage", "id": "cov1", "status": "active",
			"beneficiary": map[string]any{"reference": ref},
			// The payor identifier (CMSPayerIdentity) is how the PAS ingress
			// routes the bundle to the payer holder (FR-G40; no default route).
			"payor": []any{map[string]any{"identifier": map[string]any{
				"system": shnsdk.CMSPayerIdentity.System, "value": shnsdk.CMSPayerIdentity.Value,
			}}},
		}},
		{"resource": json.RawMessage(srJSON)},
		{"resource": map[string]any{"resourceType": "Claim", "patient": map[string]any{"reference": ref}}},
		{"resource": json.RawMessage(qrJSON)},
	}
	b, err := json.Marshal(map[string]any{"resourceType": "Bundle", "type": "collection", "entry": entries})
	if err != nil {
		return nil, fmt.Errorf("runner: marshal conformant PAS submit bundle: %w", err)
	}
	return b, nil
}

// conformantAmendBundle builds a conformant amended re-POST (Claim.related[prior]
// + Provenance + optional DiagnosticReport, FR-32) via the sdk's own builder —
// the same one test/ingressconformance/pas_test.go's TestPASIngress_CorrThreading
// amend rows use — rather than scenariodriver.BuildAmendedRePOST, whose
// minimal empty-item QR (built for the two-RI real-br-payer ceiling, not the
// SHN sandbox) SandboxAdjudicate reads as weeks=0 → denied/"still
// insufficient" (empirically verified against a live harness run).
func conformantAmendBundle(member string, qrJSON, srJSON, drJSON, provJSON []byte, corr, originalCorr string, now time.Time) ([]byte, error) {
	b, err := shnsdk.BuildConformantClaimUpdateBundle(shnsdk.ConformantClaimUpdateInputs{
		QR: qrJSON, SR: srJSON,
		PatientRef:       "Patient/" + member,
		CoverageRef:      "Coverage/" + member,
		Provenance:       provJSON,
		DiagnosticReport: drJSON,
		Corr:             corr,
		OriginalCorr:     originalCorr,
		Created:          now,
		Payer:            shnsdk.CMSPayerIdentity,
	})
	if err != nil {
		return nil, fmt.Errorf("runner: build conformant amended re-POST: %w", err)
	}
	return b, nil
}

// bundleHasQuestionnaire reports whether a $questionnaire-package response
// Bundle carries a Questionnaire entry.
func bundleHasQuestionnaire(body []byte) bool {
	var pkg struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if json.Unmarshal(body, &pkg) != nil {
		return false
	}
	for _, e := range pkg.Entry {
		if e.Resource.ResourceType == "Questionnaire" {
			return true
		}
	}
	return false
}

// randCorr returns a fresh urn:shn:correlation-style id prefixed by prefix —
// unique per call (crypto/rand, not the injected clock) so a live gate can
// re-run without tripping the Hub's replay guard (mirrors
// test/tworilive's time-seeded correlation ids, but random beats
// clock-seeded when the injected clock is fixed across runs).
func randCorr(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// conformantUC01 is a gap-fill: eligibility is not a Da Vinci ingress
// operation (no CRD/DTR/PAS route exists for it), so the conformant lane
// drives the same provider-data /scenario/uc01 route the ehr lane does — the
// same posture two-RI takes for UC-01 (D-2RI's gap-fill note). The Detail is
// prefixed so this is never mistaken for a genuine ingress-driven row.
func conformantUC01(rn *Runner, branch string) (string, error) {
	detail, err := ehrUC01(rn, branch)
	if err != nil {
		return "", err
	}
	return "SHN-originated gap-fill (eligibility is not a Da Vinci ingress op): " + detail, nil
}

// brProviderOriginatedPrefix is the provenance line the CRD prong
// stamps on the row detail when the leg actually originated through
// br-provider's real BFF (the Java trio present) — never emitted on the
// direct-mint PostCRD path.
const brProviderOriginatedPrefix = "originated by the provider system (br-provider): "

// conformantBRPScenario is the EXPLICIT, deliberately-maintained table of
// which conformant-lane UCs are permitted to originate their CRD leg through
// br-provider's real BFF (scenariodriver.OriginateThroughBRProvider) when the
// Java trio is present, and which scenariodriver.PersonaOrders key each one
// drives. (uc03 was named only as an illustrative example while this table
// was being designed, never as a commitment to include it — see below for
// why it in fact has no entry.)
//
// The binding constraint is br-provider's own curated seed world, NOT
// scenariodriver plumbing: OriginateThroughBRProvider carries exactly the
// four PersonaOrders scenarios (noPA/approve/deny/pend, all HCPCS) because
// that is what br-provider's reference implementation actually ships (the
// standing lean-on-the-RIs rule — read the RI's real seed before
// hand-authoring personas into it, never the reverse). uc02→"noPA" is TODAY
// the ONLY 1:1 mapping onto a conformant row (conformant uc02 already drives
// the same HCPCS no-PA order the "noPA" persona carries); conformant uc03 is
// a CPT-72148 lumbar-MRI flow with no br-provider seed counterpart, so it
// deliberately has NO entry here.
//
// A future UC earns an entry here only after DELIBERATELY reading
// br-provider's actual seed data to confirm a genuine 1:1 mapping exists —
// never by a scenario key happening to string-match (or almost-match) a
// PersonaOrders key. Falling into or out of the BFF path by accident is
// exactly what this table exists to prevent.
var conformantBRPScenario = map[string]string{
	"uc02": "noPA",
}

func conformantUC02(rn *Runner, branch string) (string, error) {
	order := scenariodriver.PersonaOrders["noPA"] // E0250, Hospital Bed with Side Rails
	const member = "MBR-COVERED"

	// When the Java trio is present (Config.BFFURL set)
	// AND conformantBRPScenario names a BRP scenario for this UC, this CRD
	// leg originates through br-provider's real BFF instead of the driver's
	// own direct-mint PostCRD — the ONE row/scenario key pair
	// (noPA/MBR-COVERED) that maps 1:1 onto OriginateThroughBRProvider's
	// persona-order table, so the assertions below are unchanged either way.
	if scen, ok := conformantBRPScenario["uc02"]; rn.cfg.BFFURL != "" && ok {
		res, err := rn.cfg.Driver.OriginateThroughBRProvider(scen, member)
		if err != nil {
			return "", fmt.Errorf("runner: conformant/uc02: originate via br-provider BFF: %w", err)
		}
		if res.Status != 200 {
			return "", conformantIngressErr("uc02: CRD", res.Status, res.Body)
		}
		if res.Covered() != "covered" {
			return "", fmt.Errorf("runner: conformant/uc02: covered=%q, want %q", res.Covered(), "covered")
		}
		if res.PANeeded() != shnsdk.PANeededNoAuth {
			return "", fmt.Errorf("runner: conformant/uc02: paNeeded=%q, want %q (no-PA card)", res.PANeeded(), shnsdk.PANeededNoAuth)
		}
		return fmt.Sprintf("%s%s (HCPCS %s %s): covered=%s paNeeded=%s", brProviderOriginatedPrefix, res.Cards.Cards[0].Summary, order.Code, order.Display, res.Covered(), res.PANeeded()), nil
	}

	body, err := scenariodriver.BuildCRDRequest(member, scenariodriver.SystemHCPCS, order.Code, order.Display)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc02: build CRD request: %w", err)
	}
	res, err := rn.cfg.Driver.PostCRD(body)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc02: POST CRD: %w", err)
	}
	if res.Status != 200 {
		return "", conformantIngressErr("uc02: CRD", res.Status, res.Body)
	}
	cards, err := scenariodriver.ParseCards(res.Body)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc02: parse cards: %w", err)
	}
	if cards.Covered() != "covered" {
		return "", fmt.Errorf("runner: conformant/uc02: covered=%q, want %q", cards.Covered(), "covered")
	}
	// Empirically verified against a live harness run: an earlier draft of
	// this row expected PANeeded()=="" for the no-PA card; the sandbox actually emits the
	// explicit sentinel shnsdk.PANeededNoAuth ("no-auth"), never empty.
	if cards.PANeeded() != shnsdk.PANeededNoAuth {
		return "", fmt.Errorf("runner: conformant/uc02: paNeeded=%q, want %q (no-PA card)", cards.PANeeded(), shnsdk.PANeededNoAuth)
	}
	return fmt.Sprintf("%s (HCPCS %s %s): covered=%s paNeeded=%s", cards.Cards[0].Summary, order.Code, order.Display, cards.Covered(), cards.PANeeded()), nil
}

func conformantUC03(rn *Runner, branch string) (string, error) {
	const member = "MBR-COVERED"
	ref := "Patient/" + member

	crdBody, err := scenariodriver.BuildCRDRequest(member, shnsdk.SystemCPT, "72148", "MRI lumbar spine")
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: build CRD request: %w", err)
	}
	crdRes, err := rn.cfg.Driver.PostCRD(crdBody)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: POST CRD: %w", err)
	}
	if crdRes.Status != 200 {
		return "", conformantIngressErr("uc03: CRD", crdRes.Status, crdRes.Body)
	}
	cards, err := scenariodriver.ParseCards(crdRes.Body)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: parse cards: %w", err)
	}
	qs := cards.Questionnaires()
	if len(qs) == 0 {
		return "", fmt.Errorf("runner: conformant/uc03: card carries no questionnaire canonical")
	}
	canonical := qs[0]

	pkgRes, err := rn.cfg.Driver.PostQuestionnairePackage(canonical, member)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: DTR $questionnaire-package: %w", err)
	}
	if pkgRes.Status != 200 {
		return "", conformantIngressErr("uc03: DTR package", pkgRes.Status, pkgRes.Body)
	}
	if !bundleHasQuestionnaire(pkgRes.Body) {
		return "", fmt.Errorf("runner: conformant/uc03: DTR package response has no Questionnaire entry")
	}

	now := rn.now()
	srJSON, err := shnsdk.BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: build order ServiceRequest: %w", err)
	}
	qrJSON, err := fillLumbarQR(member, shnsdk.SandboxUC03Context(), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: %w", err)
	}
	bundle, err := conformantSubmitBundle(member, srJSON, qrJSON)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: %w", err)
	}
	out, err := rn.cfg.Driver.SubmitPAS(bundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: submit PAS: %w", err)
	}
	if out.Status != 200 {
		return "", conformantIngressErr("uc03: submit", out.Status, out.Body)
	}
	if !out.Approved || out.PreAuthRef == "" {
		return "", fmt.Errorf("runner: conformant/uc03: submit not approved: %s", excerpt(out.Body))
	}
	return fmt.Sprintf("CRD card + DTR package + PAS submit approved via direct-mint ingress, auth %s", out.PreAuthRef), nil
}

func conformantUC04(rn *Runner, branch string) (string, error) {
	const member = "MBR-COVERED"
	ref := "Patient/" + member
	now := rn.now()
	submitCorr := randCorr("kit-uc04-submit")
	amendCorr := randCorr("kit-uc04-amend")

	srJSON, err := shnsdk.BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: build order ServiceRequest: %w", err)
	}
	// SandboxUC04Context: priorSurgery=true, no operative DR in the submit → pends.
	qrJSON, err := fillLumbarQR(member, shnsdk.SandboxUC04Context(), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: %w", err)
	}
	submitBundle, err := conformantSubmitBundle(member, srJSON, qrJSON)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: %w", err)
	}
	submitBundle, err = scenariodriver.InjectShnCorrelation(submitBundle, submitCorr)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: inject correlation: %w", err)
	}
	submitOut, err := rn.cfg.Driver.SubmitPAS(submitBundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: submit PAS: %w", err)
	}
	if submitOut.Status != 200 {
		return "", conformantIngressErr("uc04: submit", submitOut.Status, submitOut.Body)
	}
	if !submitOut.Pended {
		return "", fmt.Errorf("runner: conformant/uc04: submit outcome not pended: %s", excerpt(submitOut.Body))
	}

	drJSON, err := shnsdk.BuildDiagnosticReport("dr-kit-uc04", ref, "72148", "Operative report — lumbar microdiscectomy")
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: build operative DiagnosticReport: %w", err)
	}
	provJSON, err := shnsdk.BuildProvenance("DiagnosticReport/dr-kit-uc04", "Organization/provider", now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: build Provenance: %w", err)
	}
	amendBundle, err := conformantAmendBundle(member, qrJSON, srJSON, drJSON, provJSON, amendCorr, submitCorr, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: %w", err)
	}
	amendOut, err := rn.cfg.Driver.SubmitPAS(amendBundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc04: submit amended re-POST: %w", err)
	}
	if amendOut.Status != 200 {
		return "", conformantIngressErr("uc04: amend", amendOut.Status, amendOut.Body)
	}
	if !amendOut.Approved || amendOut.PreAuthRef == "" {
		return "", fmt.Errorf("runner: conformant/uc04: amend not approved: %s", excerpt(amendOut.Body))
	}
	return fmt.Sprintf("pended (operative-diagnostic-report) then approved via amended re-POST, auth %s", amendOut.PreAuthRef), nil
}

func conformantUC05(rn *Runner, branch string) (string, error) {
	const member = "MBR-COVERED"
	ref := "Patient/" + member
	now := rn.now()
	submitCorr := randCorr("kit-uc05-submit")
	amendCorr := randCorr("kit-uc05-amend")

	srJSON, err := shnsdk.BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc05: build order ServiceRequest: %w", err)
	}
	qrJSON, err := fillLumbarQR(member, shnsdk.SandboxUC04Context(), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc05: %w", err)
	}
	submitBundle, err := conformantSubmitBundle(member, srJSON, qrJSON)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc05: %w", err)
	}
	submitBundle, err = scenariodriver.InjectShnCorrelation(submitBundle, submitCorr)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc05: inject correlation: %w", err)
	}
	submitOut, err := rn.cfg.Driver.SubmitPAS(submitBundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc05: submit PAS: %w", err)
	}
	if submitOut.Status != 200 {
		return "", conformantIngressErr("uc05: submit", submitOut.Status, submitOut.Body)
	}
	if !submitOut.Pended {
		return "", fmt.Errorf("runner: conformant/uc05: submit outcome not pended: %s", excerpt(submitOut.Body))
	}

	// The federated CDex-middle evidence UC-05 retrieves (CXL-D11: CDex
	// middle bracketed by SHN gateways, not real external CDex actors).
	drJSON, provJSON, err := scenariodriver.FacilityCDexEvidence(member, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc05: facility CDex evidence: %w", err)
	}
	amendBundle, err := conformantAmendBundle(member, qrJSON, srJSON, drJSON, provJSON, amendCorr, submitCorr, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc05: %w", err)
	}
	amendOut, err := rn.cfg.Driver.SubmitPAS(amendBundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc05: submit federated amended re-POST: %w", err)
	}
	if amendOut.Status != 200 {
		return "", conformantIngressErr("uc05: amend", amendOut.Status, amendOut.Body)
	}
	if !amendOut.Approved || amendOut.PreAuthRef == "" {
		return "", fmt.Errorf("runner: conformant/uc05: amend not approved: %s", excerpt(amendOut.Body))
	}
	return fmt.Sprintf("pended then approved via amended re-POST carrying CDex-federated facility evidence, auth %s", amendOut.PreAuthRef), nil
}

func conformantUC06(rn *Runner, branch string) (string, error) {
	const member = "MBR-UC06"
	ref := "Patient/" + member
	now := rn.now()
	submitCorr := randCorr("kit-uc06-submit")
	amendCorr := randCorr("kit-uc06-amend")

	pkgRes, err := rn.cfg.Driver.PostQuestionnairePackage(shnsdk.QuestionnaireCanonicalLumbarMRI, member)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: DTR $questionnaire-package: %w", err)
	}
	if pkgRes.Status != 200 {
		return "", conformantIngressErr("uc06: DTR package", pkgRes.Status, pkgRes.Body)
	}
	if !bundleHasQuestionnaire(pkgRes.Body) {
		return "", fmt.Errorf("runner: conformant/uc06: DTR package response has no Questionnaire entry")
	}

	srJSON, err := shnsdk.BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: build order ServiceRequest: %w", err)
	}
	qrJSON, err := fillLumbarQR(member, shnsdk.SandboxUC04Context(), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: %w", err)
	}
	submitBundle, err := conformantSubmitBundle(member, srJSON, qrJSON)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: %w", err)
	}
	submitBundle, err = scenariodriver.InjectShnCorrelation(submitBundle, submitCorr)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: inject correlation: %w", err)
	}
	submitOut, err := rn.cfg.Driver.SubmitPAS(submitBundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: submit PAS: %w", err)
	}
	if submitOut.Status != 200 {
		return "", conformantIngressErr("uc06: submit", submitOut.Status, submitOut.Body)
	}
	if !submitOut.Pended {
		return "", fmt.Errorf("runner: conformant/uc06: submit outcome not pended: %s", excerpt(submitOut.Body))
	}

	drJSON, err := shnsdk.BuildDiagnosticReport("dr-kit-uc06", ref, "72148", "Operative report — lumbar microdiscectomy")
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: build operative DiagnosticReport: %w", err)
	}
	provJSON, err := shnsdk.BuildProvenance("DiagnosticReport/dr-kit-uc06", "Organization/provider", now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: build Provenance: %w", err)
	}
	amendBundle, err := conformantAmendBundle(member, qrJSON, srJSON, drJSON, provJSON, amendCorr, submitCorr, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: %w", err)
	}
	amendOut, err := rn.cfg.Driver.SubmitPAS(amendBundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: submit amended re-POST: %w", err)
	}
	if amendOut.Status != 200 {
		return "", conformantIngressErr("uc06: amend", amendOut.Status, amendOut.Body)
	}
	if !amendOut.Approved || amendOut.PreAuthRef == "" {
		return "", fmt.Errorf("runner: conformant/uc06: amend not approved: %s", excerpt(amendOut.Body))
	}
	return fmt.Sprintf("DTR package fetched via ingress; pended then approved via amended re-POST, auth %s; manual DTR SPA deferred (D-2RI-1)", amendOut.PreAuthRef), nil
}

func conformantUC07(rn *Runner, branch string) (string, error) {
	const member = "MBR-UC07HCPCS"
	ref := "Patient/" + member
	now := rn.now()
	order := scenariodriver.PersonaOrders["approve"] // L8000, HCPCS approve persona

	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, order.Code, order.Display, "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: build order ServiceRequest: %w", err)
	}
	qrJSON, err := fillLumbarQR(member, shnsdk.SandboxUC03Context(), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: %w", err)
	}
	bundle, err := conformantSubmitBundle(member, srJSON, qrJSON)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: %w", err)
	}
	out, err := rn.cfg.Driver.SubmitPAS(bundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: submit PAS: %w", err)
	}
	if out.Status != 200 {
		return "", conformantIngressErr("uc07: submit", out.Status, out.Body)
	}
	if !out.Approved || out.PreAuthRef == "" {
		return "", fmt.Errorf("runner: conformant/uc07: submit not approved: %s", excerpt(out.Body))
	}
	detail := fmt.Sprintf("HCPCS %s (%s) approved via direct-mint PAS submit, auth %s", order.Code, order.Display, out.PreAuthRef)

	n, total, err := uc07PatientSurfaceReadBack(rn)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: %w", err)
	}
	if n == 0 {
		return "", fmt.Errorf("runner: conformant/uc07: 0 approved rows in patient-surface read-back (of %d)", total)
	}
	return detail + fmt.Sprintf("; hybrid patient-surface read-back (D-2RI-6 analog): %d/%d approved row(s)", n, total), nil
}

// conformantUC08 drives the deny branch: a QR-driven bundle whose answers
// (SandboxUC08Context — 4 weeks of conservative therapy, <6) SandboxAdjudicate
// denies purely off QR content. NOT a golden rebind (empirically verified):
// scenariodriver.PASApproveGolden() carries no QR at all,
// so rebinding it onto any member would still approve (it never reaches the
// weeks-based deny rule) — the deny outcome can only come from an
// SandboxUC08Context-filled QR, mirroring how
// test/ingressconformance/pas_test.go's pasConformantPendBundle builds its
// pend bundle.
func conformantUC08(rn *Runner, branch string) (string, error) {
	const member = "MBR-UC08"
	ref := "Patient/" + member
	now := rn.now()

	srJSON, err := shnsdk.BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc08: build order ServiceRequest: %w", err)
	}
	qrJSON, err := fillLumbarQR(member, shnsdk.SandboxUC08Context(), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc08: %w", err)
	}
	bundle, err := conformantSubmitBundle(member, srJSON, qrJSON)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc08: %w", err)
	}
	out, err := rn.cfg.Driver.SubmitPAS(bundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc08: submit PAS: %w", err)
	}
	if out.Status != 200 {
		return "", conformantIngressErr("uc08: submit", out.Status, out.Body)
	}
	if out.Approved {
		return "", fmt.Errorf("runner: conformant/uc08: submit approved, want denied: %s", excerpt(out.Body))
	}
	// A pended outcome is NOT a denial either — a regression that pends the
	// deny persona must read as a failed row, never as a passed deny.
	if out.Pended {
		return "", fmt.Errorf("runner: conformant/uc08: submit pended, want denied: %s", excerpt(out.Body))
	}
	return "denied via direct-mint PAS submit (conservative therapy <6 weeks)", nil
}
