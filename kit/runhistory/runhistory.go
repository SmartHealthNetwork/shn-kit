// Package runhistory is the SHN Kit's run-history capture spine: a
// file-per-run Store that persists a Record — a Summary
// plus its full run-timeline Events slice — and a Recorder that captures one
// Record synchronously each time a runner.Runner completes a run, via the
// runner.Sink hook.
//
// A Record is byte-for-byte the export document: what Save writes to disk is
// exactly what a later "download this run" affordance
// hands back to the caller, unmodified. Import direction is one-way —
// runhistory imports runner and event; runner sees only its own Sink
// interface and never imports runhistory — so the runner package stays
// ignorant of history capture entirely.
package runhistory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/runner"
)

// Summary is a run's history-list entry: identity, outcome, and the Time of
// the run's first stamped event (the run.started event's Time in practice) —
// everything the local UI's run-history list needs without paying to decode
// every run's full Events slice.
type Summary struct {
	RunID      string    `json:"runId"`
	Lane       string    `json:"lane"`
	UC         string    `json:"uc"`
	Branch     string    `json:"branch"`
	State      string    `json:"state"`
	Detail     string    `json:"detail"`
	Time       time.Time `json:"time"`
	EventCount int       `json:"eventCount"`
}

// Record is one run's full history capture: its Summary plus every bus event
// stamped with the run's id. This is the export document (package doc) —
// Save/Get round-trip it byte-for-byte.
type Record struct {
	Summary
	Events []event.Event `json:"events"`
}

// Store persists Records as one JSON file per run under dir, retaining only
// the most recent keep runs. Zero value is not usable; construct with
// NewStore.
type Store struct {
	dir  string
	keep int

	mu sync.Mutex
}

// NewStore constructs a Store rooted at dir, retaining at most keep runs.
// dir need not exist yet — Save creates it on first use.
func NewStore(dir string, keep int) *Store {
	return &Store{dir: dir, keep: keep}
}

// fileName is the on-disk filename for rec: the run's own clock-derived Time
// (rec.Time.UnixNano(), zero-padded so lexical and chronological file-name
// order agree), not a wall-clock read of Save's own — Save takes no read of
// its own so a Record's filename is fully determined by its contents (review
// finding 7).
func fileName(rec Record) string {
	return fmt.Sprintf("%020d-%s.json", rec.Time.UnixNano(), rec.RunID)
}

// Save writes rec to dir/<unixnano>-<runID>.json (mode 0600) and prunes the
// oldest files beyond keep.
func (s *Store) Save(rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("runhistory: create %s: %w", s.dir, err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("runhistory: marshal record %s: %w", rec.RunID, err)
	}
	path := filepath.Join(s.dir, fileName(rec))
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("runhistory: write %s: %w", path, err)
	}

	s.prune()
	return nil
}

// prune removes the oldest run files beyond keep. Filenames are timestamp-
// prefixed (fileName) so ascending name order is ascending run time.
// Best-effort: a failure to list or remove a file does not fail the Save
// that triggered it — retention is a housekeeping nicety, not a durability
// guarantee.
func (s *Store) prune() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	names := jsonFileNames(entries)
	sort.Strings(names) // ascending: oldest first
	for len(names) > s.keep {
		_ = os.Remove(filepath.Join(s.dir, names[0]))
		names = names[1:]
	}
}

// jsonFileNames returns the .json regular-file names in entries, unsorted.
func jsonFileNames(entries []os.DirEntry) []string {
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	return names
}

// List returns every run's Summary, newest first. A missing dir (no run has
// ever been saved) is not an error — it reads as an empty list.
//
// Each file is fully decoded (into a Summary, which simply ignores the
// on-disk "events" array) to fill in the list — 200 payload-heavy files per
// call is fine for the local UI's on-change fetch; revisit if a
// partner-facing --history-keep setting grows the retention window far
// beyond that.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("runhistory: list %s: %w", s.dir, err)
	}
	names := jsonFileNames(entries)
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // newest first

	out := make([]Summary, 0, len(names))
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue // best-effort: one unreadable file must not break the whole list
		}
		var sum Summary
		if err := json.Unmarshal(b, &sum); err != nil {
			continue
		}
		out = append(out, sum)
	}
	return out, nil
}

// Get returns the full Record for runID, or (nil, nil) when no run with that
// id has been saved.
func (s *Store) Get(runID string) (*Record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("runhistory: list %s: %w", s.dir, err)
	}
	suffix := "-" + runID + ".json"
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("runhistory: read %s: %w", e.Name(), err)
		}
		var rec Record
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, fmt.Errorf("runhistory: decode %s: %w", e.Name(), err)
		}
		return &rec, nil
	}
	return nil, nil
}

// Recorder implements runner.Sink: it captures one Record per completed run
// by reading the bus's ring since its last read.
//
// The cursor is sound under sequential-only v1 ONLY: RunCompleted runs
// synchronously inside runLocked's defer, still under the run lock, so by
// the time it fires no later event on the bus can
// belong to an EARLIER run — it is safe to advance the cursor past
// everything Since just returned (including unstamped noise events, which
// were never capturable by RunID anyway) rather than re-scanning the whole
// ring on every call.
type Recorder struct {
	store *Store
	bus   *event.Bus
	now   func() time.Time
	logf  func(string, ...any)

	mu     sync.Mutex
	cursor uint64
}

// NewRecorder constructs a Recorder. now is the fallback Time source for a
// (defensively unexpected) run with no matched events; logf receives a save
// failure's diagnostic line (Recorder.RunCompleted never fails loudly — a
// history-capture problem must not take down a run).
//
// A nil now defaults to time.Now and a nil logf defaults to a no-op —
// defensive defaults so a misconfigured caller cannot inject a nil-func-call
// panic into RunCompleted, which the runner (kit/runner) invokes synchronously
// from inside runLocked's lock-held defer: a nil logf
// called after a failed Save would panic there, and — absent the runner's own
// isolating recover — wedge the runner into permanent ErrRunInFlight.
func NewRecorder(store *Store, bus *event.Bus, now func() time.Time, logf func(string, ...any)) *Recorder {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Recorder{store: store, bus: bus, now: now, logf: logf}
}

// RunCompleted implements runner.Sink: it reads the bus since the Recorder's
// cursor, keeps only res.RunID's events, advances the cursor past everything
// it saw (see the type doc), and saves the resulting Record.
func (rc *Recorder) RunCompleted(res runner.Result) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	evs := rc.bus.Since(rc.cursor)
	var matched []event.Event
	var maxSeq uint64
	for _, e := range evs {
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
		if e.RunID == res.RunID {
			matched = append(matched, e)
		}
	}
	if maxSeq > rc.cursor {
		rc.cursor = maxSeq
	}

	t := rc.now()
	if len(matched) > 0 {
		t = matched[0].Time
	}

	rec := Record{
		Summary: Summary{
			RunID:      res.RunID,
			Lane:       res.Lane,
			UC:         res.UC,
			Branch:     res.Branch,
			State:      res.State,
			Detail:     res.Detail,
			Time:       t,
			EventCount: len(matched),
		},
		Events: matched,
	}
	if err := rc.store.Save(rec); err != nil {
		rc.logf("kit/runhistory: save run %s: %v", res.RunID, err)
	}
}
