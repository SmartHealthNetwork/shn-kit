package kitd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-kit/internal/testrecord"
)

// recordedDataServer answers FreshenPersonas with what a real multitenant HAPI
// data server answered it (testdata/recordings/freshen-provider.json: the
// warm-up, 14 transaction bundles and the seed-complete marker).
//
// It is hand-written, not the strict replay, for one reason: the seeder stamps
// every Observation's effectiveDateTime in a transaction bundle with the
// current time, so four recorded bundles differ from what is sent on any later
// day in exactly that field. The server blanks that one field on both sides,
// after checking that each sent value is an RFC 3339 time inside the test's
// run, and then matches as the strict replay does: method, path, no query,
// Content-Type and the whole JSON body. The warm-up and the marker carry no
// clock and are matched unchanged. Each request gets its recorded status,
// Content-Type and body; a transaction answer also gets the Location header the
// server sent, the answer Bundle's absolute URL, built from this server's own
// address. A request the recording does not hold, or one asked more often than
// the pass allows, fails the test and is answered 599, as the strict replay's
// miss is.
//
// The recording is one freshen against a server that held no data, so a pass
// asks each recorded exchange exactly once. A test that freshens more than
// once says how many passes it makes and calls nextPass between them; later
// passes get the same answers, where
// the real server would report updates (200 OK entries, a 200 for the marker)
// instead of creates, and the seeder reads only the status class. A request
// asked a second time within a pass gets no answer. At each pass's end every
// exchange must have been asked for exactly once in it, unless the test calls
// partialPass and says why.
type recordedDataServer struct {
	t         *testing.T
	exchanges []testrecord.Exchange
	passes    int
	started   time.Time

	mu      sync.Mutex
	pass    int   // the pass being served, from 1
	served  []int // times each exchange was asked for, over all passes so far
	partial bool
}

// newRecordedDataServer serves the recording for passes freshens.
func newRecordedDataServer(t *testing.T, passes int) *recordedDataServer {
	t.Helper()
	path := filepath.Join("testdata", "recordings", "freshen-provider.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := testrecord.Parse(path, raw)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	d := &recordedDataServer{
		t:         t,
		exchanges: rec.Exchanges,
		passes:    passes,
		// RFC 3339 as stamped has whole seconds; a stamp in the test's first
		// second is truncated below its start.
		started: time.Now().UTC().Truncate(time.Second),
		pass:    1,
		served:  make([]int, len(rec.Exchanges)),
	}
	t.Cleanup(d.checkServed)
	return d
}

// partialPass lets a test leave recorded exchanges unasked, for a freshen a
// fault stops early; say why at the call site. Repeats stay bounded.
func (d *recordedDataServer) partialPass() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.partial = true
}

// withoutClock is body with every Observation entry's effectiveDateTime
// blanked, and the values it held.
func withoutClock(body []byte) ([]byte, []string, error) {
	var bundle map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&bundle); err != nil {
		return nil, nil, err
	}
	var stamps []string
	entries, _ := bundle["entry"].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		if res == nil || res["resourceType"] != "Observation" {
			continue
		}
		if v, ok := res["effectiveDateTime"]; ok {
			s, _ := v.(string)
			stamps = append(stamps, s)
			res["effectiveDateTime"] = ""
		}
	}
	out, err := json.Marshal(bundle)
	return out, stamps, err
}

func sameJSON(a, b []byte) bool {
	decode := func(raw []byte) (any, bool) {
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			return nil, false
		}
		_, err := dec.Token()
		return v, err == io.EOF
	}
	x, okA := decode(a)
	y, okB := decode(b)
	return okA && okB && reflect.DeepEqual(x, y)
}

func isTransaction(req testrecord.Request) bool {
	return req.Method == http.MethodPost && req.Path == "/fhir/provider"
}

// match returns the recorded exchanges r matches, in recorded order.
func (d *recordedDataServer) match(r *http.Request, body []byte) []int {
	sent, stamps := body, []string(nil)
	if r.Method == http.MethodPost && r.URL.Path == "/fhir/provider" {
		var err error
		if sent, stamps, err = withoutClock(body); err != nil {
			d.t.Errorf("recordedDataServer: transaction body is not JSON: %v", err)
			return nil
		}
		now := time.Now().UTC()
		for _, s := range stamps {
			at, err := time.Parse(time.RFC3339, s)
			if err != nil || at.Before(d.started) || at.After(now) {
				d.t.Errorf("recordedDataServer: Observation effectiveDateTime %q is not an RFC 3339 time inside the test's run (%s to %s)", s, d.started.Format(time.RFC3339), now.Format(time.RFC3339))
				return nil
			}
		}
	}
	var hits []int
	for i, ex := range d.exchanges {
		req := ex.Request
		if req.Method != r.Method || req.Path != r.URL.Path || r.URL.RawQuery != "" ||
			len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != req.Headers["Content-Type"] {
			continue
		}
		recorded := []byte(req.Body)
		if isTransaction(req) {
			var err error
			if recorded, _, err = withoutClock(req.Body); err != nil {
				d.t.Errorf("recorded transaction %d is not JSON: %v", i, err)
				return nil
			}
		}
		if sameJSON(recorded, sent) {
			hits = append(hits, i)
		}
	}
	return hits
}

func (d *recordedDataServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	hits := d.match(r, body)
	i := -1
	d.mu.Lock()
	for _, h := range hits {
		if d.served[h] < d.pass {
			i = h
			d.served[h]++
			break
		}
	}
	d.mu.Unlock()
	if i < 0 {
		if len(hits) > 0 {
			d.t.Errorf("recordedDataServer: %s %s asked again within pass %d", r.Method, r.URL.Path, d.pass)
		} else {
			d.t.Errorf("recordedDataServer: no recorded answer for %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "recordedDataServer: no recorded answer", 599)
		return
	}
	ex := d.exchanges[i]
	w.Header().Set("Content-Type", ex.Response.Headers["Content-Type"])
	if isTransaction(ex.Request) {
		var answer struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(ex.Response.Body, &answer); err != nil || answer.ID == "" {
			d.t.Errorf("recorded transaction answer %d has no Bundle id: %v", i, err)
		}
		w.Header().Set("Location", "http://"+r.Host+"/fhir/provider/Bundle/"+answer.ID)
	}
	w.WriteHeader(ex.Response.Status)
	_, _ = w.Write(ex.Response.Body)
}

// nextPass ends a pass: every recorded exchange must have been asked for
// exactly once in it.
func (d *recordedDataServer) nextPass() {
	d.t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.checkPassLocked()
	d.pass++
}

// checkServed ends the test: its last pass is checked as nextPass checks one,
// and the test must have made the passes it declared.
func (d *recordedDataServer) checkServed() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pass != d.passes {
		d.t.Errorf("recordedDataServer: the test made %d pass(es), declared %d", d.pass, d.passes)
	}
	d.checkPassLocked()
}

func (d *recordedDataServer) checkPassLocked() {
	if d.partial {
		return
	}
	for i, n := range d.served {
		if n != d.pass {
			req := d.exchanges[i].Request
			d.t.Errorf("recorded exchange %d (%s %s) was asked for %d time(s) by the end of pass %d, want once per pass", i, req.Method, req.Path, n, d.pass)
		}
	}
}
