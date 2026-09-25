package runner

import (
	"bytes"
	"context"
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
//	E0424 stationary oxygen     conditional; held, and held again    (uc04, uc05, uc06)
//	J3490 unclassified drug     not covered; formally denied         (uc08)
//
// Every approval is fenced on the reference payer's own AUTH-NNNN authorization
// (requireAuthRef): a run that quietly fell back to some other payer cannot pass.
//
// uc03's "bridge-demo" branch is the one row the directory routes ELSEWHERE — to a peer
// that declares newer contract lines, so its legs cross a version boundary. That peer
// forwards to the same reference payer, so the row drives the same L8000 family and is
// fenced on the same AUTH-NNNN: the exhibit is the boundary, not the verdict.
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
// conformant-lane row's failure Detail names when the row's hardcoded seeded
// member (MBR-COVERED, MBR-NOTCOVERED, MBR-UC06, MBR-UC07HCPCS, MBR-UC08,
// MBR-BRIDGE-DEMO) is not a Patient on the partner's connected FHIR server
// under an applied EHR swap. The row learns it from the partner's own server
// before sending anything (requireConformantMember, Config.MemberOnConnectedEHR):
// the Kit's gateway carries a member its system of record does not hold, so
// the exchange itself would not say so. The remedy is loading the demo
// persona bundle (fhirseed.ConformantSeedBundle(), downloadable from
// GET /api/byo/seed-bundle/conformant) onto that server, or restoring demo
// data.
//
// The conformant lane's sentence differs from the ehr/free-form lane's
// (rows_ehr.go's freeformProviderUnknownMemberSentence, "check the id or
// refresh the patient list") because the two lanes' members come from
// DIFFERENT places: a free-form member is caller-typed, while every
// conformant-lane member is a hardcoded seeded persona the row itself chose.
//
// EXPORTED so the live kit gate's both-states rows (test/kitlive/byo_test.go)
// assert against the constant itself rather than retyping the sentence.
const ConformantMemberNotOnConnectedEHRSentence = "this member isn't on your connected EHR — load the demo personas or restore demo data"

// memberCheckTimeout bounds the connected-EHR member check. It is one small
// search; a server that cannot answer in this time is treated as "could not
// check", and the row runs as usual.
const memberCheckTimeout = 10 * time.Second

// requireConformantMember checks, before a conformant row sends anything,
// that the connected EHR carries the row's seeded member. From shn-gateway
// v0.53.0 the gateway carries a member its system of record does not hold
// instead of refusing it, so the ingress no longer answers "unknown member"
// for a partner server missing the demo personas; the Kit asks the partner's
// own server itself (Config.MemberOnConnectedEHR, set only under an applied
// EHR swap) so the row still names the remedy rather than passing on data the
// partner's system does not have. A check that cannot run leaves the row to
// run as usual and records why (Runner.memberCheckNote) — shown, never
// assumed.
func (rn *Runner) requireConformantMember(uc, member string) error {
	check := rn.cfg.MemberOnConnectedEHR
	if check == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(rn.baseCtx, memberCheckTimeout)
	defer cancel()
	held, err := check(ctx, member)
	if err != nil {
		rn.memberCheckNote = "could not check your connected EHR for " + member + ": " + err.Error()
		return nil
	}
	if !held {
		return fmt.Errorf("runner: conformant/%s: %s (%s is not on the connected EHR)", uc, ConformantMemberNotOnConnectedEHRSentence, member)
	}
	return nil
}

// conformantIngressErr builds a conformant row's failure error for a
// non-200 ingress response at step (a short "ucNN: CRD"/"ucNN: submit"-style
// label), keeping the ingress's own status and body. A seeded member missing
// from the connected EHR is caught before anything is sent
// (requireConformantMember); the Kit's gateway carries a member its system of
// record does not hold, so the ingress no longer answers "unknown member" for
// one, and this function relabels nothing.
func conformantIngressErr(step string, status int, body []byte) error {
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

// l8000Canonical is the reference payer's questionnaire canonical for the L8000
// (approve) family — the value the live gate READ OFF the running reference payer
// (test/tworilive's TestTwoRI_Pin_L8000Canonical, captured to
// testdata/Questionnaire-L8000.json), never guessed. Duplicated as a literal for the
// same reason as homeOxygenCanonical above.
//
// It replaced shnsdk.QuestionnaireCanonicalLumbarMRI on the L8000 rows: that constant is
// SHN's OWN demo questionnaire, and stamping it on a QuestionnaireResponse bound for the
// reference payer named a questionnaire that payer never advertised.
const l8000Canonical = "http://example.org/fhir/Questionnaire/PriorAuthRequired"

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

// conformantRequestingProvider is the participant record the Kit's own rows name
// as the requesting provider — the party a prior-authorization request comes
// from. A payer matches a later inquiry on the member id PLUS the ordering or
// rendering provider identifier, so the request carries this record as a
// resolvable entry that Claim.provider references; without it the payer stores
// an authorization no conformant inquiry can find again.
func conformantRequestingProvider() []byte {
	return []byte(`{"resourceType":"Organization","id":"kit-requesting-provider",` +
		`"identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"1417947384"}],` +
		`"name":"Kit Reference Provider"}`)
}

// conformantMemberCoverage is the participant record the Kit's own rows name as
// the coverage a prior-authorization request is made under. The payer locates the
// policy from the Coverage the request names and matches a later inquiry against
// the coverage it stored, so the request carries a record with its own id and its
// own member identifier — one the Kit's inquiry can name again — rather than one
// the builder minted for every member alike.
//
// Its payor names conformantPayerOrganization's record, the SAME payer record
// every bundle this file builds carries as an entry. That is not a detail: the
// payer resolves a message's whole reference graph and refuses one that names a
// resource the message does not carry — HTTP 422 "PAS response graph: unresolved
// source reference". The SUBMIT builder repoints the payor onto the payer
// Organization entry itself (sdk's repointPayorToEntry), so a payor naming
// anything else was invisible there; the INQUIRY builder sends the requester's
// records as they stand and rewrites nothing, so the same record named a payer
// organization no inquiry bundle carried, and the real payer refused every
// inquiry the rows built from it. One record, named the same way by the
// submission and by the inquiry about it.
func conformantMemberCoverage(member string) []byte {
	return []byte(`{"resourceType":"Coverage","id":"kit-cov-` + strings.ToLower(member) + `","status":"active",` +
		`"identifier":[{"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},` +
		`"system":"urn:shn:coverage","value":"` + member + `"}],` +
		`"beneficiary":{"reference":"Patient/` + member + `"},` +
		`"relationship":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/subscriber-relationship","code":"self"}]},` +
		`"payor":[{"reference":"Organization/` + conformantPayerOrganizationID + `"}]}`)
}

// conformantPayerOrganizationID is the id of the payer record above — stated once
// so the Coverage's payor and the Organization entry can never drift apart.
const conformantPayerOrganizationID = "org-cms-payer"

// conformantPayerOrganization is the participant record the Kit's own rows name
// as the payer a request is made under.
//
// The payer scopes an inquiry's search by the insurer and never re-homes an
// Organization carrying a plan identifier rather than an NPI, so whatever a
// submission names is what an inquiry has to name. The Kit therefore carries its
// own record for the payer -- the one its member's Coverage names as payor --
// rather than one the builder mints, for the same reason it carries its own
// requesting provider and its own coverage.
func conformantPayerOrganization(payer shnsdk.PayerIdentifier) []byte {
	return []byte(`{"resourceType":"Organization","id":"` + conformantPayerOrganizationID + `",` +
		`"identifier":[{"system":"` + payer.System + `","value":"` + payer.Value + `"}],` +
		`"name":"Kit Reference Payer"}`)
}

// conformantSubmitBundle assembles the PAS $submit Claim Bundle the hosted Da Vinci
// reference payer answers, in the TWO-STEP shape the live gate proved
// (test/tworilive/ingress_resolve_test.go — R1):
//
//  1. the sdk builder with PayerOrgEntry:true (the reference payer resolves the payor
//     off bundle ENTRIES only — a contained payer Organization yields no payor at all)
//     and AbsoluteRefs:true (it does not resolve relative refs against absolute entry
//     fullUrls), THEN
//  2. scenariodriver.AddRoutablePayorFor(b, payer), because the Kit's own ingress routes
//     payload-FIRST off an INLINE Coverage.payor[0].identifier — and step 1's
//     AbsoluteRefs rewrote the payor REFERENCE into a form the ingress's relative-ref
//     resolver cannot match, so without the inline identifier the ingress rejects the
//     bundle ("no payer identifier on member coverage") before it ever reaches the
//     payer. The stamp is purely additive: the payor reference step 1 needs survives.
//     It is parameterized by payer (not hardcoded to the reference payer's CMS identity)
//     so this SAME builder can also serve a row whose member's Coverage names a
//     different payer holder — see conformantUC03BridgeDemo.
//
// qrJSON may be nil — this builder tolerates a submit with no questionnaire answers
// (the amended re-submit builder does not; see conformantAmendBundle).
func conformantSubmitBundle(member string, payer shnsdk.PayerIdentifier, srJSON, qrJSON []byte, corr string, now time.Time) ([]byte, error) {
	b, err := shnsdk.BuildConformantClaimBundle(shnsdk.ConformantClaimInputs{
		QR:             qrJSON,
		Provider:       conformantRequestingProvider(),
		Coverage:       conformantMemberCoverage(member),
		Insurer:        conformantPayerOrganization(payer),
		SR:             srJSON,
		PatientRef:     "Patient/" + member,
		CoverageRef:    "Coverage/" + member,
		Corr:           corr,
		Created:        now,
		PayerOrgEntry:  true,
		AbsoluteRefs:   true,
		Payer:          payer,
		MemberID:       member,
		MemberIDSystem: shnsdk.MemberSystem,
	})
	if err != nil {
		return nil, fmt.Errorf("runner: build conformant PAS submit bundle: %w", err)
	}
	routable, err := scenariodriver.AddRoutablePayorFor(b, payer)
	if err != nil {
		return nil, fmt.Errorf("runner: make the PAS submit bundle routable: %w", err)
	}
	return routable, nil
}

// conformantAmendBundle builds the amended re-POST that carries new evidence about a
// held request to the payer — never the thing that resolves it; the payer decides
// (Claim.related[prior] + Provenance + optional DiagnosticReport, FR-32) in the same
// two-step shape conformantSubmitBundle documents. qrJSON is REQUIRED here — the sdk's
// update builder rejects a nil QR — so a row with no populated answer set passes the
// minimal attested QuestionnaireResponse conformantQR mints; the reference payer's
// verdict is code-keyed and never reads the answers, so what the amendment is really
// proving is the attested evidence (the Provenance, and the report when there is one).
func conformantAmendBundle(member string, qrJSON, srJSON, drJSON, provJSON []byte, corr, originalCorr string, now time.Time) ([]byte, error) {
	b, err := shnsdk.BuildConformantClaimUpdateBundle(shnsdk.ConformantClaimUpdateInputs{
		QR:               qrJSON,
		Provider:         conformantRequestingProvider(),
		Coverage:         conformantMemberCoverage(member),
		Insurer:          conformantPayerOrganization(shnsdk.CMSPayerIdentity),
		SR:               srJSON,
		PatientRef:       "Patient/" + member,
		CoverageRef:      "Coverage/" + member,
		MemberID:         member,
		MemberIDSystem:   shnsdk.MemberSystem,
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
// The unwrap is scenariodriver.PackageEntries, which reads the package Bundle under each
// name the DTR lines publish for it (return, PackageBundle, packagebundle), as
// shnsdk.ExtractQuestionnaireFromPackage and the gateway's own reader do.
//
// The fence stays strict — a Parameters with no packagebundle Bundle, a packagebundle
// Bundle carrying no Questionnaire, and an empty or malformed body are all false.
func packageHasQuestionnaire(body []byte) bool {
	entries, err := scenariodriver.PackageEntries(body)
	if err != nil {
		return false
	}
	for _, e := range entries {
		var entry struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
			} `json:"resource"`
		}
		if json.Unmarshal(e, &entry) != nil {
			continue
		}
		if entry.Resource.ResourceType == "Questionnaire" {
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
	return requireAuthRefValue(uc, out.PreAuthRef)
}

// requireAuthRefValue is the anti-fallback fence on an authorization number this
// row learned any way at all — from the answer to a $submit, or from the payer's
// answer to the follow-up inquiry.
func requireAuthRefValue(uc, preAuthRef string) error {
	if !strings.HasPrefix(preAuthRef, "AUTH-") {
		return fmt.Errorf("runner: conformant/%s: authorization %q is not the reference payer's AUTH-NNNN — this run did not reach the reference payer", uc, preAuthRef)
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
	return attestedQR(pkg.Canonical, member, now)
}

// attestedQR is the minimal completed QuestionnaireResponse — subject, the questionnaire the
// payer's own package advertised, one attested item — that conformantQR falls back to when no
// provider system is wired. Split out so a row that must NOT take the br-provider prong can
// ask for it by name: the bridging demo runs against a persona br-provider's curated seed
// world has never heard of, so a populate call for that subject would be answering about a
// patient that does not exist there.
func attestedQR(canonical, member string, now time.Time) ([]byte, error) {
	qr, err := json.Marshal(map[string]any{
		"resourceType": "QuestionnaireResponse", "status": "completed",
		"questionnaire": canonical,
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
	"uc04": "pend",    // E0424 home oxygen — conditional; the request is held, and the decision is asked for
	"uc06": "pend",    // E0424 — the held request is amended by the attested re-submit, then asked about
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
//
// THE NO-PA FENCE IS `!= auth-needed`, NEVER `== no-auth`. The reference payer OMITS the
// pa-needed sub-extension entirely on a no-PA card: its live answer for this family is
// covered with pa-needed absent (PANeeded() == ""). That is the two-RI pin —
// test/tworilive/origination_test.go TestTwoRI_BRP_DVNoPA and tworilive_test.go
// TestTwoRI_DVNoPA both assert Covered()=="covered" && PANeeded()!="auth-needed" — and the
// pin, not the hermetic stand-in, is what a Kit fence is copied from. An explicit "no-auth"
// is equally acceptable (a payer MAY spell the absence out), so the fence tolerates both
// and refuses only a card that actually demands prior authorization.
func conformantUC02(rn *Runner, branch string) (string, error) {
	const member = "MBR-COVERED"
	if err := rn.requireConformantMember("uc02", member); err != nil {
		return "", err
	}
	order := scenariodriver.PersonaOrders["noPA"] // E0250, Hospital Bed with Side Rails

	cards, viaBFF, err := conformantCRD(rn, "uc02", "noPA", member)
	if err != nil {
		return "", err
	}
	if cards.Covered() != shnsdk.CoveredCovered {
		return "", fmt.Errorf("runner: conformant/uc02: covered=%q, want %q", cards.Covered(), shnsdk.CoveredCovered)
	}
	if cards.PANeeded() == shnsdk.PANeededAuthNeeded {
		return "", fmt.Errorf("runner: conformant/uc02: paNeeded=%q, want anything but %q — the reference payer advertises no prior authorization for this family (it omits pa-needed)",
			cards.PANeeded(), shnsdk.PANeededAuthNeeded)
	}
	summary := "coverage answer"
	if len(cards.Cards) > 0 && cards.Cards[0].Summary != "" {
		summary = cards.Cards[0].Summary
	}
	return originated(viaBFF, fmt.Sprintf("%s (HCPCS %s %s): covered=%s, %s",
		summary, order.Code, order.Display, cards.Covered(), noPADetail(cards.PANeeded()))), nil
}

// noPADetail renders the no-prior-authorization half of uc02's row detail out of what the
// card ACTUALLY said — never a value the Kit assumed on the payer's behalf. The reference
// payer omits pa-needed on a no-PA card, so the common case has no value to print at all;
// when a payer does spell one out, the raw value is shown.
func noPADetail(paNeeded string) string {
	if paNeeded == "" {
		return "no prior authorization (the card advertises no pa-needed)"
	}
	return "no prior authorization (paNeeded=" + paNeeded + ")"
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
	if err := rn.requireConformantMember("uc03", member); err != nil {
		return "", err
	}
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

// conformantUC03BridgeDemo is the BRIDGING DEMO branch — a different exhibit from the row
// above, and the only difference that matters is WHICH PEER answers. The MBR-BRIDGE-DEMO
// persona's own Coverage names the bridging demo payer holder, which declares NEWER contract
// lines than a stock build, so this run's CRD and DTR legs cross a contract-version boundary
// to reach it. That boundary is the exhibit.
//
// The VERDICT is not part of the exhibit: this peer native-forwards to the same pinned
// reference payer every other lane answers from, so the row drives the same family the
// unbridged row above drives (L8000 — covered, prior authorization required, the payer's own
// questionnaire on the card, approved on submit with an AUTH-NNNN reference). A bridged
// exchange and an ordinary one differ on the wire, never in what the payer decided — which is
// exactly the claim the exhibit is allowed to make.
func conformantUC03BridgeDemo(rn *Runner) (string, error) {
	const member = "MBR-BRIDGE-DEMO"
	if err := rn.requireConformantMember("uc03", member); err != nil {
		return "", err
	}
	ref := "Patient/" + member
	order := scenariodriver.PersonaOrders["approve"] // L8000

	// Direct-mint, never the br-provider BFF prong the unbridged row can take: br-provider's
	// curated seed world has no bridging persona, and the payer identity this run must reach
	// is read back off THIS request's own prefetch Coverage below.
	crdBody, err := scenariodriver.BuildCRDRequest(member, scenariodriver.SystemHCPCS, order.Code, order.Display)
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
	// The SAME two-halves check the unbridged row makes, for the same reason: this family is
	// covered AND needs prior authorization. Asserting it HERE is what proves the bridged
	// legs carried the request faithfully — a transform that mangled the order would show up
	// as a different coverage answer, not as a transport error.
	if cards.Covered() != shnsdk.CoveredCovered {
		return "", fmt.Errorf("runner: conformant/uc03(bridge-demo): covered=%q, want %q (the reference payer behind this peer covers this family — prior authorization is what it asks for)", cards.Covered(), shnsdk.CoveredCovered)
	}
	if cards.PANeeded() != shnsdk.PANeededAuthNeeded {
		return "", fmt.Errorf("runner: conformant/uc03(bridge-demo): paNeeded=%q, want %q (this family needs prior authorization)", cards.PANeeded(), shnsdk.PANeededAuthNeeded)
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
	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, order.Code, order.Display, "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: build order ServiceRequest: %w", err)
	}
	// The answers ride the peer's OWN questionnaire — the canonical its card advertised —
	// never a questionnaire the Kit brought with it. attestedQR rather than conformantQR:
	// see attestedQR's comment for why this row must not take the br-provider prong.
	qrJSON, err := attestedQR(canonical, member, now)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: %w", err)
	}
	// The submit bundle names the SAME payer the CRD/DTR legs of this run just
	// routed to — read back off the driver's own CRD request rather than
	// hardcoded, so this member, whose Coverage names the demo payer, cannot end
	// up with its PAS leg routed to the reference payer's holder instead.
	//
	// The submit itself is the SAME conformant two-step shape every reference-payer row
	// uses (conformantSubmitBundle), parameterized by that payer rather than a fixed CMS
	// identity — it no longer needs its own hand-assembled bundle. The demo peer's
	// payer-edge identity mapping (PAYER_DAVINCI_PAYOR_OWN / PAYER_DAVINCI_PAYOR_BACKEND)
	// re-stamps ownership at its back edge before forwarding, so conformance and routing
	// no longer trade off against each other — that trade-off was the old hand-assembled
	// bundle's whole rationale, and it is dead now that the payer stamp is parameterized.
	payer, err := conformantPayorFromCRD(crdBody)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc03: %w", err)
	}
	bundle, err := conformantSubmitBundle(member, payer, srJSON, qrJSON, randCorr("kit-uc03-bridge-submit"), now)
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
	// The SAME anti-fallback fence every reference-payer row carries: this peer forwards its
	// PAS pair to the reference payer, so an authorization that is not an AUTH-NNNN means the
	// submit was answered by something else and the row must not pass as approved.
	if err := requireAuthRef("uc03(bridge-demo)", out); err != nil {
		return "", err
	}
	return fmt.Sprintf("CRD card + DTR package + PAS submit approved across a contract-version boundary, auth %s", out.PreAuthRef), nil
}

// conformantStillHeld is the row detail for a request the payer is still holding
// after it was asked.
//
// It states the payer's state and nothing more. There is no authorization number in
// it because the payer has not given one, and it claims no decision because none was
// made: a row that ended here reached exactly this and says exactly this.
const conformantStillHeld = "still held by the payer; the payer has decided nothing yet and the continuation is recorded"

// The schedule a held row asks on: the first inquiry after conformantFirstInquiry,
// each later one after twice the previous delay capped at conformantInquiryBackoff,
// at most shnsdk.MaxPriorAuthInquiries inquiries and none falling due after
// shnsdk.MaxPriorAuthWait. It is the schedule the SDK client follows when a caller
// asks it to wait (2, 4, 5, 5, 5, 5 seconds; the last falls due at 26 s), so what
// this row does is what the shipped client does.
const (
	conformantFirstInquiry   = 2 * time.Second
	conformantInquiryBackoff = 5 * time.Second
)

// conformantContinuation records what a requester keeps when a payer goes on holding
// its request, then ASKS — Claim/$inquire through the gateway's inquiry leg, on the
// bounded schedule above — until the payer states a decision or the bound is reached.
// It returns the row's ending: the payer's decision as the payer stated it when asked,
// or the hold, still standing, with the continuation recorded.
//
// WHY A ROW ENDS HERE. A payer decides when it decides — often hours or days later.
// The payer's answer to a $submit is its answer to THAT operation; a decision made
// later reaches the requester only because the requester learns it, and the manual
// way to learn it is an inquiry (Claim/$inquire). So a still-held ending is a real
// outcome, not a soft failure: it is what a partner integrating against a live payer
// sees most of the time, and a row that could only end "approved" taught the opposite.
//
// The continuation is built from the bytes actually sent and the answer actually
// received (shnsdk.NewPriorAuthContinuation), plus the requester's own records —
// never from a second reading of what the row meant to send. Every inquiry is its own
// exchange with its own identifier, and the payer's answer is read by the INQUIRY
// reader (shnsdk.InquiryDecision), never the submit one: an inquiry answer's envelope
// is the line's (a response Bundle at 2.0.1/2.1.0, a Parameters carrying Bundles at
// 2.2.1), and its contents are every authorization the payer holds for the parties
// the inquiry named, so this row's has to be SELECTED — by the continuation's own
// facts. An answer that is still a hold updates the continuation with what the payer
// said, exactly as the shipped client does.
//
// WHAT THE WAIT IS, AND WHAT IT IS NOT. Prior Authorization's mechanism for learning
// a later decision is subscription; inquiry is the permitted manual status check.
// This network offers no notification path yet, so the row stands a BOUNDED wait in:
// a handful of inquiries, none after the bound, made by this requester because it
// chose to wait — never a poll a gateway runs on a participant's behalf. Reaching the
// bound is a real outcome: the payer has not decided, and the row says so.
func conformantContinuation(rn *Runner, uc, member string, payer shnsdk.PayerIdentifier, submitted, answered []byte) (string, error) {
	cont, err := shnsdk.NewPriorAuthContinuation("2.0", string(payer.Value), member, submitted, answered)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/%s: continuation: %w", uc, err)
	}
	// The request lines, carrying the numbers the payer gave them, are how the
	// requester will pick ITS authorization out of an answer that carries every
	// authorization the payer holds for the parties named. A continuation without
	// them is a record that could never be matched back.
	if len(cont.Items) == 0 {
		return "", fmt.Errorf("runner: conformant/%s: the payer's answer leaves no request line to ask about later: %s", uc, excerpt(answered))
	}
	patient, err := conformantBundlePatient(submitted)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/%s: %w", uc, err)
	}
	start := rn.now()
	deadline := start.Add(shnsdk.MaxPriorAuthWait)
	delay := conformantFirstInquiry
	inquiries := 0
	for inquiries < shnsdk.MaxPriorAuthInquiries && !rn.now().Add(delay).After(deadline) {
		if err := rn.wait(delay); err != nil {
			return "", fmt.Errorf("runner: conformant/%s: wait before inquiry: %w", uc, err)
		}
		inquiries++
		now := rn.now()
		inquiry, err := shnsdk.BuildPASInquiryBundle("2.0", cont.InquiryInputs(
			"kit-inquiry", shnsdk.PASIdentifier{System: shnsdk.PASInquiryIdentifierSystem, Value: randCorr("kit-" + uc + "-inquire")},
			shnsdk.PASInquiryRecords{
				Patient:  patient,
				Coverage: conformantMemberCoverage(member),
				Provider: conformantRequestingProvider(),
				Insurer:  conformantPayerOrganization(payer),
			}, now))
		if err != nil {
			return "", fmt.Errorf("runner: conformant/%s: build inquiry: %w", uc, err)
		}
		routable, err := scenariodriver.AddRoutablePayorFor(inquiry.Body, payer)
		if err != nil {
			return "", fmt.Errorf("runner: conformant/%s: make the inquiry routable: %w", uc, err)
		}
		if !bytes.Contains(routable, []byte(`"`+payer.Value+`"`)) {
			return "", fmt.Errorf("runner: conformant/%s: the prepared inquiry does not name the payer it is sent to", uc)
		}
		res, err := rn.cfg.Driver.InquirePAS(routable)
		if err != nil {
			return "", fmt.Errorf("runner: conformant/%s: inquire: %w", uc, err)
		}
		if res.Status != http.StatusOK {
			return "", conformantIngressErr(uc+": inquire", res.Status, res.Body)
		}
		decision, err := shnsdk.InquiryDecision(res.Body, cont)
		if err != nil {
			// The reader's own words carry the reason — no decision about this
			// request, more than one, or an answer it could not read — so the
			// wrapper names none of them.
			return "", fmt.Errorf("runner: conformant/%s: the payer's inquiry answer does not state this request's decision (%w): %s", uc, err, excerpt(res.Body))
		}
		asked := conformantAsked(inquiries, rn.now().Sub(start))
		switch decision.Outcome {
		case "pended":
			if err := cont.Record(res.Body); err != nil {
				return "", fmt.Errorf("runner: conformant/%s: record the payer's answer: %w", uc, err)
			}
			delay = min(2*delay, conformantInquiryBackoff)
		case "approved":
			if err := requireAuthRefValue(uc, decision.PreAuthRef); err != nil {
				return "", err
			}
			return fmt.Sprintf("approved when asked, auth %s (%s)", decision.PreAuthRef, asked), nil
		default:
			// A decision that is not the approval this scenario is about, in the
			// payer's own words where it gave any.
			words := ""
			if decision.Denial != nil && decision.Denial.Rationale != "" {
				words = ": " + decision.Denial.Rationale
			}
			return "", fmt.Errorf("runner: conformant/%s: the payer did not approve when asked: it answered %q%s (%s)", uc, decision.Outcome, words, asked)
		}
	}
	return fmt.Sprintf("%s (%s)", conformantStillHeld, conformantAsked(inquiries, rn.now().Sub(start))), nil
}

// conformantAsked states how the asking went: how many inquiries, over how long.
func conformantAsked(inquiries int, elapsed time.Duration) string {
	noun := "inquiries"
	if inquiries == 1 {
		noun = "inquiry"
	}
	return fmt.Sprintf("%d %s over %s", inquiries, noun, elapsed.Round(100*time.Millisecond))
}

// conformantBundlePatient returns the Patient entry a request carried — the
// requester's own record of the member, which the inquiry embeds unchanged.
func conformantBundlePatient(bundle []byte) ([]byte, error) {
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		return nil, fmt.Errorf("read the submitted bundle: %w", err)
	}
	for _, e := range b.Entry {
		var head struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(e.Resource, &head) == nil && head.ResourceType == "Patient" {
			return e.Resource, nil
		}
	}
	return nil, fmt.Errorf("the submitted bundle carries no Patient entry")
}

// conformantHeldStillHeld is the shared body of the two rows that submit an E0424
// request the reference payer HOLDS and amend it with attested evidence: uc04 carries
// the operative report the clinician wrote, uc05 the facility evidence a federated
// query retrieved. The evidence is the only difference, so it is the only parameter
// (dr/prov), alongside the scenario key the CRD prong routes on.
//
// WHAT THE AMENDMENT DOES. It carries evidence. It does not decide, and neither does
// this gateway: a payer holding a request answers the amendment with its own answer,
// which for this reference payer is a fresh "still held", and decides on its own
// schedule. So the row relays that answer, records the continuation against it and
// ASKS for the decision on a bounded schedule (conformantContinuation); it ends on
// the payer's own answer to the asking — the decision, or the hold still standing.
//
// A still-held ending is a real outcome, not a soft failure. It is what a partner
// integrating against a live payer will see most of the time, and a row that could
// only end "approved" taught the opposite.
func conformantHeldStillHeld(rn *Runner, uc, member string, evidence func(ref string, now time.Time) (drJSON, provJSON []byte, err error)) (viaBFF bool, detail string, err error) {
	if err := rn.requireConformantMember(uc, member); err != nil {
		return false, "", err
	}
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
	// The payer's answer to the amendment reaches the requester as the payer wrote
	// it. A payer that decides right there says so; this reference payer re-pends and
	// decides on its own timer.
	if !amendOut.Pended {
		if err := requireAuthRef(uc, amendOut); err != nil {
			return viaBFF, "", err
		}
		return viaBFF, fmt.Sprintf("decided on the payer's answer to the amended re-submit, auth %s", amendOut.PreAuthRef), nil
	}
	// The state a partner most needs to recognise: the payer has not decided, nothing
	// has gone wrong, and the requester keeps the continuation and asks with it. Real
	// payers stay here for hours or days; the reference payer decides on its timer.
	detail, err = conformantContinuation(rn, uc, member, shnsdk.CMSPayerIdentity, amendBundle, amendOut.Body)
	if err != nil {
		return viaBFF, "", err
	}
	return viaBFF, "held again on the amendment, then " + detail, nil
}

// conformantUC04 — E0424: the reference payer HOLDS the request; the amended
// re-submit carries the operative report the clinician wrote, and the payer goes on
// holding it.
//
// The amendment carries evidence; it does not resolve anything. This row used to say
// the authorization was "resolved on the amended re-submit carrying the operative
// report", which reads as though the report changed the payer's mind on that
// exchange. Measured against the reference payer, no gateway in between: it answers
// the amendment "still held" and decides on its own schedule, whatever the amendment
// carried.
func conformantUC04(rn *Runner, branch string) (string, error) {
	viaBFF, detail, err := conformantHeldStillHeld(rn, "uc04", "MBR-COVERED", func(ref string, now time.Time) ([]byte, []byte, error) {
		drJSON, err := shnsdk.BuildDiagnosticReport("dr-kit-uc04", ref, "E0424", "Operative report — home oxygen assessment")
		if err != nil {
			return nil, nil, fmt.Errorf("build operative DiagnosticReport: %w", err)
		}
		provJSON, err := shnsdk.BuildProvenanceWithIdentifier("DiagnosticReport/dr-kit-uc04", shnsdk.ProvenanceIdentifier{System: "http://smarthealth.network/ids/holder", Value: "provider"}, now)
		if err != nil {
			return nil, nil, fmt.Errorf("build Provenance: %w", err)
		}
		return drJSON, provJSON, nil
	})
	if err != nil {
		return "", err
	}
	return originated(viaBFF, "held by the reference payer; the amended re-submit carried the operative report, and "+detail), nil
}

// conformantUC05 — the same held E0424 request, amended with evidence a FEDERATED
// query retrieved from the facility (CXL-D11: the CDex middle bracketed by SHN
// gateways, not real external CDex actors). As in uc04, the federated evidence is
// what the amendment CARRIES; the payer decides on its own schedule.
func conformantUC05(rn *Runner, branch string) (string, error) {
	const member = "MBR-COVERED"
	viaBFF, detail, err := conformantHeldStillHeld(rn, "uc05", member, func(ref string, now time.Time) ([]byte, []byte, error) {
		drJSON, provJSON, err := scenariodriver.FacilityCDexEvidence(member, now)
		if err != nil {
			return nil, nil, fmt.Errorf("facility CDex evidence: %w", err)
		}
		return drJSON, provJSON, nil
	})
	if err != nil {
		return "", err
	}
	return originated(viaBFF, "held by the reference payer; the amended re-submit carried the federated facility evidence, and "+detail), nil
}

// conformantUC06 — the questionnaire row: the E0424 coverage check answers CONDITIONAL
// and advertises no questionnaire, so the reference payer's HomeOxygen package is
// fetched BY CANONICAL, filled (by br-provider's real populate under the Java trio,
// by a minimal attestation without it), submitted — held — amended by the
// clinician-attested re-submit whose Provenance attests those very answers, and held
// again. The attestation is what the amendment carries; the payer decides on its own
// schedule.
func conformantUC06(rn *Runner, branch string) (string, error) {
	const member = "MBR-UC06"
	if err := rn.requireConformantMember("uc06", member); err != nil {
		return "", err
	}
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
	provJSON, err := shnsdk.BuildProvenanceWithIdentifier("QuestionnaireResponse/attested", shnsdk.ProvenanceIdentifier{System: "http://smarthealth.network/ids/holder", Value: "provider"}, now)
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
	if !amendOut.Pended {
		if err := requireAuthRef("uc06", amendOut); err != nil {
			return "", err
		}
		return originated(viaBFF, fmt.Sprintf("questionnaire fetched and filled by the provider system; held, then decided on the payer's answer to the clinician-attested re-submit, auth %s", amendOut.PreAuthRef)), nil
	}
	// The payer answered the amendment "still held" and decides on its own schedule.
	// The row records the continuation, asks with it, and reports the payer's own
	// answer to the asking — nothing more.
	detail, err := conformantContinuation(rn, "uc06", member, shnsdk.CMSPayerIdentity, amendBundle, amendOut.Body)
	if err != nil {
		return "", err
	}
	const prefix = "questionnaire fetched and filled by the provider system; held, then held again on the clinician-attested re-submit, then "
	return originated(viaBFF, prefix+detail), nil
}

// conformantUC07 — the patient surface: an L8000 request the reference payer approves,
// then read back the way the patient sees it. The coverage check is not what this row
// is about, so it submits directly.
func conformantUC07(rn *Runner, branch string) (string, error) {
	const member = "MBR-UC07HCPCS"
	if err := rn.requireConformantMember("uc07", member); err != nil {
		return "", err
	}
	ref := "Patient/" + member
	now := rn.now()
	order := scenariodriver.PersonaOrders["approve"] // L8000

	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, order.Code, order.Display, "M51.16", ref)
	if err != nil {
		return "", fmt.Errorf("runner: conformant/uc07: build order ServiceRequest: %w", err)
	}
	qrJSON, err := conformantQR(rn, scenariodriver.DTRPackage{Canonical: l8000Canonical, Member: member}, member, now)
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
	if err := rn.requireConformantMember("uc08", member); err != nil {
		return "", err
	}
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
