package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/caribou-crew/ensemble/ensemble/config"
	retraceconfig "github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
	"github.com/caribou-crew/ensemble/retrace/serve"
	"github.com/caribou-crew/ensemble/retrace/sync"
)

// retraceInstanceFor resolves which of RetraceConfig.EffectiveInstances a
// request targets. An explicit key (the ?instance= query param) must name
// a real entry — ok=false otherwise. With no key: the sole entry when
// there is exactly one (the common single-repo case, and what makes an
// unmodified single-instance config behave exactly as it did before
// instance support existed — the synthetic "default" entry, unlabeled and
// invisible to the caller); with more than one entry and no key, the
// request is ambiguous and ok=false.
func retraceInstanceFor(rc *config.RetraceConfig, key string) (string, config.RetraceInstanceConfig, bool) {
	instances := rc.EffectiveInstances()
	if key != "" {
		inst, ok := instances[key]
		return key, inst, ok
	}
	if len(instances) == 1 {
		for name, inst := range instances {
			return name, inst, true
		}
	}
	return "", config.RetraceInstanceConfig{}, false
}

// resolveRetraceInstance resolves the RetraceInstanceConfig a request
// targets, reading the optional ?instance= query param — see
// retraceInstanceFor. Writes its own error response and returns ok=false
// both when no `retrace:` block is configured at all (501, same as
// before instance support existed) and when the requested/implied
// instance can't be resolved (404 for an unknown key, 400 when multiple
// instances exist and none was named).
func (s *server) resolveRetraceInstance(w http.ResponseWriter, r *http.Request) (string, config.RetraceInstanceConfig, bool) {
	if s.Cfg.Retrace == nil {
		writeErr(w, http.StatusNotImplemented, "retrace not configured — add a retrace: block to ensemble.yaml")
		return "", config.RetraceInstanceConfig{}, false
	}
	key := r.URL.Query().Get("instance")
	name, inst, ok := retraceInstanceFor(s.Cfg.Retrace, key)
	if !ok {
		if key != "" {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("retrace: no such instance %q", key))
		} else {
			writeErr(w, http.StatusBadRequest, "retrace: multiple instances configured — specify ?instance=")
		}
		return "", config.RetraceInstanceConfig{}, false
	}
	return name, inst, true
}

// retraceDeps resolves a fresh retrace/serve.Deps for the current
// request's instance (see resolveRetraceInstance). There is no
// config-reload trigger in this package (unlike `retrace serve`'s own
// reloadConfig) — retrace.yaml and its overlay are cheap to read, so
// every request re-discovers them rather than caching a stale copy.
//
// CfgFor resolves each app against the instance's Apps map, re-discovering
// retrace/config.Config from that app's own directory — see
// EffectiveAppDir's own doc comment for why this exists: a run synced from
// a different repository's CI was recorded against a retrace.yaml the
// stack dir never has. An app with no Apps entry resolves to the SAME
// directory the un-mapped Cfg above was already discovered from, so the
// closure returns cfg directly rather than re-discovering it — every
// stack with no apps: map pays no extra Discover call and behaves
// byte-for-byte as before this field existed.
func (s *server) retraceDeps(w http.ResponseWriter, r *http.Request) (serve.Deps, bool) {
	_, rc, ok := s.resolveRetraceInstance(w, r)
	if !ok {
		return serve.Deps{}, false
	}
	dir := rc.EffectiveDir(s.Cfg.Dir)
	cfg, err := retraceconfig.Discover(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return serve.Deps{}, false
	}
	return serve.Deps{
		Cwd: dir, Cfg: cfg, Version: s.Version,
		CfgFor: func(app string) (*retraceconfig.Config, error) {
			appDir := rc.EffectiveAppDir(s.Cfg.Dir, app)
			if appDir == dir {
				return cfg, nil
			}
			return retraceconfig.Discover(appDir)
		},
	}, true
}

// retraceInstanceInfo is one entry in GET /api/retrace/instances' list —
// Key is what the frontend passes back as ?instance= on every subsequent
// call; Label is what a picker shows. They're the same value today (the
// config map key IS the display name — see RetraceConfig.Instances' own
// doc comment); kept as separate fields in case a friendlier display name
// is ever wanted without changing the routing key.
type retraceInstanceInfo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

func (s *server) handleRetraceInstances(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.Retrace == nil {
		writeErr(w, http.StatusNotImplemented, "retrace not configured — add a retrace: block to ensemble.yaml")
		return
	}
	instances := s.Cfg.Retrace.EffectiveInstances()
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]retraceInstanceInfo, len(names))
	for i, name := range names {
		out[i] = retraceInstanceInfo{Key: name, Label: name}
	}
	writeJSON(w, http.StatusOK, struct {
		Instances []retraceInstanceInfo `json:"instances"`
	}{Instances: out})
}

func (s *server) handleRetraceQueue(w http.ResponseWriter, r *http.Request) {
	d, ok := s.retraceDeps(w, r)
	if !ok {
		return
	}
	serve.WriteQueue(w, d, serve.QueueFilterFromQuery(r.URL.Query()))
}

// retraceFlowFrom resolves Deps plus the {app}/{flow} path values,
// validating the flow exists via the same ResolveFlow retrace/serve's own
// routes use — so a bad app/flow gets the identical status/message here as
// it would from `retrace serve` directly.
func (s *server) retraceFlowFrom(w http.ResponseWriter, r *http.Request) (serve.Deps, string, string, bool) {
	d, ok := s.retraceDeps(w, r)
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

// handleRetraceRuns is the per-flow RUNS LIST (every run of a surface,
// newest first) — the drill-down the queue row opens. Its absence is why
// clicking a flow in ensemble-ui showed "no runs" even for a flow whose
// runs exist on disk: with no route, the path fell through to the SPA
// fallback and the UI read HTML where it expected a runs array. Mirrors
// retrace/serve's own GET /queue/{app}/{flow}/runs.
func (s *server) handleRetraceRuns(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.retraceFlowFrom(w, r)
	if !ok {
		return
	}
	serve.WriteRuns(w, d, app, flow)
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
	_, rc, ok := s.resolveRetraceInstance(w, r)
	if !ok {
		return
	}
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
	_, rc, ok := s.resolveRetraceInstance(w, r)
	if !ok {
		return
	}
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
