// sync.go is the standalone viewer's half of "discover → filter → select →
// pull": it exposes retrace/sync's List/Run over HTTP so `retrace serve`
// can browse and pull GitHub Actions runs directly, without ensemble in the
// loop. Unlike ensemble's own /api/retrace/sync routes (ensemble/server),
// this package's Deps carries no repo default — retrace.yaml has no `sync:`
// block — so every request names its own repo(s).
package serve

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/runs"
	"github.com/caribou-crew/ensemble/retrace/sync"
)

// candidateWithLocalRuns is one Candidate plus the local-runs join a
// click-to-view sync panel needs: sync.Candidate itself has no notion of
// what already exists on disk (it is built purely from `gh run list`), so
// the join lives here at the HTTP boundary, where a caller actually needs
// the answer, rather than inside the sync package.
type candidateWithLocalRuns struct {
	sync.Candidate
	// LocalRuns is every "app/flow/run-id" already pulled from this CI
	// run's URL (runs.SourcesByURL's reverse index — see its doc comment
	// for why RunURL is the join key). Never null: an empty slice means
	// "not pulled yet", the same never-null contract every other array on
	// this response already carries.
	LocalRuns []string `json:"localRuns"`
}

// syncCandidatesResponse is GET /api/sync/candidates's body. Candidates is
// never null — sync.List's own doc comment already guarantees this, kept
// here so the wire shape says so too.
type syncCandidatesResponse struct {
	Candidates []candidateWithLocalRuns `json:"candidates"`
}

// handleSyncConfig returns the repo.yaml sync defaults so the Browse-&-sync
// panel can prefill the repo and filters rather than making a human retype
// what retrace.repo.yaml already declares. Empty Repo means no configured
// default (the pre-repo.yaml behavior: the panel asks for one).
func (s *server) handleSyncConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.syncCfg)
}

func (s *server) handleSyncCandidates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repos := reposFrom(q.Get("repo"), q.Get("repos"))
	// Fall back to the repo.yaml default when the request names none — the
	// config already declared it, so a browse should not 400 for omitting it.
	if len(repos) == 0 && s.syncCfg.Repo != "" {
		repos = []string{s.syncCfg.Repo}
	}
	if len(repos) == 0 {
		writeErr(w, http.StatusBadRequest, "sync needs a repo — pass ?repo=org/repo or ?repos=org/a,org/b")
		return
	}
	since := sync.DefaultSince
	if v := q.Get("since"); v != "" {
		parsed, err := sync.ParseSince(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		since = parsed
	}
	candidates, err := sync.List(sync.Options{
		Cwd: s.deps().Cwd, From: "github",
		Repos: repos, Workflows: splitCSV(q.Get("workflows")),
		Branch: q.Get("branch"), Actor: q.Get("actor"), Event: q.Get("event"), Status: q.Get("status"),
		Since: since,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// A traversal failure here is not fatal to the whole response — every
	// candidate simply comes back with no known local runs, same as if
	// nothing had ever been synced. The candidate list itself (what CI has)
	// is still useful without the local-runs join (what's already pulled).
	byURL, _ := runs.SourcesByURL(runs.RunsRoot(s.deps().Cwd))
	out := make([]candidateWithLocalRuns, len(candidates))
	for i, c := range candidates {
		local := byURL[c.URL]
		if local == nil {
			local = []string{}
		}
		out[i] = candidateWithLocalRuns{Candidate: c, LocalRuns: local}
	}
	writeJSON(w, http.StatusOK, syncCandidatesResponse{Candidates: out})
}

// syncRequest is POST /api/sync's body. Every field List's query params
// also carry travels here too, alongside Selections — this package has no
// config to remember a prior candidates call's filters by, so a caller
// pulling by selection still names the repo(s) that selection came from
// (sync.Run re-lists before it can match a Selection against a run).
type syncRequest struct {
	Repo       string           `json:"repo"`
	Repos      []string         `json:"repos"`
	Workflows  []string         `json:"workflows"`
	Branch     string           `json:"branch"`
	Actor      string           `json:"actor"`
	Event      string           `json:"event"`
	Status     string           `json:"status"`
	Since      string           `json:"since"`
	Selections []sync.Selection `json:"selections"`
}

func (s *server) handleSync(w http.ResponseWriter, r *http.Request) {
	var body syncRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "sync: invalid JSON body: "+err.Error())
		return
	}
	repos := reposFrom(body.Repo, strings.Join(body.Repos, ","))
	if len(repos) == 0 && s.syncCfg.Repo != "" {
		repos = []string{s.syncCfg.Repo}
	}
	if len(repos) == 0 {
		writeErr(w, http.StatusBadRequest, "sync needs a repo — set \"repo\" or \"repos\" in the request body")
		return
	}
	since := sync.DefaultSince
	if body.Since != "" {
		parsed, err := sync.ParseSince(body.Since)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		since = parsed
	}
	base := sync.Options{
		From:  "github",
		Repos: repos, Workflows: body.Workflows,
		Branch: body.Branch, Actor: body.Actor, Event: body.Event, Status: body.Status,
		Since: since, Selections: body.Selections,
	}

	// Multi-root (retrace.repo.yaml): run the sync once PER ROOT, each with
	// that root's Cwd and app allowlist, so an app's runs land under the
	// root holding its own .retrace-ref/ rather than all under the serve
	// process's single cwd. Writing every app's runs to one cwd orphans
	// every app whose reference lives in another root, which the queue then
	// shows as quarantined/no-source — the exact failure this routing
	// fixes. Mirrors the per-root watchTarget list `--watch` already uses.
	// Single-root (sources == nil): one sync at the default cwd, unchanged.
	sources := s.currentSources()
	if sources == nil {
		base.Cwd = s.deps().Cwd
		res, err := sync.Run(base)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	merged := sync.Result{Synced: []string{}, Skipped: []sync.SkipReason{}}
	for _, t := range sources.RootTargets() {
		o := base
		o.Cwd = t.Root
		o.Apps = t.Apps
		res, err := sync.Run(o)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		merged.Synced = append(merged.Synced, res.Synced...)
		merged.Skipped = append(merged.Skipped, res.Skipped...)
	}
	writeJSON(w, http.StatusOK, merged)
}

// reposFrom resolves the repo(s) a request named: repos (comma-separated)
// takes precedence, falling back to the single repo value.
func reposFrom(repo, repos string) []string {
	if list := splitCSV(repos); len(list) > 0 {
		return list
	}
	if repo != "" {
		return []string{repo}
	}
	return nil
}

// splitCSV splits a comma-separated query/body value into a trimmed,
// empty-entry-free slice — nil for an empty input, so callers can treat
// "absent" and "empty" identically.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
