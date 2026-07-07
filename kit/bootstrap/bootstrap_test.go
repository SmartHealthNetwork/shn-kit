package bootstrap

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
	"github.com/SmartHealthNetwork/shn-sdk/accounts"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// --- test fixtures -----------------------------------------------------

// idTokenWithEmail builds a JWT-ish string whose middle segment base64url-decodes
// to {"email": email}. No signature is verified anywhere in this package.
func idTokenWithEmail(email string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return "hdr." + payload + ".sig"
}

// freePort allocates an ephemeral 127.0.0.1 port and releases it immediately, so
// tests never bind the registered loopback ports (8400-8404).
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// fakeCognito is a stub Cognito user pool: OIDC discovery + an /authorize
// endpoint that 302s straight to the loopback redirect_uri with a fixed code
// + a /token endpoint the test supplies (authorization_code and/or
// refresh_token grants).
type fakeCognito struct {
	*httptest.Server
	authorizeHits int32
}

func (f *fakeCognito) AuthorizeHits() int { return int(atomic.LoadInt32(&f.authorizeHits)) }

func newFakeCognito(t *testing.T, tokenHandler http.HandlerFunc) *fakeCognito {
	t.Helper()
	fc := &fakeCognito{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": fc.Server.URL + "/authorize",
			"token_endpoint":         fc.Server.URL + "/token",
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fc.authorizeHits, 1)
		redir := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		http.Redirect(w, r, redir+"?code=test-code&state="+state, http.StatusFound)
	})
	if tokenHandler != nil {
		mux.HandleFunc("/token", tokenHandler)
	}
	fc.Server = httptest.NewServer(mux)
	t.Cleanup(fc.Close)
	return fc
}

// popCall records one POST /clients/{id}/pop.
type popCall struct {
	id   string
	body map[string]string
}

// accountsRecorder captures what the fake Accounts service saw.
type accountsRecorder struct {
	mu      sync.Mutex
	creates []map[string]string
	pops    []popCall
}

// newFakeAccounts starts a fake Accounts service: /cli-config points at
// cognito; POST /clients records the body and answers createStatus (0 means
// 200 with {"id":"kit-h1"}) or createStatus+createBody when non-zero;
// POST /clients/kit-h1/pop records the body.
func newFakeAccounts(t *testing.T, cognito *fakeCognito, rec *accountsRecorder, createStatus int, createBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		issuer := ""
		if cognito != nil {
			issuer = cognito.Server.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":    issuer,
			"client_id": "cli-1",
			"scopes":    []string{"openid", "email"},
		})
	})
	mux.HandleFunc("/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.mu.Lock()
		rec.creates = append(rec.creates, body)
		rec.mu.Unlock()
		if createStatus != 0 && createStatus != http.StatusOK {
			w.WriteHeader(createStatus)
			_, _ = w.Write([]byte(createBody))
			return
		}
		_, _ = w.Write([]byte(`{"id":"kit-h1"}`))
	})
	mux.HandleFunc("/clients/kit-h1/pop", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.mu.Lock()
		rec.pops = append(rec.pops, popCall{id: "kit-h1", body: body})
		rec.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// readEvents GETs an SSE url and collects exactly n "data:" events (as
// event.Event), or fails the test after a 5s deadline.
func readEvents(t *testing.T, url string, n int) []event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	var out []event.Event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() && len(out) < n {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e event.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
			t.Fatalf("unmarshal SSE data %q: %v", line, err)
		}
		out = append(out, e)
	}
	if len(out) != n {
		t.Fatalf("read %d SSE events, want %d: %+v", len(out), n, out)
	}
	return out
}

// waitForState polls m.Status() until it reports want or a 2s deadline
// elapses (10ms step), per the brief's row-2 polling shape.
func waitForState(t *testing.T, m *Machine, want State) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := m.Status()
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for state %s; last status = %+v", want, st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- Row 1: bundle present ⇒ provisioned at New -------------------------

func TestNew_BundlePresentIsProvisioned(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")

	ident, err := shnsdk.GenerateIdentity("placeholder")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := shnsdk.WriteBundle(secretsDir, ident, "provider", "http://x"); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	m := New(Config{SecretsDir: secretsDir})

	st := m.Status()
	if st.State != StateProvisioned {
		t.Fatalf("State = %s, want %s", st.State, StateProvisioned)
	}
	if st.HolderID != "placeholder" {
		t.Errorf("HolderID = %q, want placeholder", st.HolderID)
	}

	select {
	case <-m.Provisioned():
	default:
		t.Error("Provisioned() channel not already closed")
	}

	if _, ok := m.Bundle(); !ok {
		t.Error("Bundle() ok = false, want true")
	}
}

// --- Row 2: full PKCE arc ------------------------------------------------

func TestSignIn_FullPKCEArc(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"expires_in":    3600,
		})
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")

	bus := event.NewBus(now)
	srv := httptest.NewServer(bus.Handler())
	defer srv.Close()
	resultCh := make(chan []event.Event, 1)
	go func() { resultCh <- readEvents(t, srv.URL+"/events", 5) }()

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Bus:             bus,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL == "" {
		t.Fatal("SignIn returned an empty authorize URL, want non-empty")
	}
	if st := m.Status(); st.State != StateSigningIn {
		t.Errorf("State right after SignIn = %s, want %s", st.State, StateSigningIn)
	}

	go func() { _, _ = http.Get(authzURL) }()

	st := waitForState(t, m, StateProvisioned)
	if st.HolderID != "kit-h1" {
		t.Errorf("HolderID = %q, want kit-h1", st.HolderID)
	}
	if st.Email != "dev@x.io" {
		t.Errorf("Email = %q, want dev@x.io", st.Email)
	}

	select {
	case <-m.Provisioned():
	default:
		t.Error("Provisioned() not closed")
	}

	rec.mu.Lock()
	creates := append([]map[string]string(nil), rec.creates...)
	pops := append([]popCall(nil), rec.pops...)
	rec.mu.Unlock()

	if len(creates) != 1 {
		t.Fatalf("creates = %d, want 1: %+v", len(creates), creates)
	}
	create := creates[0]
	if create["name"] != "SHN Kit" || create["role"] != "provider" ||
		create["encPub"] == "" || create["signPub"] == "" ||
		create["baseURL"] != "http://holder.example" {
		t.Errorf("create body = %+v, missing/incorrect fields", create)
	}
	if len(pops) != 1 || pops[0].id != "kit-h1" {
		t.Fatalf("pops = %+v, want one for kit-h1", pops)
	}

	b, err := shnsdk.LoadBundle(secretsDir)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	if b.Manifest.ID != "kit-h1" {
		t.Errorf("bundle manifest id = %q, want kit-h1", b.Manifest.ID)
	}

	tok, ok := tokens.Load()
	if !ok {
		t.Fatal("token store Load: ok = false, want true")
	}
	if tok.RefreshToken == "" {
		t.Error("token store holds no refresh token")
	}

	select {
	case events := <-resultCh:
		var signingIn, provisioning, provisioned = -1, -1, -1
		for i, e := range events {
			if e.Type != event.TypeBootstrap {
				t.Errorf("event[%d].Type = %q, want %q", i, e.Type, event.TypeBootstrap)
			}
			if signingIn == -1 && strings.Contains(e.Detail, "signing-in") {
				signingIn = i
			}
			if provisioning == -1 && strings.Contains(e.Detail, "provisioning") {
				provisioning = i
			}
			if provisioned == -1 && e.Detail == "provisioned" {
				provisioned = i
			}
		}
		if signingIn == -1 || provisioning == -1 || provisioned == -1 {
			t.Fatalf("missing expected event details: %+v", events)
		}
		if !(signingIn < provisioning && provisioning < provisioned) {
			t.Errorf("wrong event order: signing-in=%d provisioning=%d provisioned=%d (%+v)",
				signingIn, provisioning, provisioned, events)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the bus events")
	}
}

// --- Row 3: token-reuse fast path ----------------------------------------

func TestSignIn_TokenReuseFastPath(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("token endpoint hit unexpectedly on the token-reuse fast path")
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := tokens.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		AccessToken:  "at-existing",
		RefreshToken: "rt-existing",
		Expiry:       fixedNow.Add(time.Hour),
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL != "" {
		t.Errorf("SignIn authorize URL = %q, want empty (token-reuse fast path)", authzURL)
	}

	waitForState(t, m, StateProvisioned)

	if hits := cognito.AuthorizeHits(); hits != 0 {
		t.Errorf("authorize endpoint hits = %d, want 0", hits)
	}
}

// --- Row 4: refresh fast path ---------------------------------------------

func TestSignIn_RefreshFastPath(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostFormValue("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.PostFormValue("refresh_token"); got != "rt-1" {
			t.Errorf("refresh_token = %q, want rt-1", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at-2",
			"refresh_token": "rt-2",
			"expires_in":    3600,
		})
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := tokens.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		AccessToken:  "at-expired",
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL != "" {
		t.Errorf("SignIn authorize URL = %q, want empty (refresh fast path)", authzURL)
	}

	waitForState(t, m, StateProvisioned)

	if hits := cognito.AuthorizeHits(); hits != 0 {
		t.Errorf("authorize endpoint hits = %d, want 0", hits)
	}

	tok, ok := tokens.Load()
	if !ok || tok.RefreshToken != "rt-2" {
		t.Errorf("token store after refresh: ok=%v refreshToken=%q, want rt-2", ok, tok.RefreshToken)
	}
}

// --- Row 5: refresh fails ---------------------------------------------

func TestSignIn_RefreshFails(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := tokens.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	bus := event.NewBus(now)
	srv := httptest.NewServer(bus.Handler())
	defer srv.Close()
	resultCh := make(chan []event.Event, 1)
	go func() { resultCh <- readEvents(t, srv.URL+"/events", 2) }()

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Bus:             bus,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL != "" {
		t.Errorf("SignIn authorize URL = %q, want empty (refresh fast path)", authzURL)
	}

	st := waitForState(t, m, StateSignInRequired)
	if !strings.Contains(st.Detail, "sign in") {
		t.Errorf("Detail = %q, want mentioning sign in", st.Detail)
	}

	select {
	case events := <-resultCh:
		last := events[len(events)-1]
		if !strings.Contains(last.Detail, "failed") {
			t.Errorf("bus event detail = %q, want mentioning failed", last.Detail)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the failure bootstrap event")
	}
}

// --- Row 5b: refreshTokens' own discovery re-fetch fails ----------------
//
// refreshAndProvision re-fetches cli-config + OIDC discovery (neither is
// persisted with the tokens) before exchanging the refresh token; both
// fetches have their own error-wrapping return inside refreshTokens,
// distinct from the equivalent fetches SignIn's fresh-PKCE path makes.
// These two branches were previously unexercised — the refresh
// fast path's own accounts server always answered /cli-config successfully
// in the other rows above.

func TestSignIn_RefreshFastPath_CLIConfigFetchFails(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	accountsSrv := httptest.NewServer(http.NewServeMux()) // no /cli-config route
	defer accountsSrv.Close()

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := tokens.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	if _, err := m.SignIn(context.Background()); err != nil {
		t.Fatalf("SignIn: %v", err)
	}

	st := waitForState(t, m, StateSignInRequired)
	if !strings.Contains(st.Detail, "sign in") {
		t.Errorf("Detail = %q, want mentioning sign in", st.Detail)
	}
}

func TestSignIn_RefreshFastPath_OIDCFetchFails(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	issuer := httptest.NewServer(http.NewServeMux()) // no /.well-known/openid-configuration route
	defer issuer.Close()

	accountsMux := http.NewServeMux()
	accountsMux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":    issuer.URL,
			"client_id": "cli-1",
			"scopes":    []string{"openid", "email"},
		})
	})
	accountsSrv := httptest.NewServer(accountsMux)
	defer accountsSrv.Close()

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := tokens.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	if _, err := m.SignIn(context.Background()); err != nil {
		t.Fatalf("SignIn: %v", err)
	}

	st := waitForState(t, m, StateSignInRequired)
	if !strings.Contains(st.Detail, "sign in") {
		t.Errorf("Detail = %q, want mentioning sign in", st.Detail)
	}
}

// --- Row 6: accounts create fails ---------------------------------------

func TestSignIn_AccountsCreateFails(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"expires_in":    3600,
		})
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, http.StatusInternalServerError, "boom: internal registration error")

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	go func() { _, _ = http.Get(authzURL) }()

	st := waitForState(t, m, StateSignInRequired)
	if !strings.Contains(st.Detail, "boom: internal registration error") {
		t.Errorf("Detail = %q, want carrying the server body excerpt", st.Detail)
	}

	if _, err := os.Stat(secretsDir); !os.IsNotExist(err) {
		t.Errorf("secrets dir exists after a failed create: err=%v", err)
	}

	select {
	case <-m.Provisioned():
		t.Error("Provisioned() closed, want not closed")
	default:
	}
}

// --- Row 7: sign-in while busy --------------------------------------------

func TestSignIn_WhileBusyRejected(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token": idTokenWithEmail("dev@x.io"), "access_token": "at", "expires_in": 3600,
		})
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")
	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             time.Now,
		Ports:           []int{freePort(t)},
	})

	// Bound the wait so the leftover flow (never completed) self-cleans
	// quickly instead of parking its loopback listener for 5 minutes.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	authzURL, err := m.SignIn(ctx)
	if err != nil {
		t.Fatalf("first SignIn: %v", err)
	}
	if authzURL == "" {
		t.Fatal("first SignIn returned an empty authorize URL")
	}
	if st := m.Status(); st.State != StateSigningIn {
		t.Fatalf("State = %s, want %s", st.State, StateSigningIn)
	}

	_, err = m.SignIn(context.Background())
	if err == nil {
		t.Fatal("second concurrent SignIn: err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("second SignIn err = %v, want mentioning already in progress", err)
	}
}

// --- Row 8: reset ----------------------------------------------------------

func TestReset(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	ident, err := shnsdk.GenerateIdentity("placeholder")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := shnsdk.WriteBundle(secretsDir, ident, "provider", "http://x"); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	tokens := NewFileTokenStore(tokenPath, "https://accounts.example.org")
	if err := tokens.Save(accounts.Tokens{IDToken: "id-1"}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	m := New(Config{SecretsDir: secretsDir, Tokens: tokens, AccountsURL: "https://accounts.example.org"})
	if st := m.Status(); st.State != StateProvisioned {
		t.Fatalf("precondition: State = %s, want %s", st.State, StateProvisioned)
	}

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Errorf("token file still exists after Reset: err=%v", err)
	}
	if _, err := os.Stat(secretsDir); !os.IsNotExist(err) {
		t.Errorf("secrets dir still exists after Reset: err=%v", err)
	}

	st := m.Status()
	if st.State != StateSignInRequired {
		t.Errorf("State after Reset = %s, want %s", st.State, StateSignInRequired)
	}
	if !strings.Contains(strings.ToLower(st.Detail), "restart") {
		t.Errorf("Detail = %q, want mentioning restart", st.Detail)
	}
}

// --- Row 8b: Reset fences a straggling in-flight provision() ---------------

// TestReset_FencesStragglingProvision: SignIn's provision() goroutine parks
// mid-flight inside POST /clients; while it's parked, the operator calls
// Reset() (clears tokens, removes the secrets dir, flips to
// signin-required). The straggler is then unblocked and allowed to run to
// completion. Its final commit (state write, provisioned-close, WriteBundle,
// token Save) must all be no-ops — the operator's Reset must not be silently
// undone.
func TestReset_FencesStragglingProvision(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	popDone := make(chan struct{}, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // park here until the test has run Reset()
		_, _ = w.Write([]byte(`{"id":"kit-h1"}`))
	})
	mux.HandleFunc("/clients/kit-h1/pop", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
		popDone <- struct{}{}
	})
	accountsSrv := httptest.NewServer(mux)
	defer accountsSrv.Close()

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := tokens.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(time.Hour),
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL != "" {
		t.Errorf("SignIn authorize URL = %q, want empty (token-reuse fast path)", authzURL)
	}

	// Wait until provision() is genuinely parked inside POST /clients before
	// resetting, so the race is deterministic rather than best-effort.
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provision() to reach POST /clients")
	}

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if st := m.Status(); st.State != StateSignInRequired {
		t.Fatalf("State right after Reset = %s, want %s", st.State, StateSignInRequired)
	}

	// Let the straggler finish.
	close(release)
	select {
	case <-popDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the straggling provision() to reach /pop")
	}

	// Give provision()'s post-pop tail (generation check, WriteBundle,
	// saveTokens, final mu-commit) a moment to run; it's all local
	// filesystem work, so this settles in well under the budget below.
	time.Sleep(300 * time.Millisecond)

	if st := m.Status(); st.State != StateSignInRequired {
		t.Errorf("State after the straggler finished = %s, want %s (Reset must not be undone)", st.State, StateSignInRequired)
	}
	if _, err := os.Stat(secretsDir); !os.IsNotExist(err) {
		t.Errorf("secrets dir reappeared after Reset raced with a straggling provision(): err=%v", err)
	}
	select {
	case <-m.Provisioned():
		t.Error("Provisioned() closed by a straggling provision() after Reset")
	default:
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Errorf("token file reappeared after Reset raced with a straggling provision(): err=%v", err)
	}
}

// --- Row 8c: Reset fences a straggler parked between WriteBundle and the --
// --- final commit (via the TokenStore.Save seam) ---------------------------

// blockingTokenStore wraps a TokenStore so its FIRST Save call can be parked
// mid-flight: it signals entered (best-effort, once) then blocks on release
// before delegating, and closes saveDone once the delegate Save returns —
// giving a test a deterministic hook on "the straggler is between
// WriteBundle and its final commit" (bootstrap.go's check2/check3 window)
// without racing on timing. Every Save call AFTER the first delegates
// straight through with no parking — the inflight-latch tests reuse a single
// Machine (and hence a single Tokens store) across two generations, and only
// the FIRST generation is meant to park.
type blockingTokenStore struct {
	inner    TokenStore
	entered  chan struct{}
	release  chan struct{}
	saveDone chan struct{}

	once sync.Once

	// tokenPath, when set, is stat'd from INSIDE Save — right after the
	// delegate Save returns and strictly before saveDone closes — so a test
	// gets a deterministic "did the file exist the instant Save completed"
	// answer without racing the straggler's own post-Save cleanup goroutine
	// (which can only start once Save has returned). Guarded by mu since the
	// test goroutine reads it after saveDone but before any further Save.
	tokenPath string

	mu               sync.Mutex
	existedAfterSave bool
	statErrAfterSave error
}

func (b *blockingTokenStore) Load() (accounts.Tokens, bool) { return b.inner.Load() }

func (b *blockingTokenStore) Save(t accounts.Tokens) error {
	first := false
	b.once.Do(func() { first = true })
	if !first {
		return b.inner.Save(t)
	}
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	err := b.inner.Save(t)
	if b.tokenPath != "" {
		_, statErr := os.Stat(b.tokenPath)
		b.mu.Lock()
		b.existedAfterSave = statErr == nil
		b.statErrAfterSave = statErr
		b.mu.Unlock()
	}
	close(b.saveDone)
	return err
}

// tokenExistedAfterSave reports whether tokenPath existed at the instant the
// FIRST (parked) Save call's delegate returned — recorded inside Save itself
// so the answer can't race the straggler's cleanup, which only runs after
// Save has returned.
func (b *blockingTokenStore) tokenExistedAfterSave() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.existedAfterSave, b.statErrAfterSave
}

func (b *blockingTokenStore) Clear() error { return b.inner.Clear() }

// TestReset_FencesStragglingProvisionAfterWriteBundle: provision()'s
// straggler is parked inside Tokens.Save — i.e. strictly after WriteBundle
// has already put a bundle on disk (check2 already passed) and strictly
// before the final under-mu commit (check3). The operator's Reset lands in
// that window. When the straggler is released, it must undo its OWN stray
// side effects — the bundle Reset already removed must stay gone, and the
// token file the straggler's own (delegated) Save call recreates after
// Reset must be cleaned back up — rather than leaving a bundle+token pair
// on disk that a future shnkitd restart's New() (bundle-presence-only)
// would silently re-arm as provisioned.
func TestReset_FencesStragglingProvisionAfterWriteBundle(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, nil, rec, 0, "")

	inner := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := inner.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(time.Hour), // valid ⇒ token-reuse fast path
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	tokens := &blockingTokenStore{
		inner:     inner,
		entered:   make(chan struct{}, 1),
		release:   make(chan struct{}),
		saveDone:  make(chan struct{}),
		tokenPath: tokenPath,
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL != "" {
		t.Errorf("SignIn authorize URL = %q, want empty (token-reuse fast path)", authzURL)
	}

	// Wait until provision() is genuinely parked inside Tokens.Save — by
	// this point Create/SubmitPoP/WriteBundle have already run, so the
	// bundle is on disk.
	select {
	case <-tokens.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provision() to reach Tokens.Save")
	}
	if _, err := os.Stat(secretsDir); err != nil {
		t.Fatalf("secrets dir missing while the straggler is parked in Save: %v", err)
	}

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if st := m.Status(); st.State != StateSignInRequired {
		t.Fatalf("State right after Reset = %s, want %s", st.State, StateSignInRequired)
	}

	// Let the straggler's Save proceed.
	close(tokens.release)
	select {
	case <-tokens.saveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the straggler's Save to complete")
	}

	// The straggler's own (delegated) Save just recreated the token file
	// Reset had removed — proving the race is real before the straggler's
	// tail gets a chance to clean up after itself. This is recorded INSIDE
	// Save (before saveDone closed above) rather than stat'd here, because
	// cleanupStraggler() can start running the instant Save returns and can
	// legitimately win a stat issued from this goroutine.
	if existed, statErr := tokens.tokenExistedAfterSave(); !existed {
		t.Fatalf("stray token file missing right after the straggler's Save completed: %v", statErr)
	}

	// Give provision()'s tail (the final generation check + cleanup) a
	// moment to run; poll rather than sleep-and-hope.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the straggler to clean up its stray token file")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if st := m.Status(); st.State != StateSignInRequired {
		t.Errorf("State after the straggler finished = %s, want %s (Reset must not be undone)", st.State, StateSignInRequired)
	}
	if _, err := os.Stat(secretsDir); !os.IsNotExist(err) {
		t.Errorf("secrets dir reappeared after Reset raced with a straggling provision(): err=%v", err)
	}
	select {
	case <-m.Provisioned():
		t.Error("Provisioned() closed by a straggling provision() after Reset")
	default:
	}
}

// --- additional coverage: error paths + the nil-TokenStore contract -------

// TestSignIn_AlreadyProvisionedRejected: SignIn on an already-provisioned
// Machine must reject with a reset-first error (claimSignIn's other reject
// branch, alongside row 7's busy check).
func TestSignIn_AlreadyProvisionedRejected(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")

	ident, err := shnsdk.GenerateIdentity("placeholder")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := shnsdk.WriteBundle(secretsDir, ident, "provider", "http://x"); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	m := New(Config{SecretsDir: secretsDir})
	if st := m.Status(); st.State != StateProvisioned {
		t.Fatalf("precondition: State = %s, want %s", st.State, StateProvisioned)
	}

	if _, err := m.SignIn(context.Background()); err == nil {
		t.Fatal("SignIn on a provisioned Machine: err = nil, want an error")
	} else if !strings.Contains(err.Error(), "reset first") {
		t.Errorf("err = %v, want mentioning reset first", err)
	}
}

// TestSignIn_CLIConfigFetchFails: a down Accounts service fails the initial
// discovery fetch synchronously; SignIn surfaces the error and rolls the
// state back to signin-required.
func TestSignIn_CLIConfigFetchFails(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")

	accountsSrv := httptest.NewServer(http.NewServeMux()) // no /cli-config route
	defer accountsSrv.Close()

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Ports:           []int{freePort(t)},
	})

	if _, err := m.SignIn(context.Background()); err == nil {
		t.Fatal("SignIn: err = nil, want a discovery fetch error")
	}

	st := m.Status()
	if st.State != StateSignInRequired {
		t.Errorf("State = %s, want %s", st.State, StateSignInRequired)
	}
	if st.Detail == "" {
		t.Error("Detail is empty, want the fetch failure recorded")
	}
}

// TestSignIn_NilTokenStore: Config.Tokens == nil means no persistence (the
// documented test default). The full PKCE arc must still complete — no fast
// path is possible without a store to read from, and provisioning/Reset must
// not fail trying to touch a store that doesn't exist.
func TestSignIn_NilTokenStore(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"expires_in":    3600,
		})
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	go func() { _, _ = http.Get(authzURL) }()

	waitForState(t, m, StateProvisioned)

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset with a nil TokenStore: %v", err)
	}
}

// TestBundle_NotProvisioned: Bundle() before provisioning reports ok=false.
func TestBundle_NotProvisioned(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{SecretsDir: filepath.Join(dir, "secrets")})
	if _, ok := m.Bundle(); ok {
		t.Error("Bundle() ok = true before provisioning, want false")
	}
}

// TestProvision_MidFlightExpiryRefreshes: a token valid at claim time but
// expired by the time provision() actually runs (a real race; the
// step-after-first-call clock below simulates it deterministically) must
// transparently refresh before registering, per provision's mid-flight
// expiry contract.
func TestProvision_MidFlightExpiryRefreshes(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	var calls int32
	now := func() time.Time {
		if atomic.AddInt32(&calls, 1) == 1 {
			return fixedNow // claimSignIn's read: token still valid
		}
		return fixedNow.Add(2 * time.Hour) // every later read: token now expired
	}

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostFormValue("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at-2",
			"refresh_token": "rt-2",
			"expires_in":    3600,
		})
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := tokens.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(time.Hour), // valid at claim (1st Now() read)
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL != "" {
		t.Errorf("SignIn authorize URL = %q, want empty (reuse claim)", authzURL)
	}

	waitForState(t, m, StateProvisioned)

	tok, ok := tokens.Load()
	if !ok || tok.RefreshToken != "rt-2" {
		t.Errorf("token store after mid-flight refresh: ok=%v refreshToken=%q, want rt-2", ok, tok.RefreshToken)
	}
}

// TestProvision_MidFlightExpiryNoRefreshFails: same race, but the token
// carries no refresh token — provision() must fail with the
// sign-in-again message rather than attempt a refresh it cannot make.
func TestProvision_MidFlightExpiryNoRefreshFails(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	var calls int32
	now := func() time.Time {
		if atomic.AddInt32(&calls, 1) == 1 {
			return fixedNow
		}
		return fixedNow.Add(2 * time.Hour)
	}

	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, nil, rec, 0, "")

	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := tokens.Save(accounts.Tokens{
		IDToken: idTokenWithEmail("dev@x.io"),
		Expiry:  fixedNow.Add(time.Hour), // valid at claim, no refresh token
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL != "" {
		t.Errorf("SignIn authorize URL = %q, want empty (reuse claim)", authzURL)
	}

	st := waitForState(t, m, StateSignInRequired)
	if !strings.Contains(st.Detail, "sign in") {
		t.Errorf("Detail = %q, want mentioning sign in", st.Detail)
	}
}

// --- Inflight latch serializes generations ----------------------------------

// TestSignIn_InflightLatchSerializesGenerations reproduces the race this
// latch prevents: G1 parks inside Tokens.Save with its bundle
// already on disk (WriteBundle succeeded). Reset() fires — clearing G1's
// bundle/tokens and flipping state to signin-required — and an operator
// immediately retries SignIn. Without the latch, that immediate retry (G2)
// would be allowed to start (and fully provision+commit) purely because state
// already read signin-required; G1's late cleanup would then fire and
// delete G2's LEGITIMATE bundle+tokens. It must instead be rejected as
// "already in progress" until G1 has fully unwound, at which point a fresh
// SignIn is allowed to run to completion untouched by G1's (already
// finished) cleanup.
func TestSignIn_InflightLatchSerializesGenerations(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at-2",
			"refresh_token": "rt-2",
			"expires_in":    3600,
		})
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")

	inner := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := inner.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(time.Hour), // valid ⇒ G1 takes the reuse fast path
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	tokens := &blockingTokenStore{
		inner:    inner,
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		saveDone: make(chan struct{}),
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	// G1: reuse fast path — runs Create/SubmitPoP/WriteBundle, then parks in
	// Tokens.Save (blockingTokenStore's first, blocking call).
	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("G1 SignIn: %v", err)
	}
	if authzURL != "" {
		t.Fatalf("G1 SignIn authorize URL = %q, want empty (reuse fast path)", authzURL)
	}

	select {
	case <-tokens.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for G1's provision() to reach Tokens.Save")
	}
	if _, err := os.Stat(secretsDir); err != nil {
		t.Fatalf("secrets dir missing while G1 is parked in Save: %v", err)
	}

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if st := m.Status(); st.State != StateSignInRequired {
		t.Fatalf("State right after Reset = %s, want %s", st.State, StateSignInRequired)
	}

	// G1 is still parked in Save — an immediate re-SignIn (G2, attempt 1)
	// MUST be rejected even though state already reads signin-required.
	// Pre-fix (gen alone, no inflight latch) this call succeeds with no
	// error and starts a second, concurrent generation — the Critical
	// finding.
	if _, err := m.SignIn(context.Background()); err == nil {
		t.Fatal("SignIn immediately after Reset (G1 still unwinding): err = nil, want already-in-progress")
	} else if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("SignIn immediately after Reset: err = %v, want mentioning already in progress", err)
	}

	// Let G1 proceed: its delegated Save recreates the token file, then its
	// own stale-generation cleanup must remove both the bundle and that
	// token file again before releasing the latch.
	close(tokens.release)
	select {
	case <-tokens.saveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for G1's Save to complete")
	}

	// Poll SignIn until G1 has fully unwound (its own cleanup has released
	// the inflight latch) — this is the real, legitimate G2 attempt.
	deadline := time.Now().Add(2 * time.Second)
	var g2AuthzURL string
	for {
		g2AuthzURL, err = m.SignIn(context.Background())
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "already in progress") {
			t.Fatalf("SignIn while waiting for G1 to unwind: unexpected err = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for G1's inflight latch to clear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if g2AuthzURL == "" {
		t.Fatal("G2 SignIn returned an empty authorize URL, want a fresh PKCE run (G1's tokens are gone)")
	}
	go func() { _, _ = http.Get(g2AuthzURL) }()

	st := waitForState(t, m, StateProvisioned)
	if st.HolderID != "kit-h1" {
		t.Errorf("HolderID = %q, want kit-h1", st.HolderID)
	}

	// G1's straggling cleanup (long since finished before G2 was even
	// allowed to start) must not have raced back in and deleted G2's
	// legitimate, freshly-committed bundle/tokens.
	if _, err := shnsdk.LoadBundle(secretsDir); err != nil {
		t.Errorf("LoadBundle after G2 provisioned: %v (bundle clobbered by G1's straggling cleanup?)", err)
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Errorf("token file missing after G2 provisioned: %v", err)
	}
	select {
	case <-m.Provisioned():
	default:
		t.Error("Provisioned() not closed after G2 provisioned")
	}
}

// failAfterSaveTokenStore wraps a TokenStore so its Save call can be parked
// mid-flight (the same entered/release/saveDone handshake as
// blockingTokenStore) but, once released, ALWAYS reports failure even
// though its delegate Save() call genuinely persists the token first —
// modeling a realistic partial-success backend (e.g. an OS keychain: the
// secret lands, but the overall operation still errors) that reaches
// fail()'s stale branch specifically, rather than provision()'s own
// already-fixed post-WriteBundle/final-commit stale(gen) checks.
type failAfterSaveTokenStore struct {
	inner    TokenStore
	entered  chan struct{}
	release  chan struct{}
	saveDone chan struct{}
}

func (f *failAfterSaveTokenStore) Load() (accounts.Tokens, bool) { return f.inner.Load() }

func (f *failAfterSaveTokenStore) Save(t accounts.Tokens) error {
	select {
	case f.entered <- struct{}{}:
	default:
	}
	<-f.release
	_ = f.inner.Save(t) // the delegate genuinely persists the token...
	close(f.saveDone)
	return errors.New("save boom: partial-success backend still reports failure")
}

func (f *failAfterSaveTokenStore) Clear() error { return f.inner.Clear() }

// TestFail_StaleBranchCleansUpAfterSaveTokensError is the Important
// finding's scenario: WriteBundle succeeds (bundle on disk, check2 already
// passed) → the straggler parks inside Tokens.Save → a concurrent Reset()
// fences it (removing the bundle + the pre-saved token file, bumping gen) →
// the straggler is released: its delegate Save() genuinely RECREATES the
// token file, but the store still reports an error overall → provision()
// takes the saveTokens-error branch into fail(), NOT check3. Before this
// fix, fail()'s stale branch did nothing, leaving that just-recreated token
// file stranded on disk (the exact class of bug the prior WriteBundle-window
// fix was for, reachable here via any TokenStore whose Save can fail). It
// must instead clean up, and the inflight latch must still clear so a later
// SignIn is not stuck reporting "in progress" forever.
func TestFail_StaleBranchCleansUpAfterSaveTokensError(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, nil, rec, 0, "")

	inner := NewFileTokenStore(tokenPath, accountsSrv.URL)
	if err := inner.Save(accounts.Tokens{
		IDToken:      idTokenWithEmail("dev@x.io"),
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		Expiry:       fixedNow.Add(time.Hour), // valid ⇒ reuse fast path, no PKCE needed
	}); err != nil {
		t.Fatalf("pre-save tokens: %v", err)
	}

	tokens := &failAfterSaveTokenStore{
		inner:    inner,
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		saveDone: make(chan struct{}),
	}

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL != "" {
		t.Errorf("SignIn authorize URL = %q, want empty (reuse fast path)", authzURL)
	}

	// Wait until provision() is parked inside Tokens.Save — by this point
	// Create/SubmitPoP/WriteBundle have already run, so the bundle is on
	// disk and check2 has already passed.
	select {
	case <-tokens.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provision() to reach Tokens.Save")
	}
	if _, err := os.Stat(secretsDir); err != nil {
		t.Fatalf("secrets dir missing while the straggler is parked in Save: %v", err)
	}

	// The operator resets mid-flight — strictly after check2, so the
	// straggler is committed to the saveTokens/fail() path rather than
	// check2's own (already-fixed) cleanup branch.
	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if st := m.Status(); st.State != StateSignInRequired {
		t.Fatalf("State right after Reset = %s, want %s", st.State, StateSignInRequired)
	}

	// Let the straggler's Save proceed: its delegate genuinely re-persists
	// the token (recreating the file Reset just removed), then the wrapper
	// still reports failure — routing provision() into fail(), not check3.
	// (No intermediate "file exists right after Save returns" assertion
	// here: fail()'s own cleanup runs on the same goroutine immediately
	// afterward with no further synchronization point, so that window isn't
	// reliably observable — asserting on it would race the very fix this
	// test is proving. The poll below is the load-bearing assertion.)
	close(tokens.release)
	select {
	case <-tokens.saveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the straggler's Save to complete")
	}

	// fail()'s stale branch must clean that stray token file back up
	// (bounded poll, not a sleep-and-hope).
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for fail()'s stale branch to clean up the stray token file")
		}
		time.Sleep(10 * time.Millisecond)
	}

	st := waitForState(t, m, StateSignInRequired)
	if !strings.Contains(strings.ToLower(st.Detail), "restart") {
		t.Errorf("Detail = %q, want the Reset detail (mentioning restart) — fail()'s stale branch must not stomp state a concurrent Reset already committed", st.Detail)
	}
	if _, err := os.Stat(secretsDir); !os.IsNotExist(err) {
		t.Errorf("secrets dir reappeared after the straggler's fenced failure: err=%v", err)
	}

	// The inflight latch must have cleared: a later SignIn either succeeds
	// cleanly or fails for a reason OTHER than "already in progress" — it
	// must never stay stuck reporting in-progress forever.
	deadline = time.Now().Add(2 * time.Second)
	for {
		_, err := m.SignIn(context.Background())
		if err == nil || !strings.Contains(err.Error(), "already in progress") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the inflight latch to clear after fail()'s stale branch")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- Exported sentinels, Reset closes the in-flight PKCE flow --------------

// TestSignIn_SentinelErrors pins the identity of the two exported sentinels
// (errors.Is, not string matching) that kitd's mux mapping switches
// on to produce 409s: ErrAlreadyProvisioned once the Machine is provisioned,
// ErrSignInInProgress while a previous generation is still claimed.
func TestSignIn_SentinelErrors(t *testing.T) {
	t.Run("already provisioned", func(t *testing.T) {
		dir := t.TempDir()
		secretsDir := filepath.Join(dir, "secrets")

		ident, err := shnsdk.GenerateIdentity("placeholder")
		if err != nil {
			t.Fatalf("GenerateIdentity: %v", err)
		}
		if err := shnsdk.WriteBundle(secretsDir, ident, "provider", "http://x"); err != nil {
			t.Fatalf("WriteBundle: %v", err)
		}

		m := New(Config{SecretsDir: secretsDir})
		if st := m.Status(); st.State != StateProvisioned {
			t.Fatalf("precondition: State = %s, want %s", st.State, StateProvisioned)
		}

		_, err = m.SignIn(context.Background())
		if !errors.Is(err, ErrAlreadyProvisioned) {
			t.Errorf("errors.Is(err, ErrAlreadyProvisioned) = false, err = %v", err)
		}
	})

	t.Run("mid-flight", func(t *testing.T) {
		dir := t.TempDir()
		secretsDir := filepath.Join(dir, "secrets")
		tokenPath := filepath.Join(dir, "tokens.json")

		cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id_token": idTokenWithEmail("dev@x.io"), "access_token": "at", "expires_in": 3600,
			})
		})
		rec := &accountsRecorder{}
		accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")
		tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)

		m := New(Config{
			AccountsURL:     accountsSrv.URL,
			SecretsDir:      secretsDir,
			ClientName:      "SHN Kit",
			Role:            "provider",
			RegisterBaseURL: "http://holder.example",
			Tokens:          tokens,
			Now:             time.Now,
			Ports:           []int{freePort(t)},
		})

		// Bound the wait so the leftover flow (never completed) self-cleans
		// quickly instead of parking its loopback listener for 5 minutes.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		authzURL, err := m.SignIn(ctx)
		if err != nil {
			t.Fatalf("first SignIn: %v", err)
		}
		if authzURL == "" {
			t.Fatal("first SignIn returned an empty authorize URL")
		}

		_, err = m.SignIn(context.Background())
		if !errors.Is(err, ErrSignInInProgress) {
			t.Errorf("errors.Is(err, ErrSignInInProgress) = false, err = %v", err)
		}
	})
}

// --- Status.AuthExpiry omitzero ---------------------------------------------

// TestStatus_AuthExpiryOmitzero pins the JSON wire shape the UI polls:
// a zero-value AuthExpiry must not appear in the marshaled Status at all
// (omitzero, not omitempty's zero-time special case), while a non-zero one
// must.
func TestStatus_AuthExpiryOmitzero(t *testing.T) {
	b, err := json.Marshal(Status{State: StateSignInRequired})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "authExpiry") {
		t.Errorf("marshaled zero-value Status contains authExpiry: %s", b)
	}

	fixed := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	b, err = json.Marshal(Status{State: StateProvisioned, AuthExpiry: fixed})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), "authExpiry") {
		t.Errorf("marshaled non-zero AuthExpiry Status missing authExpiry: %s", b)
	}
}

// --- Reset closes the in-flight PKCE flow -----------------------------------

// TestReset_ClosesInFlightPKCEFlow: a SignIn parks its PKCE flow in Wait
// (browser never driven — cfg.OpenBrowser is unset). Reset() must Close()
// that retained flow so the parked Wait unblocks immediately (rather than
// sitting on the 5-minute bound) and its stale-gen fail()+finish()
// release the inflight latch promptly enough for a NEW SignIn to succeed —
// with a fresh authorize URL and no ErrSignInInProgress — well within this
// test's short poll budget.
func TestReset_ClosesInFlightPKCEFlow(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	cognito := newFakeCognito(t, nil) // the parked flow never reaches /token
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")
	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             time.Now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if authzURL == "" {
		t.Fatal("SignIn returned an empty authorize URL, want non-empty")
	}

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var newURL string
	for {
		newURL, err = m.SignIn(context.Background())
		if err == nil {
			break
		}
		if !errors.Is(err, ErrSignInInProgress) {
			t.Fatalf("SignIn while polling after Reset: unexpected err = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Reset to close the in-flight flow and release the inflight latch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if newURL == "" {
		t.Fatal("new SignIn after Reset returned an empty authorize URL, want a fresh PKCE run")
	}
	if st := m.Status(); st.State != StateSigningIn {
		t.Errorf("State after the new SignIn = %s, want %s", st.State, StateSigningIn)
	}
}

// --- Flow cleared on normal completion — Reset then flow no-op -------------

// TestReset_AfterCompletedSignInFlowIsNil: once a PKCE flow completes
// normally, waitAndProvision's deferred cleanup clears m.flow back to nil.
// This is asserted directly (package bootstrap gives us the field) right
// before Reset() runs, rather than inferred from Close's idempotency — a
// redundant Close on a non-nil flow would be indistinguishable from this
// invariant by any external observation. Reset() is then called on top of
// that pinned-nil state and must still complete cleanly (skipping the
// Close() call entirely rather than merely tolerating a redundant one).
func TestReset_AfterCompletedSignInFlowIsNil(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	cognito := newFakeCognito(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"expires_in":    3600,
		})
	})
	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, cognito, rec, 0, "")
	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             now,
		Ports:           []int{freePort(t)},
	})

	authzURL, err := m.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	go func() { _, _ = http.Get(authzURL) }()

	waitForState(t, m, StateProvisioned)

	// Direct assertion (package bootstrap, in-package test): waitAndProvision's
	// deferred cleanup must have cleared m.flow on normal completion, BEFORE
	// Reset() ever runs — otherwise a redundant sync.Once Close() would mask
	// the very invariant this test claims to pin.
	// Bounded poll (2s cap, 10ms step), not a one-shot read: waitAndProvision's
	// deferred cleanup runs in its own goroutine, so provisioned-status
	// visibility (waitForState above) can precede the m.flow clear — a
	// one-shot read right after is a flake window, not a genuine invariant
	// check.
	var flow *accounts.PKCEFlow
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		flow = m.flow
		m.mu.Unlock()
		if flow == nil || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if flow != nil {
		t.Fatalf("m.flow = %v after a completed sign-in, want nil (waitAndProvision's deferred cleanup must have cleared m.flow on normal completion)", flow)
	}

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset after a completed sign-in: %v", err)
	}
	if st := m.Status(); st.State != StateSignInRequired {
		t.Errorf("State after Reset = %s, want %s", st.State, StateSignInRequired)
	}
}

// --- The SignIn staleness branch itself -------------------------------------

// TestSignIn_ResetRacesStaleness forces the staleness branch added right
// after StartPKCE returns in SignIn (bootstrap.go ~lines 267-276): a
// concurrent Reset() must be able to land AFTER claimSignIn has claimed the
// transition but BEFORE StartPKCE returns, so that by the time SignIn
// resumes and takes the mu-guarded gen check, m.gen no longer matches the
// generation it claimed. The fake OIDC-discovery endpoint blocks on a
// channel (mirroring TestReset_FencesStragglingProvision's blocking-fixture
// pattern) so the race is deterministic rather than best-effort: the test
// waits until SignIn is genuinely parked inside FetchOIDC — strictly before
// StartPKCE is even called — before calling Reset().
//
// Expected outcome: the parked SignIn call returns an error mentioning
// "reset during sign-in" (never a live flow's authorize URL — the flow must
// be Close()'d immediately, not exposed to the operator); the abort unwinds
// via fail's existing stale branch (cleanup + finish), which must actually
// release the inflight latch — proven by a SECOND SignIn succeeding (or at
// least never wedging on ErrSignInInProgress) within a short poll budget.
func TestSignIn_ResetRacesStaleness(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	tokenPath := filepath.Join(dir, "tokens.json")

	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	cognitoMux := http.NewServeMux()
	cognitoMux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // park here until the test has run Reset()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": "http://127.0.0.1/authorize",
			"token_endpoint":         "http://127.0.0.1/token",
		})
	})
	cognitoSrv := httptest.NewServer(cognitoMux)
	defer cognitoSrv.Close()

	rec := &accountsRecorder{}
	accountsSrv := newFakeAccounts(t, &fakeCognito{Server: cognitoSrv}, rec, 0, "")
	tokens := NewFileTokenStore(tokenPath, accountsSrv.URL)

	m := New(Config{
		AccountsURL:     accountsSrv.URL,
		SecretsDir:      secretsDir,
		ClientName:      "SHN Kit",
		Role:            "provider",
		RegisterBaseURL: "http://holder.example",
		Tokens:          tokens,
		Now:             time.Now,
		// This test drives two back-to-back sign-ins (the parked/reset flow, then a
		// fresh one). A single-port pool is racy: the second SignIn can land on the
		// first flow's not-yet-released loopback listener ("no free loopback port").
		// Give it several ports so the fresh flow always has one free to bind.
		Ports: []int{freePort(t), freePort(t), freePort(t)},
	})

	type signInResult struct {
		url string
		err error
	}
	firstDone := make(chan signInResult, 1)
	go func() {
		url, err := m.SignIn(context.Background())
		firstDone <- signInResult{url, err}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SignIn to reach OIDC discovery (before StartPKCE)")
	}

	if st := m.Status(); st.State != StateSigningIn {
		t.Fatalf("State while parked in OIDC discovery = %s, want %s", st.State, StateSigningIn)
	}

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if st := m.Status(); st.State != StateSignInRequired {
		t.Fatalf("State right after Reset = %s, want %s", st.State, StateSignInRequired)
	}

	close(release) // let the parked SignIn resume: FetchOIDC returns, StartPKCE runs locally, then the gen check fires

	var first signInResult
	select {
	case first = <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the parked SignIn to return after Reset")
	}
	if first.err == nil {
		t.Fatal("parked SignIn returned nil error, want one mentioning \"reset during sign-in\"")
	}
	if !strings.Contains(first.err.Error(), "reset during sign-in") {
		t.Errorf("parked SignIn err = %v, want it to mention %q", first.err, "reset during sign-in")
	}
	if first.url != "" {
		t.Errorf("parked SignIn authorize URL = %q, want empty (a stale flow must never be exposed to the operator)", first.url)
	}

	if st := m.Status(); st.State != StateSignInRequired {
		t.Errorf("State after the stale SignIn unwound = %s, want %s (fail's stale branch must not stomp Reset's committed state)", st.State, StateSignInRequired)
	}

	// The inflight latch must have been released via fail's stale branch
	// (cleanup + finish) — prove it by driving a fresh SignIn to success (or
	// at least off ErrSignInInProgress) within a short poll budget. Bound the
	// new flow's own wait tightly so its loopback listener doesn't linger
	// past this test.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	deadline := time.Now().Add(2 * time.Second)
	var newURL string
	var err error
	for {
		newURL, err = m.SignIn(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrSignInInProgress) {
			t.Fatalf("second SignIn: unexpected err = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the stale SignIn's cleanup to release the inflight latch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if newURL == "" {
		t.Error("second SignIn returned an empty authorize URL, want a fresh PKCE run")
	}
	if st := m.Status(); st.State != StateSigningIn {
		t.Errorf("State after the second SignIn = %s, want %s", st.State, StateSigningIn)
	}
}
