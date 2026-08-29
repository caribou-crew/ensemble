package sync

import (
	"path/filepath"
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

func TestListRequiresAtLeastOneRepo(t *testing.T) {
	if _, err := List(Options{Cwd: t.TempDir(), From: "github"}); err == nil {
		t.Fatal("expected an error when no repo/repos is set")
	}
}
