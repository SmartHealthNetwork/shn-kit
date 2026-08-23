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
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// ---- canned reference-payer answers ----------------------------------------

// The CRD cards, in the exact shape scenariodriver.ParseCards reads
// (gateway/scenariodriver/cards.go — extension{covered,paNeeded,questionnaires}).
// Which card each family gets is the reference payer's own answer, mirrored:
// E0250 covered/no-auth, L8000 covered/auth-needed + a questionnaire canonical,
// E0424 CONDITIONAL with NO questionnaire advertised, J3490 not-covered.
const (
	dvCardCovered     = `{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`
	dvCardAuthNeeded  = `{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"covered","paNeeded":"auth-needed","questionnaires":["` + shnsdk.QuestionnaireCanonicalLumbarMRI + `"]}}]}`
	dvCardConditional = `{"cards":[{"summary":"Coverage is conditional","indicator":"warning","extension":{"covered":"conditional","paNeeded":"no-auth"}}]}`
	dvCardNotCovered  = `{"cards":[{"summary":"Not covered under the member's plan","indicator":"warning","extension":{"covered":"not-covered"}}]}`

	// dvCardAuthNeededNotCovered is dvCardAuthNeeded with ONE fact mutated: the
	// coverage answer. The prior-authorization answer and the questionnaire
	// canonical are untouched, so a row that only reads paNeeded would sail
	// straight past it.
	dvCardAuthNeededNotCovered = `{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"not-covered","paNeeded":"auth-needed","questionnaires":["` + shnsdk.QuestionnaireCanonicalLumbarMRI + `"]}}]}`

	// dvCardAuthNeededNoQuestionnaire is dvCardAuthNeeded with ONE fact
	// mutated: the questionnaire canonical is gone. Coverage and the
	// prior-authorization answer still say "fetch a questionnaire and submit".
	dvCardAuthNeededNoQuestionnaire = `{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"covered","paNeeded":"auth-needed"}}]}`
)

// dvPackage is a $questionnaire-package response Bundle carrying one Questionnaire —
// enough for bundleHasQuestionnaire and for br-provider's populate to have something
// to fill.
const dvPackage = `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Questionnaire","id":"pkg-q","status":"active","url":"` + homeOxygenCanonical + `"}}]}`

// dvPopulatedQR is what the fake br-provider populate endpoint answers with.
const dvPopulatedQR = `{"resourceType":"QuestionnaireResponse","id":"populated","status":"completed","questionnaire":"` + homeOxygenCanonical + `","subject":{"reference":"Patient/MBR-COVERED"},"item":[{"linkId":"1","answer":[{"valueString":"populated by the provider system"}]}]}`

// dvApproved is the reference payer's approved ClaimResponse with the given
// authorization reference.
func dvApproved(preAuthRef string) string {
	return `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"` + preAuthRef + `"}`
}

// dvPended is the held (A4) response shape, built by the sdk's own producer.
func dvPended() string {
	b, err := shnsdk.BuildPendedResponse("Patient/MBR-COVERED", "corr-fake", []string{"pend-resolution-timer"}, fixedClock())
	if err != nil {
		panic(err)
	}
	return string(b)
}

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

	mu           sync.Mutex
	crdBodies    []string
	pkgBodies    []string
	submitBodies []string
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

	mux.HandleFunc("POST /cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
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
		ing.record(&ing.pkgBodies, readBody(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(dvPackage))
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

	ing.srv = httptest.NewServer(mux)
	t.Cleanup(ing.srv.Close)
	return ing
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

func (i *dvIngress) crds() []string     { return i.snapshot(&i.crdBodies) }
func (i *dvIngress) packages() []string { return i.snapshot(&i.pkgBodies) }
func (i *dvIngress) submits() []string  { return i.snapshot(&i.submitBodies) }

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
	mux.HandleFunc("POST /api/cds-services/order-select-crd", func(w http.ResponseWriter, r *http.Request) {
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
	if bff != nil {
		dcfg.BFFURL = bff.srv.URL
		cfg.BFFURL = bff.srv.URL
	}
	cfg.Driver = scenariodriver.New(dcfg)
	return New(cfg)
}

// ---- the pass table --------------------------------------------------------

func TestConformantRows_Pass(t *testing.T) {
	for _, tc := range []struct {
		name, uc, branch string
		want             []string
	}{
		{"uc01 covered", "uc01", "covered", []string{"covered=true", "active coverage"}},
		{"uc01 notcovered", "uc01", "notcovered", []string{"covered=false", "coverage terminated"}},
		{"uc02", "uc02", "", []string{"E0250", "covered=covered", "paNeeded=no-auth"}},
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
	for _, tc := range []struct {
		name, uc, branch string
		card             func(string) string
		verdict          func(string, bool) string
		wantErr          string
	}{
		{"uc03 non-reference verdict prefix", "uc03", "", nil, verdictFor("L8000", false, dvApproved("PA-deadbeef")), "AUTH-"},
		// The reference payer's answer for uc03's family is "covered + prior
		// authorization required": BOTH halves are the row's subject, so both get
		// a mutation.
		{"uc03 coverage check says not covered", "uc03", "", cardFor("L8000", dvCardAuthNeededNotCovered), nil, `want "covered"`},
		{"uc03 coverage check requires no prior authorization", "uc03", "", cardFor("L8000", dvCardCovered), nil, `want "auth-needed"`},
		{"uc03 card advertises no questionnaire", "uc03", "", cardFor("L8000", dvCardAuthNeededNoQuestionnaire), nil, "no questionnaire canonical"},
		{"uc04 first submit approved", "uc04", "", nil, verdictFor("E0424", false, dvApproved("AUTH-9")), "not pended"},
		{"uc04 amend still held", "uc04", "", nil, verdictFor("E0424", true, dvPended()), "not approved"},
		{"uc04 amend non-reference prefix", "uc04", "", nil, verdictFor("E0424", true, dvApproved("PA-1")), "AUTH-"},
		{"uc05 first submit approved", "uc05", "", nil, verdictFor("E0424", false, dvApproved("AUTH-9")), "not pended"},
		{"uc05 amend still held", "uc05", "", nil, verdictFor("E0424", true, dvPended()), "not approved"},
		{"uc05 amend non-reference prefix", "uc05", "", nil, verdictFor("E0424", true, dvApproved("PA-1")), "AUTH-"},
		{"uc06 first submit approved", "uc06", "", nil, verdictFor("E0424", false, dvApproved("AUTH-9")), "not pended"},
		{"uc06 amend still held", "uc06", "", nil, verdictFor("E0424", true, dvPended()), "not approved"},
		{"uc06 amend non-reference prefix", "uc06", "", nil, verdictFor("E0424", true, dvApproved("PA-1")), "AUTH-"},
		// The E0424 family's coverage answer is CONDITIONAL on all three rows that
		// drive it — a covered/no-auth answer means the payer decided the request
		// at the coverage check, which is not what these rows exercise.
		{"uc04 coverage check not conditional", "uc04", "", cardFor("E0424", dvCardCovered), nil, `want "conditional"`},
		{"uc05 coverage check not conditional", "uc05", "", cardFor("E0424", dvCardCovered), nil, `want "conditional"`},
		{"uc06 coverage check not conditional", "uc06", "", cardFor("E0424", dvCardCovered), nil, `want "conditional"`},
		{"uc07 non-reference verdict prefix", "uc07", "", nil, verdictFor("L8000", false, dvApproved("PA-1")), "AUTH-"},
		{"uc08 approved", "uc08", "", nil, verdictFor("J3490", false, dvApproved("AUTH-9")), "want denied"},
		{"uc08 held", "uc08", "", nil, verdictFor("J3490", false, dvPended()), "want denied"},
		{"uc08 denied without a rationale", "uc08", "", nil, verdictFor("J3490", false, dvDeniedNoRationale), "rationale"},
		{"uc08 coverage check says covered", "uc08", "", cardFor("J3490", dvCardCovered), nil, "not-covered"},
		{"uc02 coverage check demands prior authorization", "uc02", "", cardFor("E0250", dvCardAuthNeeded), nil, "no-auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ing := newDVIngress(t)
			ing.card, ing.verdict = tc.card, tc.verdict
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
