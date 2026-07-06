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

func TestBuildStack_EnvRecipe(t *testing.T) {
	stateDir := t.TempDir()
	cfg := StackConfig{
		GatewayBinary: "/bin/true",
		StateDir:      stateDir,
		SecretsDir:    "/secrets/provider",
		DiscoveryURL:  "http://127.0.0.1:9001/discovery",
		AuditURL:      "http://127.0.0.1:9002",
		PHGURL:        "http://127.0.0.1:9003",
		ConsentURL:    "http://127.0.0.1:9004",
		FakeValidator: true,
		// FHIRDataURL and OriginationProfile left "" deliberately (the
		// pre-trio posture): the recipe must omit both entries, not emit
		// them empty.
	}

	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
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
		"SHN_FAKE_VALIDATOR=1",
		fmt.Sprintf("OBSERVER_ADDR=127.0.0.1:%d", obsPort),
		"PROVIDER_DAVINCI_INGRESS=1",
		fmt.Sprintf("PROVIDER_DAVINCI_INGRESS_BASE_URL=%s", stack.GatewayURL),
		"INGRESS_CLIENTS_FILE=" + filepath.Join(stateDir, "ingress-clients.json"),
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
	for _, bad := range []string{"FHIR_DATA_URL=", "ORIGINATION_PROFILE="} {
		for _, e := range spec.Env {
			if strings.HasPrefix(e, bad) {
				t.Errorf("Env contains %q, want it omitted when unset", e)
			}
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
// rules run in both directions: FHIR_DATA_URL and ORIGINATION_PROFILE show up
// (with the configured value, in recipe order) when the caller sets them.
func TestBuildStack_EnvRecipe_OptionalFieldsPresent(t *testing.T) {
	cfg := StackConfig{
		GatewayBinary:      "/bin/true",
		StateDir:           t.TempDir(),
		SecretsDir:         "/secrets/provider",
		DiscoveryURL:       "http://127.0.0.1:9001/discovery",
		FHIRDataURL:        "http://127.0.0.1:9010/fhir/provider",
		OriginationProfile: "provider-data",
	}
	stack, err := BuildStack(cfg)
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	spec := stack.Children[0]
	if !hasEnv(spec.Env, "FHIR_DATA_URL=http://127.0.0.1:9010/fhir/provider") {
		t.Errorf("Env = %q, want FHIR_DATA_URL set", spec.Env)
	}
	if !hasEnv(spec.Env, "ORIGINATION_PROFILE=provider-data") {
		t.Errorf("Env = %q, want ORIGINATION_PROFILE set", spec.Env)
	}
	if hasEnv(spec.Env, "SHN_FAKE_VALIDATOR=1") {
		t.Errorf("Env = %q, want SHN_FAKE_VALIDATOR omitted (cfg.FakeValidator false)", spec.Env)
	}
	// FHIR_DATA_URL must precede ORIGINATION_PROFILE (recipe order).
	fi, oi := -1, -1
	for i, e := range spec.Env {
		if strings.HasPrefix(e, "FHIR_DATA_URL=") {
			fi = i
		}
		if strings.HasPrefix(e, "ORIGINATION_PROFILE=") {
			oi = i
		}
	}
	if fi == -1 || oi == -1 || fi > oi {
		t.Errorf("Env order = %q, want FHIR_DATA_URL before ORIGINATION_PROFILE", spec.Env)
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
	for _, bad := range []string{"FHIR_VALIDATE_URL=", "PROVIDER_DTR_NATIVE=", "PROVIDER_DTR_POPULATE_URL="} {
		for _, e := range stack.Children[0].Env {
			if strings.HasPrefix(e, bad) {
				t.Errorf("Env contains %q, want it absent when no trio is configured", e)
			}
		}
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
// the trio comes BEFORE the gateway, never appended after it.
func TestBuildStack_TrioPresent_ChildrenOrder(t *testing.T) {
	stack, err := BuildStack(trioCfg(t, nil))
	if err != nil {
		t.Fatalf("BuildStack: %v", err)
	}
	wantNames := []string{"validator", "data-server", "br-provider", "gateway"}
	if len(stack.Children) != len(wantNames) {
		t.Fatalf("Children = %d, want %d: %+v", len(stack.Children), len(wantNames), stack.Children)
	}
	for i, want := range wantNames {
		if stack.Children[i].Name != want {
			t.Errorf("Children[%d].Name = %q, want %q (trio must precede the gateway — this is the order flip from today's ExtraChildren-append shape)", i, stack.Children[i].Name, want)
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
	gwEnv := stack.Children[3].Env
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
	if !hasEnv(stack.Children[3].Env, "SHN_FAKE_VALIDATOR=1") {
		t.Errorf("gateway Env = %q, want SHN_FAKE_VALIDATOR=1 (explicitly forced)", stack.Children[3].Env)
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
	if !hasEnv(stack.Children[3].Env, want) {
		t.Errorf("gateway Env = %q, want %q", stack.Children[3].Env, want)
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
	if !hasEnv(stack.Children[3].Env, "FHIR_DATA_URL="+byoURL) {
		t.Errorf("gateway Env = %q, want the caller's FHIRDataURL untouched", stack.Children[3].Env)
	}
	defaultURL := "FHIR_DATA_URL=" + stack.DataServerURL + "/fhir/provider"
	if hasEnv(stack.Children[3].Env, defaultURL) {
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
	gwEnv := stack.Children[3].Env
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

	brProviderSpec := stack.Children[2]
	if brProviderSpec.Name != brProviderChildName {
		t.Fatalf("Children[2].Name = %q, want %q", brProviderSpec.Name, brProviderChildName)
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
