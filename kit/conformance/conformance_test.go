package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LoadMissingFileIsZeroConfig(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	cfg, err := s.Load()
	if err != nil {
		t.Fatalf("Load() on a missing file = %v, want nil error", err)
	}
	if cfg.Level != "" {
		t.Fatalf("Load() on a missing file = %+v, want a zero Config (Level empty)", cfg)
	}
}

func TestStore_SetThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if err := s.Set("strict"); err != nil {
		t.Fatalf("Set(strict) = %v, want nil", err)
	}
	cfg, err := s.Load()
	if err != nil {
		t.Fatalf("Load() after Set(strict) = %v, want nil", err)
	}
	if cfg.Level != "strict" {
		t.Fatalf("Load() after Set(strict) = %+v, want Level=strict", cfg)
	}

	if err := s.Set("none"); err != nil {
		t.Fatalf("Set(none) = %v, want nil", err)
	}
	cfg, err = s.Load()
	if err != nil {
		t.Fatalf("Load() after Set(none) = %v, want nil", err)
	}
	if cfg.Level != "none" {
		t.Fatalf("Load() after Set(none) = %+v, want Level=none", cfg)
	}
}

// Set("") clears back to the published default — the same "absence, not a
// value" contract the whole feature is built on. It must persist as a real
// clear (an empty Level on the next Load), not a no-op that leaves a stale
// prior value in place.
func TestStore_SetEmptyClearsToDefault(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Set("strict"); err != nil {
		t.Fatalf("Set(strict) = %v, want nil", err)
	}
	if err := s.Set(""); err != nil {
		t.Fatalf("Set(\"\") = %v, want nil", err)
	}
	cfg, err := s.Load()
	if err != nil {
		t.Fatalf("Load() after Set(\"\") = %v, want nil", err)
	}
	if cfg.Level != "" {
		t.Fatalf("Load() after Set(\"\") = %+v, want Level empty (cleared)", cfg)
	}
}

// Set validates BEFORE writing — a rejected value must never reach disk,
// the same discipline byo.Store.SetEHR/SetDaVinci hold (validate, then
// persist, never the reverse).
func TestStore_SetRejectsUnknownLevelWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	err := s.Set("some-future-level")
	if err == nil {
		t.Fatal(`Set("some-future-level") = nil, want an error — only "", "strict", "none" are accepted`)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "conformance.json")); statErr == nil {
		t.Fatal(`Set("some-future-level") wrote conformance.json despite rejecting the value`)
	}
}

// A present-but-corrupt file is a real error (fail-safe, not fail-silent) —
// mirrors byo.Store.Load's own contract exactly.
func TestStore_LoadCorruptFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conformance.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	s := NewStore(dir)
	if _, err := s.Load(); err == nil {
		t.Fatal("Load() on a corrupt file = nil error, want a parse error")
	}
}

func TestValidateLevel(t *testing.T) {
	for _, ok := range []string{"", "strict", "none"} {
		if err := ValidateLevel(ok); err != nil {
			t.Errorf("ValidateLevel(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"None", "Strict", " strict", "strict ", "middle"} {
		if err := ValidateLevel(bad); err == nil {
			t.Errorf("ValidateLevel(%q) = nil, want an error", bad)
		}
	}
}
