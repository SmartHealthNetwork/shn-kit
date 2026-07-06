// Package byo implements the SHN Kit's "bring-your-own systems" swap config:
// a Kit user can point the kit at their own
// EHR (a US Core FHIR data server, optionally SMART Backend Services
// authenticated) and/or register their own Da Vinci ingress client, instead
// of running purely against the bundled sandbox.
//
// Validation here is EXACT PARITY with the gateway's own boot-time checks
// (gateway/app/app.go loadConfig's FHIR_TOKEN_URL all-or-nothing guard +
// loadSmartKey for the EHR lane, loadIngressClients for the DaVinci lane):
// an entry the gateway would refuse at boot must be refused here too —
// a swap the Kit accepts but the gateway then can't boot on is
// worse than refusing upfront, since the failure surfaces far from the form
// that caused it.
//
// Key material lives ONLY in the 0600 key file this package manages
// (EHRKeyPath) — it is never written into byo.json, never echoed back by
// Load, and byo.json is written 0600 as well.
package byo

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/golang-jwt/jwt/v5"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"

	"github.com/SmartHealthNetwork/shn-gateway/connectors/smartauth"

	"github.com/SmartHealthNetwork/shn-kit/bootstrap"
)

// EHR is the bring-your-own FHIR data-server lane. DataURL is always
// required; the remaining fields are all-or-nothing once TokenURL is set
// (mirrors gateway/app/app.go's FHIR_TOKEN_URL guard).
type EHR struct {
	DataURL  string `json:"dataUrl"`
	TokenURL string `json:"tokenUrl,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	Alg      string `json:"alg,omitempty"`   // ES384|RS384, required with TokenURL
	Scope    string `json:"scope,omitempty"` // empty -> EHRHTTPClient applies defaultEHRScope
	KID      string `json:"kid,omitempty"`
}

// defaultEHRScope mirrors gateway/app/app.go loadConfig's
// def("FHIR_CLIENT_SCOPE", "system/*.read") -- the gateway's own default for
// an unspecified FHIR client scope. EHRHTTPClient applies it here so the Kit
// stays in parity with the gateway it hands EHR config off to.
const defaultEHRScope = "system/*.read"

// DaVinci is the bring-your-own inbound ingress client registration lane
// (mirrors gateway/app/app.go's loadIngressClients entry shape).
type DaVinci struct {
	ClientID     string `json:"clientId"`
	Alg          string `json:"alg"` // ES384|RS384
	PublicKeyPEM string `json:"publicKeyPem"`
}

// Config is the full persisted byo.json shape. Either or both lanes may be
// nil/absent — nil means "not swapped, use the bundled default."
type Config struct {
	EHR     *EHR     `json:"ehr,omitempty"`
	DaVinci *DaVinci `json:"davinci,omitempty"`
}

const configFileName = "byo.json"
const ehrKeyFileName = "byo-ehr-key.pem"

// Store persists Config to {dir}/byo.json plus the {dir}/byo-ehr-key.pem key
// file (key bytes never enter byo.json). dir is the shnkitd state
// dir. All methods are safe for concurrent use.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns a Store rooted at dir (the shnkitd state dir).
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) configPath() string {
	return filepath.Join(s.dir, configFileName)
}

// EHRKeyPath returns {dir}/byo-ehr-key.pem, the 0600 file holding the EHR
// client's private key PEM (when the EHR lane is authenticated).
func (s *Store) EHRKeyPath() string {
	return filepath.Join(s.dir, ehrKeyFileName)
}

// Load reads the persisted Config. A missing file is not an error — it
// returns a zero Config (nothing has been swapped yet). A present-but-
// corrupt file IS an error: the caller's fail-safe consumes it
// rather than silently treating garbage as "nothing swapped".
func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (Config, error) {
	raw, err := os.ReadFile(s.configPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("kit/byo: read %s: %w", s.configPath(), err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("kit/byo: parse %s: %w", s.configPath(), err)
	}
	return cfg, nil
}

func (s *Store) saveLocked(cfg Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("kit/byo: marshal config: %w", err)
	}
	if err := os.WriteFile(s.configPath(), raw, 0o600); err != nil {
		return fmt.Errorf("kit/byo: write %s: %w", s.configPath(), err)
	}
	return nil
}

// SetEHR validates e (+ clientKeyPEM) with EXACT gateway-boot parity, then
// persists it: when clientKeyPEM is non-empty it is written to EHRKeyPath
// (0600) and byo.json never carries the PEM bytes; an unauthenticated EHR
// (empty clientKeyPEM) removes any stale key file. Validation runs BEFORE
// any file is touched, so a rejected SetEHR leaves byo.json unchanged.
func (s *Store) SetEHR(e EHR, clientKeyPEM []byte) error {
	if err := ValidateEHR(e, clientKeyPEM); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}

	if len(clientKeyPEM) > 0 {
		if err := os.WriteFile(s.EHRKeyPath(), clientKeyPEM, 0o600); err != nil {
			return fmt.Errorf("kit/byo: write ehr key: %w", err)
		}
	} else if err := removeIfExists(s.EHRKeyPath()); err != nil {
		return err
	}

	eCopy := e
	cfg.EHR = &eCopy
	return s.saveLocked(cfg)
}

// ClearEHR removes the EHR lane and its key file (if any). ClearEHR does not
// touch the DaVinci lane.
func (s *Store) ClearEHR() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	if err := removeIfExists(s.EHRKeyPath()); err != nil {
		return err
	}
	cfg.EHR = nil
	return s.saveLocked(cfg)
}

// SetDaVinci validates dv with EXACT gateway-boot parity (loadIngressClients),
// then persists it. Validation runs before any file is touched.
func (s *Store) SetDaVinci(dv DaVinci) error {
	if err := ValidateDaVinci(dv); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	dvCopy := dv
	cfg.DaVinci = &dvCopy
	return s.saveLocked(cfg)
}

// ClearDaVinci removes the DaVinci lane. ClearDaVinci does not touch the EHR
// lane (per-lane independence at the file level).
func (s *Store) ClearDaVinci() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := s.loadLocked()
	if err != nil {
		return err
	}
	cfg.DaVinci = nil
	return s.saveLocked(cfg)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("kit/byo: remove %s: %w", path, err)
	}
	return nil
}

// checkURL mirrors gateway/app/app.go's checkOptionalURL: the value must
// parse as a URL with an http or https scheme.
func checkURL(name, v string) error {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("kit/byo: invalid %s %q", name, v)
	}
	return nil
}

// ValidateEHR mirrors gateway/app/app.go's loadConfig FHIR_TOKEN_URL
// all-or-nothing guard + loadSmartKey's private-key parsing: an
// EHR entry that would fail gateway boot must fail here too.
func ValidateEHR(e EHR, clientKeyPEM []byte) error {
	if strings.TrimSpace(e.DataURL) == "" {
		return errors.New("kit/byo: EHR data URL is required")
	}
	if err := checkURL("EHR data URL", e.DataURL); err != nil {
		return err
	}

	if e.TokenURL == "" {
		return nil // unauthenticated mode — deliberate, not an error
	}
	if err := checkURL("EHR token URL", e.TokenURL); err != nil {
		return err
	}
	// All-or-nothing, mirrors gateway/app/app.go's FHIR_TOKEN_URL guard:
	// TokenURL set requires ClientID and a client key.
	if e.ClientID == "" || len(clientKeyPEM) == 0 {
		return errors.New("kit/byo: EHR token URL set requires a client id and a client key (all-or-nothing, mirrors the gateway's FHIR_TOKEN_URL guard)")
	}
	if _, err := parseSmartKey(clientKeyPEM, e.Alg); err != nil {
		return err
	}
	return nil
}

// parseSmartKey mirrors gateway/app/app.go's loadSmartKey, operating on PEM
// bytes directly instead of a path (the Kit stores the key file itself; this
// is the same parse, just fed differently). Only ES384 and RS384 are
// supported (AI-11 / OWD-6: no shared-secret algorithms).
func parseSmartKey(pemBytes []byte, alg string) (crypto.PrivateKey, error) {
	switch alg {
	case "ES384":
		key, err := jwt.ParseECPrivateKeyFromPEM(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("kit/byo: client key does not parse as an EC key (alg ES384): %w", err)
		}
		return key, nil
	case "RS384":
		key, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("kit/byo: client key does not parse as an RSA key (alg RS384): %w", err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("kit/byo: unsupported alg %q (ES384 or RS384)", alg)
	}
}

// ValidateDaVinci mirrors gateway/app/app.go's loadIngressClients per-entry
// check: client id required, alg ES384|RS384, and the public key PEM must
// parse under that alg.
func ValidateDaVinci(dv DaVinci) error {
	if strings.TrimSpace(dv.ClientID) == "" {
		return errors.New("kit/byo: client id is required")
	}
	switch dv.Alg {
	case "ES384":
		if _, err := jwt.ParseECPublicKeyFromPEM([]byte(dv.PublicKeyPEM)); err != nil {
			return fmt.Errorf("kit/byo: public key does not parse as an EC key (alg ES384): %w", err)
		}
	case "RS384":
		if _, err := jwt.ParseRSAPublicKeyFromPEM([]byte(dv.PublicKeyPEM)); err != nil {
			return fmt.Errorf("kit/byo: public key does not parse as an RSA key (alg RS384): %w", err)
		}
	default:
		return fmt.Errorf("kit/byo: unsupported alg %q (ES384 or RS384)", dv.Alg)
	}
	return nil
}

// EHRHTTPClient mirrors gateway/app/app.go's fhirHTTPClient: e == nil or an
// empty TokenURL means unauthenticated (nil, nil) — the caller falls back to
// http.DefaultClient. Otherwise it parses clientKeyPEM per e.Alg and builds a
// SMART Backend Services client via smartauth.
func EHRHTTPClient(e *EHR, clientKeyPEM []byte) (*http.Client, error) {
	if e == nil || e.TokenURL == "" {
		return nil, nil
	}
	key, err := parseSmartKey(clientKeyPEM, e.Alg)
	if err != nil {
		return nil, err
	}
	scope := e.Scope
	if scope == "" {
		scope = defaultEHRScope
	}
	hc, err := smartauth.NewHTTPClient(smartauth.Config{
		TokenURL: e.TokenURL,
		ClientID: e.ClientID,
		Scope:    scope,
		Alg:      e.Alg,
		Key:      key,
		KID:      e.KID,
	})
	if err != nil {
		return nil, fmt.Errorf("kit/byo: smartauth client: %w", err)
	}
	return hc, nil
}

// capabilityStatementProbe is the minimal shape ProbeEHR decodes off
// {dataURL}/metadata.
type capabilityStatementProbe struct {
	ResourceType string `json:"resourceType"`
}

// ProbeEHR GETs {dataURL}/metadata and reports whether it answered its
// CapabilityStatement — the Kit's "does your EHR reach the network"
// bootstrap.Probe (mirrors kit/bootstrap.Verify's probe shape). hc == nil
// defaults to http.DefaultClient.
func ProbeEHR(ctx context.Context, hc *http.Client, dataURL string) bootstrap.Probe {
	client := hc
	if client == nil {
		client = http.DefaultClient
	}

	metadataURL := strings.TrimRight(dataURL, "/") + "/metadata"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return bootstrap.Probe{Name: "byo-ehr", OK: false, Detail: fmt.Sprintf("invalid EHR data URL %q: %v", dataURL, err)}
	}
	resp, err := client.Do(req)
	if err != nil {
		return bootstrap.Probe{Name: "byo-ehr", OK: false, Detail: fmt.Sprintf("could not reach %s: %v", dataURL, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return bootstrap.Probe{Name: "byo-ehr", OK: false, Detail: fmt.Sprintf("%s responded with status %d", dataURL, resp.StatusCode)}
	}
	var body capabilityStatementProbe
	if err := json.NewDecoder(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes)).Decode(&body); err != nil {
		return bootstrap.Probe{Name: "byo-ehr", OK: false, Detail: fmt.Sprintf("%s did not return valid JSON: %v", dataURL, err)}
	}
	if body.ResourceType != "CapabilityStatement" {
		return bootstrap.Probe{Name: "byo-ehr", OK: false, Detail: fmt.Sprintf("%s returned resourceType %q, want CapabilityStatement", dataURL, body.ResourceType)}
	}
	return bootstrap.Probe{Name: "byo-ehr", OK: true, Detail: "your FHIR server answered its CapabilityStatement"}
}
