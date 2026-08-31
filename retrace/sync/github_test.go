package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
if [ -n "$GH_FAKE_INVOCATION_LOG" ]; then
  echo "$*" >> "$GH_FAKE_INVOCATION_LOG"
fi
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
  case ",$GH_FAKE_DOWNLOAD_FAIL_IDS," in
    *",$runid,"*)
      echo "no valid artifacts found to download" >&2
      exit 1
      ;;
  esac
  src="$GH_FAKE_DOWNLOAD_SRC/$runid"
  if [ -d "$src" ]; then
    cp -R "$src/." "$dir/"
  fi
  exit 0
fi
if [ "$1" = "api" ]; then
  path="$2"
  case "$path" in
    */artifacts)
      runid=$(echo "$path" | sed -E 's#.*/actions/runs/([0-9]+)/artifacts#\1#')
      count="0"
      if [ -n "$GH_FAKE_ARTIFACTS_DIR" ] && [ -f "$GH_FAKE_ARTIFACTS_DIR/$runid" ]; then
        count=$(cat "$GH_FAKE_ARTIFACTS_DIR/$runid")
      fi
      echo "$count"
      ;;
    *)
      runid=$(echo "$path" | sed -E 's#.*/actions/runs/([0-9]+)$#\1#')
      login=""
      if [ -n "$GH_FAKE_ACTORS_DIR" ] && [ -f "$GH_FAKE_ACTORS_DIR/$runid" ]; then
        login=$(cat "$GH_FAKE_ACTORS_DIR/$runid")
      fi
      echo "$login"
      ;;
  esac
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

	logPath := filepath.Join(dir, "invocations.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("creating invocation log: %v", err)
	}
	t.Setenv("GH_FAKE_INVOCATION_LOG", logPath)
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
		{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"},
		{"databaseId": 2, "workflowName": "retrace-android", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-26T10:00:00Z", "status": "completed"},
		{"databaseId": 3, "workflowName": "retrace-web", "headSha": "ccc3333", "url": "https://github.com/org/repo/actions/runs/3", "createdAt": "2026-08-25T10:00:00Z", "status": "completed"}
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
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`)
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
	writeRunListJSON(t, `[{"databaseId": 9, "workflowName": "retrace-ios", "headSha": "zzz9999", "url": "https://github.com/org/repo/actions/runs/9", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`)
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

// TestAppAllowlistAdmitsOnlyTheNamedApps is retrace-repo-config's addition
// to retrace-sync: one artifact (one databaseId) staged with run
// directories for two apps, synced with an allowlist naming only one of
// them.
func TestAppAllowlistAdmitsOnlyTheNamedApps(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-mobile", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`)
	stageDownload(t, 1, "uxt-rn-ios", "checkout", "20260827T090000Z-aaa1111")
	stageDownload(t, 1, "uxt-web", "checkout", "20260827T090000Z-aaa1111")

	cwd := t.TempDir()
	res, err := Run(Options{
		Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now),
		Apps: []string{"uxt-rn-ios"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 || res.Synced[0] != filepath.Join("uxt-rn-ios", "checkout", "20260827T090000Z-aaa1111") {
		t.Fatalf("Synced = %v, want exactly the uxt-rn-ios run", res.Synced)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Artifact == "" {
		t.Fatalf("Skipped = %v, want exactly one reason naming the excluded app", res.Skipped)
	}
	if !strings.Contains(res.Skipped[0].Reason, "uxt-web") || !strings.Contains(res.Skipped[0].Reason, "allowlist") {
		t.Fatalf("Skipped[0].Reason = %q, want it to name uxt-web and the allowlist", res.Skipped[0].Reason)
	}

	if _, err := os.Stat(filepath.Join(runs.RunsRoot(cwd), "uxt-rn-ios", "checkout", "20260827T090000Z-aaa1111", "manifest.json")); err != nil {
		t.Fatalf("uxt-rn-ios run was not merged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs.RunsRoot(cwd), "uxt-web")); err == nil {
		t.Fatal("uxt-web was merged despite not being in the allowlist")
	}
}

// TestNoAppAllowlistMergesEverythingUnchanged is the compatibility half of
// TestAppAllowlistAdmitsOnlyTheNamedApps: Options.Apps unset (every caller
// before this field existed) merges every app an artifact contains,
// exactly as retrace-ci-sync already specifies.
func TestNoAppAllowlistMergesEverythingUnchanged(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-mobile", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`)
	stageDownload(t, 1, "uxt-rn-ios", "checkout", "20260827T090000Z-aaa1111")
	stageDownload(t, 1, "uxt-web", "checkout", "20260827T090000Z-aaa1111")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 2 {
		t.Fatalf("Synced = %v, want both apps merged with no allowlist set", res.Synced)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped = %v, want none", res.Skipped)
	}
}

func TestSinceFilteringExcludesOlderRuns(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T00:00:00Z", "status": "completed"},
		{"databaseId": 2, "workflowName": "retrace-ios", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-01T00:00:00Z", "status": "completed"}
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

func TestNonCompletedRunIsSkippedNotDownloaded(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "in_progress"},
		{"databaseId": 2, "workflowName": "retrace-web", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T09:00:00Z", "status": "completed"}
	]`)
	// No stageDownload for run 1: if the code tried to download it, the
	// fake gh's own successful-but-empty response would still mask the
	// bug this test exists to catch, so it also asserts on the invocation
	// log below rather than only on the final result.
	stageDownload(t, 2, "web", "checkout", "20260827T090000Z-bbb2222")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 || res.Synced[0] != filepath.Join("web", "checkout", "20260827T090000Z-bbb2222") {
		t.Fatalf("Synced = %v, want only run 2", res.Synced)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Artifact != "run 1 (retrace-ios)" {
		t.Fatalf("Skipped = %v, want one entry naming run 1", res.Skipped)
	}
	if !strings.Contains(res.Skipped[0].Reason, "in_progress") {
		t.Fatalf("Skipped[0].Reason = %q, want it to name the status", res.Skipped[0].Reason)
	}

	log, err := os.ReadFile(filepath.Join(os.Getenv("GH_FAKE_INVOCATION_LOG")))
	if err != nil {
		t.Fatalf("reading invocation log: %v", err)
	}
	if strings.Contains(string(log), "download 1 ") {
		t.Fatalf("gh run download was invoked for the non-completed run — the pre-check did not avoid it:\n%s", log)
	}
}

func TestDownloadFailureIsSkippedNotAborted(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"},
		{"databaseId": 2, "workflowName": "retrace-web", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T09:00:00Z", "status": "completed"}
	]`)
	t.Setenv("GH_FAKE_DOWNLOAD_FAIL_IDS", "1")
	stageDownload(t, 2, "web", "checkout", "20260827T090000Z-bbb2222")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v — a single run's download failure must not abort the batch", err)
	}
	if len(res.Synced) != 1 || res.Synced[0] != filepath.Join("web", "checkout", "20260827T090000Z-bbb2222") {
		t.Fatalf("Synced = %v, want only run 2", res.Synced)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Artifact != "run 1 (retrace-ios)" {
		t.Fatalf("Skipped = %v, want one entry naming run 1", res.Skipped)
	}
	if !strings.Contains(res.Skipped[0].Reason, "no valid artifacts") {
		t.Fatalf("Skipped[0].Reason = %q, want the gh failure text", res.Skipped[0].Reason)
	}
}

func TestBranchActorEventStatusFlagsArePassedToGh(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[]`)

	cwd := t.TempDir()
	_, err := Run(Options{
		Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now),
		Branch: "main", Actor: "octocat", Event: "schedule", Status: "completed",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	log, err := os.ReadFile(os.Getenv("GH_FAKE_INVOCATION_LOG"))
	if err != nil {
		t.Fatalf("reading invocation log: %v", err)
	}
	for _, want := range []string{"--branch main", "--user octocat", "--event schedule", "--status completed"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("invocation log missing %q:\n%s", want, log)
		}
	}
}

func TestMultipleReposAreAllSynced(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-web", "headSha": "aaa1111", "url": "https://github.com/org/a/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`)
	stageDownload(t, 1, "web", "checkout", "20260827T100000Z-aaa1111")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repos: []string{"org/a", "org/b"}, Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The same fixture is returned for every repo the fake gh sees (it
	// doesn't branch on --repo), so this proves BOTH repos were listed:
	// the single staged run gets merged twice under the same dest path,
	// which is idempotent — the second is a no-op — so Synced still has
	// exactly one entry, but the invocation log proves both repos were
	// queried.
	if len(res.Synced) != 1 {
		t.Fatalf("Synced = %v, want 1", res.Synced)
	}
	log, err := os.ReadFile(os.Getenv("GH_FAKE_INVOCATION_LOG"))
	if err != nil {
		t.Fatalf("reading invocation log: %v", err)
	}
	for _, want := range []string{"--repo org/a", "--repo org/b"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("invocation log missing %q:\n%s", want, log)
		}
	}
}

func TestWorkflowGlobFiltersByPattern(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "Retrace Replay (Visual + Wire Regression)", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"},
		{"databaseId": 2, "workflowName": "Maestro iOS (Card Views)", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T09:00:00Z", "status": "completed"}
	]`)
	stageDownload(t, 1, "web", "checkout", "20260827T100000Z-aaa1111")
	stageDownload(t, 2, "web", "other", "20260827T090000Z-bbb2222")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now), Workflows: []string{"Retrace *"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 || res.Synced[0] != filepath.Join("web", "checkout", "20260827T100000Z-aaa1111") {
		t.Fatalf("Synced = %v, want only the Retrace-prefixed run", res.Synced)
	}
}

func TestInvalidWorkflowGlobIsRejected(t *testing.T) {
	fakeGH(t)
	cwd := t.TempDir()
	_, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Workflows: []string{"["}})
	if err == nil {
		t.Fatal("expected an error for a malformed workflow glob")
	}
}

// stageActor makes fetchActor return login for run databaseID via the
// fake gh's `api repos/.../actions/runs/<id>` branch.
func stageActor(t *testing.T, databaseID int64, login string) {
	t.Helper()
	dir := os.Getenv("GH_FAKE_ACTORS_DIR")
	if dir == "" {
		dir = t.TempDir()
		t.Setenv("GH_FAKE_ACTORS_DIR", dir)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(databaseID)), []byte(login), 0o644); err != nil {
		t.Fatalf("staging actor fixture: %v", err)
	}
}

// stageArtifactCount makes runHasArtifacts see count for run databaseID
// via the fake gh's `api .../artifacts` branch.
func stageArtifactCount(t *testing.T, databaseID int64, count int) {
	t.Helper()
	dir := os.Getenv("GH_FAKE_ARTIFACTS_DIR")
	if dir == "" {
		dir = t.TempDir()
		t.Setenv("GH_FAKE_ARTIFACTS_DIR", dir)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(databaseID)), []byte(fmt.Sprint(count)), 0o644); err != nil {
		t.Fatalf("staging artifact-count fixture: %v", err)
	}
}

func TestSyncedRunRecordsBranchEventActorInSource(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-web", "headBranch": "main", "event": "push", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`)
	stageDownload(t, 1, "web", "checkout", "20260827T100000Z-aaa1111")
	stageActor(t, 1, "octocat")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 {
		t.Fatalf("Synced = %v, want 1", res.Synced)
	}

	dest := filepath.Join(runs.RunsRoot(cwd), "web", "checkout", "20260827T100000Z-aaa1111")
	src, err := runs.ReadSource(runs.Paths{RunDir: dest})
	if err != nil || src == nil {
		t.Fatalf("ReadSource: %v, %+v", err, src)
	}
	if src.HeadBranch != "main" || src.Event != "push" || src.Actor != "octocat" {
		t.Fatalf("source.json provenance = %+v, want branch=main event=push actor=octocat", src)
	}
}

func TestRunWithSelectionsPullsOnlyThose(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "retrace-web", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"},
		{"databaseId": 2, "workflowName": "retrace-ios", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T09:00:00Z", "status": "completed"}
	]`)
	stageDownload(t, 1, "web", "checkout", "20260827T100000Z-aaa1111")
	stageDownload(t, 2, "ios", "checkout", "20260827T090000Z-bbb2222")

	cwd := t.TempDir()
	res, err := Run(Options{
		Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now),
		Selections: []Selection{{Repo: "org/repo", DatabaseID: 2}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 || res.Synced[0] != filepath.Join("ios", "checkout", "20260827T090000Z-bbb2222") {
		t.Fatalf("Synced = %v, want only the selected run", res.Synced)
	}
}

func TestRunWithSelectionsIgnoresSinceWindow(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-web", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-01-01T10:00:00Z", "status": "completed"}]`)
	stageDownload(t, 1, "web", "checkout", "20260101T100000Z-aaa1111")

	cwd := t.TempDir()
	res, err := Run(Options{
		Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now), Since: time.Hour,
		Selections: []Selection{{Repo: "org/repo", DatabaseID: 1}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 {
		t.Fatalf("Synced = %v, want the selected run pulled even though it's outside the 1h window", res.Synced)
	}
}

func TestRunWithSelectionsStillSkipsNonCompleted(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-web", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "queued"}]`)

	cwd := t.TempDir()
	res, err := Run(Options{
		Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now),
		Selections: []Selection{{Repo: "org/repo", DatabaseID: 1}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 0 {
		t.Fatalf("Synced = %v, want none — a selected but non-completed run must still be skipped", res.Synced)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want one entry", res.Skipped)
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
