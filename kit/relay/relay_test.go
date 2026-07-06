// relay_test.go — hermetic tests for the observer SSE relay.
// Fake observer servers are httptest only; no real gateway.
package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

func fixedClock() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }

func testLogf(t *testing.T) func(string, ...any) {
	return func(format string, args ...any) {
		t.Logf(format, args...)
	}
}

// readSSE GETs url, scans "data: " lines off the response body, and
// unmarshals each into an event.Event. It returns after collecting n
// events or a 5s deadline, whichever comes first. Mirrors kit/event's
// helper of the same name/shape.
func readSSE(t *testing.T, url string, n int) []event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	var out []event.Event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() && len(out) < n {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e event.Event
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

// compact json.Compacts b for byte-identity comparison (whitespace-blind).
func compact(t *testing.T, b []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatalf("json.Compact(%s): %v", b, err)
	}
	return buf.String()
}

// frame is one SSE id/data pair as the gateway/observer/observer.go server
// writes it: "id: <seq>\ndata: <json>\n\n".
type frame struct {
	id   string
	data string
}

func writeFrame(w http.ResponseWriter, fl http.Flusher, f frame) {
	fmt.Fprintf(w, "id: %s\ndata: %s\n\n", f.id, f.data)
	fl.Flush()
}

// fakeObserverServer writes frames once per connection then blocks until
// the request's context is done — mirroring the real handler's long-lived
// stream (gateway/observer/observer.go handleEvents), so the relay's
// reconnect loop never sees a spurious EOF mid-test.
func fakeObserverServer(t *testing.T, frames []frame) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		for _, f := range frames {
			writeFrame(w, fl, f)
		}
		<-r.Context().Done()
	})
	return httptest.NewServer(mux)
}

// TestRelay_RelaysAndStamps: row 1 — a fake observer server emits two
// frames; with SetStamp active, both arrive on the bus as Type:"observer"
// stamped with the run's RunID/Lane/UC, and Observer is byte-identical
// (modulo json.Compact) to the fake's raw data payload — never re-marshaled.
func TestRelay_RelaysAndStamps(t *testing.T) {
	frames := []frame{
		{id: "1", data: `{"seq":1,"kind":"leg.originated","legType":"crd-order-select","correlationId":"c1"}`},
		{id: "2", data: `{"seq":2,"kind":"validate.result","detail":"valid"}`},
	}
	srv := fakeObserverServer(t, frames)

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())

	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))
	r.SetStamp(Stamp{RunID: "r1", Lane: "conformant", UC: "UC-02"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		srv.Close()
		busSrv.Close()
	})
	go r.Run(ctx)

	events := readSSE(t, busSrv.URL+"/events", 2)
	for i, e := range events {
		if e.Type != event.TypeObserver {
			t.Errorf("event %d Type = %q, want %q", i, e.Type, event.TypeObserver)
		}
		if e.RunID != "r1" || e.Lane != "conformant" || e.UC != "UC-02" {
			t.Errorf("event %d stamp = RunID:%q Lane:%q UC:%q, want r1/conformant/UC-02", i, e.RunID, e.Lane, e.UC)
		}
		got := compact(t, e.Observer)
		want := compact(t, []byte(frames[i].data))
		if got != want {
			t.Errorf("event %d Observer = %s, want %s (not byte-faithful)", i, got, want)
		}
	}
}

// TestRelay_UnstampedPassthrough: row 2 — without SetStamp, events still
// relay onto the bus, with empty RunID/Lane/UC (boot-time noise is
// inspector content).
func TestRelay_UnstampedPassthrough(t *testing.T) {
	frames := []frame{
		{id: "1", data: `{"seq":1,"kind":"leg.originated"}`},
	}
	srv := fakeObserverServer(t, frames)

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())

	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		srv.Close()
		busSrv.Close()
	})
	go r.Run(ctx)

	events := readSSE(t, busSrv.URL+"/events", 1)
	e := events[0]
	if e.Type != event.TypeObserver {
		t.Errorf("Type = %q, want %q", e.Type, event.TypeObserver)
	}
	if e.RunID != "" || e.Lane != "" || e.UC != "" {
		t.Errorf("stamp = RunID:%q Lane:%q UC:%q, want all empty (unstamped passthrough)", e.RunID, e.Lane, e.UC)
	}
}

// TestRelay_ReconnectsWithLastEventID: row 3 — the fake server serves frame
// 1 then closes the connection; on the SECOND request it asserts
// Last-Event-ID: 1 and serves frame 2. Both events reach the bus exactly
// once, in order.
func TestRelay_ReconnectsWithLastEventID(t *testing.T) {
	var mu sync.Mutex
	conns := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		conns++
		n := conns
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()

		if n == 1 {
			writeFrame(w, fl, frame{id: "1", data: `{"seq":1,"kind":"leg.originated"}`})
			return // close the connection; relay must reconnect
		}

		if got := r.Header.Get("Last-Event-ID"); got != "1" {
			t.Errorf("second connection Last-Event-ID = %q, want %q", got, "1")
		}
		writeFrame(w, fl, frame{id: "2", data: `{"seq":2,"kind":"validate.result"}`})
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())

	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		srv.Close()
		busSrv.Close()
	})
	go r.Run(ctx)

	events := readSSE(t, busSrv.URL+"/events", 2)
	if !strings.Contains(string(events[0].Observer), `"seq":1`) {
		t.Errorf("event 0 Observer = %s, want seq:1 payload", events[0].Observer)
	}
	if !strings.Contains(string(events[1].Observer), `"seq":2`) {
		t.Errorf("event 1 Observer = %s, want seq:2 payload", events[1].Observer)
	}
	if events[0].Seq >= events[1].Seq {
		t.Errorf("events out of order: seq %d then %d", events[0].Seq, events[1].Seq)
	}

	mu.Lock()
	got := conns
	mu.Unlock()
	if got != 2 {
		t.Errorf("connections = %d, want 2 (one reconnect)", got)
	}

	// row 5 extension: LastSeq must reflect the resumed stream's last
	// delivered id — pins that the uint64 refactor still feeds the resume
	// header correctly. lastSeq is written AFTER bus.Emit (relay.go), so the
	// event-2 lastSeq write can lag the bus delivery readSSE just observed;
	// poll rather than reading it the instant readSSE returns (only 2 frames
	// exist, so >=2 is exactly 2). Otherwise this races under load.
	waitForLastSeq(t, r, 2)
}

// waitForLastSeq polls r.LastSeq() until it reaches (or exceeds) want, or
// fails the test after 5s.
func waitForLastSeq(t *testing.T, r *Relay, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.LastSeq() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("LastSeq() did not reach %d within 5s (got %d)", want, r.LastSeq())
}

// ---- Drain -------------------------------------------------------------------

// TestRelay_DrainFastPath: row 1 — the fixture emits 3 frames (ids 1..3) and
// its health reports 3. Once the relay has caught up, Drain returns
// immediately (the fast path: LastSeq() >= h.Events on the first check).
func TestRelay_DrainFastPath(t *testing.T) {
	frames := []frame{
		{id: "1", data: `{"seq":1}`},
		{id: "2", data: `{"seq":2}`},
		{id: "3", data: `{"seq":3}`},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		for _, f := range frames {
			writeFrame(w, fl, f)
		}
		<-r.Context().Done()
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"events":3}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForLastSeq(t, r, 3)

	dctx, dcancel := context.WithTimeout(context.Background(), time.Second)
	defer dcancel()
	if err := r.Drain(dctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// TestRelay_DrainBlocksUntilTailFrameLands: row 2 — health reports 2 from
// the start; the fixture writes frame 1 immediately but withholds frame 2
// until the test, having invoked Drain in a goroutine, closes a gate ~50ms
// later. Drain must return nil only once frame 2 has ALREADY been relayed
// onto the bus (the barrier's whole point) — asserted by reading both events
// back off the bus after Drain returns.
func TestRelay_DrainBlocksUntilTailFrameLands(t *testing.T) {
	gate := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		writeFrame(w, fl, frame{id: "1", data: `{"seq":1}`})
		<-gate
		writeFrame(w, fl, frame{id: "2", data: `{"seq":2}`})
		<-r.Context().Done()
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"events":2}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForLastSeq(t, r, 1)

	drainDone := make(chan error, 1)
	go func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer dcancel()
		drainDone <- r.Drain(dctx)
	}()

	// Give Drain time to fetch /health (events:2), observe LastSeq()==1, and
	// park in its poll loop before we let frame 2 through.
	time.Sleep(50 * time.Millisecond)
	close(gate)

	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return")
	}

	events := readSSE(t, busSrv.URL+"/events", 2)
	if !strings.Contains(string(events[1].Observer), `"seq":2`) {
		t.Fatalf("event 1 Observer = %s, want seq:2 (Drain must not return before the tail frame is on the bus)", events[1].Observer)
	}
}

// TestRelay_DrainTimeout: row 3 — health reports 5, but only 3 frames are
// ever sent. Drain under a 200ms-deadline ctx must return an error naming
// both counters and wrapping context.DeadlineExceeded.
func TestRelay_DrainTimeout(t *testing.T) {
	frames := []frame{
		{id: "1", data: `{"seq":1}`},
		{id: "2", data: `{"seq":2}`},
		{id: "3", data: `{"seq":3}`},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		for _, f := range frames {
			writeFrame(w, fl, f)
		}
		<-r.Context().Done()
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"events":5}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForLastSeq(t, r, 3)

	dctx, dcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer dcancel()
	err := r.Drain(dctx)
	if err == nil {
		t.Fatal("Drain: want a timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "relayed seq 3") {
		t.Errorf("Drain error = %v, want it to mention the relayed count (3)", err)
	}
	if !strings.Contains(err.Error(), "emitted count 5") {
		t.Errorf("Drain error = %v, want it to mention the gateway's emitted count (5)", err)
	}
}

// TestRelay_DrainHealthFetchFailure: row 4 — healthURL points at a port with
// nothing listening. Drain must return an error naming that URL.
func TestRelay_DrainHealthFetchFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	closedURL := "http://" + ln.Addr().String() + "/health"
	ln.Close() // guaranteed nothing is listening on this port now

	bus := event.NewBus(fixedClock)
	r := New("http://127.0.0.1:1/events", closedURL, bus, testLogf(t))

	err = r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: want an error for an unreachable health URL")
	}
	if !strings.Contains(err.Error(), closedURL) {
		t.Fatalf("Drain error = %v, want it to name the health URL %q", err, closedURL)
	}
}

// TestRelay_DrainHealthDecodeFailure (T-11 coverage fold): the health
// endpoint responds 200 but with a body that is not valid JSON (a
// misbehaving or non-Kit-aware endpoint on the other end) — Drain must
// surface a decode error naming the health URL, distinct from the
// fetch-failure branch above.
func TestRelay_DrainHealthDecodeFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `not json`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	r := New("http://127.0.0.1:1/events", srv.URL+"/health", bus, testLogf(t))

	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: want a decode error for a non-JSON health body")
	}
	if !strings.Contains(err.Error(), srv.URL+"/health") {
		t.Fatalf("Drain error = %v, want it to name the health URL", err)
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Fatalf("Drain error = %v, want it to identify itself as a decode failure", err)
	}
}

// TestRelay_ResetCursorFreshEpoch: the relay is
// caught up to LastSeq()==5 against "epoch 1" of a fixture. ResetCursor is
// called, then the fixture flips to "epoch 2" (ids restart at 1, health
// reports 2) — simulating a gateway-child restart on the SAME address. The
// reconnect must carry NO Last-Event-ID (asserted fixture-side on both
// epochs), both epoch-2 frames must relay, LastSeq() must land on 2 (not a
// stale 5+2), and a Drain against the new epoch's count must genuinely wait
// (not return instantly on stale-high state).
func TestRelay_ResetCursorFreshEpoch(t *testing.T) {
	var mu sync.Mutex
	epoch := 1
	restart := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		e := epoch
		mu.Unlock()
		lastEventID := r.Header.Get("Last-Event-ID")

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()

		if e == 1 {
			if lastEventID != "" {
				t.Errorf("epoch-1 connection Last-Event-ID = %q, want empty (fresh connect)", lastEventID)
			}
			for _, f := range []frame{
				{id: "1", data: `{"seq":1}`},
				{id: "2", data: `{"seq":2}`},
				{id: "3", data: `{"seq":3}`},
				{id: "4", data: `{"seq":4}`},
				{id: "5", data: `{"seq":5}`},
			} {
				writeFrame(w, fl, f)
			}
			<-restart // hold the connection open until the test signals the "restart"
			return    // hang up: the relay sees EOF and reconnects
		}

		// epoch 2: a freshly-restarted hub. The reconnect after ResetCursor
		// must carry NO Last-Event-ID — a stale cursor would ask this new
		// epoch's hub to resume from a seq it never had.
		if lastEventID != "" {
			t.Errorf("epoch-2 connection Last-Event-ID = %q, want empty (fresh epoch after ResetCursor)", lastEventID)
		}
		writeFrame(w, fl, frame{id: "1", data: `{"seq":1}`})
		writeFrame(w, fl, frame{id: "2", data: `{"seq":2}`})
		<-r.Context().Done()
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		e := epoch
		mu.Unlock()
		if e == 1 {
			fmt.Fprint(w, `{"events":5}`)
		} else {
			fmt.Fprint(w, `{"events":2}`)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForLastSeq(t, r, 5)

	r.ResetCursor()
	if got := r.LastSeq(); got != 0 {
		t.Fatalf("LastSeq() after ResetCursor = %d, want 0", got)
	}

	mu.Lock()
	epoch = 2
	mu.Unlock()
	close(restart) // let the epoch-1 handler hang up now; the relay reconnects

	// Drain is invoked WHILE the reconnect is still in flight — it must
	// genuinely wait for the new epoch's frames, not instantly return on a
	// stale-high comparison.
	dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dcancel()
	if err := r.Drain(dctx); err != nil {
		t.Fatalf("Drain against the new epoch: %v", err)
	}
	if got := r.LastSeq(); got != 2 {
		t.Fatalf("LastSeq() after the new epoch relayed = %d, want 2 (not stale-high)", got)
	}
}

// waitForBusEvents polls busHealthURL (a *event.Bus's GET /health) until its
// reported {"events":N} count reaches (or exceeds) want, or fails the test
// after 5s. Used instead of a timing sleep to know precisely when a
// fixture-emitted frame has actually landed on the bus (polling
// rather than a "long enough" sleep).
func waitForBusEvents(t *testing.T, busHealthURL string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(busHealthURL)
		if err == nil {
			var h struct {
				Events int `json:"events"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&h)
			resp.Body.Close()
			if h.Events >= want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("bus events did not reach %d within 5s", want)
}

// TestRelay_StaleConnectionEmitsWithoutAdvancingCursor covers the generation
// fence. A single connection delivers frame id
// 3 (LastSeq reaches 3); ResetCursor is then called (simulating the gateway
// child dying mid-connection: a new epoch begins) WHILE that SAME connection
// is still alive and goes on to deliver two more frames, ids 4 and 5 (kept
// consecutive with id 3 deliberately, so the pre-existing per-connection gap
// check (connPrev-based) does not itself intercept them —
// this test isolates the epoch fence, not the unrelated gap check). Both
// stale-epoch frames must still be emitted onto the bus byte-faithfully (a
// dying child's already-buffered frames are real observed data), but must
// NOT advance the cursor: LastSeq() must stay 0 throughout. The connection
// then ends (fixture returns rather than blocking on ctx.Done()), the relay
// reconnects, and the fixture's second serve asserts NO Last-Event-ID header
// (proving the fresh epoch truly starts clean) and replays fresh-epoch ids
// 1..2, which DO advance the (now-current-generation) cursor to 2.
func TestRelay_StaleConnectionEmitsWithoutAdvancingCursor(t *testing.T) {
	afterReset := make(chan struct{})
	var mu sync.Mutex
	conns := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		conns++
		n := conns
		mu.Unlock()
		lastEventID := r.Header.Get("Last-Event-ID")

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()

		if n == 1 {
			// Connection 1: the dying child's connection. First frame (id 3)
			// is this connection's first ever, so it is gap-exempt regardless
			// of value (mirrors TestRelay_FirstFrameGapExempt).
			writeFrame(w, fl, frame{id: "3", data: `{"seq":3}`})
			<-afterReset // held open until the test has called ResetCursor()
			// Deliver two more frames on this SAME (stale-epoch) connection,
			// consecutive with id 3 so the unrelated gap check does not fire.
			writeFrame(w, fl, frame{id: "4", data: `{"seq":4}`})
			writeFrame(w, fl, frame{id: "5", data: `{"seq":5}`})
			return // hang up: the relay sees EOF and reconnects
		}

		// Connection 2 (the reconnect): must carry NO Last-Event-ID — the
		// fence proves the reset cursor (still 0 after the stale-epoch
		// frames), not a stale one, drove the resume header. Fresh epoch,
		// ids restart at 1.
		if lastEventID != "" {
			t.Errorf("reconnect Last-Event-ID = %q, want empty (fresh epoch after ResetCursor)", lastEventID)
		}
		writeFrame(w, fl, frame{id: "1", data: `{"seq":1,"epoch":2}`})
		writeFrame(w, fl, frame{id: "2", data: `{"seq":2,"epoch":2}`})
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForLastSeq(t, r, 3)

	r.ResetCursor()
	if got := r.LastSeq(); got != 0 {
		t.Fatalf("LastSeq() immediately after ResetCursor = %d, want 0", got)
	}
	close(afterReset) // let the stale connection deliver frames 4 and 5

	// Wait for both stale-epoch frames to actually land on the bus (3 total
	// events so far: 3, 4, 5), then assert the cursor never moved off 0.
	waitForBusEvents(t, busSrv.URL+"/health", 3)
	if got := r.LastSeq(); got != 0 {
		t.Fatalf("LastSeq() after stale-epoch frames 4/5 = %d, want 0 (the fence must not let a dying connection's frames advance the reset cursor)", got)
	}

	// The reconnect (fresh epoch) DOES advance the cursor normally.
	waitForLastSeq(t, r, 2)

	events := readSSE(t, busSrv.URL+"/events", 5)
	wantSeqs := []string{`"seq":3`, `"seq":4`, `"seq":5`, `"seq":1,"epoch":2`, `"seq":2,"epoch":2`}
	for i, want := range wantSeqs {
		if !strings.Contains(string(events[i].Observer), want) {
			t.Errorf("event %d Observer = %s, want to contain %s", i, events[i].Observer, want)
		}
	}
}

// TestRelay_GapDetectionReconnects covers the gap check: one
// connection delivers ids 1, 2, 4 (skipping 3). The relay must emit 1 and 2,
// must NOT emit 4, must drop the connection, and must reconnect with
// Last-Event-ID: 2 — the fixture's second connection then back-fills 3 and
// 4, so all four land on the bus exactly once.
func TestRelay_GapDetectionReconnects(t *testing.T) {
	var mu sync.Mutex
	conns := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		conns++
		n := conns
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()

		if n == 1 {
			writeFrame(w, fl, frame{id: "1", data: `{"seq":1}`})
			writeFrame(w, fl, frame{id: "2", data: `{"seq":2}`})
			writeFrame(w, fl, frame{id: "4", data: `{"seq":4}`}) // gap: skips 3
			<-r.Context().Done()
			return
		}

		if got := r.Header.Get("Last-Event-ID"); got != "2" {
			t.Errorf("reconnect Last-Event-ID = %q, want %q (must roll back to the last GOOD frame)", got, "2")
		}
		writeFrame(w, fl, frame{id: "3", data: `{"seq":3}`})
		writeFrame(w, fl, frame{id: "4", data: `{"seq":4}`})
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	events := readSSE(t, busSrv.URL+"/events", 4)
	seen := map[string]bool{}
	for _, e := range events {
		var payload struct {
			Seq int `json:"seq"`
		}
		if err := json.Unmarshal(e.Observer, &payload); err != nil {
			t.Fatalf("unmarshal observer payload %s: %v", e.Observer, err)
		}
		key := fmt.Sprintf("%d", payload.Seq)
		if seen[key] {
			t.Fatalf("seq %d relayed more than once", payload.Seq)
		}
		seen[key] = true
	}
	for _, want := range []string{"1", "2", "3", "4"} {
		if !seen[want] {
			t.Errorf("seq %s never relayed", want)
		}
	}
}

// TestRelay_FirstFrameGapExempt: row 7 (first-frame exemption) — a brand new
// connection whose FIRST frame starts at id 7 (hub-ring-eviction shape) must
// be accepted without tripping the gap check; connPrev==0 exempts it.
func TestRelay_FirstFrameGapExempt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		writeFrame(w, fl, frame{id: "7", data: `{"seq":7}`})
		writeFrame(w, fl, frame{id: "8", data: `{"seq":8}`})
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := event.NewBus(fixedClock)
	busSrv := httptest.NewServer(bus.Handler())
	defer busSrv.Close()

	r := New(srv.URL+"/events", srv.URL+"/health", bus, testLogf(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	events := readSSE(t, busSrv.URL+"/events", 2)
	if !strings.Contains(string(events[0].Observer), `"seq":7`) || !strings.Contains(string(events[1].Observer), `"seq":8`) {
		t.Fatalf("events = %+v, want seq 7 then 8 relayed without a gap trip (first-frame exemption)", events)
	}
	if got := r.LastSeq(); got != 8 {
		t.Fatalf("LastSeq() = %d, want 8", got)
	}
}
