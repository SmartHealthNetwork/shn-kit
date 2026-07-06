// supportbundle.go — GET /api/support-bundle: a downloadable zip an operator
// can hand to support without
// screen-sharing, assembled from an EXPLICIT inventory — never a wholesale
// walk of StateDir — which is exactly what keeps secrets (tokens.json, the
// byo EHR client key) out of it: neither is a *.log file, the manifest path,
// or a history record, so neither is ever a candidate for inclusion in the
// first place.
package kitd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/SmartHealthNetwork/shn-kit/bootstrap"
	"github.com/SmartHealthNetwork/shn-kit/runhistory"
)

// supportBundleHistoryLimit bounds GET /api/support-bundle's history.json to
// the most recent runs, newest first — a diagnostic snapshot, never the full
// keep-200 retention window runhistory.Store itself holds on disk.
const supportBundleHistoryLimit = 20

// handleSupportBundle serves GET /api/support-bundle: builds the zip
// in-memory (bundle sizes are Kit-local logs/history, never PHI-scale) so a
// build failure can still answer a clean 500 instead of a truncated
// half-written zip on the wire.
func (d *Daemon) handleSupportBundle(w http.ResponseWriter, _ *http.Request) {
	var buf bytes.Buffer
	if err := d.writeSupportBundle(&buf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="shn-kit-support-bundle.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// writeSupportBundle assembles the bundle's explicit inventory into zw:
//
//   - logs/<basename> for every *.log file directly under Config.StateDir —
//     the same glob-by-extension seam every child's supervisor.ChildSpec.LogPath
//     already writes into (kitd/stack.go, kitd/javachildren.go all place
//     "<child>.log" straight under StateDir), so no new plumbing from the
//     supervisor is needed: StateDir is already wired into Config today. An
//     unreadable individual log file is skipped (best-effort — mirrors
//     runhistory.Store.List's own one-bad-file tolerance), never fails the
//     whole bundle.
//   - about/versions.json — Config.ManifestPath's bytes, verbatim, omitted
//     entirely when ManifestPath is "" or unreadable (the same absence GET
//     /api/about itself treats as "not available," never an error here).
//   - probes.json — the latest published bootstrap.Probe results (the SAME
//     state GET /api/bootstrap's "verify" field reads).
//   - history.json — up to the last supportBundleHistoryLimit runhistory.Record
//     entries, newest first (Config.History == nil ⇒ an empty array, never a
//     missing file or a bundle-level error).
func (d *Daemon) writeSupportBundle(w *bytes.Buffer) error {
	zw := zip.NewWriter(w)

	if err := addLogEntries(zw, d.cfg.StateDir); err != nil {
		return err
	}
	if err := addManifestEntry(zw, d.cfg.ManifestPath); err != nil {
		return err
	}
	if err := addProbesEntry(zw, d.currentProbes()); err != nil {
		return err
	}
	if err := addHistoryEntry(zw, d.cfg.History); err != nil {
		return err
	}

	return zw.Close()
}

// currentProbes returns the latest SetVerify-published probes (never nil, so
// probes.json always marshals as "[]" rather than "null" when nothing has
// ever been probed).
func (d *Daemon) currentProbes() []bootstrap.Probe {
	d.mu.RLock()
	probes := d.verify
	d.mu.RUnlock()
	if probes == nil {
		return []bootstrap.Probe{}
	}
	return probes
}

// addLogEntries globs StateDir for *.log files (sorted, so the bundle's
// contents are deterministic across runs) and adds each as logs/<basename>.
func addLogEntries(zw *zip.Writer, stateDir string) error {
	matches, err := filepath.Glob(filepath.Join(stateDir, "*.log"))
	if err != nil {
		return fmt.Errorf("kitd: glob %s/*.log: %w", stateDir, err)
	}
	sort.Strings(matches)
	for _, path := range matches {
		b, err := os.ReadFile(path)
		if err != nil {
			continue // best-effort: one unreadable log must not sink the whole bundle
		}
		if err := writeZipEntry(zw, "logs/"+filepath.Base(path), b); err != nil {
			return err
		}
	}
	return nil
}

// addManifestEntry adds about/versions.json when manifestPath is set and
// readable; a silent no-op otherwise (the dev posture GET /api/about itself
// treats as an honest absence, not an error).
func addManifestEntry(zw *zip.Writer, manifestPath string) error {
	if manifestPath == "" {
		return nil
	}
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}
	return writeZipEntry(zw, "about/versions.json", b)
}

// addProbesEntry adds probes.json: the currently-published "hello substrate"
// probe results, indented JSON.
func addProbesEntry(zw *zip.Writer, probes []bootstrap.Probe) error {
	b, err := json.MarshalIndent(probes, "", "  ")
	if err != nil {
		return fmt.Errorf("kitd: marshal probes: %w", err)
	}
	return writeZipEntry(zw, "probes.json", b)
}

// addHistoryEntry adds history.json: up to the last supportBundleHistoryLimit
// runhistory.Record entries (Summary + Events), newest first. store == nil
// (History not configured, the S3..S7-shaped Config embeddings) writes an
// empty array, not an error and not a missing file.
func addHistoryEntry(zw *zip.Writer, store *runhistory.Store) error {
	var records []runhistory.Record
	if store != nil {
		sums, err := store.List()
		if err != nil {
			return fmt.Errorf("kitd: list history for support bundle: %w", err)
		}
		if len(sums) > supportBundleHistoryLimit {
			sums = sums[:supportBundleHistoryLimit]
		}
		for _, s := range sums {
			rec, err := store.Get(s.RunID)
			if err != nil {
				continue // best-effort, mirrors addLogEntries' one-bad-file tolerance
			}
			if rec != nil {
				records = append(records, *rec)
			}
		}
	}
	if records == nil {
		records = []runhistory.Record{}
	}
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("kitd: marshal history for support bundle: %w", err)
	}
	return writeZipEntry(zw, "history.json", b)
}

// writeZipEntry adds one entry to zw with data as its full content.
func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	fw, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("kitd: create zip entry %s: %w", name, err)
	}
	if _, err := fw.Write(data); err != nil {
		return fmt.Errorf("kitd: write zip entry %s: %w", name, err)
	}
	return nil
}
