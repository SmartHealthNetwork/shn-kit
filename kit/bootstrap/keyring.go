package bootstrap

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"

	"github.com/SmartHealthNetwork/shn-sdk/accounts"
)

// keyringService is the OS keychain/Credential Manager service name every
// keyringTokenStore registers under.
const keyringService = "SHN Kit"

// maxRefreshTokenBytes defensively bounds the value Save will even attempt
// to hand the OS keyring: Windows Credential Manager (wincred) caps a
// credential blob at roughly 2.5KB, and Cognito refresh tokens alone run
// close to 2KB — not much headroom. Save rejects anything over
// this bound with the named ErrRefreshTokenTooLarge BEFORE ever calling into
// wincred, so an oversized value fails with a clear Go error instead of a
// wincred-specific mystery.
const maxRefreshTokenBytes = 2000

// ErrRefreshTokenTooLarge is returned by Save when the refresh token exceeds
// maxRefreshTokenBytes — a real bug (e.g. some
// other token mistakenly threaded through as a refresh token), not an
// environmental keychain outage, so it is a hard failure rather than a
// fallback trigger.
var ErrRefreshTokenTooLarge = errors.New("bootstrap: refresh token too large for OS keychain")

// keyringTokenStore is an OS-keychain-backed TokenStore:
// it persists ONLY the refresh token (see maxRefreshTokenBytes), scoped by
// accounts URL exactly like fileTokenStore (same trimmed-URL account name),
// and falls back to a caller-provided TokenStore whenever the OS keyring
// itself is unavailable — never silently: Detail() always names which
// backend the caller's result actually came from and, when it didn't come
// from the keychain, why.
//
// # Semantics table
//
// "keychain: ok/miss/err" is the OS keyring call's own outcome for THIS call
// ("miss" only applies to Load: keyring.ErrNotFound, or an explicit empty
// secret — never-saved under this account, or a store scoped to a DIFFERENT
// accounts URL; "err" is any OTHER keyring error — locked, daemon down,
// etc.). "fallback: ok/miss/err" is s.fallback's own outcome for the SAME
// call (Load's TokenStore.Load has no error, only ok=false; Save/Clear's
// fallback can itself error).
//
// Save/Load follow OR semantics: the call succeeds if EITHER backend can
// serve it, because the caller's actual goal is "give me a usable token by
// any means." Clear follows AND semantics instead: the call only fully
// succeeds if BOTH backends are cleared, because leaving a stale token
// behind in EITHER store (even one the caller no longer reads from) is
// itself the failure mode Clear exists to prevent.
//
//	Save(t):
//	  keychain ok                    → nil,         Detail "keychain"
//	    (best-effort s.fallback.Clear() also runs here — see the "stale
//	    fallback" note below; its own error is ignored and never affects
//	    Detail or the return, since the keychain Save itself succeeded.)
//	  keychain err, fallback ok      → nil,         Detail "file (keychain unavailable: <kErr>)"
//	  keychain err, fallback err     → error (both),Detail "unavailable (keychain: <kErr>; file: <fErr>)"
//	  (ErrRefreshTokenTooLarge short-circuits before any of this — a bug, not
//	  an outage — and never touches Detail or either backend.)
//
//	  Stale-fallback fix: once a Save reaches the OS
//	  keychain successfully, any earlier outage-era plaintext token left in
//	  s.fallback from a PRIOR Save that had degraded to it is no longer the
//	  truth and must not keep lingering on disk indefinitely — a later
//	  keychain MISS (e.g. a differently-scoped read, or the keychain entry
//	  being removed out-of-band) would otherwise union-resurrect that older,
//	  stale token via Load's fallback-on-miss path. So a successful keychain
//	  Save now best-effort clears the fallback too. This is intentionally
//	  NOT the same as Clear()'s AND semantics: Save's own success does not
//	  depend on the fallback clear, so its error is swallowed here.
//
//	Load():
//	  keychain ok (found)            → tokens,true, Detail "keychain"
//	  keychain miss, fallback ok     → tokens,true, Detail "file (keychain entry missing)"
//	  keychain miss, fallback miss   → {},false,    Detail "keychain" (nothing anywhere — e.g. fresh install)
//	  keychain err,  fallback ok     → tokens,true, Detail "file (keychain unavailable: <kErr>)"
//	  keychain err,  fallback miss   → {},false,    Detail "keychain unavailable: <kErr> (file empty too)"
//
//	  CRITICAL fix: the "keychain miss, fallback ok" row
//	  previously returned {},false with Detail "keychain" — i.e. a keychain
//	  miss was NEVER union-checked against the fallback at all. That silently
//	  stranded a token that Save had written to the fallback during a prior
//	  keychain outage: once the keychain recovered, Load would keyring.Get an
//	  entry that was never actually written there, get ErrNotFound, and
//	  discard the real fallback token with Detail falsely reading "keychain".
//	  Load now always consults the fallback on a miss, exactly like it already
//	  did on an error.
//
//	Clear(): (the fallback is unconditionally attempted too, regardless of
//	  the keychain outcome — Save may have degraded to it during a prior
//	  outage, so Clear must remove whichever store(s) actually hold state.
//	  keyring.ErrNotFound on Delete is normalized to "keychain ok", same as
//	  Load/Save: an already-absent entry is not a failure.)
//	  keychain ok, fallback ok       → nil,          Detail "keychain"
//	  keychain ok, fallback err      → fErr,         Detail "file (clear failed: <fErr>)"
//	  keychain err, fallback ok      → kErr,         Detail "keychain unavailable: <kErr> (file cleared)"
//	  keychain err, fallback err     → error (both), Detail "keychain unavailable: <kErr>; file clear also failed: <fErr>"
//
//	  IMPORTANT fix: the "keychain ok, fallback err" row
//	  previously set Detail to "keychain" (the healthy-primary string) while
//	  RETURNING the fallback's error — the surfaced error's source contradicted
//	  Detail. And the "keychain err, fallback ok" row previously swallowed the
//	  keychain failure entirely (returned nil), which doesn't hold up under
//	  Clear's AND semantics: a keychain delete that genuinely errored (not
//	  ErrNotFound) may have left a stale entry behind in the OS keychain even
//	  though the fallback file was successfully wiped, so that row now also
//	  returns a real error. Every row's Detail now names whichever store(s)
//	  actually failed, matching the error actually returned.
type keyringTokenStore struct {
	account  string // trimmed accounts URL — the keyring "user" (same scoping rule as fileTokenStore)
	fallback TokenStore

	mu     sync.Mutex
	detail string
}

// NewKeyringTokenStore builds a TokenStore backed by the OS keychain/
// Credential Manager, scoped to accountsURL (trailing-slash-trimmed, same
// rule as NewFileTokenStore) and falling back to fallback whenever the OS
// keyring is unavailable on Save/Load/Clear (see keyringTokenStore's
// semantics table for the full per-op behavior). The returned TokenStore also
// implements interface{ Detail() string }, reporting "keychain" when the OS
// backend served the result, or a "file (...)" / "keychain unavailable: ..."
// string naming the fallback and/or the reason otherwise.
func NewKeyringTokenStore(accountsURL string, fallback TokenStore) TokenStore {
	return &keyringTokenStore{
		account:  strings.TrimRight(accountsURL, "/"),
		fallback: fallback,
		detail:   "keychain",
	}
}

// Detail reports which backend the caller's most recent Save/Load/Clear
// result actually came from (or, for Clear, which store(s) failed) — see
// keyringTokenStore's semantics table. Safe for concurrent use.
func (s *keyringTokenStore) Detail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detail
}

func (s *keyringTokenStore) setDetail(d string) {
	s.mu.Lock()
	s.detail = d
	s.mu.Unlock()
}

// Save persists ONLY t.RefreshToken to the OS keyring under keyringService/
// s.account. An oversized refresh token is rejected outright — never handed
// to the OS keyring at all (ErrRefreshTokenTooLarge) — rather than falling
// back, since it signals a real bug rather than an environmental keychain
// outage. Any OTHER keyring error falls back to s.fallback (OR semantics: the
// call succeeds if either backend accepts it) and surfaces the reason via
// Detail(); if the fallback ALSO fails, that failure is returned to the
// caller and Detail names both.
//
// On a successful keychain Save, s.fallback is best-effort cleared too:
// a prior outage-era Save may have left a stale
// plaintext refresh token sitting in the fallback, and once the keychain
// holds the current token that stale copy must not keep lingering on disk —
// a later keychain miss would otherwise union-resurrect it via Load's
// fallback-on-miss path. The clear's own error is ignored; Save already
// succeeded via the keychain, so it is not reflected in the return value or
// Detail.
func (s *keyringTokenStore) Save(t accounts.Tokens) error {
	if len(t.RefreshToken) > maxRefreshTokenBytes {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrRefreshTokenTooLarge, len(t.RefreshToken), maxRefreshTokenBytes)
	}
	kErr := keyring.Set(keyringService, s.account, t.RefreshToken)
	if kErr == nil {
		_ = s.fallback.Clear() // best-effort; ignore error — Save succeeded via the keychain
		s.setDetail("keychain")
		return nil
	}
	if fErr := s.fallback.Save(t); fErr != nil {
		s.setDetail(fmt.Sprintf("unavailable (keychain: %v; file: %v)", kErr, fErr))
		return fmt.Errorf("keychain unavailable: %w; file store also failed: %w", kErr, fErr)
	}
	s.setDetail(fmt.Sprintf("file (keychain unavailable: %v)", kErr))
	return nil
}

// Load returns Tokens{RefreshToken: ...} with a zero Expiry — the existing
// bootstrap state machine's claimSignIn/provision already treat that shape
// as expired-access, refresh-on-use (bootstrap.go:311-318).
//
// Load follows OR semantics (see keyringTokenStore's semantics table): it
// consults the fallback whenever the keychain does NOT hand back a usable
// token — on an ordinary miss (keyring.ErrNotFound, or an explicit empty
// secret) exactly as much as on a genuine keyring error — because a miss can
// mean either "nothing was ever saved" OR "Save degraded to the fallback
// during a past outage that has since recovered"; Load cannot tell those
// apart from the keychain's answer alone, so it must check regardless.
func (s *keyringTokenStore) Load() (accounts.Tokens, bool) {
	secret, err := keyring.Get(keyringService, s.account)
	switch {
	case err == nil && secret != "":
		s.setDetail("keychain")
		return accounts.Tokens{RefreshToken: secret}, true
	case err == nil || errors.Is(err, keyring.ErrNotFound):
		if tok, ok := s.fallback.Load(); ok {
			s.setDetail("file (keychain entry missing)")
			return tok, true
		}
		s.setDetail("keychain")
		return accounts.Tokens{}, false
	default:
		if tok, ok := s.fallback.Load(); ok {
			s.setDetail(fmt.Sprintf("file (keychain unavailable: %v)", err))
			return tok, true
		}
		s.setDetail(fmt.Sprintf("keychain unavailable: %v (file empty too)", err))
		return accounts.Tokens{}, false
	}
}

// Clear removes the OS keyring entry (an already-absent entry, i.e.
// keyring.ErrNotFound, is normalized to success — mirroring TokenStore's
// contract) AND unconditionally clears the fallback store too: Save may have
// degraded to it during a prior keychain outage, so Clear must remove
// whichever store(s) actually hold state.
//
// Unlike Save/Load, Clear follows AND semantics (see keyringTokenStore's
// semantics table): it only fully succeeds if BOTH backends clear cleanly,
// because a token left behind in EITHER store — even one the caller isn't
// currently reading from — is exactly the failure Clear exists to prevent.
// The returned error and Detail() always name whichever store(s) actually
// failed: naming only "keychain" would imply full success while masking a
// fallback failure that is the error actually being returned, and would let
// a genuine keychain-delete failure go silently swallowed whenever the
// fallback happened to clear cleanly.
func (s *keyringTokenStore) Clear() error {
	kErr := keyring.Delete(keyringService, s.account)
	if errors.Is(kErr, keyring.ErrNotFound) {
		kErr = nil
	}
	fErr := s.fallback.Clear()

	switch {
	case kErr == nil && fErr == nil:
		s.setDetail("keychain")
		return nil
	case kErr == nil:
		s.setDetail(fmt.Sprintf("file (clear failed: %v)", fErr))
		return fErr
	case fErr == nil:
		s.setDetail(fmt.Sprintf("keychain unavailable: %v (file cleared)", kErr))
		return fmt.Errorf("keychain unavailable: %w", kErr)
	default:
		s.setDetail(fmt.Sprintf("keychain unavailable: %v; file clear also failed: %v", kErr, fErr))
		return fmt.Errorf("keychain unavailable: %w; file clear also failed: %w", kErr, fErr)
	}
}

var (
	_ TokenStore                   = (*keyringTokenStore)(nil)
	_ interface{ Detail() string } = (*keyringTokenStore)(nil)
)
