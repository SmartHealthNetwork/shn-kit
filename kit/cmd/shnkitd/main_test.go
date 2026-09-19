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
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

// newTestGatewayEnvSwitch builds a gatewayEnvSwitch over a recorder, an
// optional relay, and an optional baseline env — the shared construction
// every fixture in this file builds on (demoFixture/conformanceFixture for
// a single closure in isolation, crossToggleFixture for both closures over
// the SAME switch instance).
func newTestGatewayEnvSwitch(rec *demoRestartRecorder, base []string, rly *relay.Relay) *gatewayEnvSwitch {
	var rlyPtr atomic.Pointer[relay.Relay]
	if rly != nil {
		rlyPtr.Store(rly)
	}
	var gwEnvPtr atomic.Pointer[[]string]
	if base != nil {
		gwEnvPtr.Store(&base)
	}
	return &gatewayEnvSwitch{restart: rec.restart, rlyPtr: &rlyPtr, gwEnvPtr: &gwEnvPtr, gatewayBinary: "/missing-test-child"}
}

// demoFixture wires a newBridgingDemo closure over its OWN gatewayEnvSwitch
// (a single-closure fixture — see crossToggleFixture below for the shape
// that exercises interaction between the two toggles), a live bus, and a
// baseline gateway env.
func demoFixture(t *testing.T, rec *demoRestartRecorder, base []string, rly *relay.Relay) (func(context.Context, bool) error, *event.Bus) {
	t.Helper()
	bus := event.NewBus(func() time.Time { return time.Unix(0, 0).UTC() })
	return newBridgingDemo(newTestGatewayEnvSwitch(rec, base, rly), bus), bus
}

// TestNewBridgingDemo_EnvAndHook pins the toggle's whole contract in one
// pass: enabling appends exactly the FIXED "2.0" knob (no picker) plus the
// edge-capture flag to a CLONE of the baseline, disabling restarts with the
// bare baseline (neither knob present), the gateway child is the target, the
// relay's identity and cursor refresh ride the preSpawn hook, and each successful
// toggle emits its child-typed bus event.
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
	wantEnabled := append(append([]string{}, base...), "SHN_DEMO_EGRESS_NATIVE_LINES=2.0", "SHN_DEMO_EDGE_CAPTURE=true")
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

	// The source reset must ride the preSpawn hook, never a
	// call after the restart returns (the stale-gen wedge).
	if rec.preSpawn[0] == nil {
		t.Fatal("preSpawn was nil with a relay published — the cursor reset would never happen")
	}
	// ResetSource invalidates an installed prior stamp only inside the hook.
	rly.SetStamp(relay.Stamp{RunID: "prior"})
	rly.Begin(relay.Stamp{}, func(error) {})
	rec.preSpawn[0]()
	var boundaryErr error
	rly.End(false, func(err error) { boundaryErr = err })
	if boundaryErr == nil {
		t.Fatal("preSpawn did not invalidate the old source window")
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
	enabledEnv := append(append([]string{}, base...), "SHN_DEMO_EGRESS_NATIVE_LINES=2.0", "SHN_DEMO_EDGE_CAPTURE=true")

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

// fakePersister is levelPersister's test double: records every Set call and
// answers setErr (nil ⇒ success) — never touches disk.
type fakePersister struct {
	mu     sync.Mutex
	calls  []string
	setErr error
}

func (p *fakePersister) Set(level string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, level)
	return p.setErr
}

func (p *fakePersister) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.calls...)
}

// conformanceFixture wires a newConformanceLevel closure over a recorder, a
// fake persister, a live bus, and a baseline gateway env — the same shape as
// demoFixture above.
func conformanceFixture(t *testing.T, rec *demoRestartRecorder, persist *fakePersister, base []string, rly *relay.Relay) (func(context.Context, string) error, *event.Bus) {
	t.Helper()
	bus := event.NewBus(func() time.Time { return time.Unix(0, 0).UTC() })
	return newConformanceLevel(newTestGatewayEnvSwitch(rec, base, rly), persist, bus), bus
}

// crossToggleFixture wires BOTH the bridging-demo and conformance-level
// closures over the SAME gatewayEnvSwitch instance — the shape that pins
// the mutual-clobber fix (gatewayEnvSwitch's own doc has the full story):
// each toggle's transform must compose onto whatever the OTHER most
// recently applied, never a frozen boot baseline, and the two toggles must
// serialize against EACH OTHER, not just each against itself.
func crossToggleFixture(t *testing.T, rec *demoRestartRecorder, persist *fakePersister, base []string) (demoToggle func(context.Context, bool) error, levelToggle func(context.Context, string) error) {
	t.Helper()
	bus := event.NewBus(func() time.Time { return time.Unix(0, 0).UTC() })
	sw := newTestGatewayEnvSwitch(rec, base, nil)
	return newBridgingDemo(sw, bus), newConformanceLevel(sw, persist, bus)
}

func TestSetConformanceEnvVar(t *testing.T) {
	base := []string{"ROLE=provider", "PORT=9999"}

	// level == "": the entry (if any) is removed, nothing appended —
	// mirrors conformanceEnv("")'s own "emit nothing" rule.
	got := setConformanceEnvVar(base, "")
	want := []string{"ROLE=provider", "PORT=9999"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("setConformanceEnvVar(base, \"\") = %v, want %v (no entry, nothing added)", got, want)
	}

	// A named level appends the entry.
	got = setConformanceEnvVar(base, "strict")
	want = []string{"ROLE=provider", "PORT=9999", "CONFORMANCE_ENFORCEMENT=strict"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("setConformanceEnvVar(base, strict) = %v, want %v", got, want)
	}

	// REPLACE, not duplicate: a base that already carries an entry (the
	// boot-time seed) gets that entry swapped, never a second one appended.
	baseWithEntry := []string{"ROLE=provider", "CONFORMANCE_ENFORCEMENT=strict", "PORT=9999"}
	got = setConformanceEnvVar(baseWithEntry, "none")
	want = []string{"ROLE=provider", "PORT=9999", "CONFORMANCE_ENFORCEMENT=none"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("setConformanceEnvVar(baseWithEntry, none) = %v, want %v (replaced, not duplicated)", got, want)
	}

	// Clearing back to "" from a base that carries an entry removes it.
	got = setConformanceEnvVar(baseWithEntry, "")
	want = []string{"ROLE=provider", "PORT=9999"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("setConformanceEnvVar(baseWithEntry, \"\") = %v, want %v (entry removed)", got, want)
	}

	// base's own backing array is never mutated or shared with the result.
	if &got[0] == &baseWithEntry[0] {
		t.Fatal("setConformanceEnvVar's result shares a backing array with base")
	}
	if strings.Join(baseWithEntry, "\x00") != "ROLE=provider\x00CONFORMANCE_ENFORCEMENT=strict\x00PORT=9999" {
		t.Fatalf("base was mutated: %v", baseWithEntry)
	}
}

// TestNewConformanceLevel_EnvReplacementAndHook pins the happy-path
// contract: the requested level lands as the gateway child's SOLE
// CONFORMANCE_ENFORCEMENT entry (replacing whatever the baseline carried),
// the gateway child is the restart target, the relay's cursor reset rides
// preSpawn, a successful toggle persists, and a bus event fires.
func TestNewConformanceLevel_EnvReplacementAndHook(t *testing.T) {
	base := append(make([]string, 0, 8), "ROLE=provider", "CONFORMANCE_ENFORCEMENT=strict")
	rec := &demoRestartRecorder{}
	persist := &fakePersister{}
	rly := relay.New("http://127.0.0.1:1/events", "http://127.0.0.1:1/health", event.NewBus(time.Now), log.Printf)
	toggle, bus := conformanceFixture(t, rec, persist, base, rly)

	if err := toggle(context.Background(), "none"); err != nil {
		t.Fatalf("toggle(none): %v", err)
	}
	if rec.names[0] != gatewayChild {
		t.Fatalf("restart target = %q, want %q", rec.names[0], gatewayChild)
	}
	gotEnv := rec.envs[0]
	wantEnv := []string{"ROLE=provider", "CONFORMANCE_ENFORCEMENT=none"}
	if strings.Join(gotEnv, "\x00") != strings.Join(wantEnv, "\x00") {
		t.Fatalf("applied env = %v, want %v (replaced, not duplicated)", gotEnv, wantEnv)
	}
	if len(base) != 2 || base[1] != "CONFORMANCE_ENFORCEMENT=strict" {
		t.Fatalf("the baseline itself was mutated: %v", base)
	}
	if rec.preSpawn[0] == nil {
		t.Fatal("preSpawn was nil with a relay published — the cursor reset would never happen")
	}
	rly.SetStamp(relay.Stamp{RunID: "prior"})
	rly.Begin(relay.Stamp{}, func(error) {})
	rec.preSpawn[0]()
	var boundaryErr error
	rly.End(false, func(err error) { boundaryErr = err })
	if boundaryErr == nil {
		t.Fatal("preSpawn did not invalidate the old source window")
	}

	if got := persist.snapshot(); len(got) != 1 || got[0] != "none" {
		t.Fatalf("persist calls = %v, want exactly one call with \"none\"", got)
	}

	var details []string
	for _, e := range bus.Since(0) {
		if e.Type == event.TypeChild && e.Child == gatewayChild {
			details = append(details, e.Detail)
		}
	}
	if len(details) != 1 || details[0] != "conformance-enforcement: none" {
		t.Fatalf("bus events = %v, want [conformance-enforcement: none]", details)
	}
}

// TestNewConformanceLevel_ClearToDefault proves level == "" both removes
// the env entry AND persists the clear (not a no-op).
func TestNewConformanceLevel_ClearToDefault(t *testing.T) {
	base := []string{"ROLE=provider", "CONFORMANCE_ENFORCEMENT=strict"}
	rec := &demoRestartRecorder{}
	persist := &fakePersister{}
	toggle, _ := conformanceFixture(t, rec, persist, base, nil)

	if err := toggle(context.Background(), ""); err != nil {
		t.Fatalf("toggle(\"\"): %v", err)
	}
	want := []string{"ROLE=provider"}
	if strings.Join(rec.envs[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("cleared env = %v, want %v", rec.envs[0], want)
	}
	if got := persist.snapshot(); len(got) != 1 || got[0] != "" {
		t.Fatalf("persist calls = %v, want exactly one call with \"\"", got)
	}
}

// TestNewConformanceLevel_RejectsUnknownLevel proves an invalid level is
// refused BEFORE any restart is attempted or anything persisted — the same
// validate-before-touching-anything discipline conformance.Store.Set holds.
func TestNewConformanceLevel_RejectsUnknownLevel(t *testing.T) {
	rec := &demoRestartRecorder{}
	persist := &fakePersister{}
	toggle, _ := conformanceFixture(t, rec, persist, []string{"ROLE=provider"}, nil)

	if err := toggle(context.Background(), "middle"); err == nil {
		t.Fatal(`toggle("middle") = nil, want an error — only "", "strict", "none" are accepted`)
	}
	if rec.calls() != 0 {
		t.Fatalf("restart called %d times for an invalid level, want 0", rec.calls())
	}
	if got := persist.snapshot(); len(got) != 0 {
		t.Fatalf("persist called %v for an invalid level, want none", got)
	}
}

// TestNewConformanceLevel_NoBaseline refuses rather than restarting the
// gateway with a synthesized env before BuildStack has published one.
func TestNewConformanceLevel_NoBaseline(t *testing.T) {
	rec := &demoRestartRecorder{}
	persist := &fakePersister{}
	toggle, _ := conformanceFixture(t, rec, persist, nil, nil)

	err := toggle(context.Background(), "strict")
	if err == nil || !strings.Contains(err.Error(), "has not been built") {
		t.Fatalf("toggle before BuildStack = %v, want a refusal naming the missing stack", err)
	}
	if rec.calls() != 0 {
		t.Fatalf("restart called %d times before the stack existed, want 0", rec.calls())
	}
}

// TestNewConformanceLevel_FailedToggleNeverPersists proves persistence is
// gated on the restart's own success: a failed toggle (even after the
// revert succeeds) must not write a level the running gateway child does
// NOT actually have.
func TestNewConformanceLevel_FailedToggleNeverPersists(t *testing.T) {
	rec := &demoRestartRecorder{err: fmt.Errorf("supervisor: gateway not ready within 30s")}
	persist := &fakePersister{}
	toggle, bus := conformanceFixture(t, rec, persist, []string{"ROLE=provider"}, nil)

	if err := toggle(context.Background(), "strict"); err == nil {
		t.Fatal("toggle(strict) with a failing restart returned nil")
	}
	if got := persist.snapshot(); len(got) != 0 {
		t.Fatalf("persist called %v for a FAILED toggle, want none", got)
	}
	for _, e := range bus.Since(0) {
		if strings.Contains(e.Detail, "conformance-enforcement") {
			t.Fatalf("a FAILED toggle emitted %q", e.Detail)
		}
	}
}

// TestNewConformanceLevel_FailedToggleRevertsEnv mirrors
// TestNewBridgingDemo_FailedToggleRevertsEnv's revert-to-baseline case: a
// failed restart runs one more arc re-registering the ORIGINAL baseline env
// (which may already carry its own CONFORMANCE_ENFORCEMENT entry from the
// boot-time seed — that entry is the prior, WORKING level and must be
// restored verbatim, never stripped).
func TestNewConformanceLevel_FailedToggleRevertsEnv(t *testing.T) {
	base := []string{"ROLE=provider", "CONFORMANCE_ENFORCEMENT=strict"}
	failErr := fmt.Errorf("supervisor: gateway not ready within 30s")
	rec := &demoRestartRecorder{errs: []error{failErr, nil}}
	persist := &fakePersister{}
	toggle, _ := conformanceFixture(t, rec, persist, base, nil)

	err := toggle(context.Background(), "none")
	if err == nil || !errors.Is(err, failErr) {
		t.Fatalf("toggle(none) = %v, want the restart failure", err)
	}
	if rec.calls() != 2 {
		t.Fatalf("restart called %d times, want 2 (failed change + revert)", rec.calls())
	}
	if strings.Join(rec.envs[1], "\x00") != strings.Join(base, "\x00") {
		t.Fatalf("revert env = %v, want the bare baseline (its own strict entry intact) %v", rec.envs[1], base)
	}
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

// TestGatewayEnvSwitch_CrossToggleComposition is the pin for the
// mutual-clobber bug gatewayEnvSwitch fixes (see its own doc): the
// bridging-demo toggle and the conformance-level toggle restart the SAME
// gateway child, and each must layer its own knob onto whatever the OTHER
// most recently applied — never onto a frozen boot baseline, which would
// silently erase the other toggle's live effect while leaving its own
// recorded state (GET /api/status) claiming otherwise. Both orders.
func TestGatewayEnvSwitch_CrossToggleComposition(t *testing.T) {
	t.Run("conformance then demo: the conformance entry survives the demo toggle", func(t *testing.T) {
		rec := &demoRestartRecorder{}
		persist := &fakePersister{}
		demoToggle, levelToggle := crossToggleFixture(t, rec, persist, []string{"ROLE=provider"})

		if err := levelToggle(context.Background(), "strict"); err != nil {
			t.Fatalf("levelToggle(strict): %v", err)
		}
		if err := demoToggle(context.Background(), true); err != nil {
			t.Fatalf("demoToggle(true): %v", err)
		}

		final := rec.envs[len(rec.envs)-1]
		want := []string{
			"ROLE=provider",
			"CONFORMANCE_ENFORCEMENT=strict",
			"SHN_DEMO_EGRESS_NATIVE_LINES=2.0",
			"SHN_DEMO_EDGE_CAPTURE=true",
		}
		if strings.Join(final, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("final env after conformance-then-demo = %v, want %v (the conformance entry must survive)", final, want)
		}
	})

	t.Run("demo then conformance: the demo entries survive the conformance toggle", func(t *testing.T) {
		rec := &demoRestartRecorder{}
		persist := &fakePersister{}
		demoToggle, levelToggle := crossToggleFixture(t, rec, persist, []string{"ROLE=provider"})

		if err := demoToggle(context.Background(), true); err != nil {
			t.Fatalf("demoToggle(true): %v", err)
		}
		if err := levelToggle(context.Background(), "strict"); err != nil {
			t.Fatalf("levelToggle(strict): %v", err)
		}

		final := rec.envs[len(rec.envs)-1]
		want := []string{
			"ROLE=provider",
			"SHN_DEMO_EGRESS_NATIVE_LINES=2.0",
			"SHN_DEMO_EDGE_CAPTURE=true",
			"CONFORMANCE_ENFORCEMENT=strict",
		}
		if strings.Join(final, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("final env after demo-then-conformance = %v, want %v (the demo entries must survive)", final, want)
		}
	})

	t.Run("disabling demo after setting a level removes only the demo entries", func(t *testing.T) {
		rec := &demoRestartRecorder{}
		persist := &fakePersister{}
		demoToggle, levelToggle := crossToggleFixture(t, rec, persist, []string{"ROLE=provider"})

		if err := levelToggle(context.Background(), "strict"); err != nil {
			t.Fatalf("levelToggle(strict): %v", err)
		}
		if err := demoToggle(context.Background(), true); err != nil {
			t.Fatalf("demoToggle(true): %v", err)
		}
		if err := demoToggle(context.Background(), false); err != nil {
			t.Fatalf("demoToggle(false): %v", err)
		}

		final := rec.envs[len(rec.envs)-1]
		want := []string{"ROLE=provider", "CONFORMANCE_ENFORCEMENT=strict"}
		if strings.Join(final, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("final env after level+demo-on+demo-off = %v, want %v (the level must survive demo being disabled)", final, want)
		}
	})

	t.Run("clearing the level after enabling demo removes only the conformance entry", func(t *testing.T) {
		rec := &demoRestartRecorder{}
		persist := &fakePersister{}
		demoToggle, levelToggle := crossToggleFixture(t, rec, persist, []string{"ROLE=provider"})

		if err := demoToggle(context.Background(), true); err != nil {
			t.Fatalf("demoToggle(true): %v", err)
		}
		if err := levelToggle(context.Background(), "strict"); err != nil {
			t.Fatalf("levelToggle(strict): %v", err)
		}
		if err := levelToggle(context.Background(), ""); err != nil {
			t.Fatalf("levelToggle(\"\"): %v", err)
		}

		final := rec.envs[len(rec.envs)-1]
		want := []string{"ROLE=provider", "SHN_DEMO_EGRESS_NATIVE_LINES=2.0", "SHN_DEMO_EDGE_CAPTURE=true"}
		if strings.Join(final, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("final env after demo-on+level+level-clear = %v, want %v (demo must survive the level being cleared)", final, want)
		}
	})
}

// shnkitdSources parses this package's own non-test .go sources — the same
// source-level-guard posture gateway/engine/conformance_sources_test.go
// uses (its own doc explains why: some properties only exist in main()'s
// wiring, which no closure-level test can observe, since a closure-level
// fixture builds its own switch directly rather than exercising main()'s
// actual construction).
func shnkitdSources(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read .: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = f
	}
	return fset, files
}

// TestGatewayEnvSwitchWiringIsSingleInstance is the source-level half of the
// mutual-clobber fix — the half TestGatewayEnvSwitch_CrossToggleComposition
// cannot cover, because that test builds its OWN gatewayEnvSwitch directly
// and never touches main()'s actual wiring. Nothing in the type system stops
// a future edit from constructing a second gatewayEnvSwitch and handing one
// to newBridgingDemo and the other to newConformanceLevel — which would
// reopen exactly the bug gatewayEnvSwitch exists to fix, with every
// closure-level test in this file still green (each closure is internally
// correct; only the SHARING would be gone). This reads main.go's own source
// and asserts, at the syntax-tree level:
//
//  1. exactly one `&gatewayEnvSwitch{...}` struct literal exists in the
//     package's non-test source;
//  2. the call to newBridgingDemo and the call to newConformanceLevel each
//     pass a simple identifier as their first argument, and it is the SAME
//     identifier in both calls.
//
// A second construction, or either call wired to a different identifier,
// must fail here.
func TestGatewayEnvSwitchWiringIsSingleInstance(t *testing.T) {
	fset, files := shnkitdSources(t)

	var switchSites []string
	var bridgingArg, bridgingSite string
	var levelArg, levelSite string

	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			// &gatewayEnvSwitch{...}
			if ue, ok := n.(*ast.UnaryExpr); ok && ue.Op == token.AND {
				if cl, ok := ue.X.(*ast.CompositeLit); ok {
					if id, ok := cl.Type.(*ast.Ident); ok && id.Name == "gatewayEnvSwitch" {
						pos := fset.Position(n.Pos())
						switchSites = append(switchSites, fmt.Sprintf("%s:%d", name, pos.Line))
					}
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			switch id.Name {
			case "newBridgingDemo":
				pos := fset.Position(n.Pos())
				bridgingSite = fmt.Sprintf("%s:%d", name, pos.Line)
				if len(call.Args) > 0 {
					if argID, ok := call.Args[0].(*ast.Ident); ok {
						bridgingArg = argID.Name
					}
				}
			case "newConformanceLevel":
				pos := fset.Position(n.Pos())
				levelSite = fmt.Sprintf("%s:%d", name, pos.Line)
				if len(call.Args) > 0 {
					if argID, ok := call.Args[0].(*ast.Ident); ok {
						levelArg = argID.Name
					}
				}
			}
			return true
		})
	}

	sort.Strings(switchSites)
	if len(switchSites) != 1 {
		t.Fatalf("found %d &gatewayEnvSwitch{...} construction(s) in kit/cmd/shnkitd's non-test source (%v), want exactly 1 — two independent instances reopen the mutual-clobber bug gatewayEnvSwitch exists to fix (see its own doc comment)", len(switchSites), switchSites)
	}
	if bridgingSite == "" {
		t.Fatal("no call to newBridgingDemo found in kit/cmd/shnkitd's non-test source")
	}
	if levelSite == "" {
		t.Fatal("no call to newConformanceLevel found in kit/cmd/shnkitd's non-test source")
	}
	if bridgingArg == "" {
		t.Fatalf("newBridgingDemo (%s) is not called with a simple identifier as its first argument — cannot verify it shares gatewayEnvSwitch with newConformanceLevel", bridgingSite)
	}
	if levelArg == "" {
		t.Fatalf("newConformanceLevel (%s) is not called with a simple identifier as its first argument — cannot verify it shares gatewayEnvSwitch with newBridgingDemo", levelSite)
	}
	if bridgingArg != levelArg {
		t.Fatalf("newBridgingDemo (%s, wired to %q) and newConformanceLevel (%s, wired to %q) are constructed over DIFFERENT variables — this is the mutual-clobber bug's exact shape: both toggles must restart through the SAME shared gatewayEnvSwitch instance", bridgingSite, bridgingArg, levelSite, levelArg)
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

// Each respawn, including rollback, must discard its predecessor's trusted
// executable profile before the replacement may start.
func TestBridgingRespawnRefreshesChangedExecutableOnRevert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/barrier" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"events":0}`)
	}))
	defer srv.Close()
	bus := event.NewBus(time.Now)
	rly := relay.New(srv.URL+"/events", srv.URL+"/health", bus, t.Logf)
	var ptr atomic.Pointer[relay.Relay]
	ptr.Store(rly)
	base := []string{"ROLE=provider"}
	var envPtr atomic.Pointer[[]string]
	envPtr.Store(&base)
	var phases []string
	calls := 0
	restart := func(_ context.Context, _ string, _ []string, preSpawn func()) error {
		calls++
		rly.SetGatewayProfile(relay.GatewayLegacySync0431)
		phases = append(phases, "stopped")
		if preSpawn == nil {
			t.Fatal("missing before-spawn hook")
		}
		preSpawn()
		phases = append(phases, "identity-and-cursor")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := rly.Drain(ctx); err == nil || !strings.Contains(err.Error(), "status 404") {
			t.Fatalf("changed executable retained predecessor profile: %v", err)
		}
		phases = append(phases, "spawn")
		if calls == 1 {
			return errors.New("replacement readiness failed")
		}
		return nil
	}
	sw := &gatewayEnvSwitch{restart: restart, rlyPtr: &ptr, gwEnvPtr: &envPtr, gatewayBinary: filepath.Join(t.TempDir(), "changed-or-missing-gateway")}
	toggle := newBridgingDemo(sw, bus)
	if err := toggle(context.Background(), true); err == nil {
		t.Fatal("expected failed primary respawn")
	}
	if got := strings.Join(phases, ","); got != "stopped,identity-and-cursor,spawn,stopped,identity-and-cursor,spawn" {
		t.Fatal(got)
	}
}

// TestConformanceEnv pins --conformance-enforcement's translation into the
// gateway child's ExtraEnv: an empty flag (the operator never passed it)
// emits NOTHING, so the child's own CONFORMANCE_ENFORCEMENT default applies
// untouched — a Kit gateway follows the published default the same way any
// other unconfigured gateway does, and that default has exactly one home
// (the gateway's own env loader), never a second copy hardcoded here that
// could drift from it the next time that default moves. A named value
// reaches the child exactly as given; conformanceEnv does not validate it —
// the gateway child's own boot-time refusal is the honest place for that
// (main.go's flag help text says so).
func TestConformanceEnv(t *testing.T) {
	cases := []struct {
		name  string
		level string
		want  []string
	}{
		{"absent: nothing emitted, the child's own default applies", "", nil},
		{"strict reaches the child verbatim", "strict", []string{"CONFORMANCE_ENFORCEMENT=strict"}},
		{"none reaches the child verbatim", "none", []string{"CONFORMANCE_ENFORCEMENT=none"}},
		{"an invalid value still passes through unvalidated", "middle", []string{"CONFORMANCE_ENFORCEMENT=middle"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := conformanceEnv(tc.level)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("conformanceEnv(%q) = %#v, want %#v", tc.level, got, tc.want)
			}
		})
	}
}
