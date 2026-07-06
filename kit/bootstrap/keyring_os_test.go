//go:build keyring_os

package bootstrap

import (
	"path/filepath"
	"testing"

	"github.com/SmartHealthNetwork/shn-sdk/accounts"
)

// TestKeyringOS_RoundTrip exercises NewKeyringTokenStore against the REAL OS
// keychain/Credential Manager — excluded from
// every default gate by the keyring_os build tag (a bare `go test
// ./bootstrap/ -race` never compiles this file) and run only by the
// packaging workflow's per-OS matrix (macOS: after `security
// unlock-keychain` unlocks the runner's login keychain; Windows: Credential
// Manager needs no such unlock step). The hermetic keyring_test.go rows
// prove the fallback/scoping/guard LOGIC against keyring.MockInit(); this
// file is the one proof that the real OS backend actually works at all —
// the packaging smoke never exercises token Save/Load at all.
//
// A synthetic accounts URL + refresh token keep this from ever colliding
// with a real operator's persisted Kit session on the machine it runs on.
func TestKeyringOS_RoundTrip(t *testing.T) {
	const accountsURL = "https://kit-s8-keyring-os-test.invalid/accounts"
	fallback := NewFileTokenStore(filepath.Join(t.TempDir(), "tokens.json"), accountsURL)
	store := NewKeyringTokenStore(accountsURL, fallback)
	t.Cleanup(func() { _ = store.Clear() })

	want := accounts.Tokens{RefreshToken: "kit-s8-keyring-os-test-refresh-token"}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save (real OS keychain): %v", err)
	}
	d, ok := store.(interface{ Detail() string })
	if !ok {
		t.Fatal("store does not implement interface{ Detail() string }")
	}
	if got := d.Detail(); got != "keychain" {
		t.Fatalf("Detail after Save = %q, want %q (Save must have hit the REAL keychain, not a fallback)", got, "keychain")
	}

	got, ok := store.Load()
	if !ok {
		t.Fatal("Load (real OS keychain): ok = false, want true")
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("Load.RefreshToken = %q, want %q", got.RefreshToken, want.RefreshToken)
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear (real OS keychain): %v", err)
	}
	if _, ok := store.Load(); ok {
		t.Error("Load after Clear: ok = true, want false")
	}
}
