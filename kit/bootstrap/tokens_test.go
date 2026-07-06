package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-sdk/accounts"
)

func TestFileTokenStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	store := NewFileTokenStore(path, "https://accounts.example.org")

	want := accounts.Tokens{
		IDToken:      "id-1",
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		Expiry:       time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := store.Load()
	if !ok {
		t.Fatal("Load: ok = false, want true")
	}
	if got.IDToken != want.IDToken || got.AccessToken != want.AccessToken ||
		got.RefreshToken != want.RefreshToken || !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestFileTokenStore_FileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	store := NewFileTokenStore(path, "https://accounts.example.org")
	if err := store.Save(accounts.Tokens{IDToken: "id-1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestFileTokenStore_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewFileTokenStore(filepath.Join(dir, "missing.json"), "https://accounts.example.org")
	if _, ok := store.Load(); ok {
		t.Error("Load on missing file: ok = true, want false")
	}
}

func TestFileTokenStore_LoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	store := NewFileTokenStore(path, "https://accounts.example.org")
	if _, ok := store.Load(); ok {
		t.Error("Load on corrupt file: ok = true, want false")
	}
}

func TestFileTokenStore_Clear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	store := NewFileTokenStore(path, "https://accounts.example.org")
	if err := store.Save(accounts.Tokens{IDToken: "id-1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after Clear: err = %v", err)
	}
	if _, ok := store.Load(); ok {
		t.Error("Load after Clear: ok = true, want false")
	}
	// Clearing an already-cleared store is not an error.
	if err := store.Clear(); err != nil {
		t.Errorf("Clear on a missing file: %v", err)
	}
}

func TestFileTokenStore_SaveCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested", "sub")
	path := filepath.Join(nested, "tokens.json")
	store := NewFileTokenStore(path, "https://accounts.example.org")
	if err := store.Save(accounts.Tokens{IDToken: "id-1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat nested dir: %v", err)
	}
	if !fi.IsDir() {
		t.Fatal("nested path is not a directory")
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 0700", fi.Mode().Perm())
	}
}

// TestFileTokenStore_ScopedToAccountsURL: repointing --accounts over an
// existing state dir must never refresh/reuse tokens against the wrong pool
// (the same scoping check used by the CLI's cachedCreds.Accounts).
func TestFileTokenStore_ScopedToAccountsURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	store := NewFileTokenStore(path, "https://accounts.example.org")
	if err := store.Save(accounts.Tokens{IDToken: "id-1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	other := NewFileTokenStore(path, "https://accounts.other.org")
	if _, ok := other.Load(); ok {
		t.Error("Load from a store scoped to a different accounts URL: ok = true, want false")
	}

	// A trailing slash must not defeat the scoping match.
	trimmed := NewFileTokenStore(path, "https://accounts.example.org/")
	if _, ok := trimmed.Load(); !ok {
		t.Error("Load with a trailing-slash-equivalent accounts URL: ok = false, want true")
	}

	if _, ok := store.Load(); !ok {
		t.Error("Load from the original scoping: ok = false, want true")
	}
}
