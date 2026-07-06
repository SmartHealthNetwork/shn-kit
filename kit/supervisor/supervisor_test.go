// This test file is Unix-only in practice: isAlive uses syscall.Signal(0)
// process probing (darwin/linux — the repo's dev+CI targets). Production
// code stays Windows-portable via runtime.GOOS in Stop.
package supervisor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess is not a real test — it is the stdlib helper-process
// pattern's stub child, invoked as a subprocess via
// `os.Args[0] -test.run=TestHelperProcess` with GO_WANT_HELPER_PROCESS=1.
// It serves a tiny HTTP server whose behavior is driven entirely by env
// (STUB_ADDR, STUB_HEALTHY, STUB_READY_AFTER_MS) so tests never touch a
// real binary.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	addr := os.Getenv("STUB_ADDR")
	healthy := os.Getenv("STUB_HEALTHY") == "1"
	// STUB_READY_AFTER_MS: /health stays 503 for that many ms after boot,
	// then follows STUB_HEALTHY — a slow-to-become-ready child, for racing
	// Stop against an in-flight Start's spawn/probe window.
	var readyAfter time.Duration
	if ms, err := strconv.Atoi(os.Getenv("STUB_READY_AFTER_MS")); err == nil {
		readyAfter = time.Duration(ms) * time.Millisecond
	}

	// STUB_PID_FILE: if set, every boot of this spec (across every
	// respawn/Restart of the same ChildSpec) appends its own PID as one
	// line — a spawn history independent of the supervisor's own
	// bookkeeping, so a test can prove "exactly one live process" even
	// across a Restart-vs-crash-monitor interleave. Boot number (1-based)
	// is derived from the line count already in the file when this boot
	// starts, and also gates STUB_UNHEALTHY_FROM_BOOT below.
	bootN := 0
	if pf := os.Getenv("STUB_PID_FILE"); pf != "" {
		if data, err := os.ReadFile(pf); err == nil && len(data) > 0 {
			bootN = len(strings.Split(strings.TrimRight(string(data), "\n"), "\n"))
		}
		if f, err := os.OpenFile(pf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
		}
	}
	thisBoot := bootN + 1

	// STUB_UNHEALTHY_FROM_BOOT: 1-based boot number from which /health goes
	// permanently 503 regardless of STUB_HEALTHY — simulates a spec that
	// was healthy on its first boot but fails its ready probe on a later
	// respawn (requires STUB_PID_FILE for boot counting).
	if n, err := strconv.Atoi(os.Getenv("STUB_UNHEALTHY_FROM_BOOT")); err == nil && n > 0 && thisBoot >= n {
		healthy = false
	}

	bootT := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		if healthy && time.Since(bootT) >= readyAfter {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("POST /exit", func(http.ResponseWriter, *http.Request) {
		fmt.Println("stub: exiting on cue")
		os.Exit(2)
	})
	fmt.Println("stub: listening", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Println("stub:", err)
		os.Exit(1)
	}
}

// stubSpec builds a ChildSpec that spawns THIS test binary in helper-process
// mode, bound to a freshly allocated loopback port.
func stubSpec(t *testing.T, name string, healthy bool, restartMax int) (ChildSpec, string) {
	t.Helper()
	ports, err := AllocatePorts(1)
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", ports[0])
	return ChildSpec{
		Name:    name,
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess"},
		Env: []string{"GO_WANT_HELPER_PROCESS=1", "STUB_ADDR=" + addr,
			"STUB_HEALTHY=" + map[bool]string{true: "1", false: "0"}[healthy]},
		LogPath:      filepath.Join(t.TempDir(), name+".log"),
		ReadyURLs:    []string{"http://" + addr + "/health"},
		ReadyTimeout: 5 * time.Second,
		RestartMax:   restartMax,
	}, addr
}

// noticeCollector is a mutex-guarded sink for Notice callbacks, so tests can
// poll observed state transitions without racing the supervisor's goroutines.
type noticeCollector struct {
	mu    sync.Mutex
	items []Notice
}

func (c *noticeCollector) notify(n Notice) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, n)
}

func (c *noticeCollector) snapshot() []Notice {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Notice, len(c.items))
	copy(out, c.items)
	return out
}

func statesOf(notices []Notice, child string) []string {
	var out []string
	for _, n := range notices {
		if n.Child == child {
			out = append(out, n.State)
		}
	}
	return out
}

func countState(states []string, want string) int {
	n := 0
	for _, s := range states {
		if s == want {
			n++
		}
	}
	return n
}

// containsInOrder reports whether seq appears as a (not necessarily
// contiguous) subsequence of states, in order.
func containsInOrder(states []string, seq []string) bool {
	i := 0
	for _, s := range states {
		if i >= len(seq) {
			break
		}
		if s == seq[i] {
			i++
		}
	}
	return i == len(seq)
}

// waitFor polls cond until it's true or timeout elapses, returning cond's
// final value. No fixed sleeps as synchronization — only bounded polling.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return cond()
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// isAlive reports whether pid names a live process, via signal-0 probing
// (unix). The Kit's dev/CI targets are darwin/linux.
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func postExit(addr string) {
	resp, err := http.Post("http://"+addr+"/exit", "text/plain", nil)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

func statusOf(statuses []ChildStatus, name string) (ChildStatus, bool) {
	for _, st := range statuses {
		if st.Name == name {
			return st, true
		}
	}
	return ChildStatus{}, false
}

// Row 1: healthy child — Start succeeds, Status reports ready+PID, the log
// file captures stub output, and starting/ready notices fire in order.
func TestSupervisor_HealthyChild(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, _ := stubSpec(t, "healthy1", true, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	st, ok := statusOf(s.Status(), "healthy1")
	if !ok {
		t.Fatal("expected a status entry for healthy1")
	}
	if st.State != "ready" {
		t.Fatalf("expected state ready, got %q", st.State)
	}
	if st.PID <= 0 {
		t.Fatalf("expected PID > 0, got %d", st.PID)
	}

	data, err := os.ReadFile(spec.LogPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "stub: listening") {
		t.Fatalf("log missing %q marker, got: %s", "stub: listening", data)
	}

	states := statesOf(nc.snapshot(), "healthy1")
	if len(states) != 2 || states[0] != "starting" || states[1] != "ready" {
		t.Fatalf("expected notices [starting ready], got %v", states)
	}
}

// Row 2: unhealthy child (always 503) — Start fails naming the child and the
// failing URL, and the spawned process is killed.
func TestSupervisor_UnhealthyChild(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, _ := stubSpec(t, "unhealthy1", false, 0)
	spec.ReadyTimeout = 1500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.Start(ctx, spec)
	if err == nil {
		t.Fatal("expected Start to return an error")
	}
	if !strings.Contains(err.Error(), "unhealthy1") {
		t.Fatalf("error missing child name: %v", err)
	}
	if !strings.Contains(err.Error(), spec.ReadyURLs[0]) {
		t.Fatalf("error missing failing URL: %v", err)
	}

	st, ok := statusOf(s.Status(), "unhealthy1")
	if !ok {
		t.Fatal("expected a status entry for unhealthy1")
	}
	if st.State != "failed" {
		t.Fatalf("expected state failed, got %q", st.State)
	}
	if st.PID <= 0 {
		t.Fatalf("expected PID recorded, got %d", st.PID)
	}

	if !waitFor(3*time.Second, func() bool { return !isAlive(st.PID) }) {
		t.Fatalf("process %d still alive after ready-timeout kill", st.PID)
	}
}

// Row 3: crash on cue — after a POST /exit, the supervisor observes
// exited→restarting→ready, Restarts increments, and the SAME address answers
// /health again (spec-stable restart, env/ports fixed).
func TestSupervisor_CrashRestarts(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, addr := stubSpec(t, "crash1", true, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	postExit(addr)

	if !waitFor(5*time.Second, func() bool {
		return containsInOrder(statesOf(nc.snapshot(), "crash1"), []string{"exited", "restarting", "ready"})
	}) {
		t.Fatalf("did not observe exited/restarting/ready in order: %v", statesOf(nc.snapshot(), "crash1"))
	}

	st, ok := statusOf(s.Status(), "crash1")
	if !ok {
		t.Fatal("expected a status entry for crash1")
	}
	if st.Restarts != 1 {
		t.Fatalf("expected Restarts=1, got %d", st.Restarts)
	}

	if !waitFor(3*time.Second, func() bool {
		resp, err := http.Get("http://" + addr + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatal("restarted child not answering /health on the same address")
	}
}

// Row 4: restart cap — crash twice (second crash only after the restart's
// own ready notice lands); the second crash exceeds RestartMax=1 and the
// child ends in state failed with a Detail mentioning the restart cap.
func TestSupervisor_RestartCap(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, addr := stubSpec(t, "cap1", true, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	postExit(addr)

	if !waitFor(5*time.Second, func() bool {
		return countState(statesOf(nc.snapshot(), "cap1"), "ready") >= 2
	}) {
		t.Fatalf("did not observe the restart's ready notice: %v", statesOf(nc.snapshot(), "cap1"))
	}

	postExit(addr)

	if !waitFor(5*time.Second, func() bool {
		for _, n := range nc.snapshot() {
			if n.Child == "cap1" && n.State == "failed" && strings.Contains(n.Detail, "restart cap") {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("did not observe a failed notice mentioning restart cap: %v", nc.snapshot())
	}

	st, ok := statusOf(s.Status(), "cap1")
	if !ok {
		t.Fatal("expected a status entry for cap1")
	}
	if st.State != "failed" {
		t.Fatalf("expected final state failed, got %q", st.State)
	}
}

// Row 5: deliberate Stop — no restart notice fires, the child ends stopped,
// and the process is gone.
func TestSupervisor_Stop(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, _ := stubSpec(t, "stop1", true, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}

	before, ok := statusOf(s.Status(), "stop1")
	if !ok {
		t.Fatal("expected a status entry for stop1")
	}

	if err := s.Stop("stop1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	after, ok := statusOf(s.Status(), "stop1")
	if !ok {
		t.Fatal("expected a status entry for stop1 after Stop")
	}
	if after.State != "stopped" {
		t.Fatalf("expected state stopped, got %q", after.State)
	}

	for _, n := range nc.snapshot() {
		if n.Child == "stop1" && n.State == "restarting" {
			t.Fatalf("unexpected restarting notice after deliberate Stop: %+v", n)
		}
	}

	if !waitFor(3*time.Second, func() bool { return !isAlive(before.PID) }) {
		t.Fatalf("process %d still alive after Stop", before.PID)
	}
}

// Fix round 1 (CRITICAL): a deliberate Stop landing in the
// exited→restarting→respawn window must be honored — NO respawn follows,
// the child ends stopped. The 500ms backoff gives the window; we
// synchronize on the exited notice (never a sleep) to land Stop inside it.
func TestSupervisor_StopDuringRestartWindow(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, addr := stubSpec(t, "stopwin1", true, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	postExit(addr)

	if !waitFor(5*time.Second, func() bool {
		return countState(statesOf(nc.snapshot(), "stopwin1"), "exited") >= 1
	}) {
		t.Fatalf("did not observe the exited notice: %v", statesOf(nc.snapshot(), "stopwin1"))
	}

	if err := s.Stop("stopwin1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if !waitFor(5*time.Second, func() bool {
		st, ok := statusOf(s.Status(), "stopwin1")
		return ok && st.State == "stopped"
	}) {
		st, _ := statusOf(s.Status(), "stopwin1")
		t.Fatalf("child did not reach stopped after Stop-in-window; state=%q notices=%v",
			st.State, statesOf(nc.snapshot(), "stopwin1"))
	}

	// Bounded observation window (> the 500ms×1 backoff, generously): the
	// child must NOT come back on its address after the deliberate Stop.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			t.Fatalf("child answered %s after deliberate Stop — respawn leaked", addr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	states := statesOf(nc.snapshot(), "stopwin1")
	if countState(states, "ready") != 1 {
		t.Fatalf("expected exactly one ready notice (no post-Stop restart), got %v", states)
	}
	st, _ := statusOf(s.Status(), "stopwin1")
	if st.State != "stopped" {
		t.Fatalf("expected final state stopped, got %q", st.State)
	}
}

// Fix round 2 (IMPORTANT): a Stop racing an in-flight Start (child already
// registered, spawn/ready-probe not yet complete — the StopAll-during-boot
// shape) must converge to stopped: never a running child whose Stop already
// returned nil. The stub stays 503 for 300ms so Stop reliably lands in the
// pre-spawn or probe window; whichever window the scheduler picks, the
// invariant asserted is the one that matters: after both calls return, the
// process is gone and the final state is stopped — never ready.
func TestSupervisor_StopDuringStart(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, addr := stubSpec(t, "stopstart1", true, 2)
	spec.Env = append(spec.Env, "STUB_READY_AFTER_MS=300")
	spec.ReadyTimeout = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(ctx, spec) }()

	if !waitFor(5*time.Second, func() bool {
		return countState(statesOf(nc.snapshot(), "stopstart1"), "starting") >= 1
	}) {
		t.Fatalf("did not observe the starting notice: %v", nc.snapshot())
	}

	if err := s.Stop("stopstart1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	err := <-startErr
	// Start either returns the stopped-during-startup error, or — had the
	// scheduler let Start finish first — nil with Stop converging via the
	// normal monitor path. Either way the invariant below must hold.

	if !waitFor(5*time.Second, func() bool {
		st, ok := statusOf(s.Status(), "stopstart1")
		return ok && st.State == "stopped"
	}) {
		st, _ := statusOf(s.Status(), "stopstart1")
		t.Fatalf("child did not converge to stopped after Stop-during-Start; state=%q startErr=%v notices=%v",
			st.State, err, statesOf(nc.snapshot(), "stopstart1"))
	}

	if st, _ := statusOf(s.Status(), "stopstart1"); st.PID > 0 {
		if !waitFor(3*time.Second, func() bool { return !isAlive(st.PID) }) {
			t.Fatalf("process %d still alive after Stop-during-Start", st.PID)
		}
	}

	// The child must never come up on its address after the Stop.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			t.Fatalf("child answered %s after Stop-during-Start", addr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Fix round 1 (IMPORTANT b): a child whose spawn fails (nonexistent binary)
// still gets a notice stream beginning with starting, then failed.
func TestSupervisor_SpawnFailNotices(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, _ := stubSpec(t, "nospawn1", true, 0)
	spec.Command = filepath.Join(t.TempDir(), "no-such-binary")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.Start(ctx, spec)
	if err == nil {
		t.Fatal("expected Start to fail for a nonexistent binary")
	}

	states := statesOf(nc.snapshot(), "nospawn1")
	if len(states) != 2 || states[0] != "starting" || states[1] != "failed" {
		t.Fatalf("expected notices [starting failed], got %v", states)
	}

	st, ok := statusOf(s.Status(), "nospawn1")
	if !ok {
		t.Fatal("expected a status entry for nospawn1")
	}
	if st.State != "failed" {
		t.Fatalf("expected state failed, got %q", st.State)
	}
}

// Fix round 1 (IMPORTANT a): a child whose log file can't be opened emits a
// failed notice (it never started, so no starting precedes it) and is
// visible in Status as failed.
func TestSupervisor_LogOpenFailNotices(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, _ := stubSpec(t, "nolog1", true, 0)
	spec.LogPath = filepath.Join(t.TempDir(), "no-such-dir", "child.log")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.Start(ctx, spec)
	if err == nil {
		t.Fatal("expected Start to fail when the log file can't be opened")
	}

	states := statesOf(nc.snapshot(), "nolog1")
	if len(states) != 1 || states[0] != "failed" {
		t.Fatalf("expected notices [failed], got %v", states)
	}

	st, ok := statusOf(s.Status(), "nolog1")
	if !ok {
		t.Fatal("expected a status entry for nolog1")
	}
	if st.State != "failed" {
		t.Fatalf("expected state failed, got %q", st.State)
	}
}

// Restart row 1 (happy path): a ready child, Restart'd, comes back with a
// NEW pid, walks stopped->starting->ready (never restarting — that state is
// crash-only, reserved for the SSE relay's gateway ResetCursor hook), its
// Restarts counter resets to 0, and the log file accumulates both boots'
// output (reopened in append mode, not truncated).
func TestSupervisor_Restart(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, _ := stubSpec(t, "restart1", true, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	before, ok := statusOf(s.Status(), "restart1")
	if !ok {
		t.Fatal("expected a status entry for restart1")
	}

	if err := s.Restart(ctx, "restart1"); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	after, ok := statusOf(s.Status(), "restart1")
	if !ok {
		t.Fatal("expected a status entry for restart1 after Restart")
	}
	if after.State != "ready" {
		t.Fatalf("expected state ready after Restart, got %q", after.State)
	}
	if after.PID <= 0 || after.PID == before.PID {
		t.Fatalf("expected a NEW pid after Restart, before=%d after=%d", before.PID, after.PID)
	}
	if after.Restarts != 0 {
		t.Fatalf("expected Restarts reset to 0 after Restart, got %d", after.Restarts)
	}

	if !waitFor(3*time.Second, func() bool { return !isAlive(before.PID) }) {
		t.Fatalf("old process %d still alive after Restart", before.PID)
	}
	if !isAlive(after.PID) {
		t.Fatal("new process not alive after Restart")
	}

	states := statesOf(nc.snapshot(), "restart1")
	if !containsInOrder(states, []string{"stopped", "starting", "ready"}) {
		t.Fatalf("expected a stopped->starting->ready subsequence, got %v", states)
	}
	if countState(states, "restarting") != 0 {
		t.Fatalf("a deliberate Restart must never emit a restarting notice (crash-only), got %v", states)
	}

	data, err := os.ReadFile(spec.LogPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if n := strings.Count(string(data), "stub: listening"); n != 2 {
		t.Fatalf("expected 2 boots' worth of output in the single log file, got %d occurrences: %s", n, data)
	}
}

// Restart row 2: an unknown child name errors, naming the child.
func TestSupervisor_RestartUnknownChild(t *testing.T) {
	s := New(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.Restart(ctx, "nosuchchild")
	if err == nil {
		t.Fatal("expected Restart to error for an unknown child")
	}
	if !strings.Contains(err.Error(), "nosuchchild") {
		t.Fatalf("error missing child name: %v", err)
	}
}

// Restart row 3: Restart of a child that already crashed past RestartMax
// (state failed) respawns cleanly — the recovery affordance the UI needs.
func TestSupervisor_RestartRecoversFailedChild(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, addr := stubSpec(t, "restartfailed1", true, 0) // RestartMax=0: first crash goes straight to failed

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	postExit(addr)

	if !waitFor(5*time.Second, func() bool {
		st, ok := statusOf(s.Status(), "restartfailed1")
		return ok && st.State == "failed"
	}) {
		t.Fatalf("child did not reach failed after crash past RestartMax=0: %v",
			statesOf(nc.snapshot(), "restartfailed1"))
	}

	if err := s.Restart(ctx, "restartfailed1"); err != nil {
		t.Fatalf("Restart of a failed child: %v", err)
	}

	after, ok := statusOf(s.Status(), "restartfailed1")
	if !ok {
		t.Fatal("expected a status entry after Restart")
	}
	if after.State != "ready" {
		t.Fatalf("expected state ready after recovering a failed child, got %q", after.State)
	}
	if after.PID <= 0 {
		t.Fatalf("expected a live PID after recovery, got %d", after.PID)
	}
	if !waitFor(3*time.Second, func() bool {
		resp, err := http.Get("http://" + addr + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatal("recovered child not answering /health")
	}
}

// Restart row 4: the respawn's ready probe fails (the child was healthy on
// its first boot, but this spec is configured to go unhealthy starting on
// boot 2 — i.e. the Restart's respawn). The child ends failed, Restart
// returns an error, and the killed process leaves no zombie.
func TestSupervisor_RestartReadyProbeFailure(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, _ := stubSpec(t, "restartfail1", true, 2)
	spec.Env = append(spec.Env,
		"STUB_PID_FILE="+filepath.Join(t.TempDir(), "restartfail1.pids"),
		"STUB_UNHEALTHY_FROM_BOOT=2")
	spec.ReadyTimeout = 1500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	err := s.Restart(ctx, "restartfail1")
	if err == nil {
		t.Fatal("expected Restart to error when the respawn fails its ready probe")
	}

	st, ok := statusOf(s.Status(), "restartfail1")
	if !ok {
		t.Fatal("expected a status entry for restartfail1")
	}
	if st.State != "failed" {
		t.Fatalf("expected state failed after a ready-probe failure on respawn, got %q", st.State)
	}
	if st.PID <= 0 {
		t.Fatalf("expected the killed process's PID recorded, got %d", st.PID)
	}
	if !waitFor(3*time.Second, func() bool { return !isAlive(st.PID) }) {
		t.Fatalf("process %d still alive after a ready-probe failure on respawn (zombie)", st.PID)
	}
}

// Restart row 5 (interleave): kill the child (crash) then, synchronized on
// the exited notice (never a sleep — mirrors the existing Stop-vs-crash
// interleave rows), immediately Restart. Exactly one live process must
// remain: Status gives the current generation's pid; STUB_PID_FILE gives the
// FULL spawn history so a stale crash-monitor respawn can't hide behind
// Status's point-in-time snapshot. This is the no-double-spawn / generation
// field's reason to exist (I6).
func TestSupervisor_RestartRacesCrashMonitor(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, addr := stubSpec(t, "restartrace1", true, 3)
	pidFile := filepath.Join(t.TempDir(), "restartrace1.pids")
	spec.Env = append(spec.Env, "STUB_PID_FILE="+pidFile)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	postExit(addr)

	if !waitFor(5*time.Second, func() bool {
		return countState(statesOf(nc.snapshot(), "restartrace1"), "exited") >= 1
	}) {
		t.Fatalf("did not observe the exited notice: %v", statesOf(nc.snapshot(), "restartrace1"))
	}

	if err := s.Restart(ctx, "restartrace1"); err != nil {
		t.Fatalf("Restart racing the crash monitor: %v", err)
	}

	st, ok := statusOf(s.Status(), "restartrace1")
	if !ok {
		t.Fatal("expected a status entry for restartrace1")
	}
	if st.State != "ready" {
		t.Fatalf("expected state ready after Restart, got %q", st.State)
	}
	if !isAlive(st.PID) {
		t.Fatalf("Status-reported PID %d is not alive", st.PID)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(data)))
	aliveCount := 0
	for _, l := range lines {
		pid, err := strconv.Atoi(l)
		if err != nil {
			continue
		}
		if isAlive(pid) {
			aliveCount++
		}
	}
	if aliveCount != 1 {
		t.Fatalf("expected exactly one live process across all spawned generations, got %d alive (pids=%v, status pid=%d)",
			aliveCount, lines, st.PID)
	}
}

// Restart row 5b (CRITICAL): unlike row 5 above — where the
// existing 500ms crash-bounce backoff always lets Restart finish BEFORE the
// bounce's own spawnAndWatch call even starts — this row forces Restart to
// land while the bounce's spawnAndWatch is genuinely in flight, blocked in
// its own ready probe (spawn already happened, not yet committed either
// way): the exact window the generation fence exists to protect, and which
// was previously untested.
// STUB_READY_AFTER_MS holds boot 2 (the crash-bounce's own respawn)
// un-ready for a window; we synchronize on STUB_PID_FILE recording boot 2's
// start (proof its spawnAndWatch has called spawn and is now polling
// /health) before calling Restart, so Restart's Stop() is guaranteed to
// target boot 2's process/exited channel and therefore cannot proceed
// (clear stopping, bump generation, spawn its own boot 3) until the
// bounce's own spawnAndWatch commits its decision — deterministically
// driving execution through the exact commit-point code this fix touches
// (the combined stopping/stale read, and the close-exited-only-after-
// deciding ordering) and asserting the correct outcome: boot 3 ends ready,
// never clobbered to failed, and the notice stream shows the bounce
// correctly resolving to stopped (not failed) before Restart's own
// starting/ready.
//
// Honesty note: this deterministically exercises the fixed code path, but
// does NOT reliably flip red on the pre-fix code — verified by temporarily
// reverting supervisor.go and re-running this row 5x under -race: it still
// passed every time. That is because in THIS construction Restart's Stop()
// is gated on THIS generation's own exited close either way (old code:
// closeExited() then a separate isStopping() lock call; new: one combined
// lock read), and the checking goroutine's remaining work (a mutex op) is
// far faster than Restart's remaining work (an os.OpenFile syscall) after
// unblocking, so the pre-fix ordering bug — while real, as reasoned through
// in the code comments above spawnAndWatch — needs an adversarial scheduler
// preemption between two adjacent statements to actually manifest, which
// this test's timing does not reliably force. A fully deterministic
// reproduction of "generation observed stale AT the commit point" (as
// opposed to "stopping observed true at the commit point, with generation
// still current") would additionally require winning a sub-microsecond
// race on Go's own goroutine scheduling that the existing STUB_* hooks
// cannot control; per the task brief this is flagged rather than papered
// over with a sleep-based flaky test. The fix's correctness for that
// narrower case rests on the field-backed generation check + targeted
// reasoning documented on spawnAndWatch, not on this test alone.
func TestSupervisor_RestartInterruptsInFlightSpawn(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, addr := stubSpec(t, "restartinflight1", true, 3)
	pidFile := filepath.Join(t.TempDir(), "restartinflight1.pids")
	spec.Env = append(spec.Env, "STUB_PID_FILE="+pidFile, "STUB_READY_AFTER_MS=300")
	spec.ReadyTimeout = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	postExit(addr) // crash boot 1

	if !waitFor(5*time.Second, func() bool {
		return countState(statesOf(nc.snapshot(), "restartinflight1"), "exited") >= 1
	}) {
		t.Fatalf("did not observe the exited notice: %v", statesOf(nc.snapshot(), "restartinflight1"))
	}

	// Wait for the crash-bounce's own respawn (boot 2) to have actually
	// started: its spawnAndWatch has called spawn and is now polling boot
	// 2's not-yet-ready /health (STUB_READY_AFTER_MS=300ms) — genuinely
	// in-flight, not yet committed either way.
	if !waitFor(5*time.Second, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		return len(strings.Split(strings.TrimRight(string(data), "\n"), "\n")) >= 2
	}) {
		t.Fatalf("crash-bounce's boot 2 never started (pidfile): %v",
			statesOf(nc.snapshot(), "restartinflight1"))
	}

	if err := s.Restart(ctx, "restartinflight1"); err != nil {
		t.Fatalf("Restart racing an in-flight crash-bounce spawn: %v", err)
	}

	st, ok := statusOf(s.Status(), "restartinflight1")
	if !ok {
		t.Fatal("expected a status entry for restartinflight1")
	}
	if st.State != "ready" {
		t.Fatalf("expected state ready after Restart, got %q (the CRITICAL bug clobbers this to failed)", st.State)
	}
	if !isAlive(st.PID) {
		t.Fatalf("Status-reported PID %d is not alive", st.PID)
	}
	if st.Restarts != 0 {
		t.Fatalf("expected Restarts reset to 0 by Restart, got %d", st.Restarts)
	}

	states := statesOf(nc.snapshot(), "restartinflight1")
	if countState(states, "failed") != 0 {
		t.Fatalf("unexpected failed notice — the stale in-flight spawn wrongly committed failTerminal: %v", states)
	}
	if !containsInOrder(states, []string{"exited", "restarting", "stopped", "starting", "ready"}) {
		t.Fatalf("expected exited->restarting->stopped->starting->ready subsequence, got %v", states)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(data)))
	aliveCount := 0
	for _, l := range lines {
		pid, err := strconv.Atoi(l)
		if err != nil {
			continue
		}
		if isAlive(pid) {
			aliveCount++
		}
	}
	if aliveCount != 1 {
		t.Fatalf("expected exactly one live process across all spawned generations, got %d alive (pids=%v, status pid=%d)",
			aliveCount, lines, st.PID)
	}
}

// Restart row 6 (interleave): Stop and Restart called concurrently on the
// same child must both return (no deadlock) and converge to one of exactly
// two terminal outcomes — stopped, or a clean restarted ready — never a
// leaked process under a state that claims otherwise.
func TestSupervisor_StopRacesRestart(t *testing.T) {
	var nc noticeCollector
	s := New(nc.notify)
	spec, _ := stubSpec(t, "stoprestartrace1", true, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.StopAll()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = s.Stop("stoprestartrace1")
	}()
	go func() {
		defer wg.Done()
		_ = s.Restart(ctx, "stoprestartrace1")
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("Stop and Restart did not both return — possible deadlock")
	}

	st, ok := statusOf(s.Status(), "stoprestartrace1")
	if !ok {
		t.Fatal("expected a status entry for stoprestartrace1")
	}
	switch st.State {
	case "stopped":
		if st.PID > 0 && !waitFor(3*time.Second, func() bool { return !isAlive(st.PID) }) {
			t.Fatalf("state stopped but process %d still alive (leak)", st.PID)
		}
	case "ready":
		if st.PID <= 0 || !isAlive(st.PID) {
			t.Fatalf("state ready but no live process (pid=%d)", st.PID)
		}
	default:
		t.Fatalf("unexpected terminal state %q after Stop-races-Restart (want stopped or ready)", st.State)
	}
}

// Row 6: AllocatePorts returns n distinct, immediately bindable ports.
func TestAllocatePorts(t *testing.T) {
	ports, err := AllocatePorts(5)
	if err != nil {
		t.Fatalf("AllocatePorts: %v", err)
	}
	if len(ports) != 5 {
		t.Fatalf("expected 5 ports, got %d", len(ports))
	}
	seen := make(map[int]bool, len(ports))
	for _, p := range ports {
		if seen[p] {
			t.Fatalf("duplicate port %d", p)
		}
		seen[p] = true
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			t.Fatalf("port %d not bindable: %v", p, err)
		}
		l.Close()
	}
}
