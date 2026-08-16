// javachildren_test.go — hermetic ChildSpec-assembly tests for the Java trio.
// No Java, no Docker, no network: these assert the
// ChildSpec shape only — Command/Args/Env/Dir/ReadyURLs/LogPath — never spawn
// anything. tools/kitassets/build.sh's boot proof (a live gate) is what
// certifies the config channel actually boots real HAPI/br-provider.
package kitd

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// All 9 IGs present (validator-sidecar + shnig), each pointing into assetsDir.
	absAssets, _ := filepath.Abs(assetsDir)
	validatorIGs20 := kitdIGPinsValidator("2.0")
	for _, g := range validatorIGs20 {
		key := "hapi.fhir.implementationguides." + g.key + ".packageUrl"
		want := "file://" + filepath.Join(absAssets, "igs-validator", g.name+"-"+g.version+".tgz")
		if cfg[key] != want {
			t.Errorf("%s = %q, want %q", key, cfg[key], want)
		}
	}
	if len(validatorIGs20) != 9 {
		t.Fatalf("kitdIGPinsValidator(\"2.0\") has %d entries, want 9 (validator-sidecar + shnig)", len(validatorIGs20))
	}
}

// ---- validator ChildSpec: per-line -------------------------------------------

// TestBuildValidatorChildSpec_NonDefaultLine_ColdTimeoutOwnDirAndExtSet proves
// three things together for a non-default line: (1) it gets its OWN state-dir
// child name ("validator-2.2"), never colliding with the prewarmed default's
// "validator" dir; (2) its ReadyTimeout is the COLD bound (no prewarm exists
// for it); (3) line 2.2 carries the 10-IG extensions-closure superset
// (validator-sidecar-ext + shnig), not the 9-IG validator-sidecar + shnig set.
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
	if len(validatorIGs22) != 10 {
		t.Fatalf("kitdIGPinsValidator(\"2.2\") has %d entries, want 10 (validator-sidecar-ext + shnig)", len(validatorIGs22))
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
