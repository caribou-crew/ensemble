package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/capture"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- helper processes -------------------------------------------------
//
// The test command `retrace run` executes is THIS test binary re-invoked
// with `-test.run <helper>` plus an env selector — the standard Go idiom,
// so the tests need no external tool (curl may be absent, and `go run`
// would collapse the child's exit code anyway).

func TestHelperFetchesThroughProxy(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "fetch" {
		return
	}
	resp, err := http.Get(os.Getenv("RETRACE_PROXY_URL") + "/cart")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

// TestHelperFetchesThroughProxyNoisy is TestHelperFetchesThroughProxy plus
// stdout chatter BEFORE the request completes — the shape of a real test
// runner (jest/junit reporters print their own JSON, and plain progress
// dots, ahead of anything retrace writes). --json's whole point is that
// none of this reaches retrace's own stdout.
func TestHelperFetchesThroughProxyNoisy(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "fetch-noisy" {
		return
	}
	fmt.Println(`{"junit":"noise","this":"is the test runner's stdout"}`)
	fmt.Println("some plain log line")
	resp, err := http.Get(os.Getenv("RETRACE_PROXY_URL") + "/cart")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

// TestHelperSleepsBriefly is the test command for the dead-proxy regression
// below. It never touches RETRACE_PROXY_URL — that test replaces the
// session's ProxyURL with a listener the test itself controls, so the
// helper only needs to occupy wall-clock time while that listener is
// closed out from under the run.
func TestHelperSleepsBriefly(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "sleep" {
		return
	}
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
}

func TestHelperExitsSeven(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "exit7" {
		return
	}
	os.Exit(7)
}

func TestHelperPostsMarkers(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "markers" {
		return
	}
	door := os.Getenv("RETRACE_MARKER_URL")
	post := func(path, body string) {
		resp, err := http.Post(door+path, "application/json", strings.NewReader(body))
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper marker:", err)
			os.Exit(9)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			fmt.Fprintln(os.Stderr, "helper marker status:", resp.StatusCode)
			os.Exit(9)
		}
	}
	post("/group", `{"name":"login"}`)
	time.Sleep(5 * time.Millisecond)
	post("/group", `{"name":"checkout"}`)
	time.Sleep(5 * time.Millisecond)
	post("/group/end", `{}`)
	os.Exit(0)
}

// --- harness ----------------------------------------------------------

// buildRetrace builds the real binary. Exit codes are NEVER asserted
// through `go run` (global-constraints.md): it reports a non-zero child as
// its own exit 1, so every assertion that matters here — 2 for the config
// refusal, 3 for a usage error, 7 for the test command's own code — would
// read as 1.
func buildRetrace(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "retrace")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

type runResult struct {
	code           int
	stdout, stderr string
}

// runRetrace executes the built binary in cwd with helper selected by
// helper (may be ""), returning its real process exit code.
func runRetrace(t *testing.T, bin, cwd, helper string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "RETRACE_TEST_HELPER="+helper)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running retrace: %v", err)
	}
	return runResult{code: code, stdout: out.String(), stderr: errOut.String()}
}

// selfCmd is the `-- <test command>` tail: this test binary, filtered to
// one helper.
func selfCmd(t *testing.T, helper string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return []string{"--", self, "-test.run", "^" + helper + "$"}
}

func writeConfig(t *testing.T, cwd, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, "retrace.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write retrace.yaml: %v", err)
	}
}

func onlyManifest(t *testing.T, cwd, app, flow string) runs.Manifest {
	t.Helper()
	root := runs.RunsRoot(cwd)
	ids := runs.ListRuns(root, app, flow)
	if len(ids) != 1 {
		t.Fatalf("run directories for %s/%s = %v, want exactly one", app, flow, ids)
	}
	p, err := runs.PathsFor(root, app, flow, ids[0])
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	return m
}

// --- tests ------------------------------------------------------------

func TestRunStandaloneRecordsAndWritesAManifest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nredact:\n  - token\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Mode != runs.ModeStandalone {
		t.Errorf("mode = %q, want %q", m.Mode, runs.ModeStandalone)
	}
	if m.Test.ExitCode != 0 {
		t.Errorf("test.exitCode = %d, want 0", m.Test.ExitCode)
	}
	if m.Wire.Calls != 1 {
		t.Errorf("wire.calls = %d, want 1", m.Wire.Calls)
	}
	if m.Hops != nil {
		t.Errorf("hops = %+v, want absent in standalone mode", m.Hops)
	}
	if m.Flow != "checkout" || m.App != "web" {
		t.Errorf("app/flow = %q/%q", m.App, m.Flow)
	}
	// A healthy run — one call made and recorded, exit 0, no history to
	// compare screenshot counts against — must actually reach VerdictOK now
	// that Task 6 fills the assessTrust seam. A regression back to the
	// hard-coded suspect placeholder, or any change that keeps a clean run
	// from ever reaching ok, would fail here.
	if m.Capture.Status != trace.VerdictOK {
		t.Errorf("capture.status = %q, want %q; reasons: %+v", m.Capture.Status, trace.VerdictOK, m.Capture.Reasons)
	}
}

// TestRunBannersANonOkVerdict is Task 6's Step 6 regression: a run whose
// command makes no calls at all must both (a) print a stderr line
// identifying the verdict, and (b) persist the same status to the manifest
// — the banner and the durable record must never disagree.
func TestRunBannersANonOkVerdict(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	// "true" never touches RETRACE_PROXY_URL and posts no markers, so
	// retrace never sees a single request of any kind: RequestsSeen == 0.
	args := []string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL, "--", "true"}
	res := runRetrace(t, bin, cwd, "", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 (the test command itself succeeded)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "capture-trust:") || !strings.Contains(res.stderr, "broken") {
		t.Fatalf("stderr does not banner a broken capture-trust verdict:\n%s", res.stderr)
	}
	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Capture.Status != trace.VerdictBroken {
		t.Errorf("manifest capture.status = %q, want %q — the banner and the manifest must agree", m.Capture.Status, trace.VerdictBroken)
	}
}

func TestRunRequiresFlow(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	res := runRetrace(t, bin, cwd, "", "run", "--app", "web", "--upstream", "http://127.0.0.1:1", "--", "true")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.code, exitUsage, res.stderr)
	}
	// Deliberately stricter than "mentions --flow": the usage banner names
	// every flag, so a substring match on "--flow" alone passes even when
	// `run` is not implemented at all.
	if !strings.Contains(res.stderr, "--flow is required") {
		t.Errorf("stderr does not say --flow is required: %s", res.stderr)
	}
}

// A failing test must fail the pipeline: retrace's own 0/1/2/3 contract
// only applies when the test command itself succeeded.
func TestRunPropagatesTheTestCommandsExitCode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperExitsSeven")...)
	res := runRetrace(t, bin, cwd, "exit7", args...)
	if res.code != 7 {
		t.Fatalf("exit = %d, want 7\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if m := onlyManifest(t, cwd, "web", "checkout"); m.Test.ExitCode != 7 {
		t.Errorf("test.exitCode = %d, want 7", m.Test.ExitCode)
	}
}

// This is the test that keeps flow-part groups from being a write-only
// feature: markers reach groups.jsonl and must be FOLDED into the
// manifest, or the wire diff's named sections silently collapse to one
// unnamed section on every real run while every unit test stays green.
func TestRunFoldsMarkersIntoManifestGroups(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperPostsMarkers")...)
	res := runRetrace(t, bin, cwd, "markers", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if len(m.Groups) != 2 {
		t.Fatalf("groups = %+v, want 2", m.Groups)
	}
	if m.Groups[0].Name != "login" || m.Groups[1].Name != "checkout" {
		t.Fatalf("group names = %q, %q; want login, checkout in start order", m.Groups[0].Name, m.Groups[1].Name)
	}
	if !m.Groups[0].EndedAt.Equal(m.Groups[1].StartedAt) {
		t.Errorf("group %q ended at %v, want the next group's start %v",
			m.Groups[0].Name, m.Groups[0].EndedAt, m.Groups[1].StartedAt)
	}
}

// --- the config refusal (zero-value pin) ------------------------------

// config.Discover does not walk up the directory tree — that is a security
// property — so running from a subdirectory of a monorepo finds no config
// and capture would write UNREDACTED hops to disk. Config.Loaded's zero
// value (false) is the unsafe-to-proceed one on purpose, and `retrace run`
// refuses rather than degrading silently.
func TestRunRefusesToCaptureWhenNoConfigWasFound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir() // deliberately no retrace.yaml

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d (hard gate)\nstdout: %s\nstderr: %s", res.code, exitGate, res.stdout, res.stderr)
	}
	want := filepath.Join(cwd, "retrace.yaml")
	if !strings.Contains(res.stderr, want) {
		t.Errorf("stderr does not name the absolute path %q:\n%s", want, res.stderr)
	}
	if !strings.Contains(res.stderr, "--no-config") {
		t.Errorf("stderr does not name the --no-config override:\n%s", res.stderr)
	}
	if entries, _ := os.ReadDir(runs.RunsRoot(cwd)); len(entries) != 0 {
		t.Errorf("a refused run wrote %d entries under the runs root", len(entries))
	}
	// The message must say plainly what proceeding costs, not just that
	// config is missing — this is the one message standing between a user
	// and a secret landing in a file they may later commit.
	if !strings.Contains(res.stderr, "unredacted") {
		t.Errorf("stderr does not say plainly that bodies would be written unredacted:\n%s", res.stderr)
	}
}

// The refusal is keyed on Config.Loaded, NEVER on len(Redact): an empty
// redact list is correct and deliberate — baseline redaction lives in
// core/trace's redactor and config.Redact supplies user ADDITIONS on top.
// Mutating cmdRun to key the refusal on `len(cfg.Redact) == 0` makes this
// test fail (it would exit 2 instead of capturing).
func TestRunCapturesWithAConfigThatDeclaresNoRedactKeys(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n") // a real config, no redact: key at all

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — an empty redact list is a valid config, not a missing one\nstderr: %s",
			res.code, res.stderr)
	}
	if m := onlyManifest(t, cwd, "web", "checkout"); m.Wire.Calls != 1 {
		t.Errorf("wire.calls = %d, want 1", m.Wire.Calls)
	}
}

func TestRunNoConfigOverridesTheRefusal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir() // no retrace.yaml

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--no-config", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 with --no-config\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if m := onlyManifest(t, cwd, "web", "checkout"); m.Wire.Calls != 1 {
		t.Errorf("wire.calls = %d, want 1", m.Wire.Calls)
	}
}

// An unstartable test command (bad path, not executable) never produces a
// manifest — so the run directory StartStandalone already created must not
// survive either. Left behind, runs.ListRuns would list it, and a "latest"
// selector (Task 8) would resolve to a run that never happened.
func TestRunUnstartableTestCommandLeavesNoOrphanRunDirectory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	res := runRetrace(t, bin, cwd, "", "run", "--flow", "checkout", "--app", "web",
		"--upstream", upstream.URL, "--", "/no/such/binary-xyz")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d (could not run the test command)\nstdout: %s\nstderr: %s",
			res.code, exitUsage, res.stdout, res.stderr)
	}
	if ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout"); len(ids) != 0 {
		t.Errorf("run directories for web/checkout = %v, want none left behind", ids)
	}
}

// --json is the CI contract: the manifest, verbatim and ALONE, on stdout.
// The helper is deliberately noisy on its OWN stdout — the shape of a real
// test runner (jest/junit reporters print JSON of their own) — to prove
// that chatter is routed to stderr, not merely that a manifest can be found
// somewhere inside a mixed stream. An assertion that locates the payload
// with strings.Index(stdout, "{") cannot tell contaminated output from
// clean; this one requires the WHOLE of stdout to parse.
func TestRunJSONEmitsOnlyTheManifestOnStdout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--json", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxyNoisy")...)
	res := runRetrace(t, bin, cwd, "fetch-noisy", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	var m runs.Manifest
	if err := json.Unmarshal([]byte(res.stdout), &m); err != nil {
		t.Fatalf("stdout does not parse AS A WHOLE as a manifest (contaminated?): %v\nstdout: %q", err, res.stdout)
	}
	if m.Schema != runs.Schema || m.Flow != "checkout" {
		t.Fatalf("manifest = %+v", m)
	}
	// The noise didn't vanish — it went to stderr instead of stdout.
	if !strings.Contains(res.stderr, "is the test runner's stdout") {
		t.Errorf("the test command's stdout chatter did not reach stderr:\n%s", res.stderr)
	}
}

// --- Fix round 3: runFlow must join WatchProxy before Close ------------
//
// runFlow used to `go s.WatchProxy(ctx)` and never wait for it: cancel()
// fired, then s.Close() tore the proxy listener down, then assessTrust ran
// — all while the watcher goroutine could still be mid-flight. WatchProxy's
// own ctx.Done() branch re-probes the listener on the way out, so an
// unjoined watcher's teardown probe would race Close() and dial a listener
// Close() had already killed, fabricating a ProxyFailure on a healthy run;
// the same lack of a happens-before edge let a genuinely dead proxy's
// failure go unobserved if the read raced the write the other way.
//
// Both tests below call runFlow directly — never WatchProxy in isolation —
// because the defect is in how runFlow sequences cancel/join/close, not in
// WatchProxy's own detection logic (which already has a passing unit test
// in retrace/capture). Run with -count=20: this is a race, and a single
// green run proves nothing about it.

// TestRunFlowHealthyProxyNeverFabricatesAFailure is the 17/20 fabrication
// case: a completely healthy standalone run must never record a
// ProxyFailure, no matter how the teardown probe's timing lands relative to
// Close().
func TestRunFlowHealthyProxyNeverFabricatesAFailure(t *testing.T) {
	t.Setenv("RETRACE_TEST_HELPER", "fetch")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	s, err := capture.StartStandalone(capture.Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	_, err = runFlow(s, runOptions{
		Cwd:     t.TempDir(),
		App:     "web",
		Flow:    "checkout",
		TestCmd: selfCmd(t, "TestHelperFetchesThroughProxy")[1:], // drop the leading "--"
		Stdout:  io.Discard,
		Stderr:  io.Discard,
		Now:     time.Now,
	})
	if err != nil {
		t.Fatalf("runFlow: %v", err)
	}
	if f := s.ProxyFailure(); f != nil {
		t.Fatalf("ProxyFailure = %+v, want nil on a healthy run — the teardown probe raced Close()", f)
	}
}

// TestRunFlowDeadProxyIsAlwaysRecorded is the missed-detection half: a
// listener that genuinely dies while the test command is running must be
// recorded every time runFlow returns, never lost to a read racing the
// watcher goroutine's write. It stands in its own listener for WatchProxy
// to monitor (by overwriting the exported Session.ProxyURL field) so the
// death can be triggered from this package without reaching into capture's
// unexported stopProxy — retrace/capture is not touched by this fix.
func TestRunFlowDeadProxyIsAlwaysRecorded(t *testing.T) {
	t.Setenv("RETRACE_TEST_HELPER", "sleep")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	s, err := capture.StartStandalone(capture.Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	fakeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	s.ProxyURL = "http://" + fakeLn.Addr().String()
	go func() {
		time.Sleep(30 * time.Millisecond)
		fakeLn.Close() // the proxy "dies" long before the 300ms helper exits
	}()

	_, err = runFlow(s, runOptions{
		Cwd:     t.TempDir(),
		App:     "web",
		Flow:    "checkout",
		TestCmd: selfCmd(t, "TestHelperSleepsBriefly")[1:], // drop the leading "--"
		Stdout:  io.Discard,
		Stderr:  io.Discard,
		Now:     time.Now,
	})
	if err != nil {
		t.Fatalf("runFlow: %v", err)
	}
	if f := s.ProxyFailure(); f == nil {
		t.Fatal("ProxyFailure = nil, want recorded — the watcher must be joined before the read")
	}
}
