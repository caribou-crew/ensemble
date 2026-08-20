package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
