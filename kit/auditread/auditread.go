// Package auditread is a READ VIEW of the substrate's Audit Plane
// tamper-evident chain (GET {auditURL}/auditor), decoded into a LOCAL
// struct rather than importing internal/audit — that wire-decode IS the
// Kit's publish-boundary pattern: the JSON tags mirror
// internal/audit.Record's wire contract, but shnkitd never imports
// the private substrate module.
//
// Two facts callers must hold: the audit chain carries no correlation-id
// field, so Kit run attribution is done by the caller via a
// seq-window (Fetch a HighWater before the run, Fetch again after, then
// After(post, preHW) is the run's records — sound only because Kit runs
// are sequential-only in v1); and the hosted Audit Plane exposes no public
// read route, so callers must pass a reachable (local/in-process) audit
// URL or skip the merge entirely.
package auditread

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Record is one substrate Audit Plane chain entry, as read by the Kit.
// Unknown wire fields (prevHash, recordHash, signatures, ...) are decoded
// and discarded — this struct is intentionally a subset of
// internal/audit.Record's fields.
type Record struct {
	Seq             int    `json:"seq"`
	Timestamp       string `json:"timestamp"`
	Sender          string `json:"sender"`
	Recipient       string `json:"recipient"`
	TransactionType string `json:"transactionType"`
	AuthorityFrame  string `json:"authorityFrame"`
	Scope           string `json:"scope"`
	Outcome         string `json:"outcome"`
	SubjectPCI      string `json:"subjectPCI"`
}

// Fetch retrieves the Audit Plane's chain records from GET
// {auditURL}/auditor. Errors are wrapped with the URL for diagnosability.
func Fetch(ctx context.Context, hc *http.Client, auditURL string) ([]Record, error) {
	url := auditURL + "/auditor"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("auditread: building request for %s: %w", url, err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auditread: fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auditread: %s returned status %d", url, resp.StatusCode)
	}
	var recs []Record
	if err := json.NewDecoder(resp.Body).Decode(&recs); err != nil {
		return nil, fmt.Errorf("auditread: decoding response from %s: %w", url, err)
	}
	return recs, nil
}

// HighWater returns the maximum Seq in recs, or 0 when recs is empty.
func HighWater(recs []Record) int {
	hw := 0
	for _, r := range recs {
		if r.Seq > hw {
			hw = r.Seq
		}
	}
	return hw
}

// After returns the records in recs with Seq strictly greater than seq,
// preserving chain order.
func After(recs []Record, seq int) []Record {
	var out []Record
	for _, r := range recs {
		if r.Seq > seq {
			out = append(out, r)
		}
	}
	return out
}
