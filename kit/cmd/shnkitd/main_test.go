// main_test.go — unit tests for main's extracted, testable helpers.
// main() itself carries no logic beyond flag parsing
// and wiring (see the package doc comment); these helpers are the pieces of
// that wiring worth asserting on directly rather than only through the live
// kit-e2e/trio gates.
package main

import (
	"path/filepath"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-kit/bootstrap"
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
