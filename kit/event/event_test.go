// event_test.go — hermetic tests for the Kit's ring-buffered SSE event bus
// (ported from gateway/observer/observer_test.go).
package event

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixedClock() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }

// readSSE GETs url (optionally with Last-Event-ID when lastEventID is
// non-empty), scans "data: " lines off the response body, and unmarshals
// each into an Event. It returns after collecting n events or a 5s
// deadline, whichever comes first.
func readSSE(t *testing.T, url, lastEventID string, n int) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	var out []Event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() && len(out) < n {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
			t.Fatalf("unmarshal SSE data %q: %v", line, err)
		}
		out = append(out, e)
	}
	if len(out) != n {
		t.Fatalf("read %d SSE events, want %d: %+v", len(out), n, out)
	}
	return out
}

func TestBus_EmitAssignsSeqAndTime(t *testing.T) {
	b := NewBus(fixedClock)
	b.Emit(Event{Type: "child", Child: "gateway", Detail: "ready"})
	b.Emit(Event{Type: "run.started", RunID: "r1", Lane: "ehr", UC: "UC-01"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	events := readSSE(t, srv.URL+"/events", "", 2)
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("seq = %d,%d want 1,2", events[0].Seq, events[1].Seq)
	}
	if !events[0].Time.Equal(fixedClock()) {
		t.Fatalf("time not stamped from clock: %v", events[0].Time)
	}
	if events[1].Lane != "ehr" || events[1].UC != "UC-01" {
		t.Fatalf("stamps lost: %+v", events[1])
	}
	if events[0].Child != "gateway" || events[0].Detail != "ready" {
		t.Fatalf("child/detail stamps lost: %+v", events[0])
	}
	if events[1].RunID != "r1" {
		t.Fatalf("runID stamp lost: %+v", events[1])
	}
}

// TestBus_EmitPreservesPreStampedTime: Emit only stamps Time from the clock
// when the caller left it zero — a relay that pre-stamps a
// gateway-observer timestamp must survive Emit unchanged.
func TestBus_EmitPreservesPreStampedTime(t *testing.T) {
	b := NewBus(fixedClock)
	preStamped := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	b.Emit(Event{Type: "observer", Time: preStamped})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	events := readSSE(t, srv.URL+"/events", "", 1)
	if !events[0].Time.Equal(preStamped) {
		t.Fatalf("pre-stamped time overwritten: got %v want %v", events[0].Time, preStamped)
	}
	if events[0].Seq != 1 {
		t.Fatalf("seq not assigned despite pre-stamped time: %d", events[0].Seq)
	}
}

func TestBus_LastEventIDReplay(t *testing.T) {
	b := NewBus(fixedClock)
	for i := 0; i < 5; i++ {
		b.Emit(Event{Type: "child", Detail: "n"})
	}
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	events := readSSE(t, srv.URL+"/events", "3", 2) // replay only seq 4,5
	if events[0].Seq != 4 || events[1].Seq != 5 {
		t.Fatalf("replay after 3 = %d,%d want 4,5", events[0].Seq, events[1].Seq)
	}
}

// TestBus_Health: the health endpoint reports the running Seq count.
func TestBus_Health(t *testing.T) {
	b := NewBus(fixedClock)
	b.Emit(Event{Type: "child"})
	b.Emit(Event{Type: "child"})
	b.Emit(Event{Type: "run.finished"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Events uint64 `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health body: %v", err)
	}
	if body.Events != 3 {
		t.Fatalf("/health events = %d, want 3", body.Events)
	}
}

// TestBus_Since: Since(0) returns all buffered events decoded, oldest first,
// Seq ascending; Since(n) filters to Seq > n; and once the ring evicts an
// event (emits beyond bufSize), that event is gone from Since's result too —
// the run-history Recorder's synchronous read path only ever sees
// what the ring still holds.
func TestBus_Since(t *testing.T) {
	b := NewBus(fixedClock)
	for i := 0; i < 5; i++ {
		b.Emit(Event{Type: "child", Detail: fmt.Sprintf("n%d", i)})
	}

	all := b.Since(0)
	if len(all) != 5 {
		t.Fatalf("Since(0) = %d events, want 5", len(all))
	}
	for i, e := range all {
		want := uint64(i + 1)
		if e.Seq != want {
			t.Fatalf("Since(0)[%d].Seq = %d, want %d (oldest first, ascending)", i, e.Seq, want)
		}
	}

	filtered := b.Since(3)
	if len(filtered) != 2 {
		t.Fatalf("Since(3) = %d events, want 2", len(filtered))
	}
	if filtered[0].Seq != 4 || filtered[1].Seq != 5 {
		t.Fatalf("Since(3) seqs = %d,%d want 4,5", filtered[0].Seq, filtered[1].Seq)
	}

	// Evict past bufSize: emit enough events that seq 1 falls out of the ring.
	for i := 0; i < bufSize; i++ {
		b.Emit(Event{Type: "child"})
	}
	evicted := b.Since(0)
	if len(evicted) != bufSize {
		t.Fatalf("Since(0) after eviction = %d events, want %d (ring capacity)", len(evicted), bufSize)
	}
	if evicted[0].Seq == 1 {
		t.Fatalf("Since(0) after eviction still returns seq 1, want it evicted from the ring")
	}
}

// TestBus_EmitNonBlockingUnderSlowSubscriber: a subscriber that never drains
// its channel must not block Emit — fan-out is lossy by design (package
// doc), not backpressuring.
func TestBus_EmitNonBlockingUnderSlowSubscriber(t *testing.T) {
	b := NewBus(fixedClock)
	_, ch, unsub := b.subscribe(0)
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < subDepth+100; i++ {
			b.Emit(Event{Type: "child"})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked on a slow/undrained subscriber")
	}
	_ = ch // intentionally left undrained to exercise the lossy path
}
