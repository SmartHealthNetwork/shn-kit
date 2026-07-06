// stack.go — native-process stack composition for the Kit's single
// provider-role gateway child. BuildStack is
// pure composition plus a handful of local file writes (the ingress client
// registration) — it spawns no processes; kit/cmd/shnkitd's main hands its
// output to a supervisor.Supervisor.
package kitd

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"time"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"
	"software.sslmate.com/src/go-pkcs12"

	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

const (
	gatewayChildName = "gateway"

	// ingressClientID is the shn-kit driver's registered UDAP B2B client_id —
	// the JWT iss the scenariodriver mints its direct bearers under.
	ingressClientID = "shn-kit-driver"

	gatewayReadyTimeout = 30 * time.Second
	gatewayRestartMax   = 3
)

// StackConfig configures BuildStack's composition of one Kit deployment: a
// single provider-role gateway child, config-only. ExtraEnv/
// ExtraChildren are the seam used to fold in a real
// validator/data-server/br-provider without touching this base shape.
type StackConfig struct {
	GatewayBinary string // absolute path to the published gateway binary
	StateDir      string // logs, ingress-clients.json, driver key
	SecretsDir    string // pre-provisioned shn register / Init bundle (SHN_SECRETS)
	DiscoveryURL  string // SHN_DISCOVERY_URL (required by the binary)
	AuditURL      string // env-set trust planes (not discovery-resolved)
	PHGURL        string
	ConsentURL    string
	FHIRDataURL   string // "" ⇒ memstub SoR (the gate posture without provider data configured)
	// OriginationProfile is "" by default; a later flip enables provider-data origination.
	OriginationProfile string
	FakeValidator      bool // true when no packaged validator is configured
	// GatewayPort is 0 ⇒ allocate; non-zero when the caller pre-registered
	// the holder BaseURL (the gate does).
	GatewayPort int
	ExtraEnv    []string
	// ExtraChildren is a generic append-AFTER-gateway extension seam (still
	// available to any caller); the Java trio (below) is now assembled
	// directly by BuildStack, keyed on JavaAssetsDir — it does NOT go
	// through this field, and it is ordered BEFORE the gateway, not after.
	ExtraChildren []supervisor.ChildSpec

	// JavaAssetsDir is the packaged Java trio's asset dir (HAPI validator +
	// seeded HAPI data server + br-provider — hapi/main.war, brprovider/
	// main.war, igs-validator/*.tgz, igs-data/*.tgz, prewarm/{validator,
	// data}-h2, a jre-<goos>-<goarch>/bin/java). "" => no trio: today's
	// behavior, byte-identical.
	JavaAssetsDir string
	// JREDir is the JRE's root (containing bin/java[.exe]) — resolved
	// per-arch by main (jre-<goos>-<goarch> under JavaAssetsDir by
	// default). Required when JavaAssetsDir != "".
	JREDir string

	// FHIRTokenURL/FHIRClientID/FHIRClientKeyPath/FHIRClientAlg/
	// FHIRClientScope/FHIRClientKID are the SMART Backend Services quad the
	// gateway authenticates its FHIR_DATA_URL client with (gateway/app/app.go
	// loadConfig: FHIR_TOKEN_URL/FHIR_CLIENT_ID/FHIR_CLIENT_KEY/FHIR_CLIENT_ALG
	// are all-or-nothing once FHIR_TOKEN_URL is set; FHIR_CLIENT_SCOPE/
	// FHIR_CLIENT_KID are independently optional). BuildStack does not
	// validate this quad itself — kit/byo's Validate* functions run EXACT
	// gateway-boot parity upstream, at byo.json save time, so an
	// entry that reaches here is already known-good. FHIRClientKeyPath is a
	// path (the gateway reads FHIR_CLIENT_KEY off disk itself), never key
	// bytes.
	FHIRTokenURL      string
	FHIRClientID      string
	FHIRClientKeyPath string
	FHIRClientAlg     string
	// FHIRClientScope/FHIRClientKID: "" is OMITTED from the child's env
	// (never emitted as an empty override) so the gateway's own
	// def("FHIR_CLIENT_SCOPE", "system/*.read") default applies — an emitted
	// empty string would defeat that default rather than fall through to it.
	FHIRClientScope string
	FHIRClientKID   string

	// ExtraIngressClients are bring-your-own Da Vinci inbound registrations
	// merged into ingress-clients.json AFTER the
	// internal shn-kit-driver entry, every boot (see BuildStack: this is the
	// only correct merge point, since BuildStack clobbers the file each
	// time). Empty ⇒ today's single-entry file, unchanged.
	ExtraIngressClients []IngressClient
}

// IngressClient is one bring-your-own Da Vinci ingress registration
// (kit/byo.DaVinci, already validated at save time). Scopes are
// deliberately not part of this shape: an entry written from it carries no
// "scopes" field, so the gateway's own loadIngressClients default
// (["system/Davinci.write"], gateway/app/app.go:373-376) applies, the same
// as the internal driver's explicit scope today.
type IngressClient struct {
	ClientID     string
	Alg          string
	PublicKeyPEM string
}

// Stack is BuildStack's output.
type Stack struct {
	Children    []supervisor.ChildSpec
	Driver      scenariodriver.Config // IngressURL/IngressBase/ClientID/Key/ProviderDataURL/PHGURL/BFFURL filled
	ObserverURL string                // http://127.0.0.1:<port>/events
	// ObserverHealthURL is the observer hub's GET /health — the relay drain
	// barrier's counter.
	ObserverHealthURL string
	GatewayURL        string

	// ValidatorURL/DataServerURL/BRProviderURL are the Java trio's own
	// http://127.0.0.1:<port> bases — "" when JavaAssetsDir == "" (no
	// trio). The br-provider ingress client's own key material stays
	// internal to BuildStack (never exposed here) — callers need only its
	// URL, which BuildStack has already folded into Driver.BFFURL and
	// ingress-clients.json.
	ValidatorURL  string
	DataServerURL string
	BRProviderURL string
}

// ingressClientFile is one entry of the ingress-clients.json array the
// gateway's app.loadIngressClients parses (gateway/app/app.go:334-380).
type ingressClientFile struct {
	ClientID     string   `json:"client_id"`
	Alg          string   `json:"alg"`
	PublicKeyPEM string   `json:"public_key_pem"`
	Scopes       []string `json:"scopes"`
}

// BuildStack allocates ports, generates the driver's RS384 ingress signing
// key (and, when the Java trio is configured, a second RS384 keypair for
// br-provider), materializes ingress-clients.json under StateDir, and
// assembles the gateway ChildSpec (plus, when JavaAssetsDir != "", the
// validator/data-server/br-provider ChildSpecs BEFORE it, and
// cfg.ExtraChildren after it). It spawns no processes and blocks only on
// local disk I/O.
func BuildStack(cfg StackConfig) (Stack, error) {
	trio := cfg.JavaAssetsDir != ""

	need := 1 // observer, always on
	if cfg.GatewayPort == 0 {
		need++
	}
	if trio {
		need += 3 // validator, data server, br-provider
	}
	ports, err := supervisor.AllocatePorts(need)
	if err != nil {
		return Stack{}, fmt.Errorf("kitd: allocate ports: %w", err)
	}
	next := 0
	nextPort := func() int {
		p := ports[next]
		next++
		return p
	}

	observerPort := nextPort()
	gatewayPort := cfg.GatewayPort
	if gatewayPort == 0 {
		gatewayPort = nextPort()
	}
	var validatorPort, dataServerPort, brProviderPort int
	var validatorURL, dataServerURL, brProviderURL string
	if trio {
		validatorPort = nextPort()
		dataServerPort = nextPort()
		brProviderPort = nextPort()
		validatorURL = fmt.Sprintf("http://127.0.0.1:%d", validatorPort)
		dataServerURL = fmt.Sprintf("http://127.0.0.1:%d", dataServerPort)
		brProviderURL = fmt.Sprintf("http://127.0.0.1:%d", brProviderPort)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Stack{}, fmt.Errorf("kitd: generate driver signing key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return Stack{}, fmt.Errorf("kitd: marshal driver public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	// br-provider's own RS384 keypair, generated beside the driver's, only
	// when the trio is present: br-provider signs its own CDS-client JWT
	// with it (PKCS12-exported for its SECURITY_CERT_FILE), and the
	// ingress verifies that JWT against the SAME key's public half,
	// registered below under ClientID = brProviderURL.
	var brpEntry *ingressClientFile
	var brpCertPath, brpCertPassword string
	if trio {
		brpKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return Stack{}, fmt.Errorf("kitd: generate br-provider signing key: %w", err)
		}
		brpPubDER, err := x509.MarshalPKIXPublicKey(&brpKey.PublicKey)
		if err != nil {
			return Stack{}, fmt.Errorf("kitd: marshal br-provider public key: %w", err)
		}
		brpPubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: brpPubDER})

		certDER, err := selfSignedCert(brpKey, "shn-kit-br-provider")
		if err != nil {
			return Stack{}, fmt.Errorf("kitd: br-provider self-signed cert: %w", err)
		}
		cert, err := x509.ParseCertificate(certDER)
		if err != nil {
			return Stack{}, fmt.Errorf("kitd: parse br-provider cert: %w", err)
		}
		brpCertPassword, err = randomHexString(16)
		if err != nil {
			return Stack{}, fmt.Errorf("kitd: generate br-provider PFX password: %w", err)
		}
		pfxData, err := pkcs12.Encode(rand.Reader, brpKey, cert, nil, brpCertPassword)
		if err != nil {
			return Stack{}, fmt.Errorf("kitd: encode br-provider PKCS12: %w", err)
		}
		brpCertPath = filepath.Join(cfg.StateDir, "br-provider-cert.pfx")
		if err := os.WriteFile(brpCertPath, pfxData, 0600); err != nil {
			return Stack{}, fmt.Errorf("kitd: write br-provider PFX %s: %w", brpCertPath, err)
		}
		brpEntry = &ingressClientFile{ClientID: brProviderURL, Alg: "RS384", PublicKeyPEM: string(brpPubPEM)}
	}

	ingressClientsPath := filepath.Join(cfg.StateDir, "ingress-clients.json")
	// The driver entry is always first (Driver.ClientID below is pinned to
	// it); the br-provider entry (when the trio is present) is merged in
	// right after it — an internal, kit-managed entry, same as the
	// driver's; ExtraIngressClients (bring-your-own Da Vinci registrations)
	// are appended last, without a scopes field — see
	// IngressClient's doc comment for why that's deliberate. This merge
	// happens here, at write time, because BuildStack clobbers
	// ingress-clients.json on every boot: this is the only correct place to
	// fold byo.json's DaVinci lane back in.
	ingressEntries := []ingressClientFile{{
		ClientID:     ingressClientID,
		Alg:          "RS384",
		PublicKeyPEM: string(pubPEM),
		Scopes:       []string{"system/Davinci.write"},
	}}
	if brpEntry != nil {
		ingressEntries = append(ingressEntries, *brpEntry)
	}
	for _, ec := range cfg.ExtraIngressClients {
		ingressEntries = append(ingressEntries, ingressClientFile{
			ClientID:     ec.ClientID,
			Alg:          ec.Alg,
			PublicKeyPEM: ec.PublicKeyPEM,
		})
	}
	clientsJSON, err := json.MarshalIndent(ingressEntries, "", "  ")
	if err != nil {
		return Stack{}, fmt.Errorf("kitd: marshal ingress-clients.json: %w", err)
	}
	if err := os.WriteFile(ingressClientsPath, clientsJSON, 0600); err != nil {
		return Stack{}, fmt.Errorf("kitd: write ingress-clients.json: %w", err)
	}

	gatewayURL := fmt.Sprintf("http://127.0.0.1:%d", gatewayPort)
	observerAddr := fmt.Sprintf("127.0.0.1:%d", observerPort)
	observerURL := fmt.Sprintf("http://%s/events", observerAddr)
	observerHealthURL := fmt.Sprintf("http://%s/health", observerAddr)

	// FHIRDataURL defaults to the trio's own data server ("provider" tenant)
	// ONLY when the caller left it empty — byo/flag overrides always win.
	fhirDataURL := cfg.FHIRDataURL
	if trio && fhirDataURL == "" {
		fhirDataURL = dataServerURL + "/fhir/provider"
	}

	// The env recipe: deploy/compose.multiprocess.yml:471-502's provider
	// block, minus FHIR/SMART/pg/DTR-native (the pre-trio gate posture), plus the
	// boot-gate env-override posture (test/gatewayboot). HOST is always
	// 127.0.0.1 — the Kit gateway is a local child, never a network service.
	env := []string{
		"ROLE=provider",
		fmt.Sprintf("PORT=%d", gatewayPort),
		"HOST=127.0.0.1",
		"SHN_SECRETS=" + cfg.SecretsDir,
		"SHN_DISCOVERY_URL=" + cfg.DiscoveryURL,
		"AUDIT_URL=" + cfg.AuditURL,
		"PHG_URL=" + cfg.PHGURL,
		"CONSENT_URL=" + cfg.ConsentURL,
	}
	if cfg.FakeValidator {
		env = append(env, "SHN_FAKE_VALIDATOR=1")
	}
	if trio {
		// Real validator child present: point the gateway at it. This is
		// emitted regardless of cfg.FakeValidator — the gateway's own
		// selectValidator checks SHN_FAKE_VALIDATOR FIRST (gateway/app/app.go),
		// so an explicitly-forced fake validator still wins; this just never
		// leaves FHIR_VALIDATE_URL unset when a real validator is standing by.
		env = append(env, "FHIR_VALIDATE_URL="+validatorURL+"/fhir")
	}
	env = append(env,
		"OBSERVER_ADDR="+observerAddr,
		"PROVIDER_DAVINCI_INGRESS=1",
		"PROVIDER_DAVINCI_INGRESS_BASE_URL="+gatewayURL,
		"INGRESS_CLIENTS_FILE="+ingressClientsPath,
	)
	if fhirDataURL != "" {
		env = append(env, "FHIR_DATA_URL="+fhirDataURL)
	}
	if trio {
		// Native DTR populate against the real operated-CQL data server
		// (compose.multiprocess.yml parity).
		env = append(env,
			"PROVIDER_DTR_NATIVE=true",
			"PROVIDER_DTR_POPULATE_URL="+dataServerURL+"/fhir/provider/Questionnaire/$populate",
		)
	}
	if cfg.OriginationProfile != "" {
		env = append(env, "ORIGINATION_PROFILE="+cfg.OriginationProfile)
	}
	// The SMART quad: gated on FHIRTokenURL, mirroring gateway/app/app.go's
	// own FHIR_TOKEN_URL emptiness guard (loadConfig:256-266) — a half-set
	// quad (e.g. FHIRClientID alone, with FHIRTokenURL "") must never trip
	// that guard, so the whole block is skipped rather than emitted piecemeal.
	if cfg.FHIRTokenURL != "" {
		env = append(env,
			"FHIR_TOKEN_URL="+cfg.FHIRTokenURL,
			"FHIR_CLIENT_ID="+cfg.FHIRClientID,
			"FHIR_CLIENT_KEY="+cfg.FHIRClientKeyPath,
			"FHIR_CLIENT_ALG="+cfg.FHIRClientAlg,
		)
		// Scope-parity: omit rather than emit empty, so the
		// gateway's own def("FHIR_CLIENT_SCOPE", "system/*.read") default
		// applies instead of an empty override defeating it. Same for KID
		// (it has no gateway-side default, but the omission keeps the two
		// fields' treatment uniform and never sends an empty header value).
		if cfg.FHIRClientScope != "" {
			env = append(env, "FHIR_CLIENT_SCOPE="+cfg.FHIRClientScope)
		}
		if cfg.FHIRClientKID != "" {
			env = append(env, "FHIR_CLIENT_KID="+cfg.FHIRClientKID)
		}
	}
	// Propagate a minimal PATH from the parent env — exec of a static binary
	// needs nothing else; Env is otherwise kept fully explicit.
	if path := os.Getenv("PATH"); path != "" {
		env = append(env, "PATH="+path)
	}
	env = append(env, cfg.ExtraEnv...)

	gatewaySpec := supervisor.ChildSpec{
		Name:    gatewayChildName,
		Command: cfg.GatewayBinary,
		Env:     env,
		Dir:     cfg.StateDir,
		LogPath: filepath.Join(cfg.StateDir, "gateway.log"),
		// NOT /cds-services: that handler is ingress-auth-gated and 401s an
		// unauthenticated probe (gateway/engine/ingress.go:72-76), which
		// would deadlock the ready loop forever. /.well-known/smart-
		// configuration is registered whenever the ingress is enabled and is
		// genuinely unauthenticated (gateway/engine/ingressauth.go:332-341).
		ReadyURLs: []string{
			gatewayURL + "/.well-known/smart-configuration",
			observerHealthURL,
		},
		ReadyTimeout: gatewayReadyTimeout,
		RestartMax:   gatewayRestartMax,
	}

	// Children order: when the trio is present it
	// comes BEFORE the gateway — [validator, data-server, br-provider,
	// gateway] — never appended after it. cfg.ExtraChildren still follows the
	// gateway, unchanged. The supervisor starts children sequentially,
	// blocking on each one's ready probe, so this order is also the staged
	// boot screen's order, for free.
	var children []supervisor.ChildSpec
	if trio {
		validatorSpec, err := BuildValidatorChildSpec(cfg.JavaAssetsDir, cfg.JREDir, cfg.StateDir, validatorPort, runtime.GOOS)
		if err != nil {
			return Stack{}, err
		}
		dataServerSpec, err := BuildDataServerChildSpec(cfg.JavaAssetsDir, cfg.JREDir, cfg.StateDir, dataServerPort, runtime.GOOS)
		if err != nil {
			return Stack{}, err
		}
		brProviderSpec, err := BuildBRProviderChildSpec(cfg.JavaAssetsDir, cfg.JREDir, cfg.StateDir, brProviderPort, runtime.GOOS,
			gatewayURL, brProviderURL, brpCertPath, brpCertPassword)
		if err != nil {
			return Stack{}, err
		}
		children = append(children, validatorSpec, dataServerSpec, brProviderSpec)
	}
	children = append(children, gatewaySpec)
	children = append(children, cfg.ExtraChildren...)

	driverCfg := scenariodriver.Config{
		IngressURL:      gatewayURL,
		IngressBase:     gatewayURL,
		ClientID:        ingressClientID,
		Key:             key,
		ProviderDataURL: gatewayURL,
		PHGURL:          cfg.PHGURL,
	}
	if trio {
		driverCfg.BFFURL = brProviderURL
	}

	return Stack{
		Children:          children,
		ObserverURL:       observerURL,
		ObserverHealthURL: observerHealthURL,
		GatewayURL:        gatewayURL,
		ValidatorURL:      validatorURL,
		DataServerURL:     dataServerURL,
		BRProviderURL:     brProviderURL,
		Driver:            driverCfg,
	}, nil
}

// selfSignedCert generates a minimal self-signed X.509 certificate for key,
// valid for one year — just enough shape for a PKCS12 export (br-provider's
// SECURITY_CERT_FILE): br-provider uses the cert purely to carry its own
// public key for its CDS-client JWT signing identity, not for any TLS/chain
// validation.
func selfSignedCert(key *rsa.PrivateKey, commonName string) ([]byte, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	return x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
}

// randomHexString returns n random bytes, hex-encoded — used for the
// br-provider PKCS12 export's password (never a fixed/guessable value).
func randomHexString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
