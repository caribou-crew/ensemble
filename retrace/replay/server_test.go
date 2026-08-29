package replay

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
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

// prodServer stands up a server that must NEVER be reached, and reports
// whether it was. It answers plausibly on purpose: a test whose "escape"
// target refuses connections would pass against a replay server that let
// the client leave and merely failed afterwards.
func prodServer(t *testing.T) (*httptest.Server, func() int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"from":"production"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() int64 { return hits.Load() }
}

func TestARecordedRedirectSendsTheClientBackIntoTheReplayServerAndNeverOut(t *testing.T) {
	// THE CENTRAL INVARIANT. This package has no upstream, no passthrough
	// and no way for a call to reach a live system — and none of that
	// matters if a recorded `302 Location: https://prod.example.com/next`
	// is replayed verbatim, because every browser and every default HTTP
	// client follows it and issues the next request against the RECORDED
	// host. A supposedly hermetic CI job then reads production, and
	// mutates it for any redirect the app follows with a mutating method.
	// It is silent, it lands on a hit, and the miss machinery never sees
	// it because the follow-up request never arrives here.
	//
	// So this test does not assert that a header string changed — a
	// rewrite pointing anywhere at all would satisfy that. It follows the
	// redirect with a real client and asserts where the follow-up LANDED.
	prod, prodHits := prodServer(t)

	t.Run("a recorded next exchange replays end to end", func(t *testing.T) {
		b := bundleOf(
			Exchange{
				Key: Key{Method: "GET", Path: "/login"}, Status: 302,
				Headers: map[string]string{
					"location":   prod.URL + "/dashboard?tab=1#frag",
					"x-recorded": "yes",
				},
				Seq: 1,
			},
			exch("GET", "/dashboard", "tab=1", nil, 200, `{"from":"the recording"}`, 2),
		)
		s, url := serve(t, b, Options{}, "")
		before := prodHits()

		resp := do(t, "GET", url+"/login", "", nil) // http.DefaultClient follows redirects
		body := readBody(t, resp)
		if prodHits() != before {
			t.Fatalf("the client left the replay server and hit the recorded host %d time(s) — the strict mock's whole premise is that no call can reach a live system", prodHits()-before)
		}
		if strings.Contains(body, "production") {
			t.Fatalf("the response came from the recorded host: %s", body)
		}
		if body != `{"from":"the recording"}` {
			t.Fatalf("body = %q, want the recorded /dashboard exchange — the redirect must land back in the bundle", body)
		}
		if s.MissCount() != 0 {
			t.Fatalf("MissCount = %d, want 0 — both exchanges are recorded", s.MissCount())
		}
		if s.ServedCount() != 2 {
			t.Fatalf("ServedCount = %d, want 2 — the recorded flow should replay end to end", s.ServedCount())
		}
	})

	t.Run("an unrecorded next exchange is a loud miss, not a real request", func(t *testing.T) {
		// The other arm, and the one that says what strict replay means:
		// "we did not record this" is the answer, and it must be loud.
		b := bundleOf(Exchange{
			Key: Key{Method: "GET", Path: "/login"}, Status: 302,
			Headers: map[string]string{"location": prod.URL + "/never-recorded"},
			Seq:     1,
		})
		s, url := serve(t, b, Options{}, "")
		before := prodHits()

		resp := do(t, "GET", url+"/login", "", nil)
		body := readBody(t, resp)
		if prodHits() != before {
			t.Fatalf("the client reached the recorded host %d time(s)", prodHits()-before)
		}
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("status = %d, want 501 — an unrecorded redirect target must be a miss", resp.StatusCode)
		}
		if !strings.Contains(body, "/never-recorded") {
			t.Fatalf("the 501 body does not name the unmatched follow-up: %s", body)
		}
		if s.MissCount() != 1 {
			t.Fatalf("MissCount = %d, want 1", s.MissCount())
		}
	})

	t.Run("the host named makes no difference", func(t *testing.T) {
		// Not only hosts that look like production, and not only absolute
		// URLs: a protocol-relative "//host/path" carries an authority
		// with no scheme and escapes exactly the same way.
		for _, loc := range []string{
			"https://accounts.example.com/sso?next=%2Fcart",
			"//accounts.example.com/sso",
			"http://user:secret@accounts.example.com/sso",
		} {
			b := bundleOf(Exchange{
				Key: Key{Method: "GET", Path: "/login"}, Status: 302,
				Headers: map[string]string{"location": loc}, Seq: 1,
			})
			_, url := serve(t, b, Options{}, "")
			resp := noRedirect(t, url+"/login")
			got := resp.Header.Get("Location")
			resp.Body.Close()
			u, err := neturl.Parse(got)
			if err != nil {
				t.Fatalf("Location %q is not parseable: %v", got, err)
			}
			if strings.Contains(u.Host, "accounts.example.com") {
				t.Fatalf("Location = %q, want the replay listener's own authority — a third-party host the recording never captured must become a miss, not a real request", got)
			}
			if u.User != nil {
				t.Fatalf("Location = %q still carries userinfo minted for another host", got)
			}
		}
	})

	t.Run("status, path, query, fragment and other headers survive", func(t *testing.T) {
		// The rewrite changes where the client goes, not what the
		// recording said.
		b := bundleOf(Exchange{
			Key: Key{Method: "GET", Path: "/login"}, Status: 307,
			Headers: map[string]string{
				"location":   prod.URL + "/dashboard?tab=1&q=a%20b#frag",
				"x-recorded": "yes",
			},
			Seq: 1,
		})
		_, url := serve(t, b, Options{}, "")
		resp := noRedirect(t, url+"/login")
		defer resp.Body.Close()
		if resp.StatusCode != 307 {
			t.Fatalf("status = %d, want the recorded 307", resp.StatusCode)
		}
		if got := resp.Header.Get("X-Recorded"); got != "yes" {
			t.Fatalf("X-Recorded = %q, want the recorded header untouched", got)
		}
		u, err := neturl.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if u.Path != "/dashboard" || u.RawQuery != "tab=1&q=a%20b" || u.Fragment != "frag" {
			t.Fatalf("Location = %q, want path/query/fragment preserved exactly", resp.Header.Get("Location"))
		}
		if u.Scheme != "http" {
			t.Fatalf("Location scheme = %q, want the scheme this listener actually speaks", u.Scheme)
		}
	})

	t.Run("a relative Location is left byte-identical", func(t *testing.T) {
		// The over-refusal mirror. A relative Location already resolves
		// against our own host, so it is safe — rewriting it is only a
		// chance to mangle it, and mangling it breaks flows that were
		// never in danger.
		for _, loc := range []string{"/dashboard?tab=1", "dashboard?tab=1", "http:dashboard?tab=1"} {
			b := bundleOf(
				Exchange{
					Key: Key{Method: "GET", Path: "/login"}, Status: 302,
					Headers: map[string]string{"location": loc}, Seq: 1,
				},
				exch("GET", "/dashboard", "tab=1", nil, 200, `{"from":"the recording"}`, 2),
			)
			_, url := serve(t, b, Options{}, "")
			resp := noRedirect(t, url+"/login")
			got := resp.Header.Get("Location")
			resp.Body.Close()
			if got != loc {
				t.Fatalf("Location = %q, want the recorded %q unchanged", got, loc)
			}
		}
		// And it still works end to end through a following client.
		b := bundleOf(
			Exchange{
				Key: Key{Method: "GET", Path: "/login"}, Status: 302,
				Headers: map[string]string{"location": "/dashboard?tab=1"}, Seq: 1,
			},
			exch("GET", "/dashboard", "tab=1", nil, 200, `{"from":"the recording"}`, 2),
		)
		_, url := serve(t, b, Options{}, "")
		if got := readBody(t, do(t, "GET", url+"/login", "", nil)); got != `{"from":"the recording"}` {
			t.Fatalf("body = %q, want the recorded /dashboard exchange", got)
		}
	})
}

// noRedirect fetches without following redirects, so the 3xx itself can be
// inspected.
func noRedirect(t *testing.T, url string) *http.Response {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func TestARecordedCookieDomainIsStrippedSoTheSessionActuallyEstablishes(t *testing.T) {
	// A recorded `Domain=prod.example.com` can never match a loopback
	// listener, so the browser drops the cookie: the session never
	// establishes and the test fails somewhere later wearing an app bug's
	// clothing — the same misattribution reflectCORS exists to prevent.
	b := bundleOf(Exchange{
		Key: Key{Method: "POST", Path: "/login"}, Status: 200,
		Headers: map[string]string{
			"set-cookie": "sid=abc123; Domain=prod.example.com; Path=/; Max-Age=3600; HttpOnly; Secure; SameSite=Lax",
		},
		Body: `{"ok":true}`, Seq: 1,
	})
	_, url := serve(t, b, Options{}, "")

	resp := do(t, "POST", url+"/login", "", nil)
	defer resp.Body.Close()
	raw := resp.Header.Get("Set-Cookie")
	if strings.Contains(strings.ToLower(raw), "domain=") {
		t.Fatalf("Set-Cookie = %q, still carries the recorded Domain — a browser drops that cookie on a loopback listener and the session never establishes", raw)
	}
	// Everything else is the recording's, untouched. Secure included: it
	// is not stripped, because loopback is a secure context in every
	// browser that matters and removing it would WEAKEN what was recorded
	// rather than re-address it.
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %+v, want exactly one", cookies)
	}
	c := cookies[0]
	if c.Name != "sid" || c.Value != "abc123" {
		t.Fatalf("cookie = %+v, want the recorded sid=abc123", c)
	}
	if c.Domain != "" {
		t.Fatalf("cookie Domain = %q, want none", c.Domain)
	}
	if c.Path != "/" || c.MaxAge != 3600 || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie = %+v, want every other recorded attribute preserved (Path, Max-Age, HttpOnly, Secure, SameSite)", c)
	}

	// The mirror: a recorded cookie with no Domain is passed through
	// byte-identically, so the strip is a Domain strip and not a rewrite
	// of every cookie.
	const plain = "sid=abc123; Path=/; HttpOnly"
	b2 := bundleOf(Exchange{
		Key: Key{Method: "POST", Path: "/login"}, Status: 200,
		Headers: map[string]string{"set-cookie": plain},
		Body:    `{"ok":true}`, Seq: 1,
	})
	_, url2 := serve(t, b2, Options{}, "")
	resp2 := do(t, "POST", url2+"/login", "", nil)
	defer resp2.Body.Close()
	if got := resp2.Header.Get("Set-Cookie"); got != plain {
		t.Fatalf("Set-Cookie = %q, want the recorded %q unchanged", got, plain)
	}
}

func TestATargetFilteredServerNeverServesAnotherListenersExchangeOverHttp(t *testing.T) {
	edge := exch("GET", "/cart", "", nil, 200, `{"from":"edge"}`, 1)
	edge.Target = "edge"
	auth := exch("GET", "/cart", "", nil, 200, `{"from":"auth"}`, 2)
	auth.Target = "auth"
	b := bundleOf(edge, auth)

	edgeServer, edgeURL := serve(t, b, Options{TargetFilter: "edge"}, "")
	resp := do(t, "GET", edgeURL+"/cart", "", nil)
	if got := readBody(t, resp); got != `{"from":"edge"}` {
		t.Fatalf("body = %q, want the edge-target exchange only", got)
	}

	authServer, authURL := serve(t, b, Options{TargetFilter: "auth"}, "")
	resp2 := do(t, "GET", authURL+"/cart", "", nil)
	if got := readBody(t, resp2); got != `{"from":"auth"}` {
		t.Fatalf("body = %q, want the auth-target exchange only", got)
	}

	if edgeServer.MissCount() != 0 || authServer.MissCount() != 0 {
		t.Fatalf("MissCount = %d,%d, want both servers to have matched their own target's exchange",
			edgeServer.MissCount(), authServer.MissCount())
	}
	if got := edgeServer.UnusedExchanges(); len(got) != 0 {
		t.Fatalf("edge server UnusedExchanges = %+v, want none — it must not count the auth exchange it can never serve", got)
	}
}

func TestATargetFilteredServerMissesARequestOnlyAnotherListenerRecorded(t *testing.T) {
	auth := exch("GET", "/only-auth-has-this", "", nil, 200, `{}`, 1)
	auth.Target = "auth"
	b := bundleOf(auth)

	s, url := serve(t, b, Options{TargetFilter: "edge"}, "")
	resp := do(t, "GET", url+"/only-auth-has-this", "", nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 — this exchange belongs to a different listener and must never be served here", resp.StatusCode)
	}
	if s.MissCount() != 1 {
		t.Fatalf("MissCount = %d, want 1", s.MissCount())
	}
}

func TestAssertRequestsRecordsTheRealRequestNotTheRecordedOne(t *testing.T) {
	// The recorded exchange carries no ReqHeaders and no ReqBody at all, so
	// any header or body ObservedHops() reports can only have come from the
	// actual incoming request — this is the whole point of the flag.
	b := bundleOf(exch("POST", "/cart", "", nil, 201, `{"ok":true}`, 1))
	s, url := serve(t, b, Options{AssertRequests: true}, "")

	resp := do(t, "POST", url+"/cart", `{"qty":2}`, map[string]string{"X-Client": "ios"})
	if got := readBody(t, resp); got != `{"ok":true}` {
		t.Fatalf("body = %q, want the recorded response replayed as usual", got)
	}

	hops := s.ObservedHops()
	if len(hops) != 1 {
		t.Fatalf("ObservedHops() = %+v, want exactly one hop for the one hit", hops)
	}
	h := hops[0]
	if h.Method != "POST" || h.Path != "/cart" {
		t.Fatalf("observed hop method/path = %s %s, want POST /cart", h.Method, h.Path)
	}
	if h.Req.Body != `{"qty":2}` {
		t.Fatalf("observed hop request body = %q, want the client's own body, not the recorded (empty) one", h.Req.Body)
	}
	if got := h.Req.Headers["x-client"]; got != "ios" {
		t.Fatalf("observed hop request headers = %+v, want the client's own X-Client header", h.Req.Headers)
	}
	// The response side mirrors the RECORDING, not what the wire actually
	// carried post-rewrite — see the design doc's Decision 3.
	if h.Status != 201 || h.Resp.Body != `{"ok":true}` {
		t.Fatalf("observed hop response = %d %q, want the matched exchange's own recorded status and body", h.Status, h.Resp.Body)
	}
}

func TestAssertRequestsRecordsNothingForAMiss(t *testing.T) {
	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	s, url := serve(t, b, Options{AssertRequests: true}, "")

	do(t, "GET", url+"/nowhere", "", nil).Body.Close()
	if s.MissCount() != 1 {
		t.Fatalf("MissCount = %d, want 1", s.MissCount())
	}
	if hops := s.ObservedHops(); len(hops) != 0 {
		t.Fatalf("ObservedHops() = %+v after a miss, want none — a miss is already reported once, through Misses()", hops)
	}
}

func TestAssertRequestsOffRecordsNothing(t *testing.T) {
	// The default: AssertRequests is false unless a caller opts in, so a
	// plain `retrace replay` pays nothing for this feature existing.
	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	s, url := serve(t, b, Options{}, "")

	do(t, "GET", url+"/cart", "", nil).Body.Close()
	if hops := s.ObservedHops(); len(hops) != 0 {
		t.Fatalf("ObservedHops() = %+v with AssertRequests unset, want none", hops)
	}
}
