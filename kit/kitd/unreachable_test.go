package kitd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// A test that needs an unreachable upstream never uses a closed test server's
// address: its port can be handed to the next listener any test starts,
// turning the failure into a live answer from the wrong server. Where the code
// under test takes an *http.Client, it gets refusingClient; where it dials a
// URL with its own client, it gets droppingChild, which keeps its port held.

// errRefused is what refusingClient's transport returns for every request.
var errRefused = errors.New("connect: connection refused")

// refusingClient fails every request at the transport, the way a refused
// connection does. At cleanup it requires that the transport was reached,
// and only with the expected method and path, so a row cannot pass through a
// different branch.
func refusingClient(t *testing.T, method, path string) *http.Client {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if len(seen) == 0 {
			t.Error("upstream transport was never reached: the row did not take the unreachable branch")
		}
		for _, s := range seen {
			if s != method+" "+path {
				t.Errorf("upstream request = %s, want %s %s", s, method, path)
			}
		}
	})
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		return nil, errRefused
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// droppingChild stands in for a gateway child that cannot answer: it holds its
// port for the whole test and closes every connection without a response, so
// the caller's client fails at the transport. It returns the base URL and a
// func reporting each request that reached it as "METHOD PATH".
func droppingChild(t *testing.T) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}
