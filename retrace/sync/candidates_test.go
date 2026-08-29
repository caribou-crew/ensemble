package sync

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestListReturnsCandidatesWithoutDownloading(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "Maestro iOS (Card Views)", "headBranch": "main", "event": "push", "status": "in_progress", "conclusion": "", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"},
		{"databaseId": 2, "workflowName": "Retrace Replay (Visual + Wire Regression)", "headBranch": "main", "event": "push", "status": "completed", "conclusion": "success", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T09:59:00Z"}
	]`)
	stageActor(t, 1, "octocat")
	stageActor(t, 2, "octocat")
	stageArtifactCount(t, 1, 0)
	stageArtifactCount(t, 2, 1)

	cwd := t.TempDir()
	candidates, err := List(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %v, want 2", candidates)
	}
	// Newest first.
	if candidates[0].DatabaseID != 1 || candidates[1].DatabaseID != 2 {
		t.Fatalf("candidates not newest-first: %+v", candidates)
	}
	if candidates[0].HasArtifacts {
		t.Errorf("candidate 1 HasArtifacts = true, want false")
	}
	if !candidates[1].HasArtifacts {
		t.Errorf("candidate 2 HasArtifacts = false, want true")
	}
	if candidates[0].Status != "in_progress" || candidates[1].Status != "completed" {
		t.Errorf("statuses not reported as-is: %+v", candidates)
	}
	if candidates[0].Actor != "octocat" {
		t.Errorf("Actor = %q, want octocat", candidates[0].Actor)
	}

	// List must never touch `gh run download`.
	entries, err := filepath.Glob(filepath.Join(runs.RunsRoot(cwd), "*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf(".retrace/runs is not empty after List: %v", entries)
	}
}

func TestListAppliesWorkflowGlobFilter(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "Maestro iOS (Card Views)", "status": "in_progress", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"},
		{"databaseId": 2, "workflowName": "Retrace Replay (Visual + Wire Regression)", "status": "completed", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T09:59:00Z"}
	]`)
	stageArtifactCount(t, 2, 1)

	cwd := t.TempDir()
	candidates, err := List(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now), Workflows: []string{"Retrace *"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(candidates) != 1 || candidates[0].DatabaseID != 2 {
		t.Fatalf("candidates = %+v, want only run 2", candidates)
	}
}

// List fetches each candidate's actor+artifact-count concurrently
// (candidateFetchConcurrency workers), writing into a result slot reserved
// by index rather than appended in completion order — this pins that a
// bounded-parallel fetch never mixes up which actor/artifact-count belongs
// to which run, with enough runs to exceed the worker pool and force
// several fetches to actually overlap.
func TestListParallelFetchDoesNotMisattributeRuns(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	const n = 20
	var runsJSON strings.Builder
	runsJSON.WriteString("[")
	for i := 1; i <= n; i++ {
		if i > 1 {
			runsJSON.WriteString(",")
		}
		createdAt := now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339)
		fmt.Fprintf(&runsJSON, `{"databaseId": %d, "workflowName": "Retrace Replay", "headBranch": "main", "event": "push", "status": "completed", "conclusion": "success", "headSha": "aaa%04d", "url": "https://github.com/org/repo/actions/runs/%d", "createdAt": %q}`, i, i, i, createdAt)
		stageActor(t, int64(i), fmt.Sprintf("actor-%d", i))
		stageArtifactCount(t, int64(i), i%2)
	}
	runsJSON.WriteString("]")
	writeRunListJSON(t, runsJSON.String())

	cwd := t.TempDir()
	candidates, err := List(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(candidates) != n {
		t.Fatalf("candidates = %d, want %d", len(candidates), n)
	}
	for _, c := range candidates {
		wantActor := fmt.Sprintf("actor-%d", c.DatabaseID)
		if c.Actor != wantActor {
			t.Errorf("run %d: Actor = %q, want %q", c.DatabaseID, c.Actor, wantActor)
		}
		wantHas := c.DatabaseID%2 == 1
		if c.HasArtifacts != wantHas {
			t.Errorf("run %d: HasArtifacts = %v, want %v", c.DatabaseID, c.HasArtifacts, wantHas)
		}
	}
}

func TestListRequiresAtLeastOneRepo(t *testing.T) {
	if _, err := List(Options{Cwd: t.TempDir(), From: "github"}); err == nil {
		t.Fatal("expected an error when no repo/repos is set")
	}
}
