package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	retraceconfig "github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
	"github.com/caribou-crew/ensemble/retrace/serve"
	"github.com/caribou-crew/ensemble/retrace/sync"
)

// retraceDeps resolves a fresh retrace/serve.Deps for the current request.
// There is no config-reload trigger in this package (unlike `retrace
// serve`'s own reloadConfig) — retrace.yaml and its overlay are cheap to
// read, so every request re-discovers them rather than caching a stale
// copy. A nil s.Cfg.Retrace means no `retrace:` block is configured; it
// writes the 501 itself and returns ok=false, mirroring the Insp
// nil-disables-with-501 pattern this package already uses (routes.go
// registers these handlers unconditionally, same as the Insp-gated ones).
func (s *server) retraceDeps(w http.ResponseWriter) (serve.Deps, bool) {
	if s.Cfg.Retrace == nil {
		writeErr(w, http.StatusNotImplemented, "retrace not configured — add a retrace: block to ensemble.yaml")
		return serve.Deps{}, false
	}
	dir := s.Cfg.Retrace.EffectiveDir(s.Cfg.Dir)
	cfg, err := retraceconfig.Discover(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return serve.Deps{}, false
	}
	return serve.Deps{Cwd: dir, Cfg: cfg, Version: s.Version}, true
}

func (s *server) handleRetraceQueue(w http.ResponseWriter, r *http.Request) {
	d, ok := s.retraceDeps(w)
	if !ok {
		return
	}
	serve.WriteQueue(w, d)
}

// retraceFlowFrom resolves Deps plus the {app}/{flow} path values,
// validating the flow exists via the same ResolveFlow retrace/serve's own
// routes use — so a bad app/flow gets the identical status/message here as
// it would from `retrace serve` directly.
func (s *server) retraceFlowFrom(w http.ResponseWriter, r *http.Request) (serve.Deps, string, string, bool) {
	d, ok := s.retraceDeps(w)
	if !ok {
		return serve.Deps{}, "", "", false
	}
	app, flow := r.PathValue("app"), r.PathValue("flow")
	status, msg, ok := serve.ResolveFlow(d, app, flow)
	if !ok {
		writeErr(w, status, msg)
		return serve.Deps{}, "", "", false
	}
	return d, app, flow, true
}

func (s *server) handleRetraceItem(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteItem(w, d, app, flow)
}

func (s *server) handleRetraceItemAtRun(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteItemAtRun(w, d, app, flow, r.PathValue("runId"))
}

func (s *server) handleRetraceShot(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteShot(w, d, app, flow, r.PathValue("side"), r.PathValue("name"))
}

func (s *server) handleRetraceShotAtRun(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteShotAtRun(w, d, app, flow, r.PathValue("runId"), r.PathValue("side"), r.PathValue("name"))
}

func (s *server) handleRetraceEvidence(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteEvidence(w, d, app, flow)
}

func (s *server) handleRetraceVideo(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteVideo(w, r, d, app, flow, r.PathValue("name"))
}

func (s *server) handleRetraceReport(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteReport(w, r, d, app, flow)
}

// retraceCandidateWithLocalRuns mirrors retrace/serve's own
// candidateWithLocalRuns — see that type's doc comment for why the join
// lives at the HTTP boundary rather than inside the sync package.
type retraceCandidateWithLocalRuns struct {
	sync.Candidate
	LocalRuns []string `json:"localRuns"`
}

// retraceSyncCandidatesResponse is GET /api/retrace/sync/candidates's
// body. Candidates is never null (see sync.List's own doc comment),
// matching sync.Result's Synced/Skipped convention.
type retraceSyncCandidatesResponse struct {
	Candidates []retraceCandidateWithLocalRuns `json:"candidates"`
}

func (s *server) handleRetraceSyncCandidates(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.Retrace == nil {
		writeErr(w, http.StatusNotImplemented, "retrace not configured — add a retrace: block to ensemble.yaml")
		return
	}
	rc := s.Cfg.Retrace
	repos := rc.EffectiveRepos()
	if len(repos) == 0 {
		writeErr(w, http.StatusBadRequest, "retrace sync needs a repo — set retrace.repo or retrace.repos in ensemble.yaml")
		return
	}
	q := r.URL.Query()
	sinceStr := rc.EffectiveSince()
	if v := q.Get("since"); v != "" {
		sinceStr = v
	}
	since, err := sync.ParseSince(sinceStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	candidates, err := sync.List(sync.Options{
		Cwd: rc.EffectiveDir(s.Cfg.Dir), From: "github",
		Repos: repos, Workflows: rc.EffectiveWorkflows(),
		Branch: firstNonEmpty(q.Get("branch"), rc.Branch),
		Actor:  firstNonEmpty(q.Get("actor"), rc.Actor),
		Event:  firstNonEmpty(q.Get("event"), rc.Event),
		Status: firstNonEmpty(q.Get("status"), rc.Status),
		Since:  since,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// A traversal failure here is not fatal to the whole response — every
	// candidate simply comes back with no known local runs, same as if
	// nothing had ever been synced.
	byURL, _ := runs.SourcesByURL(runs.RunsRoot(rc.EffectiveDir(s.Cfg.Dir)))
	out := make([]retraceCandidateWithLocalRuns, len(candidates))
	for i, c := range candidates {
		local := byURL[c.URL]
		if local == nil {
			local = []string{}
		}
		out[i] = retraceCandidateWithLocalRuns{Candidate: c, LocalRuns: local}
	}
	writeJSON(w, http.StatusOK, retraceSyncCandidatesResponse{Candidates: out})
}

// firstNonEmpty returns the first non-empty string among vals — used to
// let a query param override a config default without a chain of
// if-not-empty checks at every call site.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *server) handleRetraceSync(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.Retrace == nil {
		writeErr(w, http.StatusNotImplemented, "retrace not configured — add a retrace: block to ensemble.yaml")
		return
	}
	rc := s.Cfg.Retrace
	repos := rc.EffectiveRepos()
	if len(repos) == 0 {
		writeErr(w, http.StatusBadRequest, "retrace sync needs a repo — set retrace.repo or retrace.repos in ensemble.yaml")
		return
	}
	since, err := sync.ParseSince(rc.EffectiveSince())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	var body struct {
		Selections []sync.Selection `json:"selections"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "retrace sync: invalid JSON body: "+err.Error())
		return
	}

	res, err := sync.Run(sync.Options{
		Cwd: rc.EffectiveDir(s.Cfg.Dir), From: "github",
		Repos: repos, Workflows: rc.EffectiveWorkflows(),
		Branch: rc.Branch, Actor: rc.Actor, Event: rc.Event, Status: rc.Status,
		Since:      since,
		Selections: body.Selections,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
