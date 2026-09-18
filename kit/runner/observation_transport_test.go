package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("broken body") }
func (brokenBody) Close() error             { return nil }
func TestDispatchEvidence(t *testing.T) {
	for _, kind := range []string{"direct success", "direct refusal", "BFF refusal", "transport error", "body error", "early close"} {
		t.Run(kind, func(t *testing.T) {
			d := &DispatchObserver{}
			base := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				if kind == "transport error" {
					return nil, context.Canceled
				}
				status := 200
				if strings.Contains(kind, "refusal") {
					status = 503
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader("unchanged bytes"))
				if kind == "body error" {
					body = brokenBody{}
				}
				return &http.Response{StatusCode: status, Body: body, Header: make(http.Header)}, nil
			})}
			bff := ""
			if kind == "BFF refusal" {
				bff = "http://child"
			}
			resp, err := d.Client(base, bff).Post("http://child/request", "application/json", strings.NewReader("{}"))
			if err == nil {
				if kind != "early close" {
					got, _ := io.ReadAll(resp.Body)
					if kind != "body error" && string(got) != "unchanged bytes" {
						t.Fatal("body changed")
					}
				}
				resp.Body.Close()
			}
			want := kind != "direct success" && kind != "direct refusal"
			if d.Uncertain() != want {
				t.Fatalf("uncertainty = %v want %v", d.Uncertain(), want)
			}
		})
	}
}

func TestMetadataReadDoesNotTaintDispatch(t *testing.T) {
	d := &DispatchObserver{}
	base := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("metadata")), Header: make(http.Header)}, nil
	})}
	resp, err := d.Client(base, "http://child").Get("http://child/fhir/metadata")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if d.Uncertain() {
		t.Fatal("ordinary readiness read tainted clinical dispatch")
	}
}

func TestOrdinaryObservedGETStillTracksBodyError(t *testing.T) {
	d := &DispatchObserver{}
	base := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: brokenBody{}, Header: make(http.Header)}, nil
	})}
	resp, err := d.Client(base, "http://child").Get("http://child/clinical")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if !d.Uncertain() {
		t.Fatal("GET body error escaped dispatch observation")
	}
}
