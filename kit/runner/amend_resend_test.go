// amend_resend_test.go — the Da Vinci lane's amendment re-send (submitAmendment):
// the runner, as the requester, sends a PAS amendment once more after the payer's
// 409 (Conflict), under a new correlation id, and never for any other answer.
// The gateway relaying it never re-sends on the requester's behalf; the
// gateway's own tests pin that.
package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// amendRows are the rows that amend a held request through the ingress.
var amendRows = []string{"uc04", "uc05", "uc06"}

// amendBodies returns the amendments the ingress received, in order.
func (i *dvIngress) amendBodies(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, b := range i.submits() {
		if _, amend := dvSubmitOrder(t, b); amend {
			out = append(out, b)
		}
	}
	return out
}

// dvClaimCorrelation reads the Claim's own urn:shn:correlation identifier, the
// id the ingress keys the amendment's leg on.
func dvClaimCorrelation(t *testing.T, bundle string) string {
	t.Helper()
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
				Identifier   []struct {
					System string `json:"system"`
					Value  string `json:"value"`
				} `json:"identifier"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal([]byte(bundle), &b); err != nil {
		t.Fatalf("parse amendment: %v", err)
	}
	for _, e := range b.Entry {
		if e.Resource.ResourceType != "Claim" {
			continue
		}
		for _, id := range e.Resource.Identifier {
			if id.System == "urn:shn:correlation" {
				return id.Value
			}
		}
	}
	t.Fatalf("amendment Claim carries no urn:shn:correlation identifier")
	return ""
}

// resentEvents returns the run.resent events on the runner's bus.
func resentEvents(rn *Runner) []event.Event {
	var out []event.Event
	for _, e := range rn.cfg.Bus.Since(0) {
		if e.Type == event.TypeRunResent {
			out = append(out, e)
		}
	}
	return out
}

// A 409 on the amendment is sent once more under a new correlation id, rebuilt
// with nothing else changed, and the payer's answer to that attempt is what the
// row reads. The re-send is on the run timeline naming both attempts.
func TestAmendment_ConflictResentOnce(t *testing.T) {
	for _, uc := range amendRows {
		t.Run(uc, func(t *testing.T) {
			ing := newDVIngress(t)
			ing.amendAnswer = func(n int) (int, bool) {
				if n == 1 {
					return 409, false
				}
				return 0, false
			}
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: uc})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StatePassed || !strings.Contains(res.Detail, "AUTH-2001") {
				t.Fatalf("the re-send's answer must decide the row: state=%s detail=%q", res.State, res.Detail)
			}
			amends := ing.amendBodies(t)
			if len(amends) != 2 {
				t.Fatalf("want exactly two amendment attempts, got %d", len(amends))
			}
			first, second := dvClaimCorrelation(t, amends[0]), dvClaimCorrelation(t, amends[1])
			prefix := "kit-" + uc + "-amend-"
			if first == second || !strings.HasPrefix(first, prefix) || !strings.HasPrefix(second, prefix) {
				t.Fatalf("the re-send needs a new correlation id minted like the first: first=%q second=%q", first, second)
			}
			if strings.ReplaceAll(amends[0], first, second) != amends[1] {
				t.Errorf("the re-send differs from the refused amendment in more than its correlation id")
			}
			evs := resentEvents(rn)
			if len(evs) != 1 {
				t.Fatalf("want one %s event, got %+v", event.TypeRunResent, evs)
			}
			e := evs[0]
			if e.RunID != res.RunID || e.Lane != "conformant" || e.UC != uc {
				t.Errorf("%s event not stamped with its run: %+v", event.TypeRunResent, e)
			}
			var d resentDetail
			if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
				t.Fatalf("%s detail %q: %v", event.TypeRunResent, e.Detail, err)
			}
			if d != (resentDetail{CorrelationID: second, RefusedCorrelationID: first, Status: 409}) {
				t.Errorf("%s detail = %+v, want the re-send %q linked to the refused %q", event.TypeRunResent, d, second, first)
			}
		})
	}
}

// A second 409 is the row's error, naming the payer's status and its words,
// after exactly two attempts: the runner re-sends once, never more.
func TestAmendment_SecondConflictSurfaces(t *testing.T) {
	for _, uc := range amendRows {
		t.Run(uc, func(t *testing.T) {
			ing := newDVIngress(t)
			ing.amendAnswer = func(int) (int, bool) { return 409, false }
			rn := dvRunner(t, ing, nil)
			res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: uc})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := len(ing.amendBodies(t)); got != 2 {
				t.Fatalf("want exactly two amendment attempts, got %d", got)
			}
			if res.State != StateFailed || !strings.Contains(res.Detail, "status 409") || !strings.Contains(res.Detail, "HAPI-0989") || !strings.Contains(res.Detail, "sent once more") {
				t.Fatalf("a second 409 must fail the row naming it: state=%s detail=%q", res.State, res.Detail)
			}
			if got := len(resentEvents(rn)); got != 1 {
				t.Errorf("want one %s event, got %d", event.TypeRunResent, got)
			}
		})
	}
}

// Only a 409 is sent again: any other refusal is the row's error after one
// attempt, and a transport error is never retried (the runner cannot tell whether
// the payer received it).
func TestAmendment_OnlyConflictIsResent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		hangup  bool
		wantErr string
	}{
		{"unprocessable-422", 422, false, "status 422"},
		{"precondition-412", 412, false, "status 412"},
		{"server-error-500", 500, false, "status 500"},
		{"transport-error", 0, true, "submit amended re-POST"},
	} {
		for _, uc := range amendRows {
			t.Run(tc.name+"/"+uc, func(t *testing.T) {
				ing := newDVIngress(t)
				ing.amendAnswer = func(int) (int, bool) { return tc.status, tc.hangup }
				rn := dvRunner(t, ing, nil)
				res, err := rn.Run(t.Context(), Req{Lane: "conformant", UC: uc})
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if got := len(ing.amendBodies(t)); got != 1 {
					t.Fatalf("want exactly one amendment attempt, got %d", got)
				}
				if res.State != StateFailed || !strings.Contains(res.Detail, tc.wantErr) || strings.Contains(res.Detail, "sent once more") {
					t.Fatalf("state=%s detail=%q, want failed naming %q after one attempt", res.State, res.Detail, tc.wantErr)
				}
				if got := len(resentEvents(rn)); got != 0 {
					t.Errorf("want no %s event, got %d", event.TypeRunResent, got)
				}
			})
		}
	}
}
