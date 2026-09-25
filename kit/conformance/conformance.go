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
	"strings"
	"sync"

	engine "github.com/SmartHealthNetwork/shn-gateway/engine"
)

// Config is the persisted conformance.json shape. Level is "" (the
// published default applies — absence, not a value) or one of Levels().
//
// Version records which gateway semantics Level was chosen under. A file
// with no version was written by a Kit before v0.21.0, whose packaged
// gateway predated the observe level: its "none" ran every check, recorded
// what it found and relayed the message, apart from a few cases it refused
// at every level (an answer it could not read, content defects, a validator
// that could not run). Observe is the closest level now — it also relays
// those, with a finding — while "none" now runs no checks at all.
// MigrateLegacy moves such a file to observe.
type Config struct {
	Level   string `json:"level"`
	Version int    `json:"version,omitempty"`
}

// configVersion is the Version every write records: levels chosen under the
// gateway's none/observe/structural/strict semantics (gateway v0.53.0 on).
const configVersion = 2

// candidateLevels are the levels the Kit knows how to offer, from least to
// most checking. Levels() narrows them to the ones the pinned gateway
// accepts.
var candidateLevels = []string{"none", "observe", "structural", "strict"}

// Levels returns the conformance levels the Kit's pinned gateway accepts, in
// candidateLevels' order. It asks the gateway's own parser
// (engine.ParseConformanceEnforcement), so a level is offered exactly when
// the gateway child would boot with it — the gateway refuses to start on a
// level it does not know — and no version table is kept here. In a monorepo
// workspace build (go.work) the gateway module is the checkout's own
// gateway/, so there Levels reflects that tree; a standalone or packaged Kit
// build reads the release kit/go.mod pins.
func Levels() []string {
	out := make([]string, 0, len(candidateLevels))
	for _, l := range candidateLevels {
		if _, err := engine.ParseConformanceEnforcement(l); err == nil {
			out = append(out, l)
		}
	}
	return out
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

// ValidateLevel accepts "" (left unset: the gateway's published default
// applies) or one of Levels() — the levels the pinned gateway accepts. It
// runs before anything is touched, so a live change never restarts a
// working gateway child only to have it refuse to boot, and shnkitd's
// --conformance-enforcement flag is checked the same way at startup.
func ValidateLevel(level string) error {
	if level == "" {
		return nil
	}
	levels := Levels()
	for _, l := range levels {
		if l == level {
			return nil
		}
	}
	return fmt.Errorf("kit/conformance: level must be %s, or left unset for the published default; got %q", strings.Join(levels, ", "), level)
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
	raw, err := json.MarshalIndent(Config{Level: level, Version: configVersion}, "", "  ")
	if err != nil {
		return fmt.Errorf("kit/conformance: marshal config: %w", err)
	}
	if err := os.WriteFile(s.configPath(), raw, 0o600); err != nil {
		return fmt.Errorf("kit/conformance: write %s: %w", s.configPath(), err)
	}
	return nil
}

// MigrateLegacy moves a "none" saved by a Kit before v0.21.0 to "observe".
// Under the gateway that Kit packaged, "none" ran every check and recorded
// each finding (see Config for the cases it still refused); observe is the
// closest level now. Left as it was, the same saved choice would now switch
// conformance checking off. It reports
// whether it migrated, so the caller can tell the operator once. A file
// already written under the current semantics, a missing file and any other
// saved level are left untouched.
func (s *Store) MigrateLegacy() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	if cfg.Version != 0 || cfg.Level != "none" {
		return false, nil
	}
	raw, err := json.MarshalIndent(Config{Level: "observe", Version: configVersion}, "", "  ")
	if err != nil {
		return false, fmt.Errorf("kit/conformance: marshal config: %w", err)
	}
	if err := os.WriteFile(s.configPath(), raw, 0o600); err != nil {
		return false, fmt.Errorf("kit/conformance: write %s: %w", s.configPath(), err)
	}
	return true, nil
}

// LegacyNoneNotice is what the Kit tells an operator whose saved "none" was
// moved to observe by MigrateLegacy.
const LegacyNoneNotice = `Your saved conformance level "none" is now "observe". In earlier Kits "none" still ran every check and recorded what it found, and observe is the closest level now. Observe also relays a few messages the earlier "none" refused, such as an answer the gateway cannot read; choose Strict to refuse them. "None" now runs no conformance checks at all — choose it again if that is what you want.`
