package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/buildinfo"
	"github.com/caribou-crew/ensemble/core/proxy"
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

// standinBackendDelay is how long startStandinBackend waits, after runUp
// has been kicked off, before it actually binds a service's stand-in
// backend on the service's own configured port. runUp's preflight port
// check (checkPortsFree) runs synchronously, before anything else, right
// at the top of runUp — a handful of near-instant net.Listen/Close calls —
// so this only needs to outlast that, not the service's full startup.
// Generous on purpose: a slower CI box is more likely than a hang.
const standinBackendDelay = 100 * time.Millisecond

// startStandinBackend binds handler on 127.0.0.1:port and registers its
// cleanup, but only after standinBackendDelay — giving a concurrently
// running runUp's preflight port check time to see the port free before
// this test's stand-in backend (simulating "the real service", since the
// process ensemble itself spawns for these tests, "sleep 30", never binds
// anything) occupies it. Binding it any earlier would make runUp's own
// preflight check reject the port as already in use.
func startStandinBackend(t *testing.T, port int, handler http.Handler) {
	t.Helper()
	time.Sleep(standinBackendDelay)
	srv := httptest.NewUnstartedServer(handler)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listen on stand-in backend port %d: %v", port, err)
	}
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
}

// writeConfig writes a minimal ensemble.yaml declaring one native service
// ("svc") whose real port is upPort (a reserved-but-not-yet-bound port a
// caller will start a stand-in httptest backend on shortly after runUp
// begins — see startEnsemble) and whose intercept port is proxyPort — the
// same shape ensemble/server's own tests use (a long-lived dummy process
// just to give the orchestrator something to supervise; traffic actually
// flows to the httptest server once it starts listening on the same
// port).
func writeConfig(t *testing.T, dir string, upPort, proxyPort int) string {
	t.Helper()
	path := filepath.Join(dir, "ensemble.yaml")
	yaml := fmt.Sprintf(`
services:
  svc:
    run: "sleep 30"
    port: %d
    proxy: %d
    entry: true
`, upPort, proxyPort)
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// upTestEnv is a running `ensemble up` (via runUp, in-process) plus the
// httptest upstream and proxy port it fronts.
type upTestEnv struct {
	apiURL    string
	proxyPort int
	cancel    context.CancelFunc
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer

	result  chan error
	once    sync.Once
	waitErr error
}

// wait blocks until runUp returns, or fails the test after timeout. Safe to
// call more than once (e.g. once explicitly in a test and again from
// t.Cleanup): only the first call actually reads the result channel, later
// calls return the cached result immediately — runUp only ever sends once,
// so a second blocking read would hang until the timeout every time.
func (e *upTestEnv) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	e.once.Do(func() {
		select {
		case e.waitErr = <-e.result:
		case <-time.After(timeout):
			t.Fatal("runUp did not return in time")
		}
	})
	return e.waitErr
}

func startEnsemble(t *testing.T) *upTestEnv {
	t.Helper()

	upPort := freePort(t)
	proxyPort := freePort(t)
	apiPort := freePort(t)
	cfgPath := writeConfig(t, t.TempDir(), upPort, proxyPort)

	ctx, cancel := context.WithCancel(context.Background())
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	opts := upOptions{
		ConfigPath: cfgPath,
		Addr:       fmt.Sprintf("127.0.0.1:%d", apiPort),
	}

	result := make(chan error, 1)
	go func() { result <- runUp(ctx, opts, stdout, stderr) }()

	startStandinBackend(t, upPort, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from svc"))
	}))

	apiURL := "http://127.0.0.1:" + strconv.Itoa(apiPort)
	waitHealthy(t, apiURL)
	// The API now binds before orch.Up runs (services/databases come up
	// concurrently with it, not before it — see cmd_up.go), so a healthy
	// /api/health no longer implies "svc" has finished starting. Every
	// other test in this file assumes startEnsemble hands back a fully-up
	// stack, so wait for that explicitly here instead of pushing this
	// poll into each caller.
	waitServiceHealthy(t, NewClient(apiURL), "svc")

	env := &upTestEnv{apiURL: apiURL, proxyPort: proxyPort, cancel: cancel, result: result, stdout: stdout, stderr: stderr}
	t.Cleanup(func() {
		cancel()
		env.wait(t, 5*time.Second)
	})
	return env
}

func waitHealthy(t *testing.T, apiURL string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(apiURL + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never became healthy at %s", apiURL)
}

// waitServiceHealthy polls c.Status until name reports "healthy", or fails
// the test after 5s. Used where the API being reachable (waitHealthy) isn't
// enough on its own — orch.Up now runs concurrently with the API server, so
// a service can still be mid-startup, or absent from Status entirely, for a
// moment after /api/health first answers.
func waitServiceHealthy(t *testing.T, c *Client, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := c.Status(context.Background())
		if err == nil {
			for _, s := range st.Services {
				if s.Name == name && s.Status == "healthy" {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service %q never became healthy via %s", name, c.BaseURL)
}

// TestUp_ClientRoundTripAndSIGINTShutdown is the brief's core scenario: run
// `up` in-process, drive status/latency/traffic through the Client
// functions, then cancel the context (the SIGINT path) and confirm runUp
// returns cleanly.
func TestUp_ClientRoundTripAndSIGINTShutdown(t *testing.T) {
	env := startEnsemble(t)
	c := NewClient(env.apiURL)
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Services) != 1 || st.Services[0].Name != "svc" {
		t.Fatalf("status.Services = %+v", st.Services)
	}
	if st.Services[0].Status != "healthy" {
		t.Fatalf("status.Services[0].Status = %q, want healthy", st.Services[0].Status)
	}

	if _, err := c.LatencySet(ctx, proxy.LatencyRule{Target: "svc", Path: "/", FixedMs: 5, Enabled: true}); err != nil {
		t.Fatalf("latency set: %v", err)
	}
	ll, err := c.LatencyList(ctx)
	if err != nil {
		t.Fatalf("latency list: %v", err)
	}
	if len(ll.Rules) != 1 || ll.Rules[0].Target != "svc" || ll.Rules[0].FixedMs != 5 || !ll.Rules[0].Enabled {
		t.Fatalf("latency list rules = %+v", ll.Rules)
	}

	// Generate one hop of real traffic through the proxy so /api/traffic
	// has something to return.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", env.proxyPort))
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	resp.Body.Close()

	var tr TrafficResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tr, err = c.Traffic(ctx, 0, 0, false)
		if err != nil {
			t.Fatalf("traffic: %v", err)
		}
		if len(tr.Hops) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(tr.Hops) == 0 {
		t.Fatal("expected at least one recorded hop")
	}
	// The retained hops also include the "ensemble-control" annotation hop
	// PUT /api/latency recorded above (withAnnotation records every
	// mutating API call) — so assert a svc hop is *present*, not that it's
	// first.
	foundSvcHop := false
	for _, h := range tr.Hops {
		if h.To == "svc" {
			foundSvcHop = true
			break
		}
	}
	if !foundSvcHop {
		t.Fatalf("no hop with To=svc among recorded hops: %+v", tr.Hops)
	}

	// SIGINT path: cancel the context runUp was started with and confirm
	// it returns (Down completed, server drained) without error.
	env.cancel()
	if err := env.wait(t, 5*time.Second); err != nil {
		t.Fatalf("runUp returned error after context cancel: %v", err)
	}

	// final-review I2: runUp never called proxy.Close(), so the intercept
	// listener (env.proxyPort) outlived runUp's return. Confirm the port is
	// actually freed now — a fresh listener must be able to bind it.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", env.proxyPort))
	if err != nil {
		t.Fatalf("proxy intercept port %d still bound after runUp returned: %v", env.proxyPort, err)
	}
	ln.Close()
}

// findPID looks up the pid of a running process whose full command line
// contains marker, via `pgrep -f` (present on the ubuntu-latest CI runner
// and on macOS). Returns 0, false if none is found (already reaped, or
// never started).
func findPID(t *testing.T, marker string) (int, bool) {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", marker).Output()
	if err != nil {
		return 0, false // pgrep exits non-zero when nothing matches
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	pid, err := strconv.Atoi(line)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// TestRunUp_BindFailureTearsDownAndReturnsError is the fix-round-1
// regression test (code review finding): if the control-plane API listener
// can't bind (e.g. the address is already in use — a second `ensemble up`),
// runUp must not hang forever waiting on shutdownCtx.Done(), which only
// fires on SIGINT/SIGTERM or a successful POST /api/shutdown — neither of
// which a synchronous bind failure triggers. It must instead return a
// non-nil error promptly and tear down whatever it already started (here:
// a native service process).
//
// The service's pid is found via `pgrep -f` against a unique marker in its
// `run` command, rather than having the process itself write its pid to a
// file — cmd.Start() makes a process visible to `ps`/`pgrep` synchronously,
// at fork/exec time, before any of its script has actually run, so this
// avoids a real race: orch.Up()'s health gate here is satisfied by an
// already-listening httptest upstream (matching the rest of this file's
// tests), completely independent of the native process's own progress, so
// a self-reporting pid file could plausibly never get written if Down()
// reaps the process before the OS ever schedules it.
func TestRunUp_BindFailureTearsDownAndReturnsError(t *testing.T) {
	// upPort is reserved but not yet bound — see startStandinBackend below,
	// called only after runUp's preflight port check has already run, so
	// this test exercises the API-bind-failure path it's named for rather
	// than tripping the (correct, but different) preflight conflict.
	upPort := freePort(t)
	proxyPort := freePort(t)
	apiPort := freePort(t)

	// Occupy the API port so server.Serve's bind fails synchronously.
	occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", apiPort))
	if err != nil {
		t.Fatalf("occupy api port: %v", err)
	}
	defer occupied.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ensemble.yaml")
	marker := fmt.Sprintf("ensemble-bindfail-test-marker-%d", os.Getpid())
	yaml := fmt.Sprintf(`
services:
  svc:
    run: "sleep 30 # %s"
    port: %d
    proxy: %d
    entry: true
`, marker, upPort, proxyPort)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Cleanup(func() {
		if pid, ok := findPID(t, marker); ok {
			// Portable stdlib kill (not syscall.Kill) so this file still
			// vets on windows, where syscall.Kill doesn't exist.
			if proc, err := os.FindProcess(pid); err == nil {
				proc.Kill() // best-effort, in case the test itself failed the assertion below
			}
		}
	})

	opts := upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort)}
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runUp(context.Background(), opts, &stdout, &stderr) }()

	startStandinBackend(t, upPort, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	select {
	case runErr := <-done:
		if runErr == nil {
			t.Fatal("expected runUp to return a non-nil error on API bind failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runUp did not return after a bind failure — it hung")
	}

	// Confirm orch.Down() actually reaped the service process rather than
	// leaving it running after runUp gave up on the API listener.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, alive := findPID(t, marker); !alive {
			return // process gone: torn down successfully
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service process matching %q still alive after runUp returned — orch.Down was not called on bind failure", marker)
}

// TestUp_APIReachableDuringSlowStartup is the "status/dashboard get
// connection refused for the whole startup window" fix: the control-plane
// API must bind and answer before orch.Up finishes a slow, health-gated
// startup, not only after. "slow" never reports healthy until well past
// when the API should already be reachable — proving the server no longer
// waits for orch.Up to return before it starts serving.
func TestUp_APIReachableDuringSlowStartup(t *testing.T) {
	port := freePort(t)
	apiPort := freePort(t)
	proxyPort := freePort(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Nothing answers on port until well past this delay — comfortably
	// longer than the API bind (a plain net.Listen) should ever take, even
	// on a loaded CI box, so a fast /api/health here can only mean the
	// server started without waiting on "slow"'s health gate. The listen
	// itself (not just srv.Start) is deferred into the goroutine: runUp's
	// preflight port check runs synchronously right at the top of runUp,
	// so port must still be free when that check runs.
	const startupDelay = 2 * time.Second
	go func() {
		time.Sleep(startupDelay)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Errorf("listen: %v", err)
			return
		}
		srv.Listener.Close()
		srv.Listener = ln
		srv.Start()
	}()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ensemble.yaml")
	yaml := fmt.Sprintf(`
services:
  slow:
    run: "sleep 30"
    port: %d
    health: "/healthz"
    proxy: %d
    entry: true
`, port, proxyPort)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	opts := upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort)}
	done := make(chan error, 1)
	go func() { done <- runUp(ctx, opts, &stdout, &stderr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runUp did not return during cleanup")
		}
	})

	apiURL := "http://127.0.0.1:" + strconv.Itoa(apiPort)
	reachableAt := time.Now()
	waitHealthy(t, apiURL)
	if elapsed := time.Since(reachableAt); elapsed >= startupDelay {
		t.Fatalf("API took %v to become reachable, want well under the %v \"slow\" is still blocking on — the server waited for orch.Up again", elapsed, startupDelay)
	}

	// The health gate is eventually satisfied and status catches up.
	waitServiceHealthy(t, NewClient(apiURL), "slow")
}

// TestUp_PartialStartupFailureKeepsStackUpAndWarns is the "one service
// failing health check killed everything" fix: with one healthy service
// and one that never becomes healthy, runUp must not tear the stack down
// or return — the healthy service and the API stay up, `ensemble status`
// reports each service's real state, and a warning naming the failed
// service lands on stderr. Only cancelling ctx (standing in for SIGINT)
// tears everything down.
func TestUp_PartialStartupFailureKeepsStackUpAndWarns(t *testing.T) {
	goodPort := freePort(t) // stand-in backend bound after runUp's preflight check — see startStandinBackend
	badPort := freePort(t)  // nothing ever listens here — "bad" can never pass its health gate
	apiPort := freePort(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ensemble.yaml")
	yaml := fmt.Sprintf(`
services:
  good:
    run: "sleep 30"
    port: %d
    proxy: %d
    entry: true
  bad:
    run: "sleep 30"
    port: %d
    proxy: %d
    health: "/healthz"
    startup_timeout_s: 1
    entry: true
`, goodPort, freePort(t), badPort, freePort(t))
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	opts := upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort)}
	done := make(chan error, 1)
	go func() { done <- runUp(ctx, opts, &stdout, &stderr) }()
	// Registered before any assertion that could t.Fatalf partway through:
	// without this, a failed assertion below would abandon two live "sleep
	// 30" processes for the rest of the test run. shutDown guards against
	// double-draining done once the test body's own cancel+wait below (the
	// non-failure path) already did it.
	shutDown := false
	t.Cleanup(func() {
		if shutDown {
			return
		}
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runUp did not return during cleanup")
		}
	})

	startStandinBackend(t, goodPort, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	apiURL := "http://127.0.0.1:" + strconv.Itoa(apiPort)
	waitHealthy(t, apiURL)

	c := NewClient(apiURL)
	waitServiceHealthy(t, c, "good")

	// "bad" settles into failed on its own 1s startup_timeout_s.
	deadline := time.Now().Add(5 * time.Second)
	var badStatus string
	for time.Now().Before(deadline) && badStatus != "failed" {
		st, err := c.Status(context.Background())
		if err == nil {
			for _, s := range st.Services {
				if s.Name == "bad" {
					badStatus = string(s.Status)
				}
			}
		}
		if badStatus != "failed" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if badStatus != "failed" {
		t.Fatalf("bad service status = %q, want %q", badStatus, "failed")
	}

	// The whole point: runUp must still be running, not have torn down or
	// returned, despite bad's failure.
	select {
	case err := <-done:
		t.Fatalf("runUp returned (err=%v) after a partial startup failure — it should have stayed up with what did start", err)
	default:
	}

	// good's (and bad's, since its process started fine — only its health
	// check failed) service processes must still be alive: proof this
	// wasn't a "warn but call Down anyway" no-op. PIDs come from the
	// orchestrator's own bookkeeping (via /api/status), not a pgrep/marker
	// guess — a native service's argv doesn't reliably keep anything
	// distinguishing once its shell execs straight into the real command.
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, name := range []string{"good", "bad"} {
		var pid int
		for _, s := range st.Services {
			if s.Name == name {
				pid = s.PID
			}
		}
		if pid == 0 {
			t.Fatalf("%s has no recorded PID in status: %+v", name, st.Services)
		}
		if err := exec.Command("ps", "-p", strconv.Itoa(pid)).Run(); err != nil {
			t.Fatalf("%s (pid %d) is not alive — a partial startup failure must not tear down what already started: %v", name, pid, err)
		}
	}

	cancel()
	shutDown = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runUp returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runUp did not return after cancel")
	}

	stderrText := stderr.String() // safe now: runUp and every goroutine it spawned have returned
	if !strings.Contains(stderrText, "WARNING") || !strings.Contains(stderrText, "bad") {
		t.Fatalf("stderr doesn't contain a startup warning naming the failed service:\n%s", stderrText)
	}
}

// TestStubBodyFileResolvesAgainstConfigDirNotCWD guards final-review
// finding I10: stub.Respond.BodyFile was passed through to core/stub
// verbatim and read with os.ReadFile, i.e. relative to the ensemble
// process's CWD — while SeedSQL.File (orchestrator/seed.go) correctly
// resolves against Config.Dir. `ensemble up -c ../stack/ensemble.yaml`
// with a relative body_file broke at request time even though the config
// loaded and the service started fine. This test puts the config in a
// subdirectory, points body_file at a fixture relative to THAT directory,
// then runs with the process CWD set somewhere else entirely — the stub
// must still find the file.
func TestStubBodyFileResolvesAgainstConfigDirNotCWD(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "stack")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "fixtures"), 0o755); err != nil {
		t.Fatalf("mkdir fixtures: %v", err)
	}
	fixture := filepath.Join(cfgDir, "fixtures", "payment.json")
	const want = `{"fromFile":true}`
	if err := os.WriteFile(fixture, []byte(want), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stubPort := freePort(t)
	apiPort := freePort(t)
	cfgPath := filepath.Join(cfgDir, "ensemble.yaml")
	yaml := fmt.Sprintf(`
stubs:
  aws-kms:
    port: %d
    routes:
      - match: {method: GET, path: /f}
        respond: {status: 200, body_file: fixtures/payment.json}
`, stubPort)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Run with the process CWD somewhere else entirely — a body_file bug
	// resolving against CWD instead of Config.Dir must NOT accidentally
	// pass just because the test happens to run from the repo root.
	elsewhere := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origWD) })

	ctx, cancel := context.WithCancel(context.Background())
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	opts := upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort)}
	result := make(chan error, 1)
	go func() { result <- runUp(ctx, opts, stdout, stderr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-result:
		case <-time.After(5 * time.Second):
			t.Error("runUp did not return during cleanup")
		}
	})
	waitHealthy(t, "http://127.0.0.1:"+strconv.Itoa(apiPort))

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/f", stubPort))
	if err != nil {
		t.Fatalf("GET stub: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("stub returned %d: %s (body_file not resolved against Config.Dir)", resp.StatusCode, body)
	}
	if string(body) != want {
		t.Fatalf("stub body = %q, want %q", body, want)
	}
}

// TestCLI_StatusJSON drives the actual `run()` entrypoint (subcommand
// parsing + --json + --api-url) rather than the Client directly, to prove
// the flag wiring produces valid JSON output.
func TestCLI_StatusJSON(t *testing.T) {
	env := startEnsemble(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"status", "--api-url", env.apiURL, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	var out StatusResponse
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout: %v; stdout = %s", err, stdout.String())
	}
	if len(out.Services) != 1 || out.Services[0].Name != "svc" {
		t.Fatalf("Services = %+v", out.Services)
	}
}

// TestCLI_LatencySetListArmAllReset exercises the CLI's `latency`
// subcommands end to end.
func TestCLI_LatencySetListArmAllReset(t *testing.T) {
	env := startEnsemble(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"latency", "set",
		"--api-url", env.apiURL, "--json",
		"--target", "svc", "--path", "/", "--fixed", "10", "--enabled",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("latency set exit = %d, stderr = %s", code, stderr.String())
	}
	var setOut LatencyListResponse
	if err := json.Unmarshal(stdout.Bytes(), &setOut); err != nil {
		t.Fatalf("decode set stdout: %v; stdout = %s", err, stdout.String())
	}
	if len(setOut.Rules) != 1 || setOut.Rules[0].FixedMs != 10 || !setOut.Rules[0].Enabled {
		t.Fatalf("rules after set = %+v", setOut.Rules)
	}

	stdout.Reset()
	code = run([]string{"latency", "list", "--api-url", env.apiURL, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("latency list exit = %d, stderr = %s", code, stderr.String())
	}
	var listOut LatencyListResponse
	if err := json.Unmarshal(stdout.Bytes(), &listOut); err != nil {
		t.Fatalf("decode list stdout: %v; stdout = %s", err, stdout.String())
	}
	if len(listOut.Rules) != 1 {
		t.Fatalf("rules after list = %+v", listOut.Rules)
	}

	stdout.Reset()
	code = run([]string{"latency", "arm-all", "--api-url", env.apiURL, "--json", "--enabled=false"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("latency arm-all exit = %d, stderr = %s", code, stderr.String())
	}
	var armOut LatencyListResponse
	if err := json.Unmarshal(stdout.Bytes(), &armOut); err != nil {
		t.Fatalf("decode arm-all stdout: %v", err)
	}
	if len(armOut.Rules) != 1 || armOut.Rules[0].Enabled {
		t.Fatalf("rules after arm-all=false = %+v", armOut.Rules)
	}

	stdout.Reset()
	code = run([]string{"latency", "reset", "--api-url", env.apiURL, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("latency reset exit = %d, stderr = %s", code, stderr.String())
	}
	var resetOut LatencyListResponse
	if err := json.Unmarshal(stdout.Bytes(), &resetOut); err != nil {
		t.Fatalf("decode reset stdout: %v", err)
	}
	if len(resetOut.Rules) != 0 {
		t.Fatalf("rules after reset = %+v", resetOut.Rules)
	}
}

// TestCLI_TrafficJSON drives `ensemble traffic --json` after generating one
// hop of real traffic through the proxy.
func TestCLI_TrafficJSON(t *testing.T) {
	env := startEnsemble(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", env.proxyPort))
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	resp.Body.Close()

	var out TrafficResponse
	deadline := time.Now().Add(2 * time.Second)
	var stdout, stderr bytes.Buffer
	for time.Now().Before(deadline) {
		stdout.Reset()
		stderr.Reset()
		code := run([]string{"traffic", "--api-url", env.apiURL, "--json"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("traffic exit = %d, stderr = %s", code, stderr.String())
		}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("decode stdout: %v; stdout = %s", err, stdout.String())
		}
		if len(out.Hops) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(out.Hops) == 0 {
		t.Fatal("expected at least one hop")
	}
}

// TestCLI_TraceJSONAndExportCurl guards final-review promoted-minor #27:
// cmdTrace (cmd_trace.go) was the one CLI command with zero tests, and it's
// the one that formats three distinct server responses (plain table, JSON,
// and export). Drives the actual `run()` entrypoint for both the
// happy-path JSON view and --export=curl, against the same live runUp test
// env the rest of this file uses.
func TestCLI_TraceJSONAndExportCurl(t *testing.T) {
	env := startEnsemble(t)
	c := NewClient(env.apiURL)
	ctx := context.Background()

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello", env.proxyPort))
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	resp.Body.Close()

	var traceID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tr, err := c.Traffic(ctx, 0, 0, false)
		if err != nil {
			t.Fatalf("traffic: %v", err)
		}
		for _, h := range tr.Hops {
			if h.To == "svc" && h.TraceID != "" {
				traceID = h.TraceID
				break
			}
		}
		if traceID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if traceID == "" {
		t.Fatal("no traced svc hop found")
	}

	// flags before the positional traceId: flag.FlagSet.Parse (like the
	// stdlib generally) stops parsing at the first non-flag argument, so
	// unlike most of this file's other subcommands trace's positional arg
	// must come last, not first.
	var stdout, stderr bytes.Buffer
	code := run([]string{"trace", "--api-url", env.apiURL, "--json", traceID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("trace --json exit = %d, stderr = %s", code, stderr.String())
	}
	var got TraceResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode stdout: %v; stdout = %s", err, stdout.String())
	}
	if len(got.Hops) == 0 {
		t.Fatalf("expected at least one hop in trace %s", traceID)
	}
	for _, h := range got.Hops {
		if h.TraceID != traceID {
			t.Fatalf("hop with wrong traceId %q in trace %s response", h.TraceID, traceID)
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"trace", "--export", "curl", "--api-url", env.apiURL, traceID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("trace --export=curl exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "curl ") {
		t.Fatalf("--export=curl output does not look like curl commands: %q", stdout.String())
	}
}

// TestCLI_SeedUnknownFails exercises the `seed` command's error path (exit
// code gates CI): a seed name the config doesn't define.
func TestCLI_SeedUnknownFails(t *testing.T) {
	env := startEnsemble(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"seed", "does-not-exist", "--api-url", env.apiURL}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for unknown seed, got 0; stdout=%s", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("expected an error message on stderr")
	}
}

// TestCLI_Down drives the `down` command against a live `up` instance and
// confirms it triggers a graceful shutdown (POST /api/shutdown -> runUp
// returns).
func TestCLI_Down(t *testing.T) {
	env := startEnsemble(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"down", "--api-url", env.apiURL, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("down exit = %d, stderr = %s", code, stderr.String())
	}

	if err := env.wait(t, 5*time.Second); err != nil {
		t.Fatalf("runUp returned error after down: %v", err)
	}
}

// TestCLI_DashboardOpensBrowserWhenReachable drives `dashboard` against a
// fake control plane (just enough to answer GET /api/status) and confirms
// it prints the URL and hands it to openBrowserFn — stubbed here so the
// test never spawns a real browser process.
func TestCLI_DashboardOpensBrowserWhenReachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"services":[]}`))
	}))
	defer ts.Close()

	var opened string
	orig := openBrowserFn
	openBrowserFn = func(url string) error { opened = url; return nil }
	defer func() { openBrowserFn = orig }()

	var stdout, stderr bytes.Buffer
	code := run([]string{"dashboard", "--api-url", ts.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != ts.URL {
		t.Fatalf("stdout = %q, want %q", stdout.String(), ts.URL)
	}
	if opened != ts.URL {
		t.Fatalf("openBrowserFn called with %q, want %q", opened, ts.URL)
	}
}

// TestCLI_DashboardFailsWhenUnreachable checks the reachability preflight:
// with nothing listening, dashboard must fail with a clear message and
// never call openBrowserFn — better than handing the user a browser tab
// that just shows a generic connection error.
func TestCLI_DashboardFailsWhenUnreachable(t *testing.T) {
	unreachable := fmt.Sprintf("http://127.0.0.1:%d", freePort(t))

	called := false
	orig := openBrowserFn
	openBrowserFn = func(url string) error { called = true; return nil }
	defer func() { openBrowserFn = orig }()

	var stdout, stderr bytes.Buffer
	code := run([]string{"dashboard", "--api-url", unreachable}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit, stdout = %s", stdout.String())
	}
	if called {
		t.Fatal("openBrowserFn should not be called when the control plane is unreachable")
	}
	if !strings.Contains(stderr.String(), "ensemble up") {
		t.Fatalf("stderr = %q, want a hint to run `ensemble up`", stderr.String())
	}
}

// TestCLI_DashboardNoOpenSkipsReachabilityCheck checks --no-open just
// prints the URL — it must succeed even with nothing listening, since it's
// meant to work as a plain lookup (e.g. to see the address before
// `ensemble up` is even running), not a "is it running" check.
func TestCLI_DashboardNoOpenSkipsReachabilityCheck(t *testing.T) {
	unreachable := fmt.Sprintf("http://127.0.0.1:%d", freePort(t))

	called := false
	orig := openBrowserFn
	openBrowserFn = func(url string) error { called = true; return nil }
	defer func() { openBrowserFn = orig }()

	var stdout, stderr bytes.Buffer
	code := run([]string{"dashboard", "--api-url", unreachable, "--no-open"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != unreachable {
		t.Fatalf("stdout = %q, want %q", stdout.String(), unreachable)
	}
	if called {
		t.Fatal("openBrowserFn should not be called with --no-open")
	}
}

// TestBrowserCommandPerOS pins the per-OS "open a URL" launcher: darwin
// uses `open`, windows uses rundll32's URL protocol handler (not `start`,
// which is a cmd.exe builtin exec.Command can't invoke directly), and
// everything else falls back to xdg-open. Path is checked with a suffix
// match, not equality: exec.Command resolves the binary via LookPath when
// it's on this machine's PATH (e.g. "open" on this Mac), leaving it
// unresolved otherwise — either way it ends with the launcher's name.
func TestBrowserCommandPerOS(t *testing.T) {
	cases := []struct {
		goos     string
		wantPath string
		wantArgs []string
	}{
		{"darwin", "open", []string{"open", "http://x"}},
		{"windows", "rundll32", []string{"rundll32", "url.dll,FileProtocolHandler", "http://x"}},
		{"linux", "xdg-open", []string{"xdg-open", "http://x"}},
		{"freebsd", "xdg-open", []string{"xdg-open", "http://x"}},
	}
	for _, c := range cases {
		t.Run(c.goos, func(t *testing.T) {
			cmd := browserCommand(c.goos, "http://x")
			if !strings.HasSuffix(cmd.Path, c.wantPath) {
				t.Fatalf("Path = %q, want suffix %q", cmd.Path, c.wantPath)
			}
			if !reflect.DeepEqual(cmd.Args, c.wantArgs) {
				t.Fatalf("Args = %v, want %v", cmd.Args, c.wantArgs)
			}
		})
	}
}

// TestCLI_VersionFlag checks `ensemble --version` prints the resolved
// version (buildinfo.Resolve enriches the unstamped "dev" this test binary
// carries with the commit it was built from — see buildinfo_test.go) and
// exits 0, with no server needed.
func TestCLI_VersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	want := buildinfo.Resolve(version) + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

// TestCLI_UnknownCommandIsExitCode2 checks the CLI's contract that a bad
// invocation is a usage error (exit 2), not a crash.
func TestCLI_UnknownCommandIsExitCode2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

// TestUpDefaultAPIAddrIsLoopback guards final-review finding C1: `ensemble
// up` used to default --api to ":4700", binding the entire unauthenticated
// control plane (traffic capture including bodies, arbitrary seed
// execution, service restart/flip, latency injection) to every interface.
// The default must be loopback-only, matching defaultAPIURL()'s client-side
// assumption that the API lives at 127.0.0.1:4700.
func TestUpDefaultAPIAddrIsLoopback(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseUpOptions(nil, &stderr)
	if err != nil {
		t.Fatalf("parseUpOptions: %v", err)
	}
	if opts.Addr != "127.0.0.1:4700" {
		t.Fatalf("default --api = %q, want %q", opts.Addr, "127.0.0.1:4700")
	}
}

// TestDefaultAPIURLMatchesUpDefaultAddr pins the client/server default
// contract: the client commands' default --api-url must point at exactly
// the address `ensemble up` binds by default.
func TestDefaultAPIURLMatchesUpDefaultAddr(t *testing.T) {
	t.Setenv("ENSEMBLE_API", "")
	var stderr bytes.Buffer
	opts, err := parseUpOptions(nil, &stderr)
	if err != nil {
		t.Fatalf("parseUpOptions: %v", err)
	}
	want := "http://" + opts.Addr
	if got := defaultAPIURL(); got != want {
		t.Fatalf("defaultAPIURL() = %q, want %q (matching up's default --api)", got, want)
	}
}

func TestParseVariantFlag(t *testing.T) {
	got, err := parseVariantFlag("mono=real, api=stub")
	if err != nil || got["mono"] != "real" || got["api"] != "stub" || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := parseVariantFlag(""); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v, %v", got, err)
	}
	for _, bad := range []string{"mono", "=real", "mono="} {
		if _, err := parseVariantFlag(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

// fakeProfileServer answers the health + profile routes the CLI's
// attach fork uses, recording which profile switches it received.
func fakeProfileServer(t *testing.T, healthy bool) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if !healthy {
			http.Error(w, "nope", 503)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /api/profiles/{name}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.PathValue("verb")+" "+r.PathValue("name"))
		json.NewEncoder(w).Encode(map[string]any{
			"active":   []string{"lane1", "lane2"},
			"profiles": []map[string]any{{"name": "lane2", "services": []string{"b1"}, "active": true}},
		})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"active": []string{"lane1"}, "profiles": []map[string]any{{"name": "lane1", "services": []string{"a1"}, "active": true}}})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &calls
}

func TestCLI_UpWithProfileAttachesToRunningStack(t *testing.T) {
	ts, calls := fakeProfileServer(t, true)
	t.Setenv("ENSEMBLE_API", ts.URL)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"up", "lane2", "ops"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got := strings.Join(*calls, ","); got != "up lane2,up ops" {
		t.Errorf("calls = %q", got)
	}
	if !strings.Contains(stdout.String(), "attached to "+ts.URL) || !strings.Contains(stdout.String(), "lane2") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// TestCLI_UpNoArgsNoopWhenAlreadyRunning: plain `ensemble up` (no
// positional profiles) against an already-running stack must not error or
// try to start anything — just report it's already up. Distinct from
// TestCLI_UpWithProfileAttachesToRunningStack, which drives the
// positional-profile attach path instead.
func TestCLI_UpNoArgsNoopWhenAlreadyRunning(t *testing.T) {
	ts, calls := fakeProfileServer(t, true)
	t.Setenv("ENSEMBLE_API", ts.URL)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"up"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already running") {
		t.Errorf("stdout = %q, want mention of already running", stdout.String())
	}
	if len(*calls) != 0 {
		t.Errorf("no-op path must not call any profile endpoint, got %v", *calls)
	}
}

func TestCLI_DownWithProfileDeactivatesOnly(t *testing.T) {
	ts, calls := fakeProfileServer(t, true)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"down", "--api-url", ts.URL, "lane2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got := strings.Join(*calls, ","); got != "down lane2" {
		t.Errorf("calls = %q", got)
	}
}

func TestCLI_ProfilesList(t *testing.T) {
	ts, _ := fakeProfileServer(t, true)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"profiles", "--api-url", ts.URL}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "lane1") || !strings.Contains(stdout.String(), "yes") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestParseUpOptionsPositionalProfiles(t *testing.T) {
	opts, err := parseUpOptions([]string{"--profile", "ops", "lane1", "lane2"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(opts.Positional, ",") != "lane1,lane2" || strings.Join(opts.Profiles, ",") != "ops" {
		t.Errorf("opts = %+v", opts)
	}
}
