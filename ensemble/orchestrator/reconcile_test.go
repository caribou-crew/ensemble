package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

// TestReconcileGatewayPortChange is the feature's motivating example: a
// gateway's port changes in config, and Reconcile must close the old
// listener and bind the new one without requiring a full Down/Up cycle.
func TestReconcileGatewayPortChange(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer svc.Close()

	oldPort, newPort := freePort(t), freePort(t)
	cfg := &config.Config{
		Dir:      t.TempDir(),
		Services: map[string]config.Service{"svc": {Run: "sleep 30", Port: portOf(t, svc)}},
		Gateways: map[string]config.Gateway{
			"public": {Port: oldPort, Routes: []config.GatewayRoute{{Prefix: "/", Service: "svc"}}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	get := func(port int) (int, error) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/x", port))
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}

	if status, err := get(oldPort); err != nil || status != 200 {
		t.Fatalf("old port before reconcile: status=%d err=%v", status, err)
	}

	newCfg := *cfg
	newCfg.Gateways = map[string]config.Gateway{
		"public": {Port: newPort, Routes: []config.GatewayRoute{{Prefix: "/", Service: "svc"}}},
	}
	result, err := o.Reconcile(context.Background(), newCfg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if _, err := get(newPort); err != nil {
		t.Fatalf("new port after reconcile: %v", err)
	}

	// The old port must stop accepting — poll briefly since the listener
	// closes asynchronously (net/http.Server.Close doesn't block on the
	// accept loop actually exiting).
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", oldPort), 100*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("old port %d still accepting connections after reconcile", oldPort)
		}
		time.Sleep(20 * time.Millisecond)
	}

	found := false
	for _, a := range result.Actions {
		if a.Kind == "gateway" && a.Name == "public" {
			found = true
			if a.Action != "rebound" {
				t.Errorf("gateway public action = %q, want rebound", a.Action)
			}
		}
	}
	if !found {
		t.Error("no gateway action reported for public")
	}
}

// actionFor finds the action reported for (kind, name), failing the test if
// none was reported.
func actionFor(t *testing.T, result *ReconcileResult, kind, name string) string {
	t.Helper()
	for _, a := range result.Actions {
		if a.Kind == kind && a.Name == name {
			return a.Action
		}
	}
	t.Fatalf("no %s action reported for %q (got %+v)", kind, name, result.Actions)
	return ""
}

// TestReconcileAddedService: a service present only in newCfg is started.
func TestReconcileAddedService(t *testing.T) {
	cfg := &config.Config{
		Dir:      t.TempDir(),
		Services: map[string]config.Service{"a": {Run: "sleep 30"}},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	newCfg := *cfg
	newCfg.Services = map[string]config.Service{
		"a": cfg.Services["a"],
		"b": {Run: "sleep 30"},
	}
	result, err := o.Reconcile(context.Background(), newCfg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := actionFor(t, result, "service", "b"); got != "started" {
		t.Errorf("service b action = %q, want started", got)
	}
	if !o.running("b") {
		t.Error("service b must be running after reconcile")
	}
}

// TestReconcileRemovedService: a service present only in oldCfg is stopped.
func TestReconcileRemovedService(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"a": {Run: "sleep 30"},
			"b": {Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()
	if !o.running("b") {
		t.Fatal("service b must be running before reconcile")
	}

	newCfg := *cfg
	newCfg.Services = map[string]config.Service{"a": cfg.Services["a"]}
	result, err := o.Reconcile(context.Background(), newCfg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := actionFor(t, result, "service", "b"); got != "stopped" {
		t.Errorf("service b action = %q, want stopped", got)
	}
	if o.running("b") {
		t.Error("service b must not be running after reconcile")
	}
}

// TestReconcileChangedServiceRestarts: a service whose config block differs
// (an env var, here) is stopped and restarted with a new PID; an unrelated,
// unchanged service is left completely alone.
func TestReconcileChangedServiceRestarts(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"a": {Run: "sleep 30", Env: map[string]string{"X": "1"}},
			"b": {Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	stateA, _ := o.Service("a")
	stateB, _ := o.Service("b")
	oldPIDA, oldPIDB := stateA.PID, stateB.PID

	newCfg := *cfg
	newCfg.Services = map[string]config.Service{
		"a": {Run: "sleep 30", Env: map[string]string{"X": "2"}},
		"b": cfg.Services["b"],
	}
	result, err := o.Reconcile(context.Background(), newCfg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := actionFor(t, result, "service", "a"); got != "restarted" {
		t.Errorf("service a action = %q, want restarted", got)
	}
	if got := actionFor(t, result, "service", "b"); got != "unchanged" {
		t.Errorf("service b action = %q, want unchanged", got)
	}

	newStateA, _ := o.Service("a")
	newStateB, _ := o.Service("b")
	if newStateA.PID == oldPIDA {
		t.Errorf("service a PID unchanged (%d) — expected a real restart", oldPIDA)
	}
	if newStateB.PID != oldPIDB {
		t.Errorf("service b PID changed (%d -> %d) — unchanged service must not restart", oldPIDB, newStateB.PID)
	}
}

// TestReconcileGlobalOnlyChangeTouchesNothing: a redact-list-only change
// reconciles with zero service/gateway actions — see the CORS-preflight
// visibility work this session also shipped, which is exactly the kind of
// config edit this must NOT restart 30 services over.
func TestReconcileGlobalOnlyChangeTouchesNothing(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer svc.Close()

	gwPort := freePort(t)
	cfg := &config.Config{
		Dir:      t.TempDir(),
		Services: map[string]config.Service{"svc": {Run: "sleep 30", Port: portOf(t, svc)}},
		Gateways: map[string]config.Gateway{
			"public": {Port: gwPort, Routes: []config.GatewayRoute{{Prefix: "/", Service: "svc"}}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	o.Rec = rec
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	stateBefore, _ := o.Service("svc")

	newCfg := *cfg
	newCfg.Redact = []string{"x-my-secret"}
	result, err := o.Reconcile(context.Background(), newCfg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := actionFor(t, result, "global", "redact"); got != "updated" {
		t.Errorf("global redact action = %q, want updated", got)
	}
	for _, a := range result.Actions {
		if a.Kind == "service" || a.Kind == "gateway" {
			if a.Action != "unchanged" {
				t.Errorf("unexpected %s action on %q: %q (redact-only change must touch no service/gateway)", a.Kind, a.Name, a.Action)
			}
		}
	}
	stateAfter, _ := o.Service("svc")
	if stateAfter.PID != stateBefore.PID {
		t.Errorf("service svc PID changed (%d -> %d) on a redact-only reconcile", stateBefore.PID, stateAfter.PID)
	}
}

// TestUpStartsAndDownStopsStub is a regression test for moving stub
// ownership from cmd_up.go into the Orchestrator (needed so Reconcile can
// add/remove/restart a stub by name, the same as a service): Up must still
// actually start a configured stub and make it answer, and Down must still
// close it.
func TestUpStartsAndDownStopsStub(t *testing.T) {
	stubPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Stubs: map[string]config.Stub{
			"payments": {Port: stubPort, Routes: []config.StubRoute{
				{Match: config.StubMatch{Path: "/charge"}, Respond: config.StubRespond{Status: 200, Body: "ok"}},
			}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	o.Rec = rec
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/charge", stubPort))
	if err != nil {
		t.Fatalf("GET stub: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("stub response = %d %q, want 200 \"ok\"", resp.StatusCode, body)
	}

	if err := o.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", stubPort), 500*time.Millisecond); err == nil {
		t.Error("stub port still accepting connections after Down")
	}
}

// TestReconcileStubChanged: a stub whose config block differs (its canned
// response, here) is restarted and answers with the new response.
func TestReconcileStubChanged(t *testing.T) {
	stubPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Stubs: map[string]config.Stub{
			"payments": {Port: stubPort, Routes: []config.StubRoute{
				{Match: config.StubMatch{Path: "/charge"}, Respond: config.StubRespond{Status: 200, Body: "v1"}},
			}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	o.Rec = rec
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	newCfg := *cfg
	newCfg.Stubs = map[string]config.Stub{
		"payments": {Port: stubPort, Routes: []config.StubRoute{
			{Match: config.StubMatch{Path: "/charge"}, Respond: config.StubRespond{Status: 200, Body: "v2"}},
		}},
	}
	result, err := o.Reconcile(context.Background(), newCfg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := actionFor(t, result, "stub", "payments"); got != "restarted" {
		t.Errorf("stub payments action = %q, want restarted", got)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/charge", stubPort))
	if err != nil {
		t.Fatalf("GET stub: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "v2" {
		t.Errorf("stub response after reconcile = %q, want \"v2\"", body)
	}
}

// TestReconcileAppliesClientIdentityHeadersLive pins the hot-reload path for
// client_identity_headers. The proxy holds this list rather than re-reading
// cfg per request, so without an explicit reconcile case an edit to
// ensemble.yaml appears to take effect (the file changed, `ensemble reload`
// reported success) and silently does not — the parsed-but-unapplied shape of
// the same defect the lead ruled on for flows.<name>.command.
func TestReconcileAppliesClientIdentityHeadersLive(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	px := proxy.New(rec)
	defer px.Close()
	px.ClientHeaders = []string{"x-old"}

	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	o.Rec = rec
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	newCfg := *cfg
	newCfg.ClientIdentityHeaders = []string{"x-new"}
	result, err := o.Reconcile(context.Background(), newCfg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := actionFor(t, result, "global", "client_identity_headers"); got != "updated" {
		t.Errorf("action = %q, want updated", got)
	}
	if len(px.ClientHeaders) != 1 || px.ClientHeaders[0] != "x-new" {
		t.Errorf("the live proxy still reads %v — the edit did not take effect", px.ClientHeaders)
	}
}

func TestApplyProxyGlobalsCopiesEverySetting(t *testing.T) {
	px := proxy.New(proxy.NewRecorder(proxy.RecorderOpts{Ring: 1}))
	defer px.Close()
	ApplyProxyGlobals(px, config.Config{
		TraceHeader:           "x-local-trace-id",
		SourceHeaders:         []string{"x-caller"},
		ClientIdentityHeaders: []string{"x-app-client"},
	})
	if px.TraceHeader != "x-local-trace-id" {
		t.Errorf("TraceHeader = %q", px.TraceHeader)
	}
	if len(px.SourceHeaders) != 1 || px.SourceHeaders[0] != "x-caller" {
		t.Errorf("SourceHeaders = %v", px.SourceHeaders)
	}
	if len(px.ClientHeaders) != 1 || px.ClientHeaders[0] != "x-app-client" {
		t.Errorf("ClientHeaders = %v", px.ClientHeaders)
	}
}

// TestReconcileGlobalsCoversEveryProxyGlobal is the guard that keeps the two
// halves honest. ApplyProxyGlobals runs at startup and reconcileGlobals runs
// on reload; a setting added to one and forgotten in the other is a config
// key that works on a cold start and not after a reload, or the reverse —
// from the user's side, indistinguishable from a typo.
//
// It asserts by BEHAVIOUR rather than by reading source: apply a config with
// every proxy global set to one value, reconcile to a config with every one
// set to another, and require the live proxy to end up carrying the second.
func TestReconcileGlobalsCoversEveryProxyGlobal(t *testing.T) {
	before := config.Config{
		Dir:                   t.TempDir(),
		TraceHeader:           "x-old-trace",
		SourceHeaders:         []string{"x-old-caller"},
		ClientIdentityHeaders: []string{"x-old-client"},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	px := proxy.New(rec)
	defer px.Close()
	ApplyProxyGlobals(px, before)

	cfg := before
	o := New(&cfg, px, Opts{LogDir: t.TempDir()})
	o.Rec = rec
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	after := before
	after.TraceHeader = "x-new-trace"
	after.SourceHeaders = []string{"x-new-caller"}
	after.ClientIdentityHeaders = []string{"x-new-client"}
	if _, err := o.Reconcile(context.Background(), after); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Every global ApplyProxyGlobals knows how to set must also be
	// reachable by a reload.
	want := proxy.New(proxy.NewRecorder(proxy.RecorderOpts{Ring: 1}))
	defer want.Close()
	ApplyProxyGlobals(want, after)
	if px.TraceHeader != want.TraceHeader {
		t.Errorf("trace_header did not reconcile: %q, want %q", px.TraceHeader, want.TraceHeader)
	}
	if !reflect.DeepEqual(px.SourceHeaders, want.SourceHeaders) {
		t.Errorf("source_header did not reconcile: %v, want %v", px.SourceHeaders, want.SourceHeaders)
	}
	if !reflect.DeepEqual(px.ClientHeaders, want.ClientHeaders) {
		t.Errorf("client_identity_headers did not reconcile: %v, want %v", px.ClientHeaders, want.ClientHeaders)
	}
}
