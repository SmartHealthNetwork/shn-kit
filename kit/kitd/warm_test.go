package kitd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/internal/testrecord"
)

// The warm-up's answers are what a real multitenant HAPI data server answered
// it (testdata/recordings/README.md), replayed strictly: a request that differs
// from the recorded one in method, path, Content-Type or body gets no answer and
// fails the test, and every recorded exchange must be asked for.
func replayWarm(t *testing.T, name string) *testrecord.Recording {
	t.Helper()
	return testrecord.Load(t, filepath.Join("testdata", "recordings", name))
}

// slowFirstAnswer serves rec, holding the first answer back for delay. It is a
// test-side hook, not part of the recording: a recording carries bytes, not
// timing. On the captured server the real cold first $validate took 1.84 s and
// a warm one 24 ms; the delay here is shorter, and the test that uses it holds
// it inside the warm-up's own deadline.
func slowFirstAnswer(t *testing.T, rec *testrecord.Recording, delay time.Duration) *httptest.Server {
	t.Helper()
	replay := rec.Server().Config.Handler
	var asked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !asked.Swap(true) {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		replay.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The warm-up posts exactly one $validate to the tenant's Patient/$validate:
// the recording holds one exchange, so a second request would get no answer.
func TestWarmValidate_PostsOneValidateAndReportsElapsed(t *testing.T) {
	srv := replayWarm(t, "warm-provider.json").Server()
	var logged []string
	elapsed, err := WarmValidate(context.Background(), srv.URL+"/fhir", "provider", func(f string, a ...any) { logged = append(logged, f) })
	if err != nil {
		t.Fatal(err)
	}
	if elapsed <= 0 || len(logged) != 1 || !strings.Contains(logged[0], "validator warm in") {
		t.Fatalf("elapsed=%s logged=%v", elapsed, logged)
	}
}

// The warm-up warms the server whatever the tenant, and says nothing about the
// tenant. A type-level $validate does not resolve the partition, so the real
// server answered a tenant it has no partition for with the ordinary
// validation outcome (200, one dom-6 warning), and the warm-up succeeds. Its
// contract is only to pay the server-wide validation initialisation; the
// seeder's next request writes to the tenant and fails there.
func TestWarmValidate_WarmsTheServerAndSaysNothingAboutTheTenant(t *testing.T) {
	srv := replayWarm(t, "warm-unknown-tenant.json").Server()
	if _, err := WarmValidate(context.Background(), srv.URL+"/fhir", "nosuchtenant", nil); err != nil {
		t.Fatalf("the server answered the warm-up for a tenant it does not have; the warm-up must pass: %v", err)
	}
}

// A slow first answer inside the warm-up's own deadline is warm: the recorded
// answer, held back as a cold server holds its first $validate.
func TestWarmValidate_AbsorbsASlowFirstAnswer(t *testing.T) {
	srv := slowFirstAnswer(t, replayWarm(t, "warm-provider.json"), 50*time.Millisecond)
	elapsed, err := warmValidate(context.Background(), srv.URL+"/fhir", "provider", 5*time.Second, nil)
	if err != nil {
		t.Fatalf("a slow first answer inside the deadline failed: %v", err)
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("elapsed = %s, want at least the held-back 50ms", elapsed)
	}
}

// The warm-up's deadline is its own: a server slower than it is refused naming it.
// Hand-written fault: a recording holds answers, not hangs.
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

// Hand-written fault: no capture holds a 5xx from the data server's $validate
// (the server does not produce one on demand). This row is a plain-text 500,
// as a proxy in front of the server would answer, and only that. The server's
// own error answers are OperationOutcomes, and a 5xx whose body is an
// OperationOutcome is read as an answer today, so it would count as warm;
// whether it should is open, and no row here pins it either way.
func TestWarmValidate_PlainTextServerErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := WarmValidate(context.Background(), srv.URL+"/fhir", "provider", nil); err == nil {
		t.Fatal("a plain-text 500 (a proxy in front of the server) from $validate must not count as warm")
	}
}
