package byo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- 9: Patients -------------------------------------------------------

func TestPatients_QueryAndFiltering(t *testing.T) {
	var gotPath string
	var gotQuery map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = map[string][]string(r.URL.Query())
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write([]byte(`{
			"resourceType": "Bundle",
			"type": "searchset",
			"entry": [
				{
					"resource": {
						"resourceType": "Patient",
						"id": "pat-1",
						"identifier": [{"system": "urn:shn:member", "value": "MBR-1"}],
						"name": [{"text": "Linda Johansson"}],
						"birthDate": "1970-01-01"
					}
				},
				{
					"resource": {
						"resourceType": "Patient",
						"id": "pat-2",
						"identifier": [{"system": "urn:shn:member", "value": "MBR-2"}]
					}
				},
				{
					"resource": {
						"resourceType": "Patient",
						"id": "pat-3",
						"identifier": [{"system": "urn:other:system", "value": "irrelevant"}]
					}
				}
			]
		}`))
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	patients, err := b.Patients(context.Background())
	if err != nil {
		t.Fatalf("Patients: %v", err)
	}

	if gotPath != "/Patient" {
		t.Errorf("path = %q, want /Patient", gotPath)
	}
	if got := gotQuery["identifier"]; len(got) != 1 || got[0] != "urn:shn:member|" {
		t.Errorf("identifier query = %v, want [urn:shn:member|]", got)
	}
	if got := gotQuery["_count"]; len(got) != 1 || got[0] != "50" {
		t.Errorf("_count query = %v, want [50]", got)
	}

	if len(patients) != 2 {
		t.Fatalf("want 2 patients (member-identifier only), got %d: %+v", len(patients), patients)
	}
	if patients[0].FHIRID != "pat-1" || patients[0].MemberID != "MBR-1" || patients[0].Name != "Linda Johansson" || patients[0].BirthDate != "1970-01-01" {
		t.Errorf("patients[0] = %+v", patients[0])
	}
	if patients[1].FHIRID != "pat-2" || patients[1].MemberID != "MBR-2" {
		t.Errorf("patients[1] = %+v", patients[1])
	}
}

// --- 10: Context ---------------------------------------------------------

func TestContext_DeviceRequestOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/DeviceRequest"):
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","entry":[{"resource":{
				"resourceType":"DeviceRequest","id":"dr-1","status":"active",
				"codeCodeableConcept":{"text":"Home Oxygen Concentrator"}
			}}]}`))
		case strings.HasPrefix(r.URL.Path, "/ServiceRequest"):
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","entry":[]}`))
		case strings.HasPrefix(r.URL.Path, "/Coverage"):
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","entry":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	ctx, err := b.Context(context.Background(), "pat-1")
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(ctx.Order) == 0 || string(ctx.Order) == "null" {
		t.Fatalf("want Order populated, got %s", ctx.Order)
	}
	if !strings.Contains(ctx.OrderSummary, "Home Oxygen Concentrator") {
		t.Errorf("OrderSummary = %q, want it to contain the code text", ctx.OrderSummary)
	}
	if len(ctx.Coverage) != 0 && string(ctx.Coverage) != "null" {
		t.Errorf("want Coverage null/empty, got %s", ctx.Coverage)
	}
}

func TestContext_ServiceRequestFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/DeviceRequest"):
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","entry":[]}`))
		case strings.HasPrefix(r.URL.Path, "/ServiceRequest"):
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","entry":[{"resource":{
				"resourceType":"ServiceRequest","id":"sr-1","status":"active",
				"code":{"text":"MRI Lumbar Spine"}
			}}]}`))
		case strings.HasPrefix(r.URL.Path, "/Coverage"):
			_, _ = w.Write([]byte(`{"resourceType":"Bundle","entry":[{"resource":{
				"resourceType":"Coverage","id":"cov-1","status":"active",
				"payor":[{"display":"Acme Health Plan"}]
			}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	ctx, err := b.Context(context.Background(), "pat-1")
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(ctx.Order) == 0 || string(ctx.Order) == "null" {
		t.Fatalf("want ServiceRequest fallback Order populated, got %s", ctx.Order)
	}
	if !strings.Contains(ctx.OrderSummary, "MRI Lumbar Spine") {
		t.Errorf("OrderSummary = %q", ctx.OrderSummary)
	}
	if len(ctx.Coverage) == 0 || string(ctx.Coverage) == "null" {
		t.Fatalf("want Coverage populated, got %s", ctx.Coverage)
	}
	if !strings.Contains(ctx.CoverageSummary, "Acme Health Plan") {
		t.Errorf("CoverageSummary = %q, want payor display", ctx.CoverageSummary)
	}
}

func TestContext_NoOrderNoCoverage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","entry":[]}`))
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	ctx, err := b.Context(context.Background(), "pat-1")
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if string(ctx.Order) != "null" && len(ctx.Order) != 0 {
		t.Errorf("want Order null, got %s", ctx.Order)
	}
	want := "no open order found — the free-form run needs one active DeviceRequest or ServiceRequest"
	if ctx.OrderSummary != want {
		t.Errorf("OrderSummary = %q, want %q", ctx.OrderSummary, want)
	}
	if string(ctx.Coverage) != "null" && len(ctx.Coverage) != 0 {
		t.Errorf("want Coverage null, got %s", ctx.Coverage)
	}
}

// --- 11: non-2xx / non-JSON --------------------------------------------

func TestBrowse_NonJSONIsHumanUsableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json at all"))
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	if _, err := b.Patients(context.Background()); err == nil {
		t.Fatal("want error for non-JSON body")
	} else if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error = %q, want it to name the URL", err.Error())
	}
}

func TestBrowse_Non2xxIsHumanUsableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	_, err := b.Patients(context.Background())
	if err == nil {
		t.Fatal("want error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error = %q, want it to name the URL and the status", err.Error())
	}
}

// ---- HasPersona (sentinel check) -------------------------------------------

func TestHasPersona_Found(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("identifier")
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write([]byte(`{
			"resourceType": "Bundle",
			"type": "searchset",
			"entry": [
				{"resource": {"resourceType":"Patient","id":"pat-1","identifier":[{"system":"urn:shn:member","value":"MBR-COVERED"}]}}
			]
		}`))
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	ok, err := b.HasPersona(context.Background(), "MBR-COVERED")
	if err != nil {
		t.Fatalf("HasPersona: %v", err)
	}
	if !ok {
		t.Error("HasPersona = false, want true (one matching entry)")
	}
	if gotPath != "/Patient" {
		t.Errorf("path = %q, want /Patient", gotPath)
	}
	if gotQuery != "urn:shn:member|MBR-COVERED" {
		t.Errorf("identifier query = %q, want urn:shn:member|MBR-COVERED", gotQuery)
	}
}

func TestHasPersona_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","type":"searchset","entry":[]}`))
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	ok, err := b.HasPersona(context.Background(), "MBR-COVERED")
	if err != nil {
		t.Fatalf("HasPersona: %v", err)
	}
	if ok {
		t.Error("HasPersona = true, want false (empty searchset — a well-formed 'not found' is not an error)")
	}
}

func TestHasPersona_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := NewBrowser(srv.URL, nil)
	if _, err := b.HasPersona(context.Background(), "MBR-COVERED"); err == nil {
		t.Fatal("HasPersona: want error for a 500 response, got nil")
	}
}
