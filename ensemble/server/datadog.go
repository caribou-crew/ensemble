package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
	dd "github.com/caribou-crew/ensemble/ensemble/datadog"
)

// resolveDatadogCredentials resolves cfg's Datadog site + API/app keys per
// design.md's precedence: when a `datadog:` block is configured, its
// `site` is plain non-secret config (default datadoghq.com, never read
// from DD_SITE); when the block is entirely absent, DD_SITE is read
// directly, so `from-datadog` needs no ensemble.yaml changes at all to try
// out. The two key env var *names* always come from
// cfg.DatadogAPIKeyEnvName/AppKeyEnvName (defaulting to DD_API_KEY/
// DD_APP_KEY either way); their values are read through cfg.LookupEnv
// (real env, then .env).
func resolveDatadogCredentials(cfg *config.Config) (site, apiKey, appKey string, err error) {
	if cfg.Datadog != nil {
		site = cfg.DatadogSite()
	} else if v, ok := cfg.LookupEnv("DD_SITE"); ok && v != "" {
		site = v
	} else {
		site = config.DefaultDatadogSite
	}

	apiKeyEnv := cfg.DatadogAPIKeyEnvName()
	apiKey, ok := cfg.LookupEnv(apiKeyEnv)
	if !ok || apiKey == "" {
		return "", "", "", fmt.Errorf("datadog API key not set: export %s (or add it to .env)", apiKeyEnv)
	}

	appKeyEnv := cfg.DatadogAppKeyEnvName()
	appKey, ok = cfg.LookupEnv(appKeyEnv)
	if !ok || appKey == "" {
		return "", "", "", fmt.Errorf("datadog application key not set: export %s (or add it to .env)", appKeyEnv)
	}

	return site, apiKey, appKey, nil
}

// datadogClient returns Deps.Datadog when set (tests), else a real
// dd.HTTPClient built from resolveDatadogCredentials.
func (s *server) datadogClient() (dd.Client, error) {
	if s.Deps.Datadog != nil {
		return s.Deps.Datadog, nil
	}
	site, apiKey, appKey, err := resolveDatadogCredentials(s.Cfg)
	if err != nil {
		return nil, err
	}
	return dd.NewHTTPClient(site, apiKey, appKey), nil
}

func (s *server) handleLatencyFromDatadog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target        string `json:"target"`
		Query         string `json:"query"`
		WindowMinutes int    `json:"window_minutes"`
		Path          string `json:"path"`
		Enabled       *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.Target == "" {
		writeErr(w, http.StatusBadRequest, "target is required")
		return
	}
	if body.Query == "" {
		writeErr(w, http.StatusBadRequest, "query is required")
		return
	}

	client, err := s.datadogClient()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	windowMinutes := body.WindowMinutes
	if windowMinutes <= 0 {
		windowMinutes = s.Cfg.DatadogDefaultWindowMinutes()
	}
	p50, p95, p99, err := dd.QueryPercentileTriple(r.Context(), client, body.Query, windowMinutes)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// Pulled rules are stored disarmed by design (proxy.LatencyRule's own
	// doc comment) unless the caller explicitly asks otherwise.
	enabled := false
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	s.Lat.Set(proxy.LatencyRule{
		Target:  body.Target,
		Path:    body.Path,
		P50:     p50,
		P95:     p95,
		P99:     p99,
		Enabled: enabled,
		Source:  fmt.Sprintf("datadog:%s (last %dm)", body.Query, windowMinutes),
	})
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.Lat.Rules()})
}

// latencyApplyOutcome is one profile rule's apply result — always reported,
// success or failure, per design.md's "apply is best-effort per rule, not
// all-or-nothing" decision.
type latencyApplyOutcome struct {
	Target  string  `json:"target"`
	Path    string  `json:"path"`
	OK      bool    `json:"ok"`
	Error   string  `json:"error,omitempty"`
	P50     float64 `json:"p50,omitempty"`
	P95     float64 `json:"p95,omitempty"`
	P99     float64 `json:"p99,omitempty"`
	FixedMs float64 `json:"fixedMs,omitempty"`
	Source  string  `json:"source,omitempty"`
}

func (s *server) handleLatencyApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.Profile == "" {
		writeErr(w, http.StatusBadRequest, "profile is required")
		return
	}
	profile := s.Cfg.LatencyProfile(body.Profile)
	if profile == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("unknown latency profile %q", body.Profile))
		return
	}

	needsClient := false
	for _, rule := range profile.Rules {
		if rule.FromDatadog != nil {
			needsClient = true
			break
		}
	}
	var client dd.Client
	if needsClient {
		var err error
		client, err = s.datadogClient()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	outcomes := make([]latencyApplyOutcome, 0, len(profile.Rules))
	for _, rule := range profile.Rules {
		out := latencyApplyOutcome{Target: rule.Target, Path: rule.Path}
		if rule.FromDatadog != nil {
			windowMinutes := rule.FromDatadog.WindowMinutes
			if windowMinutes <= 0 {
				windowMinutes = s.Cfg.DatadogDefaultWindowMinutes()
			}
			p50, p95, p99, err := dd.QueryPercentileTriple(r.Context(), client, rule.FromDatadog.Query, windowMinutes)
			if err != nil {
				out.Error = err.Error()
				outcomes = append(outcomes, out)
				continue
			}
			out.OK = true
			out.P50, out.P95, out.P99 = p50, p95, p99
			out.Source = fmt.Sprintf("datadog:%s (last %dm)", rule.FromDatadog.Query, windowMinutes)
			s.Lat.Set(proxy.LatencyRule{
				Target: rule.Target, Path: rule.Path,
				P50: p50, P95: p95, P99: p99,
				Enabled: false,
				Source:  out.Source,
			})
		} else {
			out.OK = true
			out.FixedMs = rule.FixedMs
			s.Lat.Set(proxy.LatencyRule{
				Target: rule.Target, Path: rule.Path,
				FixedMs: rule.FixedMs,
				Enabled: false,
			})
		}
		outcomes = append(outcomes, out)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": outcomes})
}
