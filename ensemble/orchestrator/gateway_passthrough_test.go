package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// A gateway has no process, so BoundAt — when its current listener was bound — is the only
// uptime it has. The dashboard's Services tab reads it exactly the way it reads a service's
// StartedAt.
func TestOrchestratorGatewaysReportsBindTime(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	before := time.Now()
	o, _ := newGatewayPassthroughOrchestrator(t, local, upstream, "qa")

	bound := o.Gateways()[0].BoundAt
	if bound.IsZero() {
		t.Fatal("want a BoundAt for a gateway bound at Up, got the zero time")
	}
	if bound.Before(before) || bound.After(time.Now()) {
		t.Fatalf("BoundAt %v outside the window [%v, now] Up ran in", bound, before)
	}

	// A flip closes the old listener and binds a new one, so the uptime the dashboard shows
	// is that NEW listener's — reporting the pre-flip time would claim an unbroken bind
	// that did not happen.
	time.Sleep(10 * time.Millisecond)
	if err := o.FlipGateway(context.Background(), "public", "qa"); err != nil {
		t.Fatalf("flip: %v", err)
	}
	reboundAt := o.Gateways()[0].BoundAt
	if !reboundAt.After(bound) {
		t.Fatalf("want BoundAt to advance on a flip (rebind), got %v then %v", bound, reboundAt)
	}
}

// The fail-closed half: a gateway that is configured but never bound reports the zero time,
// which every client renders as "no uptime". Anything else would claim an unbound listener
// has been up since the epoch.
func TestOrchestratorGatewaysBindTimeZeroBeforeUp(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Gateways: map[string]config.Gateway{
			"public": {Port: freePort(t), Routes: []config.GatewayRoute{}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	t.Cleanup(px.Close)
	o := New(cfg, px, Opts{LogDir: t.TempDir()})

	statuses := o.Gateways()
	if len(statuses) != 1 {
		t.Fatalf("want 1 gateway status, got %+v", statuses)
	}
	if !statuses[0].BoundAt.IsZero() {
		t.Fatalf("want the zero time for a never-bound gateway, got %v", statuses[0].BoundAt)
	}
}

// Tearing a gateway's listener down clears its bind time rather than leaving the last one
// behind. Reconcile unwires before rebinding, so a rebind that FAILS must leave the gateway
// reporting no uptime — a stale time here would report a listener as up for hours when
// nothing is bound at all.
func TestOrchestratorGatewayBindTimeClearedOnUnwire(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	o, _ := newGatewayPassthroughOrchestrator(t, local, upstream, "qa")

	if o.Gateways()[0].BoundAt.IsZero() {
		t.Fatal("precondition: want a bound gateway before unwiring")
	}
	o.unwireGateway("public")
	if got := o.Gateways()[0].BoundAt; !got.IsZero() {
		t.Fatalf("want the zero time after unwiring, got %v", got)
	}
}
