package runner

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// memberCheckRunner is a Runner whose gateway child answers every request
// with a 400 that is NOT the unknown-member shape, counting what reached it,
// and whose connected-EHR member check is check.
func memberCheckRunner(t *testing.T, check func(context.Context, string) (bool, error)) (*Runner, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"stub gateway"}`))
	}))
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return New(Config{
		Driver: scenariodriver.New(scenariodriver.Config{
			IngressURL: srv.URL, IngressBase: srv.URL,
			ClientID: "kit-runner-test", Key: key,
		}),
		Bus:                  event.NewBus(fixedClock),
		MemberOnConnectedEHR: check,
	}), &hits
}

// conformantIngressRows is every conformant row that sends a seeded member
// through the gateway's Da Vinci ingress, with that member.
var conformantIngressRows = []struct{ uc, branch, member string }{
	{"uc02", "", "MBR-COVERED"},
	{"uc03", "", "MBR-COVERED"},
	{"uc03", "bridge-demo", "MBR-BRIDGE-DEMO"},
	{"uc04", "", "MBR-COVERED"},
	{"uc05", "", "MBR-COVERED"},
	{"uc06", "", "MBR-UC06"},
	{"uc07", "", "MBR-UC07HCPCS"},
	{"uc08", "", "MBR-UC08"},
}

// TestRun_Conformant_MemberNotOnConnectedEHR: under an applied EHR swap, a
// conformant row whose seeded member the partner's server does not carry fails
// with the named sentence and sends nothing. The gateway carries a member its
// system of record does not hold (shn-gateway v0.53.0), so without this check
// the row would reach the payer on data the partner's system does not have.
func TestRun_Conformant_MemberNotOnConnectedEHR(t *testing.T) {
	for _, row := range conformantIngressRows {
		t.Run(row.uc+"/"+row.branch, func(t *testing.T) {
			var asked []string
			rn, hits := memberCheckRunner(t, func(_ context.Context, member string) (bool, error) {
				asked = append(asked, member)
				return false, nil
			})
			res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: row.uc, Branch: row.branch})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.State != StateFailed || !strings.Contains(res.Detail, ConformantMemberNotOnConnectedEHRSentence) {
				t.Fatalf("Result = %s %q, want failed naming %q", res.State, res.Detail, ConformantMemberNotOnConnectedEHRSentence)
			}
			if len(asked) != 1 || asked[0] != row.member {
				t.Fatalf("checked %q, want exactly the row's member %q", asked, row.member)
			}
			if n := hits.Load(); n != 0 {
				t.Fatalf("%d requests reached the gateway, want none", n)
			}
		})
	}
}

// TestRun_Conformant_MemberOnConnectedEHR: the member is there, so the row
// runs as it always has and its own outcome stands.
func TestRun_Conformant_MemberOnConnectedEHR(t *testing.T) {
	rn, hits := memberCheckRunner(t, func(context.Context, string) (bool, error) { return true, nil })
	res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc03"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hits.Load() == 0 {
		t.Fatal("the row sent nothing, want it to run")
	}
	if strings.Contains(res.Detail, ConformantMemberNotOnConnectedEHRSentence) || strings.Contains(res.Detail, "could not check") {
		t.Fatalf("Result.Detail = %q, want the row's own outcome only", res.Detail)
	}
}

// TestRun_Conformant_MemberCheckUnavailable: a check that cannot run is never
// read as "not loaded". The row runs as usual, and its Detail says the check
// could not run, whatever the row's own outcome.
func TestRun_Conformant_MemberCheckUnavailable(t *testing.T) {
	rn, hits := memberCheckRunner(t, func(context.Context, string) (bool, error) {
		return false, errors.New("connected EHR unreachable")
	})
	res, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc03"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hits.Load() == 0 {
		t.Fatal("the row sent nothing, want it to run when the check cannot")
	}
	if strings.Contains(res.Detail, ConformantMemberNotOnConnectedEHRSentence) {
		t.Fatalf("Result.Detail = %q, must not claim the member is missing", res.Detail)
	}
	if !strings.Contains(res.Detail, "could not check your connected EHR for MBR-COVERED: connected EHR unreachable") {
		t.Fatalf("Result.Detail = %q, want it to say the check could not run", res.Detail)
	}

	// The note belongs to that run only.
	rn.cfg.MemberOnConnectedEHR = func(context.Context, string) (bool, error) { return true, nil }
	res, err = rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc03"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Detail, "could not check") {
		t.Fatalf("the next run inherited the note: %q", res.Detail)
	}
}

// TestRun_Conformant_UC01NotChecked: uc01 originates from the gateway's own
// record rather than through its ingress, so it is never checked.
func TestRun_Conformant_UC01NotChecked(t *testing.T) {
	var mu sync.Mutex
	asked := 0
	rn, _ := memberCheckRunner(t, func(context.Context, string) (bool, error) {
		mu.Lock()
		asked++
		mu.Unlock()
		return false, nil
	})
	if _, err := rn.Run(context.Background(), Req{Lane: "conformant", UC: "uc01", Branch: "covered"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if asked != 0 {
		t.Fatalf("uc01 checked the connected EHR %d times, want none", asked)
	}
}
