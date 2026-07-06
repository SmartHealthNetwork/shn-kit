// update_test.go — hermetic tests for the launch-time update check.
// Every case stubs the feed via httptest — no live network,
// no real github.com dependency.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// feedServer stands up an httptest.Server answering GET with the given body
// and status, mirroring GitHub's /releases/latest response shape.
func feedServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func releaseJSON(t *testing.T, tag, htmlURL string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"tag_name": tag, "html_url": htmlURL})
	if err != nil {
		t.Fatalf("marshal release JSON: %v", err)
	}
	return string(b)
}

// ---- Row 1: available/not, semver compare incl. v-prefixes ----------------

func TestCheck_AvailableWhenFeedNewer(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "v1.3.0", "https://example.org/releases/v1.3.0"))
	info, err := Check(context.Background(), srv.Client(), srv.URL, "1.2.0")
	if err != nil {
		t.Fatalf("Check: unexpected error: %v", err)
	}
	if !info.Available {
		t.Fatalf("Available = false, want true (feed v1.3.0 > current 1.2.0)")
	}
	if info.Latest != "v1.3.0" {
		t.Errorf("Latest = %q, want v1.3.0", info.Latest)
	}
	if info.URL != "https://example.org/releases/v1.3.0" {
		t.Errorf("URL = %q, want https://example.org/releases/v1.3.0", info.URL)
	}
}

func TestCheck_NotAvailableWhenFeedSame(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "v1.2.0", "https://example.org/releases/v1.2.0"))
	info, err := Check(context.Background(), srv.Client(), srv.URL, "1.2.0")
	if err != nil {
		t.Fatalf("Check: unexpected error: %v", err)
	}
	if info.Available {
		t.Fatalf("Available = true, want false (feed v1.2.0 == current 1.2.0)")
	}
	if info.Latest != "" || info.URL != "" {
		t.Errorf("Info = %+v, want zero Latest/URL when not available", info)
	}
}

func TestCheck_NotAvailableWhenFeedOlder(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "1.1.0", "https://example.org/releases/v1.1.0"))
	info, err := Check(context.Background(), srv.Client(), srv.URL, "v1.2.0")
	if err != nil {
		t.Fatalf("Check: unexpected error: %v", err)
	}
	if info.Available {
		t.Fatalf("Available = true, want false (feed 1.1.0 < current v1.2.0)")
	}
}

// TestCheck_VPrefixVariants proves the compare is agnostic to which side (or
// neither/both) carries the "v" prefix.
func TestCheck_VPrefixVariants(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		current string
		want    bool
	}{
		{"both v-prefixed, newer", "v2.0.0", "v1.9.9", true},
		{"tag bare, current v-prefixed, equal", "1.5.0", "v1.5.0", false},
		{"tag v-prefixed, current bare, newer patch", "v1.5.2", "1.5.1", true},
		{"minor beats patch", "v1.6.0", "1.5.9", true},
		{"major beats minor/patch", "v2.0.0", "1.9.9", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := feedServer(t, http.StatusOK, releaseJSON(t, tc.tag, "https://example.org/x"))
			info, err := Check(context.Background(), srv.Client(), srv.URL, tc.current)
			if err != nil {
				t.Fatalf("Check: unexpected error: %v", err)
			}
			if info.Available != tc.want {
				t.Fatalf("Check(tag=%s, current=%s).Available = %v, want %v", tc.tag, tc.current, info.Available, tc.want)
			}
		})
	}
}

// ---- Row 2: malformed feed => error, caller logs, never surfaces ----------

func TestCheck_MalformedJSON_Error(t *testing.T) {
	srv := feedServer(t, http.StatusOK, "{ not json")
	info, err := Check(context.Background(), srv.Client(), srv.URL, "1.0.0")
	if err == nil {
		t.Fatal("Check(malformed JSON) = nil error, want non-nil")
	}
	if info != (Info{}) {
		t.Errorf("Check(malformed JSON) Info = %+v, want zero value alongside the error", info)
	}
}

func TestCheck_EmptyTagName_Error(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "", "https://example.org/x"))
	_, err := Check(context.Background(), srv.Client(), srv.URL, "1.0.0")
	if err == nil {
		t.Fatal("Check(empty tag_name) = nil error, want non-nil")
	}
}

// TestCheck_OversizedBody_Error proves a feed serving a body larger than
// shnsdk.MaxResponseBytes is rejected with an error rather than being read
// unbounded into memory: Check must wrap resp.Body in
// io.LimitReader(resp.Body, shnsdk.MaxResponseBytes), the same idiom already
// used at bootstrap/verify.go:98, byo/byo.go:371, and byo/browse.go:89. The
// oversized padding lives in a trailing JSON field so the body is otherwise
// well-formed — a truncated read (not merely malformed JSON) is what proves
// the cap is actually enforced.
func TestCheck_OversizedBody_Error(t *testing.T) {
	pad := strings.Repeat("x", int(shnsdk.MaxResponseBytes)+1024)
	body := fmt.Sprintf(`{"tag_name":"v1.0.0","html_url":"https://example.org/x","padding":%q}`, pad)
	srv := feedServer(t, http.StatusOK, body)
	info, err := Check(context.Background(), srv.Client(), srv.URL, "0.5.0")
	if err == nil {
		t.Fatal("Check(oversized body) = nil error, want non-nil (body exceeds shnsdk.MaxResponseBytes)")
	}
	if info != (Info{}) {
		t.Errorf("Check(oversized body) Info = %+v, want zero value alongside the error", info)
	}
}

func TestCheck_NonOKStatus_Error(t *testing.T) {
	srv := feedServer(t, http.StatusNotFound, `{"message":"Not Found"}`)
	_, err := Check(context.Background(), srv.Client(), srv.URL, "1.0.0")
	if err == nil {
		t.Fatal("Check(404 status) = nil error, want non-nil")
	}
}

func TestCheck_UnreachableFeed_Error(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "v1.0.0", ""))
	url := srv.URL
	srv.Close() // closed before the call: connection refused, no live network involved
	_, err := Check(context.Background(), http.DefaultClient, url, "1.0.0")
	if err == nil {
		t.Fatal("Check(unreachable feed) = nil error, want non-nil")
	}
}

func TestCheck_UnparseableCurrentVersion_Error(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "v1.0.0", "https://example.org/x"))
	_, err := Check(context.Background(), srv.Client(), srv.URL, "not-a-version-at-all")
	if err == nil {
		t.Fatal("Check(unparseable current) = nil error, want non-nil")
	}
}

// ---- Row 3: dev-version skip is the CALLER's rule, not Check's ------------

// TestCheck_DoesNotSpecialCaseDevVersion proves Check has no notion of "dev
// build" — passing the ldflags dev sentinel "0.0.0-dev" as current is
// compared like any other version (leading-digit parse: "0-dev" => 0), never
// silently skipped. Skipping dev builds entirely is main's job (Step 3).
func TestCheck_DoesNotSpecialCaseDevVersion(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "v1.0.0", "https://example.org/x"))
	info, err := Check(context.Background(), srv.Client(), srv.URL, "0.0.0-dev")
	if err != nil {
		t.Fatalf("Check(current=0.0.0-dev): unexpected error: %v", err)
	}
	if !info.Available {
		t.Fatalf("Check(current=0.0.0-dev).Available = false, want true — Check must not skip/special-case the dev sentinel itself")
	}
}

// TestCheck_NilHTTPClientDefaults proves a nil *http.Client falls back to
// http.DefaultClient rather than panicking (defensive default, mirroring
// bootstrap.Verify's own hc==nil guard).
func TestCheck_NilHTTPClientDefaults(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "v1.0.0", "https://example.org/x"))
	_, err := Check(context.Background(), nil, srv.URL, "0.5.0")
	if err != nil {
		t.Fatalf("Check(nil client): unexpected error: %v", err)
	}
}

// TestCheck_ContextCancellation proves a cancelled ctx surfaces as an error
// rather than hanging (bounded by the caller, mirroring every other
// ctx-bounded call in this codebase).
func TestCheck_ContextCancellation(t *testing.T) {
	srv := feedServer(t, http.StatusOK, releaseJSON(t, "v1.0.0", "https://example.org/x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Check(ctx, srv.Client(), srv.URL, "0.5.0")
	if err == nil {
		t.Fatal("Check(cancelled ctx) = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("Check(cancelled ctx) error = %v, want it to wrap context.Canceled", err)
	}
}
