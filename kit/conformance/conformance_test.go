package conformance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		t.Fatal(`Set("some-future-level") = nil, want an error — only "" and the pinned gateway's levels are accepted`)
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

// Every level the pinned gateway accepts is offered, and nothing else: the
// list comes from the gateway's own parser, so a level the pinned release
// does not know (which would make the gateway child refuse to boot) is never
// offered, and "basic" — the level's working name before it shipped as
// "structural" — is refused.
func TestLevels_AreExactlyWhatThePinnedGatewayAccepts(t *testing.T) {
	want := []string{"none", "observe", "structural", "strict"}
	if got := Levels(); !slices.Equal(got, want) {
		t.Fatalf("Levels() = %v, want %v (gateway v0.54.0 accepts all four)", got, want)
	}
	for _, l := range append([]string{""}, want...) {
		if err := ValidateLevel(l); err != nil {
			t.Errorf("ValidateLevel(%q) = %v, want accepted", l, err)
		}
	}
	for _, l := range []string{"basic", "Strict", "observe ", "relaxed"} {
		err := ValidateLevel(l)
		if err == nil {
			t.Errorf("ValidateLevel(%q) = nil, want refused", l)
			continue
		}
		if !strings.Contains(err.Error(), "none, observe, structural, strict") {
			t.Errorf("ValidateLevel(%q) = %v, want the error to name the accepted levels", l, err)
		}
	}
}

// A candidate the pinned gateway's parser refuses is not offered: Levels
// filters candidateLevels through the parser, never lists them outright.
func TestLevels_OmitsACandidateThePinnedGatewayRefuses(t *testing.T) {
	saved := candidateLevels
	t.Cleanup(func() { candidateLevels = saved })
	candidateLevels = append([]string{"lenient"}, saved...)
	if got := Levels(); slices.Contains(got, "lenient") {
		t.Fatalf("Levels() = %v offers a level the pinned gateway refuses", got)
	}
	if err := ValidateLevel("lenient"); err == nil {
		t.Fatal(`ValidateLevel("lenient") = nil for a level the pinned gateway refuses`)
	}
}

// A "none" saved by a Kit before v0.21.0 meant "run every check, record what
// it found, relay the message", which observe still does: it is migrated
// once, and written with the current version so it is never migrated again.
func TestMigrateLegacy_MovesAPreV021NoneToObserve(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conformance.json"), []byte(`{"level":"none"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	migrated, err := s.MigrateLegacy()
	if err != nil || !migrated {
		t.Fatalf("MigrateLegacy() = %v, %v; want migrated", migrated, err)
	}
	cfg, err := s.Load()
	if err != nil || cfg.Level != "observe" || cfg.Version != configVersion {
		t.Fatalf("after migration Load() = %+v, %v; want observe at version %d", cfg, err, configVersion)
	}
	if again, err := s.MigrateLegacy(); err != nil || again {
		t.Fatalf("second MigrateLegacy() = %v, %v; want nothing to do", again, err)
	}
}

// Everything else is left as it was: a "none" chosen under the current
// semantics, every other level, an unset level and a missing file.
func TestMigrateLegacy_LeavesEverythingElse(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"current none", `{"level":"none","version":2}`},
		{"legacy strict", `{"level":"strict"}`},
		{"legacy unset", `{"level":""}`},
		{"missing file", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "conformance.json")
			if tc.file != "" {
				if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			migrated, err := NewStore(dir).MigrateLegacy()
			if err != nil || migrated {
				t.Fatalf("MigrateLegacy() = %v, %v; want nothing to do", migrated, err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != tc.file {
				t.Fatalf("file changed: %q, was %q", got, tc.file)
			}
		})
	}
}

// Every write records the current version, so a "none" chosen now is never
// mistaken for a pre-v0.21.0 one.
func TestStore_SetRecordsTheCurrentVersion(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Set("none"); err != nil {
		t.Fatal(err)
	}
	if migrated, _ := s.MigrateLegacy(); migrated {
		t.Fatal("a none chosen under the current semantics was migrated")
	}
	if cfg, _ := s.Load(); cfg.Level != "none" || cfg.Version != configVersion {
		t.Fatalf("Load() = %+v, want none at version %d", cfg, configVersion)
	}
}
