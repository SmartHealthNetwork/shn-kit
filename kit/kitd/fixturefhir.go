// fixturefhir.go — the Kit's OWN read-only FHIR system of record, served from the seed
// bundles this module already ships.
//
// Why it exists: a gateway requires a FHIR system of record (FHIR_DATA_URL) on every role —
// the in-process persona stub it used to fall back on is gone. The TRIO lane already has
// one (the packaged HAPI data server). The NO-TRIO lane had nothing, so its gateway child
// would refuse to boot. Rather than make the no-trio Kit depend on Java, this serves the
// SAME bundles kitd seeds into the trio's data server (fhirseed.DemoProviderPersonasBundle
// + fhirseed.ProviderDataSeedBundle) over an in-process loopback endpoint.
//
// It is deliberately NOT a general FHIR server: it implements exactly the reads
// gateway/connectors/fhirsor issues — `GET /{Type}?identifier|patient|subject|beneficiary|
// code|status` and `GET /{Type}/{id}` — and ignores any other parameter rather than
// silently narrowing, so a gateway bug never looks like missing data. Writes are refused.
package kitd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SmartHealthNetwork/shn-gateway/fhirseed"
	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// fixtureResource is one seeded resource, kept in capture order so a "first match" search
// resolves the way an insertion-ordered FHIR server's search does.
type fixtureResource struct {
	rtype, id, subject, status string
	body                       []byte
	codes                      []string // "system|code" tokens off code.coding[]
}

// fixtureFHIR is the indexed seed world.
type fixtureFHIR struct {
	all       []fixtureResource
	byRef     map[string][]byte // "Type/id" → bytes
	patientID map[string]string // member identifier value → Patient resource id
}

// newFixtureFHIR indexes the Kit's own seed bundles. Observations are FRESHENED on the way
// in (fhirseed.FreshenObservations), the same rot guard kitd already applies when it seeds
// the trio's data server: the baked effectiveDateTime values would otherwise drift out of
// the operated lookback window a few months after packaging.
func newFixtureFHIR() (*fixtureFHIR, error) {
	f := &fixtureFHIR{byRef: map[string][]byte{}, patientID: map[string]string{}}
	personas, err := fhirseed.FreshenObservations(fhirseed.DemoProviderPersonasBundle())
	if err != nil {
		return nil, fmt.Errorf("kitd: freshen demo personas bundle: %w", err)
	}
	providerData, err := fhirseed.FreshenObservations(fhirseed.ProviderDataSeedBundle())
	if err != nil {
		return nil, fmt.Errorf("kitd: freshen provider-data seed bundle: %w", err)
	}
	for _, raw := range [][]byte{personas, providerData} {
		if err := f.addBundle(raw); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *fixtureFHIR) addBundle(raw []byte) error {
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return fmt.Errorf("kitd: parse seed bundle: %w", err)
	}
	for _, e := range b.Entry {
		f.add(e.Resource)
	}
	return nil
}

func (f *fixtureFHIR) add(body []byte) {
	var probe struct {
		ResourceType string                           `json:"resourceType"`
		ID           string                           `json:"id"`
		Status       string                           `json:"status"`
		Identifier   []struct{ System, Value string } `json:"identifier"`
		Subject      struct{ Reference string }       `json:"subject"`
		Beneficiary  struct{ Reference string }       `json:"beneficiary"`
		Patient      struct{ Reference string }       `json:"patient"`
		Code         struct {
			Coding []struct{ System, Code string } `json:"coding"`
		} `json:"code"`
	}
	if json.Unmarshal(body, &probe) != nil || probe.ResourceType == "" || probe.ID == "" {
		return
	}
	r := fixtureResource{rtype: probe.ResourceType, id: probe.ID, status: probe.Status, body: append([]byte(nil), body...)}
	switch {
	case probe.Subject.Reference != "":
		r.subject = probe.Subject.Reference
	case probe.Beneficiary.Reference != "":
		r.subject = probe.Beneficiary.Reference
	case probe.Patient.Reference != "":
		r.subject = probe.Patient.Reference
	}
	for _, c := range probe.Code.Coding {
		r.codes = append(r.codes, c.System+"|"+c.Code)
	}
	if probe.ResourceType == "Patient" {
		for _, id := range probe.Identifier {
			if id.Value != "" {
				f.patientID[id.Value] = probe.ID
			}
		}
	}
	f.all = append(f.all, r)
	f.byRef[probe.ResourceType+"/"+probe.ID] = r.body
}

// localFHIRHandler serves the no-trio lane's own in-process FHIR surface under a tenant
// prefix, e.g. "/fhir/provider". It has two independent halves, and a caller may want either
// or both:
//
//   - Questionnaire/$populate — ALWAYS served. It reads no data (see handlePopulate: the
//     questionnaires this lane can answer ask for no computation), so it is useful even when
//     the participant brought their own EHR and this process is not the system of record.
//   - the read routes — served only when sor is non-nil, i.e. when this process IS the
//     system of record. With a bring-your-own server configured, a read here would be
//     answering out of fixtures about a world the participant replaced, so it is refused.
//
// Read-only either way: a write that appeared to succeed and then vanished on the next boot
// would be worse than a refusal.
func localFHIRHandler(prefix string, sor *fixtureFHIR) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, populateSuffix) {
			handlePopulate(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeOutcome(w, http.StatusMethodNotAllowed, "the Kit's fixture system of record is read-only")
			return
		}
		if sor == nil {
			writeOutcome(w, http.StatusNotFound, "this endpoint serves Questionnaire/$populate only — "+
				"the system of record is the server configured for this Kit, not this process")
			return
		}
		f := sor
		path := strings.TrimPrefix(r.URL.Path, prefix)
		segs := strings.Split(strings.Trim(path, "/"), "/")
		switch len(segs) {
		case 1:
			if segs[0] == "" {
				writeOutcome(w, http.StatusNotFound, "no resource type in path")
				return
			}
			writeFHIR(w, searchset(f.search(segs[0], r.URL.Query())))
		case 2:
			raw, ok := f.byRef[segs[0]+"/"+segs[1]]
			if !ok {
				writeOutcome(w, http.StatusNotFound, "no such resource")
				return
			}
			w.Header().Set("Content-Type", "application/fhir+json")
			_, _ = w.Write(raw)
		default:
			writeOutcome(w, http.StatusNotFound, "unsupported path")
		}
	})
	return mux
}

// search applies exactly the parameters fhirsor sends.
func (f *fixtureFHIR) search(rtype string, q map[string][]string) [][]byte {
	if ident := firstValue(q["identifier"]); ident != "" && rtype == "Patient" {
		_, value, _ := strings.Cut(ident, "|")
		if value == "" {
			value = ident
		}
		pid, ok := f.patientID[value]
		if !ok {
			return nil
		}
		if raw, ok := f.byRef["Patient/"+pid]; ok {
			return [][]byte{raw}
		}
		return nil
	}
	wantSubject := ""
	for _, key := range []string{"patient", "subject", "beneficiary"} {
		if v := firstValue(q[key]); v != "" {
			wantSubject = "Patient/" + strings.TrimPrefix(v, "Patient/")
		}
	}
	wantStatus := firstValue(q["status"])
	var wantCodes []string
	if codes := firstValue(q["code"]); codes != "" {
		wantCodes = strings.Split(codes, ",") // a token list is OR
	}
	var out [][]byte
	for _, r := range f.all {
		if r.rtype != rtype {
			continue
		}
		if wantSubject != "" && r.subject != wantSubject {
			continue
		}
		if wantStatus != "" && r.status != wantStatus {
			continue
		}
		if len(wantCodes) > 0 && !anyToken(r.codes, wantCodes) {
			continue
		}
		out = append(out, r.body)
	}
	return out
}

func anyToken(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

func firstValue(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func searchset(matches [][]byte) map[string]any {
	entries := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		entries = append(entries, map[string]any{"resource": json.RawMessage(m)})
	}
	return map[string]any{"resourceType": "Bundle", "type": "searchset", "total": len(entries), "entry": entries}
}

func writeFHIR(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeOutcome(w, http.StatusInternalServerError, "encode searchset")
		return
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	_, _ = w.Write(body)
}

func writeOutcome(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resourceType": "OperationOutcome",
		"issue":        []map[string]any{{"severity": "error", "code": "not-supported", "diagnostics": detail}},
	})
}

// ── Questionnaire/$populate ─────────────────────────────────────────────────────────────
//
// The gateway requires an operated $populate endpoint on every origination lane that talks
// to a real payer, and it is right to: the questionnaire on the wire is the PAYER's own
// resource, so a filler that only knows its own questionnaires cannot honestly fill it. The
// packaged Java trio supplies a real clinical-reasoning engine. This lane has none — and
// does not need one, because of what it actually meets.
//
// A questionnaire only needs an engine if it ASKS for computation. The reference payer's
// prior-authorization questionnaire carries no computation extension of any kind: it is a
// group, a display item and one boolean for a clinician to answer. For a questionnaire like
// that the correct operated answer is an in-progress QuestionnaireResponse SHELL — the
// subject, the questionnaire it is about, no items — because there is nothing to compute.
// That is not this endpoint imitating an engine; it is the answer a real engine returns for
// this input class, and the same shell the substrate's own $populate stand-in returns for a
// questionnaire with no computation in it.
//
// Which makes the FENCE the load-bearing half. A questionnaire that DOES ask for computation
// (a bundled CQL library, an expression to evaluate, a sub-questionnaire to assemble) is
// REFUSED by name, never shell-populated: a shell for one of those is a silently
// under-filled answer, and the whole reason the gateway demands an operated endpoint is to
// make that impossible.
// populationMechanism is one documented way a questionnaire can say "my answers are computed,
// not typed" — a family of extensions that all imply the same missing capability.
//
// Grouping the fence by MECHANISM rather than as a flat list of extension names is what makes
// completeness a question a reader can actually answer. The question is not "did we list every
// extension anyone has ever published?" — unanswerable — but "did we list every mechanism by
// which a questionnaire can demand evaluation before a human sees it?". The three below are the
// ones the specifications define:
//
//  1. EXPRESSION-BASED POPULATION — SDC's expression-based Populate, plus the older CQF
//     expression extensions it grew out of. The questionnaire carries expressions (and usually
//     a library holding them) that an engine evaluates against the patient's data.
//  2. STRUCTUREMAP-BASED POPULATION — SDC's other Populate flavour. It carries NO library and
//     NO expressions, which is exactly why it needs naming here: a fence that only knew the
//     expression family would answer a StructureMap-populated questionnaire with an empty
//     shell, the silent under-fill this whole endpoint is fenced to prevent.
//  3. MODULAR ASSEMBLY — SDC's modular questionnaires. The resource on the wire is not the
//     whole questionnaire yet; it has to be assembled before anything can be filled in.
type populationMechanism struct {
	// name is what the mechanism IS. It is quoted back beside the marker so a refusal tells an
	// operator which capability is missing, not merely which string matched.
	name string
	// exact are extension name-parts that name this mechanism outright.
	exact []string
	// suffix are case-insensitive endings that catch the same mechanism under a spelling this
	// list does not carry. The expression family keeps growing — SDC has added several
	// *Expression extensions across its revisions, and the same extension exists under both a
	// legacy core url and a current SDC one — and every one of them means the same thing here,
	// so the family is matched rather than enumerated.
	suffix []string
}

// populationMechanisms is the fence. Matching is deliberately GENEROUS: it errs toward refusing,
// because the two outcomes are not symmetric. A wrong refusal is a legible failure naming the
// missing capability, which an operator fixes by installing the packaged assets. A wrong
// acceptance is an under-filled answer that looks like a real one, which is the failure the
// gateway demands an operated endpoint in order to make impossible.
var populationMechanisms = []populationMechanism{
	{
		name: "expression-based population",
		exact: []string{
			"cqf-library",
			"cqf-calculatedValue",
			"cqf-initialValue",
			"sdc-questionnaire-itemPopulationContext",
			"sdc-questionnaire-launchContext",
			"variable",
		},
		// Catches sdc-questionnaire-{initial,calculated,candidate,context,answer}Expression,
		// the legacy core questionnaire-initialExpression spelling, and cqf-expression.
		suffix: []string{"expression"},
	},
	{
		name:  "StructureMap-based population",
		exact: []string{"sdc-questionnaire-sourceStructureMap", "sdc-questionnaire-targetStructureMap"},
	},
	{
		name:  "modular assembly",
		exact: []string{"sdc-questionnaire-subQuestionnaire", "sdc-questionnaire-assemble-expectation"},
	},
}

// populateSuffix is the operation path this endpoint answers on — the same path
// gateway/app's PROVIDER_DTR_POPULATE_URL names.
const populateSuffix = "/Questionnaire/$populate"

// handlePopulate answers POST {prefix}/Questionnaire/$populate.
//
// Request/response contract is the engine's (gateway/engine/nativepopulate.go): an SDC
// Parameters carrying `questionnaire` (the inline resource) and `subject` (a reference), and
// the QuestionnaireResponse itself as the response body. The subject is echoed back verbatim
// — the caller verifies the returned QR is about the patient it asked for, and a shell that
// renamed the subject would trip that fence for the wrong reason.
func handlePopulate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, populateMaxBody))
	if err != nil {
		writeOutcome(w, http.StatusBadRequest, "could not read the $populate request body")
		return
	}
	var params struct {
		Parameter []struct {
			Name           string `json:"name"`
			ValueReference *struct {
				Reference string `json:"reference"`
			} `json:"valueReference"`
			Resource json.RawMessage `json:"resource"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		writeOutcome(w, http.StatusBadRequest, "the $populate request body is not a Parameters resource")
		return
	}
	var questionnaire json.RawMessage
	subject := ""
	for _, p := range params.Parameter {
		switch {
		case p.Name == "questionnaire" && len(p.Resource) > 0:
			questionnaire = p.Resource
		case p.Name == "subject" && p.ValueReference != nil:
			subject = p.ValueReference.Reference
		}
	}
	if len(questionnaire) == 0 {
		writeOutcome(w, http.StatusBadRequest, "$populate needs a `questionnaire` parameter carrying the questionnaire to populate")
		return
	}
	if subject == "" {
		writeOutcome(w, http.StatusBadRequest, "$populate needs a `subject` parameter naming the patient to populate for")
		return
	}

	// THE FENCE. Refused by name, with the marker and the mechanism quoted, so an operator
	// reading the failure learns which capability was missing rather than that "populate
	// failed". The two refusals are different answers and carry different statuses: one says
	// "this lane cannot serve a well-formed request", the other "this request was unusable".
	if refusal := populateRefusalFor(questionnaire); refusal.refuses() {
		if refusal.unreadable {
			writeOutcome(w, http.StatusBadRequest, "the `questionnaire` parameter "+refusal.reason)
			return
		}
		writeOutcome(w, http.StatusUnprocessableEntity, "this questionnaire "+refusal.reason+
			", and this Kit is running without the packaged one. Install the packaged assets to run "+
			"questionnaires that compute their own answers. Refusing rather than returning an "+
			"under-filled response.")
		return
	}

	url, err := shnsdk.ParseQuestionnaireURL(questionnaire)
	if err != nil {
		writeOutcome(w, http.StatusBadRequest, "the `questionnaire` parameter carries no resolvable url")
		return
	}
	shell, err := json.Marshal(map[string]any{
		"resourceType":  "QuestionnaireResponse",
		"status":        "in-progress",
		"questionnaire": url,
		"subject":       map[string]string{"reference": subject},
		"authored":      time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		writeOutcome(w, http.StatusInternalServerError, "could not encode the populated response")
		return
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	_, _ = w.Write(shell)
}

// populateMaxBody caps the request body. A $questionnaire-package's questionnaire is a
// document, not a stream, and an unbounded read on a loopback endpoint is still an
// unbounded read.
const populateMaxBody = 8 << 20

// populateRefusal is why this endpoint must not populate a questionnaire. The zero value means
// there is no reason and populate may proceed.
type populateRefusal struct {
	// reason is the clause an operator reads after "this questionnaire ..." — it names the
	// marker found and the mechanism it belongs to.
	reason string
	// unreadable separates the two refusals: "asks to be computed" is a well-formed request
	// this lane cannot serve, while "could not be read at all" is a malformed one. They are
	// different answers to the caller and get different statuses.
	unreadable bool
}

func (r populateRefusal) refuses() bool { return r.reason != "" }

// populateRefusalFor reports whether this endpoint must refuse to populate questionnaire.
//
// It FAILS CLOSED in both directions that matter. A questionnaire naming any known population
// mechanism is refused — and so is one this function cannot read at all, because a fence that
// answered "no computation found" for input it could not parse would be deciding the
// permissive way on no evidence, which is the wrong default for a guard even where no caller
// can reach it today.
//
// It walks EVERY `url` value in the document rather than only the places the current IGs
// happen to put them: an extension slice can be nested at any depth — inside an item, inside
// answerOption, on a primitive value's sibling object, inside a contained resource, or in
// modifierExtension rather than extension — and a fence that only looked where today's
// questionnaires carry their markers would miss tomorrow's. Comparison is on the LAST SEGMENT
// of the url, so republishing an extension under another host or version cannot disarm it.
func populateRefusalFor(questionnaire []byte) populateRefusal {
	var doc any
	if err := json.Unmarshal(questionnaire, &doc); err != nil {
		return populateRefusal{
			reason:     "could not be read as JSON, so whether it asks to be computed cannot be established — refusing, because answering \"nothing found\" for input this endpoint cannot read would be a guess",
			unreadable: true,
		}
	}
	if _, ok := doc.(map[string]any); !ok {
		return populateRefusal{
			reason:     "is not a JSON object, so it is not a questionnaire this endpoint can inspect for the mechanisms it must refuse",
			unreadable: true,
		}
	}
	found := populateRefusal{}
	var walk func(any)
	walk = func(node any) {
		if found.refuses() {
			return
		}
		switch n := node.(type) {
		case map[string]any:
			if raw, ok := n["url"].(string); ok {
				if marker, mech, hit := matchPopulationMechanism(raw); hit {
					found = populateRefusal{reason: fmt.Sprintf(
						"carries %q (%s), so it asks to be filled by a clinical-reasoning engine", marker, mech)}
					return
				}
			}
			for _, v := range n {
				walk(v)
			}
		case []any:
			for _, v := range n {
				walk(v)
			}
		}
	}
	walk(doc)
	return found
}

// matchPopulationMechanism resolves one extension url against the fence, returning the marker
// segment that matched and the mechanism it belongs to.
func matchPopulationMechanism(rawURL string) (marker, mechanism string, found bool) {
	seg := rawURL
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	lower := strings.ToLower(seg)
	for _, m := range populationMechanisms {
		for _, e := range m.exact {
			if seg == e {
				return seg, m.name, true
			}
		}
		for _, suf := range m.suffix {
			if strings.HasSuffix(lower, suf) {
				return seg, m.name, true
			}
		}
	}
	return "", "", false
}
