// supportbundle_test.go — hermetic tests for GET /api/support-bundle.
// White-box (package kitd), mirroring
// kitd_test.go's style: a fake supervisor layout (plain files dropped
// directly under StateDir with the same names the real supervisor.ChildSpec
// LogPath convention uses — gateway.log, validator.log, ...) stands in for a
// real spawned child, since the bundle enumerates logs by globbing
// StateDir/*.log rather than asking a live Supervisor for anything.
package kitd

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/bootstrap"
	"github.com/SmartHealthNetwork/shn-kit/byo"
	"github.com/SmartHealthNetwork/shn-kit/event"
	"github.com/SmartHealthNetwork/shn-kit/runhistory"
	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

// doRawGET issues a GET with an optional bearer token, returning status,
// headers, and the RAW (undecoded) response body — support-bundle responses
// are a zip binary, not JSON, so doJSON's body decoding doesn't apply here.
func doRawGET(t *testing.T, url, token string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, resp.Header, b
}

// openBundleZip parses body as a zip archive, failing the test if it doesn't
// open (the "zip opens" assertion every row here implicitly makes).
func openBundleZip(t *testing.T, body []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("open support bundle as zip: %v", err)
	}
	return zr
}

// zipEntryBytes DECOMPRESSES entry name's content (zip.File.Open transparently
// inflates deflate-compressed entries) — never a raw-bytes read of the
// archive: a raw-archive
// grep is dead on arrival, since deflate coding means a leaked secret's literal
// bytes generally don't appear in the compressed stream.
func zipEntryBytes(t *testing.T, zr *zip.Reader, name string) ([]byte, bool) {
	t.Helper()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", name, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read zip entry %s: %v", name, err)
		}
		return b, true
	}
	return nil, false
}

func randomSentinel(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	// Hex, not base64 (memory: forbidden-substring/base64-collision lesson —
	// short exact-case token scans over hash-bearing bodies flake on base64
	// collisions; hex sidesteps that class of flake entirely).
	return hex.EncodeToString(b)
}

// ---- Row: zip opens, contains logs + manifest + probes + history ----------

func TestSupportBundle_ContainsLogsManifestProbesHistory(t *testing.T) {
	const token = "support-bundle-token"
	stateDir := t.TempDir()

	// Fake sup layout: plain log files dropped directly under StateDir, the
	// same names/location the real supervisor.ChildSpec.LogPath convention
	// uses (kitd/stack.go, kitd/javachildren.go) — no real child process
	// needed, since the bundle enumerates by globbing StateDir/*.log.
	gatewayLog := []byte("gateway: boot ok\ngateway: uc01 dispatched\n")
	validatorLog := []byte("validator: ready\n")
	if err := os.WriteFile(filepath.Join(stateDir, "gateway.log"), gatewayLog, 0644); err != nil {
		t.Fatalf("write gateway.log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "validator.log"), validatorLog, 0644); err != nil {
		t.Fatalf("write validator.log: %v", err)
	}
	// A non-.log file under StateDir must NOT be swept in (the bundle's
	// inventory is explicit: *.log for logs, never "everything under
	// StateDir" — see supportbundle.go).
	if err := os.WriteFile(filepath.Join(stateDir, "ingress-clients.json"), []byte("[]"), 0644); err != nil {
		t.Fatalf("write ingress-clients.json: %v", err)
	}

	manifestPath := filepath.Join(t.TempDir(), "versions.json")
	manifestBytes := []byte(`{"kit":"1.2.3","gateway":"0.20.1"}` + "\n")
	if err := os.WriteFile(manifestPath, manifestBytes, 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	histDir := t.TempDir()
	histStore := runhistory.NewStore(histDir, 200)
	rec := runhistory.Record{Summary: runhistory.Summary{
		RunID: "run-bundle-1", Lane: "ehr", UC: "uc01", Branch: "covered", State: "passed",
		Time: time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC), EventCount: 2,
	}, Events: []event.Event{{Seq: 1}, {Seq: 2}}}
	if err := histStore.Save(rec); err != nil {
		t.Fatalf("Save history record: %v", err)
	}

	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:      "127.0.0.1:0",
		StateDir:     stateDir,
		Token:        token,
		Bus:          bus,
		Sup:          supervisor.New(nil),
		ManifestPath: manifestPath,
		History:      histStore,
	}
	d, apiBase := startDaemon(t, cfg)
	d.SetVerify([]bootstrap.Probe{{Name: "discovery", OK: true, Detail: "ok"}, {Name: "registration", OK: false, Detail: "nope"}})

	status, hdr, body := doRawGET(t, apiBase+"/api/support-bundle", token)
	if status != http.StatusOK {
		t.Fatalf("GET /api/support-bundle = %d, want 200", status)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}

	zr := openBundleZip(t, body)

	if got, ok := zipEntryBytes(t, zr, "logs/gateway.log"); !ok || !bytes.Equal(got, gatewayLog) {
		t.Errorf("logs/gateway.log = %q (ok=%v), want %q", got, ok, gatewayLog)
	}
	if got, ok := zipEntryBytes(t, zr, "logs/validator.log"); !ok || !bytes.Equal(got, validatorLog) {
		t.Errorf("logs/validator.log = %q (ok=%v), want %q", got, ok, validatorLog)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == "ingress-clients.json" {
			t.Errorf("bundle contains %q — only *.log files belong under logs/, never an unrelated StateDir file", f.Name)
		}
	}

	if got, ok := zipEntryBytes(t, zr, "about/versions.json"); !ok || !bytes.Equal(got, manifestBytes) {
		t.Errorf("about/versions.json = %q (ok=%v), want verbatim manifest %q", got, ok, manifestBytes)
	}

	probesBytes, ok := zipEntryBytes(t, zr, "probes.json")
	if !ok {
		t.Fatal("bundle missing probes.json")
	}
	var probes []bootstrap.Probe
	if err := json.Unmarshal(probesBytes, &probes); err != nil {
		t.Fatalf("unmarshal probes.json: %v", err)
	}
	if len(probes) != 2 || probes[0].Name != "discovery" || probes[1].Name != "registration" {
		t.Fatalf("probes.json = %+v, want the 2 SetVerify probes", probes)
	}

	histBytes, ok := zipEntryBytes(t, zr, "history.json")
	if !ok {
		t.Fatal("bundle missing history.json")
	}
	var records []runhistory.Record
	if err := json.Unmarshal(histBytes, &records); err != nil {
		t.Fatalf("unmarshal history.json: %v", err)
	}
	if len(records) != 1 || records[0].RunID != "run-bundle-1" || len(records[0].Events) != 2 {
		t.Fatalf("history.json = %+v, want the 1 saved record with its Events", records)
	}
}

// TestSupportBundle_ManifestAbsent_NoAboutEntry proves an unset ManifestPath
// (dev posture) makes the bundle simply omit about/versions.json — the
// bundle as a whole still builds and 200s, mirroring GET /api/about's own
// "absent is not fatal" contract.
func TestSupportBundle_ManifestAbsent_NoAboutEntry(t *testing.T) {
	const token = "support-bundle-no-manifest-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		// ManifestPath intentionally left "".
	}
	_, apiBase := startDaemon(t, cfg)

	status, _, body := doRawGET(t, apiBase+"/api/support-bundle", token)
	if status != http.StatusOK {
		t.Fatalf("GET /api/support-bundle (no manifest) = %d, want 200", status)
	}
	zr := openBundleZip(t, body)
	if _, ok := zipEntryBytes(t, zr, "about/versions.json"); ok {
		t.Fatal("bundle contains about/versions.json despite ManifestPath being unset")
	}
	// probes.json and history.json must still be present (empty, never
	// missing — a nil History Config also must not crash the bundle).
	if _, ok := zipEntryBytes(t, zr, "probes.json"); !ok {
		t.Fatal("bundle missing probes.json even with no manifest configured")
	}
	if _, ok := zipEntryBytes(t, zr, "history.json"); !ok {
		t.Fatal("bundle missing history.json even with History nil")
	}
}

// TestSupportBundle_HistoryTruncatedToLast20Newest proves the bundle caps
// history.json at the last 20 records, newest first — never the full
// keep-200 retention window.
func TestSupportBundle_HistoryTruncatedToLast20Newest(t *testing.T) {
	const token = "support-bundle-history-cap-token"
	histDir := t.TempDir()
	histStore := runhistory.NewStore(histDir, 200)
	base := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		rec := runhistory.Record{Summary: runhistory.Summary{
			RunID: runIDFor(i), Lane: "ehr", UC: "uc01", Branch: "covered", State: "passed",
			Time: base.Add(time.Duration(i) * time.Minute), EventCount: 0,
		}}
		if err := histStore.Save(rec); err != nil {
			t.Fatalf("Save record %d: %v", i, err)
		}
	}

	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
		History:  histStore,
	}
	_, apiBase := startDaemon(t, cfg)

	_, _, body := doRawGET(t, apiBase+"/api/support-bundle", token)
	zr := openBundleZip(t, body)
	histBytes, ok := zipEntryBytes(t, zr, "history.json")
	if !ok {
		t.Fatal("bundle missing history.json")
	}
	var records []runhistory.Record
	if err := json.Unmarshal(histBytes, &records); err != nil {
		t.Fatalf("unmarshal history.json: %v", err)
	}
	if len(records) != 20 {
		t.Fatalf("history.json has %d records, want 20 (capped from 25 saved)", len(records))
	}
	// Newest first: record 24 (the last saved) is index 0; the 20th-newest is
	// record 5.
	if records[0].RunID != runIDFor(24) {
		t.Errorf("history.json[0].RunID = %q, want %q (newest first)", records[0].RunID, runIDFor(24))
	}
	if records[19].RunID != runIDFor(5) {
		t.Errorf("history.json[19].RunID = %q, want %q (20th newest)", records[19].RunID, runIDFor(5))
	}
}

func runIDFor(i int) string { return "run-cap-" + itoa(i) }

// itoa avoids importing strconv solely for this one helper's sake in a test
// file that otherwise has no numeric-formatting need.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ---- Row: token gated -------------------------------------------------------

func TestSupportBundle_TokenGated(t *testing.T) {
	const token = "support-bundle-gate-token"
	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: t.TempDir(),
		Token:    token,
		Bus:      bus,
		Sup:      supervisor.New(nil),
	}
	_, apiBase := startDaemon(t, cfg)

	status, _, _ := doRawGET(t, apiBase+"/api/support-bundle", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("GET /api/support-bundle without token = %d, want 401", status)
	}
}

// ---- Row: sans-secrets contract --------------------------------------------

// TestSupportBundle_NoSecretsLeak is the sans-secrets contract row: a fresh
// StateDir plants FOUR high-entropy
// sentinels across the two files a support bundle must NEVER carry —
// tokens.json (the accounts login token store) and the byo EHR
// client key file (byo.Store.EHRKeyPath) — plus the daemon's own
// in-memory session token. The bundle is then OPENED and every entry's
// DECOMPRESSED content is scanned for each sentinel (a raw-archive-bytes
// grep over the zip's compressed stream is dead on arrival: deflate coding
// means a leaked secret's literal bytes generally don't appear in the
// compressed form, so that check passes green exactly when the exclusion is
// broken — this test instead round-trips through zip.File.Open, which
// transparently inflates each entry before the scan ever runs).
//
// The exclusion here is BY INVENTORY, not by a deny-list: supportbundle.go
// only ever adds *.log files (by extension), the manifest (an explicit
// path), probes (in-memory state), and history (an explicit Store read) —
// it never walks StateDir wholesale, which is exactly why none of the
// planted secrets can appear. This test is the contract that keeps that true
// as the bundle's contents evolve; a future change that swept in "everything
// under StateDir" would fail it immediately.
func TestSupportBundle_NoSecretsLeak(t *testing.T) {
	const token = "support-bundle-secrets-token"
	stateDir := t.TempDir()

	sessionTokenSentinel := randomSentinel(t)
	refreshTokenSentinel := randomSentinel(t)
	privateKeySentinel := randomSentinel(t)
	byoKeyContentSentinel := randomSentinel(t)

	// A benign log file — logs ARE meant to be included; this is a positive
	// control proving the scan isn't just vacuously passing an empty bundle.
	if err := os.WriteFile(filepath.Join(stateDir, "gateway.log"), []byte("gateway: boot ok\n"), 0644); err != nil {
		t.Fatalf("write gateway.log: %v", err)
	}

	// tokens.json — the real on-disk shape bootstrap.fileTokenStore writes
	// (accountsUrl + tokens.refresh_token), planted with a refresh-token
	// sentinel. Must never be named in the bundle, nor its content appear
	// anywhere in it.
	tokensJSON := `{"accountsUrl":"https://accounts.example.org","tokens":{"access_token":"unused","refresh_token":"` + refreshTokenSentinel + `"}}`
	if err := os.WriteFile(filepath.Join(stateDir, "tokens.json"), []byte(tokensJSON), 0600); err != nil {
		t.Fatalf("write tokens.json: %v", err)
	}

	// The byo EHR client key file — planted with both a PEM-shaped "PRIVATE
	// KEY material" sentinel and a distinct "byo-key content" sentinel, at
	// the SAME path byo.Store.EHRKeyPath() resolves (never hardcoded, so a
	// path-convention drift can't silently defeat this test).
	byoStore := byo.NewStore(stateDir)
	pemContent := "-----BEGIN PRIVATE KEY-----\n" + privateKeySentinel + "\n-----END PRIVATE KEY-----\n# byo-key: " + byoKeyContentSentinel + "\n"
	if err := os.WriteFile(byoStore.EHRKeyPath(), []byte(pemContent), 0600); err != nil {
		t.Fatalf("write byo EHR key: %v", err)
	}

	bus := event.NewBus(fixedClock)
	cfg := Config{
		APIAddr:  "127.0.0.1:0",
		StateDir: stateDir,
		Token:    sessionTokenSentinel,
		Bus:      bus,
		Sup:      supervisor.New(nil),
	}
	_, apiBase := startDaemon(t, cfg)

	status, _, body := doRawGET(t, apiBase+"/api/support-bundle", sessionTokenSentinel)
	if status != http.StatusOK {
		t.Fatalf("GET /api/support-bundle = %d, want 200", status)
	}
	zr := openBundleZip(t, body)

	// No entry named tokens.json or the byo key file, by basename.
	forbiddenNames := map[string]bool{
		"tokens.json":                        true,
		filepath.Base(byoStore.EHRKeyPath()): true,
	}
	for _, f := range zr.File {
		if forbiddenNames[filepath.Base(f.Name)] {
			t.Errorf("bundle contains an entry named %q — tokens.json/the byo key file must never appear", f.Name)
		}
	}

	sentinels := map[string]string{
		"daemon session token": sessionTokenSentinel,
		"refresh token":        refreshTokenSentinel,
		"PRIVATE KEY material": privateKeySentinel,
		"byo-key content":      byoKeyContentSentinel,
	}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", f.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read (decompressed) zip entry %s: %v", f.Name, err)
		}
		for label, sentinel := range sentinels {
			if strings.Contains(string(content), sentinel) {
				t.Errorf("zip entry %q's DECOMPRESSED content contains the %s sentinel — secrets leaked into the support bundle", f.Name, label)
			}
		}
	}
}
