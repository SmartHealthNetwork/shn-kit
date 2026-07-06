// javachildren.go — ChildSpecs for the Kit's packaged Java trio: the HAPI
// $validate-only validator, the seeded HAPI data server (URL_BASED
// multitenancy + operated CQL), and br-provider (a real HL7-DaVinci
// reference-implementation BFF). All three are spawned as ordinary
// supervisor.ChildSpec children — nothing here is Java-specific to the
// supervisor, only to how the spec's Command/Args/Env are assembled.
//
// Launch mechanics + config channel are spike-certified verbatim by
// tools/kitassets/build.sh (read its header comment for the executable
// evidence trail): one java invocation shape for both HAPI WARs
//
//	{JREDir}/bin/java[.exe] -Xmx<n> --class-path <workDir>/main.war \
//	  -Dloader.path=main.war!/WEB-INF/classes/,main.war!/WEB-INF/,/app/extra-classes \
//	  org.springframework.boot.loader.PropertiesLauncher
//
// and ONE env var, SPRING_APPLICATION_JSON, carrying the full HAPI config
// surface as flat dotted-key strings (Spring's relaxed binding turns
// "hapi.fhir.implementationguides.uscore.packageUrl" into the nested
// property tree at runtime — no Go-side nested map is needed). br-provider is
// a different Spring app entirely and takes its config as plain named env
// vars (compose.two-ri.yml parity), never SPRING_APPLICATION_JSON.
//
// The loader.path `main.war!/…` entries are CWD-RELATIVE (the pinned images
// run from WORKDIR /app), so each child's ChildSpec.Dir MUST be a directory
// containing main.war — ensureWarLink below creates that per-child state
// subdir and a symlink to the shared asset WAR (falling back to a byte copy
// if symlinking fails, e.g. Windows without the "Create symbolic links"
// privilege / Developer Mode). /app/extra-classes is a tolerated-missing
// loader.path entry — kept in the arg verbatim, never created.
package kitd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

const (
	validatorChildName  = "validator"
	dataServerChildName = "data-server"
	brProviderChildName = "br-provider"

	javaReadyTimeout = 120 * time.Second
	javaRestartMax   = 3

	// JVM heap constants: validator/data-server share the pinned
	// prewarm boot budget; br-provider is a leaner single-purpose BFF.
	validatorHeapMB  = 768
	dataServerHeapMB = 768
	brProviderHeapMB = 512
)

// ig is one implementation guide baked into the asset pipeline
// (tools/kitassets/build.sh), keyed the same way as the
// hapi.fhir.implementationguides.<key> config map: dir is the asset
// subdirectory ("igs-validator" | "igs-data") the pipeline downloaded it
// into; name/version compose the tgz filename ("<name>-<version>.tgz").
type ig struct {
	key, dir, name, version string
}

// validatorIGs is the validator's full 8-IG set (single-tenant $validate
// only), byte-identical to tools/kitassets/build.sh's VALIDATOR_IGS list.
var validatorIGs = []ig{
	{"uscore", "igs-validator", "hl7.fhir.us.core", "6.1.0"},
	{"crd", "igs-validator", "hl7.fhir.us.davinci-crd", "2.0.1"},
	{"dtr", "igs-validator", "hl7.fhir.us.davinci-dtr", "2.0.1"},
	{"pas", "igs-validator", "hl7.fhir.us.davinci-pas", "2.0.1"},
	{"pdex", "igs-validator", "hl7.fhir.us.davinci-pdex", "2.1.0"},
	{"sdc", "igs-validator", "hl7.fhir.uv.sdc", "3.0.0"},
	{"cdex", "igs-validator", "hl7.fhir.us.davinci-cdex", "2.1.0"},
	{"hrex", "igs-validator", "hl7.fhir.us.davinci-hrex", "1.1.0"},
}

// dataIGs is the data server's 4-IG set, byte-identical to
// tools/kitassets/build.sh's DATA_IGS list.
var dataIGs = []ig{
	{"uscore", "igs-data", "hl7.fhir.us.core", "6.1.0"},
	{"cdex", "igs-data", "hl7.fhir.us.davinci-cdex", "2.1.0"},
	{"hrex", "igs-data", "hl7.fhir.us.davinci-hrex", "1.1.0"},
	{"pas", "igs-data", "hl7.fhir.us.davinci-pas", "2.0.1"},
}

// javaCommand returns the java binary path under jreDir for goos — a
// GOOS-parameterized helper (rather than reading runtime.GOOS directly) so
// the windows java.exe shape is unit-testable on every dev/CI platform.
func javaCommand(jreDir, goos string) string {
	name := "java"
	if goos == "windows" {
		name = "java.exe"
	}
	return filepath.Join(jreDir, "bin", name)
}

// javaArgs is the pinned launch arg shape (see package doc), identical for
// every Java child except the heap size, tmp dir, and the war path.
//
// -Djava.io.tmpdir=tmpDir pins the JVM's temp directory to a writable path
// under the child's own state dir. Without it, a Windows JVM launched with no
// TEMP/TMP in its environment (see javaEnv's minimal env) defaults
// java.io.tmpdir to C:\Windows — which a normal (non-admin) Windows user
// account cannot write to, so embedded Tomcat fails to create its work dir on
// first boot. The system property must precede the main class on the java
// command line (system properties are JVM options, not program arguments),
// so it's placed right after -Xmx.
func javaArgs(heapMB int, tmpDir, warPath string) []string {
	return []string{
		fmt.Sprintf("-Xmx%dm", heapMB),
		"-Djava.io.tmpdir=" + tmpDir,
		"--class-path", warPath,
		"-Dloader.path=main.war!/WEB-INF/classes/,main.war!/WEB-INF/,/app/extra-classes",
		"org.springframework.boot.loader.PropertiesLauncher",
	}
}

// ensureWarLink makes workDir contain main.war (the CWD-relative loader.path
// contract) pointing at warSrc: a symlink when possible, falling back to a
// byte copy when symlinking fails. Idempotent — a link/copy already in place
// (an earlier boot's) is left alone; warSrc need not exist on disk for the
// symlink itself to succeed (a dangling symlink is fine — the JVM is what
// resolves it, later, at spawn time).
func ensureWarLink(workDir, warSrc string) (string, error) {
	if err := os.MkdirAll(workDir, 0700); err != nil {
		return "", fmt.Errorf("kitd: create java child workdir %s: %w", workDir, err)
	}
	dst := filepath.Join(workDir, "main.war")
	if _, err := os.Lstat(dst); err == nil {
		return dst, nil // already linked/copied by an earlier boot
	}
	if err := os.Symlink(warSrc, dst); err != nil {
		data, rerr := os.ReadFile(warSrc)
		if rerr != nil {
			return "", fmt.Errorf("kitd: symlink %s -> %s failed (%v) and fallback read of the war failed: %w", dst, warSrc, err, rerr)
		}
		if werr := os.WriteFile(dst, data, 0600); werr != nil {
			return "", fmt.Errorf("kitd: symlink %s -> %s failed (%v) and fallback copy failed: %w", dst, warSrc, err, werr)
		}
	}
	return dst, nil
}

// hapiSpringConfig builds the SPRING_APPLICATION_JSON value for a HAPI child:
// the datasource pointed at h2Dir, listening on port, IG indexing for igs
// (file:// URLs resolved against assetsDir), and — only for the data
// server — URL_BASED multitenancy + partitioning flags + operated CQL
// (the pinned shape from tools/kitassets/build.sh's validator_config/
// data_config functions).
func hapiSpringConfig(assetsDir, h2Dir string, port int, igs []ig, dataServer bool) (string, error) {
	absAssets, err := filepath.Abs(assetsDir)
	if err != nil {
		return "", fmt.Errorf("kitd: resolve assets dir %s: %w", assetsDir, err)
	}
	cfg := map[string]string{
		"spring.datasource.url":             fmt.Sprintf("jdbc:h2:file:%s/db;DB_CLOSE_DELAY=-1;DB_CLOSE_ON_EXIT=FALSE", h2Dir),
		"spring.datasource.username":        "sa",
		"spring.datasource.driverClassName": "org.h2.Driver",
		"server.port":                       strconv.Itoa(port),
	}
	for _, g := range igs {
		base := "hapi.fhir.implementationguides." + g.key
		cfg[base+".packageUrl"] = "file://" + filepath.Join(absAssets, g.dir, g.name+"-"+g.version+".tgz")
		cfg[base+".name"] = g.name
		cfg[base+".version"] = g.version
	}
	if dataServer {
		cfg["hapi.fhir.tenant_identification_strategy"] = "URL_BASED"
		cfg["hapi.fhir.partitioning.partitioning_include_in_search_hashes"] = "false"
		cfg["hapi.fhir.partitioning.allow_references_across_partitions"] = "false"
		cfg["hapi.fhir.cr.enabled"] = "true"
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("kitd: marshal SPRING_APPLICATION_JSON: %w", err)
	}
	return string(b), nil
}

// javaEnv is a HAPI child's FULL env: ONLY SPRING_APPLICATION_JSON, plus a
// propagated PATH (mirrors the gateway child's own minimal-PATH posture in
// BuildStack).
func javaEnv(springJSON string) []string {
	env := []string{"SPRING_APPLICATION_JSON=" + springJSON}
	if path := os.Getenv("PATH"); path != "" {
		env = append(env, "PATH="+path)
	}
	return env
}

// BuildValidatorChildSpec assembles the validator child's ChildSpec:
// single-tenant $validate-only HAPI carrying all 8 IGs. h2Dir
// ({stateDir}/validator/h2) is populated by seed.CopyPrewarmedH2 BEFORE this
// child is ever spawned.
func BuildValidatorChildSpec(assetsDir, jreDir, stateDir string, port int, goos string) (supervisor.ChildSpec, error) {
	workDir := filepath.Join(stateDir, validatorChildName)
	h2Dir := filepath.Join(workDir, "h2")
	warLink, err := ensureWarLink(workDir, filepath.Join(assetsDir, "hapi", "main.war"))
	if err != nil {
		return supervisor.ChildSpec{}, err
	}
	tmpDir := filepath.Join(workDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return supervisor.ChildSpec{}, fmt.Errorf("kitd: create java child tmp dir %s: %w", tmpDir, err)
	}
	springJSON, err := hapiSpringConfig(assetsDir, h2Dir, port, validatorIGs, false)
	if err != nil {
		return supervisor.ChildSpec{}, fmt.Errorf("kitd: validator config: %w", err)
	}
	return supervisor.ChildSpec{
		Name:         validatorChildName,
		Command:      javaCommand(jreDir, goos),
		Args:         javaArgs(validatorHeapMB, tmpDir, warLink),
		Env:          javaEnv(springJSON),
		Dir:          workDir,
		LogPath:      filepath.Join(stateDir, "validator.log"),
		ReadyURLs:    []string{fmt.Sprintf("http://127.0.0.1:%d/fhir/metadata", port)},
		ReadyTimeout: javaReadyTimeout,
		RestartMax:   javaRestartMax,
	}, nil
}

// BuildDataServerChildSpec assembles the data server child's ChildSpec:
// URL_BASED multitenancy + partitioning flags + operated CQL, carrying the
// 4-IG set. h2Dir ({stateDir}/data-server/h2) is populated by
// seed.CopyPrewarmedH2 BEFORE this child is ever spawned; the "provider"
// tenant's persona data is (re)loaded post-ready by FreshenPersonas.
//
// Ready probing uses the TENANTED /fhir/DEFAULT/metadata route, never bare
// /fhir/metadata: HAPI special-cases the untenanted metadata route to 200
// even under URL_BASED partitioning, so it cannot discriminate tenancy —
// only a data route (or the DEFAULT-tenanted metadata route) can.
func BuildDataServerChildSpec(assetsDir, jreDir, stateDir string, port int, goos string) (supervisor.ChildSpec, error) {
	workDir := filepath.Join(stateDir, dataServerChildName)
	h2Dir := filepath.Join(workDir, "h2")
	warLink, err := ensureWarLink(workDir, filepath.Join(assetsDir, "hapi", "main.war"))
	if err != nil {
		return supervisor.ChildSpec{}, err
	}
	tmpDir := filepath.Join(workDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return supervisor.ChildSpec{}, fmt.Errorf("kitd: create java child tmp dir %s: %w", tmpDir, err)
	}
	springJSON, err := hapiSpringConfig(assetsDir, h2Dir, port, dataIGs, true)
	if err != nil {
		return supervisor.ChildSpec{}, fmt.Errorf("kitd: data server config: %w", err)
	}
	return supervisor.ChildSpec{
		Name:         dataServerChildName,
		Command:      javaCommand(jreDir, goos),
		Args:         javaArgs(dataServerHeapMB, tmpDir, warLink),
		Env:          javaEnv(springJSON),
		Dir:          workDir,
		LogPath:      filepath.Join(stateDir, "data-server.log"),
		ReadyURLs:    []string{fmt.Sprintf("http://127.0.0.1:%d/fhir/DEFAULT/metadata", port)},
		ReadyTimeout: javaReadyTimeout,
		RestartMax:   javaRestartMax,
	}, nil
}

// BuildBRProviderChildSpec assembles br-provider's ChildSpec: the same java
// launch shape (its own WAR, its own heap), but Env carries br-provider's own
// named Spring env vars (compose.two-ri.yml parity) instead of
// SPRING_APPLICATION_JSON — a different Spring app, a different config
// contract. certPath/certPassword are the PKCS12 bundle BuildStack generates
// for br-provider's own CDS-client JWT signing key (SECURITY_CERT_FILE).
func BuildBRProviderChildSpec(assetsDir, jreDir, stateDir string, port int, goos string, gatewayURL, brProviderURL, certPath, certPassword string) (supervisor.ChildSpec, error) {
	workDir := filepath.Join(stateDir, brProviderChildName)
	warLink, err := ensureWarLink(workDir, filepath.Join(assetsDir, "brprovider", "main.war"))
	if err != nil {
		return supervisor.ChildSpec{}, err
	}
	tmpDir := filepath.Join(workDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return supervisor.ChildSpec{}, fmt.Errorf("kitd: create java child tmp dir %s: %w", tmpDir, err)
	}
	env := []string{
		"SERVER_PORT=" + strconv.Itoa(port),
		"APP_PAYER_SERVERS_0_CDS_URL=" + gatewayURL + "/cds-services",
		"APP_PAYER_SERVERS_0_FHIR_URL=" + gatewayURL,
		"SECURITY_ALLOWEDLOCALHOSTS_0=127.0.0.1",
		"SECURITY_EXTERNAL_BASE_URL=" + brProviderURL,
		"SECURITY_CERT_FILE=" + certPath,
		"SECURITY_CERT_PASSWORD=" + certPassword,
		"SECURITY_FETCH_CERT=false",
	}
	if path := os.Getenv("PATH"); path != "" {
		env = append(env, "PATH="+path)
	}
	return supervisor.ChildSpec{
		Name:         brProviderChildName,
		Command:      javaCommand(jreDir, goos),
		Args:         javaArgs(brProviderHeapMB, tmpDir, warLink),
		Env:          env,
		Dir:          workDir,
		LogPath:      filepath.Join(stateDir, "br-provider.log"),
		ReadyURLs:    []string{brProviderURL + "/fhir/metadata"},
		ReadyTimeout: javaReadyTimeout,
		RestartMax:   javaRestartMax,
	}, nil
}
