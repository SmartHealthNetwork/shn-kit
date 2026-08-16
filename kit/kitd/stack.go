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
	"strings"
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
	// through this field, and it is ordered AFTER the gateway (which boots
	// first), not before.
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

	// Line is the contract line the packaged validator validates — "" resolves
	// to defaultContractLine ("2.0", the canonical default). When trio is
	// configured, the resulting validator's URL is wired into the gateway
	// child's env under fhirValidateEnvName(Line): FHIR_VALIDATE_URL for
	// "2.0" (unchanged from before this field existed), or
	// FHIR_VALIDATE_URL_<line> for any other line — mirroring
	// gateway/app/app.go's own FHIR_VALIDATE_URL/_2_1/_2_2 triad exactly, so
	// the SAME env name that lane's FHIR_VALIDATE_URL_* semantics expect is
	// the one this validator's URL lands under.
	Line string
	// AdditionalValidatorLines boots EXTRA validator-ONLY children (never a
	// second data server or br-provider) at other contract lines — config-
	// gated, empty by default (do not boot extra validators). Each is wired
	// to its own fhirValidateEnvName(line) env var for the gateway child,
	// same as Line above. A line equal to the resolved Line, or repeated
	// within this slice, is silently deduped (ResolveValidatorLines) — never a second
	// child for the same line. Every additional child boots cold
	// (javaReadyTimeoutCold), same as a non-default Line: only
	// defaultContractLine ever has a package-time prewarm to boot from.
	AdditionalValidatorLines []string

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
	// Children are the BLOCKING children: shnkitd starts them in order and
	// waits for each one's ready probe before the next, and only calls
	// SetRunner (i.e. lets scenarios run at all) once the last one is ready.
	Children []supervisor.ChildSpec

	// DeferredChildren are started in the BACKGROUND, after Children are all
	// ready and runs have gone live. Today this is exactly the extra
	// per-line validator lanes (AdditionalValidatorLines).
	//
	// They are separated because they are the only children that boot COLD:
	// no line other than defaultContractLine has a package-time prewarmed H2,
	// so each one indexes its full IG set from scratch — the 10-15 minutes
	// tools/kitassets/build.sh's prewarm step exists to spare users, and the
	// reason javaReadyTimeoutCold is 20 minutes. Blocking the core boot on
	// that would mean a freshly-installed Kit could run no scenario at all
	// for half an hour, so the wait is moved off the critical path: the Kit
	// launches exactly as fast as it did with no lanes configured, the lanes
	// warm up behind it, and their H2 stores persist so it is a once-ever
	// cost per line.
	//
	// KNOWN ROUGH EDGE, stated plainly rather than papered over: the gateway
	// child is handed FHIR_VALIDATE_URL_<line> for every configured lane at
	// BOOT, and gateway/app/app.go builds those validators lazily (no dial at
	// construction), so from the gateway's point of view a still-indexing lane
	// is already "laned". A cross-version run attempted during that first
	// warm-up window therefore selects the bridge and then fails at $validate
	// with a validator-outage error, rather than with the cleaner "no
	// configured validator lane" refusal. It fails CLOSED either way — nothing
	// is validated against the wrong IG version and nothing is fabricated —
	// but the message names the wrong cause. The condition is transient and
	// once-per-install; GET /api/status shows the lane's child still starting
	// for the duration. Making the refusal name the real cause means teaching
	// the gateway lane readiness, which is a gateway-side change, not a kit
	// one.
	//
	// A DeferredChildren start failure is NOT a boot failure: the Kit stays
	// fully usable without the optional lane (the v0.10.1 bridging defect).
	DeferredChildren []supervisor.ChildSpec

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
	// ingress-clients.json. ValidatorURL is always the PRIMARY (resolved
	// StackConfig.Line) validator's URL.
	ValidatorURL  string
	DataServerURL string
	BRProviderURL string

	// GatewayEnv is the FULL env BuildStack assembled for the gateway child —
	// value-identical to the gateway ChildSpec's own Env. Exported for ONE
	// consumer: shnkitd's bridging demo toggle, which restarts the gateway
	// with this env plus (or minus) the SHN_DEMO_EGRESS_NATIVE_LINES knob and
	// so needs the exact baseline to rebuild from, never a re-derivation that
	// could drift from what the child is actually running.
	//
	// SECURITY: this carries secrets-adjacent values (SHN_SECRETS, the SMART
	// quad's FHIR_CLIENT_KEY path, INGRESS_CLIENTS_FILE). It must NEVER be
	// surfaced through any kitd API response, event, log line, support
	// bundle, or UI — kitd consumes it in-process only, inside the demo
	// closure main builds.
	GatewayEnv []string

	// AdditionalValidatorURLs maps each StackConfig.AdditionalValidatorLines
	// entry (after ResolveValidatorLines dedup) to its own validator child's
	// URL — nil/empty when no additional lines were configured (today's
	// default).
	AdditionalValidatorURLs map[string]string
}

// ResolveValidatorLines resolves (StackConfig.Line, StackConfig.
// AdditionalValidatorLines) into (primary, all): primary is Line with ""
// defaulted to defaultContractLine; all is [primary, ...additional] with
// duplicates (including an additional entry equal to primary) dropped,
// first-seen order preserved. The single source of truth for this dedup —
// BuildStack and shnkitd's ClearStaleAssets call both use it, so the set of
// validator children actually booted and the set swept for staleness can
// never diverge.
func ResolveValidatorLines(line string, additional []string) (primary string, all []string) {
	primary = line
	if primary == "" {
		primary = defaultContractLine
	}
	seen := map[string]bool{primary: true}
	all = []string{primary}
	for _, l := range additional {
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		all = append(all, l)
	}
	return primary, all
}

// fhirValidateEnvName mirrors gateway/app/app.go's own FHIR_VALIDATE_URL/
// FHIR_VALIDATE_URL_2_1/FHIR_VALIDATE_URL_2_2 naming EXACTLY (not re-derived —
// copied verbatim, since the Kit's gateway child is that same binary): the
// canonical line ("2.0") keeps the bare FHIR_VALIDATE_URL name; any other
// line gets FHIR_VALIDATE_URL_<line> with "." replaced by "_".
func fhirValidateEnvName(line string) string {
	if line == defaultContractLine {
		return "FHIR_VALIDATE_URL"
	}
	return "FHIR_VALIDATE_URL_" + strings.ReplaceAll(line, ".", "_")
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
// validator/data-server/br-provider ChildSpecs AFTER it — the gateway boots
// first — and cfg.ExtraChildren after those). It spawns no processes and blocks only on
// local disk I/O.
func BuildStack(cfg StackConfig) (Stack, error) {
	trio := cfg.JavaAssetsDir != ""

	// resolvedLine is the primary validator's line; extraLines are the
	// (deduped, "" and resolvedLine already excluded) AdditionalValidatorLines
	// entries — each gets its own validator-only child, below. Computed
	// unconditionally (cheap, pure) even when !trio, so callers can inspect it
	// if ever needed without a trio guard of their own.
	resolvedLine, validatorLines := ResolveValidatorLines(cfg.Line, cfg.AdditionalValidatorLines)
	extraLines := validatorLines[1:]

	need := 1 // observer, always on
	if cfg.GatewayPort == 0 {
		need++
	}
	if trio {
		need += 2                   // data server, br-provider
		need += len(validatorLines) // one validator port per configured line (1 by default)
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
	additionalValidatorURLs := map[string]string{}
	additionalValidatorPorts := map[string]int{}
	if trio {
		validatorPort = nextPort()
		validatorURL = fmt.Sprintf("http://127.0.0.1:%d", validatorPort)
		for _, line := range extraLines {
			p := nextPort()
			additionalValidatorPorts[line] = p
			additionalValidatorURLs[line] = fmt.Sprintf("http://127.0.0.1:%d", p)
		}
		dataServerPort = nextPort()
		brProviderPort = nextPort()
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
		// Real validator child(ren) present: point the gateway at each one,
		// under the env name that line's FHIR_VALIDATE_URL_* semantics expect
		// (fhirValidateEnvName — bare FHIR_VALIDATE_URL for the canonical
		// "2.0" line, FHIR_VALIDATE_URL_<line> otherwise; gateway/app/app.go's
		// own naming, mirrored exactly). This is emitted regardless of
		// cfg.FakeValidator — the gateway's own selectValidator checks
		// SHN_FAKE_VALIDATOR FIRST (gateway/app/app.go), so an
		// explicitly-forced fake validator still wins; this just never leaves
		// a real validator's URL unwired when it's standing by.
		env = append(env, fhirValidateEnvName(resolvedLine)+"="+validatorURL+"/fhir")
		for _, line := range extraLines {
			env = append(env, fhirValidateEnvName(line)+"="+additionalValidatorURLs[line]+"/fhir")
		}
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

	// Children order: the gateway comes FIRST — [gateway, validator,
	// data-server, br-provider] — so it starts and passes its ready probe in
	// well under a second (its probe is its own smart-configuration endpoint;
	// it does not connect to the FHIR servers at boot), and its boot-screen
	// stage checks off fast. The bundled Java FHIR servers — the real
	// multi-minute first-launch wait — start after it and become the visible
	// wait. This is safe because runs only go live once EVERY child (including
	// the trio) has passed its ready probe (see cmd/shnkitd's start loop +
	// SetRunner). cfg.ExtraChildren still follows all of that. The supervisor
	// starts children sequentially, blocking on each one's ready probe, so
	// this order is also the staged boot screen's order, for free.
	//
	// AdditionalValidatorLines children are deliberately NOT in this list —
	// they go to Stack.DeferredChildren and start in the background once runs
	// are already live (see that field's doc for why: they are the only
	// children that boot cold, at 10-15 minutes of IG indexing apiece, and
	// the packaged Kit now ships two of them ON so bridging works out of the
	// box — the v0.10.1 bridging defect).
	var children []supervisor.ChildSpec
	var deferredChildren []supervisor.ChildSpec
	children = append(children, gatewaySpec)
	if trio {
		validatorSpec, err := BuildValidatorChildSpec(cfg.JavaAssetsDir, cfg.JREDir, cfg.StateDir, validatorPort, runtime.GOOS, resolvedLine)
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
		for _, line := range extraLines {
			extraSpec, err := BuildValidatorChildSpec(cfg.JavaAssetsDir, cfg.JREDir, cfg.StateDir, additionalValidatorPorts[line], runtime.GOOS, line)
			if err != nil {
				return Stack{}, err
			}
			deferredChildren = append(deferredChildren, extraSpec)
		}
	}
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

	stack := Stack{
		Children:          children,
		DeferredChildren:  deferredChildren,
		ObserverURL:       observerURL,
		ObserverHealthURL: observerHealthURL,
		GatewayURL:        gatewayURL,
		ValidatorURL:      validatorURL,
		DataServerURL:     dataServerURL,
		BRProviderURL:     brProviderURL,
		Driver:            driverCfg,
		// A COPY, not the spec's own backing array: the demo toggle appends
		// its knob to this baseline, and an append that happened to land in
		// spare capacity of the shared array would rewrite the registered
		// ChildSpec's env out from under the supervisor.
		GatewayEnv: append([]string(nil), env...),
	}
	if len(additionalValidatorURLs) > 0 {
		stack.AdditionalValidatorURLs = additionalValidatorURLs
	}
	return stack, nil
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
