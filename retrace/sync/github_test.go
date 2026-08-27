package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// fakeGH puts a script named "gh" on PATH for the duration of the test.
// It answers exactly two subcommands:
//
//   - `gh run list ...`     -> prints the contents of the file at
//     $GH_FAKE_RUN_LIST_JSON
//   - `gh run download <id> --dir <dir> ...` -> copies
//     $GH_FAKE_DOWNLOAD_SRC/<id>/* into <dir>, if that directory exists
//
// Both env vars are set per test via t.Setenv, so one fake binary serves
// every test case in this file.
func fakeGH(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a POSIX shell script; retrace's own suite already assumes a unix shell (see AGENTS.md)")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
set -e
if [ "$1" = "run" ] && [ "$2" = "list" ]; then
  cat "$GH_FAKE_RUN_LIST_JSON"
  exit 0
fi
if [ "$1" = "run" ] && [ "$2" = "download" ]; then
  runid="$3"
  shift 3
  dir=""
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--dir" ]; then dir="$2"; fi
    shift
  done
  src="$GH_FAKE_DOWNLOAD_SRC/$runid"
  if [ -d "$src" ]; then
    cp -R "$src/." "$dir/"
  fi
  exit 0
fi
echo "fake gh: unhandled invocation: $*" >&2
exit 1
`
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeRunListJSON points GH_FAKE_RUN_LIST_JSON at a file containing the
// given `gh run list --json ...` response.
func writeRunListJSON(t *testing.T, runsJSON string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(path, []byte(runsJSON), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", path)
}

// stageDownload prepares what a `gh run download <databaseID>` call
// should produce: a manifest.json at <root>/<databaseID>/<app>/<flow>/
// <runID>/manifest.json, mirroring the shape a CI job that uploaded
// `.retrace/runs` for one app produces.
func stageDownload(t *testing.T, databaseID int64, app, flow, runID string) {
	t.Helper()
	root := os.Getenv("GH_FAKE_DOWNLOAD_SRC")
	if root == "" {
		root = t.TempDir()
		t.Setenv("GH_FAKE_DOWNLOAD_SRC", root)
	}
	dir := filepath.Join(root, fmt.Sprint(databaseID), app, flow, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("staging download fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"app":"`+app+`","flow":"`+flow+`"}`), 0o644); err != nil {
		t.Fatalf("writing fixture manifest.json: %v", err)
	}
}

func fixedNow(t *testing.T, at time.Time) func() time.Time {
	t.Helper()
	return func() time.Time { return at }
}

func TestFirstSyncPullsEverythingInRange(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"},
		{"databaseId": 2, "workflowName": "retrace-android", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-26T10:00:00Z"},
		{"databaseId": 3, "workflowName": "retrace-web", "headSha": "ccc3333", "url": "https://github.com/org/repo/actions/runs/3", "createdAt": "2026-08-25T10:00:00Z"}
	]`)
	stageDownload(t, 1, "ios", "checkout", "20260827T090000Z-aaa1111")
	stageDownload(t, 2, "android", "checkout", "20260826T090000Z-bbb2222")
	stageDownload(t, 3, "web", "login", "20260825T090000Z-ccc3333")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 3 {
		t.Fatalf("Synced = %v, want 3 runs", res.Synced)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped = %v, want none", res.Skipped)
	}

	for _, run := range []struct{ app, flow, id string }{
		{"ios", "checkout", "20260827T090000Z-aaa1111"},
		{"android", "checkout", "20260826T090000Z-bbb2222"},
		{"web", "login", "20260825T090000Z-ccc3333"},
	} {
		dest := filepath.Join(runs.RunsRoot(cwd), run.app, run.flow, run.id)
		if _, err := os.Stat(filepath.Join(dest, "manifest.json")); err != nil {
			t.Errorf("manifest.json missing at %s: %v", dest, err)
		}
		src, err := runs.ReadSource(runs.Paths{RunDir: dest})
		if err != nil || src == nil {
			t.Fatalf("ReadSource(%s): %v, %+v", dest, err, src)
		}
		if src.Kind != runs.SourceKindCI {
			t.Errorf("Kind = %q, want %q", src.Kind, runs.SourceKindCI)
		}
	}
}

func TestReSyncingIsIdempotent(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"}]`)
	stageDownload(t, 1, "ios", "checkout", "20260827T090000Z-aaa1111")

	cwd := t.TempDir()
	opts := Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)}
	first, err := Run(opts)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if len(first.Synced) != 1 {
		t.Fatalf("first Synced = %v, want 1", first.Synced)
	}

	dest := filepath.Join(runs.RunsRoot(cwd), "ios", "checkout", "20260827T090000Z-aaa1111", "manifest.json")
	before, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading synced manifest: %v", err)
	}

	second, err := Run(opts)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(second.Synced) != 0 {
		t.Fatalf("second Synced = %v, want none — already on disk", second.Synced)
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("re-reading manifest after second sync: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("second sync modified an already-synced run's manifest.json")
	}
}

func TestMalformedArtifactIsSkippedNotMerged(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 9, "workflowName": "retrace-ios", "headSha": "zzz9999", "url": "https://github.com/org/repo/actions/runs/9", "createdAt": "2026-08-27T10:00:00Z"}]`)
	// No stageDownload call for run 9: the fake gh's `run download` finds
	// no matching source directory and produces an empty --dir, exactly
	// as a real artifact containing files but no manifest.json would.
	root := t.TempDir()
	t.Setenv("GH_FAKE_DOWNLOAD_SRC", root)

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 0 {
		t.Fatalf("Synced = %v, want none", res.Synced)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want exactly one reason", res.Skipped)
	}
	if entries, _ := os.ReadDir(runs.RunsRoot(cwd)); len(entries) != 0 {
		t.Fatalf(".retrace/runs is not empty after a malformed-artifact sync: %v", entries)
	}
}

func TestSinceFilteringExcludesOlderRuns(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T00:00:00Z"},
		{"databaseId": 2, "workflowName": "retrace-ios", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-01T00:00:00Z"}
	]`)
	stageDownload(t, 1, "ios", "checkout", "20260827T000000Z-aaa1111")
	stageDownload(t, 2, "ios", "checkout", "20260801T000000Z-bbb2222")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Since: 48 * time.Hour, Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 || res.Synced[0] != filepath.Join("ios", "checkout", "20260827T000000Z-aaa1111") {
		t.Fatalf("Synced = %v, want only the run inside the 48h window", res.Synced)
	}
}

func TestGhMissingFailsFastWithoutNetworkCall(t *testing.T) {
	// Deliberately NOT calling fakeGH: PATH has no "gh" at all (assuming
	// the test host doesn't have one either — CI runners for this repo's
	// own test suite don't install gh).
	t.Setenv("PATH", t.TempDir())
	_, err := Run(Options{Cwd: t.TempDir(), From: "github", Repo: "org/repo"})
	if err == nil {
		t.Fatal("expected an error when gh is not on PATH")
	}
}

func TestUnknownFromIsRejected(t *testing.T) {
	if _, err := Run(Options{Cwd: t.TempDir(), From: "s3", Repo: "org/repo"}); err == nil {
		t.Fatal("expected an error for --from s3 (not yet supported)")
	}
	if _, err := Run(Options{Cwd: t.TempDir(), From: "dropbox"}); err == nil {
		t.Fatal("expected an error for an unknown --from value")
	}
}
