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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

// writeConfig writes a minimal ensemble.yaml declaring one native service
// ("svc") whose real port is the httptest upstream's port and whose
// intercept port is proxyPort — the same shape ensemble/server's own tests
// use (a long-lived dummy process just to give the orchestrator something
// to supervise; traffic actually flows to the already-running httptest
// server on the same port).
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

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from svc"))
	}))
	t.Cleanup(upstream.Close)

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	upPort, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("upstream port: %v", err)
	}

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

	apiURL := "http://127.0.0.1:" + strconv.Itoa(apiPort)
	waitHealthy(t, apiURL)

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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	upPort, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("upstream port: %v", err)
	}
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

// TestCLI_VersionFlag checks `ensemble --version` prints the stamped
// version var and exits 0, with no server needed.
func TestCLI_VersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != version+"\n" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), version+"\n")
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
