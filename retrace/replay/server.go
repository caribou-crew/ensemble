package replay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/caribou-crew/ensemble/core/httpguard"
)

// ABSENCE IS NEVER AGREEMENT.
//
// This server answers a test's HTTP calls from a recorded bundle. Its
// entire value is that a call the bundle does not contain is reported
// LOUDLY: an unmatched request must never fall through to a passthrough, a
// 200, an empty body, or the nearest recorded exchange. It is a 501
// carrying a field-level explanation, a misses.jsonl line, and a non-zero
// final exit from `retrace replay`. That is the whole of "client
// deviation detected in CI" — a mock that answered plausibly would let a
// test pass while proving nothing, which is worse than no test at all.
//
// There is deliberately no ServeMux here. A mock matches against its own
// exchange table, so route patterns would add nothing but the ServeMux
// traps in global-constraints.md (a method-less pattern panicking against
// a "GET /" sibling; a subtree-redirect 301 dropping a POST body). One
// http.HandlerFunc answers every method and every path itself, including
// the two cases a mux would otherwise have covered:
//
//   - OPTIONS is answered BEFORE the table lookup: a preflight is not a
//     recorded exchange and must never be a miss.
//   - every other verb falls through to Match, and a miss is a 501.

// MissUnmatched is the only miss kind there is today, and it is set on
// every Miss this package produces. Kind is never left empty: an
// unclassified miss in misses.jsonl reads to a consumer grouping by kind
// as no miss at all.
const MissUnmatched = "unmatched"

// maxRequestBody caps what the handler reads off one request before
// matching. Matching is structural over decoded JSON; a body larger than
// this is not a body a recorded fixture is going to match anyway, and an
// unbounded read is a memory hazard on a listener a test command can
// drive.
const maxRequestBody = 8 << 20

// Miss is one request the bundle could not answer, as it goes into
// misses.jsonl and into the CLI's report.
type Miss struct {
	TS      time.Time   `json:"ts"`
	Kind    string      `json:"kind"`
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Query   string      `json:"query,omitempty"`
	Diff    []MissField `json:"diff,omitempty"`
	Nearest *Key        `json:"nearest,omitempty"`
}

// Server answers from a Bundle and remembers every miss.
type Server struct {
	http.Handler

	mu         sync.Mutex
	bundle     *Bundle
	opts       Options
	missesPath string
	misses     []Miss
	served     int
	logErr     error
	now        func() time.Time
}

// NewServer builds the replay handler. missesPath comes from
// runs.Paths.MissesPath at the call site that knows the run directory —
// the misses file has ONE name and ONE owner, so Options carries no second
// field naming it. An empty string means "record misses in memory only",
// which is what the unit tests use; it never means "do not record".
func NewServer(b *Bundle, o Options, missesPath string) *Server {
	s := &Server{bundle: b, opts: o, missesPath: missesPath, now: time.Now}
	// The SAME guard every other loopback listener in this repo sits
	// behind (core/httpguard), never an inlined copy of part of it. nil
	// allowed-hosts = answer only as loopback.
	s.Handler = httpguard.Handler(nil, http.HandlerFunc(s.serve))
	return s
}

// MissCount is the number of unmatched requests seen. `retrace replay`
// exits 2 when it is non-zero.
func (s *Server) MissCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.misses)
}

// ServedCount is the number of requests this server ANSWERED FROM THE
// BUNDLE. It exists because "no misses" and "the recording was honoured"
// are two different facts, and a replay in which the client never called
// anything satisfies the first while proving nothing about the second: an
// app with a hard-coded base URL that ignores RETRACE_PROXY_URL, a runner
// that skipped its suite, a `--` command that exited early. Zero served is
// "nothing was compared", which `retrace replay` reports as could-not-
// evaluate rather than as a clean pass.
//
// Preflights and guard rejections are deliberately NOT counted: neither is
// an exchange the recording answered.
func (s *Server) ServedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served
}

// UnusedExchanges names every recorded exchange no request ever matched,
// in recorded order. A replay that served two of nine recorded calls is a
// materially different event from one that served all nine, and the
// difference is invisible in a miss count.
func (s *Server) UnusedExchanges() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Key
	for i := range s.bundle.Exchanges {
		if s.bundle.Exchanges[i].used == 0 {
			out = append(out, s.bundle.Exchanges[i].Key)
		}
	}
	return out
}

// Misses returns a copy of every miss recorded so far.
func (s *Server) Misses() []Miss {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Miss(nil), s.misses...)
}

// MissLogErr reports the first failure to append to misses.jsonl, if any.
// A miss is ALWAYS recorded in memory first, so a write failure can never
// cost the exit code or the report — but it does cost the durable record a
// CI job reads afterwards, so it is surfaced rather than swallowed.
func (s *Server) MissLogErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logErr
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	// CORS first, and on EVERY path out of here. A browser blocked by CORS
	// never sees the loud 501 body, only a network error, which reads to a
	// developer as an app bug rather than as a replay miss.
	reflectCORS(w, r)

	if r.Method == http.MethodOptions {
		// A preflight is not a recorded exchange. Answered before the
		// table lookup so it can never be counted as a client deviation.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var body any
	var raw string
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
		if err == nil {
			raw = string(b)
			body = decodeJSON(b)
		}
	}

	s.mu.Lock()
	res := s.bundle.Match(Request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body, Raw: raw,
	}, s.opts)
	if res.Miss || res.Hit == nil {
		miss := Miss{
			TS: s.now().UTC(), Kind: MissUnmatched,
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Diff: res.Diff,
		}
		if res.Nearest != nil {
			k := res.Nearest.Key
			miss.Nearest = &k
		}
		s.misses = append(s.misses, miss)
		s.appendMissLocked(miss)
		s.mu.Unlock()
		writeMiss(w, miss)
		return
	}
	// Copied out under the lock: the Exchange lives in a slice the next
	// Match may re-read, and the response is written after the unlock.
	hit := *res.Hit
	s.served++
	s.mu.Unlock()
	writeHit(w, r, hit)
}

// writeHit replays one recorded exchange. Headers are replayed as
// recorded except two families that describe THIS exchange rather than the
// recorded payload:
//
//   - connection headers. Content-Length is recomputed by net/http from
//     the body actually written, and hop-by-hop headers belong to the
//     original connection and would be wrong (or fatal) on this one.
//   - the CORS plane. reflectCORS has already answered THIS request's
//     Origin, and a recorded Access-Control-Allow-Origin would otherwise
//     win the Set() and replay the ORIGIN OF THE RECORDING to a browser on
//     localhost — which is every browser-driven capture, the primary
//     consumer. A recorded bare "*" would replay as a bare "*", the exact
//     value reflectCORS exists never to emit, and a credentialed request
//     would fail. The browser then blocks the response, the developer sees
//     a network error, and it reads as an app bug — and because it lands on
//     a HIT, the miss machinery never sees it. Vary goes with them: it is
//     part of the same answer, and this server's Vary is Origin.
//
// Two more are REWRITTEN rather than skipped, because they are legitimate
// parts of a recorded flow that name the world the RECORDING was made in
// (the family Access-Control-Allow-Origin belongs to) and would send the
// client somewhere else: see rewriteLocation and stripCookieDomain.
func writeHit(w http.ResponseWriter, r *http.Request, e Exchange) {
	for k, v := range e.Headers {
		if connectionHeader(k) || corsHeader(k) {
			continue
		}
		switch {
		case strings.EqualFold(k, "location"):
			v = rewriteLocation(v, r)
		case strings.EqualFold(k, "set-cookie"):
			v = stripCookieDomain(v)
		}
		w.Header().Set(k, v)
	}
	status := e.Status
	if status == 0 {
		// A recorded hop with no status is a hop whose upstream never
		// answered. Replaying it as 200 would invent a success the
		// recording never saw; 502 says what actually happened.
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, e.Body)
}

func connectionHeader(name string) bool {
	switch strings.ToLower(name) {
	case "content-length", "transfer-encoding", "connection", "keep-alive",
		"proxy-authenticate", "proxy-authorization", "te", "trailer", "upgrade":
		return true
	}
	return false
}

// rewriteLocation points a recorded redirect back at THIS server.
//
// THE ESCAPE THIS CLOSES IS THE PACKAGE'S CENTRAL INVARIANT. A replay
// server has no upstream, no passthrough and no way for a call to reach a
// live system — verified by construction, and worth nothing if a recorded
// `302 Location: https://prod.example.com/next` is replayed verbatim,
// because every browser and every default HTTP client follows it and
// issues the next request AGAINST THE RECORDED HOST. A CI job that is
// supposed to be hermetic then reads production data, and mutates it for
// any redirect the app follows with a method that mutates. It is silent,
// it lands on a HIT, and the miss machinery cannot see it: the follow-up
// request never arrives here.
//
// Rewritten rather than refused at load, unlike the Content-Encoding and
// partial-response arms: a redirect is an ordinary part of a real recorded
// flow, so refusing would reject a large fraction of honest bundles. With
// the rewrite the client follows the redirect BACK INTO US, and then
// either the recording contains that next exchange — a hit, and the
// recorded flow replays end to end, which is what we wanted — or it does
// not, and it is a 501 miss with a misses.jsonl line and exit 2, which is
// the loud, correct answer for a flow the recording does not cover. Either
// way nothing escapes.
//
// The authority written in is the one the CLIENT reached us on (r.Host,
// which httpguard has already confirmed is loopback), so it is the
// listener's actual bound address and port rather than a configured or
// assumed one. Path, query and fragment are preserved exactly; any
// userinfo is dropped, because credentials minted for another host have no
// business being sent to this one.
//
// Two things are deliberately NOT rewritten:
//
//   - A RELATIVE Location ("/next", "next") already resolves against our
//     own host, so it is safe and is left byte-identical. Rewriting it
//     would only be a chance to mangle it.
//   - A non-HTTP scheme ("myapp://callback", "mailto:"). It cannot reach
//     an HTTP upstream, no HTTP client follows it, and rewriting it would
//     break the deep-link flows apps legitimately redirect into. (The
//     opaque form, "http:next", is left alone for the same reason it is
//     safe: a client resolves it against the base URL — us.)
//
// Which HOST is named makes no difference. A Location to some third party
// the recording never captured is rewritten too, so it becomes a miss
// rather than a real request to that host: under strict replay "we did not
// record this" is the answer, and it must be loud.
func rewriteLocation(loc string, r *http.Request) string {
	u, err := url.Parse(strings.TrimSpace(loc))
	if err != nil {
		// An unparseable Location cannot be shown to be safe, so it is not
		// passed on. Dropping it leaves the recorded 3xx with no target,
		// which stops the client rather than sending it somewhere unknown.
		return ""
	}
	if u.Scheme == "" && u.Host == "" {
		return loc // relative: already points at this server
	}
	if u.Opaque != "" {
		return loc
	}
	if u.Scheme != "" && !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return loc
	}
	u.Scheme = requestScheme(r)
	u.Host = r.Host
	u.User = nil
	return u.String()
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// stripCookieDomain removes the Domain attribute from a recorded
// Set-Cookie, leaving every other attribute exactly as recorded.
//
// A recorded `Domain=prod.example.com` can never match a loopback
// listener, so the browser drops the cookie outright: the session never
// establishes and the test fails somewhere later wearing an app bug's
// clothing. That is the same misattribution reflectCORS exists to prevent,
// and the same family — a recorded value naming the world the recording
// was made in. Without the attribute the cookie is host-only for whatever
// host the client used, which is this server.
//
// Only Domain. `Secure` is deliberately left alone — see the report: it
// does not have the same effect on a loopback listener, and stripping it
// would weaken what the recording said rather than re-address it.
//
// One interaction, reported rather than papered over: trace.Hop headers
// are a map[string]string, and core/proxy's flatHeaders JOINS a repeated
// header with ", ", so a response that set TWO cookies is already recorded
// as one malformed value before replay sees it. Splitting that on ";" can
// drop a segment carrying the second cookie's name. This function does not
// try to unpick it — reconstructing cookies from a lossy join would hide a
// capture-layer defect behind a plausible-looking result, which is the one
// thing this package refuses to do.
func stripCookieDomain(v string) string {
	parts := strings.Split(v, ";")
	out := parts[:0]
	for _, p := range parts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), "domain=") {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}

// corsHeader reports the response headers the REPLAY SERVER owns rather
// than the recording — see writeHit.
func corsHeader(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "access-control-") || n == "vary"
}

// writeMiss is the loud refusal: 501 Not Implemented, with everything a
// human needs to see why, in a body a machine can parse.
func writeMiss(w http.ResponseWriter, m Miss) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(struct {
		Error   string      `json:"error"`
		Hint    string      `json:"hint"`
		Method  string      `json:"method"`
		Path    string      `json:"path"`
		Query   string      `json:"query,omitempty"`
		Nearest *Key        `json:"nearest,omitempty"`
		Diff    []MissField `json:"diff,omitempty"`
	}{
		Error: "replay: no recorded exchange matches " + m.Method + " " + m.Path + pathQuery(m.Query),
		Hint: "this call is not in the reference bundle. Either the client changed and this is the deviation " +
			"replay exists to catch, or the recording is stale — re-record the flow with `retrace run` and " +
			"promote it with `retrace ref accept`.",
		Method: m.Method, Path: m.Path, Query: m.Query, Nearest: m.Nearest, Diff: m.Diff,
	})
}

func pathQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

// appendMissLocked appends one miss to misses.jsonl. Called with s.mu
// held, after the in-memory record — so the authoritative count is already
// safe when this fails.
func (s *Server) appendMissLocked(m Miss) {
	if s.missesPath == "" {
		return
	}
	line, err := json.Marshal(m)
	if err != nil {
		s.noteLogErr(err)
		return
	}
	f, err := os.OpenFile(s.missesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		s.noteLogErr(err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		s.noteLogErr(err)
	}
}

func (s *Server) noteLogErr(err error) {
	if s.logErr == nil {
		s.logErr = err
	}
}

// reflectCORS reflects the request's actual Origin, never a bare "*": with
// "*" a credentialed request fails outright, and the developer sees a CORS
// error instead of the 501 this server went to the trouble of writing.
// Origins the httpguard rejects never reach here at all.
func reflectCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Allow-Methods", "*")
	if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
		h.Set("Access-Control-Allow-Headers", req)
	}
}

func decodeJSON(raw []byte) any {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}
