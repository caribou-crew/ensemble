package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow is the reference clock for every age assertion here. Ages are
// computed against it explicitly, never against time.Now(), so a slow test
// machine cannot flip a "young" case into an "abandoned" one.
var fixedNow = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

// runIDAt builds the timestamp-first run id NewRunID would produce at t, so
// the age-fallback tests exercise the real parser rather than a shape the
// production writer never emits.
func runIDAt(t time.Time) string { return NewRunID(t, "abcdef1234") }

func newRun(t *testing.T, runID string) Paths {
	t.Helper()
	root := t.TempDir()
	p, err := Create(root, "shop", "checkout", runID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return p
}

// pinAlive replaces the process-liveness probe for one test. Swapping the
// package var is what keeps these tests from forking real children and
// asserting on the OS scheduler.
func pinAlive(t *testing.T, alive func(pid int) bool) {
	t.Helper()
	prev := processAlive
	processAlive = alive
	t.Cleanup(func() { processAlive = prev })
}

func TestMarkRunningStampsSchemaAndOwnPID(t *testing.T) {
	started := fixedNow.Add(-time.Minute)
	p := newRun(t, runIDAt(started))
	// PID deliberately wrong on the way in: MarkRunning must stamp the
	// real one, so there is exactly one answer to "which process is this".
	if err := MarkRunning(p, Running{PID: 999999, App: "shop", Flow: "checkout", RunID: p.RunDir, StartedAt: started}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	got, err := ReadRunning(p)
	if err != nil {
		t.Fatalf("ReadRunning: %v", err)
	}
	if got == nil {
		t.Fatal("ReadRunning returned nil after MarkRunning")
	}
	if got.PID != os.Getpid() {
		t.Errorf("pid = %d, want this process %d", got.PID, os.Getpid())
	}
	if got.Schema != SuperviseSchema {
		t.Errorf("schema = %q, want %q", got.Schema, SuperviseSchema)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("startedAt = %v, want %v", got.StartedAt, started)
	}
}

func TestMarkRunningRejectsZeroStartedAt(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	err := MarkRunning(p, Running{App: "shop"})
	if err == nil {
		t.Fatal("MarkRunning accepted a zero startedAt; a run with no start time cannot be aged")
	}
	if _, statErr := os.Stat(p.RunningPath()); statErr == nil {
		t.Error("a rejected MarkRunning still wrote running.json")
	}
}

func TestFinalizeWritesSentinelAndClearsOwner(t *testing.T) {
	started := fixedNow.Add(-2 * time.Minute)
	p := newRun(t, runIDAt(started))
	if err := MarkRunning(p, Running{StartedAt: started}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := Finalize(p, Finalized{RunID: "r1", FinishedAt: fixedNow, ExitCode: 3}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	done, err := ReadFinalized(p)
	if err != nil {
		t.Fatalf("ReadFinalized: %v", err)
	}
	if done == nil {
		t.Fatal("no finalized record after Finalize")
	}
	if done.ExitCode != 3 {
		t.Errorf("exitCode = %d, want 3", done.ExitCode)
	}
	if done.Schema != SuperviseSchema {
		t.Errorf("schema = %q, want %q", done.Schema, SuperviseSchema)
	}
	// The owner record must be gone: a finalized run that still advertises
	// a live pid is the exact confusion these sentinels exist to end.
	if _, err := os.Stat(p.RunningPath()); !os.IsNotExist(err) {
		t.Errorf("running.json survived Finalize (stat err = %v)", err)
	}
}

func TestFinalizeRejectsZeroFinishedAt(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	if err := Finalize(p, Finalized{RunID: "r1"}); err == nil {
		t.Fatal("Finalize accepted a zero finishedAt")
	}
	if _, err := os.Stat(p.FinalizedPath()); err == nil {
		t.Error("a rejected Finalize still wrote the sentinel")
	}
}

func TestFinalizeIsFineWithNoOwnerRecord(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	if err := Finalize(p, Finalized{RunID: "r1", FinishedAt: fixedNow}); err != nil {
		t.Fatalf("Finalize with no running.json: %v", err)
	}
}

func TestSentinelWritesLeaveNoTempFiles(t *testing.T) {
	started := fixedNow.Add(-time.Minute)
	p := newRun(t, runIDAt(started))
	if err := MarkRunning(p, Running{StartedAt: started}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := Finalize(p, Finalized{RunID: "r1", FinishedAt: fixedNow}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	entries, err := os.ReadDir(p.RunDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestStatusComplete(t *testing.T) {
	started := fixedNow.Add(-time.Hour)
	id := runIDAt(started)
	p := newRun(t, id)
	if err := Finalize(p, Finalized{RunID: id, FinishedAt: fixedNow}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	// Liveness must not matter once finalized: pinned dead, still complete.
	pinAlive(t, func(int) bool { return false })
	st, err := Status(p, "shop", "checkout", id, fixedNow, DefaultAbandonAfter)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != StateComplete {
		t.Errorf("state = %q, want %q (reason: %s)", st.State, StateComplete, st.Reason)
	}
	if st.Done == nil {
		t.Error("complete run reported no Done record")
	}
}

func TestStatusRunningWhenOwnerAlive(t *testing.T) {
	// Deliberately older than the fallback bound: a live owner must beat
	// the age rule, or a legitimately long suite reads as abandoned.
	started := fixedNow.Add(-3 * DefaultAbandonAfter)
	id := runIDAt(started)
	p := newRun(t, id)
	if err := MarkRunning(p, Running{StartedAt: started}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	pinAlive(t, func(int) bool { return true })
	st, err := Status(p, "shop", "checkout", id, fixedNow, DefaultAbandonAfter)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != StateRunning {
		t.Errorf("state = %q, want %q (reason: %s)", st.State, StateRunning, st.Reason)
	}
	if st.Owner == nil {
		t.Fatal("running run reported no Owner")
	}
	if st.AgeSeconds != int(3*DefaultAbandonAfter/time.Second) {
		t.Errorf("ageSeconds = %d, want %d", st.AgeSeconds, int(3*DefaultAbandonAfter/time.Second))
	}
}

func TestStatusAbandonedWhenOwnerGone(t *testing.T) {
	// Deliberately YOUNGER than the fallback bound: a dead owner must beat
	// the age rule in the other direction too, or a crash 10s in stays
	// "running" for another 15 minutes.
	started := fixedNow.Add(-10 * time.Second)
	id := runIDAt(started)
	p := newRun(t, id)
	if err := MarkRunning(p, Running{StartedAt: started}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	pinAlive(t, func(int) bool { return false })
	st, err := Status(p, "shop", "checkout", id, fixedNow, DefaultAbandonAfter)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != StateAbandoned {
		t.Errorf("state = %q, want %q (reason: %s)", st.State, StateAbandoned, st.Reason)
	}
	if !strings.Contains(st.Reason, "gone") {
		t.Errorf("reason %q does not say the owner is gone", st.Reason)
	}
}

func TestStatusAgeFallbackWithNoOwner(t *testing.T) {
	// processAlive must be irrelevant here — there is no pid to probe. Pin
	// it to true so a rule that reached for it would produce "running" and
	// fail the old case below.
	pinAlive(t, func(int) bool { return true })
	cases := []struct {
		name  string
		since time.Duration
		want  State
	}{
		{"young", DefaultAbandonAfter - time.Minute, StateRunning},
		{"old", DefaultAbandonAfter + time.Minute, StateAbandoned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := fixedNow.Add(-tc.since)
			id := runIDAt(started)
			p := newRun(t, id)
			st, err := Status(p, "shop", "checkout", id, fixedNow, DefaultAbandonAfter)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if st.State != tc.want {
				t.Errorf("state = %q, want %q (reason: %s)", st.State, tc.want, st.Reason)
			}
			if st.Owner != nil {
				t.Error("Owner should be nil when no running.json exists")
			}
			if st.AgeSeconds != int(tc.since/time.Second) {
				t.Errorf("ageSeconds = %d, want %d (age must come from the run id)", st.AgeSeconds, int(tc.since/time.Second))
			}
		})
	}
}

func TestStatusUnparseableRunIDWithNoOwnerIsAbandoned(t *testing.T) {
	pinAlive(t, func(int) bool { return true })
	p := newRun(t, "not-a-timestamp")
	st, err := Status(p, "shop", "checkout", "not-a-timestamp", fixedNow, DefaultAbandonAfter)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != StateAbandoned {
		t.Errorf("state = %q, want %q (reason: %s)", st.State, StateAbandoned, st.Reason)
	}
}

func TestStatusCorruptOwnerFallsBackToAgeAndSaysSo(t *testing.T) {
	started := fixedNow.Add(-DefaultAbandonAfter - time.Minute)
	id := runIDAt(started)
	p := newRun(t, id)
	if err := os.WriteFile(p.RunningPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt owner: %v", err)
	}
	pinAlive(t, func(int) bool { return true })
	st, err := Status(p, "shop", "checkout", id, fixedNow, DefaultAbandonAfter)
	if err != nil {
		t.Fatalf("Status must not fail on a corrupt owner record: %v", err)
	}
	if st.State != StateAbandoned {
		t.Errorf("state = %q, want %q (reason: %s)", st.State, StateAbandoned, st.Reason)
	}
	if !strings.Contains(st.Reason, "unreadable") {
		t.Errorf("reason %q does not disclose the unreadable owner record", st.Reason)
	}
}

func TestReadSentinelsRejectWrongSchema(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	write := func(path string, v map[string]any) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(p.RunningPath(), map[string]any{"schema": "retrace/supervise/999", "pid": 1})
	if _, err := ReadRunning(p); err == nil {
		t.Error("ReadRunning accepted a future schema")
	}
	write(p.FinalizedPath(), map[string]any{"schema": "retrace/supervise/999"})
	if _, err := ReadFinalized(p); err == nil {
		t.Error("ReadFinalized accepted a future schema")
	}
}

func TestReadSentinelsMissingIsNotAnError(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	r, err := ReadRunning(p)
	if err != nil || r != nil {
		t.Errorf("ReadRunning on a fresh dir = (%v, %v), want (nil, nil)", r, err)
	}
	f, err := ReadFinalized(p)
	if err != nil || f != nil {
		t.Errorf("ReadFinalized on a fresh dir = (%v, %v), want (nil, nil)", f, err)
	}
}

func TestStatusAllCoversEveryRunInChronologicalOrder(t *testing.T) {
	root := t.TempDir()
	t0 := fixedNow.Add(-2 * time.Hour)
	t1 := fixedNow.Add(-time.Hour)
	idOld, idNew := runIDAt(t0), runIDAt(t1)

	pOld, err := Create(root, "shop", "checkout", idOld)
	if err != nil {
		t.Fatalf("Create old: %v", err)
	}
	if err := Finalize(pOld, Finalized{RunID: idOld, FinishedAt: t0.Add(time.Minute)}); err != nil {
		t.Fatalf("Finalize old: %v", err)
	}
	pNew, err := Create(root, "shop", "checkout", idNew)
	if err != nil {
		t.Fatalf("Create new: %v", err)
	}
	if err := MarkRunning(pNew, Running{StartedAt: t1}); err != nil {
		t.Fatalf("MarkRunning new: %v", err)
	}
	// A second app, to prove the walk is not single-app.
	pOther, err := Create(root, "admin", "login", idNew)
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	_ = pOther

	pinAlive(t, func(int) bool { return false })
	all, err := StatusAll(root, fixedNow, DefaultAbandonAfter)
	if err != nil {
		t.Fatalf("StatusAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d runs, want 3: %+v", len(all), all)
	}
	// ListApps sorts, so admin precedes shop; within a flow, run ids sort
	// chronologically because NewRunID is timestamp-first.
	if all[0].App != "admin" {
		t.Errorf("all[0].App = %q, want admin (apps must be sorted)", all[0].App)
	}
	shop := all[1:]
	if shop[0].RunID != idOld || shop[1].RunID != idNew {
		t.Errorf("shop runs out of chronological order: %q then %q", shop[0].RunID, shop[1].RunID)
	}
	if shop[0].State != StateComplete {
		t.Errorf("old run state = %q, want complete", shop[0].State)
	}
	if shop[1].State != StateAbandoned {
		t.Errorf("new run state = %q, want abandoned (owner pinned dead)", shop[1].State)
	}
}

func TestStatusAllEmptyRootIsEmptyNotAnError(t *testing.T) {
	all, err := StatusAll(filepath.Join(t.TempDir(), "never-written"), fixedNow, DefaultAbandonAfter)
	if err != nil {
		t.Fatalf("StatusAll on a missing root: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("got %d runs, want 0", len(all))
	}
}
