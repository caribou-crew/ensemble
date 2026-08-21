package main

// cmd_replay_test.go drives `retrace replay` and `retrace revalidate`
// through the BUILT binary (never `go run`, which collapses every non-zero
// child to 1 — see global-constraints.md), against a bundle produced by
// `retrace run` + `retrace ref accept` rather than a hand-built one. The
// exit codes ARE the contract here — 2 for a client deviation, 1 for
// drift, 3 for no reference — so they are asserted as real process exit
// codes.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/replay"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestHelperReplayDeviates makes the recorded call AND one the recording
// never saw. It never inspects the status it gets back — that is the
// point: a test suite that would have "passed" is failed by retrace on
// the strength of the unmatched call alone.
func TestHelperReplayDeviates(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-deviate" {
		return
	}
	proxy := os.Getenv("RETRACE_PROXY_URL")
	for _, p := range []string{"/cart", "/admin/purge"} {
		resp, err := http.Get(proxy + p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch:", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	os.Exit(0)
}

// TestHelperReplayChecksHandshakeEnv asserts, inside the replayed test
// command, that replay exports the same three variables `retrace run`
// does and that the marker door behind RETRACE_MARKER_URL is live.
func TestHelperReplayChecksHandshakeEnv(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-env" {
		return
	}
	for _, k := range []string{"RETRACE_RUN_DIR", "RETRACE_PROXY_URL", "RETRACE_MARKER_URL"} {
		if os.Getenv(k) == "" {
			fmt.Fprintln(os.Stderr, "helper: "+k+" is not set")
			os.Exit(9)
		}
	}
	resp, err := http.Post(os.Getenv("RETRACE_MARKER_URL")+"/group", "application/json",
		strings.NewReader(`{"name":"login"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		fmt.Fprintln(os.Stderr, "helper marker door:", err, resp)
		os.Exit(9)
	}
	resp.Body.Close()
	// And the recorded call, so the replay itself is clean.
	got, err := http.Get(os.Getenv("RETRACE_PROXY_URL") + "/cart")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	got.Body.Close()
	os.Exit(0)
}

// recordAndAccept records one run of web/checkout against upstream and
// promotes it into the committed reference bundle.
func recordAndAccept(t *testing.T, bin, cwd, upstreamURL string) {
	t.Helper()
	runOnce(t, bin, cwd, "web", "checkout", upstreamURL)
	acc := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web")
	if acc.code != 0 {
		t.Fatalf("ref accept: exit = %d\nstdout: %s\nstderr: %s", acc.code, acc.stdout, acc.stderr)
	}
}

func TestReplayExitsTwoAndReportsTheUnmatchedRequest(t *testing.T) {
	// The spec's "client deviation caught in CI" scenario, end to end
	// through a real HTTP client.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplayDeviates")...)
	res := runRetrace(t, bin, cwd, "replay-deviate", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d — the test command exited 0, so only the unmatched call can fail this\nstdout: %s\nstderr: %s",
			res.code, exitGate, res.stdout, res.stderr)
	}
	out := res.stdout + res.stderr
	if !strings.Contains(out, "/admin/purge") {
		t.Fatalf("the report never names the unmatched call:\n%s", out)
	}
	if !strings.Contains(out, "/cart") {
		t.Fatalf("the report never names the nearest recorded exchange:\n%s", out)
	}

	// And the miss is durable, in the replay run directory.
	dir := newestReplayRunDir(t, cwd, "web", "checkout")
	b, err := os.ReadFile(filepath.Join(dir, "misses.jsonl"))
	if err != nil {
		t.Fatalf("misses.jsonl was not written to %s: %v", dir, err)
	}
	var m replay.Miss
	if err := json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]), &m); err != nil {
		t.Fatalf("misses.jsonl line is not a Miss: %v\n%s", err, b)
	}
	if m.Path != "/admin/purge" {
		t.Fatalf("misses.jsonl records %q, want /admin/purge", m.Path)
	}
}

func TestReplayExitsZeroWhenEveryCallMatched(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)
	recorded := hits.Load()

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	// The whole point of replay: the live stack was never involved. Without
	// this, a replay that quietly proxied would pass this test.
	if hits.Load() != recorded {
		t.Fatalf("the upstream saw %d call(s) during replay — replay must answer from the bundle, never from the live stack", hits.Load()-recorded)
	}
}

func TestReplayExportsTheSameHandshakeEnvAsRun(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplayChecksHandshakeEnv")...)
	res := runRetrace(t, bin, cwd, "replay-env", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — the helper exits 9 when a handshake variable is missing\nstdout: %s\nstderr: %s",
			res.code, res.stdout, res.stderr)
	}
	dir := newestReplayRunDir(t, cwd, "web", "checkout")
	if _, err := os.Stat(filepath.Join(dir, "groups.jsonl")); err != nil {
		t.Fatalf("the marker the helper posted never reached the run directory: %v", err)
	}
}

func TestReplayWithoutAReferenceExplainsHowToCreateOne(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	// 3, could-not-evaluate — NEVER 0. "No reference" means nothing was
	// checked, which is not the same as "nothing deviated".
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "retrace ref accept") {
		t.Fatalf("stderr does not name the verb that fixes this:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, "no runs captured") {
		t.Fatalf("stderr does not carry the resolver's reason:\n%s", res.stderr)
	}
}

func TestReplayJsonReportsEveryMissAndTheTestExitCode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--json"},
		selfCmd(t, "TestHelperReplayDeviates")...)
	res := runRetrace(t, bin, cwd, "replay-deviate", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d\n%s\n%s", res.code, exitGate, res.stdout, res.stderr)
	}
	var doc struct {
		App       string        `json:"app"`
		Flow      string        `json:"flow"`
		MissCount int           `json:"missCount"`
		Misses    []replay.Miss `json:"misses"`
		Ref       struct {
			Kind string `json:"kind"`
		} `json:"ref"`
		Test struct {
			ExitCode int `json:"exitCode"`
		} `json:"test"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, res.stdout)
	}
	if doc.Ref.Kind != "bundle" {
		t.Fatalf("ref.kind = %q, want \"bundle\"", doc.Ref.Kind)
	}
	if doc.MissCount != 1 || len(doc.Misses) != 1 || doc.Misses[0].Path != "/admin/purge" {
		t.Fatalf("misses = %+v (count %d)", doc.Misses, doc.MissCount)
	}
	// The test command's own code is still reported even though the exit
	// status is retrace's: "the tests passed and the client deviated" is
	// two facts, not one.
	if doc.Test.ExitCode != 0 {
		t.Fatalf("test.exitCode = %d, want 0", doc.Test.ExitCode)
	}
	if doc.App != "web" || doc.Flow != "checkout" {
		t.Fatalf("app/flow = %q/%q", doc.App, doc.Flow)
	}
}

func TestRevalidateReportsDriftAgainstTheLiveStackAndExitsOne(t *testing.T) {
	var moved atomic.Bool
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if moved.Load() {
			w.Write([]byte(`{"total":199,"currency":"EUR"}`))
			return
		}
		w.Write([]byte(`{"total":199,"currency":"USD"}`))
	}))
	defer live.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, live.URL)

	// Clean first: the recording still holds, so exit 0. Without this arm
	// the drift arm below could pass on a revalidate that flags anything.
	clean := runRetrace(t, bin, cwd, "", "revalidate", "--ref", "checkout", "--app", "web", "--upstream", live.URL)
	if clean.code != 0 {
		t.Fatalf("revalidate against an unchanged stack: exit = %d, want 0\nstdout: %s\nstderr: %s", clean.code, clean.stdout, clean.stderr)
	}

	moved.Store(true)
	res := runRetrace(t, bin, cwd, "", "revalidate", "--ref", "checkout", "--app", "web", "--upstream", live.URL, "--json")
	if res.code != exitDiff {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitDiff, res.stdout, res.stderr)
	}
	var rep replay.RevalReport
	if err := json.Unmarshal([]byte(res.stdout), &rep); err != nil {
		t.Fatalf("--json stdout is not a RevalReport: %v\n%s", err, res.stdout)
	}
	if rep.Verdict != replay.VerdictDrift || rep.Checked != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Drifts) != 1 || len(rep.Drifts[0].Fields) != 1 || rep.Drifts[0].Fields[0].Path != "currency" {
		t.Fatalf("drifts = %+v, want the currency field named", rep.Drifts)
	}
}

func TestRevalidateWithoutAReferenceExplainsHowToCreateOne(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	res := runRetrace(t, bin, cwd, "", "revalidate", "--ref", "checkout", "--app", "web", "--upstream", "http://127.0.0.1:1")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "retrace ref accept") {
		t.Fatalf("stderr does not name the verb that fixes this:\n%s", res.stderr)
	}
}

// newestReplayRunDir returns the replay run directory `retrace replay`
// created. Replays live under their OWN root, never the runs root: a
// replay is not a recording, and a directory with no manifest sitting in
// the runs root would become what `--b latest` resolves to.
func newestReplayRunDir(t *testing.T, cwd, app, flow string) string {
	t.Helper()
	root := replaysRoot(cwd)
	ids := runs.ListRuns(root, app, flow)
	if len(ids) == 0 {
		t.Fatalf("no replay run directory under %s/%s/%s", root, app, flow)
	}
	p, err := runs.PathsFor(root, app, flow, ids[len(ids)-1])
	if err != nil {
		t.Fatal(err)
	}
	return p.RunDir
}

// TestReplayRunDirsNeverPolluteTheRunsRoot pins that separation: a replay
// must not add a manifest-less directory to the runs root, where
// `retrace diff --b latest` would resolve to it.
func TestReplayRunDirsNeverPolluteTheRunsRoot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)
	before := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout")

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	if res := runRetrace(t, bin, cwd, "fetch", args...); res.code != 0 {
		t.Fatalf("replay: exit = %d\n%s\n%s", res.code, res.stdout, res.stderr)
	}
	after := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout")
	if len(after) != len(before) {
		t.Fatalf("the runs root gained %d directory/ies during a replay: %v -> %v", len(after)-len(before), before, after)
	}
}
