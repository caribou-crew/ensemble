package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// testEnv wires a real orchestrator (one httptest-backed service, native
// placement via a long-lived dummy process) behind a real server.New
// handler, matching the brief's "real orchestrator on a tiny fake config"
// test setup.
type testEnv struct {
	ts        *httptest.Server
	upstream  *httptest.Server
	rec       *proxy.Recorder
	lat       *proxy.LatencyStore
	sessions  *proxy.SessionManager
	orch      *orchestrator.Orchestrator
	cfg       *config.Config
	proxyPort int
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("boom"))
			return
		}
		w.Write([]byte("hello from svc"))
	}))
	t.Cleanup(upstream.Close)

	upURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	upPort, err := strconv.Atoi(upURL.Port())
	if err != nil {
		t.Fatalf("upstream port: %v", err)
	}
	proxyPort := freePort(t)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", Port: upPort, Proxy: proxyPort, Entry: true},
		},
	}

	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 256})
	px := proxy.New(rec)
	lat := proxy.NewLatencyStore(func() float64 { return 0 })
	px.Latency = lat

	orch := orchestrator.New(cfg, px, orchestrator.Opts{LogDir: t.TempDir()})
	if err := orch.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(func() { orch.Down() })

	sessions := proxy.NewSessionManager(px, rec, []string{"svc"})
	t.Cleanup(sessions.Close)

	handler := server.New(server.Deps{
		Cfg: cfg, Orch: orch, Rec: rec, Lat: lat, Sessions: sessions, Version: "test",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &testEnv{ts: ts, upstream: upstream, rec: rec, lat: lat, sessions: sessions, orch: orch, cfg: cfg, proxyPort: proxyPort}
}

func (e *testEnv) do(t *testing.T, method, path string, body []byte) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reader)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s %s: %v", method, path, err)
	}
	return resp, respBody
}

func (e *testEnv) get(t *testing.T, path string) (*http.Response, []byte) {
	return e.do(t, http.MethodGet, path, nil)
}
func (e *testEnv) post(t *testing.T, path string, body []byte) (*http.Response, []byte) {
	return e.do(t, http.MethodPost, path, body)
}
func (e *testEnv) put(t *testing.T, path string, body []byte) (*http.Response, []byte) {
	return e.do(t, http.MethodPut, path, body)
}
func (e *testEnv) delete(t *testing.T, path string) (*http.Response, []byte) {
	return e.do(t, http.MethodDelete, path, nil)
}

// pollUntil retries fn until it returns true or timeout elapses, for
// assertions on state that updates asynchronously (e.g. SessionManager's
// subscription loop).
func pollUntil(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %v", timeout)
	}
}

func TestHealth(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/api/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["ok"] != true {
		t.Errorf("ok = %v, want true", got["ok"])
	}
	if got["version"] != "test" {
		t.Errorf("version = %v, want %q", got["version"], "test")
	}
}

// TestUIRootServesHTML proves the embedded dashboard is actually mounted:
// GET / must reach ui.Handler, not 404 via the mux's default behavior, and
// it must still be reachable through guard (same-origin request, no
// Origin header, Host = the httptest server's own loopback address).
func TestUIRootServesHTML(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(string(body), "<html") {
		t.Fatalf("GET / body doesn't look like HTML: %q", body)
	}
}

func TestStatusShape(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/api/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Services []orchestrator.ServiceState `json:"services"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "svc" {
		t.Fatalf("services = %+v", got.Services)
	}
	if got.Services[0].Status != orchestrator.StatusHealthy {
		t.Errorf("status = %q, want %q", got.Services[0].Status, orchestrator.StatusHealthy)
	}
	if got.Services[0].ProxyPort != e.proxyPort {
		t.Errorf("proxyPort = %d, want %d", got.Services[0].ProxyPort, e.proxyPort)
	}
}

func TestTopologyShape(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/api/topology")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got server.TopologyResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Name != "svc" || got.Nodes[0].Category != "service" || !got.Nodes[0].Entry {
		t.Fatalf("nodes = %+v", got.Nodes)
	}
	if got.Nodes[0].Status != string(orchestrator.StatusHealthy) {
		t.Errorf("node status = %q, want %q", got.Nodes[0].Status, orchestrator.StatusHealthy)
	}
}

// depends_on and env-wired proxy references must both surface as edges.
// This config is never Up'd — topology must work from static config plus
// whatever runtime state the orchestrator happens to have (none, here).
func TestTopologyEdgesFromDependsOnAndEnvWiring(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"bff": {Run: "sleep 30", Proxy: 19001, DependsOn: []string{"api"}, Entry: true},
			"api": {Run: "sleep 30", Proxy: 19002, Env: map[string]string{"BFF_URL": "http://127.0.0.1:19001/x"}},
		},
		Databases: map[string]config.Database{
			"db": {Image: "postgres:local", Port: 15432},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 16})
	px := proxy.New(rec)
	orch := orchestrator.New(cfg, px, orchestrator.Opts{LogDir: t.TempDir()})
	lat := proxy.NewLatencyStore(nil)
	sessions := proxy.NewSessionManager(px, rec, nil)
	t.Cleanup(sessions.Close)

	handler := server.New(server.Deps{Cfg: cfg, Orch: orch, Rec: rec, Lat: lat, Sessions: sessions, Version: "test"})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/topology")
	if err != nil {
		t.Fatalf("GET topology: %v", err)
	}
	defer resp.Body.Close()
	var got server.TopologyResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got.Nodes) != 3 {
		t.Fatalf("nodes = %+v, want 3", got.Nodes)
	}
	edges := map[string]bool{}
	for _, e := range got.Edges {
		edges[e.From+"->"+e.To] = true
	}
	if !edges["bff->api"] {
		t.Errorf("missing depends_on edge bff->api; got %v", got.Edges)
	}
	if !edges["api->bff"] {
		t.Errorf("missing env-wired edge api->bff; got %v", got.Edges)
	}
}

func TestLatencyCRUDRoundTrip(t *testing.T) {
	e := newTestEnv(t)

	rule := proxy.LatencyRule{Target: "svc", Path: "/", FixedMs: 50, Enabled: true}
	body, _ := json.Marshal(rule)
	resp, respBody := e.put(t, "/api/latency", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", resp.StatusCode, respBody)
	}

	// Drives the real LatencyStore, not just the HTTP response shape.
	rules := e.lat.Rules()
	if len(rules) != 1 || rules[0].Target != "svc" || rules[0].FixedMs != 50 {
		t.Fatalf("LatencyStore after PUT = %+v", rules)
	}

	resp, respBody = e.get(t, "/api/latency")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", resp.StatusCode, respBody)
	}
	var got struct {
		Rules []proxy.LatencyRule `json:"rules"`
	}
	if err := json.Unmarshal(respBody, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Rules) != 1 {
		t.Fatalf("GET rules = %+v", got.Rules)
	}

	resp, respBody = e.post(t, "/api/latency/arm-all", []byte(`{"enabled":false}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("arm-all status = %d, body = %s", resp.StatusCode, respBody)
	}
	if e.lat.Rules()[0].Enabled {
		t.Fatalf("arm-all(false) left rule enabled: %+v", e.lat.Rules())
	}

	resp, respBody = e.delete(t, "/api/latency?target=svc&path=%2F")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", resp.StatusCode, respBody)
	}
	if len(e.lat.Rules()) != 0 {
		t.Fatalf("rule not removed: %+v", e.lat.Rules())
	}

	e.lat.Set(proxy.LatencyRule{Target: "svc", Path: "/", Enabled: true})
	resp, respBody = e.post(t, "/api/latency/reset", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d, body = %s", resp.StatusCode, respBody)
	}
	if len(e.lat.Rules()) != 0 {
		t.Fatalf("reset left rules: %+v", e.lat.Rules())
	}
}

func TestTrafficReturnsRecordedHops(t *testing.T) {
	e := newTestEnv(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", e.proxyPort))
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	resp.Body.Close()

	_, body := e.get(t, "/api/traffic")
	var got struct {
		Hops []trace.Hop `json:"hops"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Hops) == 0 {
		t.Fatal("expected at least one recorded hop")
	}
	last := got.Hops[len(got.Hops)-1]
	if last.To != "svc" {
		t.Errorf("hop.To = %q, want %q", last.To, "svc")
	}

	// errorsOnly filter: hit /boom (500), then confirm the filtered view
	// contains it and excludes the earlier 200.
	resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/boom", e.proxyPort))
	if err != nil {
		t.Fatalf("GET /boom through proxy: %v", err)
	}
	resp.Body.Close()

	_, body = e.get(t, "/api/traffic?errorsOnly=true")
	got.Hops = nil
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Hops) == 0 {
		t.Fatal("errorsOnly returned no hops")
	}
	for _, h := range got.Hops {
		if h.Status < 400 && h.Err == "" {
			t.Errorf("errorsOnly leaked a non-error hop: %+v", h)
		}
	}
}

func TestTrafficStreamSSEReadsTwoEventsThenDisconnects(t *testing.T) {
	e := newTestEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.ts.URL+"/api/traffic/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	go func() {
		for i := 0; i < 2; i++ {
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", e.proxyPort))
			if err == nil {
				resp.Body.Close()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	reader := bufio.NewReader(resp.Body)
	events := 0
	deadline := time.Now().Add(5 * time.Second)
	for events < 2 && time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		if strings.HasPrefix(line, "event: hop") {
			events++
		}
	}
	cancel() // disconnect
	if events < 2 {
		t.Fatalf("got %d hop events, want >= 2", events)
	}
}

// TestTrafficSinceLimitPagesOldestFirstWithoutGaps guards final-review
// finding I9: with `since` set, GET /api/traffic?since=&limit= used to
// keep the newest `limit` of the filtered set (`out[len(out)-limit:]`),
// which breaks cursor paging — a client polling since=<lastSeq>&limit=100
// against a burst of 500 got hops 401-500, advanced its cursor to 500, and
// silently lost hops 1-400 forever. With `since` set the endpoint must
// return the OLDEST `limit` hops after since instead, so paging through
// with an advancing cursor covers the whole burst with no gaps or dupes.
func TestTrafficSinceLimitPagesOldestFirstWithoutGaps(t *testing.T) {
	e := newTestEnv(t)

	const total = 250
	const pageLimit = 37
	for i := 0; i < total; i++ {
		e.rec.Record(trace.Hop{To: "x"})
	}

	var all []trace.Hop
	since := uint64(0)
	for pages := 0; ; pages++ {
		if pages > total { // guard against an infinite loop if paging never progresses
			t.Fatalf("paging did not terminate: collected %d of %d so far", len(all), total)
		}
		_, body := e.get(t, fmt.Sprintf("/api/traffic?since=%d&limit=%d", since, pageLimit))
		var got struct {
			Hops []trace.Hop `json:"hops"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Hops) == 0 {
			break
		}
		if len(got.Hops) > pageLimit {
			t.Fatalf("page returned %d hops, want <= limit %d", len(got.Hops), pageLimit)
		}
		all = append(all, got.Hops...)
		since = got.Hops[len(got.Hops)-1].Seq
	}

	if len(all) != total {
		t.Fatalf("paged %d hops total, want %d (gaps or dupes in cursor paging)", len(all), total)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq != all[i-1].Seq+1 {
			t.Fatalf("gap between page results at index %d: seq %d -> %d", i, all[i-1].Seq, all[i].Seq)
		}
	}
}

// TestTrafficLimitWithoutSinceReturnsNewest pins the other half of I9's
// contract: with no `since`, `limit` alone still means "the most recent N"
// (a simple tail view), which is the right behavior for a client that
// isn't paging.
func TestTrafficLimitWithoutSinceReturnsNewest(t *testing.T) {
	e := newTestEnv(t)

	const total = 20
	const limit = 5
	var last trace.Hop
	for i := 0; i < total; i++ {
		last = e.rec.Record(trace.Hop{To: "x"})
	}

	_, body := e.get(t, fmt.Sprintf("/api/traffic?limit=%d", limit))
	var got struct {
		Hops []trace.Hop `json:"hops"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Hops) != limit {
		t.Fatalf("got %d hops, want %d", len(got.Hops), limit)
	}
	if newest := got.Hops[len(got.Hops)-1]; newest.Seq != last.Seq {
		t.Fatalf("last hop.Seq = %d, want the most recent (%d)", newest.Seq, last.Seq)
	}
}

func TestSessionsStartEndRoundTrip(t *testing.T) {
	e := newTestEnv(t)

	reqBody, _ := json.Marshal(map[string]string{"id": "sess1", "entry": "svc"})
	resp, body := e.post(t, "/api/sessions", reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST sessions status = %d, body = %s", resp.StatusCode, body)
	}
	var start struct {
		ID       string `json:"id"`
		EdgeAddr string `json:"edgeAddr"`
	}
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if start.ID != "sess1" || start.EdgeAddr == "" {
		t.Fatalf("start = %+v", start)
	}

	hresp, err := http.Get("http://" + start.EdgeAddr + "/")
	if err != nil {
		t.Fatalf("GET session edge: %v", err)
	}
	hresp.Body.Close()

	// The SessionManager attributes hops asynchronously off a Recorder
	// subscription — poll rather than assume it's landed by now.
	pollUntil(t, 2*time.Second, func() bool {
		_, body := e.get(t, "/api/sessions/sess1/hops")
		return len(strings.TrimSpace(string(body))) > 0
	})

	resp, body = e.get(t, "/api/sessions/sess1/hops")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET hops status = %d, body = %s", resp.StatusCode, body)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("expected NDJSON hop lines, got %q", body)
	}
	var h trace.Hop
	if err := json.Unmarshal([]byte(lines[0]), &h); err != nil {
		t.Fatalf("hop line not valid JSON: %v (%q)", err, lines[0])
	}

	resp, body = e.delete(t, "/api/sessions/sess1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", resp.StatusCode, body)
	}
	var end struct {
		ID      string   `json:"id"`
		Hops    int      `json:"hops"`
		Verdict string   `json:"verdict"`
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal(body, &end); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if end.ID != "sess1" || end.Hops == 0 {
		t.Fatalf("end = %+v", end)
	}

	// Ended session is no longer active.
	resp, _ = e.delete(t, "/api/sessions/sess1")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 404", resp.StatusCode)
	}
}

func TestTraceExportHAR(t *testing.T) {
	e := newTestEnv(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", e.proxyPort))
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	resp.Body.Close()

	hops := e.rec.Snapshot()
	if len(hops) == 0 {
		t.Fatal("no hops recorded")
	}
	traceID := hops[len(hops)-1].TraceID
	if traceID == "" {
		t.Fatal("hop has no trace id")
	}

	_, body := e.get(t, "/api/traces/"+traceID+"/export?format=har")
	var har trace.Har
	if err := json.Unmarshal(body, &har); err != nil {
		t.Fatalf("unmarshal HAR: %v (%s)", err, body)
	}
	if len(har.Log.Entries) == 0 {
		t.Fatalf("HAR has no entries: %s", body)
	}

	// Unknown format is a 4xx JSON error.
	resp2, body2 := e.get(t, "/api/traces/"+traceID+"/export?format=bogus")
	if resp2.StatusCode < 400 || resp2.StatusCode >= 500 {
		t.Fatalf("bogus format status = %d", resp2.StatusCode)
	}
	var errBody map[string]string
	if err := json.Unmarshal(body2, &errBody); err != nil || errBody["error"] == "" {
		t.Fatalf("expected {error:...} body, got %s", body2)
	}
}

// TestTraceExportRewritesHostToProxyPort guards the export-reproducibility
// fix: a hop's `to` is a logical service name, not a resolvable host, so
// exported requests must be rewritten to the intercept address the service
// actually listens on. Unknown "to" names (stubs, external hosts) fall back
// to the recorded Host header untouched.
func TestTraceExportRewritesHostToProxyPort(t *testing.T) {
	e := newTestEnv(t)

	traceID := "trace-export-rewrite"
	e.rec.Record(trace.Hop{
		TraceID: traceID, SpanID: "span-known", To: "svc",
		Method: "GET", Path: "/widgets", Status: 200,
		Req: trace.Payload{Headers: map[string]string{"host": "svc.internal:9999"}},
	})
	e.rec.Record(trace.Hop{
		TraceID: traceID, SpanID: "span-unknown", To: "some-stub",
		Method: "GET", Path: "/stub-path", Status: 200,
		Req: trace.Payload{Headers: map[string]string{"host": "stub.example:1234"}},
	})

	wantHost := fmt.Sprintf("127.0.0.1:%d", e.proxyPort)

	_, curlBody := e.get(t, "/api/traces/"+traceID+"/export?format=curl")
	curlOut := string(curlBody)
	if !strings.Contains(curlOut, "http://"+wantHost+"/widgets") {
		t.Fatalf("curl export did not rewrite known-service host: %s", curlOut)
	}
	if strings.Contains(curlOut, "svc.internal:9999") {
		t.Fatalf("curl export leaked unreachable host for known service: %s", curlOut)
	}
	if !strings.Contains(curlOut, "http://stub.example:1234/stub-path") {
		t.Fatalf("curl export must leave unknown service host untouched: %s", curlOut)
	}

	_, rawBody := e.get(t, "/api/traces/"+traceID+"/export?format=raw")
	rawOut := string(rawBody)
	if !strings.Contains(rawOut, "host: "+wantHost) {
		t.Fatalf("raw export did not rewrite known-service host: %s", rawOut)
	}
	if !strings.Contains(rawOut, "host: stub.example:1234") {
		t.Fatalf("raw export must leave unknown service host untouched: %s", rawOut)
	}

	_, harBody := e.get(t, "/api/traces/"+traceID+"/export?format=har")
	var har trace.Har
	if err := json.Unmarshal(harBody, &har); err != nil {
		t.Fatalf("unmarshal HAR: %v (%s)", err, harBody)
	}
	var sawKnown, sawUnknown bool
	for _, entry := range har.Log.Entries {
		switch entry.Request.URL {
		case "http://" + wantHost + "/widgets":
			sawKnown = true
		case "http://stub.example:1234/stub-path":
			sawUnknown = true
		}
	}
	if !sawKnown {
		t.Fatalf("HAR export did not rewrite known-service URL: %+v", har.Log.Entries)
	}
	if !sawUnknown {
		t.Fatalf("HAR export must leave unknown service URL untouched: %+v", har.Log.Entries)
	}
}

func TestTraceHopsAndLogicalView(t *testing.T) {
	e := newTestEnv(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", e.proxyPort))
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	resp.Body.Close()

	hops := e.rec.Snapshot()
	traceID := hops[len(hops)-1].TraceID

	_, body := e.get(t, "/api/traces/"+traceID)
	var got struct {
		Hops    []trace.Hop `json:"hops"`
		Logical []any       `json:"logical"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Hops) == 0 || len(got.Logical) == 0 {
		t.Fatalf("trace response = %s", body)
	}
}

// Every mutating endpoint records a control-plane annotation hop; PUT
// /api/latency is the brief's explicit test case.
func TestControlPlaneAnnotationOnPutLatency(t *testing.T) {
	e := newTestEnv(t)
	before := len(e.rec.Snapshot())

	body, _ := json.Marshal(proxy.LatencyRule{Target: "svc", Path: "/", FixedMs: 10, Enabled: true})
	resp, respBody := e.put(t, "/api/latency", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", resp.StatusCode, respBody)
	}

	hops := e.rec.Snapshot()
	if len(hops) != before+1 {
		t.Fatalf("hops after PUT = %d, want %d (snapshot: %+v)", len(hops), before+1, hops)
	}
	last := hops[len(hops)-1]
	if last.To != "ensemble-control" {
		t.Errorf("annotation.To = %q, want %q", last.To, "ensemble-control")
	}
	if last.Method != http.MethodPut {
		t.Errorf("annotation.Method = %q, want PUT", last.Method)
	}
	if last.Path != "/api/latency" {
		t.Errorf("annotation.Path = %q, want /api/latency", last.Path)
	}
	if last.Status != http.StatusOK {
		t.Errorf("annotation.Status = %d, want 200", last.Status)
	}
}

func TestServiceRestart(t *testing.T) {
	e := newTestEnv(t)

	resp, body := e.post(t, "/api/services/svc/restart", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var st orchestrator.ServiceState
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.Name != "svc" || st.Status != orchestrator.StatusHealthy {
		t.Fatalf("state = %+v", st)
	}
}

func TestServiceRestartUnknownIs404JSON(t *testing.T) {
	e := newTestEnv(t)

	resp, body := e.post(t, "/api/services/nope/restart", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["error"] == "" {
		t.Fatalf("expected non-empty error field, got %s", body)
	}
}

func TestServiceFlip(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "docker-calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"" + logPath + "\"\n" +
		"case \"$1\" in\n" +
		"  inspect) echo true ;;\n" +
		"  run) echo fakecontainerid ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {
				Run:    "sleep 30",
				Docker: &config.DockerPlacement{Image: "svc:local", Ports: []string{"18099:8080"}},
			},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 16})
	px := proxy.New(rec)
	orch := orchestrator.New(cfg, px, orchestrator.Opts{LogDir: t.TempDir()})
	if err := orch.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(func() { orch.Down() })
	lat := proxy.NewLatencyStore(nil)
	sessions := proxy.NewSessionManager(px, rec, nil)
	t.Cleanup(sessions.Close)

	handler := server.New(server.Deps{Cfg: cfg, Orch: orch, Rec: rec, Lat: lat, Sessions: sessions, Version: "test"})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/services/svc/flip", "application/json", nil)
	if err != nil {
		t.Fatalf("POST flip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var st orchestrator.ServiceState
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.Placement != "docker" {
		t.Fatalf("placement = %q, want docker", st.Placement)
	}
}

func TestSeedRunsHTTPSteps(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.Seeds = map[string]config.Seed{
		"basic": {HTTP: []config.SeedHTTP{{Method: http.MethodGet, URL: e.upstream.URL + "/"}}},
	}

	resp, body := e.post(t, "/api/seed/basic", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Results []orchestrator.SeedStepResult `json:"results"`
		OK      bool                          `json:"ok"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.OK || len(got.Results) != 1 || !got.Results[0].OK {
		t.Fatalf("seed result = %+v", got)
	}
}

func TestSeedUnknownIs404(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.post(t, "/api/seed/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}

func TestShutdownInvokesHookWhenConfigured(t *testing.T) {
	e := newTestEnv(t)

	called := make(chan struct{}, 1)
	handler := server.New(server.Deps{
		Cfg: e.cfg, Orch: e.orch, Rec: e.rec, Lat: e.lat, Sessions: e.sessions, Version: "test",
		Shutdown: func() { called <- struct{}{} },
	})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["ok"] != true {
		t.Errorf("ok = %v, want true", got["ok"])
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown hook was not invoked")
	}
}

func TestShutdownNotConfiguredIs501(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.post(t, "/api/shutdown", nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}

func TestOpenAPIListsEndpoints(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/api/openapi.json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.OpenAPI == "" {
		t.Fatal("missing openapi version")
	}
	for _, want := range []string{
		"/api/health", "/api/status", "/api/topology",
		"/api/services/{name}/restart", "/api/services/{name}/flip",
		"/api/seed/{name}", "/api/traffic", "/api/traffic/stream",
		"/api/traces/{traceId}", "/api/traces/{traceId}/export",
		"/api/latency", "/api/latency/arm-all", "/api/latency/reset",
		"/api/sessions", "/api/sessions/{id}", "/api/sessions/{id}/hops",
		"/api/openapi.json", "/api/shutdown",
	} {
		if _, ok := doc.Paths[want]; !ok {
			t.Errorf("openapi missing path %q", want)
		}
	}
}

// TestServeReturnsPromptlyOnShutdownWithAttachedSSEStream guards final-review
// finding I1: http.Server.Shutdown does not cancel in-flight request
// contexts on its own, so a handler blocked on r.Context().Done() (like
// handleTrafficStream) only returns when the *client* disconnects — and
// Shutdown then burns the full shutdownGrace and returns
// context.DeadlineExceeded. Serve must give stream handlers a context tied
// to its own ctx so they unblock immediately on shutdown, independent of the
// client, and a clean shutdown must return nil.
func TestServeReturnsPromptlyOnShutdownWithAttachedSSEStream(t *testing.T) {
	e := newTestEnv(t)
	handler := server.New(server.Deps{
		Cfg: e.cfg, Orch: e.orch, Rec: e.rec, Lat: e.lat, Sessions: e.sessions, Version: "test",
	})
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- server.Serve(ctx, addr, handler) }()

	var resp *http.Response
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/api/traffic/stream")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect to stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Do NOT close resp.Body before canceling: the fix must not depend on
	// client disconnect to unblock the handler.
	cancel()

	select {
	case serveErr := <-serveErrCh:
		if serveErr != nil {
			t.Fatalf("Serve returned %v, want nil", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s of ctx cancellation (shutdownGrace is 5s)")
	}
}
