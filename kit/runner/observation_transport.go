package runner

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// DispatchObserver retains unresolved direct and forwarded dispatch uncertainty
// for the daemon lifetime. Gateway-only replacement deliberately cannot reset it.
type DispatchObserver struct {
	taint   atomic.Bool
	pending atomic.Int64
}

func (d *DispatchObserver) Uncertain() bool { return d.taint.Load() || d.pending.Load() != 0 }

// Client clones a client and observes only its driver traffic, preserving its
// timeout, redirect, transport and response-byte semantics. Use the same observer
// for both child clients; bffURL identifies the forwarding origin.
func (d *DispatchObserver) Client(base *http.Client, bffURL string) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	rt := base.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	clone.Transport = &observingTransport{base: rt, dispatch: d, bff: strings.TrimRight(bffURL, "/")}
	return &clone
}

type observingTransport struct {
	base     http.RoundTripper
	dispatch *DispatchObserver
	bff      string
}

func (t *observingTransport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	// The known BFF readiness probe closes unread metadata by contract and
	// cannot forward clinical work. Other requests remain observed.
	if req.Method == http.MethodGet && t.bff != "" && req.URL.String() == t.bff+"/fhir/metadata" {
		return t.base.RoundTrip(req)
	}
	t.dispatch.pending.Add(1)
	handed := false
	defer func() {
		if !handed {
			t.dispatch.taint.Store(true)
			t.dispatch.pending.Add(-1)
		}
	}()
	resp, err = t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if t.bff != "" && strings.HasPrefix(req.URL.String(), t.bff+"/") && resp.StatusCode >= 400 {
		t.dispatch.taint.Store(true)
	}
	resp.Body = &observedBody{ReadCloser: resp.Body, dispatch: t.dispatch}
	handed = true
	return resp, nil
}

type observedBody struct {
	io.ReadCloser
	dispatch *DispatchObserver
	once     sync.Once
}

func (b *observedBody) finish(uncertain bool) {
	b.once.Do(func() {
		if uncertain {
			b.dispatch.taint.Store(true)
		}
		b.dispatch.pending.Add(-1)
	})
}
func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.finish(err != io.EOF)
	}
	return n, err
}
func (b *observedBody) Close() error { err := b.ReadCloser.Close(); b.finish(true); return err }
