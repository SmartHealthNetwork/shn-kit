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

// conformantRows is the "Da Vinci provider" lane's row table: each row drives the
// child's Da Vinci ingress directly (CRD order-select, DTR
// $questionnaire-package, PAS $submit — all UDAP B2B direct-bearer-authed) via
// scenariodriver.Driver, and every payer leg is routed by the static payer
// directory to the HOSTED DA VINCI REFERENCE PAYER. The verdict source is that
// reference payer, never this Kit: its behaviour is keyed on four HCPCS families,
// and the rows below are those families —
//
//	E0250 hospital bed          covered, no prior authorization      (uc02)
//	L8000 breast prosthesis     prior authorization, approved        (uc03, uc07)
//	E0424 stationary oxygen     conditional; held, then resolved     (uc04, uc05, uc06)
//	J3490 unclassified drug     not covered; formally denied         (uc08)
//
// Every approval is fenced on the reference payer's own AUTH-NNNN authorization
// (requireAuthRef): a run that quietly fell back to some other payer cannot pass.
//
// test/tworilive is the executable reference each row was copied from — it drives
// these same exchanges against the running reference payer, and it (not this file)
// is where a shape is proven before it is written here. This package never imports
// it: the Kit's publish boundary forbids importing the private repository, so the
// shapes are rebuilt on the sdk's own builders.
//
// When the Java trio is present the CRD leg of the rows named in
// conformantBRPScenario originates through br-provider's real BFF instead of the
// driver's own direct-mint request, and the questionnaires are filled by
// br-provider's real populate endpoint — same assertions either way.
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
// Patient on the partner's connected FHIR server — the remedy is loading the
// demo persona bundle (fhirseed.ConformantSeedBundle(), downloadable from
// GET /api/byo/seed-bundle/conformant, manual transaction-POST) onto it, or
// restoring demo data.
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

// homeOxygenCanonical is the reference payer's HomeOxygen questionnaire canonical.
// The E0424 order-sign card advertises NO questionnaire, so uc06 fetches the
// package BY CANONICAL — this value is the one the live gate READ OFF the running
// reference payer (test/tworilive, R1), never guessed. Duplicated as a literal
// because the Kit cannot import the private repository that owns it.
const homeOxygenCanonical = "http://example.org/fhir/Questionnaire/HomeOxygenDispatch"

// buildOrderServiceRequest is shnsdk.BuildServiceRequest with an explicit
// procedure coding system — needed because every reference-payer family is an
// HCPCS code, which shnsdk.BuildServiceRequest cannot express (it hardcodes
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

// conformantSubmitBundle assembles the PAS $submit Claim Bundle the hosted Da Vinci
// reference payer answers, in the TWO-STEP shape the live gate proved
// (test/tworilive/ingress_resolve_test.go — R1):
//
//  1. the sdk builder with PayerOrgEntry:true (the reference payer resolves the payor
//     off bundle ENTRIES only — a contained payer Organization yields no payor at all)
//     and AbsoluteRefs:true (it does not resolve relative refs against absolute entry
//     fullUrls), THEN
//  2. scenariodriver.AddRoutablePayor, because the Kit's own ingress routes
//     payload-FIRST off an INLINE Coverage.payor[0].identifier — and step 1's
//     AbsoluteRefs rewrote the payor REFERENCE into a form the ingress's relative-ref
//     resolver cannot match, so without the inline identifier the ingress rejects the
//     bundle ("no payer identifier on member coverage") before it ever reaches the
//     payer. The stamp is purely additive: the payor reference step 1 needs survives.
//
// qrJSON may be nil — this builder tolerates a submit with no questionnaire answers
// (the amended re-submit builder does not; see conformantAmendBundle).
func conformantSubmitBundle(member string, payer shnsdk.PayerIdentifier, srJSON, qrJSON []byte, corr string, now time.Time) ([]byte, error) {
	b, err := shnsdk.BuildConformantClaimBundle(shnsdk.ConformantClaimInputs{
		QR:            qrJSON,
		SR:            srJSON,
		PatientRef:    "Patient/" + member,
		CoverageRef:   "Coverage/" + member,
		Corr:          corr,
		Created:       now,
		PayerOrgEntry: true,
		AbsoluteRefs:  true,
		Payer:         payer,
		MemberID:      member,
	})
	if err != nil {
		return nil, fmt.Errorf("runner: build conformant PAS submit bundle: %w", err)
	}
	routable, err := scenariodriver.AddRoutablePayor(b)
	if err != nil {
		return nil, fmt.Errorf("runner: make the PAS submit bundle routable: %w", err)
	}
	return routable, nil
}

// conformantAmendBundle builds the amended re-POST that resolves a held request
// (Claim.related[prior] + Provenance + optional DiagnosticReport, FR-32) in the same
// two-step shape conformantSubmitBundle documents. qrJSON is REQUIRED here — the sdk's
// update builder rejects a nil QR — so a row with no populated answer set passes the
// minimal attested QuestionnaireResponse conformantQR mints; the reference payer's
// verdict is code-keyed and never reads the answers, so what the amendment is really
// proving is the attested evidence (the Provenance, and the report when there is one).
func conformantAmendBundle(member string, qrJSON, srJSON, drJSON, provJSON []byte, corr, originalCorr string, now time.Time) ([]byte, error) {
	b, err := shnsdk.BuildConformantClaimUpdateBundle(shnsdk.ConformantClaimUpdateInputs{
		QR:               qrJSON,
		SR:               srJSON,
		PatientRef:       "Patient/" + member,
		CoverageRef:      "Coverage/" + member,
		MemberID:         member,
		Provenance:       provJSON,
		DiagnosticReport: drJSON,
		Corr:             corr,
		OriginalCorr:     originalCorr,
		Created:          now,
		PayerOrgEntry:    true,
		AbsoluteRefs:     true,
		Payer:            shnsdk.CMSPayerIdentity,
	})
	if err != nil {
		return nil, fmt.Errorf("runner: build conformant amended re-POST: %w", err)
	}
	routable, err := scenariodriver.AddRoutablePayor(b)
	if err != nil {
		return nil, fmt.Errorf("runner: make the amended re-POST routable: %w", err)
	}
	return routable, nil
}

// packageHasQuestionnaire reports whether a $questionnaire-package response carries a
// Questionnaire — across BOTH shapes that reach the Kit on the wire:
//
//   - the hosted Da Vinci reference payer (br-payer) answers a Parameters profiled on
//     dtr-qpackage-output-parameters, whose parameter[name=="packagebundle"].resource is
//     the collection Bundle carrying the Questionnaire (plus an outcome parameter). The
//     gateway relays that response VERBATIM on the ingress, so the wrapper — not the
//     Bundle — is what this fence reads.
//   - the bridging demo payer and SHN's own provider-data/$populate path answer a BARE
//     collection Bundle.
//
// The unwrap is a deliberate SMALL LOCAL COPY of gateway/engine's
// unwrapQuestionnairePackage and scenariodriver's packageBundleResource: the Kit cannot
// take a gateway cut for this fix, and shnsdk.ExtractQuestionnaireFromPackage is
// bare-Bundle-only by design. FOLLOW-UP: dedupe onto one shared unwrap the next time a
// gateway/sdk cut is due.
//
// The fence stays strict — a Parameters with no packagebundle Bundle, a packagebundle
// Bundle carrying no Questionnaire, and an empty or malformed body are all false.
func packageHasQuestionnaire(body []byte) bool {
	var top struct {
		ResourceType string `json:"resourceType"`
		Parameter    []struct {
			Name     string          `json:"name"`
			Resource json.RawMessage `json:"resource"`
		} `json:"parameter"`
	}
	if json.Unmarshal(body, &top) != nil {
		return false
	}
	if top.ResourceType == "Parameters" {
		for _, param := range top.Parameter {
			if param.Name == "packagebundle" && len(param.Resource) > 0 {
				return bundleHasQuestionnaire(param.Resource)
			}
		}
		return false
	}
	return bundleHasQuestionnaire(body)
}

// bundleHasQuestionnaire reports whether a $questionnaire-package collection Bundle
// carries a Questionnaire entry. Only packageHasQuestionnaire calls it — a caller that
// reached it directly would be back to the bare-Bundle-only fence.
func bundleHasQuestionnaire(body []byte) bool {
	var pkg struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if json.Unmarshal(body, &pkg) != nil {
		return false
	}
	if pkg.ResourceType != "Bundle" {
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

// requireAuthRef is the anti-fallback fence: the hosted Da Vinci reference payer
// issues an AUTH-NNNN authorization, so a run that silently reached some other
// payer — whose references are shaped differently — can never pass as approved.
func requireAuthRef(uc string, out scenariodriver.PASOutcome) error {
	if !out.Approved || out.PreAuthRef == "" {
		return fmt.Errorf("runner: conformant/%s: not approved: %s", uc, excerpt(out.Body))
	}
	if !strings.HasPrefix(out.PreAuthRef, "AUTH-") {
		return fmt.Errorf("runner: conformant/%s: authorization %q is not the reference payer's AUTH-NNNN — this run did not reach the reference payer", uc, out.PreAuthRef)
	}
	return nil
}

// conformantQR produces the questionnaire answers a row carries: br-provider's REAL
// populate output when the Java trio is present and there is a fetched package to
// fill; otherwise a minimal completed QuestionnaireResponse (subject, questionnaire,
// one attested item). The reference payer's verdict never reads the answers (its
// families are code-keyed), so what matters is the shape: a completed QR the
// Provenance can attest.
func conformantQR(rn *Runner, pkg scenariodriver.DTRPackage, member string, now time.Time) ([]byte, error) {
	if rn.cfg.BFFURL != "" && len(pkg.Body) > 0 {
		qr, err := rn.cfg.Driver.PopulateViaBRProvider(pkg)
		if err != nil {
			return nil, fmt.Errorf("runner: fill the questionnaire via the provider system: %w", err)
		}
		return qr, nil
	}
	qr, err := json.Marshal(map[string]any{
		"resourceType": "QuestionnaireResponse", "status": "completed",
		"questionnaire": pkg.Canonical,
		"subject":       map[string]any{"reference": "Patient/" + member},
		"authored":      now.UTC().Format(time.RFC3339),
		"item": []any{map[string]any{"linkId": "1", "text": "Clinician attestation",
			"answer": []any{map[string]any{"valueString": "attested"}}}},
	})
	if err != nil {
		return nil, fmt.Errorf("runner: build the attested QuestionnaireResponse: %w", err)
	}
	return qr, nil
}

// conformantUC01 is the one row with no Da Vinci ingress leg: eligibility is not a
// Da Vinci ingress operation (no CRD/DTR/PAS route exists for it), so this row
// drives the MAIN child's /scenario/uc01 origination route (ehrScenarioMain) — the
// same posture the two-RI gate takes for UC-01. The Detail is prefixed so it is
// never mistaken for an ingress-driven row.
func conformantUC01(rn *Runner, branch string) (string, error) {
	var out struct {
		Covered bool   `json:"covered"`
		Reason  string `json:"reason"`
	}
	if err := ehrScenarioMain(rn, "/scenario/uc01", map[string]string{"branch": branch}, &out); err != nil {
		return "", err
	}
	want := branch == "covered"
	if out.Covered != want {
		return "", fmt.Errorf("runner: conformant/uc01(%s): covered=%v, want %v (reason=%q)", branch, out.Covered, want, out.Reason)
	}
	detail := fmt.Sprintf("covered=%v: %s", out.Covered, out.Reason)
	return "SHN-originated (eligibility is not a Da Vinci ingress operation): " + detail, nil
}

// brProviderOriginatedPrefix is the provenance line the CRD prong
// stamps on the row detail when the leg actually originated through
// br-provider's real BFF (the Java trio present) — never emitted on the
// direct-mint PostCRD path.
const brProviderOriginatedPrefix = "originated by the provider system (br-provider): "

// conformantBRPScenario names, per UC, the br-provider scenario
// (scenariodriver.PersonaOrders key) whose CRD leg originates through br-provider's
// real BFF when the Java trio is present.
//
// The binding constraint is br-provider's own curated seed world, NOT scenariodriver
// plumbing: OriginateThroughBRProvider carries exactly the four PersonaOrders
// scenarios (noPA/approve/deny/pend, all HCPCS) because that is what br-provider's
// reference implementation actually ships (the standing lean-on-the-RIs rule — read
// the RI's real seed before hand-authoring personas into it, never the reverse).
// Those four orders ARE the hosted reference payer's four families, so every entry
// below is a genuine 1:1 mapping, verified by reading both sides — never by a
// scenario key happening to string-match a row name. Falling into or out of the BFF
// path by accident is exactly what this table exists to prevent.
//
// uc05 and uc07 stay direct-mint deliberately: their CRD leg is not what the scenario
// is about (uc05 is the federated evidence amendment, uc07 the patient surface), and
// uc01 has no CRD leg at all.
var conformantBRPScenario = map[string]string{
	"uc02": "noPA",    // E0250 hospital bed — covered, no prior authorization
	"uc03": "approve", // L8000 — prior authorization, approved
	"uc04": "pend",    // E0424 home oxygen — conditional; the request is held, then resolved
	"uc06": "pend",    // E0424 — the held request is resolved by the attested re-submit
	"uc08": "deny",    // J3490 — not covered; the submitted request is formally denied
}

// conformantCRD is every row's coverage-check (CRD) prong: through br-provider's real
// BFF when the Java trio is present AND conformantBRPScenario names this scenario for
// this UC, else the driver's own direct-mint request. It returns the parsed cards and
// whether the BFF carried the leg. The direct-mint request BODY is deliberately NOT
// returned: the one row that reads a payer identity back off it (the bridging demo)
// builds its own body, so returning it here would only be a value every caller drops.
func conformantCRD(rn *Runner, uc, scenario, member string) (scenariodriver.Cards, bool, error) {
	order := scenariodriver.PersonaOrders[scenario]
	if scen, ok := conformantBRPScenario[uc]; rn.cfg.BFFURL != "" && ok && scen == scenario {
		res, err := rn.cfg.Driver.OriginateThroughBRProvider(scen, member)
		if err != nil {
			return scenariodriver.Cards{}, true, fmt.Errorf("runner: conformant/%s: originate via the provider system: %w", uc, err)
		}
		if res.Status != http.StatusOK {
			return scenariodriver.Cards{}, true, conformantIngressErr(uc+": CRD", res.Status, res.Body)
		}
		return res.Cards, true, nil
	}
	body, err := scenariodriver.BuildCRDRequest(member, scenariodriver.SystemHCPCS, order.Code, order.Display)
	if err != nil {
		return scenariodriver.Cards{}, false, fmt.Errorf("runner: conformant/%s: build CRD request: %w", uc, err)
	}
	res, err := rn.cfg.Driver.PostCRD(body)
	if err != nil {
		return scenariodriver.Cards{}, false, fmt.Errorf("runner: conformant/%s: POST CRD: %w", uc, err)
	}
	if res.Status != http.StatusOK {
		return scenariodriver.Cards{}, false, conformantIngressErr(uc+": CRD", res.Status, res.Body)
	}
	cards, err := scenariodriver.ParseCards(res.Body)
	if err != nil {
		return scenariodriver.Cards{}, false, fmt.Errorf("runner: conformant/%s: parse cards: %w", uc, err)
	}
	return cards, false, nil
}

// originated prefixes detail with the provider-system provenance line when the CRD
// leg actually came through br-provider's BFF.
func originated(viaBFF bool, detail string) string {
	if viaBFF {
		return brProviderOriginatedPrefix + detail
	}
	return detail
}

// conformantUC02 — E0250 hospital bed: the reference payer covers it and asks for no
// prior authorization, so the row ends at the coverage check.
func conformantUC02(rn *Runner, branch string) (string, error) {
	const member = "MBR-COVERED"
	order := scenariodriver.PersonaOrders["noPA"] // E0250, Hospital Bed with Side Rails

	cards, viaBFF, err := conformantCRD(rn, "uc02", "noPA", member)
	if err != nil {
		return "", err
	}
	if cards.Covered() != shnsdk.CoveredCovered {
		return "", fmt.Errorf("runner: conformant/uc02: covered=%q, want %q", cards.Covered(), shnsdk.CoveredCovered)
	}
	if cards.PANeeded() != shnsdk.PANeededNoAuth {
		return "", fmt.Errorf("runner: conformant/uc02: paNeeded=%q, want %q (the reference payer requires no prior authorization for this family)", cards.PANeeded(), shnsdk.PANeededNoAuth)
	}
	return originated(viaBFF, fmt.Sprintf("%s (HCPCS %s %s): covered=%s paNeeded=%s",
		cards.Cards[0].Summary, order.Code, order.Display, cards.Covered(), cards.PANeeded())), nil
}

// conformantPayorFromCRD reads the payer identity out of a driver-built CDS
// Hooks request's prefetch Coverage — the SAME bytes, parsed by the SAME
// shnsdk.ParsePayerIdentifier the gateway's payload-first ingress routes with
// (AI-G13). Single-sourcing it this way (rather than re-deriving the member ->
// payer mapping here) is what guarantees a row's PAS submit bundle names the
// payer its own CRD/DTR legs already reached.
func conformantPayorFromCRD(crdBody []byte) (shnsdk.PayerIdentifier, error) {
	var req struct {
		Prefetch struct {
			Coverage json.RawMessage `json:"coverage"`
		} `json:"prefetch"`
	}
	if err := json.Unmarshal(crdBody, &req); err != nil {
		return shnsdk.PayerIdentifier{}, fmt.Errorf("read payer identity off the CRD request: %w", err)
	}
	pid, ok := shnsdk.ParsePayerIdentifier(req.Prefetch.Coverage, nil)
	if !ok {
		return shnsdk.PayerIdentifier{}, fmt.Errorf("the CRD request's prefetch Coverage carries no resolvable payer identifier")
	}
	return pid, nil
}

// conformantUC03 — L8000: the reference payer covers it but requires prior
// authorization, advertises the questionnaire on the card, and approves the submitted
// request. Branch "bridge-demo" is a different scenario entirely (the bridging demo,
// below) and shares nothing but the row name.
func conformantUC03(rn *Runner, branch string) (string, error) {
	if branch == "bridge-demo" {
		return conformantUC03BridgeDemo(rn)
	}
	const member = "MBR-COVERED"
	ref := "Patient/" + member
	now := rn.now()
	order := scenariodriver.PersonaOrders["approve"] // L8000

	cards, viaBFF, err := conformantCRD(rn, "uc03", "approve", member)
	if err != nil {
		return "", err
	}
	// The reference payer's answer for this family is BOTH halves — covered, and
	// prior authorization required. A not-covered answer that still asked for prior
	// authorization would be a different scenario (uc08's), never this row's.
	if cards.Covered() != shnsdk.CoveredCovered {
		return "", fmt.Errorf("runner: conformant/uc03: covered=%q, want %q (the reference payer covers this family — prior authorization is what it asks for)", cards.Covered(), shnsdk.CoveredCovered)
	}
	if cards.PANeeded() != shnsdk.PANeededAuthNeeded {
		return "", fmt.Errorf("runner: conformant/uc03: paNeeded=%q, want %q (this family needs prior authorization)", cards.PANeeded(), shnsdk.PANeededAuthNeeded)
	}
	qs := cards.Questionnaires()
	if len(qs) == 0 {
		return "", fmt.Errorf("runner: conformant/uc03: the card carries no questionnaire canonical")
	}
	canonical := qs[0]

	pkgRes, err := rn.cfg.Driver.PostQuestionnairePackage(canonical, member)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: DTR $questionnaire-package: %w", err)
	}
	if pkgRes.Status != http.StatusOK {
		return "", conformantIngressErr("uc03: DTR package", pkgRes.Status, pkgRes.Body)
	}
	if !packageHasQuestionnaire(pkgRes.Body) {
		return "", fmt.Errorf("runner: conformant/uc03: DTR package response has no Questionnaire entry")
	}
	qrJSON, err := conformantQR(rn, scenariodriver.DTRPackage{
		Status: pkgRes.Status, Body: pkgRes.Body, Canonical: canonical, Member: member,
	}, member, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: %w", err)
	}

	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, order.Code, order.Display, "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: build order ServiceRequest: %w", err)
	}
	bundle, err := conformantSubmitBundle(member, shnsdk.CMSPayerIdentity, srJSON, qrJSON, randCorr("kit-uc03"), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: %w", err)
	}
	out, err := rn.cfg.Driver.SubmitPAS(bundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: submit PAS: %w", err)
	}
	if out.Status != http.StatusOK {
		return "", conformantIngressErr("uc03: submit", out.Status, out.Body)
	}
	if err := requireAuthRef("uc03", out); err != nil {
		return "", err
	}
	return originated(viaBFF, fmt.Sprintf("CRD card + DTR package + PAS submit approved by the reference payer, auth %s", out.PreAuthRef)), nil
}

// conformantUC03BridgeDemo is the BRIDGING DEMO branch — a different exhibit from the
// row above: the MBR-BRIDGE-DEMO persona's own Coverage names the demo payer holder
// (gateway/engine/holderdata.go's MBR-BRIDGE-DEMO), so this run is routed there and
// answered by the demo payer, not by the hosted Da Vinci reference payer. Its legs are
// kept byte-for-byte as they were (the lumbar order, the demo payer's own questionnaire
// and verdict shape) — this is the bridging exhibit the live bridging gate observes,
// and changing its bytes would change what that gate sees.
func conformantUC03BridgeDemo(rn *Runner) (string, error) {
	const member = "MBR-BRIDGE-DEMO"
	ref := "Patient/" + member

	crdBody, err := scenariodriver.BuildCRDRequest(member, shnsdk.SystemCPT, "72148", "MRI lumbar spine")
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: build CRD request: %w", err)
	}
	crdRes, err := rn.cfg.Driver.PostCRD(crdBody)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: POST CRD: %w", err)
	}
	if crdRes.Status != http.StatusOK {
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
	if pkgRes.Status != http.StatusOK {
		return "", conformantIngressErr("uc03: DTR package", pkgRes.Status, pkgRes.Body)
	}
	if !packageHasQuestionnaire(pkgRes.Body) {
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
	// The submit bundle names the SAME payer the CRD/DTR legs of this run just
	// routed to — read back off the driver's own CRD request rather than
	// hardcoded, so this member, whose Coverage names the demo payer, cannot end
	// up with its PAS leg routed to the reference payer's holder instead.
	payer, err := conformantPayorFromCRD(crdBody)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: %w", err)
	}
	bundle, err := bridgeDemoSubmitBundle(member, payer, srJSON, qrJSON)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: %w", err)
	}
	out, err := rn.cfg.Driver.SubmitPAS(bundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: submit PAS: %w", err)
	}
	if out.Status != http.StatusOK {
		return "", conformantIngressErr("uc03: submit", out.Status, out.Body)
	}
	if !out.Approved || out.PreAuthRef == "" {
		return "", fmt.Errorf("runner: conformant/uc03: submit not approved: %s", excerpt(out.Body))
	}
	return fmt.Sprintf("CRD card + DTR package + PAS submit approved by the bridging demo payer, auth %s", out.PreAuthRef), nil
}

// fillLumbarQR fills the lumbar-MRI DTR questionnaire for member under cc, authored at
// now. Used ONLY by the bridging demo branch above, whose demo payer reads the ANSWERS
// to decide (FR-35) — every reference-payer row is code-keyed instead.
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

// bridgeDemoSubmitBundle is the bridging demo's PAS $submit bundle — the hand-assembled
// shape that exhibit has always put on the wire (Patient + Coverage + order + Claim +
// the answered QuestionnaireResponse), kept byte-for-byte. It deliberately does NOT go
// through conformantSubmitBundle: that builder stamps the reference payer's routable
// identifier, which would misroute this run away from the demo payer holder the
// exhibit is about.
func bridgeDemoSubmitBundle(member string, payer shnsdk.PayerIdentifier, srJSON, qrJSON []byte) ([]byte, error) {
	ref := "Patient/" + member
	entries := []map[string]any{
		{"resource": map[string]any{"resourceType": "Patient", "id": member}},
		{"resource": map[string]any{
			"resourceType": "Coverage", "id": "cov1", "status": "active",
			"beneficiary": map[string]any{"reference": ref},
			// The payor identifier is how the PAS ingress routes the bundle to the
			// payer holder (FR-G40; no default route) — so it must name the SAME payer
			// this run's earlier CRD/DTR legs routed to, or one run gets split across
			// two payer holders. This row passes what it read back off its OWN CRD
			// request (conformantPayorFromCRD), which is member-derived.
			"payor": []any{map[string]any{"identifier": map[string]any{
				"system": payer.System, "value": payer.Value,
			}}},
		}},
		{"resource": json.RawMessage(srJSON)},
		{"resource": map[string]any{"resourceType": "Claim", "patient": map[string]any{"reference": ref}}},
		{"resource": json.RawMessage(qrJSON)},
	}
	b, err := json.Marshal(map[string]any{"resourceType": "Bundle", "type": "collection", "entry": entries})
	if err != nil {
		return nil, fmt.Errorf("runner: marshal bridging demo PAS submit bundle: %w", err)
	}
	return b, nil
}

// conformantHeldThenResolved is the shared body of the two rows that submit an E0424
// request the reference payer HOLDS, then resolve it with an amended re-submit: uc04
// carries the operative report the clinician wrote, uc05 the facility evidence a
// federated query retrieved. The evidence is the only difference, so it is the only
// parameter (dr/prov), alongside the scenario key the CRD prong routes on.
func conformantHeldThenResolved(rn *Runner, uc, member string, evidence func(ref string, now time.Time) (drJSON, provJSON []byte, err error)) (viaBFF bool, authRef string, err error) {
	ref := "Patient/" + member
	now := rn.now()
	submitCorr := randCorr("kit-" + uc + "-submit")
	amendCorr := randCorr("kit-" + uc + "-amend")
	order := scenariodriver.PersonaOrders["pend"] // E0424, Stationary Oxygen System

	cards, viaBFF, err := conformantCRD(rn, uc, "pend", member)
	if err != nil {
		return viaBFF, "", err
	}
	// The reference payer answers this family CONDITIONAL at the coverage check and
	// advertises no questionnaire — the request is what it decides on, and it holds it.
	if cards.Covered() != shnsdk.CoveredConditional {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: covered=%q, want %q", uc, cards.Covered(), shnsdk.CoveredConditional)
	}

	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, order.Code, order.Display, "J44.9", ref)
	if err != nil {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: build order ServiceRequest: %w", uc, err)
	}
	submitBundle, err := conformantSubmitBundle(member, shnsdk.CMSPayerIdentity, srJSON, nil, submitCorr, now)
	if err != nil {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: %w", uc, err)
	}
	submitOut, err := rn.cfg.Driver.SubmitPAS(submitBundle)
	if err != nil {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: submit PAS: %w", uc, err)
	}
	if submitOut.Status != http.StatusOK {
		return viaBFF, "", conformantIngressErr(uc+": submit", submitOut.Status, submitOut.Body)
	}
	if !submitOut.Pended {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: submit outcome not pended: %s", uc, excerpt(submitOut.Body))
	}

	drJSON, provJSON, err := evidence(ref, now)
	if err != nil {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: %w", uc, err)
	}
	// The amended re-submit's questionnaire is the minimal attested answer set: this
	// family advertises no questionnaire, and the sdk's update builder requires one.
	qrJSON, err := conformantQR(rn, scenariodriver.DTRPackage{Canonical: homeOxygenCanonical, Member: member}, member, now)
	if err != nil {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: %w", uc, err)
	}
	amendBundle, err := conformantAmendBundle(member, qrJSON, srJSON, drJSON, provJSON, amendCorr, submitCorr, now)
	if err != nil {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: %w", uc, err)
	}
	amendOut, err := rn.cfg.Driver.SubmitPAS(amendBundle)
	if err != nil {
		return viaBFF, "", fmt.Errorf("runner: conformant/%s: submit amended re-POST: %w", uc, err)
	}
	if amendOut.Status != http.StatusOK {
		return viaBFF, "", conformantIngressErr(uc+": amend", amendOut.Status, amendOut.Body)
	}
	if err := requireAuthRef(uc, amendOut); err != nil {
		return viaBFF, "", err
	}
	return viaBFF, amendOut.PreAuthRef, nil
}

// conformantUC04 — E0424: the reference payer holds the request, and the amended
// re-submit carrying the operative report resolves it.
func conformantUC04(rn *Runner, branch string) (string, error) {
	viaBFF, authRef, err := conformantHeldThenResolved(rn, "uc04", "MBR-COVERED", func(ref string, now time.Time) ([]byte, []byte, error) {
		drJSON, err := shnsdk.BuildDiagnosticReport("dr-kit-uc04", ref, "E0424", "Operative report — home oxygen assessment")
		if err != nil {
			return nil, nil, fmt.Errorf("build operative DiagnosticReport: %w", err)
		}
		provJSON, err := shnsdk.BuildProvenance("DiagnosticReport/dr-kit-uc04", "Organization/provider", now)
		if err != nil {
			return nil, nil, fmt.Errorf("build Provenance: %w", err)
		}
		return drJSON, provJSON, nil
	})
	if err != nil {
		return "", err
	}
	return originated(viaBFF, fmt.Sprintf("held by the reference payer, then resolved on the amended re-submit carrying the operative report, auth %s", authRef)), nil
}

// conformantUC05 — the same held E0424 request, resolved by evidence a FEDERATED query
// retrieved from the facility (CXL-D11: the CDex middle bracketed by SHN gateways,
// not real external CDex actors).
func conformantUC05(rn *Runner, branch string) (string, error) {
	const member = "MBR-COVERED"
	viaBFF, authRef, err := conformantHeldThenResolved(rn, "uc05", member, func(ref string, now time.Time) ([]byte, []byte, error) {
		drJSON, provJSON, err := scenariodriver.FacilityCDexEvidence(member, now)
		if err != nil {
			return nil, nil, fmt.Errorf("facility CDex evidence: %w", err)
		}
		return drJSON, provJSON, nil
	})
	if err != nil {
		return "", err
	}
	return originated(viaBFF, fmt.Sprintf("held by the reference payer, then resolved on the amended re-submit carrying the federated facility evidence, auth %s", authRef)), nil
}

// conformantUC06 — the questionnaire row: the E0424 coverage check answers CONDITIONAL
// and advertises no questionnaire, so the reference payer's HomeOxygen package is
// fetched BY CANONICAL, filled (by br-provider's real populate under the Java trio,
// by a minimal attestation without it), submitted — held — and then resolved by the
// clinician-attested re-submit whose Provenance attests those very answers.
func conformantUC06(rn *Runner, branch string) (string, error) {
	const member = "MBR-UC06"
	ref := "Patient/" + member
	now := rn.now()
	submitCorr := randCorr("kit-uc06-submit")
	amendCorr := randCorr("kit-uc06-amend")
	order := scenariodriver.PersonaOrders["pend"] // E0424

	cards, viaBFF, err := conformantCRD(rn, "uc06", "pend", member)
	if err != nil {
		return "", err
	}
	if cards.Covered() != shnsdk.CoveredConditional {
		return "", fmt.Errorf("runner: conformant/uc06: covered=%q, want %q", cards.Covered(), shnsdk.CoveredConditional)
	}

	pkgRes, err := rn.cfg.Driver.PostQuestionnairePackage(homeOxygenCanonical, member)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: DTR $questionnaire-package: %w", err)
	}
	if pkgRes.Status != http.StatusOK {
		return "", conformantIngressErr("uc06: DTR package", pkgRes.Status, pkgRes.Body)
	}
	if !packageHasQuestionnaire(pkgRes.Body) {
		return "", fmt.Errorf("runner: conformant/uc06: DTR package response has no Questionnaire entry")
	}
	qrJSON, err := conformantQR(rn, scenariodriver.DTRPackage{
		Status: pkgRes.Status, Body: pkgRes.Body, Canonical: homeOxygenCanonical, Member: member,
	}, member, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: %w", err)
	}

	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, order.Code, order.Display, "J44.9", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: build order ServiceRequest: %w", err)
	}
	submitBundle, err := conformantSubmitBundle(member, shnsdk.CMSPayerIdentity, srJSON, qrJSON, submitCorr, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: %w", err)
	}
	submitOut, err := rn.cfg.Driver.SubmitPAS(submitBundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: submit PAS: %w", err)
	}
	if submitOut.Status != http.StatusOK {
		return "", conformantIngressErr("uc06: submit", submitOut.Status, submitOut.Body)
	}
	if !submitOut.Pended {
		return "", fmt.Errorf("runner: conformant/uc06: submit outcome not pended: %s", excerpt(submitOut.Body))
	}

	// The Provenance attests the QuestionnaireResponse itself (no report on this row);
	// the sdk rewrites the target onto the QR id the update bundle carries.
	provJSON, err := shnsdk.BuildProvenance("QuestionnaireResponse/attested", "Organization/provider", now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: build Provenance: %w", err)
	}
	amendBundle, err := conformantAmendBundle(member, qrJSON, srJSON, nil, provJSON, amendCorr, submitCorr, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: %w", err)
	}
	amendOut, err := rn.cfg.Driver.SubmitPAS(amendBundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc06: submit amended re-POST: %w", err)
	}
	if amendOut.Status != http.StatusOK {
		return "", conformantIngressErr("uc06: amend", amendOut.Status, amendOut.Body)
	}
	if err := requireAuthRef("uc06", amendOut); err != nil {
		return "", err
	}
	return originated(viaBFF, fmt.Sprintf("questionnaire fetched and filled by the provider system; held, then resolved on the clinician-attested re-submit, auth %s", amendOut.PreAuthRef)), nil
}

// conformantUC07 — the patient surface: an L8000 request the reference payer approves,
// then read back the way the patient sees it. The coverage check is not what this row
// is about, so it submits directly.
func conformantUC07(rn *Runner, branch string) (string, error) {
	const member = "MBR-UC07HCPCS"
	ref := "Patient/" + member
	now := rn.now()
	order := scenariodriver.PersonaOrders["approve"] // L8000

	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, order.Code, order.Display, "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: build order ServiceRequest: %w", err)
	}
	qrJSON, err := conformantQR(rn, scenariodriver.DTRPackage{Canonical: shnsdk.QuestionnaireCanonicalLumbarMRI, Member: member}, member, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: %w", err)
	}
	bundle, err := conformantSubmitBundle(member, shnsdk.CMSPayerIdentity, srJSON, qrJSON, randCorr("kit-uc07"), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: %w", err)
	}
	out, err := rn.cfg.Driver.SubmitPAS(bundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: submit PAS: %w", err)
	}
	if out.Status != http.StatusOK {
		return "", conformantIngressErr("uc07: submit", out.Status, out.Body)
	}
	if err := requireAuthRef("uc07", out); err != nil {
		return "", err
	}
	detail := fmt.Sprintf("HCPCS %s (%s) approved by the reference payer, auth %s", order.Code, order.Display, out.PreAuthRef)

	// Skip the patient-surface read-back gracefully when it is not externally reachable
	// (hosted topology — the reads are internal/patient-only); the PA already succeeded.
	// Reachability gate, not a removal (see runner.Config.PatientSurfaceReadable).
	if !rn.cfg.PatientSurfaceReadable {
		return detail + "; patient-surface read-back skipped (hosted patient reads are internal/patient-only)", nil
	}
	n, total, err := uc07PatientSurfaceReadBack(rn)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: %w", err)
	}
	if n == 0 {
		return "", fmt.Errorf("runner: conformant/uc07: 0 approved rows in patient-surface read-back (of %d)", total)
	}
	// No internal decision id in the rendered sentence — this Detail is
	// participant-facing copy. (It is the two-RI gate's own hybrid
	// patient-surface read-back, run here against the Kit's stack.)
	return detail + fmt.Sprintf("; hybrid patient-surface read-back: %d/%d approved row(s)", n, total), nil
}

// conformantUC08 — J3490: the reference payer does not cover this family, so the
// coverage check says not-covered and the submitted request comes back formally
// DENIED, with the payer's own rationale. A denial is a decision: the row asserts the
// denial AND that a reason came with it — an approval or a hold here is a failed row,
// never a passed deny.
func conformantUC08(rn *Runner, branch string) (string, error) {
	const member = "MBR-UC08"
	ref := "Patient/" + member
	now := rn.now()
	order := scenariodriver.PersonaOrders["deny"] // J3490, Unclassified drugs

	cards, viaBFF, err := conformantCRD(rn, "uc08", "deny", member)
	if err != nil {
		return "", err
	}
	if cards.Covered() != shnsdk.CoveredNotCovered {
		return "", fmt.Errorf("runner: conformant/uc08: covered=%q, want %q", cards.Covered(), shnsdk.CoveredNotCovered)
	}

	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, order.Code, order.Display, "D57.1", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc08: build order ServiceRequest: %w", err)
	}
	bundle, err := conformantSubmitBundle(member, shnsdk.CMSPayerIdentity, srJSON, nil, randCorr("kit-uc08"), now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc08: %w", err)
	}
	out, err := rn.cfg.Driver.SubmitPAS(bundle)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc08: submit PAS: %w", err)
	}
	if out.Status != http.StatusOK {
		return "", conformantIngressErr("uc08: submit", out.Status, out.Body)
	}
	if out.Approved {
		return "", fmt.Errorf("runner: conformant/uc08: submit approved, want denied: %s", excerpt(out.Body))
	}
	// A held outcome is NOT a denial either — a regression that holds this family
	// must read as a failed row, never as a passed deny.
	if out.Pended {
		return "", fmt.Errorf("runner: conformant/uc08: submit pended, want denied: %s", excerpt(out.Body))
	}
	pr, err := shnsdk.ParseClaimResponse(out.Body)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc08: parse the payer's response: %w (%s)", err, excerpt(out.Body))
	}
	if pr.Outcome != "denied" {
		return "", fmt.Errorf("runner: conformant/uc08: outcome=%q, want denied: %s", pr.Outcome, excerpt(out.Body))
	}
	if pr.Denial == nil || pr.Denial.Rationale == "" {
		return "", fmt.Errorf("runner: conformant/uc08: denied without the payer's rationale: %s", excerpt(out.Body))
	}
	return originated(viaBFF, fmt.Sprintf("not covered at the coverage check; the submitted request was formally denied: %s", pr.Denial.Rationale)), nil
}
