package auditread

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureJSON is a hand-written 3-record /auditor array. It includes extra
// unknown fields (prevHash, recordHash, signatures) that Record does not
// declare — pinning that decoding is tolerant of the full wire contract
// (internal/audit.Record has more fields than the Kit's local view).
const fixtureJSON = `[
  {
    "seq": 1,
    "prevHash": "",
    "recordHash": "aaa",
    "timestamp": "2026-07-03T00:00:00Z",
    "sender": "provider-org",
    "recipient": "hub",
    "transactionType": "crd-order-select",
    "authorityFrame": "provider-treatment",
    "scope": "patient/Coverage.read",
    "outcome": "approved",
    "subjectPCI": "pci-1",
    "signatures": ["sig-a"]
  },
  {
    "seq": 2,
    "prevHash": "aaa",
    "recordHash": "bbb",
    "timestamp": "2026-07-03T00:00:01Z",
    "sender": "hub",
    "recipient": "payer-org",
    "transactionType": "pas-claim-submit",
    "authorityFrame": "payer-adjudication",
    "scope": "patient/Claim.write",
    "outcome": "pended",
    "subjectPCI": "pci-1",
    "signatures": ["sig-b"]
  },
  {
    "seq": 3,
    "prevHash": "bbb",
    "recordHash": "ccc",
    "timestamp": "2026-07-03T00:00:02Z",
    "sender": "payer-org",
    "recipient": "hub",
    "transactionType": "pas-claim-response",
    "authorityFrame": "payer-adjudication",
    "scope": "patient/ClaimResponse.read",
    "outcome": "approved",
    "subjectPCI": "pci-1",
    "signatures": ["sig-c"]
  }
]`

func TestFetch_DecodesInOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auditor" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixtureJSON))
	}))
	defer srv.Close()

	recs, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	for i, want := range []int{1, 2, 3} {
		if recs[i].Seq != want {
			t.Errorf("recs[%d].Seq = %d, want %d", i, recs[i].Seq, want)
		}
	}
	if recs[0].Sender != "provider-org" || recs[0].TransactionType != "crd-order-select" {
		t.Errorf("recs[0] fields not decoded: %+v", recs[0])
	}
	if recs[2].Outcome != "approved" || recs[2].SubjectPCI != "pci-1" {
		t.Errorf("recs[2] fields not decoded: %+v", recs[2])
	}
}

func TestHighWater(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixtureJSON))
	}))
	defer srv.Close()

	recs, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if hw := HighWater(recs); hw != 3 {
		t.Errorf("HighWater = %d, want 3", hw)
	}
	if hw := HighWater(nil); hw != 0 {
		t.Errorf("HighWater(nil) = %d, want 0", hw)
	}
}

func TestAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixtureJSON))
	}))
	defer srv.Close()

	recs, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got := After(recs, 1)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].Seq != 2 || got[1].Seq != 3 {
		t.Errorf("After(recs, 1) = seqs %d,%d, want 2,3", got[0].Seq, got[1].Seq)
	}
}

func TestFetch_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("Fetch: want error on 500, got nil")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("Fetch error %q does not name the URL %q", err.Error(), srv.URL)
	}
}

func TestFetch_Unreachable(t *testing.T) {
	_, err := Fetch(context.Background(), http.DefaultClient, "http://127.0.0.1:1/unreachable")
	if err == nil {
		t.Fatal("Fetch: want error for unreachable URL, got nil")
	}
}
