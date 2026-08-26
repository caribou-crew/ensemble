package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// fakeDatadogClient answers QueryPercentile from a static map keyed by the
// literal query string, or an error keyed the same way — the same shape as
// ensemble/datadog's own test fake, redefined here to avoid a test-only
// dependency between the two packages.
type fakeDatadogClient struct {
	results map[string]float64
	errs    map[string]error
}

func (f *fakeDatadogClient) QueryPercentile(ctx context.Context, query string, windowMinutes int) (float64, error) {
	if err, ok := f.errs[query]; ok {
		return 0, err
	}
	return f.results[query], nil
}

// datadogTestEnv is a minimal server.New setup — just Cfg + a LatencyStore
// + an injected Datadog client — for the latency/from-datadog and
// latency/apply endpoints, which don't touch the orchestrator or recorder.
type datadogTestEnv struct {
	ts  *httptest.Server
	lat *proxy.LatencyStore
	cfg *config.Config
}

func newDatadogTestEnv(t *testing.T, cfg *config.Config, deps server.Deps) *datadogTestEnv {
	t.Helper()
	lat := proxy.NewLatencyStore(nil)
	deps.Cfg = cfg
	deps.Lat = lat
	deps.Version = "test"
	ts := httptest.NewServer(server.New(deps))
	t.Cleanup(ts.Close)
	return &datadogTestEnv{ts: ts, lat: lat, cfg: cfg}
}

func (e *datadogTestEnv) post(t *testing.T, path string, body any) (*http.Response, []byte) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(e.ts.URL+path, "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", path, err)
	}
	return resp, got
}

func TestLatencyFromDatadogHappyPath(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"billing": {Run: "./billing", Port: 8081, Proxy: 9081},
		},
	}
	client := &fakeDatadogClient{results: map[string]float64{
		// Datadog's trace-duration metrics report in seconds; QueryPercentileTriple
		// converts to ms, so these become p50=45 p95=120 p99=340 below.
		"p50:trace.dur{service:billing}": 0.045,
		"p95:trace.dur{service:billing}": 0.120,
		"p99:trace.dur{service:billing}": 0.340,
	}}
	e := newDatadogTestEnv(t, cfg, server.Deps{Datadog: client})

	resp, body := e.post(t, "/api/latency/from-datadog", map[string]any{
		"target": "billing",
		"query":  "p{P}:trace.dur{service:billing}",
		"path":   "/",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var got struct {
		Rules []proxy.LatencyRule `json:"rules"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got.Rules) != 1 {
		t.Fatalf("rules = %+v, want 1", got.Rules)
	}
	rule := got.Rules[0]
	if rule.P50 != 45 || rule.P95 != 120 || rule.P99 != 340 {
		t.Errorf("rule = %+v, want p50=45 p95=120 p99=340", rule)
	}
	if rule.Enabled {
		t.Error("pulled rule should be stored disarmed by default")
	}
	if !strings.HasPrefix(rule.Source, "datadog:") {
		t.Errorf("rule.Source = %q, want datadog: prefix", rule.Source)
	}
}

func TestLatencyFromDatadogMissingCredentials(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	e := newDatadogTestEnv(t, cfg, server.Deps{}) // no Datadog override -> real client -> no env vars set

	resp, body := e.post(t, "/api/latency/from-datadog", map[string]any{
		"target": "billing",
		"query":  "p{P}:trace.dur{service:billing}",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "DD_API_KEY") {
		t.Errorf("body = %s, want mention of DD_API_KEY", body)
	}
}

func writeApplyProfileFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLatencyApplyAllSucceed(t *testing.T) {
	dir := t.TempDir()
	writeApplyProfileFile(t, dir, "latency-production.yaml", `
rules:
  - target: billing
    path: /
    from_datadog:
      query: "p{P}:trace.dur{service:billing}"
  - target: billing
    path: /health
    fixed_ms: 5
`)
	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"billing": {Run: "./billing", Port: 8081, Proxy: 9081},
		},
		Latency: config.Latency{Profiles: map[string]config.LatencyProfile{
			"production": {File: "latency-production.yaml"},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := &fakeDatadogClient{results: map[string]float64{
		"p50:trace.dur{service:billing}": 0.010,
		"p95:trace.dur{service:billing}": 0.020,
		"p99:trace.dur{service:billing}": 0.030,
	}}
	e := newDatadogTestEnv(t, cfg, server.Deps{Datadog: client})

	resp, body := e.post(t, "/api/latency/apply", map[string]any{"profile": "production"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Results []struct {
			Target  string  `json:"target"`
			Path    string  `json:"path"`
			OK      bool    `json:"ok"`
			P50     float64 `json:"p50"`
			FixedMs float64 `json:"fixedMs"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("results = %+v, want 2", got.Results)
	}
	for _, r := range got.Results {
		if !r.OK {
			t.Errorf("result %+v not ok", r)
		}
	}
	for _, r := range got.Results {
		if r.Path == "/" && r.P50 != 10 {
			t.Errorf("Datadog result P50 = %v, want 10 (0.010s converted to ms)", r.P50)
		}
	}
	if rules := e.lat.Rules(); len(rules) != 2 {
		t.Fatalf("LatencyStore rules = %+v, want 2", rules)
	}
}

func TestLatencyApplyPartialFailure(t *testing.T) {
	dir := t.TempDir()
	writeApplyProfileFile(t, dir, "latency-production.yaml", `
rules:
  - target: billing
    path: /good
    from_datadog:
      query: "p{P}:good{service:billing}"
  - target: billing
    path: /bad
    from_datadog:
      query: "p{P}:bad{service:billing}"
`)
	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"billing": {Run: "./billing", Port: 8081, Proxy: 9081},
		},
		Latency: config.Latency{Profiles: map[string]config.LatencyProfile{
			"production": {File: "latency-production.yaml"},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := &fakeDatadogClient{
		results: map[string]float64{
			"p50:good{service:billing}": 0.010,
			"p95:good{service:billing}": 0.020,
			"p99:good{service:billing}": 0.030,
		},
		errs: map[string]error{
			"p50:bad{service:billing}": errNoData,
		},
	}
	e := newDatadogTestEnv(t, cfg, server.Deps{Datadog: client})

	resp, body := e.post(t, "/api/latency/apply", map[string]any{"profile": "production"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s (partial failure must not fail the whole request)", resp.StatusCode, body)
	}
	var got struct {
		Results []struct {
			Path  string `json:"path"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("results = %+v, want 2", got.Results)
	}
	byPath := map[string]bool{}
	for _, r := range got.Results {
		byPath[r.Path] = r.OK
		if r.Path == "/bad" && r.Error == "" {
			t.Error("failed rule should carry an error message")
		}
	}
	if !byPath["/good"] || byPath["/bad"] {
		t.Errorf("byPath = %+v, want /good=true /bad=false", byPath)
	}
	if rules := e.lat.Rules(); len(rules) != 1 {
		t.Fatalf("LatencyStore rules = %+v, want 1 (only the successful rule applied)", rules)
	}
}

func TestLatencyApplyUnknownProfile(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	e := newDatadogTestEnv(t, cfg, server.Deps{Datadog: &fakeDatadogClient{}})

	resp, body := e.post(t, "/api/latency/apply", map[string]any{"profile": "ghost"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}

var errNoData = errors.New("no data points in window")
