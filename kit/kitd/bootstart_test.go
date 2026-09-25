package kitd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

// TestBootstartHelperChild is not a test: it is the stub child StartStack's
// rows start, run as this test binary with KITD_BOOTSTART_HELPER=1. It serves
// GET /ready on BOOT_ADDR, and exits 1 with the bind error when the address
// is taken, as the gateway does.
func TestBootstartHelperChild(t *testing.T) {
	if os.Getenv("KITD_BOOTSTART_HELPER") != "1" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if err := http.ListenAndServe(os.Getenv("BOOT_ADDR"), mux); err != nil {
		fmt.Println("stub:", err)
		os.Exit(1)
	}
}

// bootChild is a ChildSpec for the stub child on addr.
func bootChild(t *testing.T, name, addr string) supervisor.ChildSpec {
	t.Helper()
	return supervisor.ChildSpec{
		Name:         name,
		Command:      os.Args[0],
		Args:         []string{"-test.run=TestBootstartHelperChild"},
		Env:          []string{"KITD_BOOTSTART_HELPER=1", "BOOT_ADDR=" + addr},
		LogPath:      filepath.Join(t.TempDir(), name+".log"),
		ReadyURLs:    []string{"http://" + addr + "/ready"},
		ReadyTimeout: 20 * time.Second,
	}
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) notify(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

// A child whose port another process took before it could bind it exits
// during startup; the boot builds the stack again on fresh ports and
// completes. The collision here is real: the test holds the first port.
func TestStartStack_RetriesAnEarlyExitOnFreshPorts(t *testing.T) {
	sup := supervisor.New(nil)
	t.Cleanup(sup.StopAll)
	var builds []string
	closedBy := map[int]bool{} // build number → whether its stack was closed
	var held net.Listener
	t.Cleanup(func() {
		if held != nil {
			held.Close()
		}
	})
	build := func() (Stack, error) {
		ports, err := supervisor.AllocatePorts(1)
		if err != nil {
			return Stack{}, err
		}
		addr := fmt.Sprintf("127.0.0.1:%d", ports[0])
		builds = append(builds, addr)
		n := len(builds)
		defer func() { closedBy[n] = false }()
		if len(builds) == 1 {
			if held, err = net.Listen("tcp", addr); err != nil {
				t.Fatalf("hold %s: %v", addr, err)
			}
		}
		return Stack{GatewayURL: "http://" + addr, Children: []supervisor.ChildSpec{bootChild(t, "gateway", addr)},
			closeFixtureSoR: func() error { closedBy[n] = true; return nil }}, nil
	}
	var prepared []string
	prepare := func(s Stack) error { prepared = append(prepared, s.GatewayURL); return nil }
	var log logLines

	stack, err := StartStack(context.Background(), sup, BootStartAttempts, build, prepare, log.notify)
	if err != nil {
		t.Fatalf("StartStack: %v", err)
	}
	if len(builds) != 2 || builds[0] == builds[1] {
		t.Fatalf("builds = %v, want a second build on a fresh port", builds)
	}
	if stack.GatewayURL != "http://"+builds[1] || len(prepared) != 2 || prepared[1] != stack.GatewayURL {
		t.Fatalf("stack %s, prepared %v: want the second build's stack, prepared before its children started", stack.GatewayURL, prepared)
	}
	if len(log.lines) != 1 || !strings.Contains(log.lines[0], "exited during startup (attempt 1 of 3)") || !strings.Contains(log.lines[0], "address already in use") {
		t.Fatalf("log = %q, want one retry line naming the bind error", log.lines)
	}
	if !closedBy[1] || closedBy[2] {
		t.Fatalf("closed = %v: want the discarded first build released and the kept one left open", closedBy)
	}
	resp, err := http.Get(stack.GatewayURL + "/ready")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the started child does not answer: %v", err)
	}
	resp.Body.Close()
}

// A child that exits during startup every time still fails the boot, after
// the bounded number of builds, with the child's own reason.
func TestStartStack_GivesUpAfterBoundedAttempts(t *testing.T) {
	sup := supervisor.New(nil)
	t.Cleanup(sup.StopAll)
	builds := 0
	build := func() (Stack, error) {
		ports, err := supervisor.AllocatePorts(1)
		if err != nil {
			return Stack{}, err
		}
		addr := fmt.Sprintf("127.0.0.1:%d", ports[0])
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("hold %s: %v", addr, err)
		}
		t.Cleanup(func() { l.Close() })
		builds++
		return Stack{Children: []supervisor.ChildSpec{bootChild(t, "gateway", addr)}}, nil
	}
	var log logLines
	_, err := StartStack(context.Background(), sup, BootStartAttempts, build, func(Stack) error { return nil }, log.notify)
	var cse *ChildStartError
	var early *supervisor.StartupExitError
	if !errors.As(err, &cse) || cse.Child != "gateway" || !errors.As(err, &early) || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("err = %v, want the gateway's start failure naming the bind error", err)
	}
	if builds != BootStartAttempts || len(log.lines) != BootStartAttempts-1 {
		t.Fatalf("builds = %d, retry lines = %d; want %d builds", builds, len(log.lines), BootStartAttempts)
	}
}

// fakeStarter scripts Start results by call and records Stop/Forget.
type fakeStarter struct {
	results []error
	calls   []string
	stopped []string
	forgot  []string
}

func (f *fakeStarter) Start(_ context.Context, spec supervisor.ChildSpec) error {
	f.calls = append(f.calls, spec.Name)
	if len(f.results) == 0 {
		return nil
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r
}
func (f *fakeStarter) Stop(name string) error   { f.stopped = append(f.stopped, name); return nil }
func (f *fakeStarter) Forget(name string) error { f.forgot = append(f.forgot, name); return nil }

func twoChildren() (Stack, error) {
	return Stack{Children: []supervisor.ChildSpec{{Name: "gateway"}, {Name: "validator"}}}, nil
}

// Every child the failed attempt registered is stopped and forgotten, last
// first, before the next build starts them again.
func TestStartStack_RetryClearsTheAttemptsChildren(t *testing.T) {
	f := &fakeStarter{results: []error{nil, &supervisor.StartupExitError{Child: "validator", Status: "exit status 1"}}}
	if _, err := StartStack(context.Background(), f, BootStartAttempts, twoChildren, func(Stack) error { return nil }, func(string) {}); err != nil {
		t.Fatalf("StartStack: %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "gateway,validator,gateway,validator" {
		t.Fatalf("starts = %s", got)
	}
	if got := strings.Join(f.stopped, ","); got != "validator,gateway" || strings.Join(f.forgot, ",") != got {
		t.Fatalf("stopped %v, forgot %v; want validator,gateway for both", f.stopped, f.forgot)
	}
}

// Only an early exit is retried: a child that is slow to become ready (or
// fails any other way) ends the boot on the first attempt, as before.
func TestStartStack_DoesNotRetryOtherFailures(t *testing.T) {
	timeout := errors.New("supervisor: gateway not ready within 30s (http://127.0.0.1:1/x)")
	f := &fakeStarter{results: []error{timeout}}
	builds := 0
	build := func() (Stack, error) { builds++; return twoChildren() }
	_, err := StartStack(context.Background(), f, BootStartAttempts, build, func(Stack) error { return nil }, func(string) {})
	var cse *ChildStartError
	if !errors.As(err, &cse) || cse.Child != "gateway" || !errors.Is(err, timeout) || builds != 1 || len(f.forgot) != 0 {
		t.Fatalf("err = %v, builds = %d, forgot = %v; want the timeout on the only build", err, builds, f.forgot)
	}
}

// cancellingStarter cancels the boot while a child starts, as a shutdown
// signal does, and reports the child's exit that the kill caused.
type cancellingStarter struct {
	fakeStarter
	cancel context.CancelFunc
}

func (c *cancellingStarter) Start(ctx context.Context, spec supervisor.ChildSpec) error {
	c.calls = append(c.calls, spec.Name)
	c.cancel()
	return &supervisor.StartupExitError{Child: spec.Name, Status: "signal: killed"}
}

// A boot cancelled by shutdown is not retried, whether the shutdown lands
// while a child starts or before a build.
func TestStartStack_CancelledBootIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &cancellingStarter{cancel: cancel}
	builds := 0
	build := func() (Stack, error) { builds++; return twoChildren() }
	if _, err := StartStack(ctx, c, BootStartAttempts, build, func(Stack) error { return nil }, func(string) {}); err == nil || builds != 1 || len(c.forgot) != 0 {
		t.Fatalf("err = %v, builds = %d, forgot = %v; want the failure on the only build", err, builds, c.forgot)
	}
	if _, err := StartStack(ctx, c, BootStartAttempts, build, func(Stack) error { return nil }, func(string) {}); !errors.Is(err, context.Canceled) || builds != 1 {
		t.Fatalf("an already-cancelled boot: err = %v, builds = %d; want nothing built", err, builds)
	}
}

// A build or prepare failure ends the boot before any child starts.
func TestStartStack_BuildAndPrepareFailuresEndTheBoot(t *testing.T) {
	f := &fakeStarter{}
	boom := errors.New("boom")
	if _, err := StartStack(context.Background(), f, BootStartAttempts, func() (Stack, error) { return Stack{}, boom }, func(Stack) error { return nil }, func(string) {}); !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "build stack: ") {
		t.Fatalf("build failure: %v", err)
	}
	if _, err := StartStack(context.Background(), f, BootStartAttempts, twoChildren, func(Stack) error { return boom }, func(string) {}); !errors.Is(err, boom) {
		t.Fatalf("prepare failure: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("children started despite the failure: %v", f.calls)
	}
}

// A gateway whose port the caller fixed (--gateway-port) is not retried: a
// rebuild would reuse the port it could not bind. The reason is reported.
func TestStartStack_PinnedGatewayPortIsNotRetried(t *testing.T) {
	f := &fakeStarter{results: []error{&supervisor.StartupExitError{Child: "gateway", Status: "exit status 1", LastLog: "bind: address already in use"}}}
	builds := 0
	build := func() (Stack, error) {
		builds++
		return Stack{GatewayPortPinned: true, Children: []supervisor.ChildSpec{{Name: "gateway"}}}, nil
	}
	var log logLines
	_, err := StartStack(context.Background(), f, BootStartAttempts, build, func(Stack) error { return nil }, log.notify)
	var cse *ChildStartError
	if !errors.As(err, &cse) || cse.Child != "gateway" || builds != 1 {
		t.Fatalf("err = %v, builds = %d; want the gateway's failure on the only build", err, builds)
	}
	if len(log.lines) != 1 || !strings.Contains(log.lines[0], "not retried") || !strings.Contains(log.lines[0], "--gateway-port") {
		t.Fatalf("notices = %q, want one naming why it is not retried", log.lines)
	}
}

// refusingStarter refuses to forget, as the supervisor does for a child that
// is not terminal.
type refusingStarter struct{ fakeStarter }

func (r *refusingStarter) Forget(name string) error {
	return fmt.Errorf("supervisor: cannot forget %s while it is restarting", name)
}

// When the retry cannot clear the attempt's children, the boot fails with
// the child's own reason still findable beside why.
func TestStartStack_RetryThatCannotForgetKeepsTheCause(t *testing.T) {
	r := &refusingStarter{fakeStarter{results: []error{nil, &supervisor.StartupExitError{Child: "validator", Status: "exit status 1", LastLog: "bind: address already in use"}}}}
	_, err := StartStack(context.Background(), r, BootStartAttempts, twoChildren, func(Stack) error { return nil }, func(string) {})
	var cse *ChildStartError
	if !errors.As(err, &cse) || cse.Child != "validator" || !strings.Contains(err.Error(), "cannot forget") {
		t.Fatalf("err = %v, want the validator's failure and the refusal", err)
	}
}
