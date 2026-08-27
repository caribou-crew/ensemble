package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

// freePort finds a currently-unused TCP port on 127.0.0.1 by briefly
// binding then releasing it.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func newTestOrchestrator(t *testing.T, cfg *config.Config, opts Opts) *Orchestrator {
	t.Helper()
	if opts.LogDir == "" {
		opts.LogDir = t.TempDir()
	}
	px := proxy.New(proxy.NewRecorder(proxy.RecorderOpts{Ring: 64}))
	return New(cfg, px, opts)
}

// Test 1: topological order. a depends on b depends on c; Up must start
// them c, b, a.
func TestUpTopologicalOrder(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"a": {Run: "sleep 30", DependsOn: []string{"b"}},
			"b": {Run: "sleep 30", DependsOn: []string{"c"}},
			"c": {Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	var order []string
	o.testStartHook = func(name string) { order = append(order, name) }

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	want := []string{"c", "b", "a"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("start order = %v, want %v", order, want)
	}
}

// Test 2: a cycle a<->b must be reported, naming both services.
func TestUpCycleDetection(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"a": {Run: "sleep 30", DependsOn: []string{"b"}},
			"b": {Run: "sleep 30", DependsOn: []string{"a"}},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	err := o.Up(context.Background())
	if err == nil {
		t.Fatal("expected a cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("cycle error doesn't name both services: %v", err)
	}
}

// Test 3: a health path with nothing listening must fail within the
// configured HealthTimeout, and mark the service state failed.
func TestUpHealthGateFailure(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"bff": {Run: "sleep 30", Port: freePort(t), Health: "/healthz"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 500 * time.Millisecond})

	start := time.Now()
	err := o.Up(context.Background())
	elapsed := time.Since(start)
	defer o.Down()

	if err == nil {
		t.Fatal("expected a health gate error, got nil")
	}
	if !strings.Contains(err.Error(), "bff") {
		t.Fatalf("error doesn't name the service: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("health gate took too long: %v (timeout override not honored?)", elapsed)
	}

	st, ok := o.Service("bff")
	if !ok {
		t.Fatal("expected a state for bff")
	}
	if st.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", st.Status, StatusFailed)
	}
}

// TestUpContinuesPastFailedNodeAndSkipsDependents pins the "one bad service
// shouldn't take the whole stack down" fix: a failed node must not abort
// the rest of Up. An unrelated, independent node still comes up healthy,
// and a node that depends on the failed one is marked failed and skipped
// — never actually started, not just health-gated and given up on — so it
// doesn't sit out its own timeout only to fail for the same reason.
func TestUpContinuesPastFailedNodeAndSkipsDependents(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"bad":       {Run: "sleep 30", Port: freePort(t), Health: "/healthz"},
			"good":      {Run: "sleep 30"},
			"dependent": {Run: "sleep 30", DependsOn: []string{"bad"}},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 200 * time.Millisecond})

	err := o.Up(context.Background())
	defer o.Down()

	if err == nil {
		t.Fatal("expected Up to return a non-nil joined error naming the failed/skipped nodes")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("Up error doesn't name the failed service: %v", err)
	}

	badSt, ok := o.Service("bad")
	if !ok || badSt.Status != StatusFailed {
		t.Fatalf("bad state = %+v, ok=%v, want StatusFailed", badSt, ok)
	}

	goodSt, ok := o.Service("good")
	if !ok || goodSt.Status != StatusHealthy {
		t.Fatalf("good state = %+v, ok=%v, want StatusHealthy — an unrelated node must not be taken down by bad's failure", goodSt, ok)
	}

	depSt, ok := o.Service("dependent")
	if !ok {
		t.Fatal("expected a state for dependent")
	}
	if depSt.Status != StatusFailed {
		t.Fatalf("dependent status = %q, want %q (skipped because its dependency failed)", depSt.Status, StatusFailed)
	}
	if !strings.Contains(depSt.LastErr, "skipped") || !strings.Contains(depSt.LastErr, "bad") {
		t.Fatalf("dependent LastErr = %q, want it to explain it was skipped because bad failed", depSt.LastErr)
	}
	if depSt.PID != 0 {
		t.Fatalf("dependent PID = %d, want 0 — it must never actually be started", depSt.PID)
	}
}

// TestUpHealthGateHonorsPerServiceStartupTimeout pins config.Service.
// StartupTimeoutS: a service whose health endpoint takes longer to come up
// than the orchestrator-wide default (Opts.HealthTimeout) must still
// succeed when its own StartupTimeoutS covers that wait — e.g. a JVM
// service paying a slow classloading/Spring-context cost on every boot,
// without raising the default for every other, fast-starting service.
func TestUpHealthGateHonorsPerServiceStartupTimeout(t *testing.T) {
	// Bound ONCE and held, rather than freePort(t) followed by a re-listen on
	// the number it returned. freePort closes its listener before returning,
	// so that pattern leaves a window in which the kernel can hand the same
	// ephemeral port to anyone else — `go test ./...` runs packages in
	// parallel, and this test failed in CI on exactly that
	// ("bind: address already in use"). Every other freePort caller hands the
	// number to the orchestrator to bind and cannot avoid the window; this
	// one binds the port itself, so it has no reason to let go of it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Listener.Close()
	srv.Listener = ln
	defer srv.Close()

	// Simulates the slow starter: nothing answers on port until well past
	// the 200ms global default below, but comfortably inside the 2s
	// per-service override.
	go func() {
		time.Sleep(700 * time.Millisecond)
		srv.Start()
	}()

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"jvm": {Run: "sleep 30", Port: port, Health: "/healthz", StartupTimeoutS: 2},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 200 * time.Millisecond})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v (StartupTimeoutS=2s override should have covered the 700ms startup delay)", err)
	}
	defer o.Down()

	st, ok := o.Service("jvm")
	if !ok {
		t.Fatal("expected a state for jvm")
	}
	if st.Status != StatusHealthy {
		t.Fatalf("status = %q, want %q", st.Status, StatusHealthy)
	}
}

// Test 5: build-if-stale, driven through Restart. First Restart builds
// (stamp missing); a Restart with no watched-file changes skips the build;
// touching a watched file forces the next Restart to rebuild.
func TestRestartBuildIfStale(t *testing.T) {
	dir := t.TempDir()
	builtPath := filepath.Join(dir, "built")

	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"svc": {
				Dir:   ".",
				Build: "touch built",
				Watch: []string{"*.txt"},
				Run:   "true",
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	if err := o.Restart(context.Background(), "svc"); err != nil {
		t.Fatalf("first Restart: %v", err)
	}
	info1, err := os.Stat(builtPath)
	if err != nil {
		t.Fatalf("expected build to run: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if err := o.Restart(context.Background(), "svc"); err != nil {
		t.Fatalf("second Restart: %v", err)
	}
	info2, err := os.Stat(builtPath)
	if err != nil {
		t.Fatalf("built file disappeared: %v", err)
	}
	if !info2.ModTime().Equal(info1.ModTime()) {
		t.Fatalf("build re-ran with nothing changed: %v -> %v", info1.ModTime(), info2.ModTime())
	}

	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write watched file: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := o.Restart(context.Background(), "svc"); err != nil {
		t.Fatalf("third Restart: %v", err)
	}
	info3, err := os.Stat(builtPath)
	if err != nil {
		t.Fatalf("built file disappeared: %v", err)
	}
	if !info3.ModTime().After(info2.ModTime()) {
		t.Fatalf("build did not re-run after watched file changed: %v -> %v", info2.ModTime(), info3.ModTime())
	}
}

// Test 6: a service with Proxy set gets wired during Up; a GET through the
// intercept port reaches the upstream and lands a hop in the recorder.
func TestUpProxyWiring(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	upURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	upPort, err := strconv.Atoi(upURL.Port())
	if err != nil {
		t.Fatalf("upstream port: %v", err)
	}
	proxyPort := freePort(t)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", Port: upPort, Proxy: proxyPort},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	o := New(cfg, px, Opts{LogDir: t.TempDir()})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", proxyPort))
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "hello from upstream" {
		t.Fatalf("body = %q, want %q", body, "hello from upstream")
	}

	hops := rec.Snapshot()
	if len(hops) == 0 {
		t.Fatal("expected at least one hop in the recorder")
	}
	if hops[0].To != "svc" {
		t.Fatalf("hop.To = %q, want %q", hops[0].To, "svc")
	}
}

// TestUpProxyWiringDerivesCalledByFromDependsOn: wireProxy must pass each
// service's Config.CalledBy hint (derived here from depends_on, since
// "backend" declares no explicit called_by) into proxy.Target, so a hit
// with no traceparent — an un-instrumented backend's own behavior — still
// gets attributed instead of falling back to the synthetic "client" root.
func TestUpProxyWiringDerivesCalledByFromDependsOn(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	upURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	upPort, err := strconv.Atoi(upURL.Port())
	if err != nil {
		t.Fatalf("upstream port: %v", err)
	}
	proxyPort := freePort(t)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"backend": {Run: "sleep 30", Port: upPort, Proxy: proxyPort},
			"bff":     {Run: "sleep 30", DependsOn: []string{"backend"}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	o := New(cfg, px, Opts{LogDir: t.TempDir()})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", proxyPort))
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) == 0 {
		t.Fatal("expected at least one hop in the recorder")
	}
	if hops[0].From != "bff" {
		t.Fatalf("hop.From = %q, want %q", hops[0].From, "bff")
	}
	if hops[0].Attribution != "inferred" {
		t.Fatalf("hop.Attribution = %q, want %q", hops[0].Attribution, "inferred")
	}
}

// Restart must not re-wire the proxy: px.Serve binds a listener with no
// release mechanism, so a second bind on the same Listen address would
// fail with "address already in use". The original wiring from Up keeps
// forwarding to the service's (static) port across restarts.
func TestRestartDoesNotRewireProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	upURL, _ := url.Parse(upstream.URL)
	upPort, _ := strconv.Atoi(upURL.Port())
	proxyPort := freePort(t)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", Port: upPort, Proxy: proxyPort},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	if err := o.Restart(context.Background(), "svc"); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", proxyPort))
	if err != nil {
		t.Fatalf("GET through proxy after restart: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}

// Restart must record and abort, not paper over, a genuine failure to stop
// the previous instance — starting a replacement over a possibly-still-live
// predecessor on the same port would be worse than doing nothing. Injects
// the failure via the killGroup seam (a real EPERM/EACCES from the OS is
// not reliably provocable in a test).
func TestRestartAbortsOnKillFailure(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	before, ok := o.Service("svc")
	if !ok {
		t.Fatal("expected a state for svc after Up")
	}

	injected := errors.New("injected kill failure")
	o.killGroup = func(pid int, sig syscall.Signal) error { return injected }

	t.Cleanup(func() {
		// Restore the real implementation so cleanup actually kills the
		// still-running "sleep 30" process instead of re-triggering the
		// injected failure.
		o.killGroup = killProcessGroup
		o.Down()
	})

	err := o.Restart(context.Background(), "svc")
	if err == nil {
		t.Fatal("expected Restart to return an error")
	}
	if !strings.Contains(err.Error(), "svc") {
		t.Fatalf("error doesn't name the service: %v", err)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("error doesn't wrap the injected failure: %v", err)
	}

	after, ok := o.Service("svc")
	if !ok {
		t.Fatal("expected a state for svc after the failed restart")
	}
	if after.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", after.Status, StatusFailed)
	}
	if !strings.Contains(after.LastErr, "injected kill failure") {
		t.Fatalf("LastErr = %q, want it to mention the injected failure", after.LastErr)
	}
	// The strongest proof of "aborted, didn't start a replacement": the
	// PID is unchanged from right after Up.
	if after.PID != before.PID {
		t.Fatalf("PID changed from %d to %d — Restart started a replacement despite the kill failure", before.PID, after.PID)
	}
	if !processAlive(before.PID) {
		t.Fatalf("original process %d is gone — Restart's abort path must leave it tracked/running", before.PID)
	}
}

// Stop must kill the running process, mark the node StatusStopped, and
// leave it in the active profile set so a later Restart starts it again
// exactly like a first-time Up (Stop deliberately doesn't touch o.active).
func TestStop(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(func() { o.Down() })

	before, ok := o.Service("svc")
	if !ok || before.PID == 0 {
		t.Fatalf("expected a live PID for svc after Up, got %+v", before)
	}
	if !processAlive(before.PID) {
		t.Fatalf("process %d not alive right after Up", before.PID)
	}

	if err := o.Stop("svc"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	after, ok := o.Service("svc")
	if !ok {
		t.Fatal("expected a state for svc after Stop")
	}
	if after.Status != StatusStopped {
		t.Fatalf("status = %q, want %q", after.Status, StatusStopped)
	}
	// Stop mirrors Down's per-name teardown, which leaves the last-known
	// PID in state rather than zeroing it (Down does the same) — the
	// authoritative signal that the process is gone is StatusStopped plus
	// the OS no longer reporting the PID alive, not a zeroed field. The
	// kill signal is synchronous but reaping (proc.go's cmd.Wait goroutine)
	// isn't, so poll rather than asserting immediately.
	err := pollUntil(context.Background(), time.Second, func() (bool, error) {
		return !processAlive(before.PID), nil
	})
	if err != nil {
		t.Fatalf("process %d still alive after Stop: %v", before.PID, err)
	}

	// Restart after Stop must behave like a first-time start, not error
	// out because nothing is currently running.
	if err := o.Restart(context.Background(), "svc"); err != nil {
		t.Fatalf("Restart after Stop: %v", err)
	}
	revived, ok := o.Service("svc")
	if !ok || revived.Status != StatusHealthy {
		t.Fatalf("state after Restart-following-Stop = %+v", revived)
	}
	if !processAlive(revived.PID) {
		t.Fatalf("revived process %d not alive", revived.PID)
	}
}

// Stopping a service that isn't part of the active profile set is an
// error, not a silent no-op — there's nothing tracked to tear down and
// callers (the REST handler) need to surface that as a real failure.
func TestStopUnknownServiceErrors(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	err := o.Stop("nope")
	if err == nil {
		t.Fatal("expected an error stopping a service that was never started")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error doesn't name the service: %v", err)
	}
}

// Docker driver: exercised with a fake `docker` on PATH so CI needs no real
// docker install. Confirms run/inspect/rm shape out the documented CLI
// invocations and that the orchestrator's own state tracks them.
func TestDockerServiceLifecycle(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "docker-calls.log")
	writeFakeDocker(t, binDir, logPath)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"payments": {
				Docker: &config.DockerPlacement{
					Image: "payments:local",
					Ports: []string{"8010:8080"},
				},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	st, ok := o.Service("payments")
	if !ok || st.Status != StatusHealthy || st.Placement != "docker" {
		t.Fatalf("state = %+v (ok=%v), want healthy/docker", st, ok)
	}

	if err := o.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}

	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake docker call log: %v", err)
	}
	log := string(calls)
	if !strings.Contains(log, "run -d --name ensemble-payments") {
		t.Errorf("docker run not shaped as expected:\n%s", log)
	}
	if !strings.Contains(log, "inspect -f {{.State.Running}} ensemble-payments") {
		t.Errorf("docker inspect not shaped as expected:\n%s", log)
	}
	if !strings.Contains(log, "rm -f ensemble-payments") {
		t.Errorf("docker rm not shaped as expected:\n%s", log)
	}
}

// writeFakeDocker drops a `docker` shell script on PATH that logs its
// argv and answers `inspect -f {{.State.Running}}` with "true" so the
// no-health-path gate (process/container running check) is satisfied.
func writeFakeDocker(t *testing.T, dir, logPath string) {
	t.Helper()
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
case "$1" in
  inspect) echo true ;;
  run) echo fakecontainerid ;;
esac
exit 0
`
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
}
