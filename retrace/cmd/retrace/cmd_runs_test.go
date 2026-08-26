package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/capture"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

var fabricateSeq atomic.Int64

// fabricateRun writes a run directory in whatever supervision state the
// test needs, WITHOUT running a capture.
//
// The abandoned cases use the age fallback (no owner record, an old run id)
// rather than an owner naming a dead pid. A test that wanted a dead pid
// would have to reap a real process and hope the number was not reused
// before the assertion ran; the age path reaches the same StateAbandoned
// verdict with no dependency on the OS pid allocator. The dead-owner rule
// itself is pinned in retrace/runs, where processAlive can be replaced.
func fabricateRun(t *testing.T, cwd, app, flow string, age time.Duration, finalize bool) string {
	t.Helper()
	started := time.Now().Add(-age)
	// NewRunID has 1s resolution, so two runs fabricated with the same age
	// in the same second would collide on the directory name. The sha half
	// of the id is what keeps them distinct — the same escape hatch a CI
	// matrix recording one flow twice inside a second relies on.
	id := runs.NewRunID(started, fmt.Sprintf("fab%04d000", fabricateSeq.Add(1)))
	p, err := runs.Create(runs.RunsRoot(cwd), app, flow, id)
	if err != nil {
		t.Fatalf("Create %s/%s: %v", app, flow, err)
	}
	if finalize {
		if err := runs.Finalize(p, runs.Finalized{RunID: id, FinishedAt: started.Add(time.Minute)}); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
	}
	return id
}

func decodeRuns(t *testing.T, out string) runsResult {
	t.Helper()
	var got runsResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode runs JSON: %v\n%s", err, out)
	}
	return got
}

func TestRunsReportsCompleteAndAbandonedSeparately(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	doneID := fabricateRun(t, cwd, "web", "checkout", 2*time.Hour, true)
	// Older than the fallback bound and never finalized: abandoned.
	lostID := fabricateRun(t, cwd, "web", "checkout", 2*time.Hour, false)
	// Younger than the bound and never finalized: too young to judge, so
	// running. This row is what stops a rule of "no sentinel = abandoned"
	// from passing this test.
	freshID := fabricateRun(t, cwd, "web", "cart", time.Minute, false)

	res := runRetrace(t, bin, cwd, "", "runs", "--json")
	if res.code != exitOK {
		t.Fatalf("exit = %d, want 0 (runs is a report, not a gate)\n%s", res.code, res.stderr)
	}
	got := decodeRuns(t, res.stdout)
	states := map[string]runs.State{}
	for _, r := range got.Runs {
		states[r.RunID] = r.State
	}
	for id, want := range map[string]runs.State{
		doneID: runs.StateComplete, lostID: runs.StateAbandoned, freshID: runs.StateRunning,
	} {
		if states[id] != want {
			t.Errorf("run %s state = %q, want %q", id, states[id], want)
		}
	}
	if got.Counts[runs.StateAbandoned] != 1 {
		t.Errorf("counts[abandoned] = %d, want 1", got.Counts[runs.StateAbandoned])
	}
	if got.Counts[runs.StateComplete] != 1 {
		t.Errorf("counts[complete] = %d, want 1", got.Counts[runs.StateComplete])
	}
}

func TestRunsAbandonedRowCarriesAnAuditableReason(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	fabricateRun(t, cwd, "web", "checkout", 3*time.Hour, false)

	res := runRetrace(t, bin, cwd, "", "runs")
	if res.code != exitOK {
		t.Fatalf("exit = %d, want 0\n%s", res.code, res.stderr)
	}
	// A verdict a human cannot audit is one they override blindly, so the
	// rule that produced it has to be on the page.
	if !strings.Contains(res.stdout, "abandoned") {
		t.Errorf("listing never says abandoned:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "finalized") {
		t.Errorf("listing does not name the missing sentinel:\n%s", res.stdout)
	}
}

func TestRunsFiltersByAppFlowAndState(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	fabricateRun(t, cwd, "web", "checkout", 2*time.Hour, true)
	fabricateRun(t, cwd, "web", "cart", 2*time.Hour, false)
	fabricateRun(t, cwd, "admin", "login", 2*time.Hour, true)

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"no filter", []string{"runs", "--json"}, 3},
		{"by app", []string{"runs", "--json", "--app", "web"}, 2},
		{"by flow", []string{"runs", "--json", "--flow", "checkout"}, 1},
		{"by state", []string{"runs", "--json", "--state", "abandoned"}, 1},
		{"app and state together", []string{"runs", "--json", "--app", "web", "--state", "complete"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runRetrace(t, bin, cwd, "", tc.args...)
			if res.code != exitOK {
				t.Fatalf("exit = %d, want 0\n%s", res.code, res.stderr)
			}
			if got := decodeRuns(t, res.stdout); len(got.Runs) != tc.want {
				t.Errorf("got %d runs, want %d: %+v", len(got.Runs), tc.want, got.Runs)
			}
		})
	}
}

func TestRunsRejectsAnUnknownState(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	res := runRetrace(t, bin, cwd, "", "runs", "--state", "wedged")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d — a typo'd filter that silently matched nothing would read as 'no such runs'", res.code, exitUsage)
	}
	if !strings.Contains(res.stderr, "wedged") {
		t.Errorf("error does not name the rejected value:\n%s", res.stderr)
	}
}

func TestRunsAbandonedAfterIsHonoured(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	fabricateRun(t, cwd, "web", "checkout", 10*time.Minute, false)

	// Default bound is 15m, so a 10m-old run is "running"...
	res := runRetrace(t, bin, cwd, "", "runs", "--json")
	if got := decodeRuns(t, res.stdout); got.Counts[runs.StateAbandoned] != 0 {
		t.Errorf("with the default bound, abandoned = %d, want 0", got.Counts[runs.StateAbandoned])
	}
	// ...and tightening the bound past its age must flip it.
	res = runRetrace(t, bin, cwd, "", "runs", "--json", "--abandoned-after", "1m")
	if got := decodeRuns(t, res.stdout); got.Counts[runs.StateAbandoned] != 1 {
		t.Errorf("with --abandoned-after=1m, abandoned = %d, want 1", got.Counts[runs.StateAbandoned])
	}
}

// TestRunFinalizesTheRunDirectory is the integration assertion the whole
// feature rests on: a real capture must leave the sentinel behind, and must
// clear the owner record it wrote at the start. Without this, every unit
// test above is asserting on a state the product never actually produces.
func TestRunFinalizesTheRunDirectory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")
	id := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	p, err := runs.PathsFor(runs.RunsRoot(cwd), "web", "checkout", id)
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	if _, err := os.Stat(p.FinalizedPath()); err != nil {
		t.Fatalf("a completed run left no finalized sentinel: %v", err)
	}
	if _, err := os.Stat(p.RunningPath()); !os.IsNotExist(err) {
		t.Errorf("running.json survived a completed run (stat err = %v) — a finished run must not advertise a live owner", err)
	}
	done, err := runs.ReadFinalized(p)
	if err != nil || done == nil {
		t.Fatalf("ReadFinalized = (%v, %v)", done, err)
	}
	if done.RunID != id {
		t.Errorf("finalized names run %q, want %q", done.RunID, id)
	}

	// And the listing must agree with the disk.
	res := runRetrace(t, bin, cwd, "", "runs", "--json")
	got := decodeRuns(t, res.stdout)
	if len(got.Runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(got.Runs))
	}
	if got.Runs[0].State != runs.StateComplete {
		t.Errorf("state = %q, want complete (reason: %s)", got.Runs[0].State, got.Runs[0].Reason)
	}
}

func TestHumanAge(t *testing.T) {
	for _, tc := range []struct {
		sec  int
		want string
	}{
		{5, "5s"},
		{59, "59s"},
		{60, "1m"},
		{3599, "59m"},
		{3600, "1h0m"},
		{3661, "1h1m"},
		{86400, "1d0h"},
		{90000, "1d1h"},
		// A run id stamped in the future must not round to "0s" — it makes
		// every other age on the page suspect and the user needs to know.
		{-30, "in 30s"},
	} {
		if got := humanAge(tc.sec); got != tc.want {
			t.Errorf("humanAge(%d) = %q, want %q", tc.sec, got, tc.want)
		}
	}
}

func TestSummarizeIsOrderedAndDropsNothing(t *testing.T) {
	got := summarize(map[runs.State]int{
		runs.StateAbandoned: 2, runs.StateComplete: 5, runs.StateRunning: 1,
	})
	if got != "5 complete, 1 running, 2 abandoned" {
		t.Errorf("summarize = %q, want a fixed complete/running/abandoned order", got)
	}
	if got := summarize(map[runs.State]int{runs.StateComplete: 0}); got != "0 runs" {
		t.Errorf("empty summarize = %q, want %q", got, "0 runs")
	}
	// A State constant added later must surface rather than be rounded away.
	got = summarize(map[runs.State]int{runs.StateComplete: 1, runs.State("wedged"): 3})
	if !strings.Contains(got, "3 wedged") {
		t.Errorf("summarize dropped an unknown state: %q", got)
	}
}

// TestAFailedManifestWriteDoesNotFinalize pins the ORDERING the sentinel's
// whole guarantee rests on: finalized is written after manifest.json, and
// only if it landed.
//
// The happy path cannot see this — both writes succeed either way — so the
// manifest write is deliberately made to fail (a directory where the file
// should go, so os.WriteFile gets EISDIR). Without this, swapping the two
// statements is a silent change that ships a run advertising itself as
// complete while holding no results at all.
func TestAFailedManifestWriteDoesNotFinalize(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cwd := t.TempDir()
	s, err := capture.StartStandalone(capture.Options{
		Cwd: cwd, App: "web", Flow: "checkout", Upstream: upstream.URL,
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	// A directory where manifest.json belongs: os.WriteFile cannot truncate
	// a directory, so WriteManifest fails without any other behaviour
	// changing.
	if err := os.Mkdir(s.Paths.ManifestPath, 0o755); err != nil {
		t.Fatalf("sabotage manifest path: %v", err)
	}

	var out, errOut strings.Builder
	_, err = runFlow(s, runOptions{
		Cwd: cwd, App: "web", Flow: "checkout",
		TestCmd: []string{"/bin/sh", "-c", "true"},
		Stdout:  &out, Stderr: &errOut, Now: time.Now,
	})
	if err == nil {
		t.Fatal("runFlow reported success though the manifest could not be written")
	}
	if _, statErr := os.Stat(s.Paths.FinalizedPath()); !os.IsNotExist(statErr) {
		t.Fatalf("a run whose manifest failed to write was still finalized (stat err = %v) — finalized must be the LAST write, and only if the manifest landed", statErr)
	}
	// And it must therefore surface as un-finalized rather than clean.
	st, err := runs.Status(s.Paths, "web", "checkout", s.RunID, time.Now(), runs.DefaultAbandonAfter)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State == runs.StateComplete {
		t.Error("a run with no manifest reports as complete")
	}
}
