// main_test.go — unit tests for main's extracted, testable helpers.
// main() itself carries no logic beyond flag parsing
// and wiring (see the package doc comment); these helpers are the pieces of
// that wiring worth asserting on directly rather than only through the live
// kit-e2e/trio gates.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/bootstrap"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/kitd"
	"github.com/SmartHealthNetwork/shn-kit/relay"
)

func TestResolveTokenStoreKind(t *testing.T) {
	cases := []struct {
		name       string
		explicit   bool
		value      string
		javaAssets string
		want       string
	}{
		{"explicit wins over java-assets set", true, "file", "/opt/trio", "file"},
		{"explicit wins with no java-assets", true, "keychain", "", "keychain"},
		{"packaged default: java-assets set, not explicit", false, "", "/opt/trio", "keychain"},
		{"dev default: no java-assets, not explicit", false, "", "", "file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveTokenStoreKind(tc.explicit, tc.value, tc.javaAssets)
			if got != tc.want {
				t.Errorf("resolveTokenStoreKind(%v, %q, %q) = %q, want %q", tc.explicit, tc.value, tc.javaAssets, got, tc.want)
			}
		})
	}
}

func TestNewTokenStore(t *testing.T) {
	fileStore := bootstrap.NewFileTokenStore(filepath.Join(t.TempDir(), "tokens.json"), "https://accounts.example.org")

	if got := newTokenStore("file", fileStore, "https://accounts.example.org"); got != fileStore {
		t.Error(`newTokenStore("file", ...) did not return the injected file store unchanged`)
	}
	// An unknown/typo'd kind fails safe to the file store rather than
	// silently doing nothing or panicking.
	if got := newTokenStore("bogus", fileStore, "https://accounts.example.org"); got != fileStore {
		t.Error(`newTokenStore("bogus", ...) did not fail safe to the file store`)
	}

	ks := newTokenStore("keychain", fileStore, "https://accounts.example.org")
	if ks == fileStore {
		t.Error(`newTokenStore("keychain", ...) returned the file store unchanged, want a keyring-backed wrapper`)
	}
	if _, ok := ks.(interface{ Detail() string }); !ok {
		t.Error(`newTokenStore("keychain", ...) does not implement Detail() string`)
	}
}

// TestValidateSecretsAccounts asserts the hard startup error: without it,
// --secrets pointing at a dir with no loadable bundle AND --accounts empty
// silently degrades into a Kit that can never sign in (no persisted bundle
// to resume from, no accounts URL to sign in fresh against).
func TestValidateSecretsAccounts(t *testing.T) {
	dir := t.TempDir()

	// --secrets unset (""): nothing to validate here — the caller's own
	// separate "--accounts required unless --secrets is set" check handles
	// that case.
	if err := validateSecretsAccounts("", ""); err != nil {
		t.Errorf(`validateSecretsAccounts("", "") = %v, want nil`, err)
	}

	// --secrets set, no loadable bundle there, --accounts empty: hard error
	// naming BOTH facts.
	err := validateSecretsAccounts(dir, "")
	if err == nil {
		t.Fatal("validateSecretsAccounts(unloadable dir, \"\") = nil, want an error naming both facts")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("validateSecretsAccounts error = %q, want it to name the secrets dir %q", err.Error(), dir)
	}
	if !strings.Contains(err.Error(), "--accounts") {
		t.Errorf("validateSecretsAccounts error = %q, want it to name --accounts", err.Error())
	}

	// --secrets set, no loadable bundle there, but --accounts IS set: fine —
	// the Kit can still sign in fresh (the pre-existing fresh-install path).
	if err := validateSecretsAccounts(dir, "https://accounts.example.org"); err != nil {
		t.Errorf("validateSecretsAccounts(unloadable dir, accountsURL set) = %v, want nil", err)
	}

	// --secrets points at an ACTUALLY loadable bundle: fine regardless of
	// --accounts (the existing pre-provisioned-bundle fast path).
	loadable := filepath.Join(t.TempDir(), "secrets")
	ident, err := shnsdk.GenerateIdentity("test-holder")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := shnsdk.WriteBundle(loadable, ident, "provider", "https://example.org/kit-originator"); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	if err := validateSecretsAccounts(loadable, ""); err != nil {
		t.Errorf("validateSecretsAccounts(loadable bundle, \"\") = %v, want nil", err)
	}
}

// demoRestartRecorder is envRestarter's test double: records each call's env
// and preSpawn hook, optionally invokes the hook (as the real supervisor
// does), and can block mid-call so a test can pin serialization.
type demoRestartRecorder struct {
	mu       sync.Mutex
	names    []string
	envs     [][]string
	preSpawn []func()
	hookRan  int
	err      error
	errs     []error // per-call errors consumed in order; once drained, err applies

	entered chan struct{} // signalled (non-blocking) on every entry, when non-nil
	release chan struct{} // waited on before returning, when non-nil
}

func (r *demoRestartRecorder) restart(_ context.Context, name string, env []string, preSpawn func()) error {
	r.mu.Lock()
	r.names = append(r.names, name)
	r.envs = append(r.envs, env)
	r.preSpawn = append(r.preSpawn, preSpawn)
	entered, release, err := r.entered, r.release, r.err
	if len(r.errs) > 0 {
		err = r.errs[0]
		r.errs = r.errs[1:]
	}
	r.mu.Unlock()

	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if preSpawn != nil {
		preSpawn()
		r.mu.Lock()
		r.hookRan++
		r.mu.Unlock()
	}
	if release != nil {
		<-release
	}
	return err
}

func (r *demoRestartRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.names)
}

// demoFixture wires a newBridgingDemo closure over a recorder, a live bus,
// and a baseline gateway env.
func demoFixture(t *testing.T, rec *demoRestartRecorder, base []string, rly *relay.Relay) (func(context.Context, bool) error, *event.Bus) {
	t.Helper()
	bus := event.NewBus(func() time.Time { return time.Unix(0, 0).UTC() })
	var rlyPtr atomic.Pointer[relay.Relay]
	if rly != nil {
		rlyPtr.Store(rly)
	}
	var gwEnvPtr atomic.Pointer[[]string]
	if base != nil {
		gwEnvPtr.Store(&base)
	}
	return newBridgingDemo(rec.restart, bus, &rlyPtr, &gwEnvPtr), bus
}

// TestNewBridgingDemo_EnvAndHook pins the toggle's whole contract in one
// pass: enabling appends exactly the FIXED "2.0" knob (no picker) to a
// CLONE of the baseline, disabling restarts with the bare baseline, the
// gateway child is the target, the relay's ResetCursor is what rides the
// preSpawn hook, and each successful toggle emits its child-typed bus event.
func TestNewBridgingDemo_EnvAndHook(t *testing.T) {
	// SPARE CAPACITY IS LOAD-BEARING (verified by mutation): an
	// append-onto-the-baseline bug only writes through when the baseline has
	// room, and a len==cap fixture would let the bug allocate a fresh array
	// and pass. kitd.Stack.GatewayEnv is itself built with append, so spare
	// capacity is the REAL shape this closure receives.
	base := append(make([]string, 0, 8), "ROLE=provider", "PORT=9999")
	rec := &demoRestartRecorder{}
	rly := relay.New("http://127.0.0.1:1/events", "http://127.0.0.1:1/health", event.NewBus(time.Now), log.Printf)
	toggle, bus := demoFixture(t, rec, base, rly)

	if err := toggle(context.Background(), true); err != nil {
		t.Fatalf("toggle(true): %v", err)
	}
	if rec.names[0] != gatewayChild {
		t.Fatalf("restart target = %q, want %q", rec.names[0], gatewayChild)
	}
	gotEnv := rec.envs[0]
	wantEnabled := append(append([]string{}, base...), "SHN_DEMO_EGRESS_NATIVE_LINES=2.0")
	if strings.Join(gotEnv, "\x00") != strings.Join(wantEnabled, "\x00") {
		t.Fatalf("enabled env = %v, want %v", gotEnv, wantEnabled)
	}
	// CLONE-before-append, checked on the BACKING ARRAY: comparing contents
	// alone passes even when the append aliases the baseline, because the
	// append usually lands in fresh capacity. Only pointer identity catches
	// a toggle that could rewrite the Stack's own baseline in place.
	if &gotEnv[0] == &base[0] {
		t.Fatal("the enabled env shares its backing array with the baseline — an append could rewrite the Stack's env in place")
	}
	if len(base) != 2 {
		t.Fatalf("the baseline itself was mutated: %v", base)
	}

	// preSpawn IS relay.ResetCursor — the reset must ride the hook, never a
	// call after the restart returns (the stale-gen wedge).
	if rec.preSpawn[0] == nil {
		t.Fatal("preSpawn was nil with a relay published — the cursor reset would never happen")
	}
	if got, want := reflect.ValueOf(rec.preSpawn[0]).Pointer(), reflect.ValueOf(rly.ResetCursor).Pointer(); got != want {
		t.Fatalf("preSpawn is not relay.ResetCursor (got %v, want %v)", got, want)
	}
	if rec.hookRan != 1 {
		t.Fatalf("hook ran %d times, want 1", rec.hookRan)
	}

	if err := toggle(context.Background(), false); err != nil {
		t.Fatalf("toggle(false): %v", err)
	}
	if strings.Join(rec.envs[1], "\x00") != strings.Join(base, "\x00") {
		t.Fatalf("disabled env = %v, want the bare baseline %v", rec.envs[1], base)
	}

	var details []string
	for _, e := range bus.Since(0) {
		if e.Type == event.TypeChild && e.Child == gatewayChild {
			details = append(details, e.Detail)
		}
	}
	if len(details) != 2 || details[0] != "demo-mode: enabled" || details[1] != "demo-mode: disabled" {
		t.Fatalf("bus events = %v, want [demo-mode: enabled, demo-mode: disabled]", details)
	}
}

// TestNewBridgingDemo_NoRelayNoBaseline covers the two daemon-first edges:
// with no relay published yet the hook is nil (nothing to reset), and with no
// baseline env published the toggle refuses rather than restarting the
// gateway with an empty env.
func TestNewBridgingDemo_NoRelayNoBaseline(t *testing.T) {
	rec := &demoRestartRecorder{}
	toggle, _ := demoFixture(t, rec, []string{"ROLE=provider"}, nil)
	if err := toggle(context.Background(), true); err != nil {
		t.Fatalf("toggle with no relay: %v", err)
	}
	if rec.preSpawn[0] != nil {
		t.Fatal("preSpawn non-nil with no relay published")
	}

	rec2 := &demoRestartRecorder{}
	toggleNoEnv, _ := demoFixture(t, rec2, nil, nil)
	err := toggleNoEnv(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "has not been built") {
		t.Fatalf("toggle before BuildStack = %v, want a refusal naming the missing stack", err)
	}
	if rec2.calls() != 0 {
		t.Fatalf("restart called %d times before the stack existed, want 0", rec2.calls())
	}
}

// TestNewBridgingDemo_FailedToggleEmitsNothing proves a failed restart is not
// reported as a state change: no bus event, and the error reaches the caller
// (kitd then leaves its recorded demoMode untouched).
func TestNewBridgingDemo_FailedToggleEmitsNothing(t *testing.T) {
	rec := &demoRestartRecorder{err: fmt.Errorf("supervisor: gateway not ready within 30s")}
	toggle, bus := demoFixture(t, rec, []string{"ROLE=provider"}, nil)

	if err := toggle(context.Background(), true); err == nil {
		t.Fatal("toggle(true) with a failing restart returned nil")
	}
	for _, e := range bus.Since(0) {
		if strings.Contains(e.Detail, "demo-mode") {
			t.Fatalf("a FAILED toggle emitted %q — the bus would claim a mode the gateway isn't running", e.Detail)
		}
	}
}

// TestNewBridgingDemo_FailedToggleRevertsEnv pins the "toggle reverts"
// failure mode: a failed restart runs ONE more restart arc re-registering the
// env of the last successful toggle, so the child never sits with an env the
// recorded demoMode denies (a crash-respawn would otherwise come back in demo
// mode against a status that says otherwise). Three sub-cases: revert from a
// never-toggled baseline, revert to a PRIOR ENABLED env, and a revert that
// itself fails (error-joined, both messages visible).
func TestNewBridgingDemo_FailedToggleRevertsEnv(t *testing.T) {
	base := []string{"ROLE=provider"}
	enabledEnv := append(append([]string{}, base...), "SHN_DEMO_EGRESS_NATIVE_LINES=2.0")

	t.Run("revert to baseline before any successful toggle", func(t *testing.T) {
		failErr := fmt.Errorf("supervisor: gateway not ready within 30s")
		rec := &demoRestartRecorder{errs: []error{failErr, nil}}
		toggle, bus := demoFixture(t, rec, base, nil)

		err := toggle(context.Background(), true)
		if err == nil || !errors.Is(err, failErr) {
			t.Fatalf("toggle(true) = %v, want the restart failure", err)
		}
		if rec.calls() != 2 {
			t.Fatalf("restart called %d times, want 2 (failed enable + revert)", rec.calls())
		}
		if strings.Join(rec.envs[0], "\x00") != strings.Join(enabledEnv, "\x00") {
			t.Fatalf("first restart env = %v, want the enabled env %v", rec.envs[0], enabledEnv)
		}
		if strings.Join(rec.envs[1], "\x00") != strings.Join(base, "\x00") {
			t.Fatalf("revert env = %v, want the bare baseline %v", rec.envs[1], base)
		}
		for _, e := range bus.Since(0) {
			if strings.Contains(e.Detail, "demo-mode") {
				t.Fatalf("a FAILED toggle emitted %q", e.Detail)
			}
		}
	})

	t.Run("revert to the prior enabled env", func(t *testing.T) {
		failErr := fmt.Errorf("supervisor: gateway not ready within 30s")
		rec := &demoRestartRecorder{errs: []error{nil, failErr, nil}}
		toggle, _ := demoFixture(t, rec, base, nil)

		if err := toggle(context.Background(), true); err != nil {
			t.Fatalf("toggle(true): %v", err)
		}
		if err := toggle(context.Background(), false); err == nil {
			t.Fatal("toggle(false) with a failing restart returned nil")
		}
		if rec.calls() != 3 {
			t.Fatalf("restart called %d times, want 3 (enable + failed disable + revert)", rec.calls())
		}
		if strings.Join(rec.envs[2], "\x00") != strings.Join(enabledEnv, "\x00") {
			t.Fatalf("revert env = %v, want the prior ENABLED env %v", rec.envs[2], enabledEnv)
		}
	})

	t.Run("revert failure is error-joined", func(t *testing.T) {
		failErr := fmt.Errorf("supervisor: gateway not ready within 30s")
		revErr := fmt.Errorf("supervisor: spawn: fork/exec failed")
		rec := &demoRestartRecorder{errs: []error{failErr, revErr}}
		toggle, _ := demoFixture(t, rec, base, nil)

		err := toggle(context.Background(), true)
		if err == nil || !errors.Is(err, failErr) || !errors.Is(err, revErr) {
			t.Fatalf("toggle(true) = %v, want BOTH the toggle failure and the revert failure joined", err)
		}
		if rec.calls() != 2 {
			t.Fatalf("restart called %d times, want 2", rec.calls())
		}
	})
}

// TestNewBridgingDemo_Serialized proves the closure serializes itself: the
// handler's in-flight gate is a plain atomic read, so two simultaneous POSTs
// both clear it — without this mutex their stop/respawn arcs could interleave
// and leave the running env disagreeing with the recorded state.
//
// The negative assertion is bounded-wait: a scheduler slow enough to delay
// the second call past the window makes this test pass, never flake red.
func TestNewBridgingDemo_Serialized(t *testing.T) {
	rec := &demoRestartRecorder{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	toggle, _ := demoFixture(t, rec, []string{"ROLE=provider"}, nil)

	firstDone := make(chan error, 1)
	go func() { firstDone <- toggle(context.Background(), true) }()
	select {
	case <-rec.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first toggle never reached the restart seam")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- toggle(context.Background(), false) }()
	select {
	case <-rec.entered:
		t.Fatal("a second toggle entered the restart seam while the first was still in flight — toggles are not serialized")
	case <-time.After(250 * time.Millisecond):
	}

	close(rec.release)
	for i, ch := range []chan error{firstDone, secondDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("toggle %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("toggle %d never returned", i)
		}
	}
	if rec.calls() != 2 {
		t.Fatalf("restart calls = %d, want both toggles applied, one after the other", rec.calls())
	}
}

// TestGatewayChildMatchesBuildStack pins main's mirrored child name against
// the one kitd.BuildStack actually registers: gatewayChild is a hand-copy of
// kitd's unexported gatewayChildName, so nothing but this assertion catches a
// rename that would silently turn the demo toggle (and the relay's crash
// fence) into a no-op against an unknown child.
func TestGatewayChildMatchesBuildStack(t *testing.T) {
	stack, err := kitd.BuildStack(kitd.StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
	})
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if stack.Children[0].Name != gatewayChild {
		t.Fatalf("BuildStack's gateway child is %q, but main mirrors it as %q", stack.Children[0].Name, gatewayChild)
	}
}
