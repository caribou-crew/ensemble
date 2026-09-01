package orchestrator

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// wiringListener binds and holds open a real TCP listener on port,
// simulating a live backend process without needing the orchestrator's own
// `run:` command to bind anything — same "hand the number to something
// else that binds it" trade-off freePort's own doc comment already
// accepts for proxy ports; here it lets `sleep 30` stand in for a service
// while still passing the health gate's TCP dial (see gateHealth).
func wiringListener(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listen on %d: %v", port, err)
	}
	t.Cleanup(func() { ln.Close() })
}

// TestUpComputesWiringWarnings covers task 3.2's Up hook: a service whose
// env: references another service's real port (not its proxy: port) shows
// up in Orchestrator.WiringWarnings() as soon as Up returns.
func TestUpComputesWiringWarnings(t *testing.T) {
	catalogPort := freePort(t)
	catalogProxy := freePort(t)
	edgePort := freePort(t)
	edgeProxy := freePort(t)
	wiringListener(t, catalogPort)
	wiringListener(t, edgePort)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"catalog": {Run: "sleep 30", Port: catalogPort, Proxy: catalogProxy},
			"edge": {
				Run: "sleep 30", Port: edgePort, Proxy: edgeProxy,
				Env: map[string]string{"CATALOG_URL": fmt.Sprintf("http://127.0.0.1:%d", catalogPort)},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	warnings := o.WiringWarnings()
	if len(warnings) != 1 {
		t.Fatalf("expected 1 wiring warning after Up, got %+v", warnings)
	}
	if w := warnings[0]; w.Service != "edge" || w.Target != "catalog" || w.Port != catalogPort || w.ProxyPort != catalogProxy {
		t.Errorf("unexpected warning: %+v", w)
	}
}

// TestUpNoWiringWarningsWhenProxyWired is the clean counterpart: the same
// topology, wired through catalog's proxy: port, must report zero
// warnings — the "stack still starts, nothing to flag" path.
func TestUpNoWiringWarningsWhenProxyWired(t *testing.T) {
	catalogPort := freePort(t)
	catalogProxy := freePort(t)
	edgePort := freePort(t)
	wiringListener(t, catalogPort)
	wiringListener(t, edgePort)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"catalog": {Run: "sleep 30", Port: catalogPort, Proxy: catalogProxy},
			"edge": {
				Run: "sleep 30", Port: edgePort,
				Env: map[string]string{"CATALOG_URL": fmt.Sprintf("http://127.0.0.1:%d", catalogProxy)},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	if warnings := o.WiringWarnings(); len(warnings) != 0 {
		t.Fatalf("expected no wiring warnings when wired through the proxy port, got %+v", warnings)
	}
}

// TestSetVariantRecomputesWiringWarnings covers task 3.2's SetVariant hook
// and the spec's "only one variant mis-wired" scenario: the warning
// appears only while the mis-wired variant is the one actually running,
// and clears again on switching back.
func TestSetVariantRecomputesWiringWarnings(t *testing.T) {
	catalogPort := freePort(t)
	catalogProxy := freePort(t)
	edgePort := freePort(t)
	wiringListener(t, catalogPort)
	wiringListener(t, edgePort)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"catalog": {Run: "sleep 30", Port: catalogPort, Proxy: catalogProxy},
			"edge": {
				Port: edgePort, Default: "stub",
				Variants: map[string]config.Variant{
					"stub": {Run: "sleep 30", Env: map[string]string{"CATALOG_URL": fmt.Sprintf("http://127.0.0.1:%d", catalogProxy)}},
					"real": {Run: "sleep 30", Env: map[string]string{"CATALOG_URL": fmt.Sprintf("http://127.0.0.1:%d", catalogPort)}},
				},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	if warnings := o.WiringWarnings(); len(warnings) != 0 {
		t.Fatalf("expected no warnings with variant %q active, got %+v", "stub", warnings)
	}

	if err := o.SetVariant(context.Background(), "edge", "real"); err != nil {
		t.Fatalf("SetVariant real: %v", err)
	}
	warnings := o.WiringWarnings()
	if len(warnings) != 1 || warnings[0].Variant != "real" || warnings[0].Service != "edge" {
		t.Fatalf("expected 1 warning with variant %q active, got %+v", "real", warnings)
	}

	if err := o.SetVariant(context.Background(), "edge", "stub"); err != nil {
		t.Fatalf("SetVariant stub: %v", err)
	}
	if warnings := o.WiringWarnings(); len(warnings) != 0 {
		t.Fatalf("expected the warning to clear after switching back to %q, got %+v", "stub", warnings)
	}
}
