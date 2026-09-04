package sync

import (
	"fmt"
	"testing"
	"time"
)

func TestListBranchesGroupsByBranchKeepingMostRecentRun(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "Retrace Web", "headBranch": "main", "event": "push", "status": "completed", "conclusion": "success", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T09:00:00Z"},
		{"databaseId": 2, "workflowName": "Retrace Web", "headBranch": "main", "event": "push", "status": "completed", "conclusion": "success", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T10:00:00Z"},
		{"databaseId": 3, "workflowName": "Retrace Web", "headBranch": "e2e/checkout-fix", "event": "workflow_dispatch", "status": "completed", "conclusion": "success", "headSha": "ccc3333", "url": "https://github.com/org/repo/actions/runs/3", "createdAt": "2026-08-27T08:00:00Z"}
	]`)

	branches, err := ListBranches(Options{From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %+v, want 2 entries (main, e2e/checkout-fix)", branches)
	}
	byName := map[string]BranchCandidate{}
	for _, b := range branches {
		byName[b.Name] = b
	}
	main, ok := byName["main"]
	if !ok {
		t.Fatalf("branches = %+v, missing \"main\"", branches)
	}
	if !main.LastRunAt.Equal(time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("main.LastRunAt = %v, want the newer of its two runs (10:00, not 09:00)", main.LastRunAt)
	}
	if _, ok := byName["e2e/checkout-fix"]; !ok {
		t.Fatalf("branches = %+v, missing \"e2e/checkout-fix\"", branches)
	}
}

func TestListBranchesAppliesBranchGlobFilter(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "Retrace Web", "headBranch": "main", "event": "push", "status": "completed", "conclusion": "success", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"},
		{"databaseId": 2, "workflowName": "Retrace Web", "headBranch": "e2e/checkout-fix", "event": "workflow_dispatch", "status": "completed", "conclusion": "success", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T10:00:00Z"},
		{"databaseId": 3, "workflowName": "Retrace Web", "headBranch": "dependabot/bump-x", "event": "push", "status": "completed", "conclusion": "success", "headSha": "ccc3333", "url": "https://github.com/org/repo/actions/runs/3", "createdAt": "2026-08-27T10:00:00Z"}
	]`)

	branches, err := ListBranches(Options{From: "github", Repo: "org/repo", Now: fixedNow(t, now), Branches: []string{"main", "e2e/*"}})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %+v, want 2 (main, e2e/checkout-fix), not dependabot/bump-x", branches)
	}
	for _, b := range branches {
		if b.Name == "dependabot/bump-x" {
			t.Fatalf("branches = %+v, dependabot/bump-x must be filtered out", branches)
		}
	}
}

func TestListBranchesEmptyFilterReturnsEveryBranch(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "Retrace Web", "headBranch": "main", "event": "push", "status": "completed", "conclusion": "success", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"},
		{"databaseId": 2, "workflowName": "Retrace Web", "headBranch": "dependabot/bump-x", "event": "push", "status": "completed", "conclusion": "success", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T10:00:00Z"}
	]`)

	branches, err := ListBranches(Options{From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %+v, want 2 — an empty Branches filter must not exclude anything", branches)
	}
}

func TestListBranchesAppliesWorkflowFilter(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "Retrace Web", "headBranch": "main", "event": "push", "status": "completed", "conclusion": "success", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"},
		{"databaseId": 2, "workflowName": "Some Other CI", "headBranch": "unrelated-branch", "event": "push", "status": "completed", "conclusion": "success", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T10:00:00Z"}
	]`)

	branches, err := ListBranches(Options{From: "github", Repo: "org/repo", Now: fixedNow(t, now), Workflows: []string{"Retrace *"}})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 1 || branches[0].Name != "main" {
		t.Fatalf("branches = %+v, want only [main] — unrelated-branch only ran \"Some Other CI\"", branches)
	}
}

func TestListBranchesExcludesRunsOlderThanSince(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, fmt.Sprintf(`[
		{"databaseId": 1, "workflowName": "Retrace Web", "headBranch": "main", "event": "push", "status": "completed", "conclusion": "success", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": %q},
		{"databaseId": 2, "workflowName": "Retrace Web", "headBranch": "e2e/stale", "event": "push", "status": "completed", "conclusion": "success", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": %q}
	]`, now.Add(-1*time.Hour).Format(time.RFC3339), now.Add(-40*24*time.Hour).Format(time.RFC3339)))

	branches, err := ListBranches(Options{From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 1 || branches[0].Name != "main" {
		t.Fatalf("branches = %+v, want only [main] — e2e/stale's only run is 40 days old, outside DefaultBranchSince (30d)", branches)
	}
}
