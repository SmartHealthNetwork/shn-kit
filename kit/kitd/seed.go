// seed.go — the Java trio's two boot-time seed steps, DELIBERATELY split
// across two different points in shnkitd's boot sequence:
//
//   - CopyPrewarmedH2 is PRE-SPAWN: it runs between BuildStack and the
//     supervisor's Start loop, copying the package-time-prewarmed H2 stores
//     (tools/kitassets/build.sh's 10-15-minute IG-indexing cost, paid once at
//     package time) into this install's state dir. It MUST run before either
//     HAPI child ever spawns: a running child creates its own empty H2 file
//     and holds the file lock the moment it starts, so copying afterward
//     would either silently no-op (a naive "skip if the dir exists" check) or
//     collide with a live lock (a non-skipping copy). Gated on a marker FILE
//     written last, never on destination-directory existence — see the
//     regression pin in seed_test.go for why directory existence alone can't
//     be the gate.
//
//   - FreshenPersonas is POST-READY: it runs after the data server child
//     passes its ReadyURLs probe, before the daemon's SetRunner. Unlike the
//     H2 copy, this one has NO gate — it re-POSTs the provider-data persona
//     bundles, the sandbox provider personas bundle, and
//     re-writes the seed-complete marker on EVERY boot, unconditionally.
//     That's deliberate: FreshenObservations rewrites each Observation's
//     effectiveDateTime to now, keeping the operated CQL's 3-month
//     ObservationLookBack alive across restarts — a stale prewarmed dataset
//     would otherwise silently age out of the lookback window (this was true
//     of the provider-data bundles from the start; the same fix later closed
//     the same gap for the sandbox personas bundle, whose lumbar-questionnaire
//     therapy-weeks Observation is the ehr-lane uc03..08 prepop answer).
package kitd

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/SmartHealthNetwork/shn-gateway/fhirseed"
)

// prewarmMarkerName is CopyPrewarmedH2's copy-complete marker file, written
// into stateDir LAST — only once both H2 stores have been fully copied. Its
// presence (never destination-directory existence) is the sole skip gate.
const prewarmMarkerName = ".prewarm-copied"

// seedTenant is the FHIR partition CopyPrewarmedH2/FreshenPersonas seed —
// the data server's "provider" tenant (mirrors deploy/compose.multiprocess.yml's
// provider-data posture).
const seedTenant = "provider"

// CopyPrewarmedH2 copies the package-time-prewarmed validator/data-server H2
// stores from assetsDir/prewarm/{validator,data}-h2 into
// stateDir/{validator,data-server}/h2 — the exact H2 dirs
// BuildValidatorChildSpec/BuildDataServerChildSpec point their datasource at.
// A no-op (nil, no I/O) when assetsDir is "" (no trio configured). Idempotent
// across restarts of the SAME install: once stateDir/.prewarm-copied exists,
// the copy is skipped entirely — a fresh install (or a state dir that was
// wiped by a reset) always has neither the marker nor the H2 dirs, so the
// copy always runs there.
func CopyPrewarmedH2(assetsDir, stateDir string, logf func(string, ...any)) error {
	if assetsDir == "" {
		return nil
	}
	marker := filepath.Join(stateDir, prewarmMarkerName)
	if _, err := os.Stat(marker); err == nil {
		if logf != nil {
			logf("kitd: prewarmed H2 already copied (%s present) — skipping", marker)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("kitd: stat prewarm marker %s: %w", marker, err)
	}

	pairs := []struct{ src, dst string }{
		{filepath.Join(assetsDir, "prewarm", "validator-h2"), filepath.Join(stateDir, validatorChildName, "h2")},
		{filepath.Join(assetsDir, "prewarm", "data-h2"), filepath.Join(stateDir, dataServerChildName, "h2")},
	}
	for _, p := range pairs {
		if logf != nil {
			logf("kitd: copying prewarmed H2 %s -> %s", p.src, p.dst)
		}
		if err := copyDirTree(p.src, p.dst); err != nil {
			return fmt.Errorf("kitd: copy prewarmed H2 %s -> %s: %w", p.src, p.dst, err)
		}
	}
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0600); err != nil {
		return fmt.Errorf("kitd: write prewarm marker %s: %w", marker, err)
	}
	return nil
}

// copyDirTree recursively copies src onto dst, creating directories as
// needed and overwriting any existing files (deliberate: the copy must
// proceed even when dst already
// exists — e.g. a stray empty dir left by a prior partial boot — since the
// marker file, not directory existence, is the only gate).
func copyDirTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0600)
	})
}

// FreshenPersonas (re)loads the provider-data persona bundles AND the sandbox
// provider personas bundle into the data server's "provider" tenant, then
// (re)writes its seed-complete marker — ALWAYS, unconditionally, every time
// it is called (the caller — shnkitd's boot goroutine — gates the call
// itself on the trio being configured). Idempotent PUTs/POSTs: safe to
// re-run every boot. Runs after the data server child has passed its
// ReadyURLs probe (dataURL is reachable) and before the daemon's SetRunner.
//
// The sandbox personas bundle (fhirseed.SandboxProviderPersonasBundle)
// carries baked static Observation effectiveDateTime values — the same
// therapy-weeks freshness trap LoadProviderDataBundles already closes for the
// provider-data bundles via FreshenObservations. Without re-posting it here
// too, the packaged Kit's operated-CQL lumbar $populate would silently rot
// out of the 3-month ObservationLookBack a few months after packaging (or
// after this daemon has been running a long-lived install past that window).
// Re-posting through the same FreshenObservations + PostTransaction pair
// keeps it on the identical idempotent-PUT posture as the rest of this func.
func FreshenPersonas(ctx context.Context, dataURL string, logf func(string, ...any)) error {
	c := &fhirseed.Client{Base: dataURL + "/fhir", Logf: logf}
	if err := c.LoadProviderDataBundles(ctx, seedTenant); err != nil {
		return fmt.Errorf("kitd: freshen provider-data personas: %w", err)
	}
	freshPersonas, err := fhirseed.FreshenObservations(fhirseed.SandboxProviderPersonasBundle())
	if err != nil {
		return fmt.Errorf("kitd: freshen sandbox personas bundle: %w", err)
	}
	if err := c.PostTransaction(ctx, seedTenant, freshPersonas); err != nil {
		return fmt.Errorf("kitd: repost sandbox personas bundle: %w", err)
	}
	if err := c.WriteSeedMarker(ctx, seedTenant); err != nil {
		return fmt.Errorf("kitd: write provider seed marker: %w", err)
	}
	return nil
}
