package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// newGatewayEnv is newTestEnv plus a gateway "public" routing /svc to the
// proxied service and /pay to a stub, and a session manager that treats the
// gateway as an entry (as cmd_up does).
func newGatewayEnv(t *testing.T) (*testEnv, int) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from svc"))
	}))
	t.Cleanup(upstream.Close)
	upURL, _ := url.Parse(upstream.URL)
	upPort, _ := strconv.Atoi(upURL.Port())
	proxyPort, gwPort := freePort(t), freePort(t)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", Port: upPort, Proxy: proxyPort},
		},
		Stubs: map[string]config.Stub{
			"pay": {Port: freePort(t), Routes: []config.StubRoute{{Match: config.StubMatch{Path: "/charges"}}}},
		},
		Gateways: map[string]config.Gateway{
			"public": {Port: gwPort, Routes: []config.GatewayRoute{
				{Prefix: "/svc", Service: "svc", StripPrefix: true},
				{Prefix: "/svc/admin", Service: "svc"},
				{Prefix: "/pay", Service: "pay"},
			}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
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
	sessions := proxy.NewSessionManager(px, rec, []string{"public"})
	t.Cleanup(sessions.Close)
	handler := server.New(server.Deps{Cfg: cfg, Orch: orch, Rec: rec, Lat: lat, Sessions: sessions, Version: "test"})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &testEnv{ts: ts, upstream: upstream, rec: rec, lat: lat, sessions: sessions, orch: orch, cfg: cfg, proxyPort: proxyPort}, gwPort
}

func TestTopologyGatewayNodeAndEdges(t *testing.T) {
	e, _ := newGatewayEnv(t)
	resp, body := e.get(t, "/api/topology")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got server.TopologyResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	var gw *server.TopologyNode
	for i := range got.Nodes {
		if got.Nodes[i].Name == "public" {
			gw = &got.Nodes[i]
		}
	}
	if gw == nil || gw.Category != "gateway" || gw.Status != "static" || !gw.Entry {
		t.Fatalf("gateway node = %+v (nodes %+v)", gw, got.Nodes)
	}
	var fromGW []string
	for _, ed := range got.Edges {
		if ed.From == "public" {
			fromGW = append(fromGW, ed.To)
		}
	}
	// Two routes target svc; the edge is deduped.
	if strings.Join(fromGW, ",") != "pay,svc" {
		t.Fatalf("gateway edges = %v, want [pay svc]", fromGW)
	}
}

func TestSessionStartWithGatewayEntry(t *testing.T) {
	e, _ := newGatewayEnv(t)
	reqBody, _ := json.Marshal(map[string]string{"id": "gw1", "entry": "public"})
	resp, body := e.post(t, "/api/sessions", reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST sessions status = %d, body = %s", resp.StatusCode, body)
	}
	var start struct {
		EdgeAddr string `json:"edgeAddr"`
	}
	if err := json.Unmarshal(body, &start); err != nil || start.EdgeAddr == "" {
		t.Fatalf("start = %s (%v)", body, err)
	}
	hresp, err := http.Get("http://" + start.EdgeAddr + "/svc/items")
	if err != nil {
		t.Fatal(err)
	}
	hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("through session edge: status %d", hresp.StatusCode)
	}

	// Both the gateway hop and the downstream svc hop belong to the session.
	var hops []trace.Hop
	pollUntil(t, 2*time.Second, func() bool {
		_, body := e.get(t, "/api/sessions/gw1/hops")
		hops = hops[:0]
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			if line == "" {
				continue
			}
			var h trace.Hop
			if json.Unmarshal([]byte(line), &h) == nil {
				hops = append(hops, h)
			}
		}
		return len(hops) >= 2
	})
	var sawGW, sawSvc bool
	for _, h := range hops {
		switch h.To {
		case "public":
			sawGW = true
		case "svc":
			sawSvc = true
			if h.From != "public" || h.Path != "/items" {
				t.Errorf("svc hop = From %q Path %q, want From public Path /items", h.From, h.Path)
			}
		}
	}
	if !sawGW || !sawSvc {
		t.Fatalf("session hops missing gateway/svc: %+v", hops)
	}
	if v, _ := e.sessions.Get("gw1").Verdict(); v != trace.VerdictOK {
		t.Errorf("verdict = %v, want ok", v)
	}

	// Unknown entry still 404s.
	reqBody, _ = json.Marshal(map[string]string{"id": "gw2", "entry": "nope"})
	if resp, _ := e.post(t, "/api/sessions", reqBody); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown entry status = %d, want 404", resp.StatusCode)
	}
}
