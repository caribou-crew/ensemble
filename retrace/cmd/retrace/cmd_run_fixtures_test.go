package main

// cmd_run_fixtures_test.go drives `retrace run --fixtures` through the
// BUILT binary (see cmd_replay_test.go's header comment for why never `go
// run`), against a bundle produced by `retrace run` + `retrace ref accept`
// — the same fixture-setup path cmd_replay_test.go's recordAndAccept
// already established. See docs/superpowers/specs/
// 2026-08-30-retrace-run-fixtures-design.md for the design.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/replay"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// newestRunID returns the app/flow run id present after this call but not
// in before — the same "diff the listing" idiom runOnce (cmd_diff_test.go)
// uses to find its own new run without a manifest-count assumption. Needed
// here because recordAndAccept already leaves one run directory behind
// under runs.RunsRoot before the --fixtures run being tested happens.
func newestRunID(t *testing.T, cwd, app, flow string, before map[string]bool) string {
	t.Helper()
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), app, flow) {
		if !before[id] {
			return id
		}
	}
	t.Fatalf("no new run directory for %s/%s", app, flow)
	return ""
}

func runIDSet(t *testing.T, cwd, app, flow string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), app, flow) {
		out[id] = true
	}
	return out
}

// TestRunFixturesServesReferenceBundleAndReachesVerdictOK is the core
// happy path: a flow's own accepted reference bundle stands in for a live
// upstream while `run` records, and — because every call the app makes
// matches the bundle — the resulting manifest earns a genuine VerdictOK
// through capture.Assess's ordinary logic, untouched.
func TestRunFixturesServesReferenceBundleAndReachesVerdictOK(t *testing.T) {
	var liveHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		liveHits.Add(1)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)
	settlePastRunIDResolution()
	afterAccept := liveHits.Load()
	before := runIDSet(t, cwd, "web", "checkout")

	args := append([]string{"run", "--fixtures", "--flow", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	// The whole point: the live stack is never involved once fixtures are
	// serving. Without this check, a --fixtures run that quietly forwarded
	// to the real upstream would still pass every assertion below.
	if liveHits.Load() != afterAccept {
		t.Fatalf("the live upstream saw %d more call(s) during the --fixtures run — it must answer from the bundle only", liveHits.Load()-afterAccept)
	}

	id := newestRunID(t, cwd, "web", "checkout", before)
	p, err := runs.PathsFor(runs.RunsRoot(cwd), "web", "checkout", id)
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Capture.Status != trace.VerdictOK {
		t.Fatalf("capture.status = %q, want %q; reasons: %+v", m.Capture.Status, trace.VerdictOK, m.Capture.Reasons)
	}
	if m.Wire.Calls != 1 {
		t.Fatalf("wire.calls = %d, want 1", m.Wire.Calls)
	}
	if m.Fixtures == nil {
		t.Fatal("manifest.fixtures is nil, want provenance for a --fixtures run")
	}
	if m.Fixtures.Ref != "checkout" || m.Fixtures.Served != 1 || m.Fixtures.MissCount != 0 {
		t.Fatalf("manifest.fixtures = %+v, want ref=checkout served=1 missCount=0", m.Fixtures)
	}
}

// TestRunFixturesMissForcesExitGateRegardlessOfTestExitCode covers D4: a
// call the reference bundle does not cover is a hard gate that overrides
// even a test command that itself exits 0 — the same rule `retrace
// replay` already applies, and for the same reason: a green suite that
// quietly ate a synthetic 501 is the interesting failure, not a pass.
func TestRunFixturesMissForcesExitGateRegardlessOfTestExitCode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)
	settlePastRunIDResolution()
	before := runIDSet(t, cwd, "web", "checkout")

	// TestHelperReplayDeviates (cmd_replay_test.go) fetches /cart (recorded)
	// then /admin/purge (never recorded), and always exits 0 itself.
	args := append([]string{"run", "--fixtures", "--flow", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplayDeviates")...)
	res := runRetrace(t, bin, cwd, "replay-deviate", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d — the test command exited 0, so only the unmatched call can fail this\nstdout: %s\nstderr: %s",
			res.code, exitGate, res.stdout, res.stderr)
	}

	// The recording is still real and worth keeping (D4: "the run
	// completed, its manifest is on disk and readable").
	id := newestRunID(t, cwd, "web", "checkout", before)
	p, err := runs.PathsFor(runs.RunsRoot(cwd), "web", "checkout", id)
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Fixtures == nil || m.Fixtures.MissCount != 1 {
		t.Fatalf("manifest.fixtures = %+v, want missCount=1", m.Fixtures)
	}

	dir := filepath.Dir(p.ManifestPath)
	b, err := os.ReadFile(filepath.Join(dir, "misses.jsonl"))
	if err != nil {
		t.Fatalf("misses.jsonl was not written to %s: %v", dir, err)
	}
	var miss replay.Miss
	if err := json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]), &miss); err != nil {
		t.Fatalf("misses.jsonl line is not a Miss: %v\n%s", err, b)
	}
	if miss.Path != "/admin/purge" {
		t.Fatalf("misses.jsonl records %q, want /admin/purge", miss.Path)
	}
}

// TestRunFixturesWithoutAReferenceExplainsHowToCreateOne mirrors
// TestReplayWithoutAReferenceExplainsHowToCreateOne (cmd_replay_test.go):
// a flow with no accepted reference bundle refuses with the same
// candidate-listing message, never a bare stack trace or an empty
// "no reference" line.
func TestRunFixturesWithoutAReferenceExplainsHowToCreateOne(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)

	args := append([]string{"run", "--fixtures", "--flow", "checkout", "--app", "web"}, selfCmd(t, "unused")...)
	res := runRetrace(t, bin, cwd, "", args...)
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "retrace ref accept") {
		t.Fatalf("stderr does not explain how to create a reference:\n%s", res.stderr)
	}
	if ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout"); len(ids) != 0 {
		t.Fatalf("a run directory was written for a refused capture: %v", ids)
	}
}
