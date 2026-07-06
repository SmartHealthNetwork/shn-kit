// Package supervisor runs OS child processes with HTTP ready probes, bounded
// restarts (linear backoff), and per-child append logs. It will supervise
// the real gateway binary (and later the Kit's
// Java children) as spawned children of shnkitd.
//
// Log file lifecycle: LogPath is opened once (append|create, 0644) in Start
// and that single *os.File handle is reused as Stdout/Stderr across every
// respawn of that child — simplest correct choice, since exec.Cmd only needs
// the handle open while a process runs and reopening per-spawn would race
// the monitor goroutine's log writes against a fresh os.OpenFile. The handle
// is closed only once the child reaches a terminal state (failed or
// stopped) that will never spawn again.
package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// ChildSpec describes one supervised child process.
type ChildSpec struct {
	Name         string
	Command      string // absolute path to the binary
	Args         []string
	Env          []string // the child's FULL env (never inherited)
	Dir          string
	LogPath      string        // per-child append log (support-bundle seam)
	ReadyURLs    []string      // ALL must answer 2xx before the child counts as ready
	ReadyTimeout time.Duration // spawn→ready deadline; exceeded ⇒ kill + error
	RestartMax   int           // bounded restarts after unexpected exit
}

// Notice.State / ChildStatus.State values. Every child whose spawn was at
// least attempted has a notice stream beginning with StateStarting;
// StateFailed and StateStopped are terminal.
const (
	StateStarting   = "starting"
	StateReady      = "ready"
	StateExited     = "exited"
	StateRestarting = "restarting"
	StateFailed     = "failed"
	StateStopped    = "stopped"
)

// Notice is an async state-transition event for one child.
// State is one of the State* constants above.
type Notice struct{ Child, State, Detail string }

// ChildStatus is a point-in-time snapshot of one supervised child.
type ChildStatus struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
	PID      int    `json:"pid"`
	Restarts int    `json:"restarts"`
}

// child is the supervisor's internal bookkeeping for one ChildSpec. All
// fields are guarded by Supervisor.mu except where a value has been copied
// out under the lock (e.g. a monitor goroutine's local cmd reference).
type child struct {
	spec ChildSpec
	cmd  *exec.Cmd
	// exited is closed once the CURRENT cmd generation is fully gone AND
	// its terminal notice (if any) has been recorded — see spawnAndWatch's
	// and monitor's ordering comments.
	exited     chan struct{}
	state      string
	detail     string
	restarts   int
	stopping   bool
	logF       *os.File
	generation int // bumped only by Restart; see monitor's staleness re-check
}

// Supervisor supervises a set of named OS child processes.
type Supervisor struct {
	mu       sync.Mutex
	notify   func(Notice)
	children map[string]*child
	hc       *http.Client
}

// New constructs a Supervisor. notify is called for every state transition;
// it is NEVER called while s.mu is held (it wires to kit/event's
// Bus.Emit, which must not observe the supervisor's lock).
func New(notify func(Notice)) *Supervisor {
	return &Supervisor{
		notify:   notify,
		children: make(map[string]*child),
		hc:       &http.Client{Timeout: 1 * time.Second},
	}
}

func (s *Supervisor) emit(n Notice) {
	if s.notify != nil {
		s.notify(n)
	}
}

// Start spawns spec as a new supervised child and blocks until it is ready
// (all ReadyURLs answer 2xx) or ReadyTimeout/ctx expires, in which case the
// process is killed and an error returned. On success a monitor goroutine
// begins watching the child for unexpected exit.
//
// Return contract vs a concurrent Stop: the child is registered (and thus
// stoppable — e.g. StopAll during boot) BEFORE it spawns, so a Stop can land
// anywhere in Start's spawn/probe window. Start re-checks stopping before
// handing the child to a monitor (mirroring monitor's re-check-at-every-
// decision-point rule): a Stop that landed mid-startup wins —
// the child is killed, ends in state stopped, and Start returns a
// stopped-during-startup error. Start never returns nil for a child that is
// not left running and supervised.
func (s *Supervisor) Start(ctx context.Context, spec ChildSpec) error {
	s.mu.Lock()
	if _, exists := s.children[spec.Name]; exists {
		s.mu.Unlock()
		return fmt.Errorf("supervisor: child %q already started", spec.Name)
	}
	logF, err := os.OpenFile(spec.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Nothing ever ran, so no starting notice — but the failure is still
		// registered (Status shows it) and notified.
		err = fmt.Errorf("supervisor: open log for %s: %w", spec.Name, err)
		s.children[spec.Name] = &child{spec: spec, state: StateFailed, detail: err.Error()}
		s.mu.Unlock()
		s.emit(Notice{Child: spec.Name, State: StateFailed, Detail: err.Error()})
		return err
	}
	c := &child{spec: spec, logF: logF, state: StateStarting}
	s.children[spec.Name] = c
	s.mu.Unlock()

	// starting precedes the spawn attempt so every child that got this far
	// has a stream beginning with starting, even if the spawn itself fails.
	s.emit(Notice{Child: spec.Name, State: StateStarting})

	return s.spawnAndWatch(ctx, c, 0) // fresh child: generation starts at 0
}

// spawnAndWatch is the shared spawn+waitReady+monitor arc used by Start,
// Restart, and the crash-driven bounce inside monitor — one path, so the
// three callers can never copy-paste-diverge. gen is the
// generation this spawn belongs to (captured by the launched monitor, which
// re-checks it before ever respawning — see monitor's doc comment).
//
// On success the child is left in state ready (notice emitted) with a
// monitor goroutine running for gen. On failure the process is killed and
// the child is left terminal — stopped if a concurrent Stop landed in the
// spawn/probe window (mirroring Start's original stopping-wins-mid-startup
// contract), failed otherwise — with the corresponding
// notice already emitted; the returned error names the reason.
//
// Generation fence (CRITICAL): every commit point below
// (failed / stopped / ready) re-checks c.generation == gen under s.mu
// immediately before mutating state — mirroring monitor's existing
// stopping/stale re-check. A concurrent Restart can supersede an in-flight
// spawnAndWatch at any point; a stale attempt must exit quietly (no state
// mutation, no closeLog, no notice) once superseded — otherwise it can
// clobber the fresh generation's state or log handle out from under it.
// This generation's exited channel is captured locally from spawn (never
// re-read from c.exited, which a newer generation's own spawn call may
// have since overwritten) and is closed ONLY AFTER the local commit
// decision is made and any notice for it emitted — never before closing.
// Closing it any earlier would let a concurrent Restart's Stop() call
// unblock and race ahead (clear stopping, bump generation, spawn its own
// fresh generation) before this attempt's own stopping/stale re-check
// runs, so the re-check could observe stopping already cleared and wrongly
// commit failTerminal over a healthy fresh generation — the exact defect
// this fix closes.
func (s *Supervisor) spawnAndWatch(ctx context.Context, c *child, gen int) error {
	cmd, exited, err := s.spawn(c)
	if err != nil {
		if s.staleGeneration(c, gen) {
			// A fresh Restart already claimed this child before our spawn
			// even produced a process — nothing to kill, nothing to
			// report; the fresh generation owns the child now.
			return err
		}
		s.failTerminal(c, err.Error())
		return err
	}

	if err := s.waitReady(ctx, c); err != nil {
		s.killProcess(cmd)
		s.mu.Lock()
		stale := c.generation != gen
		stopping := c.stopping
		s.mu.Unlock()
		switch {
		case stale:
			close(exited)
			return err
		case stopping:
			// The probe failed because (or while) a concurrent Stop killed
			// the child mid-startup — that Stop wins: stopped, not failed.
			s.stopTerminal(c)
			close(exited)
			return fmt.Errorf("supervisor: %s stopped during startup", c.spec.Name)
		default:
			s.failTerminal(c, err.Error())
			close(exited)
			return err
		}
	}

	// Re-check stopping/stale before handing the child to a monitor: a Stop
	// that landed pre-spawn (cmd was nil, Stop returned with nothing to
	// signal) or during a probe that still passed must not leave an
	// unsupervised running child behind; a Restart that has
	// since claimed this child must not have its fresh generation clobbered
	// by our now-orphaned success.
	s.mu.Lock()
	stopping := c.stopping
	stale := c.generation != gen
	if !stopping && !stale {
		c.state = StateReady
		c.detail = ""
	}
	s.mu.Unlock()
	switch {
	case stale:
		s.killProcess(cmd)
		close(exited)
		return fmt.Errorf("supervisor: %s spawn superseded by a newer generation", c.spec.Name)
	case stopping:
		s.killProcess(cmd)
		s.stopTerminal(c)
		close(exited)
		return fmt.Errorf("supervisor: %s stopped during startup", c.spec.Name)
	}
	s.emit(Notice{Child: c.spec.Name, State: StateReady})

	go s.monitor(c, cmd, gen)
	return nil
}

// staleGeneration reports whether c has moved on from gen (a concurrent
// Restart has bumped c.generation) — read under s.mu. Once true it is
// stable: Restart only ever increments generation, never decrements or
// resets it.
func (s *Supervisor) staleGeneration(c *child, gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return c.generation != gen
}

// spawn starts a new OS process for c.spec, reusing c's already-open log
// file for Stdout/Stderr, and records it as c's current generation. The
// returned exited channel is this spawn's own reference: callers must close
// it directly rather than re-reading c.exited, which a newer generation's
// own spawn call may have since overwritten (CRITICAL).
func (s *Supervisor) spawn(c *child) (*exec.Cmd, chan struct{}, error) {
	cmd := exec.Command(c.spec.Command, c.spec.Args...)
	cmd.Env = c.spec.Env
	cmd.Dir = c.spec.Dir

	s.mu.Lock()
	cmd.Stdout = c.logF
	cmd.Stderr = c.logF
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("supervisor: start %s: %w", c.spec.Name, err)
	}

	exited := make(chan struct{})
	s.mu.Lock()
	c.cmd = cmd
	c.exited = exited
	s.mu.Unlock()
	return cmd, exited, nil
}

// waitReady polls spec.ReadyURLs every 100ms (1s per-request timeout via
// s.hc) until all answer 2xx in a single pass, spec.ReadyTimeout elapses,
// or ctx is done.
func (s *Supervisor) waitReady(ctx context.Context, c *child) error {
	deadline := time.Now().Add(c.spec.ReadyTimeout)
	for {
		failing, ready := notReadyURL(s.hc, c.spec.ReadyURLs)
		if ready {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("supervisor: %s not ready within %s (%s)",
				c.spec.Name, c.spec.ReadyTimeout, failing)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("supervisor: %s not ready: %w", c.spec.Name, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// notReadyURL probes urls in order, returning the first that does not
// answer 2xx (ready=false), or ready=true if all do. One pass, so a
// not-ready result always names a URL that actually failed this round.
func notReadyURL(hc *http.Client, urls []string) (string, bool) {
	for _, u := range urls {
		if !get2xx(hc, u) {
			return u, false
		}
	}
	return "", true
}

func get2xx(hc *http.Client, url string) bool {
	resp, err := hc.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// monitor owns one cmd generation: it blocks on cmd.Wait(), then either
// records a deliberate stop, or handles an unexpected exit by restarting
// (bounded, linear backoff) or failing terminally once RestartMax is
// exceeded. Each restart re-probes readiness against the SAME spec —
// env and ports are fixed, so a live restart proves the child
// came back on the same address.
//
// A deliberate Stop can land at ANY point in the exited→restarting→respawn
// window, so c.stopping is re-checked at every decision point (post-Wait,
// pre-cap, post-backoff, post-respawn-failure, post-respawn-success):
// "Stop ⇒ no restart" must hold in every interleaving, not just the happy
// path (CRITICAL).
//
// gen is the generation this monitor was launched to watch (captured by its
// caller at spawn time). A deliberate Restart bumps c.generation — a
// no-double-spawn invariant that is FIELD-BACKED: this
// monitor re-checks c.generation == gen under s.mu at the same decision
// points as stopping (pre-cap, post-backoff, immediately before respawning).
// A stale monitor (gen no longer current) exits without respawning and
// without touching any further state — the fresh Restart-driven
// spawnAndWatch call already owns the child's fate, so the stale monitor
// must not clobber it in either order the race resolves.
func (s *Supervisor) monitor(c *child, cmd *exec.Cmd, gen int) {
	waitErr := cmd.Wait()

	s.mu.Lock()
	exited := c.exited
	stopping := c.stopping
	if stopping {
		c.state = StateStopped
		c.detail = ""
		closeLog(c)
	} else {
		c.state = StateExited
		c.detail = exitDetail(waitErr)
	}
	detail := c.detail
	s.mu.Unlock()
	if stopping {
		// Emit BEFORE closing exited (IMPORTANT): Restart's
		// Stop() call synchronizes on exited closing, so Restart's
		// continuation (reopen log, emit starting) must not be able to run
		// before this stopped notice is recorded — otherwise the
		// stopped -> starting -> ready ordering Restart's doc comment
		// promises isn't actually guaranteed, just usually true.
		s.emit(Notice{Child: c.spec.Name, State: StateStopped})
		close(exited)
		return
	}
	s.emit(Notice{Child: c.spec.Name, State: StateExited, Detail: detail})
	close(exited)

	s.mu.Lock()
	stopping = c.stopping
	stale := c.generation != gen
	capped := c.restarts >= c.spec.RestartMax
	var restarts int
	var restartDetail string
	if !stopping && !stale && !capped {
		c.restarts++
		restarts = c.restarts
		c.state = StateRestarting
		c.detail = fmt.Sprintf("restart %d/%d", restarts, c.spec.RestartMax)
		restartDetail = c.detail
	}
	s.mu.Unlock()
	if stopping {
		s.stopTerminal(c)
		return
	}
	if stale {
		// A deliberate Restart has since claimed this child — its own
		// spawnAndWatch arc owns the child now. Exit quietly: touching
		// state/notices here would race the fresh generation's.
		return
	}
	if capped {
		s.failTerminal(c, fmt.Sprintf("restart cap (%d) reached", c.spec.RestartMax))
		return
	}
	s.emit(Notice{Child: c.spec.Name, State: StateRestarting, Detail: restartDetail})

	time.Sleep(500 * time.Millisecond * time.Duration(restarts))

	// A Stop OR a deliberate Restart may have landed during the backoff —
	// honor either instead of respawning. (No exited channel to close here:
	// this generation's was already closed above and no new one exists
	// yet.)
	s.mu.Lock()
	stopping = c.stopping
	stale = c.generation != gen
	s.mu.Unlock()
	if stopping {
		s.stopTerminal(c)
		return
	}
	if stale {
		return
	}

	// spawnAndWatch owns every remaining transition (ready-probe failure ⇒
	// stopped-or-failed with notice already emitted; success ⇒ ready notice
	// + a fresh monitor for gen) — the same shared arc Start and Restart
	// use, so there is nothing left for this crash bounce to do itself.
	_ = s.spawnAndWatch(context.Background(), c, gen)
}

// failTerminal marks c failed with detail, closes its log file, and emits
// the failed notice. Caller must not hold s.mu.
func (s *Supervisor) failTerminal(c *child, detail string) {
	s.mu.Lock()
	c.state = StateFailed
	c.detail = detail
	closeLog(c)
	s.mu.Unlock()
	s.emit(Notice{Child: c.spec.Name, State: StateFailed, Detail: detail})
}

// stopTerminal marks c stopped, closes its log file, and emits the stopped
// notice. Caller must not hold s.mu.
func (s *Supervisor) stopTerminal(c *child) {
	s.mu.Lock()
	c.state = StateStopped
	c.detail = ""
	closeLog(c)
	s.mu.Unlock()
	s.emit(Notice{Child: c.spec.Name, State: StateStopped})
}

// closeLog closes and clears c.logF. Caller must hold s.mu.
func closeLog(c *child) {
	if c.logF != nil {
		_ = c.logF.Close()
		c.logF = nil
	}
}

func exitDetail(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// killProcess force-kills and reaps a generation that has no monitor
// goroutine (Process.Wait, never cmd.Wait — cmd.Wait is reserved for the
// one monitor per generation, per os/exec's single-Wait contract).
func (s *Supervisor) killProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// Stop deliberately stops a child: no restart follows, wherever the Stop
// lands — including mid-restart or mid-Start (both re-check stopping at
// every decision point). It signals os.Interrupt (straight Kill on
// Windows), waits up to 3s for the current generation to exit, then
// force-kills.
//
// Latency bound: normally ≤3s grace + reap. When Stop races an in-flight
// ready probe (a probing Start, or a monitor respawn), the generation's
// exited channel is closed by the prober's failure path, so Stop can block
// for up to 3s + the REMAINDER of that probe's ReadyTimeout before
// returning. Stop is idempotent on an already-terminal child.
//
// One further edge: when Stop lands during a monitor respawn, it can grab
// the OLD generation's already-closed exited channel and return while the
// monitor is still killing the fresh process — "Stop ⇒ no restart" holds in
// every interleaving, but "child fully gone when Stop returns" does not in
// that one window (StopAll-then-process-exit edge).
func (s *Supervisor) Stop(name string) error {
	s.mu.Lock()
	c, ok := s.children[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("supervisor: unknown child %q", name)
	}
	if c.state == StateStopped || c.state == StateFailed {
		c.stopping = true
		s.mu.Unlock()
		return nil
	}
	c.stopping = true
	cmd := c.cmd
	exited := c.exited
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if runtime.GOOS == "windows" {
		_ = cmd.Process.Kill()
	} else {
		_ = cmd.Process.Signal(os.Interrupt)
	}

	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
	return nil
}

// Restart deliberately stops the named child (if running) and respawns its
// registered spec with a reset restart budget. Blocks until the respawned
// child passes its ready probes or fails (same contract as Start).
// Unknown name => error. No-double-spawn is a FIELD-BACKED invariant:
// child gains `generation int`; every monitor captures the
// generation at its spawn and re-checks it under s.mu immediately before
// respawning — a stale monitor observing a newer generation exits instead.
// Restart itself first drives the EXISTING hardened Stop(name) path to full
// quiescence (SIGTERM→grace→SIGKILL, blocks on exited, the old monitor sees
// stopping and goes terminal), THEN under s.mu clears stopping, bumps
// generation, resets restarts to 0, and re-runs the shared spawn arc.
func (s *Supervisor) Restart(ctx context.Context, name string) error {
	s.mu.Lock()
	c, ok := s.children[name]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("supervisor: unknown child %q", name)
	}

	if err := s.Stop(name); err != nil {
		return err
	}

	// The log file's *os.File handle was closed by the old monitor's
	// terminal transition (closeLog, same as any deliberate Stop) — reopen
	// the SAME path in append mode so both boots' output lands in one file
	// (package doc comment's log-lifecycle contract, extended across a
	// Restart rather than assuming Stop is always final).
	logF, err := os.OpenFile(c.spec.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		err = fmt.Errorf("supervisor: open log for %s: %w", name, err)
		s.mu.Lock()
		c.state = StateFailed
		c.detail = err.Error()
		// Stop (above) set stopping=true; clear it here too (MINOR) —
		// this Restart no longer intends the child stopped, it's
		// terminally failed instead, and leaving stopping=true dangling is
		// an inconsistent field even though nothing reads it once terminal.
		c.stopping = false
		s.mu.Unlock()
		s.emit(Notice{Child: name, State: StateFailed, Detail: err.Error()})
		return err
	}

	s.mu.Lock()
	c.logF = logF
	c.stopping = false
	c.generation++
	gen := c.generation
	c.restarts = 0
	c.state = StateStarting
	s.mu.Unlock()

	// stopped -> starting -> ready: a deliberate Restart never emits
	// StateRestarting. StateRestarting is reserved for the crash-driven
	// bounce inside monitor, which the SSE relay's gateway ResetCursor hook
	// keys on to detect an unexpected crash — a deliberate operator Restart
	// must not trip that crash-fence, so it stays out of this path entirely.
	s.emit(Notice{Child: name, State: StateStarting})

	return s.spawnAndWatch(ctx, c, gen)
}

// StopAll stops every supervised child, in no particular order.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	names := make([]string, 0, len(s.children))
	for n := range s.children {
		names = append(names, n)
	}
	s.mu.Unlock()
	for _, n := range names {
		_ = s.Stop(n)
	}
}

// Status returns a point-in-time snapshot of every supervised child.
func (s *Supervisor) Status() []ChildStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ChildStatus, 0, len(s.children))
	for _, c := range s.children {
		pid := 0
		if c.cmd != nil && c.cmd.Process != nil {
			pid = c.cmd.Process.Pid
		}
		out = append(out, ChildStatus{
			Name:     c.spec.Name,
			State:    c.state,
			Detail:   c.detail,
			PID:      pid,
			Restarts: c.restarts,
		})
	}
	return out
}
