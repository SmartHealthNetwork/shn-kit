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

type windowAPI interface {
	Begin(Stamp, func(error))
	End(bool, func(error))
}

func TestBarrierStrictRejections(t *testing.T) {
	const valid = `{"protocol":1,"incarnation":"a","events":0,"extra":""}`
	mutate := func(from, to string) string { return strings.Replace(valid, from, to, 1) }
	for _, row := range []struct {
		name, body string
		status     int
		diagnostic string
	}{
		{"unknown absent", valid, 404, "status 404"},
		{"server", valid, 500, "status 500"},
		{"missing protocol", mutate(`"protocol":1,`, ""), 200, "unsupported or missing observer completion protocol/incarnation"},
		{"null protocol", mutate(`"protocol":1`, `"protocol":null`), 200, "unsupported or missing observer completion protocol/incarnation"},
		{"unsupported", mutate(`"protocol":1`, `"protocol":2`), 200, "unsupported or missing observer completion protocol/incarnation"},
		{"missing count", mutate(`"events":0,`, ""), 200, "missing non-null events"},
		{"null", mutate(`"events":0`, `"events":null`), 200, "missing non-null events"},
		{"negative", mutate(`"events":0`, `"events":-1`), 200, "cannot unmarshal number -1"},
		{"fraction", mutate(`"events":0`, `"events":0.5`), 200, "cannot unmarshal number 0.5"},
		{"overflow", mutate(`"events":0`, `"events":18446744073709551616`), 200, "cannot unmarshal number 18446744073709551616"},
		{"missing epoch", mutate(`"incarnation":"a",`, ""), 200, "unsupported or missing observer completion protocol/incarnation"},
		{"empty epoch", mutate(`"incarnation":"a"`, `"incarnation":""`), 200, "unsupported or missing observer completion protocol/incarnation"},
		{"mismatch", mutate(`"incarnation":"a"`, `"incarnation":"b"`), 200, "barrier/stream incarnation mismatch"},
		{"malformed", strings.TrimSuffix(valid, "}"), 200, "unexpected end of JSON input"},
		{"trailing", valid + " {}", 200, "after top-level value"},
		{"oversized", mutate(`"extra":""`, `"extra":"`+strings.Repeat("x", 8192)+`"`), 200, "oversized observer proof"},
	} {
		t.Run(row.name, func(t *testing.T) {
			type reply struct {
				status int
				body   string
			}
			var response atomic.Pointer[reply]
			response.Store(&reply{http.StatusOK, valid})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodPost || req.URL.Path != "/barrier" {
					t.Errorf("unexpected proof request: %s %s", req.Method, req.URL.Path)
				}
				current := response.Load()
				w.WriteHeader(current.status)
				fmt.Fprint(w, current.body)
			}))
			defer srv.Close()
			r := New(srv.URL+"/events", srv.URL+"/health", event.NewBus(fixedClock), t.Logf)
			// Seed the same valid, connected SSE epoch for every row. Stream wiring
			// has separate tests; no missing connection may mask a parsing guard here.
			r.connected = true
			r.incarnation = "a"
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := r.Drain(ctx); err != nil {
				t.Fatalf("valid connected baseline rejected: %v", err)
			}
			response.Store(&reply{row.status, row.body})
			err := r.Drain(ctx)
			if err == nil {
				t.Fatal("single-mutant response proved completion")
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				t.Fatalf("unrelated wait/cancellation masked protocol guard: %v", err)
			}
			if !strings.Contains(err.Error(), row.diagnostic) {
				t.Fatalf("rejection = %v; want diagnostic %q", err, row.diagnostic)
			}
			if r.prepared {
				t.Fatal("rejected response retained baseline preparation proof")
			}
		})
	}
}

func TestWindowPublicationBoundary(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprintf("multi=%v", multi), func(t *testing.T) {
			// Blocking the bus clock stops Emit while publication owns the relay lock.
			entered, release := make(chan struct{}), make(chan struct{})
			var block atomic.Bool
			bus := event.NewBus(func() time.Time {
				if block.CompareAndSwap(true, false) {
					close(entered)
					<-release
				}
				return fixedClock()
			})
			frames := make(chan frame, 3)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/events" {
					fmt.Fprint(w, `{"protocol":1,"incarnation":"a","events":0}`)
					return
				}
				w.Header().Set("X-SHN-Observer-Incarnation", "a")
				w.(http.Flusher).Flush()
				for {
					select {
					case f := <-frames:
						writeFrame(w, w.(http.Flusher), f)
					case <-req.Context().Done():
						return
					}
				}
			}))
			defer srv.Close()
			r := New(srv.URL+"/events", srv.URL+"/health", bus, t.Logf)
			win, ok := any(r).(windowAPI)
			if !ok {
				t.Fatal("relay lacks atomic observation lifecycle")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			go r.Run(ctx)
			if err := r.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			if multi {
				second := New("", "", bus, t.Logf)
				second.prepared = true
				win = NewMulti(r, second, r)
			}
			win.Begin(Stamp{RunID: "one"}, func(err error) {
				if err != nil {
					t.Error(err)
				}
				bus.Emit(event.Event{Type: event.TypeRunStarted, RunID: "one"})
			})
			block.Store(true)
			frames <- frame{"1", `{"raw":"\\u0061","n":1}`}
			<-entered
			ended := make(chan struct{})
			go func() {
				win.End(false, func(error) { bus.Emit(event.Event{Type: event.TypeRunFinished, RunID: "one"}) })
				close(ended)
			}()
			select {
			case <-ended:
				t.Fatal("End passed an in-flight publication")
			default:
			}
			close(release)
			<-ended
			frames <- frame{"2", `{"raw":"late","n":2}`}
			waitForLastSeq(t, r, 2)
			ev := bus.Since(0)
			if len(ev) != 4 || ev[0].Type != event.TypeRunStarted || ev[1].RunID != "one" || ev[2].Type != event.TypeRunFinished || ev[3].RunID != "" {
				t.Fatalf("boundary: %+v", ev)
			}
			if string(ev[1].Observer) != `{"raw":"\\u0061","n":1}` || string(ev[3].Observer) != `{"raw":"late","n":2}` {
				t.Fatalf("bytes changed: %+v", ev)
			}
		})
	}
}

func TestProofInvalidationAndMulti(t *testing.T) {
	newReady := func() *Relay {
		r := New("", "", event.NewBus(fixedClock), t.Logf)
		r.connected = true
		r.incarnation = "a"
		r.prepared = true
		r.preparedGen = r.gen
		return r
	}
	for _, kind := range []string{"reset before begin", "reset during window", "force unscoped", "multi one failure", "dispatch reset", "legacy watch", "modern watch"} {
		t.Run(kind, func(t *testing.T) {
			a, b := newReady(), newReady()
			m := NewMulti(b, a, a)
			if len(m.members) != 2 {
				t.Fatal("duplicate relay")
			}
			var beginErr, endErr error
			s := Stamp{RunID: "one"}
			switch kind {
			case "reset before begin":
				a.ResetCursor()
			case "force unscoped":
				s = Stamp{}
			case "multi one failure":
				b.prepared = false
			case "legacy watch":
				a.profile = GatewayLegacySync0431
				s.UC = "external"
			case "modern watch":
				s.UC = "external"
			}
			m.Begin(s, func(err error) { beginErr = err })
			if kind == "reset before begin" || kind == "force unscoped" || kind == "multi one failure" {
				if beginErr == nil || stampOf(a).RunID != "" || stampOf(b).RunID != "" {
					t.Fatal("partial/stale attribution")
				}
			}
			if kind == "reset during window" {
				a.ResetCursor()
			}
			m.End(kind == "dispatch reset", func(err error) { endErr = err })
			if kind == "reset during window" && endErr == nil {
				t.Fatal("lost open-window epoch change")
			}
			if kind == "dispatch reset" {
				a.ResetSource(GatewayLegacySync0431)
				b.ResetCursor()
				if !a.uncertain || !b.uncertain {
					t.Fatal("gateway reset cleared dispatch uncertainty")
				}
			}
			if kind == "legacy watch" && !a.uncertain {
				t.Fatal("legacy watch not tainted")
			}
			if kind == "modern watch" && a.uncertain {
				t.Fatal("modern watch tainted without dispatch uncertainty")
			}
			if stampOf(a).RunID != "" || stampOf(b).RunID != "" {
				t.Fatal("End left attribution")
			}
		})
	}
}

func TestDrainEpochChangeDuringHTTP(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(w, `{"protocol":1,"incarnation":"a","events":0}`)
	}))
	defer srv.Close()
	r := New("", srv.URL+"/health", event.NewBus(fixedClock), t.Logf)
	r.connected = true
	r.incarnation = "a"
	done := make(chan error, 1)
	go func() { done <- r.Drain(context.Background()) }()
	<-entered
	r.ResetCursor()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("cross-epoch proof accepted")
	}
}

func TestLegacyFallbackRequiresExactProfileAndStrictHealth(t *testing.T) {
	for _, row := range []struct {
		name, body string
		status     int
		profile    GatewayProfile
		ok         bool
	}{
		{"trusted", `{"events":0}`, 200, GatewayLegacySync0431, true}, {"unknown", `{"events":0}`, 200, GatewayUnknown, false},
		// The packaged release serves the barrier, so a 404 from it is a fault.
		// Reading its counter instead would certify completion it never proved.
		{"packaged release", `{"events":0}`, 200, GatewayBarrier0440, false},
		{"advertised", `{"protocol":1,"incarnation":"a","events":0}`, 200, GatewayLegacySync0431, false},
		{"missing", `{}`, 200, GatewayLegacySync0431, false}, {"null", `{"events":null}`, 200, GatewayLegacySync0431, false},
		{"bad status", `{"events":0}`, 500, GatewayLegacySync0431, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/barrier" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(row.status)
				fmt.Fprint(w, row.body)
			}))
			defer srv.Close()
			r := New("", srv.URL+"/health", event.NewBus(fixedClock), t.Logf)
			r.SetGatewayProfile(row.profile)
			r.connected = true
			err := r.Drain(context.Background())
			if (err == nil) != row.ok {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestCountZeroNeedsMatchingStreamAndCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"protocol":1,"incarnation":"a","events":0}`)
	}))
	defer srv.Close()
	r := New("", srv.URL+"/health", event.NewBus(fixedClock), t.Logf)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.Drain(ctx); err == nil {
		t.Fatal("zero count without stream epoch accepted")
	}
	r.connected = true
	r.incarnation = "a"
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if err := r.Drain(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled zero-count drain = %v, want context.Canceled", err)
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.ResetSource(GatewayUnknown)
	r.Begin(Stamp{RunID: "after reset"}, func(err error) {
		if err == nil {
			t.Fatal("reset proof consumed")
		}
	})
}

func TestMissingReplayCannotProveCoverage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			fmt.Fprint(w, `{"protocol":1,"incarnation":"a","events":8}`)
			return
		}
		w.Header().Set("X-SHN-Observer-Incarnation", "a")
		writeFrame(w, w.(http.Flusher), frame{"8", `{"seq":8,"raw":"retained"}`})
		<-r.Context().Done()
	}))
	defer srv.Close()
	bus := event.NewBus(fixedClock)
	r := New(srv.URL+"/events", srv.URL+"/health", bus, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	waitForLastSeq(t, r, 8)
	if err := r.Drain(context.Background()); err == nil {
		t.Fatal("high cursor hid missing replay")
	}
	if ev := bus.Since(0); len(ev) != 1 || string(ev[0].Observer) != `{"seq":8,"raw":"retained"}` {
		t.Fatalf("raw gap bytes lost: %+v", ev)
	}
}

func TestMultiBeginAndCallbackPanicReleaseAllLocks(t *testing.T) {
	a := New("", "", event.NewBus(fixedClock), t.Logf)
	b := New("", "", a.bus, t.Logf)
	m := NewMulti(b, a, a)
	a.prepared = true
	b.prepared = true
	func() {
		defer func() {
			if recover() == nil {
				t.Error("callback did not panic")
			}
		}()
		m.Begin(Stamp{RunID: "one"}, func(error) { panic("publication") })
	}()
	done := make(chan struct{})
	go func() { NewMulti(a, b).End(false, func(error) {}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback panic stranded member locks")
	}
}

func TestProfileChangeInvalidatesInFlightPreparation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/barrier" {
			http.NotFound(w, r)
			return
		}
		close(entered)
		<-release
		fmt.Fprint(w, `{"events":0}`)
	}))
	defer srv.Close()
	r := New("", srv.URL+"/health", event.NewBus(fixedClock), t.Logf)
	r.SetGatewayProfile(GatewayLegacySync0431)
	r.connected = true
	done := make(chan error, 1)
	go func() { done <- r.Drain(context.Background()) }()
	<-entered
	r.SetGatewayProfile(GatewayUnknown)
	close(release)
	if err := <-done; err == nil {
		t.Fatal("in-flight old identity revalidated preparation")
	}
}

func TestEndReportsDispatchAndLegacyWatchUncertainty(t *testing.T) {
	for _, watch := range []bool{false, true} {
		r := New("", "", event.NewBus(fixedClock), t.Logf)
		r.prepared = true
		s := Stamp{RunID: "one"}
		if watch {
			r.profile = GatewayLegacySync0431
			s.UC = "external"
		}
		r.Begin(s, func(err error) {
			if err != nil {
				t.Fatal(err)
			}
		})
		r.prepared = true
		r.End(!watch, func(err error) {
			if err == nil || !strings.Contains(err.Error(), "unresolved") {
				t.Fatalf("uncertain close detail %v", err)
			}
		})
	}
}

// Closing a Watch with no dispatch question is sticky only for the earlier
// release's counter, which cannot count external operations still in flight.
// The packaged release's barrier covers the operations a Watch already entered,
// so closing one keeps its attribution — and an unidentified executable is not
// assumed to have the counter's limitation either, since its own barrier answer
// is what preparation believed.
func TestWatchCloseIsStickyOnlyForTheEarlierCounter(t *testing.T) {
	for _, row := range []struct {
		name     string
		profile  GatewayProfile
		unscoped bool
	}{
		{"earlier counter", GatewayLegacySync0431, true},
		{"packaged release", GatewayBarrier0440, false},
		{"unidentified", GatewayUnknown, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			r := New("", "", event.NewBus(fixedClock), t.Logf)
			r.profile = row.profile
			r.prepared = true
			r.Begin(Stamp{RunID: "one", UC: "external"}, func(err error) {
				if err != nil {
					t.Fatal(err)
				}
			})
			r.prepared = true
			r.End(false, func(err error) {
				if (err != nil) != row.unscoped {
					t.Fatalf("watch close reported %v", err)
				}
			})
			if r.uncertain != row.unscoped {
				t.Fatalf("source left unresolved = %v, want %v", r.uncertain, row.unscoped)
			}
		})
	}
}

func TestBeginOwnsAllPublicationLocksAndPublishesOnce(t *testing.T) {
	a := New("", "", event.NewBus(fixedClock), t.Logf)
	b := New("", "", a.bus, t.Logf)
	a.prepared = true
	b.prepared = true
	starts := 0
	NewMulti(b, a, a).Begin(Stamp{RunID: "one"}, func(err error) {
		if err != nil {
			t.Fatal(err)
		}
		starts++
		for _, r := range []*Relay{a, b} {
			if r.mu.TryLock() {
				r.mu.Unlock()
				t.Error("start published outside member lock")
			}
		}
		a.bus.Emit(event.Event{Type: event.TypeRunStarted, RunID: "one"})
	})
	if starts != 1 || stampOf(a).RunID != "one" || stampOf(b).RunID != "one" {
		t.Fatal("start not atomic/all-member")
	}
}
