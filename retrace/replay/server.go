package replay

import (
	"encoding/json"
	"io"
	"net/http"
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
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
		if err == nil {
			body = decodeJSON(raw)
		}
	}

	s.mu.Lock()
	res := s.bundle.Match(Request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body,
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
	s.mu.Unlock()
	writeHit(w, hit)
}

// writeHit replays one recorded exchange. Headers are replayed as
// recorded except the ones that describe THIS connection rather than the
// payload: Content-Length is recomputed by net/http from the body actually
// written, and hop-by-hop headers belong to the original connection and
// would be wrong (or fatal) on this one.
func writeHit(w http.ResponseWriter, e Exchange) {
	for k, v := range e.Headers {
		if connectionHeader(k) {
			continue
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
