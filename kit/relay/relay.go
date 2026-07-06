// Package relay consumes a Smart Gateway's loopback observer stream and
// re-emits each event onto the Kit event bus, stamped
// with the active run's identity (run id, lane, UC) — facts the
// gateway cannot know and shnkitd can (it is the thing driving the run;
// sequential-only v1 makes the attribution unambiguous).
//
// PAYER-ROLE GATEWAYS ARE VALIDATION-ONLY ON THIS STREAM, BY DESIGN:
// the engine instruments origination legs, the Da Vinci
// ingress routes, and $validate calls — NOT handleInbound. A payer-role
// gateway therefore emits only validate.result events for the legs it
// answers. Consumers (the flow inspector) must render hosted/payer-side
// hops from the provider gateway's own leg events — "shown, never
// faked" — and must never treat payer-side leg silence as a fault.
package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// reconnectDelay is how long Run waits between a dropped/ended connection
// and the next reconnect attempt.
const reconnectDelay = 500 * time.Millisecond

// drainPoll is Drain's re-check cadence while waiting for the stream to
// catch up with the hub's emitted count.
const drainPoll = 10 * time.Millisecond

// Stamp is the active run's identity (run id, lane, UC under test),
// applied to every observer event relayed while it is set (package doc).
type Stamp struct {
	RunID string
	Lane  string
	UC    string
}

// Relay is an SSE client for one Smart Gateway's observer stream. Zero
// value is not usable; construct with New. Safe for concurrent use: Run
// runs the reconnect loop while SetStamp/ClearStamp are called from other
// goroutines (e.g. the scenario runner bracketing a row).
type Relay struct {
	url       string
	healthURL string
	bus       *event.Bus
	logf      func(string, ...any)
	hc        *http.Client

	mu      sync.Mutex
	stamp   Stamp
	lastSeq uint64
	gen     uint64
}

// New constructs a Relay that streams eventsURL (a gateway's
// {OBSERVER_ADDR}/events endpoint) and re-emits frames onto bus. healthURL is
// the observer hub's GET /health — the relay drain barrier's counter,
// consulted only by Drain.
func New(eventsURL, healthURL string, bus *event.Bus, logf func(string, ...any)) *Relay {
	return &Relay{
		url:       eventsURL,
		healthURL: healthURL,
		bus:       bus,
		logf:      logf,
		hc:        &http.Client{},
	}
}

// SetStamp sets the identity attached to subsequently relayed events.
func (r *Relay) SetStamp(s Stamp) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stamp = s
}

// ClearStamp clears the active run's identity; subsequently relayed events
// pass through unstamped (boot-time/idle noise is inspector content)
// until the next SetStamp.
func (r *Relay) ClearStamp() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stamp = Stamp{}
}

// LastSeq returns the highest gateway-observer seq this relay has emitted
// onto the bus (0 before the first frame). The seq is the hub's SSE id.
func (r *Relay) LastSeq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSeq
}

// ResetCursor zeroes the relay's seq cursor. Call it when the gateway child
// restarts: the observer hub's counter is per-process, so a respawned child
// starts a NEW seq epoch — a stale-high cursor would both suppress the fresh
// hub's replay (Last-Event-ID filter) and make Drain compare across epochs
// and return falsely early.
//
// Zeroing the cursor does not itself force a reconnect (nor does the gen
// bump below). It doesn't need to: the in-flight connection is to the DEAD child's
// socket, which is already failing (the child exited), so Run's reconnect
// loop naturally re-dials — and with the cursor now zero, that re-dial
// carries no Last-Event-ID.
//
// ResetCursor also bumps gen: the DYING
// child's stream() goroutine may still be mid-connection with already
// -buffered frames from the OLD epoch queued up. Without a generation fence,
// that goroutine's ordinary "r.lastSeq = parsedID" write would re-raise the
// cursor to a stale-epoch value right after this reset, silently defeating
// it (a stale Last-Event-ID would suppress the fresh hub's replay, and Drain
// could pass falsely against the wrong epoch's count). See stream()'s fence
// comment for the other half of this invariant.
func (r *Relay) ResetCursor() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSeq = 0
	r.gen++
}

// Drain blocks until every observer event the gateway has emitted SO FAR has
// been relayed onto the bus: it reads the hub's GET /health {"events":N}
// once, then waits for LastSeq() >= N. Sound because each seam's
// emission is sequenced before its triggering HTTP response completes, so a
// caller that has seen its last response returns knows its events all have
// seq <= N. ctx bounds the wait; the error names both counters for
// diagnosability.
//
// Two assumptions this soundness rests on:
//
// (a) Delivery on the current connection is treated as in-order and
// gap-checked — a hub-side drop is healed by the gap-triggered reconnect
// (stream's connPrev check), and only an undetectable drop of the FINAL
// frames of a connection (no later frame ever arrives to reveal the gap)
// degrades Drain to its honest timeout rather than a false "caught up".
//
// (b) The ingress happens-before holds because net/http finalizes responses
// after the outer ServeHTTP handler returns — with no handler-set
// Content-Length, bodies are chunked and the TERMINATING chunk only lands
// after ServeHTTP returns, so the ordering (emit-before-response-completes)
// holds even when large bodies flush early. It would break only if an
// engine ingress handler ever set Content-Length and flushed before
// returning (none does today; grep `WriteHeader\|Content-Length` under
// gateway/engine before trusting a new handler on this assumption).
func (r *Relay) Drain(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.healthURL, nil)
	if err != nil {
		return fmt.Errorf("kit/relay: drain: build health request %s: %w", r.healthURL, err)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return fmt.Errorf("kit/relay: drain: fetch %s: %w", r.healthURL, err)
	}
	defer resp.Body.Close()
	var h struct {
		Events uint64 `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return fmt.Errorf("kit/relay: drain: decode %s: %w", r.healthURL, err)
	}
	for {
		if r.LastSeq() >= h.Events {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kit/relay: drain: relayed seq %d has not reached the gateway's emitted count %d: %w", r.LastSeq(), h.Events, ctx.Err())
		case <-time.After(drainPoll):
		}
	}
}

// Run is a blocking reconnect loop: stream one connection to the observer
// endpoint, and on that connection ending (error or clean EOF) log via
// logf and sleep reconnectDelay (respecting ctx) before reconnecting —
// resuming with Last-Event-ID so no frame is missed or duplicated across
// the reconnect. Returns once ctx is done.
func (r *Relay) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := r.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.logf("kit/relay: observer stream %s ended with error: %v", r.url, err)
		} else {
			r.logf("kit/relay: observer stream %s closed; reconnecting", r.url)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// stream makes one connection to the observer endpoint and relays frames
// onto the bus, stamped under mu per-frame, until the connection ends, a
// gap is detected (below), or ctx is done.
func (r *Relay) stream(ctx context.Context) error {
	r.mu.Lock()
	lastSeq := r.lastSeq
	connGen := r.gen
	r.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastSeq != 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(lastSeq, 10))
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kit/relay: GET %s: status %d", r.url, resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // observer payloads are full FHIR bundles

	var id, data string
	var parsedID uint64
	// connPrev is THIS CONNECTION's last emitted seq (0 = none yet). It is
	// deliberately connection-local, not r.lastSeq: the first frame of a
	// fresh connection legitimately starts wherever Last-Event-ID / hub-ring
	// eviction dictates, so connPrev==0 exempts it from
	// the gap check below.
	var connPrev uint64
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if data == "" {
				continue
			}
			if id != "" {
				// Unparseable ids keep the previous parsedID (our hub's ids
				// are always its seqs; this is a defensive fallback only).
				if v, perr := strconv.ParseUint(id, 10, 64); perr == nil {
					parsedID = v
				}
			}
			if connPrev != 0 && parsedID > connPrev+1 {
				// Gap on this connection: do NOT emit this
				// frame. Drop the connection without advancing r.lastSeq past
				// connPrev, so the reconnect's Last-Event-ID asks the hub's
				// ring to back-fill everything from connPrev+1 forward.
				return fmt.Errorf("kit/relay: gap detected on %s: connection delivered id %d after %d", r.url, parsedID, connPrev)
			}
			r.mu.Lock()
			s := r.stamp
			r.mu.Unlock()
			// Observer carries the raw data payload byte-for-byte — never
			// decode-and-re-marshal (package doc: byte-faithful relay).
			r.bus.Emit(event.Event{
				Type:     event.TypeObserver,
				RunID:    s.RunID,
				Lane:     s.Lane,
				UC:       s.UC,
				Observer: json.RawMessage(data),
			})
			// Set under mu AFTER bus.Emit, exactly as above: this ordering is
			// what makes LastSeq() >= N imply "already on the bus" (Drain's
			// barrier depends on it — keep this comment with any future
			// refactor of this line).
			//
			// Fenced by gen: the cursor
			// belongs to the current epoch; a stale connection may still
			// emit, but must never advance (or regress) the new epoch's
			// accounting. connGen was captured at connection start under
			// this same mu; if ResetCursor has since bumped r.gen, this
			// connection is talking about a dead epoch and the write is
			// skipped — the dead child's socket EOFs on its own, so there's
			// no need to also abort the connection here.
			r.mu.Lock()
			if r.gen == connGen {
				r.lastSeq = parsedID
			}
			r.mu.Unlock()
			connPrev = parsedID
			id, data = "", ""
		}
	}
	return sc.Err()
}
