package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// newTestServerWithGatewayUpstream builds a real server+orchestrator with
// one gateway ("public") declaring one upstream ("qa"), matching the
// inline-config pattern TestServiceFlipToPassthroughViaTargetBody already
// uses for the service case.
func newTestServerWithGatewayUpstream(t *testing.T) (ts *httptest.Server, gatewayName, upstreamName string) {
	t.Helper()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "local-ok")
	}))
	t.Cleanup(local.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "qa-ok")
	}))
	t.Cleanup(upstream.Close)

	localURL, err := url.Parse(local.URL)
	if err != nil {
		t.Fatalf("parse local url: %v", err)
	}
	localPort, err := strconv.Atoi(localURL.Port())
	if err != nil {
		t.Fatalf("local port: %v", err)
	}

	gwPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			// A real live backend, not just a free port — the health
			// gate polls it at Up, and an unhealthy port would hang the
			// gate for its full timeout rather than fail fast.
			"svc": {Run: "sleep 30", Port: localPort},
		},
		Gateways: map[string]config.Gateway{
			"public": {
				Port:   gwPort,
				Routes: []config.GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []config.GatewayUpstream{
					{Name: "qa", URL: upstream.URL},
				},
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
	ts = httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, "public", "qa"
}

func TestGatewayFlipViaRESTChangesActiveTarget(t *testing.T) {
	ts, gatewayName, upstreamName := newTestServerWithGatewayUpstream(t)

	resp, err := http.Post(ts.URL+"/api/gateways/"+gatewayName+"/flip", "application/json", strings.NewReader(`{"target":"`+upstreamName+`"}`))
	if err != nil {
		t.Fatalf("POST flip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	statusResp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer statusResp.Body.Close()
	var out struct {
		Gateways []orchestrator.GatewayStatus `json:"gateways"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&out); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	found := false
	for _, g := range out.Gateways {
		if g.Name == gatewayName {
			found = true
			if g.ActiveTarget != upstreamName {
				t.Fatalf("want ActiveTarget %q, got %q", upstreamName, g.ActiveTarget)
			}
		}
	}
	if !found {
		t.Fatalf("gateway %q missing from /api/status gateways: %+v", gatewayName, out.Gateways)
	}
}

// TestGatewayFlipUndeclaredTargetErrors matches handleServiceFlip's own
// convention: every FlipTo/FlipGateway failure (including an unknown
// target) surfaces as 500, not a distinguished 400 — there's no special
// casing for a "client's fault" vs "server's fault" flip error today, and
// this doesn't introduce one.
func TestGatewayFlipUndeclaredTargetErrors(t *testing.T) {
	ts, gatewayName, _ := newTestServerWithGatewayUpstream(t)
	resp, err := http.Post(ts.URL+"/api/gateways/"+gatewayName+"/flip", "application/json", strings.NewReader(`{"target":"nope"}`))
	if err != nil {
		t.Fatalf("POST flip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestGatewayFlipRequiresTargetBody(t *testing.T) {
	ts, gatewayName, _ := newTestServerWithGatewayUpstream(t)
	resp, err := http.Post(ts.URL+"/api/gateways/"+gatewayName+"/flip", "application/json", nil)
	if err != nil {
		t.Fatalf("POST flip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 (target required), got %d", resp.StatusCode)
	}
}

func TestGatewayFlipUnknownGateway404s(t *testing.T) {
	ts, _, _ := newTestServerWithGatewayUpstream(t)
	resp, err := http.Post(ts.URL+"/api/gateways/nope/flip", "application/json", strings.NewReader(`{"target":"local"}`))
	if err != nil {
		t.Fatalf("POST flip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestTopologyGatewayNodeIncludesUpstreamNames(t *testing.T) {
	ts, gatewayName, upstreamName := newTestServerWithGatewayUpstream(t)
	resp, err := http.Get(ts.URL + "/api/topology")
	if err != nil {
		t.Fatalf("GET topology: %v", err)
	}
	defer resp.Body.Close()
	var top server.TopologyResponse
	if err := json.NewDecoder(resp.Body).Decode(&top); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found *server.TopologyNode
	for i := range top.Nodes {
		if top.Nodes[i].Name == gatewayName {
			found = &top.Nodes[i]
		}
	}
	if found == nil {
		t.Fatalf("gateway node %q missing from topology", gatewayName)
	}
	got := false
	for _, u := range found.Upstreams {
		if u == upstreamName {
			got = true
		}
	}
	if !got {
		t.Fatalf("want %q in Upstreams, got %v", upstreamName, found.Upstreams)
	}
}

func TestHandleStatusIncludesGateways(t *testing.T) {
	ts, gatewayName, _ := newTestServerWithGatewayUpstream(t)
	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Gateways []orchestrator.GatewayStatus `json:"gateways"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, g := range out.Gateways {
		if g.Name == gatewayName {
			found = true
			if g.ActiveTarget != "local" {
				t.Fatalf("want local, got %q", g.ActiveTarget)
			}
		}
	}
	if !found {
		t.Fatalf("gateway %q missing from /api/status gateways", gatewayName)
	}
}
