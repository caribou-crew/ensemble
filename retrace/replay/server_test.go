package replay

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// serve stands the replay server up on a real loopback listener, so every
// assertion below travels through net/http — including the httpguard
// wrapper — rather than through a hand-called handler.
func serve(t *testing.T, b *Bundle, o Options, missesPath string) (*Server, string) {
	t.Helper()
	s := NewServer(b, o, missesPath)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func do(t *testing.T, method, url string, body string, headers map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAHitReplaysTheRecordedStatusHeadersAndBody(t *testing.T) {
	b := bundleOf(Exchange{
		Key:     Key{Method: "GET", Path: "/cart"},
		Status:  201,
		Headers: map[string]string{"Content-Type": "application/json", "X-Recorded": "yes"},
		Body:    `{"items":[{"sku":"a"}]}`,
		Seq:     1,
	})
	s, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/cart", "", nil)
	// 201, not 200: a status the server could have invented by accident.
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d, want the recorded 201", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Recorded"); got != "yes" {
		t.Fatalf("X-Recorded = %q, want the recorded header replayed", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want the recorded one", got)
	}
	if got := readBody(t, resp); got != `{"items":[{"sku":"a"}]}` {
		t.Fatalf("body = %q, want the recorded body byte-for-byte", got)
	}
	if s.MissCount() != 0 {
		t.Fatalf("MissCount = %d after a hit, want 0", s.MissCount())
	}
}

func TestAMissIs501WithAnExplanatoryJsonBodyAndAMissesJsonlLine(t *testing.T) {
	missesPath := filepath.Join(t.TempDir(), "misses.jsonl")
	b := bundleOf(
		exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1),
		exch("GET", "/orders", "", nil, 200, `{"orders":[]}`, 2),
	)
	s, url := serve(t, b, Options{}, missesPath)

	resp := do(t, "DELETE", url+"/order", "", nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 — an unmatched call must never be answered plausibly", resp.StatusCode)
	}
	body := readBody(t, resp)
	var doc struct {
		Error   string      `json:"error"`
		Method  string      `json:"method"`
		Path    string      `json:"path"`
		Nearest *Key        `json:"nearest"`
		Diff    []MissField `json:"diff"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("501 body is not JSON: %v\n%s", err, body)
	}
	if doc.Error == "" {
		t.Fatalf("501 body carries no explanation: %s", body)
	}
	if doc.Method != "DELETE" || doc.Path != "/order" {
		t.Fatalf("501 body = %s, want it to name the unmatched call", body)
	}
	if doc.Nearest == nil || doc.Nearest.Path != "/orders" {
		t.Fatalf("501 body names no nearest exchange: %s", body)
	}
	if len(doc.Diff) == 0 {
		t.Fatalf("501 body carries no field diff: %s", body)
	}

	if s.MissCount() != 1 {
		t.Fatalf("MissCount = %d, want 1", s.MissCount())
	}
	misses := s.Misses()
	if len(misses) != 1 || misses[0].Path != "/order" || misses[0].Method != "DELETE" {
		t.Fatalf("Misses() = %+v", misses)
	}
	if misses[0].Kind == "" {
		t.Fatal("Miss.Kind is empty — an unclassified miss reads as no miss at all")
	}
	if misses[0].TS.IsZero() {
		t.Fatal("Miss.TS is the zero time")
	}

	// ...and the same fact is durable, for a CI job that reads the run
	// directory rather than the process' stdout.
	f, err := os.Open(missesPath)
	if err != nil {
		t.Fatalf("misses.jsonl was not written: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			lines = append(lines, sc.Text())
		}
	}
	if len(lines) != 1 {
		t.Fatalf("misses.jsonl has %d lines, want 1:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var line Miss
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatalf("misses.jsonl line is not a Miss: %v\n%s", err, lines[0])
	}
	if line.Path != "/order" || line.Method != "DELETE" || len(line.Diff) == 0 {
		t.Fatalf("misses.jsonl line = %+v", line)
	}
}

func TestAMissIsNeverForwardedUpstream(t *testing.T) {
	// No upstream is even configured — Server has no field for one. This
	// stands a live server up anyway and proves nothing reaches it, so the
	// day somebody adds a "helpful" passthrough this goes red instead of
	// quietly turning every strict mock into a proxy.
	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Write([]byte(`{"from":"upstream"}`))
	}))
	defer upstream.Close()

	// The upstream is reachable and would answer this exact path.
	if resp := do(t, "GET", upstream.URL+"/nowhere", "", nil); resp.StatusCode != 200 {
		t.Fatalf("the upstream fixture is not answering: %d", resp.StatusCode)
	}
	before := upstreamHits.Load()

	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	// Passed through the environment the way a proxy would be configured,
	// so a fallthrough that read it would be exercised.
	t.Setenv("HTTP_PROXY", upstream.URL)
	t.Setenv("RETRACE_UPSTREAM", upstream.URL)
	s, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/nowhere", "", nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	if got := readBody(t, resp); strings.Contains(got, "upstream") {
		t.Fatalf("the miss body came from the upstream: %s", got)
	}
	if upstreamHits.Load() != before {
		t.Fatalf("upstream was hit %d time(s) by an unmatched replay request — a strict mock must never fall through", upstreamHits.Load()-before)
	}
	if s.MissCount() != 1 {
		t.Fatalf("MissCount = %d, want 1", s.MissCount())
	}
}

func TestCorsHeadersAreReflectedOnHitsAndMisses(t *testing.T) {
	// A browser consumer blocked by CORS never sees the loud 501 body — it
	// sees a network error, which reads as an app bug. Both arms are
	// asserted in one test on purpose: reflecting on hits alone is the
	// exact defect this guards against.
	//
	// The fixture CARRIES RECORDED CORS HEADERS, and that is the whole
	// point of it. A bundle with none is symmetric in the dimension under
	// test: recorded headers and reflected headers cannot be told apart,
	// so both arms pass whether writeHit defers to reflectCORS or clobbers
	// it. Every browser-driven capture — the primary consumer — records a
	// stack that serves CORS, so this fixture is also the realistic one:
	// the recorded origin is the PRODUCTION origin, and replaying it to a
	// browser on localhost blocks the response.
	const origin = "http://localhost:5173"
	b := bundleOf(Exchange{
		Key:    Key{Method: "GET", Path: "/cart"},
		Status: 200,
		Headers: map[string]string{
			"Content-Type":                     "application/json",
			"Access-Control-Allow-Origin":      "https://prod.example.com",
			"Access-Control-Allow-Credentials": "false",
			"Vary":                             "Accept-Encoding",
		},
		Body: `{"items":[]}`,
		Seq:  1,
	})
	_, url := serve(t, b, Options{}, "")

	for _, c := range []struct {
		name, path string
		want       int
	}{
		{"hit", "/cart", 200},
		{"miss", "/nowhere", http.StatusNotImplemented},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := do(t, "GET", url+c.path, "", map[string]string{"Origin": origin})
			defer resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.want)
			}
			got := resp.Header.Get("Access-Control-Allow-Origin")
			if got == "*" {
				t.Fatal("Access-Control-Allow-Origin is a bare * — a credentialed request fails against it")
			}
			if got != origin {
				t.Fatalf("Access-Control-Allow-Origin = %q, want the request's own Origin %q — the RECORDED origin must never win over the reflection",
					got, origin)
			}
			// Recorded "false" would make every credentialed fetch fail
			// with a CORS error the developer reads as an app bug.
			if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
				t.Fatalf("Access-Control-Allow-Credentials = %q, want \"true\"", got)
			}
			// The answer varies by the request's Origin, so a cache that
			// stored one origin's answer for another is a browser-visible
			// bug; the recorded Vary describes the recording's negotiation,
			// not this one.
			if got := resp.Header.Get("Vary"); !strings.Contains(got, "Origin") {
				t.Fatalf("Vary = %q, want it to name Origin", got)
			}
		})
	}
}

func TestARecordedCorsHeaderIsNeverReplayedToANonBrowserClientEither(t *testing.T) {
	// The other arm of the same mechanism: no Origin on the request, so
	// reflectCORS emits nothing at all. A recorded bare "*" must still not
	// reach the client — reflectCORS's doc comment says this server never
	// emits one, and a guarantee stated in a comment that the code does
	// not make is worse than no comment.
	b := bundleOf(Exchange{
		Key:     Key{Method: "GET", Path: "/cart"},
		Status:  200,
		Headers: map[string]string{"Access-Control-Allow-Origin": "*", "X-Recorded": "yes"},
		Body:    `{"items":[]}`,
		Seq:     1,
	})
	_, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/cart", "", nil)
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q on a request with no Origin, want none — the recorded bare * was replayed", got)
	}
	// The mirror: ordinary recorded headers are still replayed, so the
	// skip above is a CORS skip and not a header-dropping regression.
	if got := resp.Header.Get("X-Recorded"); got != "yes" {
		t.Fatalf("X-Recorded = %q, want the recorded header still replayed", got)
	}
}

func TestRecordedConnectionHeadersAreNotReplayedOntoThisConnection(t *testing.T) {
	// Content-Length, Transfer-Encoding and Connection describe the
	// ORIGINAL connection. Replayed onto this one they are wrong at best
	// (a length that does not match the body written) and fatal at worst.
	// The code has always been right here and nothing held it there.
	b := bundleOf(Exchange{
		Key:    Key{Method: "GET", Path: "/cart"},
		Status: 200,
		Headers: map[string]string{
			"Content-Type":      "application/json",
			"Content-Length":    "99999",
			"Transfer-Encoding": "chunked",
			"Connection":        "close",
			"X-Recorded":        "yes",
		},
		Body: `{"items":[]}`,
		Seq:  1,
	})
	_, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/cart", "", nil)
	body := readBody(t, resp)
	if body != `{"items":[]}` {
		t.Fatalf("body = %q, want the recorded body — a replayed Content-Length truncated or hung it", body)
	}
	if got := resp.Header.Get("Content-Length"); got == "99999" {
		t.Fatalf("Content-Length = %q, want net/http's own length for the body actually written", got)
	}
	if got := resp.Header.Get("Transfer-Encoding"); got != "" {
		t.Fatalf("Transfer-Encoding = %q, want none — it belongs to the recorded connection", got)
	}
	if got := resp.Header.Get("Connection"); got == "close" {
		t.Fatal("Connection: close was replayed onto this connection")
	}
	if got := resp.Header.Get("X-Recorded"); got != "yes" {
		t.Fatalf("X-Recorded = %q, want the recorded header replayed", got)
	}
}

func TestServedCountAndUnusedExchangesDistinguishNothingAskedFromEverythingMatched(t *testing.T) {
	// The two worlds a miss count cannot tell apart: "every call matched"
	// and "the client never called anything" both report zero misses. A
	// server that answered nothing must be able to SAY so, or `retrace
	// replay` prints "every call matched the recording" over a run in
	// which nothing was compared.
	b := bundleOf(
		exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1),
		exch("GET", "/orders", "", nil, 200, `{"orders":[]}`, 2),
	)
	s, url := serve(t, b, Options{}, "")

	if s.ServedCount() != 0 {
		t.Fatalf("ServedCount = %d before any request, want 0", s.ServedCount())
	}
	if got := s.UnusedExchanges(); len(got) != 2 {
		t.Fatalf("UnusedExchanges = %+v, want both recorded exchanges", got)
	}

	// A preflight and a guard rejection are neither hits nor misses, so
	// neither may inflate the served count into a false "something was
	// compared".
	do(t, "OPTIONS", url+"/cart", "", map[string]string{"Origin": "http://localhost:5173"}).Body.Close()
	do(t, "GET", url+"/cart", "", map[string]string{"Sec-Fetch-Site": "cross-site"}).Body.Close()
	if s.ServedCount() != 0 {
		t.Fatalf("ServedCount = %d after a preflight and a guard rejection, want 0 — neither is an exchange the recording answered", s.ServedCount())
	}

	// A miss is not a served exchange either.
	do(t, "GET", url+"/nowhere", "", nil).Body.Close()
	if s.ServedCount() != 0 {
		t.Fatalf("ServedCount = %d after a miss, want 0", s.ServedCount())
	}

	do(t, "GET", url+"/cart", "", nil).Body.Close()
	if s.ServedCount() != 1 {
		t.Fatalf("ServedCount = %d after one hit, want 1", s.ServedCount())
	}
	unused := s.UnusedExchanges()
	if len(unused) != 1 || unused[0].Path != "/orders" {
		t.Fatalf("UnusedExchanges = %+v, want only the never-called /orders", unused)
	}

	do(t, "GET", url+"/orders", "", nil).Body.Close()
	if s.ServedCount() != 2 {
		t.Fatalf("ServedCount = %d after two hits, want 2", s.ServedCount())
	}
	if got := s.UnusedExchanges(); len(got) != 0 {
		t.Fatalf("UnusedExchanges = %+v after every exchange was served, want none", got)
	}
}

func TestAnOpaqueRecordedRequestBodyIsMatchedByItsBytesOverHttp(t *testing.T) {
	// The wiring arm of the matcher's F3 pin: the handler must hand the
	// RAW bytes to Match, not only the decoded body. A handler that passed
	// only the decode would make every form post match every other one,
	// and match_test.go alone would not notice.
	b := bundleOf(Exchange{
		Key:    Key{Method: "POST", Path: "/login"},
		ReqRaw: "user=ada&pass=hunter2",
		Status: 200, Body: `{"ok":true}`, Seq: 1,
	})
	s, url := serve(t, b, Options{}, "")

	resp := do(t, "POST", url+"/login", "user=eve&pass=letmein",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 — a different form body matched a recorded one", resp.StatusCode)
	}
	if s.MissCount() != 1 {
		t.Fatalf("MissCount = %d, want 1", s.MissCount())
	}

	// The mirror: the recorded bytes still match themselves, so the
	// refusal above is not simply "no form post ever matches".
	ok := do(t, "POST", url+"/login", "user=ada&pass=hunter2",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if got := readBody(t, ok); got != `{"ok":true}` {
		t.Fatalf("body = %q, want the recorded response for the identical form body", got)
	}
}

func TestOptionsPreflightIsAnsweredNotMissed(t *testing.T) {
	const origin = "http://localhost:5173"
	b := bundleOf(exch("POST", "/orders", "", nil, 201, `{"id":1}`, 1))
	s, url := serve(t, b, Options{}, "")

	// A path the bundle DOES record and a path it does not: a preflight is
	// not a recorded exchange either way, so neither may be a miss.
	for _, path := range []string{"/orders", "/never-recorded"} {
		resp := do(t, "OPTIONS", url+path, "", map[string]string{
			"Origin":                         origin,
			"Access-Control-Request-Method":  "POST",
			"Access-Control-Request-Headers": "content-type,x-trace",
		})
		if resp.StatusCode >= 400 {
			t.Fatalf("OPTIONS %s = %d, want a preflight answer", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("preflight Allow-Origin = %q, want %q", got, origin)
		}
		if got := resp.Header.Get("Access-Control-Allow-Methods"); got == "" {
			t.Fatal("preflight carries no Access-Control-Allow-Methods")
		}
		if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "content-type,x-trace" {
			t.Fatalf("preflight Allow-Headers = %q, want the requested headers echoed", got)
		}
		resp.Body.Close()
	}
	if s.MissCount() != 0 {
		t.Fatalf("MissCount = %d after preflights, want 0 — a preflight is not a client deviation", s.MissCount())
	}
}

func TestAFreshServerReportsNoMissesAndAnEmptyList(t *testing.T) {
	// The zero-value pin from the other direction: MissCount must count
	// what happened, never default to a reassuring 0 that a broken
	// recording path would also produce. Paired with the miss test above,
	// which is what makes 0-here meaningful.
	s, _ := serve(t, bundleOf(exch("GET", "/cart", "", nil, 200, `{}`, 1)), Options{}, "")
	if s.MissCount() != 0 || len(s.Misses()) != 0 {
		t.Fatalf("a fresh server reports %d misses", s.MissCount())
	}
}

func TestARequestBodyDecidesWhichRecordedResponseIsServedOverHttp(t *testing.T) {
	// Proves the handler actually reads and decodes the request body
	// before matching — a handler that ignored it would answer both calls
	// from the first exchange and this file's other tests would not notice.
	b := bundleOf(
		exch("POST", "/orders", "", map[string]any{"sku": "a"}, 201, `{"id":"a"}`, 1),
		exch("POST", "/orders", "", map[string]any{"sku": "b"}, 201, `{"id":"b"}`, 2),
	)
	_, url := serve(t, b, Options{}, "")

	resp := do(t, "POST", url+"/orders", `{"sku":"b"}`, map[string]string{"Content-Type": "application/json"})
	if got := readBody(t, resp); got != `{"id":"b"}` {
		t.Fatalf("body = %q, want the sku=b exchange", got)
	}
	resp = do(t, "POST", url+"/orders", `{"sku":"a"}`, map[string]string{"Content-Type": "application/json"})
	if got := readBody(t, resp); got != `{"id":"a"}` {
		t.Fatalf("body = %q, want the sku=a exchange", got)
	}
}

func TestARecordedHopWithNoStatusIsNeverReplayedAsSuccess(t *testing.T) {
	// A recorded hop whose Status is 0 is one whose upstream never
	// answered. Replaying it as 200 would invent a success the recording
	// never saw — the zero-value trap, one HTTP status wide.
	b := bundleOf(Exchange{Key: Key{Method: "GET", Path: "/cart"}, Status: 0, Body: "", Seq: 1})
	_, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/cart", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("an exchange with no recorded status replayed as 200 — a success the recording never observed")
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestACrossSiteBrowserRequestIsRefusedByTheGuardAndIsNotAMiss(t *testing.T) {
	// The replay server sits behind core/httpguard like every other
	// loopback listener in this repo — not behind an inlined copy of part
	// of it. And a request the guard turned away never reached the
	// exchange table, so it must not inflate the miss count: exit 2 has to
	// mean "the client deviated", not "a browser tab poked the port".
	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	s, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/cart", "", map[string]string{"Sec-Fetch-Site": "cross-site"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — the guard is not wired in", resp.StatusCode)
	}
	if s.MissCount() != 0 {
		t.Fatalf("MissCount = %d, want 0 — a guard rejection is not a client deviation", s.MissCount())
	}
}
