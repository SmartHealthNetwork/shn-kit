// Package event is the SHN Kit's run-timeline stream: a ring-buffered SSE
// bus that the observer relay, audit merge, scenario
// runner, and daemon API emit onto and the desktop UI consumes over SSE.
// A ring buffer (last bufSize events) gives
// late/reconnecting subscribers replay via Last-Event-ID; live delivery is
// per-subscriber buffered and LOSSY under backpressure (a slow consumer
// misses events and re-syncs from the buffer on reconnect — the stream is
// diagnostic, never load-bearing for exchange correctness).
//
// This is a deliberate port of gateway/observer/observer.go: same
// concurrency shape (mutex-guarded ring buffer + per-subscriber lossy
// channel fan-out), swapped to the Kit's own Event shape, a larger buffer
// (bufSize 5000 vs the gateway's 1000), and an injected clock so tests are
// hermetic.
package event

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	bufSize  = 5000
	subDepth = 1024
)

// Valid Event.Type values. The observer relay, audit merge, scenario
// runner, and daemon API emit these onto the bus; the desktop UI's SSE
// gate consumes them.
const (
	TypeRunStarted       = "run.started"
	TypeRunFinished      = "run.finished"
	TypeRunFailed        = "run.failed"
	TypeObserver         = "observer"
	TypeAudit            = "audit"
	TypeAuditUnavailable = "audit.unavailable"
	TypeChild            = "child"
	TypeBootstrap        = "bootstrap"
	TypeVerify           = "verify"
)

// Event is one entry on the Kit's run timeline. Exactly one of the payload
// fields (Observer/Audit) is set for those types; both are raw JSON so the
// relay stays byte-faithful to what the gateway/Audit Plane emitted.
type Event struct {
	Seq      uint64          `json:"seq"`
	Time     time.Time       `json:"time"`
	Type     string          `json:"type"`
	RunID    string          `json:"runId,omitempty"`
	Lane     string          `json:"lane,omitempty"`
	UC       string          `json:"uc,omitempty"`
	Child    string          `json:"child,omitempty"`
	Detail   string          `json:"detail,omitempty"`
	Observer json.RawMessage `json:"observer,omitempty"`
	Audit    json.RawMessage `json:"audit,omitempty"`
}

// Bus fan-outs Kit run-timeline events to SSE subscribers. Zero value is not
// usable; call NewBus.
type Bus struct {
	clock func() time.Time

	mu   sync.Mutex
	seq  uint64
	buf  [][]byte // marshaled Events, oldest first, len<=bufSize
	subs map[chan []byte]struct{}
}

// NewBus constructs a Bus. clock is injected (house rule) so callers/tests
// control time; Emit stamps Event.Time from it only when the caller left
// Time zero, so a pre-stamped Event (e.g. a relayed gateway-observer
// timestamp) survives unchanged.
func NewBus(clock func() time.Time) *Bus {
	return &Bus{clock: clock, subs: make(map[chan []byte]struct{})}
}

// Emit assigns the next Seq (always) and Time (only if the caller left it
// zero), buffers the event, and fans it out to live subscribers
// non-blocking.
func (b *Bus) Emit(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.Seq = b.seq
	if e.Time.IsZero() {
		e.Time = b.clock()
	}
	bs, err := json.Marshal(e)
	if err != nil {
		return // Event has no field that can fail to marshal in practice
	}
	b.buf = append(b.buf, bs)
	if len(b.buf) > bufSize {
		b.buf = b.buf[len(b.buf)-bufSize:]
	}
	for ch := range b.subs {
		select {
		case ch <- bs:
		default: // lossy under backpressure by design (see package doc)
		}
	}
}

// Since returns the buffered events with Seq > after, decoded, oldest first —
// the run-history Recorder's synchronous read path. The ring
// (bufSize 5000) comfortably holds any single run under sequential-only v1.
func (b *Bus) Since(after uint64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Event
	for _, bs := range b.buf {
		var e Event
		if json.Unmarshal(bs, &e) == nil && e.Seq > after {
			out = append(out, e)
		}
	}
	return out
}

// subscribe registers a live channel and returns the replay set (buffered
// events with seq > after) plus an unsubscribe func.
func (b *Bus) subscribe(after uint64) ([][]byte, chan []byte, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan []byte, subDepth)
	b.subs[ch] = struct{}{}
	var replay [][]byte
	for _, bs := range b.buf {
		var s struct {
			Seq uint64 `json:"seq"`
		}
		if json.Unmarshal(bs, &s) == nil && s.Seq > after {
			replay = append(replay, bs)
		}
	}
	return replay, ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs, ch)
	}
}

// Handler serves GET /events (SSE) and GET /health.
func (b *Bus) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", b.handleEvents)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		n := b.seq
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"events":%d}`, n)
	})
	return mux
}

func (b *Bus) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	var after uint64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.ParseUint(v, 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	replay, ch, unsub := b.subscribe(after)
	defer unsub()
	write := func(bs []byte) bool {
		var s struct {
			Seq uint64 `json:"seq"`
		}
		_ = json.Unmarshal(bs, &s)
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", s.Seq, bs); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	for _, bs := range replay {
		if !write(bs) {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case bs := <-ch:
			if !write(bs) {
				return
			}
		}
	}
}
