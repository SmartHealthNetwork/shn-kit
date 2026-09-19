package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"
)

// GatewayProfile is automatic executable provenance, never an operator claim.
//
// A profile exists to excuse what a release cannot do, never to assume what it
// can: a gateway that serves the completion barrier proves that capability on
// the wire, in its own answer, so no identity is needed to believe it. Only the
// release that serves no barrier needs to be named, so its absence is read as
// the known absence it is rather than as a fault. Every profile degrades the
// same way — unknown provenance changes diagnostics only and never gates
// clinical work.
type GatewayProfile uint8

const (
	// GatewayUnknown is any executable whose build metadata names no published
	// release: a development build, a replaced module, a rebuilt copy, or a
	// release this Kit does not know. It gets no concession — a missing
	// completion barrier is reported as the failure it is.
	GatewayUnknown GatewayProfile = iota
	// GatewayLegacySync0431 is the published gateway v0.43.1, whose observer
	// listener serves no completion barrier. This is the one identity allowed to
	// fall back to that release's synchronous health counter, and the one that
	// carries the extra Watch attribution limits the counter cannot cover.
	GatewayLegacySync0431
	// GatewayBarrier0440 is the published gateway v0.44.0 and every later
	// published release whose opt-in loopback observer listener serves the same
	// completion barrier (v0.46.0, the release this Kit packages, serves it
	// unchanged). It takes no legacy concession: an absent barrier here is a
	// fault, not an expected absence, and entered-operation completion covers a
	// Watch without the counter's limits. Naming it positively is what lets
	// packaging tell the release this Kit ships from any other executable handed
	// to it.
	GatewayBarrier0440
)

func (r *Relay) SetGatewayProfile(p GatewayProfile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.profile != p {
		r.resetLocked()
	}
	r.profile = p
	r.prepared = false
}

// ResetSource follows confirmed owned-child exit. Dispatch uncertainty survives:
// an upstream BFF may still send old work to the replacement child.
func (r *Relay) ResetSource(p GatewayProfile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profile = p
	r.resetLocked()
}
func (r *Relay) resetLocked() {
	r.gen++
	r.lastSeq = 0
	r.incarnation = ""
	r.connected = false
	r.prepared = false
	r.coverageErr = nil
	r.stamp = Stamp{}
	if r.windowOpen {
		r.windowErr = errors.New("observer source changed during window")
	}
}

type barrierReply struct {
	Protocol    *uint64 `json:"protocol"`
	Incarnation string  `json:"incarnation"`
	Events      *uint64 `json:"events"`
}

func (r *Relay) fetchProof(ctx context.Context, method, address string) (barrierReply, int, error) {
	var h barrierReply
	req, err := http.NewRequestWithContext(ctx, method, address, nil)
	if err != nil {
		return h, 0, err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return h, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, resp.StatusCode, fmt.Errorf("observer %s: status %d", address, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return h, resp.StatusCode, err
	}
	if len(b) > 4096 {
		return h, resp.StatusCode, errors.New("oversized observer proof")
	}
	if err = json.Unmarshal(b, &h); err != nil {
		return h, resp.StatusCode, fmt.Errorf("decode %s: %w", address, err)
	}
	if h.Events == nil {
		return h, resp.StatusCode, errors.New("observer proof missing non-null events")
	}
	return h, resp.StatusCode, nil
}

// Drain proves source completion and byte publication in one epoch. It is
// diagnostic-only; a failure invalidates preparation, never clinical execution.
func (r *Relay) Drain(ctx context.Context) (err error) {
	r.mu.Lock()
	gen, profile := r.gen, r.profile
	r.prepared = false
	tainted := r.uncertain
	r.mu.Unlock()
	defer func() {
		if err != nil {
			r.mu.Lock()
			r.prepared = false
			r.mu.Unlock()
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	if tainted {
		return errors.New("observer attribution unscoped: unresolved dispatch until whole-chain shutdown and new daemon")
	}
	u, e := url.Parse(r.healthURL)
	if e != nil {
		return e
	}
	u.Path = path.Join(path.Dir(u.Path), "barrier")
	h, status, err := r.fetchProof(ctx, http.MethodPost, u.String())
	legacy := false
	if status == http.StatusNotFound && profile == GatewayLegacySync0431 {
		h, _, err = r.fetchProof(ctx, http.MethodGet, r.healthURL)
		if err == nil && (h.Protocol != nil || h.Incarnation != "") {
			err = errors.New("advertised observer capability has no barrier")
		}
		legacy = err == nil
	}
	if err != nil {
		return fmt.Errorf("kit/relay: drain %s: %w", r.healthURL, err)
	}
	if !legacy && (h.Protocol == nil || *h.Protocol != 1 || h.Incarnation == "") {
		return errors.New("unsupported or missing observer completion protocol/incarnation")
	}
	tick, stop := r.drainTicker()
	defer stop()
	// deadline reports a context exit with both counters, whether the poll
	// check or the wait observes it first.
	deadline := func(seq uint64, cause error) error {
		if seq >= *h.Events {
			return fmt.Errorf("kit/relay: drain: relayed seq %d reached the gateway's emitted count %d after the deadline: %w", seq, *h.Events, cause)
		}
		return fmt.Errorf("kit/relay: drain: relayed seq %d has not reached the gateway's emitted count %d: %w", seq, *h.Events, cause)
	}
	for {
		r.mu.Lock()
		if cause := ctx.Err(); cause != nil {
			seq := r.lastSeq
			r.mu.Unlock()
			return deadline(seq, cause)
		}
		switch {
		case r.gen != gen:
			err = errors.New("observer source changed during drain")
		case r.uncertain:
			err = errors.New("observer unresolved dispatch")
		case r.coverageErr != nil:
			err = r.coverageErr
		case r.connected && !legacy && r.incarnation != h.Incarnation:
			err = errors.New("observer barrier/stream incarnation mismatch")
		case r.connected && r.lastSeq >= *h.Events:
			// The deadline was checked above in this same critical section.
			r.prepared = true
			r.preparedGen = gen
			r.mu.Unlock()
			return nil
		}
		seq := r.lastSeq
		r.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return deadline(seq, ctx.Err())
		case <-tick:
		}
	}
}

// drainTicker returns Drain's poll channel and its stop function.
func (r *Relay) drainTicker() (<-chan time.Time, func()) {
	if r.drainTick != nil {
		return r.drainTick()
	}
	t := time.NewTicker(drainPoll)
	return t.C, t.Stop
}

func (r *Relay) beginLocked(s Stamp) error {
	var err error
	if s.RunID == "" {
		err = errors.New("observer attribution unscoped: preparation unavailable")
	} else if !r.prepared || r.preparedGen != r.gen || r.uncertain {
		err = errors.New("observer attribution unscoped: no current completion proof")
	}
	r.prepared = false
	r.windowOpen = true
	r.windowErr = nil
	r.stamp = Stamp{}
	if err == nil {
		r.stamp = s
	}
	return err
}
func (r *Relay) endLocked(uncertain bool) error {
	if uncertain || (r.profile == GatewayLegacySync0431 && (r.stamp.UC == "external" || r.stamp.RunID == "" || !r.prepared)) {
		r.uncertain = true
	}
	err := r.windowErr
	if r.uncertain {
		err = errors.Join(err, errors.New("observer attribution unscoped: unresolved source work until whole-chain shutdown and new daemon"))
	}
	r.stamp = Stamp{}
	r.prepared = false
	r.windowOpen = false
	r.windowErr = nil
	return err
}

// Begin consumes preparation and publishes start under the frame mutex.
// The callback must only publish; it must not call relay methods or perform I/O.
func (r *Relay) Begin(s Stamp, publish func(error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	publish(r.beginLocked(s))
}

// End clears attribution before publishing terminal under the same mutex.
func (r *Relay) End(uncertain bool, publish func(error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	publish(r.endLocked(uncertain))
}
