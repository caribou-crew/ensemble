package server

import (
	"net/http"
	"strings"

	retraceconfig "github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/repoconfig"
	"github.com/caribou-crew/ensemble/retrace/serve"
)

// handleRetraceEvidence delegates only explicitly registered GET routes to the
// existing Retrace surface. Do not mount the whole handler: it includes reference
// acceptance and rule mutations that this embedded dashboard does not expose.
func (s *server) handleRetraceEvidence(w http.ResponseWriter, r *http.Request) {
	cwd := "."
	if s.Cfg != nil && s.Cfg.Dir != "" {
		cwd = s.Cfg.Dir
	}
	cfg, err := retraceconfig.Discover(cwd)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defaultDeps := serve.Deps{Cwd: cwd, Cfg: cfg, AllowedHosts: s.AllowedHosts, Version: s.Version}
	var sources *serve.Sources
	repo, _, err := repoconfig.Discover(cwd)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if repo != nil {
		byRoot := make(map[string]serve.Deps, len(repo.Apps))
		appRoot := make(map[string]string, len(repo.Apps))
		for _, root := range repo.Roots() {
			d, err := serve.NewDepsForRoot(root, s.AllowedHosts, s.Version)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			byRoot[root] = d
		}
		for app, entry := range repo.Apps {
			appRoot[app] = entry.Root
		}
		mapped, err := serve.NewSources(byRoot, appRoot)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		sources = &mapped
	}
	next := r.Clone(r.Context())
	next.URL.Path = "/api" + strings.TrimPrefix(next.URL.Path, "/api/retrace")
	if next.URL.RawPath != "" {
		next.URL.RawPath = "/api" + strings.TrimPrefix(next.URL.RawPath, "/api/retrace")
	}
	serve.NewWithSources(defaultDeps, sources).ServeHTTP(w, next)
}
