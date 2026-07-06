package bootstrap

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
	"github.com/SmartHealthNetwork/shn-sdk/accounts"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// State is one point in the bootstrap arc.
type State string

const (
	StateSignInRequired State = "signin-required"
	StateSigningIn      State = "signing-in"
	StateProvisioning   State = "provisioning"
	StateProvisioned    State = "provisioned"
)

// signInAgain is the fixed phrase used whenever a session can no longer be
// carried forward and the operator must run SignIn again — tests assert on
// it (Detail "mentioning sign in").
const signInAgain = "session expired — sign in again"

var (
	// ErrSignInInProgress is returned by SignIn while a previous sign-in/
	// provisioning generation is still in flight (kitd maps it to 409).
	ErrSignInInProgress = errors.New("bootstrap: sign-in already in progress")
	// ErrAlreadyProvisioned is returned by SignIn once the Machine is
	// provisioned — reset first (kitd maps it to 409).
	ErrAlreadyProvisioned = errors.New("bootstrap: already provisioned; reset first")
)

// Status is a point-in-time snapshot of the Machine, safe to serialize as
// the UI's GET /api/bootstrap poll response.
type Status struct {
	State      State     `json:"state"`
	Email      string    `json:"email,omitempty"`
	HolderID   string    `json:"holderId,omitempty"`
	AuthExpiry time.Time `json:"authExpiry,omitzero"`
	Detail     string    `json:"detail,omitempty"`
}

// Config configures a Machine. Only AccountsURL/SecretsDir/ClientName/Role/
// RegisterBaseURL are meaningful inputs; the rest have safe defaults for
// production and are overridable for tests.
type Config struct {
	AccountsURL     string
	SecretsDir      string             // bundle home; a loadable bundle here ⇒ StateProvisioned at New
	ClientName      string             // Accounts display name, e.g. "SHN Kit"
	Role            string             // "provider"
	RegisterBaseURL string             // placeholder or gate override
	Tokens          TokenStore         // nil ⇒ no persistence (tests)
	Bus             *event.Bus         // nil ok (no events)
	HTTP            *http.Client       // nil ⇒ http.DefaultClient
	Now             func() time.Time   // nil ⇒ time.Now
	OpenBrowser     func(string) error // nil ⇒ never opens (--no-browser / gate)
	Ports           []int              // nil ⇒ accounts.LoopbackPorts
}

// signInPlan is the outcome of claimSignIn's under-mu decision: which path
// SignIn's remainder (run entirely outside mu) should take.
type signInPlan int

const (
	planStartPKCE signInPlan = iota
	planReuseToken
	planRefreshToken
)

// Machine is the SHN Kit's resumable sign-in/provision state machine: PKCE
// sign-in against the Accounts service, token reuse/refresh fast paths, and
// two-step Accounts client provisioning ending in a written secrets bundle.
//
// Concurrency discipline: mu guards only the small state
// snapshot (state/detail/email/holder/expiry) — it is NEVER held across
// network I/O, so Status() (which backs a UI poll) never blocks on a slow
// Accounts service. Bus.Emit is likewise never called while mu is held (the
// same supervisor discipline used elsewhere in the Kit). Reset()-vs-in-flight-provisioning races are
// serialized by gen (a stale generation aborts and undoes its own local side
// effects) plus inflight (no new generation can even be CLAIMED until the
// stale one has finished undoing them) — see the field comments below.
type Machine struct {
	cfg Config

	mu     sync.Mutex
	state  State
	detail string
	email  string
	holder string
	expiry time.Time

	// gen is bumped by Reset. Every provisioning path snapshots it under mu
	// at the moment it claims its transition (claimSignIn) and threads it
	// through to its commit points; a straggling goroutine whose snapshot no
	// longer matches m.gen aborts instead of undoing an operator's
	// concurrent Reset — Reset must fence against an in-flight sign-in/provision
	// goroutine, not just a settled one. Combined with inflight below,
	// generations are fully serialized: a stale straggler always finishes
	// cleaning up its own artifacts before any newer generation is even
	// allowed to claim a transition, so a stale write can never land after a
	// newer generation has already committed.
	gen uint64

	// flow is the currently-parked PKCE flow, retained so Reset can Close it
	// — guarded by mu. It is set in SignIn immediately
	// after StartPKCE returns (before the flow is exposed to the operator via
	// emit/OpenBrowser) and cleared back to nil by waitAndProvision's
	// deferred cleanup once that flow's Wait has returned, whichever way. A
	// Reset() landing while it is set Closes it so a parked Wait unblocks
	// immediately instead of sitting on the 5-minute bound; a Reset()
	// landing while it is nil (no flow in flight, or the prior one already
	// completed) is a no-op.
	flow *accounts.PKCEFlow

	// inflight is true from the moment claimSignIn claims a transition until
	// that generation's terminal path (successful commit, fail(), or a
	// stale-abort) clears it — under mu, as that path's true last act.
	// Unlike state, inflight is untouched by Reset: a Reset landing while a
	// sign-in/provision is running flips state back to signin-required
	// immediately (so Status() keeps reading correctly), but claimSignIn
	// still rejects a new SignIn with the ordinary "already in progress"
	// error until the straggler has actually finished unwinding —
	// self-clearing in milliseconds, since that unwind is local filesystem
	// work. Without this, gen alone left a real hole: Reset →
	// an operator immediately signs in again → the new
	// generation provisions and commits → the OLD generation's now-stale
	// cleanup then fires and deletes the NEW generation's legitimate bundle.
	inflight bool

	// provisioned is closed exactly once, the moment the Machine first
	// reaches StateProvisioned. It is set once in New and never replaced —
	// not even by Reset: a closed channel cannot be reopened, so a
	// reset Machine relies on a daemon restart to hand out a fresh one.
	// closeProvisioned guards the close with sync.Once so a same-process
	// Reset-then-SignIn-again re-provisioning (which isn't the expected
	// flow, but which nothing in the state machine forbids outright)
	// cannot double-close it and panic.
	provisioned      chan struct{}
	closeProvisioned sync.Once
}

// New constructs a Machine. If cfg.SecretsDir already holds a loadable
// bundle, the Machine starts already provisioned — the common case
// of a shnkitd restart after a prior successful bootstrap.
func New(cfg Config) *Machine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.Ports == nil {
		cfg.Ports = accounts.LoopbackPorts
	}

	m := &Machine{cfg: cfg, provisioned: make(chan struct{})}

	if b, err := shnsdk.LoadBundle(cfg.SecretsDir); err == nil {
		m.state = StateProvisioned
		m.holder = b.Manifest.ID
		close(m.provisioned)
		m.emit("provisioned (existing bundle)")
		return m
	}
	m.state = StateSignInRequired
	return m
}

// Status returns a point-in-time snapshot. It never blocks on network I/O —
// it only reads the mutex-guarded snapshot fields.
func (m *Machine) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{
		State:      m.state,
		Email:      m.email,
		HolderID:   m.holder,
		AuthExpiry: m.expiry,
		Detail:     m.detail,
	}
}

// Provisioned returns a channel that is closed the moment the Machine first
// reaches StateProvisioned. The channel identity is fixed at New and never
// replaced, so reading it requires no synchronization beyond the
// happens-before edge New's return already provides.
func (m *Machine) Provisioned() <-chan struct{} {
	return m.provisioned
}

// Bundle returns the current secrets bundle, ok=false unless the Machine is
// provisioned and the bundle still loads from disk.
func (m *Machine) Bundle() (shnsdk.Bundle, bool) {
	m.mu.Lock()
	provisioned := m.state == StateProvisioned
	m.mu.Unlock()
	if !provisioned {
		return shnsdk.Bundle{}, false
	}
	b, err := shnsdk.LoadBundle(m.cfg.SecretsDir)
	if err != nil {
		return shnsdk.Bundle{}, false
	}
	return b, true
}

// SignIn begins (or resumes via a token fast path) the sign-in →
// provisioning arc. It returns the hosted-UI authorize URL the caller (or a
// --no-browser operator) should visit, or "" when a token-reuse/refresh
// fast path skipped PKCE entirely.
//
// SignIn never holds Machine.mu across network I/O: the state transition is
// claimed synchronously under mu (claimSignIn), then every fetch/exchange
// runs outside it. A concurrent SignIn call while one is already in flight
// is rejected by the claim, not by racing on a fetch.
func (m *Machine) SignIn(ctx context.Context) (string, error) {
	tok, plan, gen, err := m.claimSignIn()
	if err != nil {
		return "", err
	}

	switch plan {
	case planReuseToken:
		m.emit("provisioning: reusing signed-in session")
		go m.provision(ctx, tok, gen)
		return "", nil
	case planRefreshToken:
		go m.refreshAndProvision(ctx, tok, gen)
		return "", nil
	}

	m.emit("signing-in: fetching accounts configuration")
	cliCfg, err := accounts.FetchCLIConfig(ctx, m.cfg.HTTP, m.cfg.AccountsURL)
	if err != nil {
		m.fail(fmt.Errorf("fetch accounts configuration: %w", err), gen)
		return "", err
	}
	oidc, err := accounts.FetchOIDC(ctx, m.cfg.HTTP, cliCfg.Issuer)
	if err != nil {
		m.fail(fmt.Errorf("fetch OIDC discovery: %w", err), gen)
		return "", err
	}
	flow, err := accounts.StartPKCE(m.cfg.HTTP, cliCfg, oidc, m.cfg.Ports, m.cfg.Now)
	if err != nil {
		m.fail(fmt.Errorf("start PKCE flow: %w", err), gen)
		return "", err
	}

	// Retain the flow so a concurrent Reset can Close it —
	// with a staleness check in the SAME critical section, immediately after
	// StartPKCE returns and BEFORE the flow is exposed to the operator via
	// emit/OpenBrowser: a raced Reset must not pop the
	// operator's browser for a dead flow. A Reset landing between
	// claimSignIn and this point would otherwise never see m.flow at all —
	// so the staleness check must live here, in this narrower window, not
	// just at the top of the function.
	m.mu.Lock()
	if m.gen != gen {
		m.mu.Unlock()
		flow.Close() // Reset raced the claim: release the listener NOW…
		err := errors.New("bootstrap: reset during sign-in")
		m.fail(err, gen) // …and unwind via the stale branch (cleanup + finish releases the latch)
		return "", err
	}
	m.flow = flow
	m.mu.Unlock()

	m.emit("signing-in: waiting for browser sign-in")
	if m.cfg.OpenBrowser != nil {
		_ = m.cfg.OpenBrowser(flow.AuthorizeURL())
	}
	go m.waitAndProvision(ctx, flow, gen)
	return flow.AuthorizeURL(), nil
}

// claimSignIn validates the current state and, if it permits starting,
// claims the transition (signing-in for a fresh PKCE run, provisioning for
// either token fast path) before releasing the lock — the claim itself is
// what makes a concurrent SignIn bounce, not anything that happens after.
//
// The inflight check runs BEFORE the state switch and independently of it:
// state alone is not enough, because Reset() flips
// state back to signin-required immediately even while a straggling
// goroutine from the previous generation is still unwinding. Rejecting on
// inflight — not state — is what actually serializes generations.
func (m *Machine) claimSignIn() (accounts.Tokens, signInPlan, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.inflight {
		return accounts.Tokens{}, 0, 0, ErrSignInInProgress
	}
	if m.state == StateProvisioned {
		return accounts.Tokens{}, 0, 0, ErrAlreadyProvisioned
	}

	m.inflight = true
	tok, hasTok := m.loadTokens()
	now := m.cfg.Now()
	switch {
	case hasTok && tok.Expiry.After(now):
		m.state = StateProvisioning
		m.detail = ""
		return tok, planReuseToken, m.gen, nil
	case hasTok && tok.RefreshToken != "":
		m.state = StateProvisioning
		m.detail = ""
		return tok, planRefreshToken, m.gen, nil
	default:
		m.state = StateSigningIn
		m.detail = ""
		return accounts.Tokens{}, planStartPKCE, m.gen, nil
	}
}

// waitAndProvision runs in its own goroutine after a fresh PKCE flow starts.
// It bounds the browser wait to 5 minutes so an operator who
// never completes sign-in doesn't leave the flow (and its bound loopback
// listener) parked forever, then always releases the flow's listener.
func (m *Machine) waitAndProvision(ctx context.Context, flow *accounts.PKCEFlow, gen uint64) {
	defer func() {
		flow.Close()
		m.mu.Lock()
		if m.flow == flow {
			m.flow = nil
		}
		m.mu.Unlock()
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	tok, err := flow.Wait(waitCtx)
	if err != nil {
		m.fail(fmt.Errorf("complete sign-in: %w", err), gen)
		return
	}
	m.provision(ctx, tok, gen)
}

// refreshAndProvision exchanges an expired token's refresh token for a fresh
// one (re-discovering the token endpoint, since it is not itself persisted)
// and, on success, continues into provision.
func (m *Machine) refreshAndProvision(ctx context.Context, t accounts.Tokens, gen uint64) {
	m.emit("provisioning: refreshing session")
	refreshed, err := m.refreshTokens(ctx, t)
	if err != nil {
		m.fail(fmt.Errorf("%s: %w", signInAgain, err), gen)
		return
	}
	m.provision(ctx, refreshed, gen)
}

// refreshTokens re-fetches cli-config/OIDC discovery to learn the token
// endpoint + client id (neither is persisted with the tokens) and exchanges
// old.RefreshToken for a fresh Tokens.
func (m *Machine) refreshTokens(ctx context.Context, old accounts.Tokens) (accounts.Tokens, error) {
	cliCfg, err := accounts.FetchCLIConfig(ctx, m.cfg.HTTP, m.cfg.AccountsURL)
	if err != nil {
		return accounts.Tokens{}, err
	}
	oidc, err := accounts.FetchOIDC(ctx, m.cfg.HTTP, cliCfg.Issuer)
	if err != nil {
		return accounts.Tokens{}, err
	}
	return accounts.Refresh(ctx, m.cfg.HTTP, oidc.TokenEndpoint, cliCfg.ClientID, old.RefreshToken, m.cfg.Now)
}

// provision runs the two-step Accounts registration (POST /clients then
// POST /clients/{id}/pop), writes the resulting secrets bundle, persists the
// tokens, and marks the Machine provisioned. Callers must already be in
// StateProvisioning.
func (m *Machine) provision(ctx context.Context, t accounts.Tokens, gen uint64) {
	if !t.Expiry.After(m.cfg.Now()) {
		if t.RefreshToken == "" {
			m.fail(errors.New(signInAgain), gen)
			return
		}
		refreshed, err := m.refreshTokens(ctx, t)
		if err != nil {
			m.fail(fmt.Errorf("%s: %w", signInAgain, err), gen)
			return
		}
		t = refreshed
	}

	m.emit("provisioning: registering client")
	ident, err := shnsdk.GenerateIdentity(placeholderID(m.cfg.ClientName))
	if err != nil {
		m.fail(fmt.Errorf("generate identity: %w", err), gen)
		return
	}

	encPub := base64.StdEncoding.EncodeToString(ident.EncPub[:])
	signPub := base64.StdEncoding.EncodeToString(ident.SignPub)
	client := accounts.NewClient(m.cfg.AccountsURL, t.IDToken).WithHTTP(m.cfg.HTTP)

	assignedID, err := client.Create(ctx, m.cfg.ClientName, m.cfg.Role, encPub, signPub, m.cfg.RegisterBaseURL)
	if err != nil {
		m.fail(fmt.Errorf("register client: %w", err), gen)
		return
	}
	// Set the server-assigned id BEFORE building the PoP so the proof signs it.
	ident.HolderID = assignedID

	m.emit("provisioning: submitting proof of possession")
	reg := ident.Registration(m.cfg.Role, m.cfg.RegisterBaseURL)
	if err := client.SubmitPoP(ctx, assignedID, reg); err != nil {
		m.fail(fmt.Errorf("submit proof of possession: %w", err), gen)
		return
	}

	// A Reset() may have fired while Create/SubmitPoP were in flight —
	// nothing else fences a straggling provision() against a concurrent Reset.
	// Check the generation now, immediately before the first irreversible
	// local side effect, so a straggler can't recreate the secrets dir a
	// Reset just removed. Nothing local has happened yet, so there is
	// nothing for cleanupStraggler to undo here — just release the latch.
	if m.stale(gen) {
		m.finish()
		return
	}
	if err := shnsdk.WriteBundle(m.cfg.SecretsDir, ident, m.cfg.Role, m.cfg.RegisterBaseURL); err != nil {
		m.fail(fmt.Errorf("write secrets bundle: %w", err), gen)
		return
	}

	// Re-check immediately after WriteBundle: a Reset landing between the
	// check above and here (i.e. during WriteBundle's own I/O) must not
	// leave this straggler's freshly written
	// bundle on disk. Unlike the check above, staleness detected HERE means
	// a local side effect has already run with no committed state behind
	// it, so cleanupStraggler undoes it: the prior version of this fence
	// merely returned here, and New() derives StateProvisioned from bundle
	// PRESENCE alone, so that stray bundle would have silently re-armed
	// "provisioned" on the daemon's next restart — the exact outcome Reset
	// exists to prevent, relocated to a race+restart rather than fixed.
	// finish() only runs once cleanup is done: with generations serialized,
	// no newer generation can even be claimed until this straggler's own
	// artifacts are gone.
	if m.stale(gen) {
		m.cleanupStraggler()
		m.finish()
		return
	}
	if err := m.saveTokens(t); err != nil {
		m.fail(fmt.Errorf("save session: %w", err), gen)
		return
	}

	email := accounts.EmailFromIDToken(t.IDToken)
	m.mu.Lock()
	if m.gen != gen {
		m.mu.Unlock()
		// Reset landed between the saveTokens call above and this final
		// commit (e.g. during saveTokens' own I/O) — the straggler's bundle
		// and its just-written token file are both its own stray side
		// effects; undo both. This can never race a NEWER generation's
		// legitimate commit: nothing can even claim a
		// fresh SignIn until finish() below actually releases the latch,
		// which happens only after cleanupStraggler returns. A Reset
		// immediately followed by a new SignIn simply sees "already in
		// progress" for the remainder of this straggler's lifetime —
		// milliseconds of local filesystem work — then proceeds normally
		// once it has unwound. The only Reset that can land AFTER this read
		// passes is an ordinary later one racing a provision() that
		// legitimately finished first, the normal linearization point.
		m.cleanupStraggler()
		m.finish()
		return
	}
	m.state = StateProvisioned
	m.holder = assignedID
	m.email = email
	m.expiry = t.Expiry
	m.detail = ""
	m.mu.Unlock()

	m.closeProvisioned.Do(func() { close(m.provisioned) })
	m.emit("provisioned")
	m.finish()
}

// stale reports whether gen no longer matches the Machine's current
// generation counter — i.e. a Reset() has run since this provisioning
// attempt claimed its transition in claimSignIn.
func (m *Machine) stale(gen uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen != gen
}

// cleanupStraggler undoes a straggling provision()'s own local side effects
// — the secrets bundle and/or the persisted tokens — once a concurrent
// Reset() has fenced it after WriteBundle already ran.
// Called from every abort path that runs after WriteBundle succeeded,
// including fail()'s stale branch: without this, a
// saveTokens error racing a concurrent Reset would leave the bundle
// stranded there. Because generations are serialized by the
// inflight latch, cleanupStraggler can never race a NEWER generation's
// artifacts: no later generation can even be claimed until this straggler's
// caller has released the latch via finish(), which runs only after
// cleanupStraggler returns. Both os.RemoveAll and Tokens.Clear are
// idempotent, so this is safe to call whether or not each side effect
// actually ran yet, and nil-safe (Clear no-ops when cfg.Tokens is nil).
// Callers must not hold mu — this performs file I/O.
func (m *Machine) cleanupStraggler() {
	_ = os.RemoveAll(m.cfg.SecretsDir)
	_ = m.clearTokens()
}

// finish clears the inflight latch under mu. It must run as the TRUE LAST
// ACT of every provisioning goroutine's terminal path — successful commit,
// fail() (either branch), or a stale-abort in provision() — so that
// claimSignIn keeps rejecting a new SignIn for exactly as long as this
// generation (including its own cleanup, if it was a straggler) is still
// unwinding, regardless of what state already reads.
// Callers must not hold mu.
func (m *Machine) finish() {
	m.mu.Lock()
	m.inflight = false
	m.mu.Unlock()
}

// fail rolls the Machine back to StateSignInRequired with err's message as
// Detail, and emits the failure. Caller must not hold mu. gen guards the
// STATE write: if a Reset() has bumped m.gen since this provisioning
// attempt claimed its transition, fail must not stomp state a concurrent
// Reset already committed. But a stale call can still follow
// a successful WriteBundle — e.g. a saveTokens error whose TokenStore
// implementation itself triggers (or merely races) a concurrent Reset — so
// the stale branch runs cleanupStraggler before releasing the latch:
// otherwise it would strand the bundle the same way as the case above.
// Either way, finish()
// is the true last act.
func (m *Machine) fail(err error, gen uint64) {
	m.mu.Lock()
	stale := m.gen != gen
	if !stale {
		m.state = StateSignInRequired
		m.detail = err.Error()
	}
	m.mu.Unlock()

	if stale {
		m.cleanupStraggler()
		m.finish()
		return
	}
	m.emit(fmt.Sprintf("bootstrap failed: %v", err))
	m.finish()
}

// Reset clears any persisted tokens and the secrets dir, then returns the
// Machine to StateSignInRequired. The Provisioned() channel is deliberately
// NOT reopened (a closed channel cannot be) — a caller must restart shnkitd
// to obtain a fresh Machine. Reset does NOT touch inflight: a
// straggling goroutine from before this Reset (if any) keeps running under
// its own stale gen snapshot and is serialized purely by that latch, not by
// anything Reset itself does — claimSignIn is what makes an immediate
// re-SignIn wait for it. What Reset DOES do now is
// Close() any currently-retained PKCE flow: Close is
// idempotent, so this is a no-op once a flow has already completed on its
// own, but for a flow genuinely parked in Wait it unblocks that Wait
// immediately — driving the straggling waitAndProvision goroutine into its
// stale-gen fail() path (cleanup + finish) right away instead of leaving it
// parked up to the 5-minute bound.
func (m *Machine) Reset() error {
	if err := m.clearTokens(); err != nil {
		return fmt.Errorf("bootstrap: clear tokens: %w", err)
	}
	if err := os.RemoveAll(m.cfg.SecretsDir); err != nil {
		return fmt.Errorf("bootstrap: remove secrets dir: %w", err)
	}

	const detail = "reset — restart shnkitd to complete"
	m.mu.Lock()
	m.gen++
	m.state = StateSignInRequired
	m.detail = detail
	m.holder = ""
	m.email = ""
	m.expiry = time.Time{}
	f := m.flow
	m.flow = nil
	m.mu.Unlock()

	if f != nil {
		f.Close() // idempotent; unblocks the parked Wait → stale-gen fail() → cleanup + finish()
	}
	m.emit(detail)
	return nil
}

// loadTokens/saveTokens/clearTokens are nil-safe wrappers over cfg.Tokens
// (nil ⇒ no persistence, the test default).

func (m *Machine) loadTokens() (accounts.Tokens, bool) {
	if m.cfg.Tokens == nil {
		return accounts.Tokens{}, false
	}
	return m.cfg.Tokens.Load()
}

func (m *Machine) saveTokens(t accounts.Tokens) error {
	if m.cfg.Tokens == nil {
		return nil
	}
	return m.cfg.Tokens.Save(t)
}

func (m *Machine) clearTokens() error {
	if m.cfg.Tokens == nil {
		return nil
	}
	return m.cfg.Tokens.Clear()
}

// emit is a nil-safe Bus.Emit wrapper. Callers must not hold mu.
func (m *Machine) emit(detail string) {
	if m.cfg.Bus == nil {
		return
	}
	m.cfg.Bus.Emit(event.Event{Type: event.TypeBootstrap, Detail: detail})
}

// placeholderID sanitizes name into a valid seed identity id. The server
// assigns the real holder id on registration (Create); this is only the
// local, throwaway placeholder GenerateIdentity needs before that happens.
func placeholderID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "pending"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
