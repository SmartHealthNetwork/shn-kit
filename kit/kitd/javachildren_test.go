// javachildren_test.go — hermetic ChildSpec-assembly tests for the Java trio.
// No Java, no Docker, no network: these assert the
// ChildSpec shape only — Command/Args/Env/Dir/ReadyURLs/LogPath — never spawn
// anything. tools/kitassets/build.sh's boot proof (a live gate) is what
// certifies the config channel actually boots real HAPI/br-provider.
package kitd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	spec, err := BuildValidatorChildSpec(assetsDir, "/opt/jre", stateDir, 18080, "darwin")
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
	// All 8 IGs present, each pointing into assetsDir.
	absAssets, _ := filepath.Abs(assetsDir)
	for _, g := range validatorIGs {
		key := "hapi.fhir.implementationguides." + g.key + ".packageUrl"
		want := "file://" + filepath.Join(absAssets, "igs-validator", g.name+"-"+g.version+".tgz")
		if cfg[key] != want {
			t.Errorf("%s = %q, want %q", key, cfg[key], want)
		}
	}
	if len(validatorIGs) != 8 {
		t.Fatalf("validatorIGs has %d entries, want 8", len(validatorIGs))
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
	for _, g := range dataIGs {
		key := "hapi.fhir.implementationguides." + g.key + ".packageUrl"
		want := "file://" + filepath.Join(absAssets, "igs-data", g.name+"-"+g.version+".tgz")
		if cfg[key] != want {
			t.Errorf("%s = %q, want %q", key, cfg[key], want)
		}
	}
	if len(dataIGs) != 4 {
		t.Fatalf("dataIGs has %d entries, want 4", len(dataIGs))
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
