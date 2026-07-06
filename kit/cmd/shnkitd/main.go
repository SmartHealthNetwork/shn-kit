// Command shnkitd is the SHN Kit daemon: it boots the run-timeline event bus,
// the child-process supervisor, and the loopback session-token-gated HTTP API
// (kit/kitd.Daemon) daemon-first — before the substrate stack exists — then,
// once the bootstrap.Machine reaches provisioned (either a pre-provisioned
// --secrets bundle at startup, or a fresh operator sign-in via the daemon's
// /api/bootstrap/signin), builds this Kit's single provider gateway
// (kit/kitd.BuildStack), the observer relay, and the scenario runner, wires
// them into the already-serving daemon, and runs the "hello
// substrate" Verify probes — the whole Kit's composition, as one thin
// process, daemon-first so the API is reachable before the substrate stack
// itself exists.
//
// main carries no logic beyond flag parsing and wiring: bootstrap.Machine,
// BuildStack, the supervisor, the relay, and the daemon each own their own
// behavior and tests; this file's only job is to hand them to each other in
// the right order and shut them down cleanly on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/bootstrap"
	"github.com/SmartHealthNetwork/shn-kit/byo"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/kitd"
	"github.com/SmartHealthNetwork/shn-kit/relay"
	"github.com/SmartHealthNetwork/shn-kit/runhistory"
	"github.com/SmartHealthNetwork/shn-kit/runner"
	"github.com/SmartHealthNetwork/shn-kit/supervisor"
	"github.com/SmartHealthNetwork/shn-kit/update"
)

// historyKeep bounds run-history retention; a
// config surface for this is a registered follow-up.
const historyKeep = 200

// devVersion is the ldflags default for kitVersion below — a genuine dev
// build sentinel: main SKIPS the async update check
// entirely when kitVersion == devVersion, since kit/update.Check itself has
// no notion of "dev version" (its own doc comment) — the skip is the
// CALLER's rule.
const devVersion = "0.0.0-dev"

// kitVersion is this build's own version, set at package time via
// `-ldflags "-X main.kitVersion=vX.Y.Z"` (tools/kitassets, the same
// versions.json-producing step that supplies --manifest below). Left at
// devVersion for every plain `go build`/`go run`/`go test` invocation.
var kitVersion = devVersion

// defaultReleasesURL is --releases-url's default: the GitHub "latest
// release" feed for this Kit's own repo. A 404 here is harmless
// until this Kit has a published release feed — update.Check's every failure is logged
// and discarded, never surfaced to an operator.
const defaultReleasesURL = "https://api.github.com/repos/SmartHealthNetwork/shn-kit/releases/latest"

func main() {
	stateDir := flag.String("state-dir", "", "Kit state directory (logs, ingress-clients.json, session.json) — required")
	gatewayBin := flag.String("gateway-bin", "", "absolute path to the published gateway binary — required")
	secrets := flag.String("secrets", "", "pre-provisioned shn register / Init bundle dir (SHN_SECRETS); \"\" => {state-dir}/secrets, populated by an operator sign-in")
	accountsURL := flag.String("accounts", "", "Accounts service base URL — required unless --secrets provides a pre-provisioned bundle")
	clientName := flag.String("client-name", "SHN Kit", "Accounts display name for this Kit's client registration")
	registerBaseURL := flag.String("register-base-url", "", `Da Vinci registration base URL ("" => derived <accounts origin>/kit-originator)`)
	noBrowser := flag.Bool("no-browser", false, "suppress the OS browser opener during sign-in; print the authorize URL instead")
	patientAppURL := flag.String("patient-app-url", "", "Smart Health account patient app URL, surfaced at GET /api/status")
	uiDir := flag.String("ui-dir", "", `built Kit UI dir served at /ui ("" => API only)`)
	discoveryURL := flag.String("discovery-url", "", "SHN_DISCOVERY_URL")
	auditURL := flag.String("audit-url", "", "AUDIT_URL")
	phgURL := flag.String("phg-url", "", "PHG_URL")
	consentURL := flag.String("consent-url", "", "CONSENT_URL")
	fhirDataURL := flag.String("fhir-data-url", "", `FHIR_DATA_URL ("" => memstub SoR)`)
	gatewayPort := flag.Int("gateway-port", 0, "gateway child port (0 = allocate)")
	apiAddr := flag.String("api-addr", "127.0.0.1:0", "kitd's loopback API bind address")
	token := flag.String("token", "", `kitd session token ("" => generate)`)
	fakeValidator := flag.Bool("fake-validator", true, "SHN_FAKE_VALIDATOR=1 for the gateway child (\"\" => derived: false when --java-assets is set and this flag is not explicitly passed)")
	uc07PCI := flag.String("uc07-pci", "", `UC-07 patient-surface PCI override ("" => resolve via ResolvePersonaPCI("Nakamura"))`)
	javaAssets := flag.String("java-assets", os.Getenv("SHN_KIT_JAVA_ASSETS"), `packaged Java trio assets dir (HAPI validator + seeded HAPI data server + br-provider; env SHN_KIT_JAVA_ASSETS) — "" => no trio, today's behavior`)
	jreDir := flag.String("jre-dir", "", `JRE root for the Java trio (containing bin/java[.exe]) ("" => {java-assets}/jre-{GOOS}-{GOARCH})`)
	tokenStoreFlag := flag.String("token-store", "", `login token storage backend: "keychain" or "file" ("" => derived: keychain when --java-assets is set, file otherwise)`)
	manifestPath := flag.String("manifest", "", `path to the package-time versions.json manifest, served verbatim at GET /api/about ("" => 404-with-body, a dev checkout with no packaged manifest)`)
	releasesURL := flag.String("releases-url", defaultReleasesURL, "GitHub \"latest release\" feed the launch-time update check GETs; overridable so a gate/test can stub it")
	flag.Parse()

	if *stateDir == "" || *gatewayBin == "" {
		fmt.Fprintln(os.Stderr, "shnkitd: --state-dir and --gateway-bin are required")
		os.Exit(1)
	}

	// --fake-validator's and --token-store's default derivations
	// share one shape: an EXPLICIT flag always wins; otherwise each
	// derives from whether the Java trio is configured (--java-assets set ⇒
	// the packaged Kit — real validator, OS keychain; unset ⇒ the dev
	// posture — fake validator, file tokens). flag.Visit only calls back for
	// flags the operator actually passed, so this correctly distinguishes
	// "explicitly set" from "left at its zero-value default."
	explicitFakeValidator := false
	explicitTokenStore := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "fake-validator":
			explicitFakeValidator = true
		case "token-store":
			explicitTokenStore = true
		}
	})
	if !explicitFakeValidator {
		*fakeValidator = *javaAssets == ""
	}
	tokenStoreKind := resolveTokenStoreKind(explicitTokenStore, *tokenStoreFlag, *javaAssets)

	// validatorPosture is the resolved --fake-validator posture GET
	// /api/status surfaces once the boot goroutine
	// calls d.SetStackInfo below — computed here (not later) because it
	// depends only on *fakeValidator, already fully resolved above.
	validatorPosture := "packaged"
	if *fakeValidator {
		validatorPosture = "stand-in"
	}

	// --jre-dir's default (main resolves it per-arch, per StackConfig.JREDir's
	// doc comment): {java-assets}/jre-{GOOS}-{GOARCH}. Left "" entirely when
	// no trio is configured — BuildStack never reads it in that case.
	resolvedJREDir := *jreDir
	if resolvedJREDir == "" && *javaAssets != "" {
		resolvedJREDir = filepath.Join(*javaAssets, fmt.Sprintf("jre-%s-%s", runtime.GOOS, runtime.GOARCH))
	}

	// kitd.Serve does not create StateDir; main must, before
	// wiring anything that writes under it (ingress-clients.json, logs,
	// session.json).
	if err := os.MkdirAll(*stateDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "shnkitd: create state dir %s: %v\n", *stateDir, err)
		os.Exit(1)
	}

	secretsDir := *secrets
	if secretsDir == "" {
		if *accountsURL == "" {
			fmt.Fprintln(os.Stderr, "shnkitd: --accounts is required unless --secrets provides a pre-provisioned bundle")
			os.Exit(1)
		}
		secretsDir = filepath.Join(*stateDir, "secrets")
	}
	// --secrets pointing at a dir with no loadable bundle AND
	// --accounts empty used to silently degrade into a Kit that could never
	// sign in (no persisted bundle to resume from, no accounts URL to sign
	// in fresh against) — a hard startup error naming BOTH facts instead.
	if err := validateSecretsAccounts(*secrets, *accountsURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	registerBase := *registerBaseURL
	if registerBase == "" && *accountsURL != "" {
		registerBase = strings.TrimRight(*accountsURL, "/") + "/kit-originator" // originator-only placeholder; never dialed, must validate publicly
	}
	var opener func(string) error
	if !*noBrowser {
		opener = openBrowser
	}

	bus := event.NewBus(time.Now)

	// rlyPtr publishes the relay once the boot goroutine constructs it
	// (below); the supervisor's notify closure is wired up here, BEFORE the
	// relay exists, so it reads through the pointer rather than closing over
	// a nil *relay.Relay.
	var rlyPtr atomic.Pointer[relay.Relay]
	sup := supervisor.New(func(n supervisor.Notice) {
		bus.Emit(event.Event{Type: event.TypeChild, Child: n.Child, Detail: n.State + ": " + n.Detail})
		if n.Child == "gateway" && n.State == supervisor.StateRestarting { // kitd.gatewayChildName
			if r := rlyPtr.Load(); r != nil {
				r.ResetCursor() // fresh child = fresh observer seq epoch
			}
		}
	})

	// tokens is the selected TokenStore: newTokenStore wraps the
	// file-backed store in a keychain-backed one (falling back to the SAME
	// file store on OS keyring error) when tokenStoreKind == "keychain",
	// else returns the file store unchanged.
	fileTokens := bootstrap.NewFileTokenStore(filepath.Join(*stateDir, "tokens.json"), *accountsURL)
	tokens := newTokenStore(tokenStoreKind, fileTokens, *accountsURL)

	m := bootstrap.New(bootstrap.Config{
		AccountsURL:     *accountsURL,
		SecretsDir:      secretsDir,
		ClientName:      *clientName,
		Role:            "provider",
		RegisterBaseURL: registerBase,
		Tokens:          tokens,
		Bus:             bus,
		OpenBrowser:     opener,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	histStore := runhistory.NewStore(filepath.Join(*stateDir, "runs"), historyKeep)

	// byo.json is loaded once, up front, so both the boot goroutine's
	// StackConfig overrides and the operator-facing surface (
	// GET /api/byo, via SetBYO below) see the SAME applied config for this
	// process's lifetime — a swap only takes effect on restart. A missing
	// file is not an error (byo.Store.Load's contract):
	// byoCfg stays the zero Config, meaning "nothing swapped, use the
	// bundled sandbox defaults." A present-but-corrupt file IS an error;
	// fail safe rather than fail closed — boot proceeds on demo
	// defaults, and the load error is surfaced via BYORuntime.LoadError
	// rather than silently swallowed.
	byoStore := byo.NewStore(*stateDir)
	byoCfg, byoLoadErr := byoStore.Load()
	if byoLoadErr != nil {
		log.Printf("shnkitd: byo.json unreadable — booting on demo defaults: %v", byoLoadErr)
		byoCfg = byo.Config{} // fail-safe; surfaced via GET /api/byo loadError
	}

	// tokenStorage surfaces tokens' Detail() at GET /api/bootstrap
	// when the selected store implements it (the keychain backend does; the
	// plain file store does not, and the key is omitted entirely in that
	// case — kitd.Config.TokenStorage's own nil-omits contract).
	var tokenStorage kitd.TokenStorage
	if ts, ok := tokens.(kitd.TokenStorage); ok {
		tokenStorage = ts
	}

	d, err := kitd.New(kitd.Config{
		APIAddr:       *apiAddr,
		StateDir:      *stateDir,
		Token:         *token,
		Bus:           bus,
		Sup:           sup,
		Runner:        nil, // daemon-first: no Runner until the boot goroutine's SetRunner, once the stack starts
		Boot:          m,
		PatientAppURL: *patientAppURL,
		UIDir:         *uiDir,
		History:       histStore,
		BYO:           byoStore,
		Restarter:     restarterFunc(sup.Restart),
		TokenStorage:  tokenStorage,
		ManifestPath:  *manifestPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "shnkitd: new daemon: %v\n", err)
		sup.StopAll() // no children yet at this point, but defensive: every exit path stops the supervisor
		os.Exit(1)
	}

	// signal.Notify is registered BEFORE the boot goroutine launches:
	// the pre-provisioned --secrets fast path can reach
	// sup.Start — spawning real child processes — synchronously inside that
	// goroutine before main would otherwise have reached the code below it,
	// so a SIGINT/SIGTERM landing in that narrow window must already have
	// somewhere to be caught. Moving Notify's registration here pins the
	// ordering by construction (a reviewable, static guarantee) rather than
	// relying on the goroutine scheduler happening to run this line first;
	// the trio gate's SIGTERM row proves the resulting behavior —
	// all children reaped, exit 0 — live.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// bootFailed is closed (once) by the boot goroutine before cancel() on any
	// genuine boot failure (BuildStack error, or sup.Start error with ctx
	// still live — see the ctx.Err() guards below). Daemon.Serve returns nil
	// on ordinary ctx cancellation (kit/kitd/kitd.go), so without this signal
	// a failed stack boot would exit 0 — the packaging smoke and any CI
	// wrapper read this process's exit code, so a swallowed boot failure
	// would read green.
	//
	// Happens-before chain for the exit code (why main's final select below
	// never races): close(bootFailed) is sequenced-before this goroutine's
	// own cancel() call, which is sequenced-before ctx.Done() being closed.
	// Per the Go memory model, a receive that returns because a channel was
	// closed happens after that close, so Serve's <-ctx.Done() (in
	// kit/kitd/kitd.go) happens after our cancel(), which happens after
	// close(bootFailed). Serve's subsequent `errCh <- nil` happens after that
	// receive, and main's `case err := <-errCh` happens after that send. So
	// by the time main reaches the `select { case <-bootFailed: ... }` below,
	// close(bootFailed) — if it happened at all — is already transitively
	// ordered before it: no timing-dependent race between observing
	// bootFailed and falling through to default. The signal path (SIGINT
	// before boot finishes) is symmetric: main calls cancel() itself, then
	// waits on <-errCh, so any subsequent boot-goroutine failure sees
	// ctx.Err() != nil and (per the guards below) does NOT close bootFailed —
	// the final select correctly falls through to default and exits 0.
	bootFailed := make(chan struct{})
	go func() {
		select {
		case <-m.Provisioned():
		case <-ctx.Done():
			return
		}

		// Best-effort restrictive Windows ACL on the secrets
		// bundle dir. m.Provisioned() closing covers BOTH cases this needs —
		// a bundle bootstrap.New already found loadable at construction
		// ("at boot when the dir exists") and a bundle a fresh provision()
		// just wrote (bootstrap.go's WriteBundle, "after bundle write") —
		// with one call site rather than two. restrictSecretsDirWindows is a
		// no-op on non-Windows. A failure here is logged and folded into the
		// bootstrap probe Detail below (fail-visible, never silent) but
		// never blocks boot.
		var aclErr error
		if err := restrictSecretsDirWindows(secretsDir); err != nil {
			aclErr = err
			log.Printf("shnkitd: secrets dir ACL: %v", err)
		}

		// On the pre-provisioned --secrets path, bootstrap.New closes
		// Provisioned() synchronously (the bundle is already loadable), so the
		// select above can fall through before the separate `go d.Serve(ctx)`
		// goroutine below has bound the listener and written session.json —
		// nothing else orders the two goroutines. Children must never spawn
		// before the daemon's API contract (session.json) exists, so wait for
		// d.Ready() (closed by kitd right before it serves) here too.
		select {
		case <-d.Ready():
		case <-ctx.Done():
			return
		}
		scfg := kitd.StackConfig{
			GatewayBinary: *gatewayBin,
			StateDir:      *stateDir,
			SecretsDir:    secretsDir, // the bundle home resolved above (--secrets or {state-dir}/secrets)
			DiscoveryURL:  *discoveryURL,
			AuditURL:      *auditURL,
			PHGURL:        *phgURL,
			ConsentURL:    *consentURL,
			FHIRDataURL:   *fhirDataURL,
			FakeValidator: *fakeValidator,
			GatewayPort:   *gatewayPort,
			JavaAssetsDir: *javaAssets,
			JREDir:        resolvedJREDir,
		}
		// byo.json overrides: the EHR lane replaces
		// the --fhir-data-url demo default and carries its own SMART quad;
		// the DaVinci lane merges into the gateway's ingress-clients.json
		// alongside the internal shn-kit-driver entry (kitd.BuildStack does
		// the merge). byoHC is the SAME authenticated (or nil ⇒
		// unauthenticated) client the boot-and-reprobe verify probe and the
		// browse panel's Browser both use below — one client, one identity,
		// for this process's lifetime.
		var byoHC *http.Client
		if e := byoCfg.EHR; e != nil {
			scfg.FHIRDataURL = e.DataURL // byo.json overrides the --fhir-data-url demo default
			scfg.FHIRTokenURL, scfg.FHIRClientID, scfg.FHIRClientAlg = e.TokenURL, e.ClientID, e.Alg
			scfg.FHIRClientScope, scfg.FHIRClientKID = e.Scope, e.KID
			if e.TokenURL != "" {
				scfg.FHIRClientKeyPath = byoStore.EHRKeyPath()
			}
			keyPEM, kerr := os.ReadFile(byoStore.EHRKeyPath()) // absent ok for unauthenticated
			if kerr != nil && !errors.Is(kerr, fs.ErrNotExist) {
				log.Printf("shnkitd: byo EHR key %s: %v", byoStore.EHRKeyPath(), kerr)
			}
			hc, herr := byo.EHRHTTPClient(e, keyPEM) // validated at save (byo.Store.SetEHR); an error here is unexpected but non-fatal
			if herr != nil {
				log.Printf("shnkitd: byo EHR http client: %v — falling back to unauthenticated probe/browse", herr)
			}
			byoHC = hc
		}
		if dv := byoCfg.DaVinci; dv != nil {
			scfg.ExtraIngressClients = append(scfg.ExtraIngressClients, kitd.IngressClient{ClientID: dv.ClientID, Alg: dv.Alg, PublicKeyPEM: dv.PublicKeyPEM})
		}

		stack, err := kitd.BuildStack(scfg)
		if err != nil {
			if ctx.Err() != nil {
				// A signal (or other cancellation) landed while BuildStack was
				// running: BuildStack's own failure (if any) is just fallout
				// from the shutdown already in flight, not a genuine boot
				// failure — ctx is already cancelled, so don't close
				// bootFailed or call cancel() again.
				bus.Emit(event.Event{Type: event.TypeChild, Detail: "boot aborted by shutdown"})
				return
			}
			bus.Emit(event.Event{Type: event.TypeChild, Detail: "build stack: " + err.Error()})
			close(bootFailed)
			cancel()
			return
		}

		// Published as soon as BuildStack has resolved the facts it carries —
		// this is what unlocks GET /api/status's
		// "validator"/"brProviderUrl" fields and POST /api/children/{name}/
		// restart's pre-boot gate; both were 503/absent before this point.
		d.SetStackInfo(kitd.StackInfo{Validator: validatorPosture, BRProviderURL: stack.BRProviderURL})

		// Pre-spawn H2 prewarm copy: MUST run
		// between BuildStack and the Start loop below — a running HAPI child
		// creates its own empty H2 store and holds its file lock the moment
		// it spawns, so copying the package-time-prewarmed store any later
		// would either silently no-op or collide with that live lock. A
		// no-op when *javaAssets == "" (CopyPrewarmedH2's own guard).
		if err := kitd.CopyPrewarmedH2(*javaAssets, *stateDir, log.Printf); err != nil {
			if ctx.Err() != nil {
				bus.Emit(event.Event{Type: event.TypeChild, Detail: "boot aborted by shutdown"})
				return
			}
			bus.Emit(event.Event{Type: event.TypeChild, Detail: "copy prewarmed H2: " + err.Error()})
			close(bootFailed)
			cancel()
			return
		}

		for _, spec := range stack.Children {
			if err := sup.Start(ctx, spec); err != nil {
				if ctx.Err() != nil {
					// Same reasoning as above: supervisor.waitReady treats ctx
					// cancellation as a start error, so a SIGINT that lands
					// while a child is starting must not be misclassified as
					// a boot failure.
					bus.Emit(event.Event{Type: event.TypeChild, Child: spec.Name, Detail: "boot aborted by shutdown"})
					return
				}
				bus.Emit(event.Event{Type: event.TypeChild, Child: spec.Name, Detail: "start failed: " + err.Error()})
				close(bootFailed)
				cancel()
				return
			}
		}

		// Post-ready persona freshen: runs after every child
		// (including the data server) has passed its ReadyURLs probe, before
		// SetRunner below. Unlike the H2 copy above, this has NO skip gate —
		// it always re-loads the provider-data personas, keeping the
		// operated CQL's 3-month Observation lookback alive across restarts.
		// A no-op when stack.DataServerURL == "" (no trio configured).
		if stack.DataServerURL != "" {
			if err := kitd.FreshenPersonas(ctx, stack.DataServerURL, log.Printf); err != nil {
				if ctx.Err() != nil {
					bus.Emit(event.Event{Type: event.TypeChild, Detail: "boot aborted by shutdown"})
					return
				}
				bus.Emit(event.Event{Type: event.TypeChild, Detail: "freshen personas: " + err.Error()})
				close(bootFailed)
				cancel()
				return
			}
		}

		// Publish the applied BYO state now that the stack (and hence
		// stack.GatewayURL) exists — mirrors SetRunner below: the daemon
		// itself is reachable before boot has these facts. Published
		// unconditionally here (not gated on the bundle check below) so
		// GET /api/byo reflects reality even on the degraded
		// reset-raced-boot path.
		var browser *byo.Browser
		if e := byoCfg.EHR; e != nil {
			browser = byo.NewBrowser(e.DataURL, byoHC)
		}
		d.SetBYO(kitd.BYORuntime{Applied: byoCfg, Browser: browser, GatewayURL: stack.GatewayURL, LoadError: errString(byoLoadErr)})

		rly := relay.New(stack.ObserverURL, stack.ObserverHealthURL, bus, log.Printf)
		rlyPtr.Store(rly)
		go rly.Run(ctx)

		driver := scenariodriver.New(stack.Driver)

		// UC07PCI: the gate passes the harness's own PCI over --uc07-pci; absent
		// that, fall back to the product path (resolve Nakamura's live PCI off
		// the patient surface).
		resolveUC07PCI := func() (string, error) { return driver.ResolvePersonaPCI("Nakamura") }
		if *uc07PCI != "" {
			pinned := *uc07PCI
			resolveUC07PCI = func() (string, error) { return pinned, nil }
		}

		d.SetRunner(runner.New(runner.Config{
			Driver:   driver,
			Bus:      bus,
			Relay:    rly,
			AuditURL: *auditURL,
			UC07PCI:  resolveUC07PCI,
			History:  runhistory.NewRecorder(histStore, bus, time.Now, log.Printf),
			BFFURL:   stack.BRProviderURL, // "" when no trio
		}))

		b, ok := m.Bundle()
		if !ok {
			// A Reset raced the boot window: Provisioned() fired, then the bundle
			// was cleared before this read. Degraded-until-restart by design —
			// publish skipped probes instead of probing an empty
			// holder id, and let the operator's restart re-run the real Verify.
			// Deliberately does NOT call d.SetVerifyFunc: the restart is the
			// recovery action, and a re-probe against a cleared bundle would lie.
			const detail = "skipped: secrets bundle unavailable (reset during boot)"
			bus.Emit(event.Event{Type: event.TypeVerify, Detail: "verify " + detail})
			d.SetVerify([]bootstrap.Probe{
				{Name: "discovery", Detail: detail},
				{Name: "registration", Detail: detail},
				{Name: "hosted-payer", Detail: detail},
			})
			return
		}
		holderID := b.Manifest.ID
		verifyFn := func(vctx context.Context) []bootstrap.Probe {
			// One probe set for boot and re-probe by construction.
			probes := bootstrap.Verify(vctx, nil, *discoveryURL, holderID, bus)
			// byo-ehr: only when an EHR swap is applied — byoCfg/byoHC are
			// boot-time facts (what this process actually applied), never
			// re-read from a possibly-since-edited byo.json, so a re-probe
			// via POST /api/verify tests the SAME EHR the running gateway
			// child was configured against.
			if e := byoCfg.EHR; e != nil {
				probes = append(probes, byo.ProbeEHR(vctx, byoHC, e.DataURL))
			}
			// secrets-acl: only when restrictSecretsDirWindows actually
			// failed (fail-visible) — a silent no-op on
			// non-Windows/success never adds probe noise.
			if aclErr != nil {
				probes = append(probes, bootstrap.Probe{Name: "secrets-acl", OK: false, Detail: aclErr.Error()})
			}
			return probes
		}
		d.SetVerify(verifyFn(ctx)) // boot result FIRST, then open the re-probe surface —
		d.SetVerifyFunc(verifyFn)  // the reverse order lets a concurrent POST's fresh
		// probes be overwritten by boot's older result
	}()

	fmt.Printf("shnkitd: state=%s secrets=%s bootstrap=%s\n", *stateDir, secretsDir, m.Status().State)

	errCh := make(chan error, 1)
	go func() { errCh <- d.Serve(ctx) }()

	// Launch-time update check: fired
	// once, asynchronously, after the daemon is Ready — it must never block
	// boot, and it is skipped entirely for dev builds (kitVersion ==
	// devVersion), since kit/update.Check itself has no notion of "dev
	// version" (that function's own doc). Every failure (offline, a 404
	// before this Kit has a published release feed, a malformed feed, ...) is logged and
	// discarded — d.SetUpdate is called ONLY on success, so a failed check
	// leaves GET /api/status's "update" key absent, exactly as if no check
	// had run at all (the same key-presence contract as every other Set*
	// field on kitd.Daemon).
	if kitVersion != devVersion {
		go func() {
			select {
			case <-d.Ready():
			case <-ctx.Done():
				return
			}
			// updateCheckTimeout bounds this single call so a hung feed can't
			// leave the goroutine open for the rest of the daemon's lifetime.
			// Generous — an update check is advisory and
			// even less urgent than a POST /api/verify re-probe (kitd.go's
			// verifyTimeout, 15s) — so 30s.
			const updateCheckTimeout = 30 * time.Second
			checkCtx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
			defer cancel()
			info, err := update.Check(checkCtx, http.DefaultClient, *releasesURL, kitVersion)
			if err != nil {
				log.Printf("shnkitd: update check: %v", err)
				return
			}
			d.SetUpdate(info)
		}()
	}

	exitCode := 0
	select {
	case <-sigCh:
		cancel()
		<-errCh // Serve returns after its bounded graceful shutdown
	case err := <-errCh:
		// Serve ended on its own: a bind/serve error, or the boot goroutine's
		// cancel() on a failed BuildStack/child start (in which case err is nil —
		// Serve's ctx-cancel branch returns nil; bootFailed carries the failure).
		if err != nil {
			fmt.Fprintf(os.Stderr, "shnkitd: serve: %v\n", err)
			exitCode = 1
		}
		cancel()
	}
	select {
	case <-bootFailed:
		exitCode = 1 // a boot-triggered shutdown must exit non-zero
	default:
	}
	sup.StopAll()
	os.Exit(exitCode)
}

// restarterFunc adapts a plain func to kitd.Restarter's RestartChild(ctx,
// name) error shape — main wires
// supervisor.Supervisor.Restart directly through it: the supervisor's own
// method is named Restart, not RestartChild, so this is a naming adapter,
// not a behavioral one.
type restarterFunc func(ctx context.Context, name string) error

func (f restarterFunc) RestartChild(ctx context.Context, name string) error { return f(ctx, name) }

// errString returns "" for a nil error, else err.Error() — used to surface
// byo.json's load fail-safe as kitd.BYORuntime.LoadError (a plain
// string, so a nil-vs-non-nil error never needs to cross the kitd package
// boundary).
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// resolveTokenStoreKind implements --token-store's derivation
// (mirroring --fake-validator's flag.Visit pattern): an EXPLICIT
// --token-store always wins; otherwise keychain is the packaged default
// (when --java-assets is set — the packaged Kit ships a real OS keychain)
// and file is the dev default (no keychain expected on a bare dev
// checkout).
func resolveTokenStoreKind(explicit bool, value, javaAssets string) string {
	if explicit {
		return value
	}
	if javaAssets != "" {
		return "keychain"
	}
	return "file"
}

// newTokenStore builds the bootstrap.TokenStore for kind ("keychain" wraps
// fileStore as NewKeyringTokenStore's fail-visible fallback; anything else —
// including "file" and an operator typo — returns fileStore unchanged, fail
// safe rather than silently doing nothing).
func newTokenStore(kind string, fileStore bootstrap.TokenStore, accountsURL string) bootstrap.TokenStore {
	if kind == "keychain" {
		return bootstrap.NewKeyringTokenStore(accountsURL, fileStore)
	}
	return fileStore
}

// validateSecretsAccounts returns a startup error naming BOTH facts when
// secretsFlag (the RAW --secrets flag value, not the {state-dir}/secrets
// derived default) is set to a dir with no loadable bundle AND accountsURL
// is empty: with neither, this Kit can never reach
// StateProvisioned — no persisted bundle to resume from, and no accounts
// URL to sign in fresh against — a silent sign-in-required-forever Kit
// today instead of a startup error an operator can actually act on.
func validateSecretsAccounts(secretsFlag, accountsURL string) error {
	if secretsFlag == "" {
		return nil // the separate --accounts-required-unless---secrets check (above) covers this case
	}
	if _, err := shnsdk.LoadBundle(secretsFlag); err == nil {
		return nil // a loadable bundle needs no --accounts (the pre-provisioned fast path)
	}
	if accountsURL != "" {
		return nil // no bundle yet, but a fresh sign-in is possible
	}
	return fmt.Errorf("shnkitd: --secrets %s has no loadable bundle, and --accounts is empty — provide a valid pre-provisioned bundle or set --accounts so this Kit can sign in", secretsFlag)
}

// openBrowser launches the platform's URL opener for the bootstrap PKCE
// sign-in flow. It is best-effort: if it fails, the operator can still copy
// the authorize URL kitd surfaces via /api/bootstrap/signin. Ported local
// from sdk/cmd/shn/login.go's defaultOpenBrowser — kit cannot import an SDK
// package main.
func openBrowser(u string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	return exec.Command(name, append(args, u)...).Start()
}
