package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// expiringCtx is a context whose Err turns to DeadlineExceeded when expire is
// called, while its Done channel never closes: Drain can only observe the
// deadline through Err, which is the ordering a poll tick that wins the select
// race produces.
type expiringCtx struct {
	context.Context
	expired atomic.Bool
	done    chan struct{}
	// firstErr, when set, is called once on the next Err call (then cleared).
	firstErr atomic.Pointer[func()]
}

func newExpiringCtx() *expiringCtx {
	return &expiringCtx{Context: context.Background(), done: make(chan struct{})}
}
func (c *expiringCtx) Done() <-chan struct{} { return c.done }
func (c *expiringCtx) Err() error {
	if f := c.firstErr.Swap(nil); f != nil {
		(*f)()
	}
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}

// TestRelay_DrainDeadlineAfterTickReportsCounts: when the deadline is first
// seen after a poll tick (not through ctx.Done), Drain still names both
// counters and wraps context.DeadlineExceeded — whether the stream is behind
// or has already caught up.
func TestRelay_DrainDeadlineAfterTickReportsCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"protocol":1,"incarnation":"a","events":5}`)
	}))
	defer srv.Close()
	for _, row := range []struct {
		name      string
		connected bool
		seq       uint64
		want      []string
	}{
		{"behind", true, 3, []string{"relayed seq 3", "has not reached", "emitted count 5"}},
		{"not yet connected", false, 0, []string{"relayed seq 0", "has not reached", "emitted count 5"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			r := New("", srv.URL+"/health", event.NewBus(fixedClock), t.Logf)
			r.connected, r.incarnation, r.lastSeq = row.connected, "a", row.seq
			ctx := newExpiringCtx()
			ticks := make(chan time.Time)
			r.drainTick = func() (<-chan time.Time, func()) {
				go func() {
					ctx.expired.Store(true) // the deadline passes before the tick is delivered
					ticks <- fixedClock()
				}()
				return ticks, func() {}
			}
			err := r.Drain(ctx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Drain = %v, want it to wrap context.DeadlineExceeded", err)
			}
			for _, w := range row.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("Drain error = %v, want it to mention %q", err, w)
				}
			}
		})
	}
	t.Run("caught up after the deadline", func(t *testing.T) {
		r := New("", srv.URL+"/health", event.NewBus(fixedClock), t.Logf)
		r.connected, r.incarnation, r.lastSeq = true, "a", 3
		ctx := newExpiringCtx()
		ticks := make(chan time.Time)
		r.drainTick = func() (<-chan time.Time, func()) {
			// The first poll check (made under the relay lock) sees seq 3 and
			// no deadline. Only after that check releases the lock does the
			// stream catch up and the deadline pass; then the tick is delivered.
			checked := make(chan struct{})
			signal := func() { close(checked) }
			ctx.firstErr.Store(&signal)
			go func() {
				<-checked
				r.mu.Lock()
				r.lastSeq = 5
				ctx.expired.Store(true)
				r.mu.Unlock()
				ticks <- fixedClock()
			}()
			return ticks, func() {}
		}
		err := r.Drain(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "relayed seq 5 reached the gateway's emitted count 5 after the deadline") {
			t.Fatalf("Drain = %v, want the caught-up deadline error with both counters", err)
		}
		if r.prepared {
			t.Fatal("a drain that ended at the deadline left the relay prepared")
		}
	})
}
