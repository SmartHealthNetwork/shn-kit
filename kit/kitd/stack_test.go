// stack_test.go — hermetic tests for BuildStack.
// BuildStack is pure composition + file writes; no processes are spawned by
// any row here (that is test/kitlive's job, monorepo-side).
package kitd

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"software.sslmate.com/src/go-pkcs12"

	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

// portOf extracts the numeric port from a "http://127.0.0.1:<port>[...]" URL.
func portOf(t *testing.T, u string) int {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", u, err)
	}
	p, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("no numeric port in %q: %v", u, err)
	}
	return p
}

// ---- Row 1: env recipe -------------------------------------------------------

// baseCfg is the plain no-trio StackConfig the env-recipe tests share: no
// Java assets, no bridge holders, no BYO swap. Each caller still gets its own
// StateDir (t.TempDir()), so callers may freely mutate the returned value.
func baseCfg(t *testing.T) StackConfig {
	t.Helper()
	return StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		AuditURL:      "http://127.0.0.1:9002",
		PHGURL:        "http://127.0.0.1:9003",
		ConsentURL:    "http://127.0.0.1:9004",
		FakeValidator: true,
		// FHIRDataURL left "" deliberately (the pre-trio posture): the recipe
		// must omit the entry, not emit it empty.
	}
}

func TestBuildStack_EnvRecipe(t *testing.T) {
	cfg := baseCfg(t)
	stateDir := cfg.StateDir

	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	defer stack.Close() //nolint:errcheck // releases the no-trio lane's fixture SoR listener
	if len(stack.Children) != 1 {
		t.Fatalf("Children = %d, want exactly 1 (no ExtraChildren configured)", len(stack.Children))
	}
	spec := stack.Children[0]

	gwPort := portOf(t, stack.GatewayURL)
	obsPort := portOf(t, stack.ObserverURL)

	want := []string{
		"ROLE=provider",
		fmt.Sprintf("PORT=%d", gwPort),
		"HOST=127.0.0.1",
		"SHN_SECRETS=/secrets/provider",
		"SHN_DISCOVERY_URL=http://127.0.0.1:9001/discovery",
		"AUDIT_URL=http://127.0.0.1:9002",
		"PHG_URL=http://127.0.0.1:9003",
		"CONSENT_URL=http://127.0.0.1:9004",
		"PAYER_DIRECTORY=" + filepath.Join(stateDir, "payer-directory.json"),
		"SHN_FAKE_VALIDATOR=1",
		fmt.Sprintf("OBSERVER_ADDR=127.0.0.1:%d", obsPort),
		"PROVIDER_DAVINCI_INGRESS=1",
		fmt.Sprintf("PROVIDER_DAVINCI_INGRESS_BASE_URL=%s", stack.GatewayURL),
		"INGRESS_CLIENTS_FILE=" + filepath.Join(stateDir, "ingress-clients.json"),
		// The no-trio lane's own system of record: the daemon serves the Kit's seed
		// bundles over a loopback read-only FHIR endpoint (fixturefhir.go). It is NOT
		// optional — a gateway with no FHIR_DATA_URL refuses to boot.
		fmt.Sprintf("FHIR_DATA_URL=http://127.0.0.1:%d/fhir/provider", fixtureSoRPortOf(t, stack)),
		// Nor is the operated $populate endpoint optional: this lane originates against a
		// real payer, so the questionnaire it must fill is the PAYER's resource, and the
		// gateway refuses to boot without an endpoint to populate it at. Same listener as
		// the system of record above, one operation deeper (fixturefhir.go's
		// handlePopulate). PROVIDER_DTR_NATIVE is deliberately NOT emitted beside it — see
		// the recipe in stack.go for why naming only the URL is the truthful half.
		fmt.Sprintf("PROVIDER_DTR_POPULATE_URL=http://127.0.0.1:%d/fhir/provider/Questionnaire/$populate", fixtureSoRPortOf(t, stack)),
	}
	if path := os.Getenv("PATH"); path != "" {
		want = append(want, "PATH="+path)
	}

	if len(spec.Env) != len(want) {
		t.Fatalf("Env = %q\nwant exactly %q (len %d vs %d)", spec.Env, want, len(spec.Env), len(want))
	}
	for i, w := range want {
		if spec.Env[i] != w {
			t.Errorf("Env[%d] = %q, want %q", i, spec.Env[i], w)
		}
	}
	// ORIGINATION_PROFILE stays omitted when unset. FHIR_DATA_URL does NOT: it is
	// ALWAYS wired — from the packaged data server with the Java trio, from the daemon's
	// own fixture endpoint without it — because a gateway with no system of record refuses
	// to boot. Its exact value is pinned in the recipe above.
	for _, e := range spec.Env {
		if strings.HasPrefix(e, "ORIGINATION_PROFILE=") {
			t.Errorf("Env contains %q, want it omitted when unset", e)
		}
	}

	wantReady := []string{
		stack.GatewayURL + "/.well-known/smart-configuration",
		fmt.Sprintf("http://127.0.0.1:%d/health", obsPort),
	}
	if len(spec.ReadyURLs) != len(wantReady) || spec.ReadyURLs[0] != wantReady[0] || spec.ReadyURLs[1] != wantReady[1] {
		t.Fatalf("ReadyURLs = %q, want %q (NOT /cds-services: gateway/engine/ingress.go:72-76 401s an unauthenticated probe)", spec.ReadyURLs, wantReady)
	}
	for _, u := range spec.ReadyURLs {
		if strings.Contains(u, "/cds-services") {
			t.Errorf("ReadyURLs contains a /cds-services probe (%q): that route is ingress-auth-gated and would deadlock the ready loop", u)
		}
	}

	if !strings.HasPrefix(spec.LogPath, stateDir) {
		t.Errorf("LogPath = %q, want it under StateDir %q", spec.LogPath, stateDir)
	}
}

// TestBuildStack_EnvRecipe_OptionalFieldsPresent proves the omit-when-empty
// rule runs in both directions: FHIR_DATA_URL shows up (with the configured
// value) when the caller sets it. The origination profile is NEVER an option on
// this child: it belongs to the provider-data child alone
// (TestBuildStack_TrioAbsent_NoProviderDataChild pins the absence here).
func TestBuildStack_EnvRecipe_OptionalFieldsPresent(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		FHIRDataURL:   "http://127.0.0.1:9010/fhir/provider",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	spec := stack.Children[0]
	if !hasEnv(spec.Env, "FHIR_DATA_URL=http://127.0.0.1:9010/fhir/provider") {
		t.Errorf("Env = %q, want FHIR_DATA_URL set", spec.Env)
	}
	if hasEnv(spec.Env, "SHN_FAKE_VALIDATOR=1") {
		t.Errorf("Env = %q, want SHN_FAKE_VALIDATOR omitted (cfg.FakeValidator false)", spec.Env)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// ---- Row 2: ingress client materialization -----------------------------------

func TestBuildStack_IngressClientMaterialized(t *testing.T) {
	stateDir := t.TempDir()
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      stateDir,
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		PHGURL:        "http://127.0.0.1:9003",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, "ingress-clients.json"))
	if err != nil {
		t.Fatalf("read ingress-clients.json: %v", err)
	}
	var clients []struct {
		ClientID     string   `json:"client_id"`
		Alg          string   `json:"alg"`
		PublicKeyPEM string   `json:"public_key_pem"`
		Scopes       []string `json:"scopes"`
	}
	if err := json.Unmarshal(raw, &clients); err != nil {
		t.Fatalf("unmarshal ingress-clients.json: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("ingress-clients.json = %d entries, want 1", len(clients))
	}
	c := clients[0]
	if c.ClientID != "shn-kit-driver" {
		t.Errorf("client_id = %q, want shn-kit-driver", c.ClientID)
	}
	if c.Alg != "RS384" {
		t.Errorf("alg = %q, want RS384", c.Alg)
	}
	if len(c.Scopes) != 1 || c.Scopes[0] != "system/Davinci.write" {
		t.Errorf("scopes = %v, want [system/Davinci.write]", c.Scopes)
	}

	// PEM round-trip: the file's public key must equal Stack.Driver.Key's.
	block, _ := pem.Decode([]byte(c.PublicKeyPEM))
	if block == nil {
		t.Fatalf("public_key_pem does not PEM-decode: %q", c.PublicKeyPEM)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParsePKIXPublicKey: %v", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("parsed public key is %T, want *rsa.PublicKey", parsed)
	}
	if stack.Driver.Key == nil {
		t.Fatal("stack.Driver.Key is nil")
	}
	if !pub.Equal(&stack.Driver.Key.PublicKey) {
		t.Errorf("materialized public key does not match Stack.Driver.Key's public key")
	}

	if stack.Driver.ClientID != "shn-kit-driver" {
		t.Errorf("Driver.ClientID = %q, want shn-kit-driver", stack.Driver.ClientID)
	}
	if stack.Driver.IngressURL != stack.GatewayURL {
		t.Errorf("Driver.IngressURL = %q, want %q", stack.Driver.IngressURL, stack.GatewayURL)
	}
	if stack.Driver.IngressBase != stack.Driver.IngressURL {
		t.Errorf("Driver.IngressBase = %q, want the same config-pinned base as IngressURL %q", stack.Driver.IngressBase, stack.Driver.IngressURL)
	}
	if stack.Driver.ProviderDataURL != stack.GatewayURL {
		t.Errorf("Driver.ProviderDataURL = %q, want %q", stack.Driver.ProviderDataURL, stack.GatewayURL)
	}
	if stack.Driver.PHGURL != cfg.PHGURL {
		t.Errorf("Driver.PHGURL = %q, want %q", stack.Driver.PHGURL, cfg.PHGURL)
	}
}

// ---- Row 3: port respect -------------------------------------------------------

func TestBuildStack_PortRespect(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		GatewayPort:   12345,
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if stack.GatewayURL != "http://127.0.0.1:12345" {
		t.Errorf("GatewayURL = %q, want http://127.0.0.1:12345", stack.GatewayURL)
	}
	spec := stack.Children[0]
	if !hasEnv(spec.Env, "PORT=12345") {
		t.Errorf("Env = %q, want PORT=12345", spec.Env)
	}
	if !hasEnv(spec.Env, "PROVIDER_DAVINCI_INGRESS_BASE_URL=http://127.0.0.1:12345") {
		t.Errorf("Env = %q, want PROVIDER_DAVINCI_INGRESS_BASE_URL=http://127.0.0.1:12345", spec.Env)
	}
	if spec.ReadyURLs[0] != "http://127.0.0.1:12345/.well-known/smart-configuration" {
		t.Errorf("ReadyURLs[0] = %q, want the pinned gateway port", spec.ReadyURLs[0])
	}
	if stack.Driver.IngressURL != "http://127.0.0.1:12345" {
		t.Errorf("Driver.IngressURL = %q, want http://127.0.0.1:12345", stack.Driver.IngressURL)
	}
}

// ---- Row 4: ObserverHealthURL derivation ---------------------------------------

// TestBuildStack_ObserverHealthURL pins the exact string derivation the
// relay drain barrier depends on: ObserverHealthURL is ObserverURL with its
// /events suffix swapped for /health, on the same host:port.
func TestBuildStack_ObserverHealthURL(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if !strings.HasSuffix(stack.ObserverURL, "/events") {
		t.Fatalf("ObserverURL = %q, want it to end in /events", stack.ObserverURL)
	}
	want := strings.TrimSuffix(stack.ObserverURL, "/events") + "/health"
	if stack.ObserverHealthURL != want {
		t.Errorf("ObserverHealthURL = %q, want %q (derived from ObserverURL)", stack.ObserverHealthURL, want)
	}
}

// TestBuildStack_GatewayEnvMirrorsSpec pins Stack.GatewayEnv as the exact
// baseline the bridging demo toggle rebuilds the gateway env from: it is
// value-identical to the gateway ChildSpec's Env, and it is an INDEPENDENT
// slice — appending to it (what the toggle does) must never be able to reach
// the registered spec's own backing array.
func TestBuildStack_GatewayEnvMirrorsSpec(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary:     "/bin/true",
		StateDir:          t.TempDir(),
		SecretsDir:        "/secrets/provider",
		DiscoveryURL:      "http://127.0.0.1:9001/discovery",
		FHIRTokenURL:      "https://ehr.example/token",
		FHIRClientID:      "kit-client",
		FHIRClientKeyPath: "/secrets/ehr.key",
		FHIRClientAlg:     "RS384",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	spec := stack.Children[0]
	if spec.Name != gatewayChildName {
		t.Fatalf("Children[0] = %q, want the gateway child", spec.Name)
	}
	if strings.Join(stack.GatewayEnv, "\x00") != strings.Join(spec.Env, "\x00") {
		t.Fatalf("GatewayEnv = %v, want the gateway spec's own env %v", stack.GatewayEnv, spec.Env)
	}
	// The BACKING ARRAY, not just the contents: an append onto a shared array
	// with spare capacity would rewrite the registered spec's env in place.
	// Comparing appended contents alone does NOT catch that (the append
	// usually lands in fresh capacity and the bug hides) — the pointer
	// identity check is the one that actually fails on a shared array.
	if len(spec.Env) > 0 && &stack.GatewayEnv[0] == &spec.Env[0] {
		t.Fatal("GatewayEnv shares its backing array with the gateway ChildSpec's Env — the demo toggle's append could rewrite a live child's env")
	}
	if len(spec.Env) == 0 {
		t.Fatal("gateway spec env is empty; the backing-array check above proved nothing")
	}
}

// ---- Row 6: SMART quad env emission ---------------------------------------------

// TestBuildStack_QuadEnv_FullySet proves the FHIR SMART quad is emitted
// verbatim when FHIRTokenURL is set, and that FHIR_CLIENT_SCOPE/FHIR_CLIENT_KID
// both appear when the caller supplies them.
func TestBuildStack_QuadEnv_FullySet(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary:     "/bin/true",
		StateDir:          t.TempDir(),
		SecretsDir:        "/secrets/provider",
		DiscoveryURL:      "http://127.0.0.1:9001/discovery",
		FHIRTokenURL:      "https://ehr.example.org/token",
		FHIRClientID:      "kit-client",
		FHIRClientKeyPath: "/state/byo-ehr-key.pem",
		FHIRClientAlg:     "RS384",
		FHIRClientScope:   "system/Patient.read",
		FHIRClientKID:     "kid-1",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	spec := stack.Children[0]
	for _, want := range []string{
		"FHIR_TOKEN_URL=https://ehr.example.org/token",
		"FHIR_CLIENT_ID=kit-client",
		"FHIR_CLIENT_KEY=/state/byo-ehr-key.pem",
		"FHIR_CLIENT_ALG=RS384",
		"FHIR_CLIENT_SCOPE=system/Patient.read",
		"FHIR_CLIENT_KID=kid-1",
	} {
		if !hasEnv(spec.Env, want) {
			t.Errorf("Env = %q, want %q", spec.Env, want)
		}
	}
}

// TestBuildStack_QuadEnv_OmittedWhenTokenURLEmpty proves the gateway's own
// FHIR_TOKEN_URL emptiness guard is never tripped by a half-set quad: with
// FHIRTokenURL "" none of the six quad vars appear, even if other quad fields
// are (incorrectly) non-empty.
func TestBuildStack_QuadEnv_OmittedWhenTokenURLEmpty(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		FHIRClientID:  "kit-client", // set despite FHIRTokenURL being empty
		FHIRClientAlg: "RS384",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	spec := stack.Children[0]
	for _, bad := range []string{
		"FHIR_TOKEN_URL=", "FHIR_CLIENT_ID=", "FHIR_CLIENT_KEY=",
		"FHIR_CLIENT_ALG=", "FHIR_CLIENT_SCOPE=", "FHIR_CLIENT_KID=",
	} {
		for _, e := range spec.Env {
			if strings.HasPrefix(e, bad) {
				t.Errorf("Env contains %q, want the whole quad omitted when FHIRTokenURL is empty", e)
			}
		}
	}
}

// TestBuildStack_QuadEnv_ScopeAndKIDOmittedWhenEmpty pins the scope-parity
// deviation from a literal six-vars-always-together reading: when the quad is
// set but Scope/KID are left "", those two vars are OMITTED (not emitted
// empty) so the gateway's own def("FHIR_CLIENT_SCOPE", "system/*.read")
// default applies, rather than an empty override defeating it. The other four
// quad vars are still present.
func TestBuildStack_QuadEnv_ScopeAndKIDOmittedWhenEmpty(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary:     "/bin/true",
		StateDir:          t.TempDir(),
		SecretsDir:        "/secrets/provider",
		DiscoveryURL:      "http://127.0.0.1:9001/discovery",
		FHIRTokenURL:      "https://ehr.example.org/token",
		FHIRClientID:      "kit-client",
		FHIRClientKeyPath: "/state/byo-ehr-key.pem",
		FHIRClientAlg:     "RS384",
		// FHIRClientScope and FHIRClientKID deliberately left "".
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	spec := stack.Children[0]
	for _, want := range []string{
		"FHIR_TOKEN_URL=https://ehr.example.org/token",
		"FHIR_CLIENT_ID=kit-client",
		"FHIR_CLIENT_KEY=/state/byo-ehr-key.pem",
		"FHIR_CLIENT_ALG=RS384",
	} {
		if !hasEnv(spec.Env, want) {
			t.Errorf("Env = %q, want %q", spec.Env, want)
		}
	}
	for _, bad := range []string{"FHIR_CLIENT_SCOPE=", "FHIR_CLIENT_KID="} {
		for _, e := range spec.Env {
			if strings.HasPrefix(e, bad) {
				t.Errorf("Env contains %q, want it omitted when the configured value is empty (scope-parity: let the gateway's own default apply)", e)
			}
		}
	}
}

// ---- Row 7: ingress-clients.json partner merge ---------------------------------

// TestBuildStack_IngressClientMerge proves ExtraIngressClients are appended
// AFTER the internal shn-kit-driver entry — never replacing it — and carry
// their exact ClientID/Alg/PublicKeyPEM with no scopes field (the gateway's
// loadIngressClients defaults empty scopes to ["system/Davinci.write"],
// gateway/app/app.go:373-376; the Kit doesn't re-derive that default).
func TestBuildStack_IngressClientMerge(t *testing.T) {
	stateDir := t.TempDir()
	const partnerPEM = "-----BEGIN PUBLIC KEY-----\ntestkey\n-----END PUBLIC KEY-----\n"
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      stateDir,
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		ExtraIngressClients: []IngressClient{
			{ClientID: "partner-1", Alg: "RS384", PublicKeyPEM: partnerPEM},
		},
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, "ingress-clients.json"))
	if err != nil {
		t.Fatalf("read ingress-clients.json: %v", err)
	}
	var clients []struct {
		ClientID     string   `json:"client_id"`
		Alg          string   `json:"alg"`
		PublicKeyPEM string   `json:"public_key_pem"`
		Scopes       []string `json:"scopes"`
	}
	if err := json.Unmarshal(raw, &clients); err != nil {
		t.Fatalf("unmarshal ingress-clients.json: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("ingress-clients.json = %d entries, want 2 (driver + partner)", len(clients))
	}
	if clients[0].ClientID != "shn-kit-driver" {
		t.Errorf("clients[0].client_id = %q, want shn-kit-driver FIRST", clients[0].ClientID)
	}
	if len(clients[0].Scopes) != 1 || clients[0].Scopes[0] != "system/Davinci.write" {
		t.Errorf("clients[0].scopes = %v, want [system/Davinci.write] unchanged", clients[0].Scopes)
	}
	got := clients[1]
	if got.ClientID != "partner-1" || got.Alg != "RS384" || got.PublicKeyPEM != partnerPEM {
		t.Errorf("clients[1] = %+v, want {ClientID:partner-1 Alg:RS384 PublicKeyPEM:%q}", got, partnerPEM)
	}
	if len(got.Scopes) != 0 {
		t.Errorf("clients[1].scopes = %v, want empty (let the gateway default apply)", got.Scopes)
	}

	if stack.Driver.ClientID != "shn-kit-driver" {
		t.Errorf("Driver.ClientID = %q, want shn-kit-driver unaffected by the merge", stack.Driver.ClientID)
	}
}

// ---- Row 5: ExtraChildren appended ---------------------------------------------

func TestBuildStack_ExtraChildrenAppended(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		ExtraChildren: []supervisor.ChildSpec{
			{Name: "validator"},
			{Name: "dataserver"},
		},
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if len(stack.Children) != 3 {
		t.Fatalf("Children = %d, want 3 (gateway + 2 extra)", len(stack.Children))
	}
	wantNames := []string{"gateway", "validator", "dataserver"}
	for i, want := range wantNames {
		if stack.Children[i].Name != want {
			t.Errorf("Children[%d].Name = %q, want %q (gateway first, then ExtraChildren in order)", i, stack.Children[i].Name, want)
		}
	}
}

// ---- Row 8: Java trio -----------------------------------------------------------

// trioCfg is a StackConfig with the Java trio configured — the asset/JRE
// paths are never actually read by BuildStack (only symlinked-to, and a
// dangling symlink target is fine hermetically), so bogus paths are safe here.
func trioCfg(t *testing.T, extra func(*StackConfig)) StackConfig {
	t.Helper()
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		JavaAssetsDir: "/assets",
		JREDir:        "/opt/jre",
	}
	if extra != nil {
		extra(&cfg)
	}
	return cfg
}

// TestBuildStack_TrioAbsent_ByteIdenticalToToday is the regression pin: with
// JavaAssetsDir == "", BuildStack's output must be identical to pre-S8
// behavior — exactly one child (gateway), none of the trio-only env vars,
// all trio URLs empty, and a single-entry ingress-clients.json.
func TestBuildStack_TrioAbsent_ByteIdenticalToToday(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if len(stack.Children) != 1 || stack.Children[0].Name != gatewayChildName {
		t.Fatalf("Children = %+v, want exactly [gateway]", stack.Children)
	}
	// Trio-only env stays absent. PROVIDER_DTR_POPULATE_URL is NO LONGER on this list, and
	// the reason is not a relaxation: the gateway requires an operated $populate endpoint on
	// this lane and refuses to boot without one, so "absent" would pin a stack that cannot
	// start. It is asserted positively below instead — still exactly, and still pinned to
	// the daemon's OWN endpoint so a trio URL can never leak in here.
	for _, bad := range []string{"FHIR_VALIDATE_URL=", "PROVIDER_DTR_NATIVE="} {
		for _, e := range stack.Children[0].Env {
			if strings.HasPrefix(e, bad) {
				t.Errorf("Env contains %q, want it absent when no trio is configured", e)
			}
		}
	}
	wantPopulate := fmt.Sprintf("PROVIDER_DTR_POPULATE_URL=http://127.0.0.1:%d/fhir/provider/Questionnaire/$populate", fixtureSoRPortOf(t, stack))
	if !hasEnv(stack.Children[0].Env, wantPopulate) {
		t.Errorf("Env = %q, want %q — the no-trio lane's operated $populate is the daemon's own endpoint", stack.Children[0].Env, wantPopulate)
	}
	if stack.ValidatorURL != "" || stack.DataServerURL != "" || stack.BRProviderURL != "" {
		t.Errorf("trio URLs = %q/%q/%q, want all empty", stack.ValidatorURL, stack.DataServerURL, stack.BRProviderURL)
	}
	if stack.Driver.BFFURL != "" {
		t.Errorf("Driver.BFFURL = %q, want empty (no trio)", stack.Driver.BFFURL)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.StateDir, "ingress-clients.json"))
	if err != nil {
		t.Fatalf("read ingress-clients.json: %v", err)
	}
	var clients []map[string]any
	if err := json.Unmarshal(raw, &clients); err != nil {
		t.Fatalf("unmarshal ingress-clients.json: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("ingress-clients.json = %d entries, want 1 (driver only, no br-provider entry)", len(clients))
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, "br-provider-cert.pfx")); err == nil {
		t.Error("br-provider-cert.pfx written despite no trio being configured")
	}
}

// TestBuildStack_TrioPresent_ChildrenOrder pins the required child-start order:
// the gateway starts FIRST — it passes its own ready probe in well under a
// second — and the trio follows, since it is the real multi-minute wait.
func TestBuildStack_TrioPresent_ChildrenOrder(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	wantNames := []string{"gateway", "validator", "data-server", "br-provider", providerDataChildName}
	if len(stack.Children) != len(wantNames) {
		t.Fatalf("Children = %d, want %d: %+v", len(stack.Children), len(wantNames), stack.Children)
	}
	for i, want := range wantNames {
		if stack.Children[i].Name != want {
			t.Errorf("Children[%d].Name = %q, want %q (gateway first, then the trio — its ready probe is fast; the trio is the real wait — then the provider-data gateway that reads the trio's data server)", i, stack.Children[i].Name, want)
		}
	}
}

// TestBuildStack_TrioPresent_ValidateURLAndNoFakeValidator proves the
// gateway env gains FHIR_VALIDATE_URL and (with cfg.FakeValidator left at
// its zero value, as main's flag.Visit derivation resolves it to when the
// trio is present) drops SHN_FAKE_VALIDATOR.
func TestBuildStack_TrioPresent_ValidateURLAndNoFakeValidator(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	gwEnv := stack.Children[0].Env
	want := "FHIR_VALIDATE_URL=" + stack.ValidatorURL + "/fhir"
	if !hasEnv(gwEnv, want) {
		t.Errorf("gateway Env = %q, want %q", gwEnv, want)
	}
	for _, e := range gwEnv {
		if strings.HasPrefix(e, "SHN_FAKE_VALIDATOR=") {
			t.Errorf("gateway Env contains %q, want SHN_FAKE_VALIDATOR omitted (cfg.FakeValidator false)", e)
		}
	}
}

// TestBuildStack_TrioPresent_FakeValidatorForced proves an explicitly-forced
// cfg.FakeValidator survives even with the trio present (main's flag.Visit
// "explicit flag wins" contract) — SHN_FAKE_VALIDATOR=1 still
// appears; FHIR_VALIDATE_URL is harmlessly also present (the gateway's own
// selectValidator checks SHN_FAKE_VALIDATOR first).
func TestBuildStack_TrioPresent_FakeValidatorForced(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, func(c *StackConfig) { c.FakeValidator = true }))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if !hasEnv(stack.Children[0].Env, "SHN_FAKE_VALIDATOR=1") {
		t.Errorf("gateway Env = %q, want SHN_FAKE_VALIDATOR=1 (explicitly forced)", stack.Children[0].Env)
	}
}

// TestBuildStack_TrioPresent_FHIRDataURLDefault pins the layering:
// FHIRDataURL defaults to the trio's own data server's "provider" tenant
// only when the caller left it empty.
func TestBuildStack_TrioPresent_FHIRDataURLDefault(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	want := "FHIR_DATA_URL=" + stack.DataServerURL + "/fhir/provider"
	if !hasEnv(stack.Children[0].Env, want) {
		t.Errorf("gateway Env = %q, want %q", stack.Children[0].Env, want)
	}
}

// TestBuildStack_TrioPresent_FHIRDataURLDefault_SharedByBothChildren proves
// the no-swap default is not just correct on the main child (the row above)
// but IDENTICAL on the provider-data child (spec amendment A1: the
// bring-your-own EHR swap — and so its absence — applies to BOTH gateway
// children off the one resolved fhirDataURL, never a second computation that
// could drift).
func TestBuildStack_TrioPresent_FHIRDataURLDefault_SharedByBothChildren(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	want := "FHIR_DATA_URL=" + stack.DataServerURL + "/fhir/provider"
	mainEnv := stack.Children[0].Env
	pdEnv := stack.Children[len(stack.Children)-1].Env
	if !hasEnv(mainEnv, want) {
		t.Errorf("main child Env = %q, want %q", mainEnv, want)
	}
	if !hasEnv(pdEnv, want) {
		t.Errorf("provider-data child Env = %q, want %q (same bundled default as the main child, A1)", pdEnv, want)
	}
}

// TestBuildStack_TrioPresent_FHIRDataURLOverrideWins pins that a
// caller-set FHIRDataURL is used VERBATIM, never overwritten by the trio
// default.
func TestBuildStack_TrioPresent_FHIRDataURLOverrideWins(t *testing.T) {
	const byoURL = "http://127.0.0.1:9999/fhir/byo"
	stack, err := BuildStack(trioCfg(t, func(c *StackConfig) { c.FHIRDataURL = byoURL }))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if !hasEnv(stack.Children[0].Env, "FHIR_DATA_URL="+byoURL) {
		t.Errorf("gateway Env = %q, want the caller's FHIRDataURL untouched", stack.Children[0].Env)
	}
	defaultURL := "FHIR_DATA_URL=" + stack.DataServerURL + "/fhir/provider"
	if hasEnv(stack.Children[0].Env, defaultURL) {
		t.Errorf("gateway Env contains the DEFAULT %q despite an explicit override being set", defaultURL)
	}
}

// TestBuildStack_TrioPresent_NativeDTRPair proves the native-DTR env pair is
// present, pointed at the trio's own data server.
func TestBuildStack_TrioPresent_NativeDTRPair(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	gwEnv := stack.Children[0].Env
	if !hasEnv(gwEnv, "PROVIDER_DTR_NATIVE=true") {
		t.Errorf("gateway Env = %q, want PROVIDER_DTR_NATIVE=true", gwEnv)
	}
	want := "PROVIDER_DTR_POPULATE_URL=" + stack.DataServerURL + "/fhir/provider/Questionnaire/$populate"
	if !hasEnv(gwEnv, want) {
		t.Errorf("gateway Env = %q, want %q", gwEnv, want)
	}
}

// TestBuildStack_TrioPresent_DriverBFFURL pins the br-provider BFF wiring point.
func TestBuildStack_TrioPresent_DriverBFFURL(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if stack.BRProviderURL == "" {
		t.Fatal("BRProviderURL is empty despite the trio being configured")
	}
	if stack.Driver.BFFURL != stack.BRProviderURL {
		t.Errorf("Driver.BFFURL = %q, want it == BRProviderURL %q", stack.Driver.BFFURL, stack.BRProviderURL)
	}
}

// TestBuildStack_TrioPresent_IngressClientsAndPFX proves the br-provider
// ingress-clients.json entry (ClientID == BRProviderURL, after the driver
// entry) and its PKCS12 file: 0600, and it decodes with the SAME password
// carried in the br-provider ChildSpec's own env, yielding the SAME public
// key registered in ingress-clients.json (round-trip proof).
func TestBuildStack_TrioPresent_IngressClientsAndPFX(t *testing.T) {
	cfg := trioCfg(t, nil)
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(cfg.StateDir, "ingress-clients.json"))
	if err != nil {
		t.Fatalf("read ingress-clients.json: %v", err)
	}
	var clients []struct {
		ClientID     string `json:"client_id"`
		Alg          string `json:"alg"`
		PublicKeyPEM string `json:"public_key_pem"`
	}
	if err := json.Unmarshal(raw, &clients); err != nil {
		t.Fatalf("unmarshal ingress-clients.json: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("ingress-clients.json = %d entries, want 2 (driver + br-provider)", len(clients))
	}
	if clients[0].ClientID != ingressClientID {
		t.Errorf("clients[0].client_id = %q, want %q first", clients[0].ClientID, ingressClientID)
	}
	if clients[1].ClientID != stack.BRProviderURL {
		t.Errorf("clients[1].client_id = %q, want %q (br-provider, after the driver entry)", clients[1].ClientID, stack.BRProviderURL)
	}
	if clients[1].Alg != "RS384" {
		t.Errorf("clients[1].alg = %q, want RS384", clients[1].Alg)
	}

	pfxPath := filepath.Join(cfg.StateDir, "br-provider-cert.pfx")
	fi, err := os.Stat(pfxPath)
	if err != nil {
		t.Fatalf("stat PFX: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("PFX perm = %v, want 0600", fi.Mode().Perm())
	}
	pfxData, err := os.ReadFile(pfxPath)
	if err != nil {
		t.Fatalf("read PFX: %v", err)
	}

	brProviderSpec := stack.Children[3]
	if brProviderSpec.Name != brProviderChildName {
		t.Fatalf("Children[3].Name = %q, want %q", brProviderSpec.Name, brProviderChildName)
	}
	var certPassword string
	for _, e := range brProviderSpec.Env {
		if v, ok := strings.CutPrefix(e, "SECURITY_CERT_PASSWORD="); ok {
			certPassword = v
		}
	}
	if certPassword == "" {
		t.Fatal("br-provider Env carries no SECURITY_CERT_PASSWORD")
	}

	privKey, cert, err := pkcs12.Decode(pfxData, certPassword)
	if err != nil {
		t.Fatalf("pkcs12.Decode: %v", err)
	}
	rsaKey, ok := privKey.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("decoded PFX private key is %T, want *rsa.PrivateKey", privKey)
	}

	block, _ := pem.Decode([]byte(clients[1].PublicKeyPEM))
	if block == nil {
		t.Fatal("clients[1].public_key_pem does not PEM-decode")
	}
	parsedPub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse ingress-clients.json public key: %v", err)
	}
	rsaPub, ok := parsedPub.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("ingress-clients.json public key is %T, want *rsa.PublicKey", parsedPub)
	}
	if !rsaKey.PublicKey.Equal(rsaPub) {
		t.Error("PFX's private key's public half does not match the ingress-clients.json entry's public key")
	}
	certPub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || !certPub.Equal(rsaPub) {
		t.Error("PFX's certificate public key does not match the ingress-clients.json entry's public key")
	}
}

// ---- line-configurable validator ---------------------------------------------

// TestResolveValidatorLines_Table pins the dedup/default rules
// BuildStack and shnkitd's ClearStaleAssets call both rely on.
func TestResolveValidatorLines_Table(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		additional  []string
		wantPrimary string
		wantAll     []string
	}{
		{"empty line defaults to 2.0, no additional", "", nil, "2.0", []string{"2.0"}},
		{"explicit 2.0, no additional", "2.0", nil, "2.0", []string{"2.0"}},
		{"non-default primary", "2.1", nil, "2.1", []string{"2.1"}},
		{"primary plus one additional", "2.0", []string{"2.2"}, "2.0", []string{"2.0", "2.2"}},
		{"additional duplicates primary — deduped", "2.0", []string{"2.0", "2.2"}, "2.0", []string{"2.0", "2.2"}},
		{"additional repeats itself — deduped", "2.0", []string{"2.1", "2.1"}, "2.0", []string{"2.0", "2.1"}},
		{"additional carries an empty entry — skipped", "2.0", []string{"", "2.1"}, "2.0", []string{"2.0", "2.1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary, all := ResolveValidatorLines(tc.line, tc.additional)
			if primary != tc.wantPrimary {
				t.Errorf("primary = %q, want %q", primary, tc.wantPrimary)
			}
			if len(all) != len(tc.wantAll) {
				t.Fatalf("all = %q, want %q", all, tc.wantAll)
			}
			for i := range tc.wantAll {
				if all[i] != tc.wantAll[i] {
					t.Errorf("all[%d] = %q, want %q (full: %q)", i, all[i], tc.wantAll[i], all)
				}
			}
		})
	}
}

// TestFHIRValidateEnvName_Table pins the exact env-name mirror of
// gateway/app/app.go's FHIR_VALIDATE_URL/_2_1/_2_2 triad.
func TestFHIRValidateEnvName_Table(t *testing.T) {
	cases := map[string]string{
		"2.0": "FHIR_VALIDATE_URL",
		"2.1": "FHIR_VALIDATE_URL_2_1",
		"2.2": "FHIR_VALIDATE_URL_2_2",
	}
	for line, want := range cases {
		if got := fhirValidateEnvName(line); got != want {
			t.Errorf("fhirValidateEnvName(%q) = %q, want %q", line, got, want)
		}
	}
}

// TestBuildStack_LineUnset_ByteIdenticalToSingleLine is the regression
// pin: leaving StackConfig.Line/AdditionalValidatorLines at their zero values
// must reproduce EXACTLY today's trio shape — 4 children (never a 5th), the
// bare FHIR_VALIDATE_URL name (never a suffixed one), and no
// AdditionalValidatorURLs.
func TestBuildStack_LineUnset_ByteIdenticalToSingleLine(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if len(stack.Children) != 5 {
		t.Fatalf("Children = %d, want 5 (the core four + the provider-data gateway; no extra validators boot by default)", len(stack.Children))
	}
	if stack.Children[1].Name != "validator" {
		t.Errorf("Children[1].Name = %q, want validator (unqualified default line)", stack.Children[1].Name)
	}
	if !hasEnv(stack.Children[0].Env, "FHIR_VALIDATE_URL="+stack.ValidatorURL+"/fhir") {
		t.Errorf("gateway Env = %q, want the bare FHIR_VALIDATE_URL name", stack.Children[0].Env)
	}
	for _, e := range stack.Children[0].Env {
		if strings.Contains(e, "FHIR_VALIDATE_URL_") {
			t.Errorf("gateway Env contains a suffixed FHIR_VALIDATE_URL_* var (%q), want none when Line/AdditionalValidatorLines are unset", e)
		}
	}
	if len(stack.AdditionalValidatorURLs) != 0 {
		t.Errorf("AdditionalValidatorURLs = %v, want empty", stack.AdditionalValidatorURLs)
	}
}

// TestBuildStack_NonDefaultLine_SuffixedEnvNoDoubleBoot proves setting Line to
// a non-default value (alone, no AdditionalValidatorLines) still boots
// exactly ONE validator child — at that line — and wires the SUFFIXED env
// name, never the bare FHIR_VALIDATE_URL (gateway/app/app.go's canonical lane
// is fixed at "2.0"; wiring FHIR_VALIDATE_URL to a 2.1 validator would silently
// mislabel it as the canonical lane).
func TestBuildStack_NonDefaultLine_SuffixedEnvNoDoubleBoot(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, func(c *StackConfig) { c.Line = "2.1" }))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if len(stack.Children) != 5 {
		t.Fatalf("Children = %d, want 5 (the core four + the provider-data gateway; Line alone must not add a child)", len(stack.Children))
	}
	if stack.Children[1].Name != "validator-2.1" {
		t.Errorf("Children[1].Name = %q, want validator-2.1", stack.Children[1].Name)
	}
	if stack.Children[1].ReadyTimeout != javaReadyTimeoutCold {
		t.Errorf("Children[1].ReadyTimeout = %v, want the cold bound (2.1 is never prewarmed)", stack.Children[1].ReadyTimeout)
	}
	if !hasEnv(stack.Children[0].Env, "FHIR_VALIDATE_URL_2_1="+stack.ValidatorURL+"/fhir") {
		t.Errorf("gateway Env = %q, want FHIR_VALIDATE_URL_2_1", stack.Children[0].Env)
	}
	for _, e := range stack.Children[0].Env {
		if strings.HasPrefix(e, "FHIR_VALIDATE_URL=") {
			t.Errorf("gateway Env contains bare FHIR_VALIDATE_URL (%q) — line 2.1 must NOT be wired as the canonical lane", e)
		}
	}
}

// TestBuildStack_AdditionalValidatorLines_ExtraChildrenAndEnv is the positive
// case for the config-gated feature: one additional line boots exactly one
// more child, carried in DeferredChildren (never in the blocking Children
// list), with its own port/URL and its own suffixed env var — while the
// primary line's wiring (bare FHIR_VALIDATE_URL) is untouched.
func TestBuildStack_AdditionalValidatorLines_ExtraChildrenAndEnv(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, func(c *StackConfig) {
		c.AdditionalValidatorLines = []string{"2.2"}
	}))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	wantNames := []string{"gateway", "validator", "data-server", "br-provider", providerDataChildName}
	if len(stack.Children) != len(wantNames) {
		t.Fatalf("Children = %d, want %d (the core four + the provider-data gateway, no extra validator): %+v", len(stack.Children), len(wantNames), stack.Children)
	}
	for i, want := range wantNames {
		if stack.Children[i].Name != want {
			t.Errorf("Children[%d].Name = %q, want %q", i, stack.Children[i].Name, want)
		}
	}
	if len(stack.DeferredChildren) != 1 || stack.DeferredChildren[0].Name != "validator-2.2" {
		t.Fatalf("DeferredChildren = %+v, want exactly [validator-2.2]", stack.DeferredChildren)
	}
	extraURL, ok := stack.AdditionalValidatorURLs["2.2"]
	if !ok || extraURL == "" {
		t.Fatalf("AdditionalValidatorURLs[2.2] missing or empty: %v", stack.AdditionalValidatorURLs)
	}
	if extraURL == stack.ValidatorURL {
		t.Errorf("AdditionalValidatorURLs[2.2] = %q, want a DIFFERENT port than the primary validator's %q", extraURL, stack.ValidatorURL)
	}
	if !hasEnv(stack.Children[0].Env, "FHIR_VALIDATE_URL_2_2="+extraURL+"/fhir") {
		t.Errorf("gateway Env = %q, want FHIR_VALIDATE_URL_2_2=%s/fhir", stack.Children[0].Env, extraURL)
	}
	if !hasEnv(stack.Children[0].Env, "FHIR_VALIDATE_URL="+stack.ValidatorURL+"/fhir") {
		t.Errorf("gateway Env = %q, want the primary line's bare FHIR_VALIDATE_URL untouched", stack.Children[0].Env)
	}
	if stack.DeferredChildren[0].ReadyTimeout != javaReadyTimeoutCold {
		t.Errorf("validator-2.2 ReadyTimeout = %v, want the cold bound", stack.DeferredChildren[0].ReadyTimeout)
	}
}

// TestBuildStack_AdditionalValidatorLines_NeverBlockTheCoreBoot is the
// launch-time guard that lets the packaged Kit ship the bridge lanes ON by
// default (the v0.10.1 bridging defect). Each extra lane boots COLD — 10-15 minutes of IG
// indexing, per tools/kitassets/build.sh's own measured prewarm cost — and
// shnkitd's start loop blocks on every Children entry's ready probe before it
// calls SetRunner. Putting the lanes in Children would therefore mean no
// scenario could run for ~half an hour after a fresh install: a worse
// regression than the bug being fixed. They go in DeferredChildren, which
// shnkitd starts in the background AFTER runs go live.
func TestBuildStack_AdditionalValidatorLines_NeverBlockTheCoreBoot(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, func(c *StackConfig) {
		c.AdditionalValidatorLines = []string{"2.1", "2.2"}
	}))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	for _, c := range stack.Children {
		if c.ReadyTimeout == javaReadyTimeoutCold {
			t.Errorf("blocking child %q carries the COLD ready bound (%v) — a cold child in Children stalls SetRunner for the whole indexing wait",
				c.Name, c.ReadyTimeout)
		}
	}
	wantDeferred := []string{"validator-2.1", "validator-2.2"}
	if len(stack.DeferredChildren) != len(wantDeferred) {
		t.Fatalf("DeferredChildren = %+v, want %v", stack.DeferredChildren, wantDeferred)
	}
	for i, want := range wantDeferred {
		if stack.DeferredChildren[i].Name != want {
			t.Errorf("DeferredChildren[%d].Name = %q, want %q", i, stack.DeferredChildren[i].Name, want)
		}
	}
}

// TestBuildStack_NoAdditionalLines_NoDeferredChildren pins the default: with
// no extra lines configured, DeferredChildren is empty, so shnkitd's
// background-start goroutine never even launches.
func TestBuildStack_NoAdditionalLines_NoDeferredChildren(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, func(_ *StackConfig) {}))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if len(stack.DeferredChildren) != 0 {
		t.Errorf("DeferredChildren = %+v, want empty", stack.DeferredChildren)
	}
}

// TestBuildStack_AdditionalValidatorLines_DuplicateOfPrimary_NoExtraChild
// proves an AdditionalValidatorLines entry equal to the resolved primary line
// is deduped away — never a second child for the same line, never a stray
// AdditionalValidatorURLs entry either.
func TestBuildStack_AdditionalValidatorLines_DuplicateOfPrimary_NoExtraChild(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, func(c *StackConfig) {
		c.AdditionalValidatorLines = []string{"2.0"} // same as the (defaulted) primary
	}))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	if len(stack.Children) != 5 {
		t.Fatalf("Children = %d, want 5 (the core four + the provider-data gateway; an AdditionalValidatorLines entry equal to the primary must not add a child)", len(stack.Children))
	}
	if len(stack.AdditionalValidatorURLs) != 0 {
		t.Errorf("AdditionalValidatorURLs = %v, want empty", stack.AdditionalValidatorURLs)
	}
}

// TestBuildStack_TrioPresent_ProviderDataChild proves the trio boots a SECOND
// gateway child on the provider-data origination profile, reading the same
// data source as the existing child (the bring-your-own swap applies to
// both — spec amendment A1), with no ingress of its own and its own observer
// hub, sharing the one static payer directory both children read.
func TestBuildStack_TrioPresent_ProviderDataChild(t *testing.T) {
	cfg := trioCfg(t, func(c *StackConfig) {
		c.PHGURL = "http://127.0.0.1:9030"
		// The bring-your-own SMART quad and swap target apply to BOTH children
		// (A1): the provider-data child inherits them from the shared recipe.
		c.FHIRTokenURL = "http://127.0.0.1:9040/token"
		c.FHIRClientID = "partner-ehr"
		c.FHIRClientKeyPath = "/secrets/ehr.key"
		c.FHIRClientAlg = "RS384"
		c.FHIRDataURL = "http://127.0.0.1:9050/fhir"
	})
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	pd := stack.Children[len(stack.Children)-1]
	if pd.Name != providerDataChildName {
		t.Fatalf("last child = %q, want %q (boots after the trio it depends on)", pd.Name, providerDataChildName)
	}
	dirPath := filepath.Join(cfg.StateDir, "payer-directory.json")
	for _, want := range []string{
		"ROLE=provider",
		"ORIGINATION_PROFILE=provider-data",
		"PAYER_DIRECTORY=" + dirPath,
		"FHIR_DATA_URL=http://127.0.0.1:9050/fhir",
		"FHIR_TOKEN_URL=http://127.0.0.1:9040/token",
		"FHIR_CLIENT_ID=partner-ehr",
		"FHIR_CLIENT_KEY=/secrets/ehr.key",
		"FHIR_CLIENT_ALG=RS384",
		"PROVIDER_DTR_NATIVE=true",
		"PROVIDER_DTR_POPULATE_URL=" + stack.DataServerURL + "/fhir/provider/Questionnaire/$populate",
		"FHIR_VALIDATE_URL=" + stack.ValidatorURL + "/fhir",
		"SHN_SECRETS=" + cfg.SecretsDir,
		"SHN_DISCOVERY_URL=" + cfg.DiscoveryURL,
		"PHG_URL=" + cfg.PHGURL,
		"PORT=" + strings.TrimPrefix(stack.ProviderDataURL, "http://127.0.0.1:"),
		"OBSERVER_ADDR=" + strings.TrimPrefix(strings.TrimSuffix(stack.ProviderDataObserverURL, "/events"), "http://"),
	} {
		if !hasEnv(pd.Env, want) {
			t.Errorf("provider-data child env lacks %q: %v", want, pd.Env)
		}
	}
	for _, bad := range []string{
		"PROVIDER_DAVINCI_INGRESS=", "PROVIDER_DAVINCI_INGRESS_BASE_URL=", "INGRESS_CLIENTS_FILE=",
	} {
		for _, e := range pd.Env {
			if strings.HasPrefix(e, bad) {
				t.Errorf("provider-data child env carries %q — it mounts no ingress of its own", e)
			}
		}
	}
	// Exactly one PORT / OBSERVER_ADDR / FHIR_DATA_URL (the derivation replaced, not duplicated).
	for _, key := range []string{"PORT=", "OBSERVER_ADDR=", "FHIR_DATA_URL="} {
		n := 0
		for _, e := range pd.Env {
			if strings.HasPrefix(e, key) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("provider-data child env carries %d %s entries, want exactly 1", n, key)
		}
	}
	// The existing child is untouched by the new one (except the profile,
	// which is the second child's alone).
	gw := stack.Children[0]
	for _, e := range gw.Env {
		if strings.HasPrefix(e, "ORIGINATION_PROFILE=") {
			t.Errorf("the existing gateway child carries %q — the profile is the second child's, never this one's", e)
		}
	}
	if !hasEnv(gw.Env, "FHIR_DATA_URL=http://127.0.0.1:9050/fhir") || !hasEnv(gw.Env, "FHIR_TOKEN_URL=http://127.0.0.1:9040/token") {
		t.Errorf("the existing child lost its own swap target / SMART quad: %v", gw.Env)
	}
	// The directory: first row is 00001 → conformance-payer (the published
	// counterparty id — not a knob); no bridge holders configured here, so
	// that is the only row.
	raw, err := os.ReadFile(dirPath)
	if err != nil {
		t.Fatalf("read payer-directory.json: %v", err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal payer-directory.json: %v", err)
	}
	if len(rows) != 1 || rows[0]["system"] != "urn:oid:2.16.840.1.113883.6.300" || rows[0]["value"] != "00001" || rows[0]["holderId"] != "conformance-payer" {
		t.Fatalf("payer-directory.json first row = %v, want exactly [{00001 → conformance-payer}]", rows)
	}
	// Its own observer + ready probes; the lane's driver points at it; the
	// existing lane's driver did not move.
	if stack.ProviderDataObserverURL == "" || stack.ProviderDataObserverURL == stack.ObserverURL {
		t.Errorf("ProviderDataObserverURL = %q, want its own hub (main = %q)", stack.ProviderDataObserverURL, stack.ObserverURL)
	}
	if stack.ProviderDataURL == "" || stack.ProviderDataURL == stack.GatewayURL {
		t.Errorf("ProviderDataURL = %q, want its own port (main = %q)", stack.ProviderDataURL, stack.GatewayURL)
	}
	wantReady := []string{stack.ProviderDataURL + "/health", stack.ProviderDataObserverHealthURL}
	if len(pd.ReadyURLs) != 2 || pd.ReadyURLs[0] != wantReady[0] || pd.ReadyURLs[1] != wantReady[1] {
		t.Errorf("ReadyURLs = %v, want %v (no ingress ⇒ no smart-configuration route; /health is served in every posture)", pd.ReadyURLs, wantReady)
	}
	if pd.LogPath != filepath.Join(cfg.StateDir, "gateway-provider-data.log") {
		t.Errorf("LogPath = %q", pd.LogPath)
	}
	if stack.ProviderDataDriver.ProviderDataURL != stack.ProviderDataURL || stack.ProviderDataDriver.PHGURL != cfg.PHGURL {
		t.Errorf("ProviderDataDriver = %+v, want ProviderDataURL=%s PHGURL=%s", stack.ProviderDataDriver, stack.ProviderDataURL, cfg.PHGURL)
	}
	if stack.Driver.ProviderDataURL != stack.GatewayURL {
		t.Errorf("the existing lane's driver moved: Driver.ProviderDataURL = %q, want %q", stack.Driver.ProviderDataURL, stack.GatewayURL)
	}
}

// TestBuildStack_TrioAbsent_NoProviderDataChild is the rejection row: without
// the trio there is no operated $populate, so there is no provider-data
// child and no lane driver — but the ONE payer directory is still written
// and still wired into the existing child (the Da Vinci lane must reach the
// reference payer with no trio present), and that child never carries the
// provider-data profile.
func TestBuildStack_TrioAbsent_NoProviderDataChild(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      t.TempDir(),
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	for _, c := range stack.Children {
		if c.Name == providerDataChildName {
			t.Fatalf("provider-data child present without the trio")
		}
	}
	if stack.ProviderDataURL != "" || stack.ProviderDataObserverURL != "" || stack.ProviderDataObserverHealthURL != "" || stack.ProviderDataDriver.ProviderDataURL != "" {
		t.Errorf("provider-data URLs set without the trio: %q %q %q %q", stack.ProviderDataURL, stack.ProviderDataObserverURL, stack.ProviderDataObserverHealthURL, stack.ProviderDataDriver.ProviderDataURL)
	}
	dirPath := filepath.Join(cfg.StateDir, "payer-directory.json")
	raw, err := os.ReadFile(dirPath)
	if err != nil {
		t.Fatalf("payer-directory.json must be written even without the trio: %v", err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal payer-directory.json: %v", err)
	}
	if len(rows) != 1 || rows[0]["holderId"] != "conformance-payer" {
		t.Errorf("payer-directory.json = %v, want exactly the conformance-payer row (no bridge holders configured)", rows)
	}
	if !hasEnv(stack.Children[0].Env, "PAYER_DIRECTORY="+dirPath) {
		t.Errorf("the existing gateway child lacks PAYER_DIRECTORY without the trio: %v", stack.Children[0].Env)
	}
	for _, e := range stack.Children[0].Env {
		if strings.HasPrefix(e, "ORIGINATION_PROFILE=") {
			t.Errorf("the existing gateway child carries %q — the profile is the second child's, never this one's", e)
		}
	}
}

// TestDeriveProviderDataEnv_Table pins the derivation: dropped keys are gone,
// kept keys survive verbatim and in order — including the SMART quad and
// PAYER_DIRECTORY, both inherited from the base recipe rather than recomputed
// (A1: the swap and the one payer directory apply to both children) — and the
// four appended keys land last.
func TestDeriveProviderDataEnv_Table(t *testing.T) {
	base := []string{
		"ROLE=provider", "PORT=1", "HOST=127.0.0.1", "OBSERVER_ADDR=127.0.0.1:2",
		"PROVIDER_DAVINCI_INGRESS=1", "PROVIDER_DAVINCI_INGRESS_BASE_URL=http://127.0.0.1:1", "INGRESS_CLIENTS_FILE=/s/ingress-clients.json",
		"FHIR_DATA_URL=http://partner/fhir", "FHIR_TOKEN_URL=http://partner/token", "FHIR_CLIENT_ID=x", "FHIR_CLIENT_KEY=/k", "FHIR_CLIENT_ALG=RS384", "FHIR_CLIENT_SCOPE=s", "FHIR_CLIENT_KID=k",
		"PROVIDER_DTR_NATIVE=true", "PATH=/usr/bin", "PAYER_DIRECTORY=/s/payer-directory.json",
	}
	got := deriveProviderDataEnv(base, 7, "127.0.0.1:8", "http://data/fhir/provider")
	want := []string{
		"ROLE=provider", "HOST=127.0.0.1",
		"FHIR_TOKEN_URL=http://partner/token", "FHIR_CLIENT_ID=x", "FHIR_CLIENT_KEY=/k", "FHIR_CLIENT_ALG=RS384", "FHIR_CLIENT_SCOPE=s", "FHIR_CLIENT_KID=k",
		"PROVIDER_DTR_NATIVE=true", "PATH=/usr/bin", "PAYER_DIRECTORY=/s/payer-directory.json",
		"PORT=7", "OBSERVER_ADDR=127.0.0.1:8", "FHIR_DATA_URL=http://data/fhir/provider",
		"ORIGINATION_PROFILE=provider-data",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("deriveProviderDataEnv =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// ---- one payer directory for both gateway children (spec amendment A1) ------

// The directory is written on EVERY boot (trio or not) and both children read it: the
// reference payer row is constant; the bridge-demo rows are present iff configured.
func TestBuildStack_PayerDirectory_BothChildren(t *testing.T) {
	cfg := trioCfg(t, func(c *StackConfig) {
		c.BridgeDemoHolder = "bridge-demo"
		c.BridgeRefuseHolder = "bridge-demo-refuse"
	})
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	dirPath := filepath.Join(cfg.StateDir, "payer-directory.json")
	gw, pd := stack.Children[0], stack.Children[len(stack.Children)-1]
	for _, child := range []supervisor.ChildSpec{gw, pd} {
		if !hasEnv(child.Env, "PAYER_DIRECTORY="+dirPath) {
			t.Errorf("%s lacks PAYER_DIRECTORY=%s: %v", child.Name, dirPath, child.Env)
		}
		n := 0
		for _, e := range child.Env {
			if strings.HasPrefix(e, "PAYER_DIRECTORY=") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s carries %d PAYER_DIRECTORY entries, want exactly 1", child.Name, n)
		}
	}
	if !hasEnv(stack.GatewayEnv, "PAYER_DIRECTORY="+dirPath) {
		t.Errorf("Stack.GatewayEnv lacks the directory (the demo-toggle restart re-uses it)")
	}
	raw, err := os.ReadFile(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{
		{"system": "urn:oid:2.16.840.1.113883.6.300", "value": "00001", "holderId": "conformance-payer"},
		{"system": "urn:shn:demo-payer", "value": "SHN-BRIDGE-DEMO", "holderId": "bridge-demo"},
		{"system": "urn:shn:demo-payer", "value": "SHN-BRIDGE-REFUSE", "holderId": "bridge-demo-refuse"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("payer-directory.json rows = %v, want %v", rows, want)
	}
}

func TestBuildStack_PayerDirectory_NoTrio_AndNoBridgeRows(t *testing.T) {
	cfg := baseCfg(t) // the existing no-trio config helper used by TestBuildStack_EnvRecipe
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	dirPath := filepath.Join(cfg.StateDir, "payer-directory.json")
	if !hasEnv(stack.Children[0].Env, "PAYER_DIRECTORY="+dirPath) {
		t.Fatalf("no-trio gateway child lacks PAYER_DIRECTORY — the Da Vinci lane must reach the reference payer without the trio")
	}
	raw, err := os.ReadFile(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	_ = json.Unmarshal(raw, &rows)
	if len(rows) != 1 || rows[0]["holderId"] != "conformance-payer" {
		t.Errorf("rows = %v, want exactly the conformance-payer row when no bridge holder is configured", rows)
	}
}

// The BYO-EHR swap (FHIR_DATA_URL + the SMART quad) applies to BOTH children (spec amendment A1).
func TestBuildStack_TrioPresent_SwapAppliesToBothChildren(t *testing.T) {
	cfg := trioCfg(t, func(c *StackConfig) {
		c.FHIRTokenURL = "http://127.0.0.1:9040/token"
		c.FHIRClientID = "partner-ehr"
		c.FHIRClientKeyPath = "/secrets/ehr.key"
		c.FHIRClientAlg = "RS384"
		c.FHIRDataURL = "http://127.0.0.1:9050/fhir"
	})
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	for _, child := range []supervisor.ChildSpec{stack.Children[0], stack.Children[len(stack.Children)-1]} {
		for _, want := range []string{"FHIR_DATA_URL=http://127.0.0.1:9050/fhir", "FHIR_TOKEN_URL=http://127.0.0.1:9040/token", "FHIR_CLIENT_ID=partner-ehr", "FHIR_CLIENT_KEY=/secrets/ehr.key", "FHIR_CLIENT_ALG=RS384"} {
			if !hasEnv(child.Env, want) {
				t.Errorf("%s lacks %q under the swap: %v", child.Name, want, child.Env)
			}
		}
	}
	pd := stack.Children[len(stack.Children)-1]
	if !hasEnv(pd.Env, "PROVIDER_DTR_POPULATE_URL="+stack.DataServerURL+"/fhir/provider/Questionnaire/$populate") {
		t.Errorf("the provider-data child's $populate must stay on the bundled data server under the swap (the stated ceiling)")
	}
}

// TestBuildStack_TrioPresent_NoSwap_NeitherChildCarriesTheSMARTQuad is the A1
// rider's NEGATIVE half: "the swap applies to both children" is only a claim
// about the swap if an unswapped boot leaves both children clean. A quad
// emitted unconditionally would make the positive test above pass for the
// wrong reason — and half-set (an empty FHIR_TOKEN_URL with the rest present)
// is the exact shape the gateway's own emptiness guard rejects, so it would
// not fail quietly either.
func TestBuildStack_TrioPresent_NoSwap_NeitherChildCarriesTheSMARTQuad(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	for _, child := range []supervisor.ChildSpec{stack.Children[0], stack.Children[len(stack.Children)-1]} {
		for _, bad := range []string{"FHIR_TOKEN_URL=", "FHIR_CLIENT_ID=", "FHIR_CLIENT_KEY=", "FHIR_CLIENT_ALG="} {
			for _, e := range child.Env {
				if strings.HasPrefix(e, bad) {
					t.Errorf("%s carries %q with no swap configured — the SMART quad is the swap's, never the base recipe's", child.Name, e)
				}
			}
		}
	}
}

// fixtureSoRPortOf reads back the port BuildStack bound the no-trio lane's fixture system
// of record on. It is allocated, not fixed, so the recipe pin reads it off the assembled
// env rather than guessing.
func fixtureSoRPortOf(t *testing.T, stack Stack) int {
	t.Helper()
	for _, kv := range stack.GatewayEnv {
		if !strings.HasPrefix(kv, "FHIR_DATA_URL=") {
			continue
		}
		u, err := url.Parse(strings.TrimPrefix(kv, "FHIR_DATA_URL="))
		if err != nil {
			t.Fatalf("FHIR_DATA_URL is not a URL: %v", err)
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			t.Fatalf("FHIR_DATA_URL has no port: %v", err)
		}
		return port
	}
	t.Fatal("no FHIR_DATA_URL in the gateway env — the no-trio child would refuse to boot")
	return 0
}
