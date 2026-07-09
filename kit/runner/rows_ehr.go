package runner

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ehrRows is the "ehr" lane's row table: each row POSTs to the child's
// /scenario/* provider-data origination route (the same surface make-e2e's
// harness drives; response contracts copied from those decode structs —
// test/harness/harness.go:745-891, read at design time; this package never
// imports it).
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

// ehrScenario POSTs body (marshaled to JSON) to path on the child's
// provider-data origination base and decodes a 200 response into out (out
// may be nil to discard the body). A non-200 status is an error carrying an
// excerpt of the body.
func ehrScenario(rn *Runner, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("runner: marshal %s request: %w", path, err)
	}
	res, err := rn.cfg.Driver.RunProviderDataScenario(path, string(b))
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

// uc03Resp mirrors test/harness.UC03Result's wire shape (the shared
// CRD→DTR→PAS scenario response for uc03/04/06/07). A local decode struct,
// not a cross-import (kit's publish boundary forbids importing the private
// substrate module).
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
	return fmt.Sprintf("covered=%v: %s", out.Covered, out.Reason), nil
}

func ehrUC02(rn *Runner, branch string) (string, error) {
	var out struct {
		PARequired  bool   `json:"paRequired"`
		CardSummary string `json:"cardSummary"`
	}
	if err := ehrScenario(rn, "/scenario/uc02", map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.PARequired {
		return "", fmt.Errorf("runner: ehr/uc02: paRequired=true, want false")
	}
	if out.CardSummary == "" {
		return "", fmt.Errorf("runner: ehr/uc02: empty cardSummary")
	}
	return fmt.Sprintf("no PA required: %s", out.CardSummary), nil
}

func ehrUC03(rn *Runner, branch string) (string, error) {
	var out uc03Resp
	if err := ehrScenario(rn, "/scenario/uc03", map[string]any{}, &out); err != nil {
		return "", err
	}
	if !out.PARequired {
		return "", fmt.Errorf("runner: ehr/uc03: paRequired=false, want true")
	}
	if out.AuthNumber == "" {
		return "", fmt.Errorf("runner: ehr/uc03: empty authNumber")
	}
	// Under the Java trio (native DTR), the gateway's
	// nativePopulator forwards to a real SDC $populate and — by frozen,
	// documented gateway design (gateway/engine/originate.go's QRAnswers
	// comment; gateway/engine/nativepopulate.go) — DROPS per-item FilledItem
	// attribution, so QRItems is always empty there; that is not a defect.
	// The genuine-CQL evidence in that mode is the approval itself: uc03's
	// sandbox persona seeds a 6-week conservative-therapy Observation and
	// SandboxAdjudicate denies below 6 weeks (a missing/unloadable Library
	// would instead read 0 weeks and deny), so reaching a non-empty
	// AuthNumber already proves the real
	// Library populated the correct value. cfg.BFFURL is only ever non-empty
	// when the trio is configured, so keep the
	// stronger attribution check as a regression pin for the non-trio
	// managed-populator path, where FilledItem IS preserved.
	if rn.cfg.BFFURL == "" && len(out.QRItems) == 0 {
		return "", fmt.Errorf("runner: ehr/uc03: 0 qrItems, want >=1")
	}
	return fmt.Sprintf("approved, auth %s, %d QR items", out.AuthNumber, len(out.QRItems)), nil
}

func ehrUC04(rn *Runner, branch string) (string, error) {
	var out uc03Resp
	if err := ehrScenario(rn, "/scenario/uc04", map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.AuthNumber == "" {
		return "", fmt.Errorf("runner: ehr/uc04: empty authNumber")
	}
	if len(out.PendedItems) == 0 {
		return "", fmt.Errorf("runner: ehr/uc04: 0 pendedItems, want >=1")
	}
	return fmt.Sprintf("pended (%v) then approved via ClaimUpdate, auth %s", out.PendedItems, out.AuthNumber), nil
}

func ehrUC05(rn *Runner, branch string) (string, error) {
	var out struct {
		PARequired    bool     `json:"paRequired"`
		AuthNumber    string   `json:"authNumber"`
		ValidUntil    string   `json:"validUntil"`
		PendedItems   []string `json:"pendedItems"`
		FacilityID    string   `json:"facilityId"`
		Pended        bool     `json:"pended"`
		ConsentDenied bool     `json:"consentDenied"`
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
		return "consent denied: federated CDex query blocked, no authorization issued", nil
	}
	if out.AuthNumber == "" {
		return "", fmt.Errorf("runner: ehr/uc05(%s): empty authNumber", branch)
	}
	if out.FacilityID == "" {
		return "", fmt.Errorf("runner: ehr/uc05(%s): empty facilityId", branch)
	}
	return fmt.Sprintf("approved via federated evidence from facility %s, auth %s", out.FacilityID, out.AuthNumber), nil
}

func ehrUC06(rn *Runner, branch string) (string, error) {
	var out uc03Resp
	if err := ehrScenario(rn, "/scenario/uc06", map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.AuthNumber == "" {
		return "", fmt.Errorf("runner: ehr/uc06: empty authNumber")
	}
	if len(out.PendedItems) == 0 {
		return "", fmt.Errorf("runner: ehr/uc06: 0 pendedItems, want >=1")
	}
	return fmt.Sprintf("pended (%v) then approved via clinician-attested ClaimUpdate, auth %s", out.PendedItems, out.AuthNumber), nil
}

func ehrUC07(rn *Runner, branch string) (string, error) {
	path := "/scenario/uc07"
	if branch == "hcpcs" {
		path = "/scenario/uc07hcpcs"
	}
	var out uc03Resp
	if err := ehrScenario(rn, path, map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.AuthNumber == "" {
		return "", fmt.Errorf("runner: ehr/uc07(%s): empty authNumber", branch)
	}
	detail := fmt.Sprintf("approved, auth %s", out.AuthNumber)
	if branch != "hcpcs" {
		return detail, nil
	}
	// The patient-surface read-back needs the hosted /personas + /authorizations render.
	// When it is not externally reachable (hosted topology: phg is the machine /notify
	// edge; the reads are internal/patient-only), skip it gracefully — the PA itself
	// already succeeded and asserted. Reachability gate, not a removal (see
	// runner.Config.PatientSurfaceReadable): a future reachable read-back won't degrade.
	if !rn.cfg.PatientSurfaceReadable {
		return detail + "; patient-surface read-back skipped (hosted patient reads are internal/patient-only)", nil
	}
	n, total, err := uc07PatientSurfaceReadBack(rn)
	if err != nil {
		return "", fmt.Errorf("runner: ehr/uc07(hcpcs): %w", err)
	}
	if n == 0 {
		return "", fmt.Errorf("runner: ehr/uc07(hcpcs): 0 approved rows in patient-surface read-back (of %d)", total)
	}
	return detail + fmt.Sprintf("; patient-surface read-back: %d/%d approved row(s)", n, total), nil
}

func ehrUC08(rn *Runner, branch string) (string, error) {
	var out struct {
		PARequired          bool   `json:"paRequired"`
		Denied              bool   `json:"denied"`
		AuthNumber          string `json:"authNumber"`
		Rationale           string `json:"rationale"`
		PatientDenialReason string `json:"patientDenialReason"`
	}
	if err := ehrScenario(rn, "/scenario/uc08", map[string]any{}, &out); err != nil {
		return "", err
	}
	if !out.Denied {
		return "", fmt.Errorf("runner: ehr/uc08: denied=false, want true")
	}
	if out.AuthNumber != "" {
		return "", fmt.Errorf("runner: ehr/uc08: authNumber=%q, want empty", out.AuthNumber)
	}
	// The payer's denial rationale always travels back — that is the
	// environment-independent signal. patientDenialReason is a best-effort,
	// fail-open patient-app lookup this build does not wire, so it is absent
	// here; assert the rationale and surface the patient reason only when set.
	if out.Rationale == "" {
		return "", fmt.Errorf("runner: ehr/uc08: empty rationale")
	}
	detail := fmt.Sprintf("denied: %s", out.Rationale)
	if out.PatientDenialReason != "" {
		detail += fmt.Sprintf("; patient reason: %s", out.PatientDenialReason)
	}
	return detail, nil
}

// ehrFreeform drives the "freeform" row: a
// caller-named member dispatched against THEIR OWN provider data via the
// child's POST /scenario/dispatch — no answer book, no lane/uc03 baked
// assumptions (gateway/engine/originate_homeoxygen.go's handleDispatch). Its
// response shares uc03Resp's wire shape byte-for-byte (paRequired/authNumber/
// validUntil/qrItems/pendedItems — the extra fields that route carries,
// amendmentCorr/attested/qrAnswers, decode as zero values here and are
// unused), so this cribs ehrUC03's own response-handling shape verbatim.
//
// When ehrScenario's error is one of the two recognized member-unknown wire
// shapes, the row names the constraint in plain language instead of
// relaying the raw status/body — but the two shapes get DIFFERENT
// sentences (see freeformProviderUnknownMemberSentence and
// freeformPayerRoutingFailedSentence's docs), because they are not the same
// claim: the provider shape is a definitive fact about the operator's OWN
// connected system, while the payer shape is an opaque relay that a payer
// outage would also produce, so it earns likely-cause phrasing rather than
// a flat assertion.
func ehrFreeform(rn *Runner, member string) (string, error) {
	var out uc03Resp
	if err := ehrScenario(rn, "/scenario/dispatch", map[string]string{"member": member}, &out); err != nil {
		if isFreeformProviderUnknownMember(err) {
			return "", fmt.Errorf("runner: ehr/freeform(%s): %s (%v)", member, freeformProviderUnknownMemberSentence, err)
		}
		if isFreeformPayerRoutingFailed(err) {
			return "", fmt.Errorf("runner: ehr/freeform(%s): %s (%v)", member, freeformPayerRoutingFailedSentence, err)
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

// freeformPayerRoutingFailedSentence is the plain-language sentence for the
// PAYER-side wire shape (the realistic bring-your-own case: the member
// exists in the operator's own connected EHR — originateDispatch's own
// ResolvePatient already passed — but the payer counterparty may not cover
// it) — status 502, body {"error":"hub routing failed"}.
// gateway/engine/gateway.go's OriginateLeg wraps EVERY postEnvelope failure
// into this one deliberately terse string (its own comment), and
// internal/hubsvc/hubsvc.go's /route handler (:472/:481) deliberately
// answers "forward to recipient failed" rather than relay the recipient's
// real body — the payer's own reason (e.g. its own 400 "unknown member")
// is visible only in hub/payer SERVER LOGS, never on this wire. Because the
// payload-blind Hub discards that reason, this exact byte shape is also
// what a payer OUTAGE produces — so unlike the provider-side sentence
// above, this one is deliberately LIKELY-CAUSE phrasing, not a definitive
// assertion: an earlier draft of this sentence asserted the payer cause
// outright, but that premise proved false on the wire once traced live
// (twice, at exactly this byte shape, before a persona-seeding fix —
// `detail="runner: POST /scenario/dispatch: status 502:
// {\"error\":\"hub routing failed\"}\n"`, from two different pre-fix
// downstream causes), so a definitive assertion would have over-claimed.
// It remains what a genuinely arbitrary
// bring-your-own member produces today — the persona-seeding fix only seeded the
// two DEMO order-dispatch personas, not arbitrary partner data.
const freeformPayerRoutingFailedSentence = "the prior authorization couldn't be routed to completion with the payer — when running against your own data, the most common cause is a member id the payer counterparty doesn't cover (see the member requirements note)"

// isFreeformProviderUnknownMember recognizes the PROVIDER-side wire shape
// (status 400, body {"error":"unknown member"}) — see
// freeformProviderUnknownMemberSentence's doc for the origin. Every other
// failure (e.g. a genuine policy denial, "preauthorization not approved" —
// gateway/engine/pas_tail.go, originate.go) keeps its raw detail unchanged;
// see TestRun_FreeformPolicyDenial_NotRelabeled.
func isFreeformProviderUnknownMember(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 400") && strings.Contains(msg, `"unknown member"`)
}

// isFreeformPayerRoutingFailed recognizes the PAYER-side wire shape (status
// 502, body {"error":"hub routing failed"}) — see
// freeformPayerRoutingFailedSentence's doc for the origin. Matching
// requires BOTH the status and the exact body text: a different 502 (e.g.
// {"error":"preauthorization not approved"}, a genuine policy denial)
// keeps its raw detail unchanged; see
// TestRun_FreeformPolicyDenial_NotRelabeled — status alone is not the
// discriminator.
func isFreeformPayerRoutingFailed(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 502") && strings.Contains(msg, `"hub routing failed"`)
}

// uc07PatientSurfaceReadBack resolves the UC-07 HCPCS PCI (Config.UC07PCI)
// and reads back the patient-surface /authorizations render, returning the
// count of approved rows and the total row count. Shared by the ehr and
// conformant uc07 hcpcs rows (D-2RI-6 analog: the read-back is lane-agnostic
// — it always reads the same patient-surface projection).
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
