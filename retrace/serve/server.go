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
	// sources is nil for every server built by New — today's single-root
	// behavior, unchanged. NewWithSources sets it: an app present in its
	// map is served from that app's own root; an app absent from it (or
	// every request, when sources is nil) falls back to d, exactly as an
	// app absent from ensemble's RetraceConfig.Apps falls back to that
	// stack's one Cfg. See depsForApp and Sources' own doc comment for why
	// this is swapped as a whole *Sources rather than mutated in place.
	sources *Sources
	// syncCfg carries the repo.yaml sync defaults (repo + filters) so the
	// sync routes can fall back to them when a request names no repo, and
	// GET /api/sync/config can hand them to the UI to prefill. Zero value
	// (no repo) is the pre-repo.yaml behavior: every sync request must name
	// its own repo. Set only by NewWithSourcesAndSync.
	syncCfg SyncConfig
}

// SyncConfig is the repo-wide sync default a standalone `retrace serve`
// reads from retrace.repo.yaml (retrace/repoconfig) and exposes at
// GET /api/sync/config, so the Browse-&-sync panel can prefill the repo and
// filters instead of making a human retype what the config already declares.
// Every field is optional; an empty Repo means "no configured default".
type SyncConfig struct {
	Repo      string   `json:"repo"`
	Workflows []string `json:"workflows,omitempty"`
	Branch    string   `json:"branch,omitempty"`
	Branches  []string `json:"branches,omitempty"`
	Actor     string   `json:"actor,omitempty"`
	Event     string   `json:"event,omitempty"`
	Status    string   `json:"status,omitempty"`
	Since     string   `json:"since,omitempty"`
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
	return buildServer(d, nil, SyncConfig{})
}

// NewWithSources is New, plus a Sources aggregating one or more additional
// project roots — what `retrace serve` builds when it finds a
// retrace.repo.yaml (retrace/repoconfig). d remains the server's default:
// every route not naming an {app} (health) or naming one absent from
// sources resolves against d, unchanged from New's own behavior. Every
// existing caller of New (including ensemble/server, which never has a
// repo-scoped config) is completely unaffected by this function's
// existence.
func NewWithSources(d Deps, sources *Sources) http.Handler {
	return buildServer(d, sources, SyncConfig{})
}

// NewWithSourcesAndSync is NewWithSources plus the repo.yaml sync defaults,
// so the sync routes can fall back to a configured repo and GET
// /api/sync/config can prefill the panel. `retrace serve` uses this when it
// finds a retrace.repo.yaml with a `repo:`; every other caller keeps using
// New / NewWithSources with no configured repo, unchanged.
func NewWithSourcesAndSync(d Deps, sources *Sources, sync SyncConfig) http.Handler {
	return buildServer(d, sources, sync)
}

func buildServer(d Deps, sources *Sources, sync SyncConfig) http.Handler {
	s := &server{d: d, sources: sources, syncCfg: sync}
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

// deps returns the current default Deps by value. Handlers work from this
// copy, so a concurrent reload cannot change the Config out from under a
// diff that is already running.
func (s *server) deps() Deps {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d
}

// depsForApp resolves the Deps that governs app: its own root's Deps when
// sources is set and maps app, else the server's default Deps — the same
// fallback DepsFor's own doc comment names. Read under one lock so a
// concurrent reloadConfig cannot hand back a d paired with a sources (or
// vice versa) from two different points in time.
func (s *server) depsForApp(app string) Deps {
	s.mu.RLock()
	sources, d := s.sources, s.d
	s.mu.RUnlock()
	if sources != nil {
		if resolved, ok := sources.DepsFor(app); ok {
			return resolved
		}
	}
	return d
}

// currentSources returns the server's current *Sources (nil for a
// single-root server), copied out under lock for the same reason deps()
// is: a concurrent reload must not change what an in-flight request reads.
func (s *server) currentSources() *Sources {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sources
}

// reloadConfig re-discovers the project config (retrace.yaml plus the
// machine-owned wire-rule overlay) at cwd and swaps it in — for the
// server's default Deps when cwd is its Cwd, and/or for that root's entry
// in sources when one is set (see Sources.withConfig's own doc comment for
// why that is a whole-value swap rather than an in-place mutation). Called
// after a rule is appended, which is the only thing this server does that
// changes what a diff means.
func (s *server) reloadConfig(cwd string) error {
	cfg, err := config.Discover(cwd)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sources != nil {
		next := s.sources.withConfig(cwd, cfg)
		s.sources = &next
	}
	if cwd == s.d.Cwd {
		s.d.Cfg = cfg
	}
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
