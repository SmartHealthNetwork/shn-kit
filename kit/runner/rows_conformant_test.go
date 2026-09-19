// rows_conformant_test.go — the Da Vinci lane's rows against a FAKE ingress that
// mirrors the hosted Da Vinci reference payer's four code-keyed families (E0250
// covered/no-PA, L8000 prior-authorization/approved, E0424 conditional/held-then-
// resolved, J3490 not-covered/denied). One passing case per row, then the mutation
// table: each passing exchange with ONE verdict swapped must fail naming the fence
// it broke — above all the AUTH- fence, which is what makes a silent fall-back to
// some other payer unable to pass.
//
// The verdict shapes are the ones test/tworilive reads off the live reference payer
// and test/harness mirrors for the hermetic gate; this package cannot import either
// (the Kit's publish boundary), so the shapes are rebuilt here from the sdk's own
// response builders.
package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// ---- canned reference-payer answers ----------------------------------------

// The CRD cards, in the exact shape scenariodriver.ParseCards reads
// (gateway/scenariodriver/cards.go — extension{covered,paNeeded,questionnaires}).
// Which card each family gets is the reference payer's own answer, mirrored:
// E0250 covered with NO pa-needed at all, L8000 covered/auth-needed + a questionnaire
// canonical, E0424 CONDITIONAL with NO questionnaire advertised, J3490 not-covered.
//
// dvCardCovered carries NO paNeeded on purpose: that is the LIVE shape of the reference
// payer's no-PA card (two-RI pin TestTwoRI_BRP_DVNoPA — covered, pa-needed absent). The
// earlier fixture spelled out "no-auth", which is what let uc02's `== no-auth` fence pass
// here and fail on a fresh machine (v0.15.1). dvCardCoveredNoAuth keeps the spelled-out
// variant so the row's TOLERANCE of both shapes is itself pinned.
const (
	dvCardCovered       = `{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered"}}]}`
	dvCardCoveredNoAuth = `{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`
	dvCardAuthNeeded    = `{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"covered","paNeeded":"auth-needed","questionnaires":["` + l8000Canonical + `"]}}]}`
	dvCardConditional   = `{"cards":[{"summary":"Coverage is conditional","indicator":"warning","extension":{"covered":"conditional","paNeeded":"no-auth"}}]}`
	dvCardNotCovered    = `{"cards":[{"summary":"Not covered under the member's plan","indicator":"warning","extension":{"covered":"not-covered"}}]}`

	// dvCardAuthNeededNotCovered is dvCardAuthNeeded with ONE fact mutated: the
	// coverage answer. The prior-authorization answer and the questionnaire
	// canonical are untouched, so a row that only reads paNeeded would sail
	// straight past it.
	dvCardAuthNeededNotCovered = `{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"not-covered","paNeeded":"auth-needed","questionnaires":["` + l8000Canonical + `"]}}]}`

	// dvCardAuthNeededNoQuestionnaire is dvCardAuthNeeded with ONE fact
	// mutated: the questionnaire canonical is gone. Coverage and the
	// prior-authorization answer still say "fetch a questionnaire and submit".
	dvCardAuthNeededNoQuestionnaire = `{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"covered","paNeeded":"auth-needed"}}]}`
)

// dvPackage is a BARE $questionnaire-package response Bundle carrying one Questionnaire —
// the shape the bridging demo payer (and SHN's own provider-data path) answers with, and
// enough for packageHasQuestionnaire and for br-provider's populate to have something
// to fill.
const dvPackage = `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Questionnaire","id":"pkg-q","status":"active","url":"` + homeOxygenCanonical + `"}}]}`

// dvPackageWrapped is the HOSTED Da Vinci reference payer's real answer to
// $questionnaire-package: a Parameters profiled on dtr-qpackage-output-parameters whose
// packagebundle parameter carries the collection Bundle, plus an outcome parameter
// (the shape captured live off the hosted payer: profile, packagebundle, outcome).
// The gateway relays it VERBATIM on the ingress, so this — not the bare Bundle — is what
// the Kit's Da Vinci rows actually read off the wire.
const dvPackageWrapped = `{"resourceType":"Parameters","meta":{"profile":["http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/dtr-qpackage-output-parameters"]},` +
	`"parameter":[{"name":"packagebundle","resource":` + dvPackage + `},` +
	`{"name":"outcome","resource":{"resourceType":"OperationOutcome","issue":[{"severity":"information","code":"informational"}]}}]}`

// dvPackageWrappedNoQuestionnaire is dvPackageWrapped with ONE fact mutated: the
// packagebundle Bundle carries no Questionnaire at all (only a Library). The wrapper and
// its profile are untouched, so a fence that merely recognised the wrapper would sail
// straight past it.
const dvPackageWrappedNoQuestionnaire = `{"resourceType":"Parameters","meta":{"profile":["http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/dtr-qpackage-output-parameters"]},` +
	`"parameter":[{"name":"packagebundle","resource":{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Library","id":"pkg-lib","status":"active"}}]}}]}`

// dvPackageBareNoQuestionnaire is the bare-Bundle shape with the same one fact mutated —
// the fence must stay strict on BOTH shapes, not only on the one it was written for.
const dvPackageBareNoQuestionnaire = `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Library","id":"pkg-lib","status":"active"}}]}`

// dvBridgeDemoMember is the bridging-demo persona: its Coverage names the DEMO payer, which
// answers $questionnaire-package with a bare Bundle — the ingress fake keys on it exactly
// the way the two real payers differ on the wire.
const dvBridgeDemoMember = "MBR-BRIDGE-DEMO"

// dvPopulatedQR is what the fake br-provider populate endpoint answers with.
const dvPopulatedQR = `{"resourceType":"QuestionnaireResponse","id":"populated","status":"completed","questionnaire":"` + homeOxygenCanonical + `","subject":{"reference":"Patient/MBR-COVERED"},"item":[{"linkId":"1","answer":[{"valueString":"populated by the provider system"}]}]}`

// dvApproved is the reference payer's approved ClaimResponse with the given
// authorization reference.
func dvApproved(preAuthRef string) string {
	return `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"` + preAuthRef + `"}`
}

// dvInquiryEnvelope is the envelope the reference payer answers Claim/$inquire in
// (the live capture the SDK's inquiry reader was built against): a Parameters
// whose single parameter, named responseBundle, holds the response Bundle. The
// gateway relays those bytes verbatim, so this is what a row's inquiry reader is
// handed — never the bare Bundle a $submit is answered with.
func dvInquiryEnvelope(bundle string) string {
	return `{"resourceType":"Parameters","parameter":[{"name":"responseBundle","resource":` + bundle + `}]}`
}

// dvInquiryNoMatch is the reference payer's answer to an inquiry that matches
// nothing it holds, byte for byte as captured: an empty Parameters, no output
// parameter at all.
const dvInquiryNoMatch = `{"resourceType":"Parameters"}`

// dvApprovedWhenAsked is the reference payer's answer to an inquiry once its timer
// has decided: the inquiry envelope around a response Bundle whose ClaimResponse
// is complete and states the authorization number in the review action's own
// number — it carries no preAuthRef at all (the live capture the SDK's inquiry
// reader was built against). The request line is present so the payer's echo of
// the inquiry's trace number has somewhere to land; without it the answer names
// no request.
func dvApprovedWhenAsked(number string) string {
	return dvInquiryEnvelope(`{"resourceType":"Bundle","type":"collection","timestamp":"2026-06-04T00:00:00Z","entry":[` +
		`{"fullUrl":"https://payer.example/fhir/ClaimResponse/cr-decided","resource":{"resourceType":"ClaimResponse","id":"cr-decided",` +
		`"status":"active","use":"preauthorization","patient":{"reference":"Patient/MBR-COVERED"},"outcome":"complete",` +
		`"item":[{"itemSequence":1,"adjudication":[{"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted"}]},` +
		`"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[` +
		`{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode",` +
		`"valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A1","display":"Certified in total"}]}},` +
		`{"url":"number","valueString":"` + number + `"}]}]}]}]}}]}`)
}

// dvDeniedWhenAsked is an inquiry answer that states a denial (A3) with the
// payer's own disposition, on the same envelope as dvApprovedWhenAsked.
func dvDeniedWhenAsked(rationale string) string {
	return dvInquiryEnvelope(`{"resourceType":"Bundle","type":"collection","timestamp":"2026-06-04T00:00:00Z","entry":[` +
		`{"fullUrl":"https://payer.example/fhir/ClaimResponse/cr-denied","resource":{"resourceType":"ClaimResponse","id":"cr-denied",` +
		`"status":"active","use":"preauthorization","patient":{"reference":"Patient/MBR-COVERED"},"outcome":"complete","disposition":"` + rationale + `",` +
		`"item":[{"itemSequence":1,"adjudication":[{"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted"}]},` +
		`"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[` +
		`{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode",` +
		`"valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A3","display":"Not Certified"}]}}]}]}]}]}}]}`)
}

// dvInquiryAnswerNoLines is an inquiry answer about SOME authorization, in the
// inquiry envelope — a complete ClaimResponse with no request line, so no trace
// number of the inquiry's can be echoed onto it and nothing ties it to the
// request asked about. Distinct from dvInquiryNoMatch: here the payer answered
// WITH a decision, and the row must not take it for its own.
var dvInquiryAnswerNoLines = dvInquiryEnvelope(`{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"https://payer.example/fhir/ClaimResponse/cr-other","resource":{"resourceType":"ClaimResponse","id":"cr-other","status":"active","use":"preauthorization","outcome":"complete"}}]}`)

// dvPended is the held (A4) response shape a $submit or $update is answered with:
// a response Bundle holding the pended ClaimResponse and the payer's profiled Task
// asking for a questionnaire.
func dvPended() string {
	return `{"resourceType":"Bundle","type":"collection","timestamp":"2026-06-04T00:00:00Z","entry":[` +
		`{"fullUrl":"https://payer.example/fhir/ClaimResponse/cr-held","resource":{"resourceType":"ClaimResponse","id":"cr-held",` +
		`"status":"active","use":"preauthorization","patient":{"reference":"Patient/MBR-COVERED"},"outcome":"queued",` +
		`"item":[{"itemSequence":1,"adjudication":[{"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted"}]},` +
		`"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[` +
		`{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode",` +
		`"valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A4","display":"Pended"}]}}]}]}]}]}},` +
		`{"fullUrl":"https://payer.example/fhir/Task/task-held","resource":{"resourceType":"Task","id":"task-held",` +
		`"identifier":[{"system":"https://payer.example/pa-request","value":"held-1"}],"status":"requested","intent":"order",` +
		`"code":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes","code":"attachment-request-questionnaire"}]},` +
		`"for":{"reference":"Patient/MBR-COVERED"},"input":[` +
		`{"type":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes","code":"payer-url"}]},"valueUrl":"https://payer.example/fhir"},` +
		`{"type":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes","code":"questionnaires-needed"}]},` +
		`"valueIdentifier":{"system":"https://payer.example/questionnaire","value":"home-oxygen"}}]}}]}`
}

// dvPendedWhenAsked is the same hold as the payer states it to an inquiry: the
// pended response Bundle inside the inquiry envelope.
func dvPendedWhenAsked() string { return dvInquiryEnvelope(dvPended()) }

// dvDenied is the formal denial, rationale included.
func dvDenied(rationale string) string {
	b, err := shnsdk.BuildDeniedResponse("Patient/MBR-UC08", "corr-fake", rationale, fixedClock())
	if err != nil {
		panic(err)
	}
	return string(b)
}

// dvDeniedNoRationale is a denial that surfaces NO payer rationale at all — neither a
// disposition nor a review-action display for shnsdk.ParseClaimResponse to fall back
// to. The mutation row that proves a row cannot pass a reasonless denial.
const dvDeniedNoRationale = `{"resourceType":"ClaimResponse","outcome":"complete","item":[{"adjudication":[{"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A2"}]}}]}]}]}]}`

// ---- the fake ingress ------------------------------------------------------

// dvIngress stands in for the Kit gateway child's Da Vinci ingress in front of the
// reference payer: CRD cards keyed on the order code, a one-Questionnaire package,
// and a PAS verdict keyed on the order code plus whether the bundle is an amendment
// (Claim.related) — the same two inputs the reference payer's families are decided
// on. card/verdict are the per-test mutation hooks.
type dvIngress struct {
	srv *httptest.Server

	card    func(code string) string             // "" ⇒ the family default
	verdict func(code string, amend bool) string // "" ⇒ the family default
	pkg     func(reqBody string) string          // "" ⇒ the payer default for this request
	// inquire answers the n-th inquiry (1-based) about an order code; "" ⇒ the
	// verdict hook's amended answer, then the payer default.
	inquire func(code string, n int) string

	mu            sync.Mutex
	crdBodies     []string
	pkgBodies     []string
	submitBodies  []string
	inquireBodies []string
	// sleeps are the waits a row asked for before each inquiry, in order — the
	// runner's Sleep is injected to record them and return at once.
	sleeps []time.Duration
}

// dvDefaultCard is the reference payer's coverage answer per family.
func dvDefaultCard(code string) string {
	switch code {
	case "E0250":
		return dvCardCovered
	case "L8000":
		return dvCardAuthNeeded
	case "E0424":
		return dvCardConditional
	case "J3490":
		return dvCardNotCovered
	}
	return ""
}

// dvDefaultVerdict is the reference payer's PAS answer per family: L8000 approves on
// the first submit, E0424 is held and resolves on the amendment, J3490 is denied.
func dvDefaultVerdict(code string, amend bool) string {
	if amend {
		return dvApproved("AUTH-2001")
	}
	switch code {
	case "L8000":
		return dvApproved("AUTH-2000")
	case "E0424":
		return dvPended()
	case "J3490":
		return dvDenied("Excluded service under the member's plan.")
	case "E0250":
		return dvApproved("AUTH-2002")
	}
	return ""
}

// dvDefaultPackage is the package answer each persona's payer really gives: the hosted
// reference payer (every families row) answers the Parameters wrapper; the bridging demo
// payer, which the MBR-BRIDGE-DEMO persona's Coverage routes to, answers a bare Bundle.
func dvDefaultPackage(reqBody string) string {
	if strings.Contains(reqBody, dvBridgeDemoMember) {
		return dvPackage
	}
	return dvPackageWrapped
}

// newDVIngress starts the fake ingress (and the /scenario/uc01 origination route the
// eligibility row drives on the main child).
func newDVIngress(t *testing.T) *dvIngress {
	t.Helper()
	ing := &dvIngress{}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Branch string `json:"branch"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Branch == "covered" {
			_, _ = w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
			return
		}
		_, _ = w.Write([]byte(`{"covered":false,"reason":"coverage terminated"}`))
	})

	mux.HandleFunc("POST /cds-services/shn-order-sign", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		ing.record(&ing.crdBodies, body)
		code := dvCRDOrderCode(t, body)
		card := ""
		if ing.card != nil {
			card = ing.card(code)
		}
		if card == "" {
			card = dvDefaultCard(code)
		}
		if card == "" {
			http.Error(w, `{"error":"no card for order code `+code+`"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(card))
	})

	mux.HandleFunc("POST /Questionnaire/$questionnaire-package", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		ing.record(&ing.pkgBodies, body)
		out := ""
		if ing.pkg != nil {
			out = ing.pkg(body)
		}
		if out == "" {
			out = dvDefaultPackage(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(out))
	})

	mux.HandleFunc("POST /Claim/$submit", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		ing.record(&ing.submitBodies, body)
		code, amend := dvSubmitOrder(t, body)
		out := ""
		if ing.verdict != nil {
			out = ing.verdict(code, amend)
		}
		if out == "" {
			out = dvDefaultVerdict(code, amend)
		}
		if out == "" {
			http.Error(w, `{"error":"no verdict for order code `+code+`"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(out))
	})

	// Claim/$inquire: the payer answers what it HOLDS for the authorization
	// right now, because a pended authorization resolves only when the requester
	// asks — the payer's answer to a $submit is its answer to THAT operation. The
	// held rows drive this (conformantContinuation) after the payer answers their
	// amendment "still held".
	//
	// The answer is INQUIRY-shaped, as the reference payer's is: a Parameters
	// whose responseBundle parameter holds the response Bundle (dvInquiryEnvelope),
	// its ClaimResponse stating the authorization number in the review action's
	// own number, with the inquiry's trace numbers echoed onto its items
	// (dvEchoItemTraceNumbers). The inquire hook's answer is sent as given, so a
	// rejection row controls the exact bytes; the verdict hook's amended answer
	// (a submit-shaped Bundle) is consulted, in the inquiry envelope, so a payer
	// that "never decides" holds on every inquiry too; the default is the
	// decision the reference payer's own timer has reached.
	mux.HandleFunc("POST /Claim/$inquire", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		ing.record(&ing.inquireBodies, body)
		n := len(ing.inquiries())
		code := dvInquiryOrder(t, body)
		out := ""
		if ing.inquire != nil {
			out = ing.inquire(code, n)
		}
		if out == "" && ing.verdict != nil {
			if held := ing.verdict(code, true); held != "" {
				out = dvInquiryEnvelope(held)
			}
		}
		if out == "" {
			out = dvApprovedWhenAsked("AUTH-2001")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(dvEchoItemTraceNumbers(t, out, body)))
	})

	ing.srv = httptest.NewServer(mux)
	t.Cleanup(ing.srv.Close)
	return ing
}

// dvInquiryOrder reads the product code an inquiry asks about — the Claim item's
// productOrService, which is the only place an inquiry states it (it carries no
// order resource).
func dvInquiryOrder(t *testing.T, body string) string {
	t.Helper()
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
				Item         []struct {
					ProductOrService struct {
						Coding []struct {
							Code string `json:"code"`
						} `json:"coding"`
					} `json:"productOrService"`
				} `json:"item"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Errorf("parse inquiry bundle: %v", err)
		return ""
	}
	for _, e := range b.Entry {
		if e.Resource.ResourceType != "Claim" {
			continue
		}
		for _, it := range e.Resource.Item {
			if len(it.ProductOrService.Coding) > 0 {
				return it.ProductOrService.Coding[0].Code
			}
		}
	}
	return ""
}

func (i *dvIngress) record(into *[]string, body string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	*into = append(*into, body)
}

func (i *dvIngress) snapshot(of *[]string) []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), (*of)...)
}

func (i *dvIngress) crds() []string      { return i.snapshot(&i.crdBodies) }
func (i *dvIngress) packages() []string  { return i.snapshot(&i.pkgBodies) }
func (i *dvIngress) submits() []string   { return i.snapshot(&i.submitBodies) }
func (i *dvIngress) inquiries() []string { return i.snapshot(&i.inquireBodies) }

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	return string(b)
}

// dvCRDOrderCode reads the order code off a CDS Hooks order-sign request
// (scenariodriver.BuildCRDRequest's context.draftOrders shape).
func dvCRDOrderCode(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Context struct {
			DraftOrders struct {
				Entry []struct {
					Resource struct {
						Code struct {
							Coding []struct {
								Code string `json:"code"`
							} `json:"coding"`
						} `json:"code"`
					} `json:"resource"`
				} `json:"entry"`
			} `json:"draftOrders"`
		} `json:"context"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Errorf("parse CRD request: %v", err)
		return ""
	}
	for _, e := range req.Context.DraftOrders.Entry {
		for _, c := range e.Resource.Code.Coding {
			if c.Code != "" {
				return c.Code
			}
		}
	}
	return ""
}

// dvSubmitOrder reads the order code off the submitted Claim Bundle's ServiceRequest
// (the same entry the reference payer's stand-in keys its families on) and reports
// whether the bundle is an amended re-submit (Claim.related).
func dvSubmitOrder(t *testing.T, body string) (code string, amend bool) {
	t.Helper()
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
				Related      []any  `json:"related"`
				Code         struct {
					Coding []struct {
						Code string `json:"code"`
					} `json:"coding"`
				} `json:"code"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Errorf("parse submit bundle: %v", err)
		return "", false
	}
	for _, e := range b.Entry {
		switch e.Resource.ResourceType {
		case "ServiceRequest":
			if code == "" && len(e.Resource.Code.Coding) > 0 {
				code = e.Resource.Code.Coding[0].Code
			}
		case "Claim":
			amend = amend || len(e.Resource.Related) > 0
		}
	}
	return code, amend
}

// ---- the fake br-provider BFF ----------------------------------------------

// dvBFF stands in for br-provider's BFF: the CRD origination endpoint and the real
// populate endpoint the Da Vinci rows use when the Java trio is present.
type dvBFF struct {
	srv *httptest.Server

	card func(code string) string

	mu          sync.Mutex
	crdHits     int
	populateHit int
}

func newDVBFF(t *testing.T) *dvBFF {
	t.Helper()
	b := &dvBFF{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/cds-services/shn-order-sign", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		b.mu.Lock()
		b.crdHits++
		b.mu.Unlock()
		code := dvCRDOrderCode(t, body)
		card := ""
		if b.card != nil {
			card = b.card(code)
		}
		if card == "" {
			card = dvDefaultCard(code)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(card))
	})
	mux.HandleFunc("POST /api/dtr/populate", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.populateHit++
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write([]byte(dvPopulatedQR))
	})
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

func (b *dvBFF) hits() (crd, populate int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.crdHits, b.populateHit
}

// ---- runner fixture --------------------------------------------------------

var (
	dvKeyOnce sync.Once
	dvKey     *rsa.PrivateKey
)

// dvTestKey is the direct-bearer signing key, generated once for the whole package
// (an RSA keygen per row would dominate this file's runtime).
func dvTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	dvKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		dvKey = k
	})
	return dvKey
}

// dvRunner builds a Runner whose Da Vinci ingress is ing; bff (may be nil) is the
// Java trio's br-provider.
func dvRunner(t *testing.T, ing *dvIngress, bff *dvBFF) *Runner {
	t.Helper()
	dcfg := scenariodriver.Config{
		IngressURL:      ing.srv.URL,
		IngressBase:     ing.srv.URL,
		ProviderDataURL: ing.srv.URL,
		ClientID:        "kit-runner-test",
		Key:             dvTestKey(t),
	}
	cfg := Config{Bus: event.NewBus(fixedClock)}
	// The held rows' follow-up wait runs on no clock here: every delay a row
	// asks for is recorded and returned at once, so the schedule itself is what
	// a test reads, not wall time.
	cfg.Sleep = func(_ context.Context, d time.Duration) error {
		ing.mu.Lock()
		defer ing.mu.Unlock()
		ing.sleeps = append(ing.sleeps, d)
		return nil
	}
	if bff != nil {
		dcfg.BFFURL = bff.srv.URL
		cfg.BFFURL = bff.srv.URL
	}
	cfg.Driver = scenariodriver.New(dcfg)
	return New(cfg)
}

// waited returns the delays a row asked for before each inquiry, in order.
func (i *dvIngress) waited() []time.Duration {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]time.Duration(nil), i.sleeps...)
}

// ---- the pass table --------------------------------------------------------

func TestConformantRows_Pass(t *testing.T) {
	for _, tc := range []struct {
		name, uc, branch string
		want             []string
	}{
		{"uc01 covered", "uc01", "covered", []string{"covered=true", "active coverage"}},
		{"uc01 notcovered", "uc01", "notcovered", []string{"covered=false", "coverage terminated"}},
		{"uc02", "uc02", "", []string{"E0250", "covered=covered", "no prior authorization (the card advertises no pa-needed)"}},
		{"uc03", "uc03", "", []string{"AUTH-2000", "approved by the reference payer"}},
		{"uc04", "uc04", "", []string{"held by the reference payer", "operative report", "AUTH-2001"}},
		{"uc05", "uc05", "", []string{"federated facility evidence", "AUTH-2001"}},
		{"uc06", "uc06", "", []string{"clinician-attested re-submit", "AUTH-2001"}},
		{"uc07", "uc07", "", []string{"AUTH-2000", "skipped"}},
		{"uc08", "uc08", "", []string{"denied", "Excluded service under the member's plan."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ing := newDVIngress(t)
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: tc.uc, Branch: tc.branch})
			if err != nil {
				t.Fatalf("Run(conformant/%s/%s): %v", tc.uc, tc.branch, err)
			}
			if res.State != StatePassed {
				t.Fatalf("state=%s, want passed (detail=%q)", res.State, res.Detail)
			}
			for _, want := range tc.want {
				if !strings.Contains(res.Detail, want) {
					t.Errorf("detail = %q, want it to contain %q", res.Detail, want)
				}
			}
			if strings.Contains(res.Detail, brProviderOriginatedPrefix) {
				t.Errorf("detail = %q, must NOT claim provider-system origination without the trio", res.Detail)
			}
		})
	}
}

// TestConformantRows_FamiliesOnTheWire pins WHICH family each row drives (the reference
// payer's behaviour is code-keyed, so the code is the row's real subject) and that the
// held rows resolve on a genuine amended re-submit, never a second plain submit.
func TestConformantRows_FamiliesOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		uc          string
		wantCode    string
		wantSubmits int
		wantAmend   bool
	}{
		{"uc02", "E0250", 0, false},
		{"uc03", "L8000", 1, false},
		{"uc04", "E0424", 2, true},
		{"uc05", "E0424", 2, true},
		{"uc06", "E0424", 2, true},
		{"uc08", "J3490", 1, false},
	} {
		t.Run(tc.uc, func(t *testing.T) {
			ing := newDVIngress(t)
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: tc.uc, Branch: ""})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StatePassed {
				t.Fatalf("state=%s, want passed (detail=%q)", res.State, res.Detail)
			}
			crds := ing.crds()
			if len(crds) != 1 {
				t.Fatalf("CRD legs = %d, want exactly 1", len(crds))
			}
			if got := dvCRDOrderCode(t, crds[0]); got != tc.wantCode {
				t.Errorf("CRD order code = %q, want %q", got, tc.wantCode)
			}
			submits := ing.submits()
			if len(submits) != tc.wantSubmits {
				t.Fatalf("PAS submits = %d, want %d", len(submits), tc.wantSubmits)
			}
			for n, body := range submits {
				code, amend := dvSubmitOrder(t, body)
				if code != tc.wantCode {
					t.Errorf("submit[%d] order code = %q, want %q", n, code, tc.wantCode)
				}
				if wantAmend := tc.wantAmend && n == len(submits)-1; amend != wantAmend {
					t.Errorf("submit[%d] amended=%v, want %v", n, amend, wantAmend)
				}
			}
		})
	}
}

// TestConformantUC06_FetchesTheOxygenQuestionnaireByCanonical: the E0424 order-sign card
// advertises NO questionnaire, so uc06 fetches the reference payer's HomeOxygen package
// BY CANONICAL — the shape the live gate proved.
func TestConformantUC06_FetchesTheOxygenQuestionnaireByCanonical(t *testing.T) {
	ing := newDVIngress(t)
	rn := dvRunner(t, ing, nil)
	res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: "uc06", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("state=%s, want passed (detail=%q)", res.State, res.Detail)
	}
	pkgs := ing.packages()
	if len(pkgs) != 1 {
		t.Fatalf("$questionnaire-package fetches = %d, want 1", len(pkgs))
	}
	if !strings.Contains(pkgs[0], homeOxygenCanonical) {
		t.Errorf("package request = %s, want it to fetch %q", pkgs[0], homeOxygenCanonical)
	}
	// The amendment that resolves the hold carries the attested answers AND the
	// Provenance that attests them.
	submits := ing.submits()
	if len(submits) != 2 {
		t.Fatalf("submits = %d, want 2", len(submits))
	}
	for _, want := range []string{"QuestionnaireResponse", "Provenance"} {
		if !strings.Contains(submits[1], want) {
			t.Errorf("amended re-submit carries no %s: %s", want, submits[1])
		}
	}
}

// TestConformantUC04UC05_AmendCarriesTheReport pins WHAT resolves the hold on the two
// evidence rows: the amended re-submit — and only the amended re-submit — carries the
// DiagnosticReport (uc04 the operative report the clinician wrote, uc05 the report a
// federated query retrieved from the facility) plus the Provenance that attests it.
// Without this the rows would pass on an amendment that changed nothing but the
// correlation, which is exactly the hold the reference payer does NOT resolve.
func TestConformantUC04UC05_AmendCarriesTheReport(t *testing.T) {
	for _, uc := range []string{"uc04", "uc05"} {
		t.Run(uc, func(t *testing.T) {
			ing := newDVIngress(t)
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: uc, Branch: ""})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StatePassed {
				t.Fatalf("state=%s, want passed (detail=%q)", res.State, res.Detail)
			}
			submits := ing.submits()
			if len(submits) != 2 {
				t.Fatalf("submits = %d, want 2", len(submits))
			}
			if strings.Contains(submits[0], `"resourceType":"DiagnosticReport"`) {
				t.Errorf("the FIRST submit already carries the report — the hold must be what the evidence answers: %s", submits[0])
			}
			for _, want := range []string{`"resourceType":"DiagnosticReport"`, "Provenance"} {
				if !strings.Contains(submits[1], want) {
					t.Errorf("amended re-submit carries no %s: %s", want, submits[1])
				}
			}
		})
	}
}

// ---- the mutation table ----------------------------------------------------

// TestConformantRows_Reject: a valid exchange with ONE fact mutated must fail, and the
// failure must name the fence it broke. The AUTH- rows are the anti-fallback fence —
// a run that quietly reached some other payer's PA-<hex> verdict can never pass.
func TestConformantRows_Reject(t *testing.T) {
	cardFor := func(code, card string) func(string) string {
		return func(got string) string {
			if got == code {
				return card
			}
			return ""
		}
	}
	verdictFor := func(code string, amend bool, out string) func(string, bool) string {
		return func(gotCode string, gotAmend bool) string {
			if gotCode == code && gotAmend == amend {
				return out
			}
			return ""
		}
	}
	packageIs := func(out string) func(string) string {
		return func(string) string { return out }
	}
	for _, tc := range []struct {
		name, uc, branch string
		card             func(string) string
		verdict          func(string, bool) string
		pkg              func(string) string
		wantErr          string
	}{
		{"uc03 non-reference verdict prefix", "uc03", "", nil, verdictFor("L8000", false, dvApproved("PA-deadbeef")), nil, "AUTH-"},
		// The reference payer's answer for uc03's family is "covered + prior
		// authorization required": BOTH halves are the row's subject, so both get
		// a mutation.
		{"uc03 coverage check says not covered", "uc03", "", cardFor("L8000", dvCardAuthNeededNotCovered), nil, nil, `want "covered"`},
		{"uc03 coverage check requires no prior authorization", "uc03", "", cardFor("L8000", dvCardCovered), nil, nil, `want "auth-needed"`},
		{"uc03 card advertises no questionnaire", "uc03", "", cardFor("L8000", dvCardAuthNeededNoQuestionnaire), nil, nil, "no questionnaire canonical"},
		{"uc04 first submit approved", "uc04", "", nil, verdictFor("E0424", false, dvApproved("AUTH-9")), nil, "not pended"},
		{"uc04 amend non-reference prefix", "uc04", "", nil, verdictFor("E0424", true, dvApproved("PA-1")), nil, "AUTH-"},
		{"uc05 first submit approved", "uc05", "", nil, verdictFor("E0424", false, dvApproved("AUTH-9")), nil, "not pended"},
		{"uc05 amend non-reference prefix", "uc05", "", nil, verdictFor("E0424", true, dvApproved("PA-1")), nil, "AUTH-"},
		{"uc06 first submit approved", "uc06", "", nil, verdictFor("E0424", false, dvApproved("AUTH-9")), nil, "not pended"},
		{"uc06 amend non-reference prefix", "uc06", "", nil, verdictFor("E0424", true, dvApproved("PA-1")), nil, "AUTH-"},
		// The E0424 family's coverage answer is CONDITIONAL on all three rows that
		// drive it — a covered/no-auth answer means the payer decided the request
		// at the coverage check, which is not what these rows exercise.
		{"uc04 coverage check not conditional", "uc04", "", cardFor("E0424", dvCardCovered), nil, nil, `want "conditional"`},
		{"uc05 coverage check not conditional", "uc05", "", cardFor("E0424", dvCardCovered), nil, nil, `want "conditional"`},
		{"uc06 coverage check not conditional", "uc06", "", cardFor("E0424", dvCardCovered), nil, nil, `want "conditional"`},
		{"uc07 non-reference verdict prefix", "uc07", "", nil, verdictFor("L8000", false, dvApproved("PA-1")), nil, "AUTH-"},
		{"uc08 approved", "uc08", "", nil, verdictFor("J3490", false, dvApproved("AUTH-9")), nil, "want denied"},
		{"uc08 held", "uc08", "", nil, verdictFor("J3490", false, dvPended()), nil, "want denied"},
		{"uc08 denied without a rationale", "uc08", "", nil, verdictFor("J3490", false, dvDeniedNoRationale), nil, "rationale"},
		{"uc08 coverage check says covered", "uc08", "", cardFor("J3490", dvCardCovered), nil, nil, "not-covered"},
		// The DTR package fence, on BOTH wire shapes. The reference payer answers a
		// Parameters wrapper; a wrapper whose packagebundle Bundle carries no
		// Questionnaire is a package the row cannot fill, and a bare Bundle without one
		// is the same failure on the demo-payer shape. Neither may pass.
		{"uc03 package wrapper carries no Questionnaire", "uc03", "", nil, nil, packageIs(dvPackageWrappedNoQuestionnaire), "no Questionnaire"},
		{"uc03 bare package carries no Questionnaire", "uc03", "", nil, nil, packageIs(dvPackageBareNoQuestionnaire), "no Questionnaire"},
		{"uc06 package wrapper carries no Questionnaire", "uc06", "", nil, nil, packageIs(dvPackageWrappedNoQuestionnaire), "no Questionnaire"},
		{"uc06 bare package carries no Questionnaire", "uc06", "", nil, nil, packageIs(dvPackageBareNoQuestionnaire), "no Questionnaire"},
		// uc02's fence refuses ONE value — a card that demands prior authorization. The
		// tolerated shapes (pa-needed absent, pa-needed "no-auth") are pinned as PASSES by
		// TestConformantUC02_ToleratesBothNoPAShapes.
		{"uc02 coverage check demands prior authorization", "uc02", "", cardFor("E0250", dvCardAuthNeeded), nil, nil, `want anything but "auth-needed"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ing := newDVIngress(t)
			ing.card, ing.verdict, ing.pkg = tc.card, tc.verdict, tc.pkg
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: tc.uc, Branch: tc.branch})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StateFailed || !strings.Contains(res.Detail, tc.wantErr) {
				t.Errorf("state=%s detail=%q, want failed naming %q", res.State, res.Detail, tc.wantErr)
			}
		})
	}
}

// TestConformantUC02_ToleratesBothNoPAShapes pins uc02's fence as `!= auth-needed`, the
// two-RI pin's shape — NOT `== no-auth`, the stand-in's. A conformant payer may say "no
// prior authorization" either by OMITTING pa-needed (what the real reference payer does:
// test/tworilive/origination_test.go TestTwoRI_BRP_DVNoPA) or by spelling out "no-auth";
// both must pass, and the row detail must report what the card actually said rather than
// a value the Kit assumed. This is the regression test for the v0.15.1 fresh-machine
// failure (`paNeeded="", want "no-auth"`): restore `== shnsdk.PANeededNoAuth` in
// conformantUC02 and the omitted-pa-needed case below goes red with exactly that message.
func TestConformantUC02_ToleratesBothNoPAShapes(t *testing.T) {
	for _, tc := range []struct {
		name, card, wantDetail string
	}{
		{"pa-needed omitted (the live reference-payer shape)", dvCardCovered, "no prior authorization (the card advertises no pa-needed)"},
		{"pa-needed spelled out as no-auth", dvCardCoveredNoAuth, "no prior authorization (paNeeded=no-auth)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ing := newDVIngress(t)
			ing.card = func(code string) string {
				if code == "E0250" {
					return tc.card
				}
				return ""
			}
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: "uc02", Branch: ""})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StatePassed {
				t.Fatalf("state=%s, want passed (detail=%q)", res.State, res.Detail)
			}
			if !strings.Contains(res.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", res.Detail, tc.wantDetail)
			}
			if strings.Contains(res.Detail, "paNeeded=no-auth") != (tc.card == dvCardCoveredNoAuth) {
				t.Errorf("detail = %q must print the payer's OWN pa-needed value, never one the Kit assumed", res.Detail)
			}
		})
	}
}

// ---- the BFF (Java trio) path ----------------------------------------------

// TestConformantUC03_UnderBFF: with the trio present the uc03 CRD leg originates through
// br-provider's real BFF and the questionnaire is filled by br-provider's real populate —
// the ingress CRD endpoint is never touched, and the answers it produced ride the submit.
func TestConformantUC03_UnderBFF(t *testing.T) {
	ing := newDVIngress(t)
	bff := newDVBFF(t)
	rn := dvRunner(t, ing, bff)

	res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: "uc03", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("state=%s, want passed (detail=%q)", res.State, res.Detail)
	}
	if !strings.Contains(res.Detail, brProviderOriginatedPrefix) {
		t.Errorf("detail = %q, want the provider-system origination line %q", res.Detail, brProviderOriginatedPrefix)
	}
	crdHits, populateHits := bff.hits()
	if crdHits != 1 {
		t.Errorf("BFF CRD hits = %d, want 1", crdHits)
	}
	if populateHits != 1 {
		t.Errorf("BFF populate hits = %d, want 1 (the questionnaire must be filled by the provider system)", populateHits)
	}
	if got := len(ing.crds()); got != 0 {
		t.Errorf("ingress CRD hits = %d, want 0 (the trio originates through the BFF)", got)
	}
	submits := ing.submits()
	if len(submits) != 1 {
		t.Fatalf("submits = %d, want 1", len(submits))
	}
	if !strings.Contains(submits[0], `"QuestionnaireResponse"`) || !strings.Contains(submits[0], "populated by the provider system") {
		t.Errorf("submit carries no provider-system-populated QuestionnaireResponse: %s", submits[0])
	}
}

// TestConformantUC05_UnderBFF_StaysDirectMint is the table's negative side: a UC with NO
// conformantBRPScenario entry keeps its own direct-mint coverage check even with the trio
// present, and claims no provider-system provenance. Falling into the BFF path by accident
// is exactly what that table exists to prevent.
func TestConformantUC05_UnderBFF_StaysDirectMint(t *testing.T) {
	ing := newDVIngress(t)
	bff := newDVBFF(t)
	rn := dvRunner(t, ing, bff)

	res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: "uc05", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("state=%s, want passed (detail=%q)", res.State, res.Detail)
	}
	crdHits, populateHits := bff.hits()
	if crdHits != 0 || populateHits != 0 {
		t.Errorf("BFF hits = crd:%d populate:%d, want 0/0 (uc05 has no entry in conformantBRPScenario)", crdHits, populateHits)
	}
	if got := len(ing.crds()); got != 1 {
		t.Errorf("ingress CRD hits = %d, want 1 (the direct-mint coverage check)", got)
	}
	if strings.Contains(res.Detail, brProviderOriginatedPrefix) {
		t.Errorf("detail = %q, must NOT claim provider-system origination", res.Detail)
	}
}

// TestConformantUC08_UnderBFF: the coverage check rides br-provider's BFF (not-covered),
// and the formal denial still comes off the reference payer through the Kit's own PAS
// ingress (the BFF has no PAS leg — see gateway/scenariodriver/brprovider.go's Gap A).
func TestConformantUC08_UnderBFF(t *testing.T) {
	ing := newDVIngress(t)
	bff := newDVBFF(t)
	rn := dvRunner(t, ing, bff)

	res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: "uc08", Branch: ""})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("state=%s, want passed (detail=%q)", res.State, res.Detail)
	}
	if !strings.Contains(res.Detail, brProviderOriginatedPrefix) {
		t.Errorf("detail = %q, want the provider-system origination line", res.Detail)
	}
	if !strings.Contains(res.Detail, "denied") {
		t.Errorf("detail = %q, want the denial", res.Detail)
	}
	crdHits, _ := bff.hits()
	if crdHits != 1 {
		t.Errorf("BFF CRD hits = %d, want 1", crdHits)
	}
	if got := len(ing.crds()); got != 0 {
		t.Errorf("ingress CRD hits = %d, want 0", got)
	}
	if got := len(ing.submits()); got != 1 {
		t.Errorf("ingress PAS submits = %d, want 1 (the denial comes through the Kit's own PAS ingress)", got)
	}
}

// ---- the package-shape helper ----------------------------------------------

// TestPackageHasQuestionnaire is the fence at unit grain, over the four shapes that reach
// it on the wire. The reference payer's Parameters wrapper is the shape the live v0.15.0
// packaging smoke tripped on; the bare Bundle is the bridging demo payer's (and SHN's own
// provider-data) answer. Both must be read, and neither may pass without a Questionnaire.
func TestPackageHasQuestionnaire(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"reference payer's Parameters wrapper with a Questionnaire", dvPackageWrapped, true},
		{"Parameters wrapper whose Bundle has no Questionnaire", dvPackageWrappedNoQuestionnaire, false},
		{"bare collection Bundle with a Questionnaire", dvPackage, true},
		{"bare collection Bundle with no Questionnaire", dvPackageBareNoQuestionnaire, false},
		{"Parameters with no packagebundle parameter", `{"resourceType":"Parameters","parameter":[{"name":"outcome","resource":{"resourceType":"OperationOutcome"}}]}`, false},
		{"Parameters whose packagebundle is not a Bundle", `{"resourceType":"Parameters","parameter":[{"name":"packagebundle","resource":{"resourceType":"Questionnaire","status":"active"}}]}`, false},
		{"empty body", "", false},
		{"malformed body", "{not json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := packageHasQuestionnaire([]byte(tc.body)); got != tc.want {
				t.Errorf("packageHasQuestionnaire(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// ---- conformantSubmitBundle payor parameterization -------------------------

// TestConformantSubmitBundle_PayorParameterized is the misroute rejection test the
// bridge-demo submit row's move onto conformantSubmitBundle depends on:
// conformantSubmitBundle used to hand the assembled bundle to
// scenariodriver.AddRoutablePayor, which unconditionally stamped CMS regardless of the
// payer argument — so a bridge-demo submit would silently route to the reference
// payer's own holder instead of the bridge payer named by the run's own CRD leg. It
// now uses scenariodriver.AddRoutablePayorFor(b, payer), so the inline
// Coverage.payor[0].identifier must be the PASSED payer.
//
// It also pins the shape the reference payer itself enforces (first bundle entry must
// be a Claim) and that an ordinary CMS call is unaffected: it still carries the CMS
// identifier inline, byte-for-byte what AddRoutablePayor always produced.
func TestConformantSubmitBundle_PayorParameterized(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	const member = "MBR-BRIDGE-DEMO"
	bridgePayor := shnsdk.PayerIdentifier{System: "urn:shn:demo-payer", Value: "SHN-BRIDGE-DEMO"}

	srJSON, err := buildOrderServiceRequest(scenariodriver.SystemHCPCS, "L8000", "Breast prosthesis, mastectomy bra", "M51.16", "Patient/"+member)
	if err != nil {
		t.Fatalf("build order ServiceRequest: %v", err)
	}
	qrJSON, err := attestedQR(l8000Canonical, member, now)
	if err != nil {
		t.Fatalf("attestedQR: %v", err)
	}

	bundle, err := conformantSubmitBundle(member, bridgePayor, srJSON, qrJSON, "kit-uc03-bridge-submit-test", now)
	if err != nil {
		t.Fatalf("conformantSubmitBundle: %v", err)
	}

	var b map[string]any
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	entries, _ := b["entry"].([]any)
	if len(entries) == 0 {
		t.Fatal("bundle has no entries")
	}
	// The shape a real Da Vinci PAS payer enforces (the exact failure the hand-assembled
	// bridging bundle this replaces used to trip: "First bundle entry must be a Claim,
	// got: Patient").
	first, _ := entries[0].(map[string]any)["resource"].(map[string]any)
	if first == nil || first["resourceType"] != "Claim" {
		t.Fatalf("entry[0].resource.resourceType = %v, want Claim", first["resourceType"])
	}

	var coveragePayor shnsdk.PayerIdentifier
	found := false
	for _, e := range entries {
		res, _ := e.(map[string]any)["resource"].(map[string]any)
		if res == nil || res["resourceType"] != "Coverage" {
			continue
		}
		payors, _ := res["payor"].([]any)
		if len(payors) == 0 {
			t.Fatal("Coverage has no payor entries")
		}
		p0, _ := payors[0].(map[string]any)
		ident, _ := p0["identifier"].(map[string]any)
		if ident == nil {
			t.Fatal("Coverage.payor[0] has no inline identifier")
		}
		sys, _ := ident["system"].(string)
		val, _ := ident["value"].(string)
		coveragePayor = shnsdk.PayerIdentifier{System: sys, Value: val}
		found = true
	}
	if !found {
		t.Fatal("bundle has no Coverage entry")
	}
	if coveragePayor != bridgePayor {
		t.Fatalf("Coverage.payor[0].identifier = %+v, want %+v (the misroute: a hardcoded CMS stamp would have produced %+v)",
			coveragePayor, bridgePayor, shnsdk.CMSPayerIdentity)
	}

	// Control row: an ordinary CMS call is untouched — still carries the CMS identifier
	// inline, byte-stable vs. the pre-parameterization AddRoutablePayor behavior.
	cmsBundle, err := conformantSubmitBundle(member, shnsdk.CMSPayerIdentity, srJSON, qrJSON, "kit-uc03-cms-submit-test", now)
	if err != nil {
		t.Fatalf("conformantSubmitBundle (CMS): %v", err)
	}
	if !bytes.Contains(cmsBundle, []byte(`"`+shnsdk.CMSPayerIdentity.System+`"`)) ||
		!bytes.Contains(cmsBundle, []byte(`"`+shnsdk.CMSPayerIdentity.Value+`"`)) {
		t.Fatalf("CMS call must still carry the CMS identifier inline: %s", cmsBundle)
	}
}

func TestConformantEvidenceUsesKnownHolderIdentity(t *testing.T) {
	for _, uc := range []string{"uc04", "uc05", "uc06"} {
		t.Run(uc, func(t *testing.T) {
			ing := newDVIngress(t)
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: uc})
			if err != nil || res.State != StatePassed {
				t.Fatalf("run=%v %v", res, err)
			}
			submits := ing.submits()
			if len(submits) != 2 {
				t.Fatalf("submits=%d", len(submits))
			}
			var b struct {
				Entry []struct {
					Resource struct {
						ResourceType string
						Agent        []struct {
							Who struct {
								Reference  string
								Identifier struct{ System, Value string }
							}
						}
						Policy []string
						Reason []struct {
							Coding []struct{ System, Code string }
						}
					}
				}
			}
			if err = json.Unmarshal([]byte(submits[1]), &b); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range b.Entry {
				p := e.Resource
				if p.ResourceType != "Provenance" {
					continue
				}
				found = true
				want := "provider"
				if uc == "uc05" {
					want = "metro-spine"
				}
				if len(p.Agent) != 1 || p.Agent[0].Who.Reference != "" || p.Agent[0].Who.Identifier.System != "http://smarthealth.network/ids/holder" || p.Agent[0].Who.Identifier.Value != want {
					t.Fatalf("wrong provenance source: %+v", p)
				}
				if uc == "uc05" && (len(p.Policy) != 1 || p.Policy[0] != "Consent/uc05-treat" || len(p.Reason) != 1 || len(p.Reason[0].Coding) != 1 || p.Reason[0].Coding[0].Code != "TREAT") {
					t.Fatalf("facility consent attribution lost: %+v", p)
				}
			}
			if !found {
				t.Fatal("missing provenance")
			}
		})
	}
}

// TestConformantRows_StillHeldIsReportedNotInvented: when the payer goes on holding a
// request through every inquiry the row makes, the row says so — and says nothing
// else.
//
// This replaces three rows that used to require the row to FAIL here ("not approved").
// It failed for the wrong reason. A payer that has not decided has not failed, and it is
// what a partner integrating against a live payer sees most of the time: real payers hold
// requests for hours or days. A row that could only end "approved" taught the opposite and
// would have gone red on the first slow payer.
//
// So the rejection this ships is the one that matters, and it is stricter than the old
// one: the row must ASK, on the bounded schedule the shipped client uses and not one
// inquiry past it, must report the payer's state as it is, must name the continuation it
// holds, and must NOT state an authorization number — there isn't one. A row that
// invented an authorization, quietly reported the pend as a decision, or kept asking
// past the bound, fails here.
func TestConformantRows_StillHeldIsReportedNotInvented(t *testing.T) {
	for _, uc := range []string{"uc04", "uc05", "uc06"} {
		t.Run(uc+" the payer never decides", func(t *testing.T) {
			ing := newDVIngress(t)
			// The payer answers "still held" to the amendment AND to every inquiry
			// after it — a payer that has not made up its mind.
			ing.verdict = func(code string, amend bool) string {
				if code == "E0424" && amend {
					return dvPended()
				}
				return ""
			}
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: uc})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StatePassed {
				t.Fatalf("state=%s detail=%q — a payer that has not decided has not failed", res.State, res.Detail)
			}
			if !strings.Contains(res.Detail, "still held by the payer") {
				t.Errorf("detail %q does not report that the payer is still holding the request", res.Detail)
			}
			if !strings.Contains(res.Detail, "continuation is recorded") {
				t.Errorf("detail %q does not name the continuation the requester keeps", res.Detail)
			}
			// The row asked, on the shipped client's schedule, and stopped at the
			// bound: 2, 4, 5, 5, 5, 5 seconds — six inquiries, the last due at 26 s.
			wantWaits := []time.Duration{2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second}
			if got := ing.waited(); !equalDurations(got, wantWaits) {
				t.Errorf("waits before each inquiry = %v, want %v", got, wantWaits)
			}
			if got := len(ing.inquiries()); got != shnsdk.MaxPriorAuthInquiries {
				t.Errorf("the row made %d inquiries, want the bound %d", got, shnsdk.MaxPriorAuthInquiries)
			}
			if !strings.Contains(res.Detail, "6 inquiries") {
				t.Errorf("detail %q does not say how many times the payer was asked", res.Detail)
			}
			if strings.Contains(res.Detail, "AUTH-") || strings.Contains(res.Detail, "approved") {
				t.Errorf("detail %q states an authorization the payer never gave", res.Detail)
			}
		})
	}
}

// TestConformantRows_TheWaitBoundStopsTheAsking: the schedule is bounded twice — by
// the inquiry count and by the wait — and the hermetic rows above, run on a clock
// that never moves, can only reach the first. This one runs on a clock that moves:
// every wait advances it by the delay asked for, and the payer takes 5 s to answer
// each inquiry, so the sixth inquiry would fall due after the 30 s bound. The row
// must stop at the last inquiry that still fits — the third, due at 21 s and
// answered at 26 s; the fourth would fall due at 31 s — and say how many it made.
// A row that counted inquiries but never looked at the clock keeps asking after
// the bound the shipped client promises a payer, and fails here.
func TestConformantRows_TheWaitBoundStopsTheAsking(t *testing.T) {
	const answerTakes = 5 * time.Second
	ing := newDVIngress(t)
	ing.verdict = func(code string, amend bool) string {
		if code == "E0424" && amend {
			return dvPended()
		}
		return ""
	}
	clock := &dvClock{now: fixedClock()}
	ing.inquire = func(string, int) string {
		clock.advance(answerTakes)
		return dvPendedWhenAsked()
	}
	rn := dvRunner(t, ing, nil)
	rn.cfg.Now = clock.Now
	rn.cfg.Sleep = func(_ context.Context, d time.Duration) error {
		ing.mu.Lock()
		ing.sleeps = append(ing.sleeps, d)
		ing.mu.Unlock()
		clock.advance(d)
		return nil
	}
	res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: "uc04"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != StatePassed {
		t.Fatalf("state=%s detail=%q — a payer that has not decided has not failed", res.State, res.Detail)
	}
	if got := len(ing.inquiries()); got != 3 {
		t.Errorf("the row made %d inquiries, want 3 — the last that falls due inside the %v bound on this clock", got, shnsdk.MaxPriorAuthWait)
	}
	if got, want := ing.waited(), []time.Duration{2 * time.Second, 4 * time.Second, 5 * time.Second}; !equalDurations(got, want) {
		t.Errorf("waits before each inquiry = %v, want %v", got, want)
	}
	if elapsed := clock.Now().Sub(fixedClock()); elapsed != 26*time.Second {
		t.Errorf("the row's clock ran %v, want 26s (2+5, 4+5, 5+5)", elapsed)
	}
	for _, want := range []string{"still held by the payer", "continuation is recorded", "3 inquiries over 26s"} {
		if !strings.Contains(res.Detail, want) {
			t.Errorf("detail %q does not contain %q", res.Detail, want)
		}
	}
}

// dvClock is a clock a row's own waits and its payer's answers move forward.
type dvClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *dvClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *dvClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestConformantRows_DecidedWhenAsked: the payer holds the amendment and decides on
// its own timer; the row learns the decision by ASKING and reports the payer's own
// authorization number, read the way the shipped client reads an inquiry answer.
//
// The decision arrives on the second inquiry here, as it does against the reference
// payer (its timer resolves before the row's second ask falls due), so the row is
// also pinned to have asked exactly twice and waited the schedule's first two delays.
func TestConformantRows_DecidedWhenAsked(t *testing.T) {
	for _, uc := range []string{"uc04", "uc05", "uc06"} {
		t.Run(uc, func(t *testing.T) {
			ing := newDVIngress(t)
			ing.verdict = func(code string, amend bool) string {
				if code == "E0424" && amend {
					return dvPended()
				}
				return ""
			}
			ing.inquire = func(code string, n int) string {
				if n < 2 {
					return dvPendedWhenAsked()
				}
				return dvApprovedWhenAsked("AUTH-2003")
			}
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: uc})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StatePassed {
				t.Fatalf("state=%s detail=%q", res.State, res.Detail)
			}
			for _, want := range []string{"approved when asked, auth AUTH-2003", "2 inquiries"} {
				if !strings.Contains(res.Detail, want) {
					t.Errorf("detail %q does not contain %q", res.Detail, want)
				}
			}
			if strings.Contains(res.Detail, "still held") {
				t.Errorf("detail %q reports a hold the payer has ended", res.Detail)
			}
			if got := ing.inquiries(); len(got) != 2 {
				t.Fatalf("the row made %d inquiries, want 2", len(got))
			}
			if got, want := ing.waited(), []time.Duration{2 * time.Second, 4 * time.Second}; !equalDurations(got, want) {
				t.Errorf("waits before each inquiry = %v, want %v", got, want)
			}
			// Each inquiry is its own exchange: it names the payer it is routed to,
			// the member asked about, the request lines by their trace numbers, and
			// an inquiry identifier of its own that the other inquiry does not share.
			ids := map[string]bool{}
			for i, inq := range ing.inquiries() {
				if !strings.Contains(inq, `"`+string(shnsdk.CMSPayerIdentity.Value)+`"`) {
					t.Errorf("inquiry %d does not name the payer it is sent to", i+1)
				}
				if len(dvInquiryTraceNumbers(t, inq)) == 0 {
					t.Errorf("inquiry %d names no request line to ask about", i+1)
				}
				id := dvInquiryIdentifier(t, inq)
				if id == "" || ids[id] {
					t.Errorf("inquiry %d carries identifier %q, want one of its own", i+1, id)
				}
				ids[id] = true
			}
		})
	}
}

// dvInquiryIdentifier reads the inquiry Claim's own identifier value.
func dvInquiryIdentifier(t *testing.T, inquiry string) string {
	t.Helper()
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
				Identifier   []struct {
					System string `json:"system"`
					Value  string `json:"value"`
				} `json:"identifier"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if json.Unmarshal([]byte(inquiry), &b) != nil {
		return ""
	}
	for _, e := range b.Entry {
		if e.Resource.ResourceType != "Claim" {
			continue
		}
		for _, id := range e.Resource.Identifier {
			if id.System == shnsdk.PASInquiryIdentifierSystem {
				return id.Value
			}
		}
	}
	return ""
}

// TestConformantRows_AskRejects: the ask's own fences. Each row is the decided-when-
// asked exchange above with ONE fact mutated, and each must fail naming the fence it
// broke — never pass by reading the first answer it found, by reading an inquiry
// answer with the submit reader, or by accepting a number that is not the reference
// payer's.
func TestConformantRows_AskRejects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		sleep   func(context.Context, time.Duration) error
		wantErr string
	}{
		// The reference payer's own answer to an inquiry that matches nothing it
		// holds — the captured empty Parameters — is not a decision: the fence is the
		// SDK reader's own (shnsdk.ErrInquiryNoMatch), asserted by its own words so
		// the row cannot pass on a different refusal wearing the runner's wrapper.
		{"the payer holds nothing for this inquiry", dvInquiryNoMatch, nil, shnsdk.ErrInquiryNoMatch.Error()},
		// An answer that names no request line of the inquiry's is an answer about
		// some other authorization: matching nothing is not answered, and the row must
		// not take the decision it carries. Same fence, reached past a ClaimResponse
		// the row could have read had it taken the first one it found.
		{"answer is about no request of this inquiry's", dvInquiryAnswerNoLines, nil, shnsdk.ErrInquiryNoMatch.Error()},
		// A bare ClaimResponse is the submit answer's shape, not an inquiry answer's:
		// the inquiry reader refuses it rather than the submit reader accepting it.
		{"answer is not inquiry-shaped", dvApproved("AUTH-2003"), nil, "not a Bundle or Parameters"},
		{"approved with a number that is not the reference payer's", dvApprovedWhenAsked("PA-1"), nil, "not the reference payer's AUTH-NNNN"},
		{"denied when asked", dvDeniedWhenAsked("Oxygen saturation criteria not met."), nil, `answered "denied": Oxygen saturation criteria not met.`},
		// The wait follows the run: a cancelled run makes no further inquiry.
		{"the wait is cancelled", "", func(context.Context, time.Duration) error { return context.Canceled }, "wait before inquiry: context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ing := newDVIngress(t)
			ing.verdict = func(code string, amend bool) string {
				if code == "E0424" && amend {
					return dvPended()
				}
				return ""
			}
			ing.inquire = func(string, int) string { return tc.answer }
			rn := dvRunner(t, ing, nil)
			if tc.sleep != nil {
				rn.cfg.Sleep = tc.sleep
			}
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: "uc04"})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StateFailed {
				t.Fatalf("state=%s detail=%q, want failed", res.State, res.Detail)
			}
			if !strings.Contains(res.Detail, tc.wantErr) {
				t.Errorf("detail %q does not name the fence %q", res.Detail, tc.wantErr)
			}
			if strings.Contains(res.Detail, "approved when asked") {
				t.Errorf("detail %q reports an approval the fence refused", res.Detail)
			}
			if tc.sleep != nil && len(ing.inquiries()) != 0 {
				t.Errorf("a cancelled wait still made %d inquiries", len(ing.inquiries()))
			}
		})
	}
}

// dvEchoItemTraceNumbers stamps the inquiry's own item trace numbers onto the
// ClaimResponse items of the answer, which is what the reference payer does.
//
// This is not decoration. A requester asks a payer about ONE authorization and
// gets back every authorization the payer holds for the parties it named; the
// trace number the requester itself sent is how it tells which of the answers is
// about its request (shnsdk.InquiryDecision selects on it). The reference payer
// echoes them on every answer it gives — the pend, the re-pend and the resolution
// alike, live-captured — so a stand-in that did not echo them would answer an
// inquiry nothing could read, and a row driving it would either fail where the
// real payer succeeds or, worse, pass by reading the first response it found.
func dvEchoItemTraceNumbers(t *testing.T, answer, inquiry string) string {
	t.Helper()
	traces := dvInquiryTraceNumbers(t, inquiry)
	if len(traces) == 0 {
		return answer
	}
	var doc any
	if json.Unmarshal([]byte(answer), &doc) != nil {
		return answer
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["resourceType"] == "ClaimResponse" {
				items, _ := x["item"].([]any)
				for i, raw := range items {
					it, ok := raw.(map[string]any)
					if !ok || i >= len(traces) {
						continue
					}
					exts, _ := it["extension"].([]any)
					it["extension"] = append(exts, map[string]any{
						"url":             "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-itemTraceNumber",
						"valueIdentifier": traces[i],
					})
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("dvEchoItemTraceNumbers: %v", err)
	}
	return string(out)
}

// dvInquiryTraceNumbers reads the item trace numbers an inquiry states, in item order.
func dvInquiryTraceNumbers(t *testing.T, inquiry string) []map[string]any {
	t.Helper()
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
				Item         []struct {
					Extension []struct {
						URL             string         `json:"url"`
						ValueIdentifier map[string]any `json:"valueIdentifier"`
					} `json:"extension"`
				} `json:"item"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if json.Unmarshal([]byte(inquiry), &b) != nil {
		return nil
	}
	for _, e := range b.Entry {
		if e.Resource.ResourceType != "Claim" {
			continue
		}
		var out []map[string]any
		for _, it := range e.Resource.Item {
			var found map[string]any
			for _, x := range it.Extension {
				if x.URL == "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-itemTraceNumber" {
					found = x.ValueIdentifier
				}
			}
			out = append(out, found)
		}
		return out
	}
	return nil
}
