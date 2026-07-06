package byo

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// parseECPublicKeyFromPEMErrs runs the SAME jwt-lib parse ValidateDaVinci
// runs for ES384, so the parity test pins sameness (not a curve policy this
// package invents on its own).
func parseECPublicKeyFromPEMErrs(pemStr string) error {
	_, err := jwt.ParseECPublicKeyFromPEM([]byte(pemStr))
	return err
}

// --- key-generation helpers (throwaway, in-test only) --------------------

func genRSAKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

func genRSAPublicKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func genECKeyPEM(t *testing.T, curve elliptic.Curve) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func genECPublicKeyPEM(t *testing.T, curve elliptic.Curve) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// --- 1: Load missing -> zero ----------------------------------------------

func TestLoad_MissingIsZero(t *testing.T) {
	s := NewStore(t.TempDir())
	cfg, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EHR != nil || cfg.DaVinci != nil {
		t.Fatalf("want zero Config, got %+v", cfg)
	}
}

// --- 2: SetEHR round trip + modes ------------------------------------------

func TestSetEHR_Unauthenticated_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	e := EHR{DataURL: "https://ehr.example.com/fhir"}
	if err := s.SetEHR(e, nil); err != nil {
		t.Fatalf("SetEHR: %v", err)
	}

	cfgPath := filepath.Join(dir, "byo.json")
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat byo.json: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("byo.json mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(s.EHRKeyPath()); !os.IsNotExist(err) {
		t.Errorf("want no key file for unauthenticated EHR, stat err = %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.EHR == nil || got.EHR.DataURL != e.DataURL {
		t.Fatalf("Load round-trip = %+v, want DataURL %q", got.EHR, e.DataURL)
	}
}

func TestSetEHR_Authenticated_KeyFileSeparateFromJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	keyPEM := genRSAKeyPEM(t)
	e := EHR{
		DataURL:  "https://ehr.example.com/fhir",
		TokenURL: "https://ehr.example.com/token",
		ClientID: "kit-client",
		Alg:      "RS384",
	}
	if err := s.SetEHR(e, keyPEM); err != nil {
		t.Fatalf("SetEHR: %v", err)
	}

	keyInfo, err := os.Stat(s.EHRKeyPath())
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", keyInfo.Mode().Perm())
	}

	rawCfg, err := os.ReadFile(filepath.Join(dir, "byo.json"))
	if err != nil {
		t.Fatalf("read byo.json: %v", err)
	}
	if strings.Contains(string(rawCfg), "PRIVATE KEY") {
		t.Errorf("byo.json must never contain key bytes, got: %s", rawCfg)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.EHR == nil || got.EHR.ClientID != e.ClientID || got.EHR.Alg != e.Alg {
		t.Fatalf("Load round-trip = %+v", got.EHR)
	}
}

// --- 3: validation rejections table ----------------------------------------

func TestSetEHR_ValidationRejections(t *testing.T) {
	rsaKey := genRSAKeyPEM(t)
	ecKey := genECKeyPEM(t, elliptic.P384())

	cases := []struct {
		name      string
		e         EHR
		key       []byte
		errSubstr string // when non-empty, the error must contain this
	}{
		{
			name: "empty data url",
			e:    EHR{DataURL: ""},
		},
		{
			name: "non-http data url",
			e:    EHR{DataURL: "not-a-url"},
		},
		{
			name:      "token url without client id",
			e:         EHR{DataURL: "https://ehr.example.com/fhir", TokenURL: "https://ehr.example.com/token", Alg: "RS384"},
			key:       rsaKey,
			errSubstr: "all-or-nothing",
		},
		{
			name: "unsupported alg RS256",
			e:    EHR{DataURL: "https://ehr.example.com/fhir", TokenURL: "https://ehr.example.com/token", ClientID: "c1", Alg: "RS256"},
			key:  rsaKey,
		},
		{
			name: "ES384 alg with RSA key",
			e:    EHR{DataURL: "https://ehr.example.com/fhir", TokenURL: "https://ehr.example.com/token", ClientID: "c1", Alg: "ES384"},
			key:  rsaKey,
		},
		{
			name: "garbage PEM",
			e:    EHR{DataURL: "https://ehr.example.com/fhir", TokenURL: "https://ehr.example.com/token", ClientID: "c1", Alg: "RS384"},
			key:  []byte("not a pem at all"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := NewStore(dir)
			err := s.SetEHR(tc.e, tc.key)
			if err == nil {
				t.Fatalf("SetEHR(%+v): want error, got nil", tc.e)
			}
			if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
				t.Errorf("SetEHR(%+v) error = %q, want it to contain %q", tc.e, err.Error(), tc.errSubstr)
			}
			if _, err := os.Stat(filepath.Join(dir, "byo.json")); !os.IsNotExist(err) {
				t.Errorf("byo.json should not exist after a rejected SetEHR, stat err = %v", err)
			}
		})
	}

	// sanity: ecKey is unused in the RS384 mismatch direction table above but
	// confirms an EC key parses fine under ES384 (used elsewhere); referencing
	// here keeps the helper exercised without an unused-var complaint if the
	// table above changes shape.
	_ = ecKey
}

// --- 4: DaVinci parity -------------------------------------------------

func TestSetDaVinci_ValidAndRejections(t *testing.T) {
	t.Run("valid RS384", func(t *testing.T) {
		dir := t.TempDir()
		s := NewStore(dir)
		dv := DaVinci{ClientID: "payer-client", Alg: "RS384", PublicKeyPEM: string(genRSAPublicKeyPEM(t))}
		if err := s.SetDaVinci(dv); err != nil {
			t.Fatalf("SetDaVinci: %v", err)
		}
		got, err := s.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.DaVinci == nil || got.DaVinci.ClientID != dv.ClientID {
			t.Fatalf("Load round-trip = %+v", got.DaVinci)
		}
	})

	t.Run("empty client id rejected", func(t *testing.T) {
		dv := DaVinci{ClientID: "", Alg: "RS384", PublicKeyPEM: string(genRSAPublicKeyPEM(t))}
		if err := ValidateDaVinci(dv); err == nil {
			t.Fatal("want error for empty client id")
		}
	})

	t.Run("EC-P256 key under alg ES384 mirrors jwt.ParseECPublicKeyFromPEM", func(t *testing.T) {
		// Parity means: whatever the jwt lib decides for a P-256 key under ES384
		// is what ValidateDaVinci decides too (the gateway boot check makes the
		// same call) -- this test pins sameness, not curve policy.
		p256PEM := string(genECPublicKeyPEM(t, elliptic.P256()))
		wantErr := parseECPublicKeyFromPEMErrs(p256PEM)
		gotErr := ValidateDaVinci(DaVinci{ClientID: "c1", Alg: "ES384", PublicKeyPEM: p256PEM})
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("parity mismatch: jwt lib err=%v, ValidateDaVinci err=%v", wantErr, gotErr)
		}
	})
}

// --- 5: Clear independence -------------------------------------------------

func TestClearEHR_RemovesKeyFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	keyPEM := genRSAKeyPEM(t)
	e := EHR{DataURL: "https://ehr.example.com/fhir", TokenURL: "https://ehr.example.com/token", ClientID: "c1", Alg: "RS384"}
	if err := s.SetEHR(e, keyPEM); err != nil {
		t.Fatalf("SetEHR: %v", err)
	}
	if err := s.ClearEHR(); err != nil {
		t.Fatalf("ClearEHR: %v", err)
	}
	if _, err := os.Stat(s.EHRKeyPath()); !os.IsNotExist(err) {
		t.Errorf("key file should be removed after ClearEHR, stat err = %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.EHR != nil {
		t.Errorf("EHR lane should be nil after ClearEHR, got %+v", got.EHR)
	}
}

func TestClearDaVinci_LeavesEHRLaneUntouched(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	e := EHR{DataURL: "https://ehr.example.com/fhir"}
	if err := s.SetEHR(e, nil); err != nil {
		t.Fatalf("SetEHR: %v", err)
	}
	dv := DaVinci{ClientID: "payer-client", Alg: "RS384", PublicKeyPEM: string(genRSAPublicKeyPEM(t))}
	if err := s.SetDaVinci(dv); err != nil {
		t.Fatalf("SetDaVinci: %v", err)
	}
	if err := s.ClearDaVinci(); err != nil {
		t.Fatalf("ClearDaVinci: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DaVinci != nil {
		t.Errorf("DaVinci lane should be nil after ClearDaVinci, got %+v", got.DaVinci)
	}
	if got.EHR == nil || got.EHR.DataURL != e.DataURL {
		t.Errorf("EHR lane should be untouched after ClearDaVinci, got %+v", got.EHR)
	}
}

// --- 6: corrupt byo.json -----------------------------------------------

func TestLoad_CorruptFileIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "byo.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt byo.json: %v", err)
	}
	s := NewStore(dir)
	if _, err := s.Load(); err == nil {
		t.Fatal("want error loading corrupt byo.json, got nil")
	}
}

// --- 7: ProbeEHR ------------------------------------------------------------

func TestProbeEHR_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metadata" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"resourceType": "CapabilityStatement"})
	}))
	defer srv.Close()

	p := ProbeEHR(context.Background(), nil, srv.URL)
	if p.Name != "byo-ehr" {
		t.Errorf("Name = %q, want byo-ehr", p.Name)
	}
	if !p.OK {
		t.Errorf("want OK, got Detail=%q", p.Detail)
	}
}

func TestProbeEHR_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := ProbeEHR(context.Background(), nil, srv.URL)
	if p.OK {
		t.Fatal("want not-OK on 500")
	}
	if !strings.Contains(p.Detail, "500") {
		t.Errorf("Detail = %q, want it to name the status", p.Detail)
	}
}

func TestProbeEHR_WrongResourceType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"resourceType": "OperationOutcome"})
	}))
	defer srv.Close()

	p := ProbeEHR(context.Background(), nil, srv.URL)
	if p.OK {
		t.Fatal("want not-OK for wrong resourceType")
	}
}

func TestProbeEHR_Unreachable(t *testing.T) {
	p := ProbeEHR(context.Background(), nil, "http://127.0.0.1:1")
	if p.OK {
		t.Fatal("want not-OK for unreachable port")
	}
	if !strings.Contains(p.Detail, "127.0.0.1:1") {
		t.Errorf("Detail = %q, want it to name the URL", p.Detail)
	}
}

// --- 8: EHRHTTPClient --------------------------------------------------

func TestEHRHTTPClient_Unauthenticated(t *testing.T) {
	hc, err := EHRHTTPClient(&EHR{DataURL: "https://ehr.example.com/fhir"}, nil)
	if err != nil {
		t.Fatalf("EHRHTTPClient: %v", err)
	}
	if hc != nil {
		t.Errorf("want nil client for unauthenticated EHR, got %v", hc)
	}
}

func TestEHRHTTPClient_Authenticated_CarriesBearer(t *testing.T) {
	var gotAuth string
	dataSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{"resourceType": "CapabilityStatement"})
	}))
	defer dataSrv.Close()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT-123", "token_type": "bearer", "expires_in": 300,
		})
	}))
	defer tokenSrv.Close()

	keyPEM := genRSAKeyPEM(t)
	e := &EHR{
		DataURL:  dataSrv.URL,
		TokenURL: tokenSrv.URL,
		ClientID: "kit-client",
		Alg:      "RS384",
	}
	hc, err := EHRHTTPClient(e, keyPEM)
	if err != nil {
		t.Fatalf("EHRHTTPClient: %v", err)
	}
	if hc == nil {
		t.Fatal("want non-nil client for authenticated EHR")
	}
	resp, err := hc.Get(dataSrv.URL + "/metadata")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if gotAuth != "Bearer AT-123" {
		t.Errorf("Authorization = %q, want Bearer AT-123", gotAuth)
	}
}

func TestEHRHTTPClient_EmptyScope_DefaultsToSystemAllRead(t *testing.T) {
	dataSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"resourceType": "CapabilityStatement"})
	}))
	defer dataSrv.Close()

	var gotScope string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("token endpoint ParseForm: %v", err)
		}
		gotScope = r.PostForm.Get("scope")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT-123", "token_type": "bearer", "expires_in": 300,
		})
	}))
	defer tokenSrv.Close()

	keyPEM := genRSAKeyPEM(t)
	e := &EHR{
		DataURL:  dataSrv.URL,
		TokenURL: tokenSrv.URL,
		ClientID: "kit-client",
		Alg:      "RS384",
		// Scope deliberately left empty -- the gateway's parity default
		// (system/*.read) must be applied by EHRHTTPClient.
	}
	hc, err := EHRHTTPClient(e, keyPEM)
	if err != nil {
		t.Fatalf("EHRHTTPClient: %v", err)
	}
	resp, err := hc.Get(dataSrv.URL + "/metadata")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if gotScope != "system/*.read" {
		t.Errorf("token request scope = %q, want system/*.read (gateway default parity)", gotScope)
	}
}
