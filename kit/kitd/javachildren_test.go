// javachildren_test.go — hermetic ChildSpec-assembly tests for the Java trio.
// No Java, no Docker, no network: these assert the
// ChildSpec shape only — Command/Args/Env/Dir/ReadyURLs/LogPath — never spawn
// anything. tools/kitassets/build.sh's boot proof (a live gate) is what
// certifies the config channel actually boots real HAPI/br-provider.
package kitd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/validatorwarm"
)

// ---- IG package file:// URLs (the v0.10.1 cold-lane defect) -----------------------------

// TestFileURLFromSlashPath_Table pins the escaping rules directly, including
// the Windows drive-letter shape that no darwin/linux CI run can reach through
// a real filesystem path.
func TestFileURLFromSlashPath_Table(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			name: "space-free POSIX path is byte-identical to the pre-fix concatenation",
			in:   "/assets/igs-validator/hl7.fhir.us.core-6.1.0.tgz",
			want: "file:///assets/igs-validator/hl7.fhir.us.core-6.1.0.tgz",
		},
		{
			name: "the shipped macOS install path (the HAPI-2031 case)",
			in:   "/Applications/SHN Kit.app/Contents/Resources/java/igs-validator/hl7.fhir.us.davinci-cdex-2.1.0.tgz",
			want: "file:///Applications/SHN%20Kit.app/Contents/Resources/java/igs-validator/hl7.fhir.us.davinci-cdex-2.1.0.tgz",
		},
		{
			name: "a Windows drive-letter path gains the third slash and escapes its spaces",
			in:   "C:/Program Files/SHN Kit/resources/java/igs-validator/x-1.0.0.tgz",
			want: "file:///C:/Program%20Files/SHN%20Kit/resources/java/igs-validator/x-1.0.0.tgz",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileURLFromSlashPath(tc.in); got != tc.want {
				t.Errorf("fileURLFromSlashPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHAPIIGPackageURLs_SpaceInInstallPath is the regression pin for issue
// the v0.10.1 cold-lane defect: the Kit installs to "/Applications/SHN Kit.app", whose
// SPACE reached HAPI's IG loader inside an unescaped file:// URL and killed
// every COLD validator lane with
//
//	HAPI-2031: Illegal character in path at index 24
//
// The default 2.0 lane masked it (it boots from the package-time prewarmed H2
// and never dereferences a packageUrl at runtime), so only the extra bridging
// lanes — every one of which boots cold by design — ever hit it. Asserted over
// BOTH Java HAPI children, since hapiSpringConfig serves both.
func TestHAPIIGPackageURLs_SpaceInInstallPath(t *testing.T) {
	stateDir := t.TempDir()
	// The real shape, not a synthetic one: an app bundle whose directory name
	// contains a space, exactly as electron-builder ships it.
	assetsDir := filepath.Join(t.TempDir(), "SHN Kit.app", "Contents", "Resources", "java")
	if err := os.MkdirAll(assetsDir, 0700); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	absAssets, err := filepath.Abs(assetsDir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// Line 2.1 — a COLD lane (never prewarmed), i.e. the exact child that
	// crashlooped in the published v0.10.1 installer.
	validatorSpec, err := BuildValidatorChildSpec(assetsDir, "/opt/jre", stateDir, 18080, "darwin", "2.1")
	if err != nil {
		t.Fatalf("BuildValidatorChildSpec: %v", err)
	}
	dataSpec, err := BuildDataServerChildSpec(assetsDir, "/opt/jre", stateDir, 18081, "darwin")
	if err != nil {
		t.Fatalf("BuildDataServerChildSpec: %v", err)
	}

	for _, tc := range []struct {
		child string
		env   []string
		igs   []ig
		dir   string
	}{
		{"validator-2.1", validatorSpec.Env, kitdIGPinsValidator("2.1"), "igs-validator"},
		{"data-server", dataSpec.Env, kitdIGPinsData("2.0"), "igs-data"},
	} {
		t.Run(tc.child, func(t *testing.T) {
			cfg := springConfig(t, tc.env)
			if len(tc.igs) == 0 {
				t.Fatalf("no IG pins for %s — test would assert nothing", tc.child)
			}
			for _, g := range tc.igs {
				key := "hapi.fhir.implementationguides." + g.key + ".packageUrl"
				got := cfg[key]
				// (1) No raw space survives into the URL — the literal
				// HAPI-2031 trigger.
				if strings.Contains(got, " ") {
					t.Errorf("%s = %q contains a raw space — HAPI rejects it with HAPI-2031", key, got)
				}
				// (2) The space is percent-escaped, not dropped or replaced.
				if !strings.Contains(got, "SHN%20Kit.app") {
					t.Errorf("%s = %q, want the install dir's space percent-escaped as SHN%%20Kit.app", key, got)
				}
				// (3) It is a well-formed file URL that round-trips back to
				// the real on-disk path — escaping that loses the path would
				// be a different bug, not a fix.
				u, perr := url.Parse(got)
				if perr != nil {
					t.Fatalf("%s = %q: not parseable as a URL: %v", key, got, perr)
				}
				if u.Scheme != "file" {
					t.Errorf("%s = %q: scheme = %q, want file", key, got, u.Scheme)
				}
				wantPath := filepath.Join(absAssets, tc.dir, g.name+"-"+g.version+".tgz")
				if u.Path != wantPath {
					t.Errorf("%s round-trips to %q, want the real path %q", key, u.Path, wantPath)
				}
			}
		})
	}
}

// ---- command path (GOOS-parameterized) ----------------------------------------

func TestJavaCommand_Unix(t *testing.T) {
	got := javaCommand("/opt/jre", "darwin")
	want := filepath.Join("/opt/jre", "bin", "java")
	if got != want {
		t.Errorf("javaCommand(darwin) = %q, want %q", got, want)
	}
}

func TestJavaCommand_Windows(t *testing.T) {
	got := javaCommand(`C:\jre`, "windows")
	want := filepath.Join(`C:\jre`, "bin", "java.exe")
	if got != want {
		t.Errorf("javaCommand(windows) = %q, want %q", got, want)
	}
}

// ---- launch args ----------------------------------------------------------------

func TestJavaArgs_Shape(t *testing.T) {
	got := javaArgs(768, "/state/validator/tmp", "/state/validator/main.war")
	want := []string{
		"-Xmx768m",
		"-Djava.io.tmpdir=/state/validator/tmp",
		"--class-path", "/state/validator/main.war",
		"-Dloader.path=main.war!/WEB-INF/classes/,main.war!/WEB-INF/,/app/extra-classes",
		"org.springframework.boot.loader.PropertiesLauncher",
	}
	if len(got) != len(want) {
		t.Fatalf("javaArgs = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("javaArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// assertJavaTmpDir asserts that args carries -Djava.io.tmpdir=<workDir>/tmp,
// that the property precedes the PropertiesLauncher main class (system
// properties must precede the main class on the java command line), and that
// the tmp dir was actually created on disk by the ChildSpec builder — a
// writable per-child dir, unlike the JVM's C:\Windows default when no
// TEMP/TMP env var is set (the non-admin Windows first-boot failure this
// guards against).
func assertJavaTmpDir(t *testing.T, args []string, workDir string) {
	t.Helper()
	wantTmp := filepath.Join(workDir, "tmp")
	wantArg := "-Djava.io.tmpdir=" + wantTmp

	tmpIdx, mainIdx := -1, -1
	for i, a := range args {
		if a == wantArg {
			tmpIdx = i
		}
		if a == "org.springframework.boot.loader.PropertiesLauncher" {
			mainIdx = i
		}
	}
	if tmpIdx == -1 {
		t.Fatalf("Args = %q, want it to contain %q", args, wantArg)
	}
	if mainIdx == -1 {
		t.Fatalf("Args = %q, want it to contain the PropertiesLauncher main class", args)
	}
	if tmpIdx > mainIdx {
		t.Errorf("-Djava.io.tmpdir at Args[%d] comes after PropertiesLauncher at Args[%d], want it before (system properties precede the main class)", tmpIdx, mainIdx)
	}

	fi, err := os.Stat(wantTmp)
	if err != nil {
		t.Fatalf("os.Stat(%s): %v, want the builder to have created it", wantTmp, err)
	}
	if !fi.IsDir() {
		t.Errorf("%s exists but is not a directory", wantTmp)
	}
}

// ---- validator ChildSpec ---------------------------------------------------------

func springConfig(t *testing.T, env []string) map[string]string {
	t.Helper()
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "SPRING_APPLICATION_JSON="); ok {
			var m map[string]string
			if err := json.Unmarshal([]byte(v), &m); err != nil {
				t.Fatalf("unmarshal SPRING_APPLICATION_JSON: %v (value=%s)", err, v)
			}
			return m
		}
	}
	t.Fatalf("Env = %q, want a SPRING_APPLICATION_JSON entry", env)
	return nil
}

func TestBuildValidatorChildSpec(t *testing.T) {
	stateDir := t.TempDir()
	assetsDir := "/assets"
	spec, err := BuildValidatorChildSpec(assetsDir, "/opt/jre", stateDir, 18080, "darwin", "2.0")
	if err != nil {
		t.Fatalf("BuildValidatorChildSpec: %v", err)
	}
	if spec.Name != "validator" {
		t.Errorf("Name = %q, want validator", spec.Name)
	}
	if spec.Command != filepath.Join("/opt/jre", "bin", "java") {
		t.Errorf("Command = %q", spec.Command)
	}
	workDir := filepath.Join(stateDir, "validator")
	wantWar := filepath.Join(workDir, "main.war")
	if len(spec.Args) < 4 || spec.Args[3] != wantWar {
		t.Fatalf("Args = %q, want --class-path %q", spec.Args, wantWar)
	}
	assertJavaTmpDir(t, spec.Args, workDir)
	if spec.Dir != workDir {
		t.Errorf("Dir = %q, want %q (loader.path's main.war!/... entries are CWD-relative)", spec.Dir, workDir)
	}
	if spec.LogPath != filepath.Join(stateDir, "validator.log") {
		t.Errorf("LogPath = %q", spec.LogPath)
	}
	wantReady := []string{"http://127.0.0.1:18080/fhir/metadata"}
	if len(spec.ReadyURLs) != 1 || spec.ReadyURLs[0] != wantReady[0] {
		t.Errorf("ReadyURLs = %q, want %q", spec.ReadyURLs, wantReady)
	}
	if spec.ReadyTimeout != javaReadyTimeout {
		t.Errorf("ReadyTimeout = %v, want %v", spec.ReadyTimeout, javaReadyTimeout)
	}
	if spec.RestartMax != javaRestartMax {
		t.Errorf("RestartMax = %d, want %d", spec.RestartMax, javaRestartMax)
	}

	// main.war symlink materialized into the workdir.
	if fi, lerr := os.Lstat(wantWar); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected a symlink at %s: fi=%v err=%v", wantWar, fi, lerr)
	}

	cfg := springConfig(t, spec.Env)
	wantH2 := filepath.Join(workDir, "h2")
	if !strings.Contains(cfg["spring.datasource.url"], wantH2) {
		t.Errorf("datasource.url = %q, want it under %q", cfg["spring.datasource.url"], wantH2)
	}
	if cfg["spring.datasource.username"] != "sa" || cfg["spring.datasource.driverClassName"] != "org.h2.Driver" {
		t.Errorf("datasource username/driver = %q/%q", cfg["spring.datasource.username"], cfg["spring.datasource.driverClassName"])
	}
	if cfg["server.port"] != "18080" {
		t.Errorf("server.port = %q, want 18080", cfg["server.port"])
	}
	// Validator is single-tenant: NONE of the URL_BASED/partitioning/cr keys.
	for _, k := range []string{
		"hapi.fhir.tenant_identification_strategy",
		"hapi.fhir.partitioning.partitioning_include_in_search_hashes",
		"hapi.fhir.partitioning.allow_references_across_partitions",
		"hapi.fhir.cr.enabled",
	} {
		if _, ok := cfg[k]; ok {
			t.Errorf("validator config carries %q, want it absent (single-tenant $validate only)", k)
		}
	}
	// All 10 IGs present (validator-sidecar with local support), each pointing into assetsDir.
	absAssets, _ := filepath.Abs(assetsDir)
	validatorIGs20 := kitdIGPinsValidator("2.0")
	for _, g := range validatorIGs20 {
		key := "hapi.fhir.implementationguides." + g.key + ".packageUrl"
		want := "file://" + filepath.Join(absAssets, "igs-validator", g.name+"-"+g.version+".tgz")
		if cfg[key] != want {
			t.Errorf("%s = %q, want %q", key, cfg[key], want)
		}
	}
	if len(validatorIGs20) != 10 {
		t.Fatalf("kitdIGPinsValidator(\"2.0\") has %d entries, want 10 (validator-sidecar with local support)", len(validatorIGs20))
	}
}

// ---- validator ChildSpec: per-line -------------------------------------------

// TestBuildValidatorChildSpec_NonDefaultLine_ColdTimeoutOwnDirAndExtSet proves
// three things together for a non-default line: (1) it gets its OWN state-dir
// child name ("validator-2.2"), never colliding with the prewarmed default's
// "validator" dir; (2) its ReadyTimeout is the COLD bound (no prewarm exists
// for it); (3) line 2.2 carries the 11-IG extensions-closure superset
// (validator-sidecar-ext with local support).
func TestBuildValidatorChildSpec_NonDefaultLine_ColdTimeoutOwnDirAndExtSet(t *testing.T) {
	stateDir := t.TempDir()
	assetsDir := "/assets"
	spec, err := BuildValidatorChildSpec(assetsDir, "/opt/jre", stateDir, 18090, "linux", "2.2")
	if err != nil {
		t.Fatalf("BuildValidatorChildSpec: %v", err)
	}
	if spec.Name != "validator-2.2" {
		t.Errorf("Name = %q, want validator-2.2", spec.Name)
	}
	wantWorkDir := filepath.Join(stateDir, "validator-2.2")
	if spec.Dir != wantWorkDir {
		t.Errorf("Dir = %q, want %q", spec.Dir, wantWorkDir)
	}
	if spec.LogPath != filepath.Join(stateDir, "validator-2.2.log") {
		t.Errorf("LogPath = %q, want %q", spec.LogPath, filepath.Join(stateDir, "validator-2.2.log"))
	}
	if spec.ReadyTimeout != javaReadyTimeoutCold {
		t.Errorf("ReadyTimeout = %v, want the cold bound %v (line 2.2 is never prewarmed)", spec.ReadyTimeout, javaReadyTimeoutCold)
	}

	cfg := springConfig(t, spec.Env)
	validatorIGs22 := kitdIGPinsValidator("2.2")
	if len(validatorIGs22) != 11 {
		t.Fatalf("kitdIGPinsValidator(\"2.2\") has %d entries, want 11 (validator-sidecar-ext with local support)", len(validatorIGs22))
	}
	if _, ok := cfg["hapi.fhir.implementationguides.extensions.packageUrl"]; !ok {
		t.Errorf("2.2-line validator config missing the extensions IG (validator-sidecar-ext): %v", cfg)
	}
}

// TestBuildValidatorChildSpec_DefaultLine_FastTimeoutOwnDir is the
// complementary pin: the default line ("2.0") keeps the FAST (prewarmed)
// timeout and the unqualified "validator" dir name, byte-identical to the
// behavior before the validator line became configurable.
func TestBuildValidatorChildSpec_DefaultLine_FastTimeoutOwnDir(t *testing.T) {
	stateDir := t.TempDir()
	spec, err := BuildValidatorChildSpec("/assets", "/opt/jre", stateDir, 18091, "linux", "2.0")
	if err != nil {
		t.Fatalf("BuildValidatorChildSpec: %v", err)
	}
	if spec.Name != "validator" {
		t.Errorf("Name = %q, want validator (unqualified — the prewarmed default line)", spec.Name)
	}
	if spec.ReadyTimeout != javaReadyTimeout {
		t.Errorf("ReadyTimeout = %v, want the fast (prewarmed) bound %v", spec.ReadyTimeout, javaReadyTimeout)
	}
}

// TestBuildValidatorChildSpec_UnknownLine_Errors is the rejection test: an
// unrecognized line must fail loudly, never silently boot a validator with an
// empty IG set (which would present as "$validate always passes" — a
// FR-36-defeating false green).
func TestBuildValidatorChildSpec_UnknownLine_Errors(t *testing.T) {
	_, err := BuildValidatorChildSpec("/assets", "/opt/jre", t.TempDir(), 18092, "linux", "9.9")
	if err == nil {
		t.Fatal("expected an error for an unrecognized line, got nil")
	}
}

// ---- data server ChildSpec --------------------------------------------------------

func TestBuildDataServerChildSpec(t *testing.T) {
	stateDir := t.TempDir()
	assetsDir := "/assets"
	spec, err := BuildDataServerChildSpec(assetsDir, "/opt/jre", stateDir, 18081, "linux")
	if err != nil {
		t.Fatalf("BuildDataServerChildSpec: %v", err)
	}
	if spec.Name != "data-server" {
		t.Errorf("Name = %q, want data-server", spec.Name)
	}
	workDir := filepath.Join(stateDir, "data-server")
	if spec.Dir != workDir {
		t.Errorf("Dir = %q, want %q", spec.Dir, workDir)
	}
	if spec.LogPath != filepath.Join(stateDir, "data-server.log") {
		t.Errorf("LogPath = %q", spec.LogPath)
	}
	assertJavaTmpDir(t, spec.Args, workDir)
	wantReady := "http://127.0.0.1:18081/fhir/DEFAULT/metadata"
	if len(spec.ReadyURLs) != 1 || spec.ReadyURLs[0] != wantReady {
		t.Errorf("ReadyURLs = %q, want [%q] (tenanted DEFAULT route — bare /fhir/metadata 200s even untenanted under URL_BASED)", spec.ReadyURLs, wantReady)
	}

	cfg := springConfig(t, spec.Env)
	wantH2 := filepath.Join(workDir, "h2")
	if !strings.Contains(cfg["spring.datasource.url"], wantH2) {
		t.Errorf("datasource.url = %q, want it under %q", cfg["spring.datasource.url"], wantH2)
	}
	if cfg["hapi.fhir.tenant_identification_strategy"] != "URL_BASED" {
		t.Errorf("tenant_identification_strategy = %q, want URL_BASED", cfg["hapi.fhir.tenant_identification_strategy"])
	}
	if cfg["hapi.fhir.partitioning.partitioning_include_in_search_hashes"] != "false" {
		t.Errorf("partitioning_include_in_search_hashes = %q, want false", cfg["hapi.fhir.partitioning.partitioning_include_in_search_hashes"])
	}
	if cfg["hapi.fhir.partitioning.allow_references_across_partitions"] != "false" {
		t.Errorf("allow_references_across_partitions = %q, want false", cfg["hapi.fhir.partitioning.allow_references_across_partitions"])
	}
	if cfg["hapi.fhir.cr.enabled"] != "true" {
		t.Errorf("cr.enabled = %q, want true", cfg["hapi.fhir.cr.enabled"])
	}
	absAssets, _ := filepath.Abs(assetsDir)
	dataIGs20 := kitdIGPinsData("2.0")
	for _, g := range dataIGs20 {
		key := "hapi.fhir.implementationguides." + g.key + ".packageUrl"
		want := "file://" + filepath.Join(absAssets, "igs-data", g.name+"-"+g.version+".tgz")
		if cfg[key] != want {
			t.Errorf("%s = %q, want %q", key, cfg[key], want)
		}
	}
	if len(dataIGs20) != 4 {
		t.Fatalf("kitdIGPinsData(\"2.0\") has %d entries, want 4", len(dataIGs20))
	}
}

// ---- br-provider ChildSpec --------------------------------------------------------

func TestBuildBRProviderChildSpec(t *testing.T) {
	stateDir := t.TempDir()
	spec, err := BuildBRProviderChildSpec("/assets", "/opt/jre", stateDir, 18082, "darwin",
		"http://127.0.0.1:9100", "http://127.0.0.1:18082", "/state/br-provider-cert.pfx", "s3cr3t")
	if err != nil {
		t.Fatalf("BuildBRProviderChildSpec: %v", err)
	}
	if spec.Name != "br-provider" {
		t.Errorf("Name = %q, want br-provider", spec.Name)
	}
	workDir := filepath.Join(stateDir, "br-provider")
	if spec.Dir != workDir {
		t.Errorf("Dir = %q, want %q", spec.Dir, workDir)
	}
	if spec.LogPath != filepath.Join(stateDir, "br-provider.log") {
		t.Errorf("LogPath = %q", spec.LogPath)
	}
	assertJavaTmpDir(t, spec.Args, workDir)
	wantReady := "http://127.0.0.1:18082/fhir/metadata"
	if len(spec.ReadyURLs) != 1 || spec.ReadyURLs[0] != wantReady {
		t.Errorf("ReadyURLs = %q, want [%q]", spec.ReadyURLs, wantReady)
	}
	want := []string{
		"SERVER_PORT=18082",
		"APP_PAYER_SERVERS_0_CDS_URL=http://127.0.0.1:9100/cds-services",
		"APP_PAYER_SERVERS_0_FHIR_URL=http://127.0.0.1:9100",
		"SECURITY_ALLOWEDLOCALHOSTS_0=127.0.0.1",
		"SECURITY_EXTERNAL_BASE_URL=http://127.0.0.1:18082",
		"SECURITY_CERT_FILE=/state/br-provider-cert.pfx",
		"SECURITY_CERT_PASSWORD=s3cr3t",
		"SECURITY_FETCH_CERT=false",
	}
	for i, w := range want {
		if i >= len(spec.Env) || spec.Env[i] != w {
			t.Errorf("Env[%d] = %q, want %q (full Env=%q)", i, valueOrMissing(spec.Env, i), w, spec.Env)
		}
	}
	for _, e := range spec.Env {
		if strings.HasPrefix(e, "SPRING_APPLICATION_JSON=") {
			t.Errorf("br-provider Env contains SPRING_APPLICATION_JSON — it takes plain named vars, not the HAPI config channel")
		}
	}
}

func valueOrMissing(env []string, i int) string {
	if i >= len(env) {
		return "<missing>"
	}
	return env[i]
}

// ---- ensureWarLink fallback -------------------------------------------------------

func TestEnsureWarLink_Idempotent(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "child")
	warSrc := "/does/not/exist/main.war" // dangling target is fine — never resolved here
	dst1, err := ensureWarLink(workDir, warSrc)
	if err != nil {
		t.Fatalf("ensureWarLink: %v", err)
	}
	dst2, err := ensureWarLink(workDir, warSrc)
	if err != nil {
		t.Fatalf("ensureWarLink (2nd call): %v", err)
	}
	if dst1 != dst2 {
		t.Errorf("dst1=%q dst2=%q, want the same path both times", dst1, dst2)
	}
}

// ---- validator ChildSpec: readiness = metadata + the verdict corpus ---------------

// fakeValidatorLane is a HAPI stand-in at the wire (metadata 200; every
// $validate an OperationOutcome) listening on a loopback port the ChildSpec
// under test is built for. respond, when set, may answer (or deliberately never
// answer) a request itself by returning false.
type fakeValidatorLane struct {
	port    int
	respond func(w http.ResponseWriter, n int, body []byte) bool
	mu      sync.Mutex
	posts   []string // "<path>?<profile>#<sha256(body)>" per $validate, in order
}

const (
	laneCleanOutcome    = `{"resourceType":"OperationOutcome","issue":[{"severity":"information","code":"informational","diagnostics":"Validation successful"}]}`
	laneNegativeOutcome = `{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"processing","details":{"coding":[{"system":"http://hl7.org/fhir/java-core-messageId","code":"Extension_EXT_Type"}]},"diagnostics":"The Extension 'http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode' definition allows for the types [CodeableConcept] but found type boolean","expression":["ClaimResponse.item[0].adjudication[0].extension[0].extension[0]"]}]}`
)

func newFakeValidatorLane(t *testing.T) *fakeValidatorLane {
	t.Helper()
	l := &fakeValidatorLane{}
	mux := http.NewServeMux()
	mux.HandleFunc("/fhir/metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"resourceType":"CapabilityStatement"}`))
	})
	mux.HandleFunc("/fhir/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		l.mu.Lock()
		l.posts = append(l.posts, fmt.Sprintf("%s?%s#%x", r.URL.Path, r.URL.Query().Get("profile"), sha256.Sum256(body)))
		n := len(l.posts)
		l.mu.Unlock()
		if l.respond != nil && !l.respond(w, n, body) {
			return
		}
		w.Header().Set("Content-Type", "application/fhir+json")
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/fhir/Bundle/$validate" {
			control := ""
			switch {
			case strings.Contains(string(body), "L9999"):
				control = "hcpcs"
			case strings.Contains(string(body), `"code":"98"`):
				control = "pos"
			case strings.Contains(string(body), "invalid-reference-type"):
				control = "encounter"
			}
			if control != "" {
				dir := "../validatorwarm/testdata"
				profile := r.URL.Query().Get("profile")
				if strings.HasSuffix(profile, "|2.2.1") {
					dir += "/2.2"
				}
				if strings.HasSuffix(profile, "|2.1.0") {
					dir += "/2.1"
				}
				raw, err := os.ReadFile(filepath.Join(dir, "pas-response-"+control+"-errors.json"))
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = w.Write(raw)
				return
			}
		}
		profile := r.URL.Query().Get("profile")
		if strings.HasSuffix(profile, "|9.9.9") || profile == "https://example.org/fhir/StructureDefinition/unavailable-profile" {
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceType": "OperationOutcome", "issue": []any{map[string]any{
				"severity": "error", "code": "processing", "details": map[string]any{"coding": []any{map[string]any{"system": "http://hl7.org/fhir/java-core-messageId", "code": "Validation_VAL_Profile_Unknown"}}},
				"diagnostics": "Invalid profile. Failed to retrieve explicitly requested profile with url=" + profile,
			}}})
			return
		}
		if r.URL.Path == "/fhir/Claim/$validate" && strings.Contains(string(body), `"id":"probe-encounter"`) && strings.Contains(string(body), `"resourceType":"Patient"`) {
			// the encounter target-type control: the contained Encounter was swapped for a Patient
			raw, err := os.ReadFile("../validatorwarm/testdata/claim-encounter-target-errors.json")
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = w.Write(raw)
			return
		}
		if strings.Contains(string(body), `"valueBoolean":true`) {
			_, _ = w.Write([]byte(laneNegativeOutcome))
			return
		}
		_, _ = w.Write([]byte(laneCleanOutcome))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	l.port, _ = strconv.Atoi(portText)
	return l
}

func (l *fakeValidatorLane) recorded() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.posts...)
}

// readyHookFor builds the validator ChildSpec for lane's port and returns its
// Ready hook — the exact closure the supervisor would run after /fhir/metadata
// answered.
func readyHookFor(t *testing.T, lane *fakeValidatorLane, line string) func(context.Context, func(string)) error {
	t.Helper()
	spec, err := BuildValidatorChildSpec("/assets", "/opt/jre", t.TempDir(), lane.port, "darwin", line)
	if err != nil {
		t.Fatalf("BuildValidatorChildSpec: %v", err)
	}
	if spec.Ready == nil {
		t.Fatal("validator ChildSpec has no Ready hook: /fhir/metadata alone would count the child ready")
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d/fhir/metadata", lane.port); len(spec.ReadyURLs) != 1 || spec.ReadyURLs[0] != want {
		t.Fatalf("ReadyURLs = %q, want [%s] (metadata stays the first half of readiness)", spec.ReadyURLs, want)
	}
	return spec.Ready
}

// The validator child's readiness hook posts the full 42-row verdict corpus
// for its line against the child's own port, and reports progress per row.
// Only the validator carries the hook: the data server and br-provider keep
// their metadata-only probes.
func TestBuildValidatorChildSpec_ReadyWarmsTheFullCorpus(t *testing.T) {
	for _, line := range []string{"2.0", "2.2"} {
		t.Run(line, func(t *testing.T) {
			lane := newFakeValidatorLane(t)
			ready := readyHookFor(t, lane, line)
			var mu sync.Mutex
			var progress []string
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := ready(ctx, func(s string) { mu.Lock(); progress = append(progress, s); mu.Unlock() }); err != nil {
				t.Fatalf("Ready: %v", err)
			}
			posts := lane.recorded()
			if len(posts) != 42 || len(progress) != 42 {
				t.Fatalf("posted %d rows with %d progress lines, want 42 and 42", len(posts), len(progress))
			}
			if !strings.HasPrefix(posts[0], "/fhir/Bundle/$validate?http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-pas-request-bundle#") {
				t.Errorf("first row = %s, want the PAS request Bundle initialization row", posts[0])
			}
			if !strings.HasPrefix(posts[37], "/fhir/Bundle/$validate?") {
				t.Errorf("row 38 = %s, want the last full-response negative control", posts[37])
			}
			for _, i := range []int{38, 39} {
				if !strings.HasPrefix(posts[i], "/fhir/Claim/$validate?http://hl7.org/fhir/StructureDefinition/Claim#") {
					t.Errorf("row %d = %s, want an encounter row against the core Claim profile", i+1, posts[i])
				}
			}
			if !strings.Contains(posts[40], "|9.9.9#") || !strings.Contains(posts[41], "?https://example.org/fhir/StructureDefinition/unavailable-profile#") {
				t.Fatalf("missing explicit-profile control requests: %v", posts[40:])
			}
			if !strings.Contains(progress[0], "1/42") || !strings.Contains(progress[41], "42/42") {
				t.Errorf("progress = %q … %q, want 1/42 … 42/42", progress[0], progress[41])
			}
		})
	}

	dataSpec, err := BuildDataServerChildSpec("/assets", "/opt/jre", t.TempDir(), 18081, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	brpSpec, err := BuildBRProviderChildSpec("/assets", "/opt/jre", t.TempDir(), 18082, "darwin", "http://127.0.0.1:1", "http://127.0.0.1:18082", "/x.p12", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if dataSpec.Ready != nil || brpSpec.Ready != nil {
		t.Error("data-server / br-provider must keep metadata-only readiness (no $validate corpus)")
	}
}

// Rejection row: /metadata 200 with $validate hanging is not ready inside the
// budget — the hook returns at the deadline naming the row, having posted it
// once.
func TestValidatorReady_MetadataOnlyWithValidateHangingIsNotReady(t *testing.T) {
	lane := newFakeValidatorLane(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	lane.respond = func(http.ResponseWriter, int, []byte) bool { <-release; return false }
	ready := readyHookFor(t, lane, "2.0")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := ready(ctx, nil)
	if err == nil {
		t.Fatal("Ready returned nil while $validate never answered")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("Ready outlived the budget by %s", time.Since(started))
	}
	if !strings.Contains(err.Error(), "init-pas-request-bundle") {
		t.Errorf("error %q should name the hanging row", err)
	}
	if got := lane.recorded(); len(got) != 1 {
		t.Errorf("posted %d times, want 1", len(got))
	}
}

// Rejection row: /metadata 200 with $validate answering a non-OperationOutcome
// is not ready.
func TestValidatorReady_NonOperationOutcomeIsNotReady(t *testing.T) {
	lane := newFakeValidatorLane(t)
	lane.respond = func(w http.ResponseWriter, _ int, _ []byte) bool {
		w.Header().Set("Content-Type", "application/fhir+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","type":"collection"}`))
		return false
	}
	ready := readyHookFor(t, lane, "2.1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := ready(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "not an OperationOutcome") {
		t.Fatalf("Ready = %v, want a 'not an OperationOutcome' failure", err)
	}
	if got := lane.recorded(); len(got) != 1 {
		t.Errorf("posted %d times after the first bad answer, want 1", len(got))
	}
}

// Rejection row: a partial warm (one row cold) is not ready, and the rows that
// already answered are not re-posted while the cold one is awaited.
func TestValidatorReady_PartialWarmIsNotReadyAndWarmRowsNotReposted(t *testing.T) {
	lane := newFakeValidatorLane(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	const coldRow = 7
	lane.respond = func(_ http.ResponseWriter, n int, _ []byte) bool {
		if n == coldRow {
			<-release
			return false
		}
		return true
	}
	ready := readyHookFor(t, lane, "2.2")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := ready(ctx, nil); err == nil {
		t.Fatal("Ready returned nil with one row still cold")
	}
	posts := lane.recorded()
	if len(posts) != coldRow {
		t.Fatalf("posted %d rows, want %d (each warm row once, the cold row once)", len(posts), coldRow)
	}
	// Rows 1-13 (initialization + prime pass) are pairwise-distinct requests
	// (route, profile form and fixture body), so any repeat among the first
	// coldRow posts is a re-post of an already-warm row.
	seen := map[string]bool{}
	for _, p := range posts {
		if seen[p] {
			t.Errorf("request %s posted more than once", p)
		}
		seen[p] = true
	}
}

// The corpus asserts a specific PAS package version per line; it must be the
// version the child actually loads from its IG pin set, or the qualification
// rows would test a different package than the one validating partner
// traffic.
func TestValidatorWarmPASVersionMatchesTheIGPinSet(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		want := ""
		for _, g := range kitdIGPinsValidator(line) {
			if g.key == "pas" {
				want = g.version
			}
		}
		if want == "" {
			t.Fatalf("line %s has no pas pin", line)
		}
		got, ok := validatorwarm.PASVersion(line)
		if !ok || got != want {
			t.Errorf("validatorwarm.PASVersion(%s) = %q,%v; the child loads PAS %s", line, got, ok, want)
		}
	}
}

// A metadata-ready child cannot qualify if either unavailable explicit profile
// receives a clean outcome, even after every prior row has passed.
func TestValidatorChildRejectsMissingExplicitProfileSuccess(t *testing.T) {
	for _, line := range []string{"2.0", "2.2"} {
		for _, row := range []int{41, 42} {
			t.Run(fmt.Sprintf("%s/row%d", line, row), func(t *testing.T) {
				lane := newFakeValidatorLane(t)
				lane.respond = func(w http.ResponseWriter, n int, _ []byte) bool {
					if n == row {
						_, _ = w.Write([]byte(laneCleanOutcome))
						return false
					}
					return true
				}
				ready := readyHookFor(t, lane, line)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := ready(ctx, func(string) {}); err == nil || !strings.Contains(err.Error(), "explicit-profile-missing-") {
					t.Fatalf("unavailable profile accepted: %v", err)
				}
				if len(lane.recorded()) != row {
					t.Fatalf("validation continued after failed explicit-profile control")
				}
			})
		}
	}
}
