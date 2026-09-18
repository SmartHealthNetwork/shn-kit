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
	"sync/atomic"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// reconnectDelay is how long Run waits between a dropped/ended connection
// and the next reconnect attempt.
var relayOrder atomic.Uint64

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

	mu          sync.Mutex
	stamp       Stamp
	lastSeq     uint64
	gen         uint64
	order       uint64
	profile     GatewayProfile
	incarnation string
	connected   bool
	prepared    bool
	preparedGen uint64
	windowOpen  bool
	windowErr   error
	uncertain   bool
	coverageErr error

	// drainTick, when set, replaces Drain's poll ticker (tests order the
	// tick against the deadline deterministically). Nil uses drainPoll.
	drainTick func() (<-chan time.Time, func())
}

// New constructs a Relay that streams eventsURL (a gateway's
// {OBSERVER_ADDR}/events endpoint) and re-emits frames onto bus. healthURL identifies
// the observer listener; Drain derives its sibling POST /barrier endpoint.
func New(eventsURL, healthURL string, bus *event.Bus, logf func(string, ...any)) *Relay {
	return &Relay{
		order:     relayOrder.Add(1),
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
	r.resetLocked()
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

	incarnation := resp.Header.Get("X-SHN-Observer-Incarnation")
	r.mu.Lock()
	if connGen == r.gen {
		if r.connected && r.incarnation != incarnation {
			r.resetLocked()
			// Reconnect without the predecessor's resume cursor before accepting bytes.
			r.mu.Unlock()
			return fmt.Errorf("kit/relay: observer incarnation changed")
		}
		r.incarnation = incarnation
		r.connected = true
	}
	r.mu.Unlock()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // observer payloads are full FHIR bundles

	var id, data string
	var parsedID uint64
	// connPrev is THIS CONNECTION's last emitted seq (0 = none yet). It is
	// deliberately connection-local, not r.lastSeq: the first frame of a
	// fresh connection may start after an eviction. Such bytes are retained,
	// but a missing first range prevents a successful completion proof.
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
			parsedID, err = strconv.ParseUint(id, 10, 64)
			validID := err == nil && parsedID != 0
			r.mu.Lock()
			current := r.gen == connGen && r.incarnation == incarnation
			if current && validID && connPrev != 0 && parsedID > connPrev+1 {
				// Reconnect from the last contiguous publication to backfill.
				r.mu.Unlock()
				return fmt.Errorf("kit/relay: gap detected on %s: id %d after %d", r.url, parsedID, connPrev)
			}
			if current && (!validID || (connPrev == 0 && parsedID != lastSeq+1)) {
				r.coverageErr = fmt.Errorf("kit/relay: missing replay coverage or invalid observer id %q after %d", id, lastSeq)
				r.prepared = false
				if r.windowOpen {
					r.windowErr = r.coverageErr
				}
			}
			s := Stamp{}
			if current {
				s = r.stamp
			}
			// Raw bytes are never decoded/re-marshaled by the relay.
			// Publication and cursor advancement share the lifecycle mutex.
			r.bus.Emit(event.Event{Type: event.TypeObserver, RunID: s.RunID, Lane: s.Lane, UC: s.UC, Observer: json.RawMessage(data)})
			if current && validID {
				r.lastSeq = parsedID
			}
			r.mu.Unlock()
			connPrev = parsedID
			id, data = "", ""
		}
	}
	return sc.Err()
}
