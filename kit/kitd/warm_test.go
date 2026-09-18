package kitd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWarmValidate_PostsOneValidateAndReportsElapsed(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome","issue":[]}`))
	}))
	defer srv.Close()
	var logged []string
	elapsed, err := WarmValidate(context.Background(), srv.URL+"/fhir", "provider", func(f string, a ...any) { logged = append(logged, f) })
	if err != nil {
		t.Fatal(err)
	}
	if elapsed <= 0 || len(paths) != 1 || paths[0] != "POST /fhir/provider/Patient/$validate" || len(logged) != 1 {
		t.Fatalf("elapsed=%s paths=%v logged=%v", elapsed, paths, logged)
	}
}

// The warm-up's deadline is its own: a server slower than it is refused naming it.
func TestWarmValidate_RefusesPastItsDeadline(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hold }))
	_, err := warmValidate(context.Background(), srv.URL+"/fhir", "provider", 30*time.Millisecond, nil)
	close(hold)
	srv.Close()
	if err == nil || !strings.Contains(err.Error(), "did not answer within 30ms") {
		t.Fatalf("want the deadline named, got %v", err)
	}
}

func TestWarmValidate_ServerErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := WarmValidate(context.Background(), srv.URL+"/fhir", "provider", nil); err == nil {
		t.Fatal("a 500 from $validate must not count as warm")
	}
}
