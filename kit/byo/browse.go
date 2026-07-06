package byo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// PatientSummary is one entry in the partner FHIR server's patient list, for
// the Kit's bring-your-own "browse the EHR" panel.
type PatientSummary struct {
	FHIRID    string `json:"fhirId"`
	MemberID  string `json:"memberId"` // the urn:shn:member value — what the free-form run posts
	Name      string `json:"name"`
	BirthDate string `json:"birthDate"`
}

// PatientContext is the open-order + coverage snapshot the free-form run
// panel shows for a selected patient. Order/Coverage are raw FHIR resource
// bytes (json.RawMessage null when absent); the Summary fields are
// plain-language one-liners for display.
type PatientContext struct {
	Order           json.RawMessage `json:"order"` // null when absent
	OrderSummary    string          `json:"orderSummary"`
	Coverage        json.RawMessage `json:"coverage"` // null when absent
	CoverageSummary string          `json:"coverageSummary"`
}

// Browser reads a partner's FHIR server for the bring-your-own browse
// panel: it mirrors the SAME queries gateway/connectors/fhirsor/fhirsor.go
// runs against a holder's US Core store (resolvePatient's identifier
// search, OpenOrder's DeviceRequest-then-ServiceRequest fallback, and
// CoverageInforce's beneficiary search) — this is a read-only, browse-only
// client; it does not implement engine.SystemOfRecord.
type Browser struct {
	base string
	hc   *http.Client
}

// NewBrowser returns a Browser over dataURL. hc == nil uses
// http.DefaultClient.
func NewBrowser(dataURL string, hc *http.Client) *Browser {
	client := hc
	if client == nil {
		client = http.DefaultClient
	}
	return &Browser{base: strings.TrimRight(dataURL, "/"), hc: client}
}

// searchset is the minimal Bundle shape the browse reads need.
type searchset struct {
	ResourceType string `json:"resourceType"`
	Entry        []struct {
		Resource json.RawMessage `json:"resource"`
	} `json:"entry"`
}

// search runs a FHIR search (GET {base}/{resourceType}?{params}), requiring
// a 200 response whose body is a Bundle. Errors name the URL and the
// failure (status or decode error) so they render usably in the browse
// panel (per the panel's human-usable-error requirement).
func (b *Browser) search(ctx context.Context, resourceType string, params url.Values) (searchset, error) {
	u := b.base + "/" + resourceType
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return searchset{}, fmt.Errorf("kit/byo: build request for %s: %w", u, err)
	}
	req.Header.Set("Accept", "application/fhir+json")

	resp, err := b.hc.Do(req)
	if err != nil {
		return searchset{}, fmt.Errorf("kit/byo: could not reach %s: %w", u, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return searchset{}, fmt.Errorf("kit/byo: %s responded with status %d", u, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes))
	if err != nil {
		return searchset{}, fmt.Errorf("kit/byo: read response from %s: %w", u, err)
	}
	var b2 searchset
	if err := json.Unmarshal(body, &b2); err != nil {
		return searchset{}, fmt.Errorf("kit/byo: %s did not return valid FHIR JSON: %w", u, err)
	}
	if b2.ResourceType != "Bundle" {
		return searchset{}, fmt.Errorf("kit/byo: %s returned resourceType %q, want Bundle", u, b2.ResourceType)
	}
	return b2, nil
}

// firstEntry returns the first entry's raw resource bytes, or (nil, false)
// when the searchset has no entries.
func firstEntry(bs searchset) (json.RawMessage, bool) {
	if len(bs.Entry) == 0 {
		return nil, false
	}
	return bs.Entry[0].Resource, true
}

// minimalPatient is the subset of Patient fields Patients() needs.
type minimalPatient struct {
	ID         string `json:"id"`
	Identifier []struct {
		System string `json:"system"`
		Value  string `json:"value"`
	} `json:"identifier"`
	Name []struct {
		Text   string   `json:"text"`
		Family string   `json:"family"`
		Given  []string `json:"given"`
	} `json:"name"`
	BirthDate string `json:"birthDate"`
}

func patientDisplayName(p minimalPatient) string {
	if len(p.Name) == 0 {
		return ""
	}
	n := p.Name[0]
	if n.Text != "" {
		return n.Text
	}
	parts := append(append([]string{}, n.Given...), n.Family)
	joined := strings.TrimSpace(strings.Join(parts, " "))
	return joined
}

// Patients lists the partner FHIR server's patients carrying the SHN
// member identifier: Patient?identifier=urn:shn:member|&_count=50 (mirrors
// gateway/connectors/fhirsor/fhirsor.go's resolvePatient identifier search,
// widened to a system-only search so it lists ALL members rather than
// resolving one). A patient entry without the member identifier is skipped
// (defensive — a partner server may hold non-member patients too).
func (b *Browser) Patients(ctx context.Context) ([]PatientSummary, error) {
	bs, err := b.search(ctx, "Patient", url.Values{
		"identifier": {shnsdk.MemberSystem + "|"},
		"_count":     {"50"},
	})
	if err != nil {
		return nil, err
	}

	var out []PatientSummary
	for _, e := range bs.Entry {
		var p minimalPatient
		if err := json.Unmarshal(e.Resource, &p); err != nil {
			continue // defensive: skip a malformed entry rather than fail the whole browse
		}
		memberID := ""
		for _, id := range p.Identifier {
			if id.System == shnsdk.MemberSystem {
				memberID = id.Value
				break
			}
		}
		if memberID == "" {
			continue // no member identifier — not an SHN member patient
		}
		out = append(out, PatientSummary{
			FHIRID:    p.ID,
			MemberID:  memberID,
			Name:      patientDisplayName(p),
			BirthDate: p.BirthDate,
		})
	}
	return out, nil
}

// HasPersona reports whether the partner FHIR server carries a Patient
// carrying the SHN member identifier memberID —
// Patient?identifier=urn:shn:member|{memberID}&_count=1 (the same search
// shape Patients() runs, narrowed to one member and count 1). This is the
// sentinel check GET /api/byo's "demoPersonas" tri-state
// uses ("does your connected server carry the demo personas" — shown,
// never assumed). A well-formed empty searchset is a legitimate answer
// (false, nil) — "not found" is not an error; only a transport/decode
// failure (network unreachable, non-200, non-Bundle body) is.
func (b *Browser) HasPersona(ctx context.Context, memberID string) (bool, error) {
	bs, err := b.search(ctx, "Patient", url.Values{
		"identifier": {shnsdk.MemberSystem + "|" + memberID},
		"_count":     {"1"},
	})
	if err != nil {
		return false, err
	}
	return len(bs.Entry) > 0, nil
}

// codeableConceptText is the minimal {text, coding[].display} shape shared
// by DeviceRequest.codeCodeableConcept, ServiceRequest.code, and
// Coverage.payor[].display lookups below.
type codeableConceptText struct {
	Text   string `json:"text"`
	Coding []struct {
		Display string `json:"display"`
	} `json:"coding"`
}

func (c codeableConceptText) display() string {
	if c.Text != "" {
		return c.Text
	}
	if len(c.Coding) > 0 {
		return c.Coding[0].Display
	}
	return ""
}

type minimalOrder struct {
	ResourceType        string              `json:"resourceType"`
	Status              string              `json:"status"`
	CodeCodeableConcept codeableConceptText `json:"codeCodeableConcept"` // DeviceRequest
	Code                codeableConceptText `json:"code"`                // ServiceRequest
}

func (o minimalOrder) display() string {
	if d := o.CodeCodeableConcept.display(); d != "" {
		return d
	}
	return o.Code.display()
}

type minimalCoverage struct {
	Status string `json:"status"`
	Payor  []struct {
		Display   string `json:"display"`
		Reference string `json:"reference"`
	} `json:"payor"`
}

func (c minimalCoverage) payorDisplay() string {
	for _, p := range c.Payor {
		if p.Display != "" {
			return p.Display
		}
	}
	return ""
}

// Context resolves the open-order + coverage snapshot for the patient at
// fhirID (the FHIR store id from a PatientSummary). Mirrors
// gateway/connectors/fhirsor/fhirsor.go's OpenOrder (DeviceRequest?patient=
// {id}&status=active, falling back to ServiceRequest, first entry) and
// CoverageInforce's beneficiary search (Coverage?beneficiary=Patient/{id},
// first entry).
func (b *Browser) Context(ctx context.Context, fhirID string) (*PatientContext, error) {
	out := &PatientContext{
		Order:    json.RawMessage("null"),
		Coverage: json.RawMessage("null"),
	}

	var orderRaw json.RawMessage
	for _, rtype := range []string{"DeviceRequest", "ServiceRequest"} {
		bs, err := b.search(ctx, rtype, url.Values{
			"patient": {fhirID},
			"status":  {"active"},
		})
		if err != nil {
			return nil, err
		}
		if raw, ok := firstEntry(bs); ok {
			orderRaw = raw
			break
		}
	}
	if orderRaw != nil {
		out.Order = orderRaw
		var order minimalOrder
		if err := json.Unmarshal(orderRaw, &order); err == nil {
			out.OrderSummary = fmt.Sprintf("%s (%s)", order.display(), order.Status)
		}
	} else {
		out.OrderSummary = "no open order found — the free-form run needs one active DeviceRequest or ServiceRequest"
	}

	covBundle, err := b.search(ctx, "Coverage", url.Values{
		"beneficiary": {"Patient/" + fhirID},
	})
	if err != nil {
		return nil, err
	}
	if raw, ok := firstEntry(covBundle); ok {
		out.Coverage = raw
		var cov minimalCoverage
		if err := json.Unmarshal(raw, &cov); err == nil {
			if payor := cov.payorDisplay(); payor != "" {
				out.CoverageSummary = fmt.Sprintf("%s (%s)", payor, cov.Status)
			} else {
				out.CoverageSummary = cov.Status
			}
		}
	}

	return out, nil
}
