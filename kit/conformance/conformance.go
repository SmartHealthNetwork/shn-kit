// Package conformance persists the Kit operator's live conformance-
// enforcement level choice — the UI-reachable equivalent of shnkitd's own
// --conformance-enforcement flag / kit.config.json's conformanceEnforcement
// field, for the one case those cannot reach: a packaged, installed Kit,
// where kit.config.json lives inside the signed, read-only app bundle and
// the operator has no shell to pass a flag from.
//
// Same shape as kit/byo.Store on purpose (this package's own doc mirrors
// that one): {stateDir}/conformance.json, 0600, validate-before-write, a
// missing file reads as the zero Config (nothing set — the published
// default applies), a present-but-corrupt file is a real error, never
// silently "nothing set".
package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Config is the persisted conformance.json shape. Level is "" (the
// published default applies — absence, not a value), "strict", or "none".
// These are the ONLY three values ValidateLevel accepts, mirroring the
// gateway's own CONFORMANCE_ENFORCEMENT contract
// (gateway/engine/conformance.go's ParseConformanceEnforcement) without this
// module importing the gateway module for three literal strings.
type Config struct {
	Level string `json:"level"`
}

const configFileName = "conformance.json"

// Store persists Config to {dir}/conformance.json. dir is the shnkitd state
// dir. All methods are safe for concurrent use.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns a Store rooted at dir (the shnkitd state dir).
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) configPath() string {
	return filepath.Join(s.dir, configFileName)
}

// ValidateLevel accepts exactly "", "strict", "none" — the same three
// values shnkitd's --conformance-enforcement flag and the gateway's
// CONFORMANCE_ENFORCEMENT env var accept (empty meaning "left unset",
// never a fourth value invented here). Unlike the CLI flag (which
// deliberately passes an invalid value through to the gateway child's own
// boot-time refusal — see kit/cmd/shnkitd/main.go's conformanceEnv doc),
// this package validates up front: a live toggle that restarts a running
// gateway child on a typo, only to have it refuse to boot, is a strictly
// worse operator experience than refusing the typo before anything is
// touched.
func ValidateLevel(level string) error {
	switch level {
	case "", "strict", "none":
		return nil
	}
	return fmt.Errorf("kit/conformance: level must be \"\", \"strict\", or \"none\", got %q", level)
}

// Load reads the persisted Config. A missing file is not an error — it
// returns a zero Config (Level == "", nothing set yet). A present-but-
// corrupt file IS an error: fail-safe, not fail-silent, mirroring
// kit/byo.Store.Load's own contract exactly.
func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (Config, error) {
	raw, err := os.ReadFile(s.configPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("kit/conformance: read %s: %w", s.configPath(), err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("kit/conformance: parse %s: %w", s.configPath(), err)
	}
	return cfg, nil
}

// Set validates level, then persists it. level == "" clears back to the
// published default (a real, persisted clear — not a no-op). Validation
// runs BEFORE any file is touched, so a rejected Set leaves conformance.json
// unchanged (mirrors byo.Store.SetEHR/SetDaVinci's validate-then-persist
// order).
func (s *Store) Set(level string) error {
	if err := ValidateLevel(level); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.MarshalIndent(Config{Level: level}, "", "  ")
	if err != nil {
		return fmt.Errorf("kit/conformance: marshal config: %w", err)
	}
	if err := os.WriteFile(s.configPath(), raw, 0o600); err != nil {
		return fmt.Errorf("kit/conformance: write %s: %w", s.configPath(), err)
	}
	return nil
}
