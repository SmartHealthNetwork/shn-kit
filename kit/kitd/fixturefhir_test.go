package kitd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureFHIRServer stands the fixture system of record up under the tenant prefix the
// no-trio lane wires FHIR_DATA_URL to.
func fixtureFHIRServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	fx, err := newFixtureFHIR()
	if err != nil {
		t.Fatalf("newFixtureFHIR: %v", err)
	}
	const prefix = "/fhir/provider"
	srv := httptest.NewServer(localFHIRHandler(prefix, fx))
	t.Cleanup(srv.Close)
	return srv, srv.URL + prefix
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestFixtureFHIR_ResolvesSeededMember is the load-bearing row: the no-trio lane's gateway
// child resolves its members through gateway/connectors/fhirsor, whose FIRST call is the
// Patient identifier search. If this row fails the child cannot resolve anybody and every
// scenario 400s "unknown member".
func TestFixtureFHIR_ResolvesSeededMember(t *testing.T) {
	_, base := fixtureFHIRServer(t)

	status, body := getJSON(t, base+"/Patient?identifier=urn:shn:member%7CMBR-COVERED")
	if status != http.StatusOK {
		t.Fatalf("Patient identifier search: status=%d, want 200", status)
	}
	if body["resourceType"] != "Bundle" || body["type"] != "searchset" {
		t.Fatalf("search answer = %v/%v, want Bundle/searchset", body["resourceType"], body["type"])
	}
	entries, _ := body["entry"].([]any)
	if len(entries) != 1 {
		t.Fatalf("MBR-COVERED identifier search returned %d entries, want exactly 1", len(entries))
	}
	res, _ := entries[0].(map[string]any)["resource"].(map[string]any)
	if res["resourceType"] != "Patient" {
		t.Fatalf("entry is a %v, want Patient", res["resourceType"])
	}
	id, _ := res["id"].(string)
	if id == "" {
		t.Fatal("the seeded Patient carries no id — a read-by-reference could not follow it")
	}

	// The read-by-reference the SoR follows after the search.
	status, read := getJSON(t, base+"/Patient/"+id)
	if status != http.StatusOK || read["id"] != id {
		t.Fatalf("Patient/%s read: status=%d id=%v", id, status, read["id"])
	}

	// The beneficiary-scoped Coverage read (the payer-of-record / routing source).
	status, cov := getJSON(t, base+"/Coverage?beneficiary="+id)
	if status != http.StatusOK {
		t.Fatalf("Coverage beneficiary search: status=%d, want 200", status)
	}
	if n, _ := cov["total"].(float64); n < 1 {
		t.Fatalf("MBR-COVERED has no seeded Coverage — payer routing would fail closed")
	}
}

// TestFixtureFHIR_UnknownMemberIsEmpty: a member the seed does not carry resolves to an
// EMPTY searchset, never a fabricated Patient. Fail-closed is the whole point of retiring
// the in-process census.
func TestFixtureFHIR_UnknownMemberIsEmpty(t *testing.T) {
	_, base := fixtureFHIRServer(t)
	status, body := getJSON(t, base+"/Patient?identifier=urn:shn:member%7CMBR-NOBODY")
	if status != http.StatusOK {
		t.Fatalf("status=%d, want 200 (an empty searchset, not an error)", status)
	}
	if n, _ := body["total"].(float64); n != 0 {
		t.Fatalf("unknown member returned %v matches, want 0", body["total"])
	}
}

// TestFixtureFHIR_ReadOnly: the fixture refuses writes. A write that appeared to succeed
// and then vanished on the next boot would be worse than a refusal.
func TestFixtureFHIR_ReadOnly(t *testing.T) {
	_, base := fixtureFHIRServer(t)
	resp, err := http.Post(base+"/Patient", "application/fhir+json", strings.NewReader(`{"resourceType":"Patient"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d, want 405", resp.StatusCode)
	}
}

// TestBuildStack_NoTrioWiresFixtureSoR pins the boot-critical wiring: with no Java trio and
// no bring-your-own FHIR server, the gateway child MUST still be handed a FHIR_DATA_URL —
// a gateway with none refuses to boot outright. Stack.Close releases the listener.
func TestBuildStack_NoTrioWiresFixtureSoR(t *testing.T) {
	st, err := BuildStack(StackConfig{
		StateDir:      t.TempDir(),
		GatewayBinary: "/nonexistent/gateway", // never started here; BuildStack only assembles specs
	})
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var dataURL string
	for _, kv := range st.GatewayEnv {
		if strings.HasPrefix(kv, "FHIR_DATA_URL=") {
			dataURL = strings.TrimPrefix(kv, "FHIR_DATA_URL=")
		}
	}
	if dataURL == "" {
		t.Fatal("no-trio gateway child got no FHIR_DATA_URL — it would refuse to boot")
	}
	// Non-vacuous: the URL actually serves the seeded roster.
	status, body := getJSON(t, dataURL+"/Patient?identifier=urn:shn:member%7CMBR-COVERED")
	if status != http.StatusOK {
		t.Fatalf("the wired FHIR_DATA_URL does not serve: status=%d", status)
	}
	if n, _ := body["total"].(float64); n != 1 {
		t.Fatalf("the wired FHIR_DATA_URL resolved %v matches for MBR-COVERED, want 1", body["total"])
	}
}

// ── Questionnaire/$populate ─────────────────────────────────────────────────────────────
//
// PROVENANCE. The fence names MECHANISMS, not a taste-list of extensions, and the table below
// carries a row per member of each. Two independent inputs shaped it:
//
//   - A CENSUS of the questionnaires the reference payer actually advertises. The one this
//     lane can meet — the prior-authorization questionnaire — carries no population extension
//     of any kind (a group, a display item, one boolean), while the home-oxygen questionnaire
//     carries a bundled library, six initial expressions, four calculated expressions and a
//     sub-questionnaire, and the home-health assessment carries a bundled library and three
//     sub-questionnaires. Driven through this endpoint: 200 with a shell for the first, 422
//     naming "cqf-library" for the other two.
//   - The SPECIFICATIONS the census cannot see. A census only ever proves what today's
//     questionnaires happen to carry; it says nothing about the mechanisms nobody in the
//     sample used. StructureMap-based population is the case in point — it carries neither a
//     library nor an expression, appears in none of the three captured resources, and a fence
//     built from the census alone answered it with a silent empty shell.
//
// The tests use hand-built questionnaires rather than the captured resources on purpose: this
// module is published and its tests must not read files outside it. What they add beyond both
// inputs is DEPTH — markers are planted at the root, on a leaf item, two items deep, and on a
// group — because an extension can sit anywhere and a fence that only looked where today's
// questionnaires happen to carry theirs would miss tomorrow's.

// populateOn posts an SDC $populate for questionnaire q about subject, against a handler with
// no system-of-record index (the bring-your-own posture — $populate is served either way).
func populateOn(t *testing.T, q map[string]any, subject string) (int, map[string]any) {
	t.Helper()
	srv := httptest.NewServer(localFHIRHandler("/fhir/provider", nil))
	t.Cleanup(srv.Close)
	qJSON, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal questionnaire: %v", err)
	}
	params, err := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []map[string]any{
			{"name": "questionnaire", "resource": json.RawMessage(qJSON)},
			{"name": "subject", "valueReference": map[string]string{"reference": subject}},
		},
	})
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	resp, err := http.Post(srv.URL+"/fhir/provider"+populateSuffix, "application/fhir+json", bytes.NewReader(params))
	if err != nil {
		t.Fatalf("POST $populate: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode $populate response: %v", err)
	}
	return resp.StatusCode, out
}

// plainQuestionnaire is the shape this endpoint CAN answer: a group, a display item and one
// boolean for a clinician to answer — no computation asked for anywhere. Modelled on the
// reference payer's own prior-authorization questionnaire.
func plainQuestionnaire() map[string]any {
	return map[string]any{
		"resourceType": "Questionnaire",
		"status":       "active",
		"url":          "http://example.org/fhir/Questionnaire/PriorAuthRequired",
		"item": []any{map[string]any{
			"linkId": "1", "type": "group",
			"item": []any{
				map[string]any{"linkId": "1.1", "type": "display", "text": "Attestation"},
				map[string]any{"linkId": "1.2", "type": "boolean", "text": "Documentation on file?"},
			},
		}},
	}
}

// TestPopulate_ShellForAQuestionnaireThatComputesNothing pins the answer this lane gives when
// there is nothing to compute: an in-progress QuestionnaireResponse naming the questionnaire
// and the subject, with no items. Empty is the CORRECT answer here, not a degraded one — the
// questionnaire asks for no computed values, so a real engine returns the same shell.
func TestPopulate_ShellForAQuestionnaireThatComputesNothing(t *testing.T) {
	status, out := populateOn(t, plainQuestionnaire(), "Patient/pat-scoped-123")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (this questionnaire asks for no computation): %v", status, out)
	}
	if out["resourceType"] != "QuestionnaireResponse" {
		t.Errorf("resourceType = %v, want QuestionnaireResponse", out["resourceType"])
	}
	if out["status"] != "in-progress" {
		t.Errorf("status = %v, want in-progress — nothing has been answered yet", out["status"])
	}
	// The caller verifies the response is about the patient it asked for and rejects it
	// otherwise, so the subject must come back VERBATIM, not normalized or re-derived.
	subj, _ := out["subject"].(map[string]any)
	if subj == nil || subj["reference"] != "Patient/pat-scoped-123" {
		t.Errorf("subject = %v, want the reference that was sent, echoed verbatim", out["subject"])
	}
	// The caller also rejects a response that claims a DIFFERENT questionnaire than the one
	// its card advertised.
	if out["questionnaire"] != "http://example.org/fhir/Questionnaire/PriorAuthRequired" {
		t.Errorf("questionnaire = %v, want the posted questionnaire's own url", out["questionnaire"])
	}
	if _, present := out["item"]; present {
		t.Errorf("the response carries items (%v) — a questionnaire with nothing to compute must come back empty, never invented", out["item"])
	}
	if _, ok := out["authored"].(string); !ok {
		t.Errorf("authored = %v, want a timestamp", out["authored"])
	}
}

// TestPopulate_RefusesEveryComputationMarker is the fence's rejection table: one row per
// marker, each planted at a DIFFERENT depth, each a valid-questionnaire-minus-one-mutation of
// the shell case above. Every row must be refused BY NAME — an under-filled response for a
// questionnaire that asked to be computed is the exact failure the gateway demands an operated
// endpoint in order to prevent, so it must be impossible to get one out of this endpoint.
func TestPopulate_RefusesEveryComputationMarker(t *testing.T) {
	ext := func(url string) map[string]any {
		return map[string]any{"url": url, "valueString": "irrelevant"}
	}
	for _, tc := range []struct {
		name, marker, mechanism string
		mutate                  func(q map[string]any)
	}{
		{
			name:   "a bundled library at the root",
			marker: "cqf-library", mechanism: "expression-based population",
			mutate: func(q map[string]any) {
				q["extension"] = []any{map[string]any{
					"url":               "http://hl7.org/fhir/StructureDefinition/cqf-library",
					"valueCanonical":    "http://example.org/Library/Prepop",
					"__whyValueDoesNot": "matter",
				}}
			},
		},
		{
			name:   "an initial expression on a leaf item",
			marker: "sdc-questionnaire-initialExpression", mechanism: "expression-based population",
			mutate: func(q map[string]any) {
				group := q["item"].([]any)[0].(map[string]any)
				leaf := group["item"].([]any)[1].(map[string]any)
				leaf["extension"] = []any{ext("http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-initialExpression")}
			},
		},
		{
			name:   "a calculated expression nested two items deep",
			marker: "sdc-questionnaire-calculatedExpression", mechanism: "expression-based population",
			mutate: func(q map[string]any) {
				group := q["item"].([]any)[0].(map[string]any)
				group["item"] = append(group["item"].([]any), map[string]any{
					"linkId": "1.3", "type": "group",
					"item": []any{map[string]any{
						"linkId": "1.3.1", "type": "integer",
						"extension": []any{ext("http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-calculatedExpression")},
					}},
				})
			},
		},
		{
			name:   "a sub-questionnaire to assemble",
			marker: "sdc-questionnaire-subQuestionnaire", mechanism: "modular assembly",
			mutate: func(q map[string]any) {
				group := q["item"].([]any)[0].(map[string]any)
				group["extension"] = []any{ext("http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-subQuestionnaire")}
			},
		},
		// The rows below are the ones an earlier, narrower fence let through as a 200 shell.
		// Each is a real population mechanism, and the first is the one that matters most:
		// StructureMap-based population carries NO library and NO expression, so a fence that
		// only knew the expression family had no way to see it at all.
		{
			name:   "a source StructureMap — population with no library and no expressions",
			marker: "sdc-questionnaire-sourceStructureMap", mechanism: "StructureMap-based population",
			mutate: func(q map[string]any) {
				q["extension"] = []any{ext("http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-sourceStructureMap")}
			},
		},
		{
			name:   "a target StructureMap",
			marker: "sdc-questionnaire-targetStructureMap", mechanism: "StructureMap-based population",
			mutate: func(q map[string]any) {
				q["extension"] = []any{ext("http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-targetStructureMap")}
			},
		},
		{
			name:   "the LEGACY core spelling of an initial expression",
			marker: "questionnaire-initialExpression", mechanism: "expression-based population",
			mutate: func(q map[string]any) {
				group := q["item"].([]any)[0].(map[string]any)
				leaf := group["item"].([]any)[1].(map[string]any)
				leaf["extension"] = []any{ext("http://hl7.org/fhir/StructureDefinition/questionnaire-initialExpression")}
			},
		},
		{
			name:   "a bare CQF expression",
			marker: "cqf-expression", mechanism: "expression-based population",
			mutate: func(q map[string]any) {
				group := q["item"].([]any)[0].(map[string]any)
				leaf := group["item"].([]any)[1].(map[string]any)
				leaf["extension"] = []any{ext("http://hl7.org/fhir/StructureDefinition/cqf-expression")}
			},
		},
		{
			name:   "a CQF calculated value",
			marker: "cqf-calculatedValue", mechanism: "expression-based population",
			mutate: func(q map[string]any) {
				group := q["item"].([]any)[0].(map[string]any)
				leaf := group["item"].([]any)[1].(map[string]any)
				leaf["extension"] = []any{ext("http://hl7.org/fhir/StructureDefinition/cqf-calculatedValue")}
			},
		},
		{
			name:   "an item population context",
			marker: "sdc-questionnaire-itemPopulationContext", mechanism: "expression-based population",
			mutate: func(q map[string]any) {
				group := q["item"].([]any)[0].(map[string]any)
				group["extension"] = []any{ext("http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-itemPopulationContext")}
			},
		},
		{
			name:   "a launch context to bind",
			marker: "sdc-questionnaire-launchContext", mechanism: "expression-based population",
			mutate: func(q map[string]any) {
				q["extension"] = []any{ext("http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-launchContext")}
			},
		},
		{
			name:   "an assemble expectation",
			marker: "sdc-questionnaire-assemble-expectation", mechanism: "modular assembly",
			mutate: func(q map[string]any) {
				q["extension"] = []any{ext("http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-assemble-expectation")}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := plainQuestionnaire()
			tc.mutate(q)
			status, out := populateOn(t, q, "Patient/pat-scoped-123")
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 — this questionnaire asks to be computed and this lane has no engine: %v", status, out)
			}
			detail := outcomeDetail(out)
			if !strings.Contains(detail, tc.marker) {
				t.Errorf("refusal = %q, want it to name the marker %q it found", detail, tc.marker)
			}
			// The mechanism, not just the string that matched: an operator reading this needs
			// to learn WHICH capability is missing, and the mechanism is the half that says so.
			if !strings.Contains(detail, tc.mechanism) {
				t.Errorf("refusal = %q, want it to name the mechanism %q the marker belongs to", detail, tc.mechanism)
			}
			if strings.Contains(detail, "QuestionnaireResponse") {
				t.Errorf("refusal = %q — a refusal must not carry a response body", detail)
			}
		})
	}
}

// TestPopulateRefusal_FailsClosedOnInputItCannotRead pins the fence's DEFAULT DIRECTION.
//
// No caller reaches these inputs today — handlePopulate's own parse rejects them first — but a
// fence's default is a design property, not a reachability question. Answering "no population
// mechanism found" for input this endpoint could not read would be deciding the permissive way
// on no evidence, which is the one thing this guard must never do.
func TestPopulateRefusal_FailsClosedOnInputItCannotRead(t *testing.T) {
	for _, tc := range []struct{ name, questionnaire string }{
		{"truncated JSON", `{`},
		{"a bare string", `"a string"`},
		{"a number", `12`},
		{"null", `null`},
		{"an array rather than a resource", `[]`},
		{"empty input", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := populateRefusalFor([]byte(tc.questionnaire))
			if !r.refuses() {
				t.Fatalf("populateRefusalFor(%s) allowed the populate — a fence must not answer \"nothing found\" for input it cannot read", tc.questionnaire)
			}
			if !r.unreadable {
				t.Errorf("populateRefusalFor(%s) refused as a computation mechanism; want it refused as unreadable, which is a different answer to the caller", tc.questionnaire)
			}
			if r.reason == "" {
				t.Error("the refusal carries no reason")
			}
		})
	}
}

// TestPopulateRefusal_AllowsTheOneItCanAnswer is the fail-closed table's other half: the fence
// must not have become an unconditional refusal. Without this row, a `return refusal{...}` at
// the top of the function would satisfy every test above.
func TestPopulateRefusal_AllowsTheOneItCanAnswer(t *testing.T) {
	q, err := json.Marshal(plainQuestionnaire())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if r := populateRefusalFor(q); r.refuses() {
		t.Fatalf("a questionnaire asking for no computation was refused: %s", r.reason)
	}
}

// TestPopulate_MarkerSurvivesAHostMove pins WHY the fence compares last segments: the same
// extension republished under a different host or version path is the same extension, and a
// fence keyed on the whole url would silently stop recognizing it.
func TestPopulate_MarkerSurvivesAHostMove(t *testing.T) {
	q := plainQuestionnaire()
	q["extension"] = []any{map[string]any{
		"url":            "http://example.org/some/other/path/cqf-library",
		"valueCanonical": "http://example.org/Library/Prepop",
	}}
	status, out := populateOn(t, q, "Patient/pat-scoped-123")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 — a relocated marker is still a marker: %v", status, out)
	}
}

// TestPopulate_RefusesAnIncompleteRequest covers the three ways the request itself can be
// unusable. Each is a refusal, never a shell about a patient nobody named.
func TestPopulate_RefusesAnIncompleteRequest(t *testing.T) {
	srv := httptest.NewServer(localFHIRHandler("/fhir/provider", nil))
	defer srv.Close()
	post := func(body string) (int, map[string]any) {
		resp, err := http.Post(srv.URL+"/fhir/provider"+populateSuffix, "application/fhir+json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST $populate: %v", err)
		}
		defer resp.Body.Close() //nolint:errcheck
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	for _, tc := range []struct{ name, body, want string }{
		{"not a Parameters resource", `["nope"]`, "Parameters"},
		{"no questionnaire parameter", `{"resourceType":"Parameters","parameter":[{"name":"subject","valueReference":{"reference":"Patient/p"}}]}`, "questionnaire"},
		{"no subject parameter", `{"resourceType":"Parameters","parameter":[{"name":"questionnaire","resource":{"resourceType":"Questionnaire","url":"http://example.org/q"}}]}`, "subject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out := post(tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %v", status, out)
			}
			if d := outcomeDetail(out); !strings.Contains(d, tc.want) {
				t.Errorf("refusal = %q, want it to name %q", d, tc.want)
			}
		})
	}
}

// TestLocalFHIR_PopulateServedWithoutTheSystemOfRecord pins the split that lets a
// bring-your-own-EHR Kit boot: $populate is served with no index behind it (it reads no
// data), while a read is refused rather than answered out of fixtures about a world the
// participant replaced.
func TestLocalFHIR_PopulateServedWithoutTheSystemOfRecord(t *testing.T) {
	srv := httptest.NewServer(localFHIRHandler("/fhir/provider", nil))
	defer srv.Close()

	status, _ := populateOn(t, plainQuestionnaire(), "Patient/p")
	if status != http.StatusOK {
		t.Errorf("$populate with no system-of-record index = %d, want 200", status)
	}

	resp, err := http.Get(srv.URL + "/fhir/provider/Patient?identifier=urn:shn:member%7CMBR-COVERED")
	if err != nil {
		t.Fatalf("GET Patient: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a read with no system-of-record index = %d, want 404 — the participant's own server is the system of record", resp.StatusCode)
	}
}

// TestLocalFHIR_StillReadOnly keeps the pre-existing refusal honest now that ONE POST route
// exists: any other write is still refused.
func TestLocalFHIR_StillReadOnly(t *testing.T) {
	fx, err := newFixtureFHIR()
	if err != nil {
		t.Fatalf("newFixtureFHIR: %v", err)
	}
	srv := httptest.NewServer(localFHIRHandler("/fhir/provider", fx))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/fhir/provider/Patient", "application/fhir+json", strings.NewReader(`{"resourceType":"Patient"}`))
	if err != nil {
		t.Fatalf("POST Patient: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST Patient = %d, want 405", resp.StatusCode)
	}
}

// outcomeDetail reads the diagnostics off an OperationOutcome body.
func outcomeDetail(out map[string]any) string {
	issues, _ := out["issue"].([]any)
	if len(issues) == 0 {
		return ""
	}
	first, _ := issues[0].(map[string]any)
	d, _ := first["diagnostics"].(string)
	return d
}
