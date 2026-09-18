package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	"github.com/SmartHealthNetwork/shn-kit/event"
)

type blockedCompletionSink struct {
	entered chan Result
	release chan struct{}
}

func (s *blockedCompletionSink) RunCompleted(res Result) {
	s.entered <- res
	<-s.release
}

func completionReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatal("completion operation did not respond within 5s")
		var zero T
		return zero
	}
}

func completionRunner(t *testing.T, sink Sink, fail bool) *Runner {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "eligibility unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"covered":true,"reason":"active coverage"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	driver := scenariodriver.New(scenariodriver.Config{ProviderDataURL: srv.URL})
	return New(Config{Driver: driver, ProviderDataDriver: driver, Bus: event.NewBus(fixedClock), Now: fixedClock, History: sink})
}

func completionResults(t *testing.T, rn *Runner) []Result {
	t.Helper()
	done := make(chan []Result, 1)
	go func() { done <- rn.Results() }()
	return completionReceive(t, done)
}

// Observe only the public result boundary: taking the sequential lock here
// would conceal a result that became visible before admission was released.
func completionWaitResult(t *testing.T, rn *Runner, runID string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		for ctx.Err() == nil {
			for _, res := range rn.Results() {
				if res.RunID == runID {
					done <- res
					return
				}
			}
			runtime.Gosched()
		}
	}()
	return completionReceive(t, done)
}

// Premature publication must fail for successful, failed and recovered rows,
// and for both ways of closing a watch. A terminal event completes the story;
// history capture still owns admission until the finalized result is visible.
func TestCompletion_HistoryBeforePublication(t *testing.T) {
	const panicUC = "uc95"
	ehrRows[panicUC] = func(*Runner, string) (string, error) { panic("completion row exploded") }
	t.Cleanup(func() { delete(ehrRows, panicUC) })
	for _, kind := range []string{"run", "synchronous run", "failed row", "panicking row", "watch stop", "watch cancellation"} {
		t.Run(kind, func(t *testing.T) {
			sink := &blockedCompletionSink{entered: make(chan Result, 8), release: make(chan struct{})}
			rn := completionRunner(t, sink, kind == "failed row")
			var once sync.Once
			release := func() { once.Do(func() { close(sink.release) }) }
			t.Cleanup(release)
			req := Req{Lane: "ehr", UC: "uc01", Branch: "covered"}
			validReq := req
			if kind == "panicking row" {
				req = Req{Lane: "ehr", UC: panicUC}
			}
			type reply struct {
				res Result
				err error
			}
			returned := make(chan reply, 1)
			var runID string
			var err error
			synchronous := kind == "synchronous run" || kind == "watch stop"
			switch kind {
			case "watch stop", "watch cancellation":
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				runID, err = rn.StartWatch(ctx)
				if err == nil {
					if kind == "watch cancellation" {
						cancel()
					} else {
						go func() { res, err := rn.StopWatch(); returned <- reply{res, err} }()
					}
				}
			case "synchronous run":
				go func() { res, err := rn.Run(context.Background(), req); returned <- reply{res, err} }()
			default:
				runID, err = rn.Start(context.Background(), req)
			}
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			captured := completionReceive(t, sink.entered)
			if kind == "synchronous run" {
				runID = captured.RunID
			}
			wantState, wantEvent := StatePassed, event.TypeRunFinished
			if kind == "failed row" || kind == "panicking row" {
				wantState, wantEvent = StateFailed, event.TypeRunFailed
			}
			if captured.RunID != runID || captured.State != wantState {
				t.Fatalf("history result = %+v, want %s/%s", captured, runID, wantState)
			}
			foundOutcome := false
			for _, frame := range rn.cfg.Bus.Since(0) {
				if frame.RunID == runID && frame.Type == wantEvent {
					foundOutcome = true
				}
			}
			if !foundOutcome {
				t.Error("history capture began before the terminal outcome event")
			}
			if results := completionResults(t, rn); len(results) != 0 {
				t.Errorf("Results prematurely published during history capture: %+v", results)
			}
			if !rn.InFlight() {
				t.Error("history capture released in-flight admission early")
			}
			if _, err := rn.Start(context.Background(), validReq); !errors.Is(err, ErrRunInFlight) {
				t.Errorf("Start during history capture = %v, want ErrRunInFlight", err)
			}
			if _, err := rn.StartWatch(context.Background()); !errors.Is(err, ErrRunInFlight) {
				t.Errorf("StartWatch during history capture = %v, want ErrRunInFlight", err)
			}
			if synchronous {
				select {
				case got := <-returned:
					t.Errorf("synchronous completion returned during history capture: %+v", got)
				default:
				}
			}
			release()
			if got := completionWaitResult(t, rn, runID); got != captured {
				t.Fatalf("published result = %+v, want captured result %+v", got, captured)
			}
			if synchronous {
				if got := completionReceive(t, returned); got.err != nil || got.res != captured {
					t.Fatalf("synchronous completion = %+v, want %+v", got, captured)
				}
			}
			if rn.InFlight() {
				t.Fatal("finalized result is visible while admission remains in flight")
			}
			nextID, err := rn.Start(context.Background(), validReq)
			if err != nil {
				t.Fatalf("Start immediately after finalized result: %v", err)
			}
			completionWaitResult(t, rn, nextID)
			watchID, err := rn.StartWatch(context.Background())
			if err != nil {
				t.Fatalf("StartWatch immediately after finalized result: %v", err)
			}
			stopped, err := rn.StopWatch()
			if err != nil || stopped.RunID != watchID {
				t.Fatalf("follow-on StopWatch = %+v, %v", stopped, err)
			}
			results := completionResults(t, rn)
			if len(results) != 3 || results[0].RunID != runID || results[1].RunID != nextID || results[2].RunID != watchID {
				t.Fatalf("finalized results lost sequential order: %+v", results)
			}
		})
	}
}

type readingCompletionSink struct {
	runner *Runner
	seen   chan []Result
}

func (s *readingCompletionSink) RunCompleted(Result) { s.seen <- s.runner.Results() }

// Holding the results mutex across capture would deadlock a history callback
// that inspects prior results, and publishing first would expose its own row.
func TestCompletion_HistoryCanReadPriorResults(t *testing.T) {
	sink := &readingCompletionSink{seen: make(chan []Result, 2)}
	rn := completionRunner(t, sink, false)
	sink.runner = rn
	runID, err := rn.Start(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
	if err != nil {
		t.Fatal(err)
	}
	if got := completionReceive(t, sink.seen); len(got) != 0 {
		t.Errorf("run history saw an unfinalized result: %+v", got)
	}
	first := completionWaitResult(t, rn, runID)
	if _, err := rn.StartWatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := rn.StopWatch(); done <- err }()
	if got := completionReceive(t, sink.seen); len(got) != 1 || got[0] != first {
		t.Errorf("watch history results = %+v, want only %+v", got, first)
	}
	if err := completionReceive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestCompletion_ImmediateSuccessorsStayOrdered(t *testing.T) {
	rn := completionRunner(t, nil, false)
	want := make([]Result, 0, 20)
	for i := 0; i < cap(want); i++ {
		runID, err := rn.Start(context.Background(), Req{Lane: "ehr", UC: "uc01", Branch: "covered"})
		if err != nil {
			t.Fatalf("Start after %d finalized results: %v", i, err)
		}
		res := completionWaitResult(t, rn, runID)
		if res.State != StatePassed {
			t.Fatalf("result = %+v, want passed", res)
		}
		want = append(want, res)
	}
	got := completionResults(t, rn)
	if len(got) != len(want) {
		t.Fatalf("Results count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Results[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
