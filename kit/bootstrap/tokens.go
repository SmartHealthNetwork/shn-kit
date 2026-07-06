// Package bootstrap implements the SHN Kit's resumable sign-in/provision
// state machine: PKCE sign-in against the Accounts service, token
// persistence + refresh, two-step Accounts client provisioning, and local
// secrets-bundle management.
package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/SmartHealthNetwork/shn-sdk/accounts"
)

// TokenStore persists the Accounts login tokens across shnkitd restarts, so
// a signed-in session survives a daemon restart without re-running the PKCE
// browser flow.
type TokenStore interface {
	// Load returns the persisted tokens, or ok=false if none are usable:
	// missing file, corrupt file, or a store scoped to a DIFFERENT accounts
	// URL than the one that saved them (see NewFileTokenStore).
	Load() (accounts.Tokens, bool)
	// Save persists tokens, replacing anything previously stored.
	Save(accounts.Tokens) error
	// Clear removes any persisted tokens. Clearing an already-empty store is
	// not an error.
	Clear() error
}

// tokenFile is the on-disk JSON shape: the tokens plus the accounts URL they
// were minted against.
type tokenFile struct {
	AccountsURL string          `json:"accountsUrl"`
	Tokens      accounts.Tokens `json:"tokens"`
}

// fileTokenStore is the on-disk TokenStore: a single 0600 JSON file scoped
// to the accounts URL it was constructed for. NewKeyringTokenStore
// (keyring.go) wraps a fileTokenStore as its own fail-visible
// fallback behind the SAME TokenStore interface — callers never see the
// swap, and this file's contract is unchanged by that backend's existence.
type fileTokenStore struct {
	path        string
	accountsURL string
}

// NewFileTokenStore builds a TokenStore backed by a single JSON file at
// path, scoped to accountsURL (trailing-slash-trimmed). A file saved by one
// accounts URL is invisible to a store scoped to a different one: repointing
// --accounts over an existing state dir must never refresh/reuse tokens
// against the wrong pool (the same scoping check used by the CLI's
// cachedCreds.Accounts).
func NewFileTokenStore(path, accountsURL string) TokenStore {
	return &fileTokenStore{path: path, accountsURL: strings.TrimRight(accountsURL, "/")}
}

func (s *fileTokenStore) Load() (accounts.Tokens, bool) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return accounts.Tokens{}, false
	}
	var tf tokenFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return accounts.Tokens{}, false
	}
	if tf.AccountsURL != s.accountsURL {
		return accounts.Tokens{}, false
	}
	return tf.Tokens, true
}

func (s *fileTokenStore) Save(t accounts.Tokens) error {
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(tokenFile{AccountsURL: s.accountsURL, Tokens: t}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

func (s *fileTokenStore) Clear() error {
	err := os.Remove(s.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
