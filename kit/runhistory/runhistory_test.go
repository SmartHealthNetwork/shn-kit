// runhistory_test.go — hermetic tests for the Kit's run-history capture
// spine: file-per-run Store (Save/List/Get) and the
// Recorder that captures each run synchronously at its terminal via the
// runner.Sink hook.
package runhistory

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/runner"
)

func fixedClock() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }

// ---- Row 4: Save/Get round-trip --------------------------------------------

func TestStore_SaveGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 10)

	recTime := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	rec := Record{
		Summary: Summary{
			RunID:      "run-x",
			Lane:       "ehr",
			UC:         "uc01",
			Branch:     "covered",
			State:      "passed",
			Detail:     "active coverage",
			Time:       recTime,
			EventCount: 3,
		},
		Events: []event.Event{
			{Seq: 1, Type: event.TypeRunStarted, RunID: "run-x"},
			{Seq: 2, Type: event.TypeObserver, RunID: "run-x"},
			{Seq: 3, Type: event.TypeRunFinished, RunID: "run-x"},
		},
	}

	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	wantName := fmt.Sprintf("%020d-run-x.json", recTime.UnixNano())
	path := filepath.Join(dir, wantName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v (want the exact filename <unixnano>-<runID>.json)", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("file mode = %v, want 0600", perm)
	}

	got, err := store.Get("run-x")
	if err != nil {
		t.Fatalf("Get(run-x): %v", err)
	}
	if got == nil {
		t.Fatal("Get(run-x) = nil, want the saved record")
	}
	if !reflect.DeepEqual(*got, rec) {
		t.Fatalf("Get(run-x) = %+v, want deep-equal %+v", *got, rec)
	}

	none, err := store.Get("nope")
	if err != nil {
		t.Fatalf("Get(nope): unexpected error %v", err)
	}
	if none != nil {
		t.Fatalf("Get(nope) = %+v, want nil", none)
	}
}

// ---- Row 4b: restart persistence -------------------------------------------

func TestStore_RestartPersistence(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 10)

	rec := Record{
		Summary: Summary{
			RunID:      "run-a",
			Lane:       "ehr",
			UC:         "uc01",
			State:      "passed",
			Time:       time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC),
			EventCount: 1,
		},
		Events: []event.Event{{Seq: 1, Type: event.TypeRunFinished, RunID: "run-a"}},
	}
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A FRESH Store instance over the same dir (simulating a daemon restart)
	// must see identical results — the on-disk bytes, not instance state, are
	// the store.
	fresh := NewStore(dir, 10)

	list, err := fresh.List()
	if err != nil {
		t.Fatalf("List (fresh store): %v", err)
	}
	if len(list) != 1 || list[0].RunID != "run-a" {
		t.Fatalf("List (fresh store) = %+v, want [run-a]", list)
	}

	got, err := fresh.Get("run-a")
	if err != nil {
		t.Fatalf("Get (fresh store): %v", err)
	}
	if got == nil {
		t.Fatal("Get (fresh store) = nil, want the saved record")
	}
	if !reflect.DeepEqual(*got, rec) {
		t.Fatalf("Get (fresh store) = %+v, want deep-equal %+v", *got, rec)
	}
}

// ---- Row 5: List newest-first + summary-only decode ------------------------

func TestStore_ListNewestFirstSummaryOnly(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 10)

	times := []time.Time{
		time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 3, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
	}
	for i, tm := range times {
		rec := Record{
			Summary: Summary{RunID: fmt.Sprintf("run-%d", i), Time: tm, EventCount: i + 1},
			Events:  make([]event.Event, i+1),
		}
		if err := store.Save(rec); err != nil {
			t.Fatalf("Save run-%d: %v", i, err)
		}
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List = %d summaries, want 3", len(list))
	}
	wantOrder := []string{"run-2", "run-1", "run-0"} // newest first
	for i, s := range list {
		if s.RunID != wantOrder[i] {
			t.Fatalf("List[%d].RunID = %q, want %q (newest first): %+v", i, s.RunID, wantOrder[i], list)
		}
	}
	if list[0].EventCount != 3 {
		t.Fatalf("List[0].EventCount = %d, want 3", list[0].EventCount)
	}

	// Summary has no Events field — confirm the file's "events" payload
	// doesn't leak into the decoded value by checking a raw byte scan of the
	// on-disk file still round-trips into a Summary with only the summary
	// fields set (no panic / no runtime "events" field exists on Summary,
	// enforced at compile time by the Summary type itself).
	var probe Summary
	rv := reflect.ValueOf(probe)
	if _, ok := rv.Type().FieldByName("Events"); ok {
		t.Fatal("Summary must not have an Events field")
	}
}

func TestStore_ListMissingDirEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	store := NewStore(dir, 10)
	list, err := store.List()
	if err != nil {
		t.Fatalf("List (missing dir): unexpected error %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List (missing dir) = %+v, want empty", list)
	}
}

// ---- Row 6: retention -------------------------------------------------------

func TestStore_Retention(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 2)

	for i := 0; i < 3; i++ {
		rec := Record{Summary: Summary{
			RunID: fmt.Sprintf("run-%d", i),
			Time:  time.Date(2026, 7, 3, 10+i, 0, 0, 0, time.UTC),
		}}
		if err := store.Save(rec); err != nil {
			t.Fatalf("Save run-%d: %v", i, err)
		}
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List = %d, want 2 (keep=2 after 3 saves)", len(list))
	}
	for _, s := range list {
		if s.RunID == "run-0" {
			t.Fatalf("List = %+v, want the oldest (run-0) pruned", list)
		}
	}
}

// ---- Row 7: Recorder end-to-end + cursor -----------------------------------

func TestRecorder_EndToEndAndCursor(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 10)
	bus := event.NewBus(fixedClock)

	var logMu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	rc := NewRecorder(store, bus, fixedClock, logf)

	startTime := time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC)
	bus.Emit(event.Event{Type: event.TypeRunStarted, RunID: "run-x", Lane: "ehr", UC: "uc01", Time: startTime})
	bus.Emit(event.Event{Type: event.TypeObserver, RunID: "run-x"})
	bus.Emit(event.Event{Type: event.TypeChild, Detail: "unstamped noise"}) // no RunID
	bus.Emit(event.Event{Type: event.TypeRunFinished, RunID: "run-x", Detail: "active coverage"})

	rc.RunCompleted(runner.Result{
		RunID: "run-x", Lane: "ehr", UC: "uc01", Branch: "covered",
		State: runner.StatePassed, Detail: "active coverage",
	})

	got, err := store.Get("run-x")
	if err != nil {
		t.Fatalf("Get(run-x): %v", err)
	}
	if got == nil {
		t.Fatal("Get(run-x) = nil, want the recorded run")
	}
	if len(got.Events) != 3 {
		t.Fatalf("run-x Events = %d, want exactly 3 (the unstamped noise event must be excluded): %+v", len(got.Events), got.Events)
	}
	for _, e := range got.Events {
		if e.RunID != "run-x" {
			t.Fatalf("run-x record contains a non-run-x event: %+v", e)
		}
	}
	if !got.Summary.Time.Equal(startTime) {
		t.Fatalf("Summary.Time = %v, want the run.started event's Time %v", got.Summary.Time, startTime)
	}
	if got.Summary.Lane != "ehr" || got.Summary.UC != "uc01" || got.Summary.Branch != "covered" ||
		got.Summary.State != runner.StatePassed || got.Summary.Detail != "active coverage" {
		t.Fatalf("Summary result fields not copied from Result: %+v", got.Summary)
	}
	if got.Summary.EventCount != 3 {
		t.Fatalf("Summary.EventCount = %d, want 3", got.Summary.EventCount)
	}

	// A second run's events land after run-x's terminal event. RunCompleted
	// for run-y must see ONLY run-y's events — the recorder's cursor advanced
	// past run-x (sound under sequential-only v1: the Sink runs under the run
	// lock, so nothing later in the ring can belong to an earlier run).
	bus.Emit(event.Event{Type: event.TypeRunStarted, RunID: "run-y", Lane: "ehr", UC: "uc02"})
	bus.Emit(event.Event{Type: event.TypeRunFinished, RunID: "run-y"})

	rc.RunCompleted(runner.Result{RunID: "run-y", Lane: "ehr", UC: "uc02", State: runner.StatePassed})

	got2, err := store.Get("run-y")
	if err != nil {
		t.Fatalf("Get(run-y): %v", err)
	}
	if got2 == nil {
		t.Fatal("Get(run-y) = nil, want the recorded run")
	}
	if len(got2.Events) != 2 {
		t.Fatalf("run-y Events = %d, want exactly 2 (run-x's events must not reappear): %+v", len(got2.Events), got2.Events)
	}
	for _, e := range got2.Events {
		if e.RunID != "run-y" {
			t.Fatalf("run-y record contains a non-run-y event (cursor did not advance past run-x): %+v", e)
		}
	}

	logMu.Lock()
	defer logMu.Unlock()
	if len(logs) != 0 {
		t.Fatalf("logf called on a successful capture: %v", logs)
	}
}

// ---- Row 8: Recorder save-failure is logged, not fatal ---------------------

func TestRecorder_SaveFailureLoggedNotFatal(t *testing.T) {
	dir := t.TempDir()
	unwritableDir := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(unwritableDir, []byte("x"), 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	store := NewStore(unwritableDir, 10) // dir points at a FILE, not a directory

	bus := event.NewBus(fixedClock)

	var logMu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	rc := NewRecorder(store, bus, fixedClock, logf)
	bus.Emit(event.Event{Type: event.TypeRunStarted, RunID: "run-z"})
	bus.Emit(event.Event{Type: event.TypeRunFinished, RunID: "run-z"})

	rc.RunCompleted(runner.Result{RunID: "run-z", State: runner.StateFailed, Detail: "boom"})

	logMu.Lock()
	defer logMu.Unlock()
	if len(logs) != 1 {
		t.Fatalf("logf calls = %d, want exactly 1: %+v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "run-z") {
		t.Fatalf("log line = %q, want it to name the run id run-z", logs[0])
	}
}

// ---- Row 9: NewRecorder nil now/logf defaults ------------------------------

// TestNewRecorder_NilLogfDefaultsToNoop: NewRecorder(store, bus, nil, nil)
// must not panic on construction, and a subsequent RunCompleted whose Save
// fails (the runner.Sink path the kit daemon wires with a real logf in
// practice, but a misconfigured caller might not) must swallow the failure
// silently rather than calling a nil logf. RED against a NewRecorder that
// stores a nil logf verbatim: logf(...) on a nil func value panics.
func TestNewRecorder_NilLogfDefaultsToNoop(t *testing.T) {
	dir := t.TempDir()
	unwritableDir := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(unwritableDir, []byte("x"), 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	store := NewStore(unwritableDir, 10) // dir points at a FILE, not a directory: Save fails

	bus := event.NewBus(fixedClock)

	rc := NewRecorder(store, bus, nil, nil)
	bus.Emit(event.Event{Type: event.TypeRunStarted, RunID: "run-nil-logf"})
	bus.Emit(event.Event{Type: event.TypeRunFinished, RunID: "run-nil-logf"})

	// Must not panic (a nil logf called by the old code would).
	rc.RunCompleted(runner.Result{RunID: "run-nil-logf", State: runner.StateFailed, Detail: "boom"})
}

// TestNewRecorder_NilNowDefaultsToTimeNow: NewRecorder(store, bus, nil, nil)
// with a WORKING store still saves correctly — nil now must default to a
// usable clock (time.Now), not a nil func value. RunCompleted is called for a
// RunID with NO matched bus events, which forces the `t := rc.now()` fallback
// path (the type doc: now is the fallback Time source for a run with no
// matched events) — a stored nil now would panic there.
func TestNewRecorder_NilNowDefaultsToTimeNow(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 10)
	bus := event.NewBus(fixedClock)

	rc := NewRecorder(store, bus, nil, nil)

	before := time.Now()
	// Deliberately no bus.Emit for "run-nil-now" — matched stays empty, so
	// RunCompleted must fall back to rc.now() rather than matched[0].Time.
	rc.RunCompleted(runner.Result{
		RunID: "run-nil-now", Lane: "ehr", UC: "uc01", Branch: "covered",
		State: runner.StatePassed, Detail: "active coverage",
	})
	after := time.Now()

	got, err := store.Get("run-nil-now")
	if err != nil {
		t.Fatalf("Get(run-nil-now): %v", err)
	}
	if got == nil {
		t.Fatal("Get(run-nil-now) = nil, want the recorded run")
	}
	if got.Summary.Time.Before(before) || got.Summary.Time.After(after) {
		t.Fatalf("Summary.Time = %v, want it within [%v, %v] (the defaulted time.Now fallback)", got.Summary.Time, before, after)
	}
	if got.Summary.RunID != "run-nil-now" || got.Summary.State != runner.StatePassed {
		t.Fatalf("Summary = %+v, unexpected", got.Summary)
	}
}
