package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/relay"
)

type lifecycleProbe struct {
	calls       []string
	stamp       relay.Stamp
	drainPanic  bool
	unsupported bool
}

func (p *lifecycleProbe) SetStamp(s relay.Stamp) { p.calls = append(p.calls, "old stamp"); p.stamp = s }
func (p *lifecycleProbe) ClearStamp() {
	p.calls = append(p.calls, "old clear")
	p.stamp = relay.Stamp{}
}
func (p *lifecycleProbe) Drain(context.Context) error {
	p.calls = append(p.calls, "drain")
	if p.drainPanic && len(p.calls) > 2 {
		panic("diagnostic panic")
	}
	return nil
}
func (p *lifecycleProbe) Begin(s relay.Stamp, f func(error)) {
	p.calls = append(p.calls, "begin")
	p.stamp = s
	f(nil)
}
func (p *lifecycleProbe) End(_ bool, f func(error)) {
	p.calls = append(p.calls, "end")
	p.stamp = relay.Stamp{}
	f(nil)
}

func TestObservationLifecycleEveryExit(t *testing.T) {
	for _, kind := range []string{"pass", "failure", "row panic", "diagnostic panic", "audit pre failure", "watch"} {
		t.Run(kind, func(t *testing.T) {
			p := &lifecycleProbe{drainPanic: kind == "diagnostic panic"}
			rn := completionRunner(t, nil, false)
			rn.cfg.Relay = p
			if kind == "audit pre failure" {
				rn.cfg.AuditURL = ":invalid"
			}
			var res Result
			if kind == "watch" {
				if _, err := rn.StartWatch(context.Background()); err != nil {
					t.Fatal(err)
				}
				res, _ = rn.StopWatch()
			} else {
				rn.mu.Lock()
				rn.inFlight.Store(true)
				res = rn.runLocked(context.Background(), "one", "ehr", "uc01", "", func(*Runner, string) (string, error) {
					if kind == "row panic" {
						panic("row panic")
					}
					if kind == "failure" {
						return "", errors.New("clinical refusal")
					}
					return "success", nil
				})
			}
			want := StatePassed
			if kind == "failure" || kind == "row panic" || kind == "audit pre failure" {
				want = StateFailed
			}
			if res.State != want {
				t.Errorf("state %s want %s: %s", res.State, want, res.Detail)
			}
			if strings.Join(p.calls, ",") != "drain,begin,drain,end" {
				t.Errorf("lifecycle = %v", p.calls)
			}
			es := rn.cfg.Bus.Since(0)
			starts, ends := 0, 0
			for _, e := range es {
				if e.Type == event.TypeRunStarted {
					starts++
				}
				if e.Type == event.TypeRunFinished || e.Type == event.TypeRunFailed {
					ends++
					if e.Seq != es[len(es)-1].Seq {
						t.Error("terminal not last")
					}
				}
			}
			if starts != 1 || ends != 1 {
				t.Fatalf("lifecycle cardinality %d/%d", starts, ends)
			}
			if kind == "diagnostic panic" && !strings.Contains(res.Detail, "diagnostic panic") {
				t.Fatal("missing diagnostic panic")
			}
		})
	}
}

func TestDiagnosticBudgetExcludesClinicalLifetime(t *testing.T) {
	now := time.Now()
	b := diagnosticBudget{remaining: 5 * time.Second, now: func() time.Time { return now }}
	if err := b.wait(context.Background(), func(ctx context.Context) error { now = now.Add(3 * time.Second); return nil }); err != nil {
		t.Fatal(err)
	}
	now = now.Add(40 * time.Second)
	if err := b.wait(context.Background(), func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		left := time.Until(deadline)
		if left > 2*time.Second || left < time.Second {
			t.Errorf("tail got %v", left)
		}
		now = now.Add(3 * time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if b.remaining != 0 {
		t.Fatalf("budget %v", b.remaining)
	}
	if err := b.wait(context.Background(), func(context.Context) error { t.Fatal("spent budget entered wait"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

type oldStamper struct{ stamped bool }

func (p *oldStamper) SetStamp(relay.Stamp)        { p.stamped = true }
func (p *oldStamper) ClearStamp()                 {}
func (p *oldStamper) Drain(context.Context) error { return nil }
func TestOldStamperDegradesWithoutStamping(t *testing.T) {
	p := &oldStamper{}
	rn := completionRunner(t, nil, false)
	rn.cfg.Relay = p
	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil || res.State != StatePassed || p.stamped || !strings.Contains(res.Detail, "unsupported") {
		t.Fatalf("%+v %v stamp %v", res, err, p.stamped)
	}
}

type blockedPreparation struct {
	lifecycleProbe
	entered, release chan struct{}
	once             sync.Once
}

func (p *blockedPreparation) Drain(ctx context.Context) error {
	p.once.Do(func() { close(p.entered); <-p.release })
	return nil
}
func TestObservationPreparationHoldsAdmission(t *testing.T) {
	p := &blockedPreparation{entered: make(chan struct{}), release: make(chan struct{})}
	rn := completionRunner(t, nil, false)
	rn.cfg.Relay = p
	req := Req{Lane: "ehr", UC: "uc01", Branch: "covered"}
	id, err := rn.Start(context.Background(), req)
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	<-p.entered
	if _, err := rn.Start(context.Background(), req); !errors.Is(err, ErrRunInFlight) {
		t.Fatal("Start admitted", err)
	}
	if _, err := rn.Run(context.Background(), req); !errors.Is(err, ErrRunInFlight) {
		t.Fatal("Run admitted", err)
	}
	if _, err := rn.StartWatch(context.Background()); !errors.Is(err, ErrRunInFlight) {
		t.Fatal("Watch admitted", err)
	}
	close(p.release)
	completionWaitResult(t, rn, id)
}

func TestUnresolvedDispatchForcesEmptyBeginDespiteSuccessfulPriorProof(t *testing.T) {
	rn := completionRunner(t, nil, false)
	p := &lifecycleProbe{}
	rn.cfg.Relay = p
	rn.cfg.Dispatch = &DispatchObserver{}
	rn.cfg.Dispatch.taint.Store(true)
	rn.mu.Lock()
	rn.inFlight.Store(true)
	res := rn.runLocked(context.Background(), "new", "ehr", "uc01", "", func(*Runner, string) (string, error) {
		if p.stamp.RunID != "" {
			t.Error("tainted window acquired a stamp")
		}
		return "clinical success", nil
	})
	if res.State != StatePassed || !strings.Contains(res.Detail, "unresolved dispatch") {
		t.Fatalf("%+v", res)
	}
	if p.calls[0] != "begin" {
		t.Fatalf("tainted preparation performed source wait: %v", p.calls)
	}
	if !rn.cfg.Dispatch.Uncertain() {
		t.Fatal("successful diagnostic cleared daemon taint")
	}
}

func TestTimedOutKnownWorkNextWindowUnscopedThenRecovers(t *testing.T) {
	for _, next := range []string{"run", "watch"} {
		t.Run(next, func(t *testing.T) {
			obs := newControllableObs(t)
			defer obs.close()
			bus := event.NewBus(fixedClock)
			life, cancelLife := context.WithCancel(context.Background())
			defer cancelLife()
			rly := newTestRelay(life, obs, bus)
			rn := completionRunner(t, nil, false)
			rn.cfg.Bus = bus
			rn.cfg.Relay = rly
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			rn.mu.Lock()
			rn.inFlight.Store(true)
			first := rn.runLocked(ctx, "first", "ehr", "uc01", "", func(*Runner, string) (string, error) { obs.health.Store(1); return "accepted", nil })
			if first.State != StatePassed || !strings.Contains(first.Detail, "incomplete") {
				t.Fatalf("first %+v", first)
			}
			secondCtx, secondCancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer secondCancel()
			deliver := func() {
				obs.frames <- "id: 1\ndata: {\"seq\":1,\"late\":true}\n\n"
				deadline := time.Now().Add(time.Second)
				for rly.LastSeq() != 1 && time.Now().Before(deadline) {
					runtime.Gosched()
				}
				if rly.LastSeq() != 1 {
					t.Fatal("late bytes not relayed")
				}
			}
			if next == "watch" {
				id, err := rn.StartWatch(secondCtx)
				if err != nil {
					t.Fatal(err)
				}
				deliver()
				completionWaitResult(t, rn, id)
			} else {
				rn.mu.Lock()
				rn.inFlight.Store(true)
				rn.runLocked(secondCtx, "second", "ehr", "uc01", "", func(*Runner, string) (string, error) { deliver(); return "second clinical success", nil })
			}
			for _, e := range bus.Since(0) {
				if e.Type == event.TypeObserver && (e.RunID != "" || string(e.Observer) != `{"seq":1,"late":true}`) {
					t.Fatalf("late bytes attributed or changed: %+v", e)
				}
			}
			rn.mu.Lock()
			rn.inFlight.Store(true)
			third := rn.runLocked(context.Background(), "third", "ehr", "uc01", "", func(*Runner, string) (string, error) { obs.pushFrame(2); return "restored", nil })
			if third.State != StatePassed || strings.Contains(third.Detail, "incomplete") {
				t.Fatalf("recovery %+v", third)
			}
			es := bus.SinceRun(0, "third")
			if len(es) != 4 || es[0].Type != event.TypeRunStarted || es[2].Type != event.TypeObserver || es[3].Type != event.TypeRunFinished {
				t.Fatalf("recovered story %+v", es)
			}
		})
	}
}

func TestDelayedDispatchCannotBeReattributedAfterGatewayReset(t *testing.T) {
	// A child that answers the barrier is treated the same whether or not its
	// build metadata identifies it: identity excuses a missing barrier, it never
	// grants anything to a child that has one. Both non-legacy arms run so that
	// equivalence is stated here rather than assumed.
	barrierProfiles := []relay.GatewayProfile{relay.GatewayBarrier0440, relay.GatewayUnknown}
	for _, legacy := range []bool{false, true} {
		for _, barrierProfile := range barrierProfiles {
			if legacy && barrierProfile != barrierProfiles[0] {
				continue // the legacy arm has no barrier, so it has one shape only
			}
			for _, bff := range []bool{false, true} {
				t.Run(fmt.Sprintf("legacy=%v/profile=%d/bff=%v", legacy, barrierProfile, bff), func(t *testing.T) {
					frames := make(chan string, 1)
					var count atomic.Uint64
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						if req.URL.Path == "/events" {
							if !legacy {
								w.Header().Set("X-SHN-Observer-Incarnation", "a")
							}
							w.(http.Flusher).Flush()
							for {
								select {
								case frame := <-frames:
									fmt.Fprint(w, frame)
									w.(http.Flusher).Flush()
								case <-req.Context().Done():
									return
								}
							}
						}
						if legacy && req.URL.Path == "/barrier" {
							http.NotFound(w, req)
							return
						}
						if legacy {
							fmt.Fprintf(w, `{"events":%d}`, count.Load())
						} else {
							fmt.Fprintf(w, `{"protocol":1,"incarnation":"a","events":%d}`, count.Load())
						}
					}))
					defer srv.Close()
					bus := event.NewBus(fixedClock)
					rly := relay.New(srv.URL+"/events", srv.URL+"/health", bus, t.Logf)
					// The non-legacy arms serve the barrier above: the release this
					// Kit packages, and a child whose build metadata says nothing.
					profile := barrierProfile
					if legacy {
						profile = relay.GatewayLegacySync0431
					}
					rly.SetGatewayProfile(profile)
					life, stop := context.WithCancel(context.Background())
					defer stop()
					go rly.Run(life)
					rn := completionRunner(t, nil, false)
					rn.cfg.Bus = bus
					rn.cfg.Relay = rly
					d := &DispatchObserver{}
					rn.cfg.Dispatch = d
					base := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
						if bff {
							return &http.Response{StatusCode: 504, Body: io.NopCloser(strings.NewReader("upstream timeout")), Header: make(http.Header)}, nil
						}
						return nil, context.Canceled
					})}
					bffURL := ""
					if bff {
						bffURL = "http://delayed"
					}
					client := d.Client(base, bffURL)
					rn.mu.Lock()
					rn.inFlight.Store(true)
					rn.runLocked(context.Background(), "old", "ehr", "uc01", "", func(*Runner, string) (string, error) {
						resp, err := client.Post("http://delayed/clinical", "application/json", strings.NewReader("{}"))
						if err == nil {
							io.ReadAll(resp.Body)
							resp.Body.Close()
						}
						return "", errors.New("dispatch unresolved")
					})
					rly.ResetSource(profile)
					// No source work has entered yet: an empty successful source barrier could
					// not establish causal completion. The retained daemon taint must win.
					id, err := rn.StartWatch(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					count.Store(1)
					frames <- "id: 1\ndata: {\"late\":true}\n\n"
					deadline := time.Now().Add(time.Second)
					for len(bus.Since(0)) < 6 && time.Now().Before(deadline) {
						runtime.Gosched()
					}
					res, err := rn.StopWatch()
					if err != nil || res.RunID != id || !strings.Contains(res.Detail, "unresolved dispatch") {
						t.Fatalf("watch %+v %v", res, err)
					}
					seen := 0
					for _, e := range bus.Since(0) {
						if e.Type == event.TypeObserver {
							seen++
							if e.RunID != "" || string(e.Observer) != `{"late":true}` {
								t.Fatalf("delayed old bytes misattributed: %+v", e)
							}
						}
					}
					if seen != 1 {
						t.Fatalf("late frame count %d", seen)
					}
					if !d.Uncertain() {
						t.Fatal("gateway reset cleared daemon taint")
					}
					if (&DispatchObserver{}).Uncertain() {
						t.Fatal("new daemon begins tainted")
					}
				})
			}
		}
	}
}

func TestDegradedPreparationStillPublishesOwnedLifecycle(t *testing.T) {
	rn := completionRunner(t, nil, false)
	rn.cfg.Relay = &lifecycleProbe{}
	rn.cfg.Dispatch = &DispatchObserver{}
	rn.cfg.Dispatch.taint.Store(true)
	res, err := rn.Run(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatal(err)
	}
	es := rn.cfg.Bus.SinceRun(0, res.RunID)
	if len(es) != 3 || es[0].Type != event.TypeRunStarted || es[2].Type != event.TypeRunFinished {
		t.Fatalf("degraded lifecycle lost identity: %+v", rn.cfg.Bus.Since(0))
	}
}

func TestCompletedDirectRefusalKeepsNextTimeline(t *testing.T) {
	obs := newControllableObs(t)
	defer obs.close()
	bus := event.NewBus(fixedClock)
	life, stop := context.WithCancel(context.Background())
	defer stop()
	rly := newTestRelay(life, obs, bus)
	rn := completionRunner(t, nil, false)
	rn.cfg.Bus = bus
	rn.cfg.Relay = rly
	d := &DispatchObserver{}
	rn.cfg.Dispatch = d
	client := d.Client(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("refused")), Header: make(http.Header)}, nil
	})}, "")
	rn.mu.Lock()
	rn.inFlight.Store(true)
	first := rn.runLocked(context.Background(), "refusal", "ehr", "uc01", "", func(*Runner, string) (string, error) {
		resp, err := client.Post("http://direct/clinical", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		return "", errors.New("clinical refusal")
	})
	if first.State != StateFailed || d.Uncertain() {
		t.Fatalf("completed refusal poisoned attribution: %+v", first)
	}
	rn.mu.Lock()
	rn.inFlight.Store(true)
	second := rn.runLocked(context.Background(), "next", "ehr", "uc01", "", func(*Runner, string) (string, error) { obs.pushFrame(1); return "success", nil })
	es := bus.SinceRun(0, "next")
	if second.State != StatePassed || len(es) != 4 || es[2].Type != event.TypeObserver || es[3].Type != event.TypeRunFinished {
		t.Fatalf("next story %+v/%+v", second, es)
	}
}
