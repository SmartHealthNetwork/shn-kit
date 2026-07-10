// seedbundle_test.go — hermetic tests for GET /api/byo/seed-bundle/{lane}.
// White-box (package kitd), mirroring supportbundle_test.go's style: reuses
// its startDaemon + doRawGET helpers (package-visible, defined there) rather
// than redefining them.
package kitd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-gateway/fhirseed"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

// seedBundleTestDaemon starts a bare daemon (no BYO Store configured) — the
// endpoint must serve regardless of BYO swap state, unlike the rest of
// /api/byo/*.
func seedBundleTestDaemon(t *testing.T) (string, string) {
	t.Helper()
	const token = "seed-bundle-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		// BYO intentionally left nil: this endpoint is NOT BYO-store-gated.
	}
	_, apiBase := startDaemon(t, cfg)
	return apiBase, token
}

// bundleEntry is the minimal transaction-Bundle entry shape these tests need
// to assert on: the request method/url and enough of the resource to check
// its type, identifiers, and (for Observations) freshness.
type bundleEntry struct {
	Request struct {
		Method string `json:"method"`
		URL    string `json:"url"`
	} `json:"request"`
	Resource struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
		Identifier   []struct {
			Value string `json:"value"`
		} `json:"identifier"`
		EffectiveDateTime string `json:"effectiveDateTime"`
	} `json:"resource"`
}

type txBundle struct {
	ResourceType string        `json:"resourceType"`
	Type         string        `json:"type"`
	Entry        []bundleEntry `json:"entry"`
}

// ---- Row: conformant lane ---------------------------------------------------

// TestSeedBundle_Conformant_ByteEqualToFhirseedFixture proves the conformant
// lane serves fhirseed.ConformantSeedBundle() byte-for-byte (frozen bytes,
// never re-derived), with the FHIR download content type, the expected
// filename, and the 5 conformant-lane members present.
func TestSeedBundle_Conformant_ByteEqualToFhirseedFixture(t *testing.T) {
	apiBase, token := seedBundleTestDaemon(t)

	status, hdr, body := doRawGET(t, apiBase+"/api/byo/seed-bundle/conformant", token)
	if status != http.StatusOK {
		t.Fatalf("GET .../seed-bundle/conformant = %d, want 200 (body=%s)", status, body)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/fhir+json" {
		t.Errorf("Content-Type = %q, want application/fhir+json", ct)
	}
	wantDisposition := `attachment; filename="shn-conformant-personas.json"`
	if cd := hdr.Get("Content-Disposition"); cd != wantDisposition {
		t.Errorf("Content-Disposition = %q, want %q", cd, wantDisposition)
	}

	want := fhirseed.ConformantSeedBundle()
	if string(body) != string(want) {
		t.Fatalf("conformant seed-bundle body != fhirseed.ConformantSeedBundle() (len got=%d want=%d)", len(body), len(want))
	}

	var bundle txBundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		t.Fatalf("unmarshal conformant bundle: %v", err)
	}
	if bundle.Type != "transaction" {
		t.Errorf("bundle.type = %q, want %q", bundle.Type, "transaction")
	}
	wantMembers := map[string]bool{
		"MBR-COVERED": false, "MBR-NOTCOVERED": false, "MBR-UC06": false,
		"MBR-UC07HCPCS": false, "MBR-UC08": false,
	}
	for _, e := range bundle.Entry {
		for _, id := range e.Resource.Identifier {
			if _, ok := wantMembers[id.Value]; ok {
				wantMembers[id.Value] = true
			}
		}
	}
	for member, found := range wantMembers {
		if !found {
			t.Errorf("conformant seed bundle missing member %q", member)
		}
	}
}

// ---- Row: ehr lane -----------------------------------------------------------

// TestSeedBundle_EHR_StructurallyFreshened proves the ehr lane serves a
// transaction Bundle structurally matching fhirseed.ProviderDataSeedBundle()
// (>=12 Patient PUTs, exactly one Organization/org-cms-payer PUT) but with
// every Observation's effectiveDateTime freshened to (close to) now — never
// byte-compared against the unfreshened fixture, since the date rewrite
// makes that comparison meaningless.
func TestSeedBundle_EHR_StructurallyFreshened(t *testing.T) {
	apiBase, token := seedBundleTestDaemon(t)

	status, hdr, body := doRawGET(t, apiBase+"/api/byo/seed-bundle/ehr", token)
	if status != http.StatusOK {
		t.Fatalf("GET .../seed-bundle/ehr = %d, want 200 (body=%s)", status, body)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/fhir+json" {
		t.Errorf("Content-Type = %q, want application/fhir+json", ct)
	}
	wantDisposition := `attachment; filename="shn-ehr-personas.json"`
	if cd := hdr.Get("Content-Disposition"); cd != wantDisposition {
		t.Errorf("Content-Disposition = %q, want %q", cd, wantDisposition)
	}

	var bundle txBundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		t.Fatalf("unmarshal ehr bundle: %v", err)
	}
	if bundle.Type != "transaction" {
		t.Errorf("bundle.type = %q, want %q", bundle.Type, "transaction")
	}

	patientPUTs := 0
	orgCMSPayerPUTs := 0
	observationCount := 0
	now := time.Now().UTC()
	for _, e := range bundle.Entry {
		if e.Request.Method == "PUT" && e.Resource.ResourceType == "Patient" {
			patientPUTs++
		}
		if e.Request.Method == "PUT" && e.Request.URL == "Organization/org-cms-payer" {
			orgCMSPayerPUTs++
		}
		if e.Resource.ResourceType == "Observation" {
			observationCount++
			if e.Resource.EffectiveDateTime == "" {
				t.Errorf("Observation %s has empty effectiveDateTime", e.Resource.ID)
				continue
			}
			ts, err := time.Parse(time.RFC3339, e.Resource.EffectiveDateTime)
			if err != nil {
				t.Errorf("Observation %s effectiveDateTime %q does not parse as RFC3339: %v", e.Resource.ID, e.Resource.EffectiveDateTime, err)
				continue
			}
			if age := now.Sub(ts); age < 0 || age > 5*time.Minute {
				t.Errorf("Observation %s effectiveDateTime = %s, want within the last few minutes of now (%s)", e.Resource.ID, e.Resource.EffectiveDateTime, now.Format(time.RFC3339))
			}
		}
	}
	if patientPUTs < 12 {
		t.Errorf("ehr seed bundle has %d Patient PUT entries, want >= 12", patientPUTs)
	}
	if orgCMSPayerPUTs != 1 {
		t.Errorf("ehr seed bundle has %d PUT Organization/org-cms-payer entries, want exactly 1", orgCMSPayerPUTs)
	}
	if observationCount == 0 {
		t.Fatal("ehr seed bundle has no Observation entries — freshness assertion is vacuous")
	}
}

// ---- Row: unknown lane -------------------------------------------------------

func TestSeedBundle_UnknownLane_400(t *testing.T) {
	apiBase, token := seedBundleTestDaemon(t)

	status, _, body := doRawGET(t, apiBase+"/api/byo/seed-bundle/bogus", token)
	if status != http.StatusBadRequest {
		t.Fatalf("GET .../seed-bundle/bogus = %d, want 400 (body=%s)", status, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if !strings.Contains(errBody.Error, "bogus") {
		t.Errorf("error body = %q, want it to name the unknown lane %q", errBody.Error, "bogus")
	}
}

// ---- Row: token gated --------------------------------------------------------

// TestSeedBundle_TokenGated mirrors TestSupportBundle_TokenGated: no bearer
// token -> 401, before the lane is ever even inspected.
func TestSeedBundle_TokenGated(t *testing.T) {
	apiBase, _ := seedBundleTestDaemon(t)

	status, _, _ := doRawGET(t, apiBase+"/api/byo/seed-bundle/conformant", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("GET .../seed-bundle/conformant without token = %d, want 401", status)
	}
}
