package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// writeReadinessCheckFile writes a minimal readiness checks file naming one
// service/path/assert, returning the file's basename for use as
// config.Readiness.File.
func writeReadinessCheckFile(t *testing.T, dir, service string, status int, bodyJQ string) string {
	t.Helper()
	assertLines := fmt.Sprintf("status: %d", status)
	if bodyJQ != "" {
		assertLines += fmt.Sprintf("\n      body_jq: %q", bodyJQ)
	}
	contents := fmt.Sprintf(`checks:
  - name: %s-up
    service: %s
    path: /check
    assert:
      %s
`, service, service, assertLines)
	path := filepath.Join(dir, "readiness.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write readiness file: %v", err)
	}
	return "readiness.yaml"
}

// readinessTestConfig builds a Config with one "catalog" service resolvable
// to server's port (no run/docker — this test never calls Up, only the
// readiness phase directly), plus a validated Readiness pointing at a
// checks file for it. Validate() is called so Config.ReadinessChecks() is
// populated, the same way Load() would populate it for a real ensemble.yaml.
func readinessTestConfig(t *testing.T, server *httptest.Server, status int, bodyJQ string, timeoutS, retryIntervalS int) *config.Config {
	t.Helper()
	dir := t.TempDir()
	port := serverPort(t, server)
	file := writeReadinessCheckFile(t, dir, "catalog", status, bodyJQ)

	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			// Run is set only to satisfy config.Validate() — these tests
			// call beginReadiness directly, never Up(), so nothing ever
			// actually spawns this command.
			"catalog": {Run: "true", Port: port},
		},
		Readiness: &config.Readiness{File: file, TimeoutS: timeoutS, RetryIntervalS: retryIntervalS},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.ReadinessChecks() == nil {
		t.Fatal("expected Validate to populate ReadinessChecks()")
	}
	return cfg
}

func serverPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	u := server.URL
	i := strings.LastIndex(u, ":")
	if i < 0 {
		t.Fatalf("could not find port in %q", u)
	}
	var port int
	if _, err := fmt.Sscanf(u[i+1:], "%d", &port); err != nil {
		t.Fatalf("parse port from %q: %v", u, err)
	}
	return port
}

// waitForReadinessState polls o.Readiness() until State equals want or
// timeout elapses, failing the test on timeout.
func waitForReadinessState(t *testing.T, o *Orchestrator, want ReadinessOverallState, timeout time.Duration) ReadinessSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last ReadinessSnapshot
	for time.Now().Before(deadline) {
		last = o.Readiness()
		if last.State == want {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("readiness state = %v (checks %+v), want %v within %s", last.State, last.Checks, want, timeout)
	return last
}

func TestReadinessCheckPassesOnFirstAttemptNeverRerun(t *testing.T) {
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"UP"}`)
	}))
	defer server.Close()

	cfg := readinessTestConfig(t, server, http.StatusOK, `.status == "UP"`, 5, 1)
	o := newTestOrchestrator(t, cfg, Opts{})

	o.beginReadiness(context.Background())
	snap := waitForReadinessState(t, o, ReadinessReady, 3*time.Second)
	if len(snap.Checks) != 1 || !snap.Checks[0].Passed {
		t.Fatalf("checks = %+v, want one passed check", snap.Checks)
	}

	afterFirstPass := atomic.LoadInt64(&hits)
	time.Sleep(1200 * time.Millisecond) // > retry_interval_s
	if got := atomic.LoadInt64(&hits); got != afterFirstPass {
		t.Errorf("hit count grew from %d to %d after passing — a passed check must not be re-executed", afterFirstPass, got)
	}
}

func TestReadinessCheckPassesOnLaterRetry(t *testing.T) {
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&hits, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"UP"}`)
	}))
	defer server.Close()

	cfg := readinessTestConfig(t, server, http.StatusOK, `.status == "UP"`, 10, 1)
	o := newTestOrchestrator(t, cfg, Opts{})

	o.beginReadiness(context.Background())
	snap := waitForReadinessState(t, o, ReadinessReady, 6*time.Second)
	if !snap.Checks[0].Passed {
		t.Fatalf("checks = %+v, want passed", snap.Checks)
	}
	if got := atomic.LoadInt64(&hits); got < 3 {
		t.Errorf("hit count = %d, want at least 3 (passed only on a later retry)", got)
	}
}

func TestReadinessCheckNeverPassesTimesOutNotReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := readinessTestConfig(t, server, http.StatusOK, "", 1, 1)
	o := newTestOrchestrator(t, cfg, Opts{})

	o.beginReadiness(context.Background())
	snap := waitForReadinessState(t, o, ReadinessNotReady, 5*time.Second)
	if len(snap.Checks) != 1 || snap.Checks[0].Passed {
		t.Fatalf("checks = %+v, want the one check still failing", snap.Checks)
	}
	if snap.Checks[0].LastError == "" {
		t.Error("expected a recorded last error for the never-passing check")
	}
}

func TestReadinessNoConfigIsImmediatelyReady(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	o := newTestOrchestrator(t, cfg, Opts{})

	o.beginReadiness(context.Background())
	snap := o.Readiness()
	if snap.State != ReadinessReady {
		t.Fatalf("state = %v, want ready when no readiness: key is configured", snap.State)
	}
	if len(snap.Checks) != 0 {
		t.Fatalf("checks = %+v, want none", snap.Checks)
	}
}

// TestReadinessUnconfiguredStaysReadyDespiteNodeFailure guards the "no
// readiness: key at all" scenario applying unconditionally — a stack that
// never opted into this feature must not have `ensemble ready` start
// failing just because some unrelated node failed to start.
func TestReadinessUnconfiguredStaysReadyDespiteNodeFailure(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"bad": {}, // neither run nor docker: fails to start
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err == nil {
		t.Fatal("expected Up to report the bad service's failure")
	}
	defer o.Down()

	if snap := o.Readiness(); snap.State != ReadinessReady {
		t.Fatalf("state = %v, want ready — no readiness: key was configured", snap.State)
	}
}

func TestReadinessHeadersFromScriptFailureFailsCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "auth.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho 'boom' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	port := serverPort(t, server)
	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"catalog": {Port: port},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	chk := config.ReadinessCheck{
		Name:        "catalog-up",
		Service:     "catalog",
		Path:        "/check",
		HeadersFrom: "auth.sh",
	}
	passed, err := o.runOneReadinessCheck(context.Background(), chk)
	if passed {
		t.Fatal("expected the check to fail when headers_from exits non-zero")
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want it to carry the script's stderr", err)
	}
}

func TestReadinessLoopStopsOnCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	// timeout_s is deliberately large — only cancellation should stop this
	// loop within the test's wait budget.
	cfg := readinessTestConfig(t, server, http.StatusOK, "", 60, 1)
	o := newTestOrchestrator(t, cfg, Opts{})

	ctx, cancel := context.WithCancel(context.Background())
	o.beginReadiness(ctx)
	cancel()

	waitForReadinessState(t, o, ReadinessNotReady, 3*time.Second)
}

// TestHeadersFromNeverResolvesThroughThePATH is a regression test for a bug
// the sample stack surfaced: `ensemble up -c ensemble.yaml` sets Config.Dir
// to ".", and filepath.Join(".", "./auth.sh") CLEANS to "auth.sh" — a name
// with no separator, which exec.Command resolves through $PATH.
//
// The visible symptom was a readiness check that could never pass
// (`executable file not found in $PATH`). The invisible one is worse: with
// any same-named script anywhere on the PATH, ensemble runs THAT instead,
// and a config that names a file next to itself silently executes a
// stranger. A relative path in a config file is a path, never a command
// name, so this test puts a decoy on the PATH and insists the local script
// wins.
//
// Every other test here uses an absolute t.TempDir() as Config.Dir, where
// Join always leaves a separator — which is exactly why none of them caught
// it, and why this one takes the trouble to reproduce a RELATIVE config dir.
func TestHeadersFromNeverResolvesThroughThePATH(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Token")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "auth.sh"),
		[]byte("#!/bin/sh\necho 'X-Token: the-one-next-to-the-config'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	decoyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoyDir, "auth.sh"),
		[]byte("#!/bin/sh\necho 'X-Token: the-one-on-the-path'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", decoyDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The condition the sample hit: the config was loaded by a relative
	// path, so its Dir is ".", and "." is the directory the process is in.
	t.Chdir(configDir)

	port := serverPort(t, server)
	cfg := &config.Config{
		Dir:      ".",
		Services: map[string]config.Service{"catalog": {Port: port}},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	chk := config.ReadinessCheck{
		Name: "catalog-up", Service: "catalog", Path: "/check",
		HeadersFrom: "./auth.sh",
	}
	passed, err := o.runOneReadinessCheck(context.Background(), chk)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if !passed {
		t.Fatal("check did not pass")
	}
	if got != "the-one-next-to-the-config" {
		t.Errorf("header = %q — headers_from resolved through the PATH instead of against the config", got)
	}
}
