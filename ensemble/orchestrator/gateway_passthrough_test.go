package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

// newGatewayPassthroughOrchestrator builds an Orchestrator with one gateway
// ("public") routing "/a" to a local backend and declaring one upstream
// (upstreamName) pointing at a second backend — the fixture every test in
// this file flips between.
func newGatewayPassthroughOrchestrator(t *testing.T, local, upstream *httptest.Server, upstreamName string) (*Orchestrator, int) {
	t.Helper()
	gwPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", Port: portOf(t, local)},
		},
		Gateways: map[string]config.Gateway{
			"public": {
				Port:   gwPort,
				Routes: []config.GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []config.GatewayUpstream{
					{Name: upstreamName, URL: upstream.URL},
				},
			},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	t.Cleanup(px.Close)
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(func() { _ = o.Down() })
	return o, gwPort
}

func getBody(t *testing.T, gwPort int, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", gwPort, path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestFlipGatewayRoundTripsLocalToUpstreamToLocal(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "local-ok")
	}))
	defer local.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "qa-ok")
	}))
	defer upstream.Close()

	o, gwPort := newGatewayPassthroughOrchestrator(t, local, upstream, "qa")

	if _, got := getBody(t, gwPort, "/a/x"); got != "local-ok" {
		t.Fatalf("before flip: want local-ok, got %q", got)
	}

	if err := o.FlipGateway(context.Background(), "public", "qa"); err != nil {
		t.Fatalf("FlipGateway to qa: %v", err)
	}
	// The upstream is a pure passthrough — the request forwards verbatim,
	// including the un-rewritten /a prefix the local route would have
	// stripped/matched; the fixture upstream ignores the path entirely,
	// so this also proves no route matching happened.
	if _, got := getBody(t, gwPort, "/a/x"); got != "qa-ok" {
		t.Fatalf("after flip to qa: want qa-ok, got %q", got)
	}

	if err := o.FlipGateway(context.Background(), "public", "local"); err != nil {
		t.Fatalf("FlipGateway to local: %v", err)
	}
	if _, got := getBody(t, gwPort, "/a/x"); got != "local-ok" {
		t.Fatalf("after flip back to local: want local-ok, got %q", got)
	}
}

func TestFlipGatewayUndeclaredUpstreamErrors(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	o, _ := newGatewayPassthroughOrchestrator(t, local, upstream, "qa")

	err := o.FlipGateway(context.Background(), "public", "sandbox")
	if err == nil || !strings.Contains(err.Error(), `no upstream "sandbox"`) {
		t.Fatalf("want no-upstream error, got %v", err)
	}
}

func TestFlipGatewayUnknownGatewayErrors(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	o, _ := newGatewayPassthroughOrchestrator(t, local, upstream, "qa")

	err := o.FlipGateway(context.Background(), "nope", "local")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestFlipGatewayPassthroughReadOnlyByDefault(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	o, gwPort := newGatewayPassthroughOrchestrator(t, local, upstream, "qa")

	if err := o.FlipGateway(context.Background(), "public", "qa"); err != nil {
		t.Fatalf("flip: %v", err)
	}

	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/a/x", gwPort), "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502 (read-only rail), got %d", resp.StatusCode)
	}
}

func TestOrchestratorGatewaysReportsActiveTarget(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	o, _ := newGatewayPassthroughOrchestrator(t, local, upstream, "qa")

	statuses := o.Gateways()
	if len(statuses) != 1 || statuses[0].Name != "public" || statuses[0].ActiveTarget != "local" {
		t.Fatalf("want [{public local}], got %+v", statuses)
	}

	if err := o.FlipGateway(context.Background(), "public", "qa"); err != nil {
		t.Fatalf("flip: %v", err)
	}
	statuses = o.Gateways()
	if statuses[0].ActiveTarget != "qa" {
		t.Fatalf("want ActiveTarget qa after flip, got %q", statuses[0].ActiveTarget)
	}
}
