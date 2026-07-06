// seed_test.go — hermetic tests for the Java trio's two seed steps.
// No Java, no Docker, no network beyond an in-process
// httptest server for FreshenPersonas.
package kitd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ---- CopyPrewarmedH2 --------------------------------------------------------------

func TestCopyPrewarmedH2_NoAssetsDir_NoOp(t *testing.T) {
	stateDir := t.TempDir()
	if err := CopyPrewarmedH2("", stateDir, nil); err != nil {
		t.Fatalf("CopyPrewarmedH2(\"\", ...): %v, want nil (no-op when no trio configured)", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, prewarmMarkerName)); err == nil {
		t.Errorf("marker written despite assetsDir == \"\"")
	}
}

func TestCopyPrewarmedH2_FreshCopy_MarkerWrittenLast(t *testing.T) {
	assetsDir := t.TempDir()
	stateDir := t.TempDir()
	mustWriteFile(t, filepath.Join(assetsDir, "prewarm", "validator-h2", "db.mv.db"), "validator-h2-bytes")
	mustWriteFile(t, filepath.Join(assetsDir, "prewarm", "data-h2", "db.mv.db"), "data-h2-bytes")

	if err := CopyPrewarmedH2(assetsDir, stateDir, nil); err != nil {
		t.Fatalf("CopyPrewarmedH2: %v", err)
	}

	if got := mustReadFile(t, filepath.Join(stateDir, "validator", "h2", "db.mv.db")); got != "validator-h2-bytes" {
		t.Errorf("validator h2 copy = %q", got)
	}
	if got := mustReadFile(t, filepath.Join(stateDir, "data-server", "h2", "db.mv.db")); got != "data-h2-bytes" {
		t.Errorf("data-server h2 copy = %q", got)
	}
	if _, err := os.Stat(filepath.Join(stateDir, prewarmMarkerName)); err != nil {
		t.Errorf("marker not written: %v", err)
	}
}

// TestCopyPrewarmedH2_MarkerPresent_NoRecopy pins the marker-gated skip: a
// sentinel written into the destination after the first (real) copy must
// survive a second call, since the marker's presence alone must skip it.
func TestCopyPrewarmedH2_MarkerPresent_NoRecopy(t *testing.T) {
	assetsDir := t.TempDir()
	stateDir := t.TempDir()
	mustWriteFile(t, filepath.Join(assetsDir, "prewarm", "validator-h2", "db.mv.db"), "orig-validator")
	mustWriteFile(t, filepath.Join(assetsDir, "prewarm", "data-h2", "db.mv.db"), "orig-data")

	if err := CopyPrewarmedH2(assetsDir, stateDir, nil); err != nil {
		t.Fatalf("CopyPrewarmedH2 (1st): %v", err)
	}

	sentinelPath := filepath.Join(stateDir, "validator", "h2", "db.mv.db")
	mustWriteFile(t, sentinelPath, "SENTINEL-must-survive")

	if err := CopyPrewarmedH2(assetsDir, stateDir, nil); err != nil {
		t.Fatalf("CopyPrewarmedH2 (2nd): %v", err)
	}
	if got := mustReadFile(t, sentinelPath); got != "SENTINEL-must-survive" {
		t.Errorf("2nd call re-copied over the sentinel (got %q) — marker presence should have skipped it entirely", got)
	}
}

// TestCopyPrewarmedH2_DirExistsNoMarker_StillCopies is the regression pin:
// a destination H2 dir that already exists (e.g. a running child created an
// empty one) but carries NO marker must still get the real copy — directory
// existence is never the gate.
func TestCopyPrewarmedH2_DirExistsNoMarker_StillCopies(t *testing.T) {
	assetsDir := t.TempDir()
	stateDir := t.TempDir()
	mustWriteFile(t, filepath.Join(assetsDir, "prewarm", "validator-h2", "db.mv.db"), "real-validator-bytes")
	mustWriteFile(t, filepath.Join(assetsDir, "prewarm", "data-h2", "db.mv.db"), "real-data-bytes")

	if err := os.MkdirAll(filepath.Join(stateDir, "validator", "h2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "data-server", "h2"), 0700); err != nil {
		t.Fatal(err)
	}

	if err := CopyPrewarmedH2(assetsDir, stateDir, nil); err != nil {
		t.Fatalf("CopyPrewarmedH2: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(stateDir, "validator", "h2", "db.mv.db")); got != "real-validator-bytes" {
		t.Errorf("copy did not run just because the dir already existed (dir-existence must never be the gate): got %q", got)
	}
}

func TestCopyPrewarmedH2_MissingSource_NamedError(t *testing.T) {
	assetsDir := t.TempDir() // no prewarm/ subdirs at all
	stateDir := t.TempDir()
	err := CopyPrewarmedH2(assetsDir, stateDir, nil)
	if err == nil {
		t.Fatal("want an error when the assets dir carries no prewarm/validator-h2")
	}
	if !strings.Contains(err.Error(), "validator-h2") {
		t.Errorf("error = %q, want it to name the missing source", err.Error())
	}
}

// ---- FreshenPersonas ---------------------------------------------------------------

// TestFreshenPersonas_AlwaysRuns proves FreshenPersonas has NO skip gate
// (unlike CopyPrewarmedH2): it re-POSTs the persona bundles and re-PUTs the
// seed marker on every call.
func TestFreshenPersonas_AlwaysRuns(t *testing.T) {
	var postCount int32
	var putCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fhir/provider", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&postCount, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("PUT /fhir/provider/Basic/seed-complete", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&putCount, 1)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if err := FreshenPersonas(context.Background(), srv.URL, nil); err != nil {
		t.Fatalf("FreshenPersonas: %v", err)
	}
	if atomic.LoadInt32(&postCount) == 0 {
		t.Errorf("no transaction bundles were POSTed")
	}
	if atomic.LoadInt32(&putCount) != 1 {
		t.Errorf("seed-complete marker PUT count = %d, want 1", putCount)
	}

	prevPosts := atomic.LoadInt32(&postCount)
	if err := FreshenPersonas(context.Background(), srv.URL, nil); err != nil {
		t.Fatalf("FreshenPersonas (2nd): %v", err)
	}
	if atomic.LoadInt32(&postCount) <= prevPosts {
		t.Errorf("2nd call did not re-POST — FreshenPersonas must have no skip gate")
	}
	if atomic.LoadInt32(&putCount) != 2 {
		t.Errorf("seed-complete marker PUT count after 2nd call = %d, want 2 (re-PUT every call)", putCount)
	}
}

// TestFreshenPersonas_SandboxPersonasBundle_ObservationsFreshened is the
// regression pin: FreshenPersonas must also re-POST the sandbox
// provider personas bundle (fhirseed.SandboxProviderPersonasBundle) through
// FreshenObservations, not just the provider-data bundles — the lumbar
// questionnaire's "conservative-therapy-weeks" Observation carries a baked
// static effectiveDateTime (2026-05-20 in the fixture) that would otherwise
// age out of the operated CQL's 3-month ObservationLookBack. The stub
// captures whichever POSTed transaction body names that Observation code and
// asserts the baked date is gone and today's date is present.
func TestFreshenPersonas_SandboxPersonasBundle_ObservationsFreshened(t *testing.T) {
	var mu sync.Mutex
	var personasBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fhir/provider", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read POST body: %v", err)
		}
		if strings.Contains(string(body), "conservative-therapy-weeks") {
			mu.Lock()
			personasBody = body
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("PUT /fhir/provider/Basic/seed-complete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if err := FreshenPersonas(context.Background(), srv.URL, nil); err != nil {
		t.Fatalf("FreshenPersonas: %v", err)
	}

	mu.Lock()
	body := string(personasBody)
	mu.Unlock()
	if body == "" {
		t.Fatal("the sandbox personas bundle (carrying conservative-therapy-weeks) was never POSTed by FreshenPersonas")
	}
	// 2026-05-20 is the therapy-weeks Observation's baked effectiveDateTime (the field
	// FreshenObservations rewrites). Other resource types in this bundle (e.g.
	// DiagnosticReport) carry their own unrelated dates FreshenObservations
	// deliberately leaves alone — asserting on those would be a false positive.
	if strings.Contains(body, "2026-05-20") {
		t.Errorf("posted sandbox personas bundle still carries the baked static therapy-weeks effectiveDateTime (2026-05-20) — FreshenObservations did not run on it")
	}
	today := time.Now().UTC().Format("2006-01-02")
	if !strings.Contains(body, today) {
		t.Errorf("posted sandbox personas bundle does not carry today's freshened effectiveDateTime (%s)", today)
	}
}

func TestFreshenPersonas_UpstreamFailure_NamedError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := FreshenPersonas(context.Background(), srv.URL, nil)
	if err == nil {
		t.Fatal("want an error when the data server rejects the transaction POSTs")
	}
	if !strings.Contains(err.Error(), "freshen provider-data personas") {
		t.Errorf("error = %q, want it named per FreshenPersonas' own wrapping", err.Error())
	}
}
