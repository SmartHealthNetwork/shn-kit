package bootstrap

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/SmartHealthNetwork/shn-sdk/accounts"
)

// fakeFallback is an in-memory TokenStore stand-in for tests that exercise
// keyringTokenStore's fallback path — it tracks call counts so a test can
// assert the fallback was actually REACHED, not merely that no error
// surfaced.
type fakeFallback struct {
	saved      accounts.Tokens
	saveErr    error
	loadTok    accounts.Tokens
	loadOK     bool
	clearErr   error
	saveCalls  int
	loadCalls  int
	clearCalls int
}

func (f *fakeFallback) Save(t accounts.Tokens) error {
	f.saveCalls++
	f.saved = t
	return f.saveErr
}

func (f *fakeFallback) Load() (accounts.Tokens, bool) {
	f.loadCalls++
	return f.loadTok, f.loadOK
}

func (f *fakeFallback) Clear() error {
	f.clearCalls++
	return f.clearErr
}

func detailOf(t *testing.T, store TokenStore) string {
	t.Helper()
	d, ok := store.(interface{ Detail() string })
	if !ok {
		t.Fatal("store does not implement interface{ Detail() string }")
	}
	return d.Detail()
}

func TestKeyringTokenStore_RoundTrip(t *testing.T) {
	keyring.MockInit()
	store := NewKeyringTokenStore("https://accounts.example.org", &fakeFallback{})

	want := accounts.Tokens{
		IDToken:      "id-1", // NOT persisted by the keyring backend
		AccessToken:  "at-1", // NOT persisted either
		RefreshToken: "rt-1",
		Expiry:       time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC), // NOT persisted
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := store.Load()
	if !ok {
		t.Fatal("Load: ok = false, want true")
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("Load.RefreshToken = %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if !got.Expiry.IsZero() {
		t.Errorf("Load.Expiry = %v, want zero (refresh-on-use via bootstrap.go's claimSignIn/provision)", got.Expiry)
	}
	if got.IDToken != "" || got.AccessToken != "" {
		t.Errorf("Load = %+v, want only RefreshToken populated (wincred blob cap)", got)
	}
	if d := detailOf(t, store); d != "keychain" {
		t.Errorf("Detail = %q, want %q", d, "keychain")
	}
}

// TestKeyringTokenStore_ScopedToAccountsURL mirrors
// TestFileTokenStore_ScopedToAccountsURL (tokens_test.go): repointing
// --accounts must never refresh/reuse tokens saved under a different pool
// (same scoping rule as the file store).
func TestKeyringTokenStore_ScopedToAccountsURL(t *testing.T) {
	keyring.MockInit()
	a := NewKeyringTokenStore("https://accounts.example.org", &fakeFallback{})
	if err := a.Save(accounts.Tokens{RefreshToken: "rt-a"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b := NewKeyringTokenStore("https://accounts.other.org", &fakeFallback{})
	if _, ok := b.Load(); ok {
		t.Error("Load from a store scoped to a different accounts URL: ok = true, want false")
	}
	if d := detailOf(t, b); d != "keychain" {
		t.Errorf("Detail on a scoping miss = %q, want %q (a miss is not a keychain failure)", d, "keychain")
	}

	// A trailing slash must not defeat the scoping match.
	trimmed := NewKeyringTokenStore("https://accounts.example.org/", &fakeFallback{})
	if _, ok := trimmed.Load(); !ok {
		t.Error("Load with a trailing-slash-equivalent accounts URL: ok = false, want true")
	}

	if _, ok := a.Load(); !ok {
		t.Error("Load from the original scoping: ok = false, want true")
	}
}

func TestKeyringTokenStore_Clear(t *testing.T) {
	keyring.MockInit()
	store := NewKeyringTokenStore("https://accounts.example.org", &fakeFallback{})
	if err := store.Save(accounts.Tokens{RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok := store.Load(); ok {
		t.Error("Load after Clear: ok = true, want false")
	}
	// Clearing an already-cleared store is not an error (TokenStore's
	// contract, mirrored from fileTokenStore).
	if err := store.Clear(); err != nil {
		t.Errorf("Clear on an empty store: %v", err)
	}
}

func TestKeyringTokenStore_SaveErrorFallsBackAndSurfacesDetail(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	fb := &fakeFallback{}
	store := NewKeyringTokenStore("https://accounts.example.org", fb)

	tok := accounts.Tokens{RefreshToken: "rt-1"}
	if err := store.Save(tok); err != nil {
		t.Fatalf("Save (should succeed via fallback): %v", err)
	}
	if fb.saveCalls != 1 {
		t.Errorf("fallback Save calls = %d, want 1 (fallback must actually be USED, not just non-erroring)", fb.saveCalls)
	}
	if fb.saved.RefreshToken != tok.RefreshToken {
		t.Errorf("fallback saved %+v, want RefreshToken %q", fb.saved, tok.RefreshToken)
	}
	d := detailOf(t, store)
	if !strings.Contains(d, "file") || !strings.Contains(d, "keychain unavailable") || !strings.Contains(d, "keychain locked") {
		t.Errorf("Detail = %q, want it to name the fallback AND the reason", d)
	}
}

func TestKeyringTokenStore_LoadErrorFallsBack(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	fb := &fakeFallback{loadTok: accounts.Tokens{RefreshToken: "rt-fallback"}, loadOK: true}
	store := NewKeyringTokenStore("https://accounts.example.org", fb)

	got, ok := store.Load()
	if !ok {
		t.Fatal("Load (should succeed via fallback): ok = false, want true")
	}
	if got.RefreshToken != "rt-fallback" {
		t.Errorf("Load = %+v, want the fallback's tokens", got)
	}
	if fb.loadCalls != 1 {
		t.Errorf("fallback Load calls = %d, want 1", fb.loadCalls)
	}
	d := detailOf(t, store)
	if !strings.Contains(d, "file") || !strings.Contains(d, "keychain unavailable") {
		t.Errorf("Detail = %q, want a fallback reason", d)
	}
}

// TestKeyringTokenStore_ClearKeychainErrFallbackOK covers Clear's
// keychain-err/fallback-ok row (semantics table, keyring.go). Clear follows
// AND semantics: the fallback IS cleared (a Clear must not leave a token
// behind in either store), but the keychain delete's own failure means a
// stale entry may still sit in the OS keychain, so Clear surfaces that as a
// real, caller-visible error rather than silently reporting success.
func TestKeyringTokenStore_ClearKeychainErrFallbackOK(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	fb := &fakeFallback{}
	store := NewKeyringTokenStore("https://accounts.example.org", fb)

	err := store.Clear()
	if err == nil {
		t.Fatal("Clear with an erroring keychain: err = nil, want a non-nil error naming the keychain failure")
	}
	if !strings.Contains(err.Error(), "keychain locked") {
		t.Errorf("Clear error = %v, want it to name the keychain failure", err)
	}
	if fb.clearCalls != 1 {
		t.Errorf("fallback Clear calls = %d, want 1 (the fallback must still be cleared)", fb.clearCalls)
	}
	d := detailOf(t, store)
	if !strings.Contains(d, "keychain locked") {
		t.Errorf("Detail = %q, want it to name the keychain failure", d)
	}
}

// TestKeyringTokenStore_ClearKeychainOKFallbackErr covers Clear's
// keychain-ok/fallback-err row: the review's IMPORTANT finding was exactly
// this combo — Detail previously read "keychain" (implying full success)
// while Clear actually returned the fallback's error. Detail must instead
// name the store that actually failed (the file/fallback), matching the
// returned error's source.
func TestKeyringTokenStore_ClearKeychainOKFallbackErr(t *testing.T) {
	keyring.MockInit()
	fb := &fakeFallback{clearErr: errors.New("disk full")}
	store := NewKeyringTokenStore("https://accounts.example.org", fb)

	err := store.Clear()
	if err == nil {
		t.Fatal("Clear with keychain ok but fallback erroring: err = nil, want the fallback's error")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("Clear error = %v, want it to name the fallback's failure", err)
	}
	d := detailOf(t, store)
	if d == "keychain" {
		t.Errorf("Detail = %q, must not read healthy %q when Clear actually failed (fallback error)", d, "keychain")
	}
	if !strings.Contains(d, "file") || !strings.Contains(d, "disk full") {
		t.Errorf("Detail = %q, want it to name the file/fallback failure", d)
	}
}

// TestKeyringTokenStore_ClearBothErr covers Clear's both-erroring row
// (missing combo test named by the review).
func TestKeyringTokenStore_ClearBothErr(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	fb := &fakeFallback{clearErr: errors.New("disk full")}
	store := NewKeyringTokenStore("https://accounts.example.org", fb)

	err := store.Clear()
	if err == nil {
		t.Fatal("Clear with both keychain and fallback erroring: err = nil, want a non-nil error")
	}
	if !strings.Contains(err.Error(), "keychain locked") || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("Clear error = %v, want it to name BOTH failures", err)
	}
	if fb.clearCalls != 1 {
		t.Errorf("fallback Clear calls = %d, want 1", fb.clearCalls)
	}
	d := detailOf(t, store)
	if !strings.Contains(d, "keychain locked") || !strings.Contains(d, "disk full") {
		t.Errorf("Detail = %q, want it to name BOTH failures", d)
	}
}

// TestKeyringTokenStore_SaveBothErr covers Save's both-erroring row (missing
// combo test named by the review): what the caller sees when neither
// backend can persist the token.
func TestKeyringTokenStore_SaveBothErr(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	fb := &fakeFallback{saveErr: errors.New("disk full")}
	store := NewKeyringTokenStore("https://accounts.example.org", fb)

	err := store.Save(accounts.Tokens{RefreshToken: "rt-1"})
	if err == nil {
		t.Fatal("Save with both keychain and fallback erroring: err = nil, want a non-nil error")
	}
	if !strings.Contains(err.Error(), "keychain locked") || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("Save error = %v, want it to name BOTH failures", err)
	}
	if fb.saveCalls != 1 {
		t.Errorf("fallback Save calls = %d, want 1", fb.saveCalls)
	}
	d := detailOf(t, store)
	if !strings.Contains(d, "keychain locked") || !strings.Contains(d, "disk full") {
		t.Errorf("Detail = %q, want it to name BOTH failures (nothing succeeded)", d)
	}
}

// TestKeyringTokenStore_LoadErrorWithEmptyFallback is the review's named
// scenario (c): a prior Save succeeded against a healthy keychain (so the
// fallback file was never written), then a LATER Load hits an erroring
// keychain with an empty fallback. There is nothing "file"-flavored to
// report — the fallback never held anything — so Load must report a real
// miss and Detail must name the keychain's OWN error.
func TestKeyringTokenStore_LoadErrorWithEmptyFallback(t *testing.T) {
	keyring.MockInit()
	fb := &fakeFallback{} // empty: loadOK defaults false
	store := NewKeyringTokenStore("https://accounts.example.org", fb)
	if err := store.Save(accounts.Tokens{RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if fb.saveCalls != 0 {
		t.Fatalf("fallback Save calls = %d, want 0 (keychain was healthy)", fb.saveCalls)
	}

	keyring.MockInitWithError(errors.New("keychain locked"))
	got, ok := store.Load()
	if ok {
		t.Errorf("Load with an erroring keychain and empty fallback: ok = true (%+v), want false", got)
	}
	if fb.loadCalls != 1 {
		t.Errorf("fallback Load calls = %d, want 1", fb.loadCalls)
	}
	d := detailOf(t, store)
	if !strings.Contains(d, "keychain locked") {
		t.Errorf("Detail = %q, want it to name the keychain error", d)
	}
}

// TestKeyringTokenStore_LoadUnionsFallbackOnRecoveredMiss is the CRITICAL
// regression named by the review, reproduced exactly: Save during a
// keychain outage lands the refresh token ONLY in the file fallback (a real
// fileTokenStore, matching production wiring in cmd/shnkitd/main.go). Once
// the keychain "recovers" (MockInit swaps in a fresh, empty in-memory
// backend — the outage-era Save was NEVER written to it), a bare
// keyring.Get on this account returns ErrNotFound: an ORDINARY miss, not an
// error. Load must still return the fallback's token — union-checking the
// fallback on a miss, not just on an error — with an honest Detail, never
// silently discarding a token that genuinely exists and forcing an
// undiagnosable re-sign-in.
func TestKeyringTokenStore_LoadUnionsFallbackOnRecoveredMiss(t *testing.T) {
	dir := t.TempDir()
	fallback := NewFileTokenStore(filepath.Join(dir, "tokens.json"), "https://accounts.example.org")

	keyring.MockInitWithError(errors.New("keychain locked"))
	store := NewKeyringTokenStore("https://accounts.example.org", fallback)
	tok := accounts.Tokens{RefreshToken: "rt-outage"}
	if err := store.Save(tok); err != nil {
		t.Fatalf("Save during outage (should fall back): %v", err)
	}

	keyring.MockInit() // keychain "recovers" — fresh, empty backend

	got, ok := store.Load()
	if !ok {
		t.Fatal("Load after keychain recovery: ok = false, want true (the fallback's token must still be reachable)")
	}
	if got.RefreshToken != tok.RefreshToken {
		t.Errorf("Load = %+v, want the fallback's RefreshToken %q", got, tok.RefreshToken)
	}
	d := detailOf(t, store)
	if strings.Contains(d, "unavailable") {
		t.Errorf("Detail = %q, must not falsely claim the keychain is 'unavailable' (it's just an ordinary miss)", d)
	}
	if d != "file (keychain entry missing)" {
		t.Errorf("Detail = %q, want %q", d, "file (keychain entry missing)")
	}
}

// TestKeyringTokenStore_SaveOKClearsStaleFallback covers Save's stale-fallback
// fix: an outage-era Save degrades to the fallback
// (matching TestKeyringTokenStore_LoadUnionsFallbackOnRecoveredMiss's setup),
// the keychain then recovers, and a LATER Save of a NEW token succeeds
// against the now-healthy keychain. That successful keychain Save must
// best-effort clear the fallback too — otherwise the old outage-era token
// would keep sitting in the fallback file indefinitely, and a future
// keychain miss (e.g. the entry removed out-of-band) would wrongly
// union-resurrect the STALE token instead of reporting an honest miss.
func TestKeyringTokenStore_SaveOKClearsStaleFallback(t *testing.T) {
	dir := t.TempDir()
	fallback := NewFileTokenStore(filepath.Join(dir, "tokens.json"), "https://accounts.example.org")

	keyring.MockInitWithError(errors.New("keychain locked"))
	store := NewKeyringTokenStore("https://accounts.example.org", fallback)
	if err := store.Save(accounts.Tokens{RefreshToken: "rt-outage"}); err != nil {
		t.Fatalf("Save during outage (should fall back): %v", err)
	}
	if _, ok := fallback.Load(); !ok {
		t.Fatal("fallback.Load after outage Save: ok = false, want true (the stale token must be on disk to make this test meaningful)")
	}

	keyring.MockInit() // keychain recovers — fresh, empty in-memory backend
	if err := store.Save(accounts.Tokens{RefreshToken: "rt-recovered"}); err != nil {
		t.Fatalf("Save after recovery: %v", err)
	}
	if d := detailOf(t, store); d != "keychain" {
		t.Errorf("Detail after recovered Save = %q, want %q", d, "keychain")
	}

	if _, ok := fallback.Load(); ok {
		t.Error("fallback.Load after a successful recovered Save: ok = true, want false (the stale outage-era token must have been cleared)")
	}

	// And the store itself must report the NEW token, not resurrect the old
	// one via any union path.
	got, ok := store.Load()
	if !ok {
		t.Fatal("Load after recovered Save: ok = false, want true")
	}
	if got.RefreshToken != "rt-recovered" {
		t.Errorf("Load = %+v, want RefreshToken %q", got, "rt-recovered")
	}
}

// TestKeyringTokenStore_OversizedRefreshTokenRejected: the defensive length
// guard must reject BEFORE ever touching the OS keyring or the
// fallback — a named Go error instead of a wincred-specific mystery, and
// never silently degrading to the file store for what is a bug, not an
// environmental outage.
func TestKeyringTokenStore_OversizedRefreshTokenRejected(t *testing.T) {
	keyring.MockInit()
	fb := &fakeFallback{}
	store := NewKeyringTokenStore("https://accounts.example.org", fb)

	huge := strings.Repeat("a", maxRefreshTokenBytes+1)
	err := store.Save(accounts.Tokens{RefreshToken: huge})
	if err == nil {
		t.Fatal("Save with an oversized refresh token: err = nil, want ErrRefreshTokenTooLarge")
	}
	if !errors.Is(err, ErrRefreshTokenTooLarge) {
		t.Errorf("Save error = %v, want errors.Is(err, ErrRefreshTokenTooLarge)", err)
	}
	if fb.saveCalls != 0 {
		t.Errorf("fallback Save calls = %d, want 0 (the oversized guard must not fall back)", fb.saveCalls)
	}
	// The guard must not have reached the OS keyring either — a fresh Load
	// on a clean mock keyring should still miss.
	if _, ok := NewKeyringTokenStore("https://accounts.example.org", &fakeFallback{}).Load(); ok {
		t.Error("Load after a rejected oversized Save: ok = true, want false (nothing should have been persisted)")
	}
}
