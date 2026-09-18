package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/relay"
)

// diagnosticBudget charges wait time only, excluding clinical and Watch lifetime.
type diagnosticBudget struct {
	remaining time.Duration
	now       func() time.Time
}

func (b *diagnosticBudget) wait(ctx context.Context, drain func(context.Context) error) (err error) {
	start := b.now()
	dctx, cancel := context.WithTimeout(ctx, b.remaining)
	defer func() {
		cancel()
		elapsed := b.now().Sub(start)
		if elapsed > 0 {
			b.remaining -= elapsed
		}
		if b.remaining < 0 {
			b.remaining = 0
		}
		if p := recover(); p != nil {
			err = fmt.Errorf("observer diagnostic panic: %v", p)
		}
	}()
	if err = dctx.Err(); err != nil {
		return err
	}
	return drain(dctx)
}

type observationWindow struct {
	runner *Runner
	relay  relay.WindowStamper
	budget diagnosticBudget
	notes  []string
	ended  bool
	stamp  relay.Stamp
	branch string
}

func (w *observationWindow) note(err error) {
	if err != nil {
		w.notes = append(w.notes, "observer drain incomplete: "+err.Error())
	}
}
func (r *Runner) beginObservation(ctx context.Context, s relay.Stamp, branch string) *observationWindow {
	w := &observationWindow{runner: r, budget: diagnosticBudget{remaining: drainTimeout, now: time.Now}, stamp: s, branch: branch}
	publish := func(err error) {
		w.note(err)
		r.cfg.Bus.Emit(event.Event{Type: event.TypeRunStarted, RunID: w.stamp.RunID, Lane: w.stamp.Lane, UC: w.stamp.UC, Branch: branch})
	}
	if r.cfg.Relay == nil {
		publish(nil)
		return w
	}
	var ok bool
	w.relay, ok = r.cfg.Relay.(relay.WindowStamper)
	if !ok {
		publish(errors.New("unsupported atomic observation; window unscoped"))
		return w
	}
	var err error
	if r.cfg.Dispatch != nil && r.cfg.Dispatch.Uncertain() {
		err = errors.New("unresolved dispatch; window unscoped until whole-chain shutdown and new daemon")
	} else {
		err = w.budget.wait(ctx, w.relay.Drain)
	}
	if err != nil {
		w.note(err)
		s = relay.Stamp{}
	}
	w.relay.Begin(s, publish)
	return w
}
func (w *observationWindow) drain(ctx context.Context) {
	if w.relay != nil {
		w.note(w.budget.wait(ctx, w.relay.Drain))
	}
}
func (w *observationWindow) end(detail string, clinicalErr error, uncertain bool) (res Result) {
	if w.ended {
		panic("observation window ended twice")
	}
	w.ended = true
	if w.runner.cfg.Dispatch != nil {
		if uncertain || w.runner.cfg.Dispatch.Uncertain() {
			w.runner.cfg.Dispatch.taint.Store(true)
		}
		uncertain = w.runner.cfg.Dispatch.Uncertain()
	}
	publish := func(err error) {
		w.note(err)
		state, typ := StatePassed, event.TypeRunFinished
		if clinicalErr != nil {
			state, typ = StateFailed, event.TypeRunFailed
			detail = clinicalErr.Error()
		}
		if len(w.notes) > 0 {
			detail = strings.TrimSpace(detail + " (" + strings.Join(w.notes, "; ") + ")")
		}
		res = Result{RunID: w.stamp.RunID, Lane: w.stamp.Lane, UC: w.stamp.UC, Branch: w.branch, State: state, Detail: detail}
		w.runner.cfg.Bus.Emit(event.Event{Type: typ, RunID: res.RunID, Lane: res.Lane, UC: res.UC, Detail: detail})
	}
	if w.relay == nil {
		publish(nil)
	} else {
		w.relay.End(uncertain, publish)
	}
	return res
}
