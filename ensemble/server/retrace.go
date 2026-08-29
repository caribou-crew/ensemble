package server

import (
	"net/http"

	retraceconfig "github.com/caribou-crew/ensemble/retrace/config"
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

func (s *server) handleRetraceShot(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteShot(w, d, app, flow, r.PathValue("side"), r.PathValue("name"))
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

// retraceSyncCandidatesResponse is GET /api/retrace/sync/candidates's
// body. Candidates is never null (see sync.List's own doc comment),
// matching sync.Result's Synced/Skipped convention.
type retraceSyncCandidatesResponse struct {
	Candidates []sync.Candidate `json:"candidates"`
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
	writeJSON(w, http.StatusOK, retraceSyncCandidatesResponse{Candidates: candidates})
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
	if rc.Repo == "" {
		writeErr(w, http.StatusBadRequest, "retrace sync needs a repo — set retrace.repo in ensemble.yaml")
		return
	}
	since, err := sync.ParseSince(rc.EffectiveSince())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, err := sync.Run(sync.Options{
		Cwd:      rc.EffectiveDir(s.Cfg.Dir),
		From:     "github",
		Repo:     rc.Repo,
		Workflow: rc.Workflow,
		Since:    since,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
