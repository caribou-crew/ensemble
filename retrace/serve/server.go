package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/caribou-crew/ensemble/core/httpguard"
	"github.com/caribou-crew/ensemble/retrace/config"
)

// server holds the review server's mutable state: the Deps it answers from,
// behind a lock because POST /api/queue/{app}/{flow}/rule RELOADS the config
// (the appended rule must take effect on the very next queue read) while
// other requests are mid-diff. The Config pointer is swapped, never mutated
// in place, so a request that already copied its Deps keeps reading a
// consistent one.
type server struct {
	mu sync.RWMutex
	d  Deps
}

// New builds the review server's HTTP surface: the /api control plane plus
// the review UI at "/", every route behind the shared Origin/Host guard.
//
// The guard is core/httpguard — the SAME body ensemble's dashboard, the
// marker door and the replay server sit behind, never a second copy of "the
// Sec-Fetch-Site part". This plane is unauthenticated and serves captured
// traffic and screenshots, so it is the only thing between a random page the
// developer has open and the recordings.
//
// d.AllowedHosts is passed straight through: nil (the loopback default) is
// the SAFE zero value there — loopback-only, never "no allow-list, so allow
// anything".
func New(d Deps) http.Handler {
	s := &server{d: d}
	mux := http.NewServeMux()
	s.routes(mux)
	// /api/* goes to the mux and NOTHING else does. Mounting the UI inside
	// the mux as a "GET /" catch-all instead would make it the fallback for
	// every API path no route matched — so GET on a POST-only verb would
	// answer 200 with the app shell rather than ServeMux's 405, and an
	// agent (the other half of this API-first surface) would read HTML as
	// success. Keeping the API in its own mux is what preserves those
	// 405s; see routes' doc comment.
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}
		s.handleUI(w, r)
	})
	return httpguard.Handler(d.AllowedHosts, root)
}

// deps returns the current Deps by value. Handlers work from this copy, so
// a concurrent reload cannot change the Config out from under a diff that
// is already running.
func (s *server) deps() Deps {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d
}

// reloadConfig re-discovers the project config (retrace.yaml plus the
// machine-owned wire-rule overlay) and swaps it in. Called after a rule is
// appended, which is the only thing this server does that changes what a
// diff means.
func (s *server) reloadConfig() error {
	cfg, err := config.Discover(s.deps().Cwd)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Cfg = cfg
	return nil
}

// writeJSON writes v as the response body. A marshal failure after the
// header is written cannot be un-sent, so the document is built first.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encoding the response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// writeErr mirrors the {"error": msg} shape every other surface in this repo
// uses, including httpguard's own refusals — a rejection from the guard and
// a rejection from a handler must not need two client-side decoders.
func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
