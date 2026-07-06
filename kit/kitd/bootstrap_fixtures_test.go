// bootstrap_fixtures_test.go — minimal fake Cognito/Accounts fixtures for
// driving a REAL bootstrap.Machine's PKCE arc through kitd's HTTP surface.
// This is a small, package-local re-implementation of
// kit/bootstrap/bootstrap_test.go's fixtures of the same names: cross-package
// _test.go helpers aren't importable, and kitd's tests don't need the fuller
// fixture (create/pop body assertions, configurable failure injection) that
// package owns — only enough to drive one sign-in arc end to end.
package kitd

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// idTokenWithEmail builds a JWT-ish string whose middle segment
// base64url-decodes to {"email": email}. No signature is verified anywhere
// in the bootstrap package.
func idTokenWithEmail(email string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return "hdr." + payload + ".sig"
}

// freePort allocates an ephemeral 127.0.0.1 port and releases it
// immediately, so tests never bind the registered loopback ports
// (8400-8404) accounts.LoopbackPorts defaults to.
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
// endpoint that 302s straight to the loopback redirect_uri with a fixed
// code + a /token endpoint that always answers a fresh, long-lived token
// set for dev@x.io.
type fakeCognito struct {
	*httptest.Server
}

func newFakeCognito(t *testing.T) *fakeCognito {
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
		redir := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		http.Redirect(w, r, redir+"?code=test-code&state="+state, http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"expires_in":    3600,
		})
	})
	fc.Server = httptest.NewServer(mux)
	t.Cleanup(fc.Close)
	return fc
}

// accountsRecorder captures what the fake Accounts service saw. kitd's
// tests only assert the arc completes (not the create/pop request bodies —
// kit/bootstrap's own tests already cover that), so it just counts.
type accountsRecorder struct {
	mu      sync.Mutex
	creates int
}

func (r *accountsRecorder) Creates() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates
}

// newFakeAccounts starts a fake Accounts service wired to cognito:
// /cli-config, POST /clients (always 200 {"id":"kit-h1"}), and POST
// /clients/kit-h1/pop (always 200 {}).
func newFakeAccounts(t *testing.T, cognito *fakeCognito, rec *accountsRecorder) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":    cognito.Server.URL,
			"client_id": "cli-1",
			"scopes":    []string{"openid", "email"},
		})
	})
	mux.HandleFunc("/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		rec.mu.Lock()
		rec.creates++
		rec.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"kit-h1"}`))
	})
	mux.HandleFunc("/clients/kit-h1/pop", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
