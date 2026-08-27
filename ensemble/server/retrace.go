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
