package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// healthServer serves the observer hub's GET /health {"events":n} shape.
func healthServer(t *testing.T, n int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"events":%d}`, n)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func stampOf(r *Relay) Stamp {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stamp
}

// TestMulti_FansOutStampAndDrainsEvery: a stamp set on the Multi lands on
// every member, ClearStamp clears every member, and Drain succeeds only when
// EVERY member has caught up with its own hub.
func TestMulti_FansOutStampAndDrainsEvery(t *testing.T) {
	bus := event.NewBus(time.Now)
	a := New("http://127.0.0.1:1/events", healthServer(t, 0).URL, bus, t.Logf) // hub emitted 0 → caught up
	b := New("http://127.0.0.1:1/events", healthServer(t, 1).URL, bus, t.Logf) // hub emitted 1, relayed 0 → lagging
	m := NewMulti(a, nil, b)

	want := Stamp{RunID: "run-1", Lane: "provider-data", UC: "uc04"}
	m.SetStamp(want)
	for i, r := range []*Relay{a, b} {
		if got := stampOf(r); got != want {
			t.Fatalf("member %d stamp = %+v, want %+v (fanned out)", i, got, want)
		}
	}
	m.ClearStamp()
	for i, r := range []*Relay{a, b} {
		if got := stampOf(r); got != (Stamp{}) {
			t.Fatalf("member %d stamp = %+v after ClearStamp, want zero", i, got)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := m.Drain(ctx); err == nil {
		t.Fatal("Drain succeeded while member b's hub count (1) exceeds its relayed seq (0) — a Multi must drain EVERY member")
	}
	// Rejection twin: over only the caught-up member, Drain succeeds.
	if err := NewMulti(a).Drain(context.Background()); err != nil {
		t.Fatalf("single caught-up member: Drain = %v, want nil", err)
	}
	// An empty Multi is trivially drained (no children, nothing to wait for).
	if err := NewMulti().Drain(context.Background()); err != nil {
		t.Fatalf("empty Multi: Drain = %v, want nil", err)
	}
}
