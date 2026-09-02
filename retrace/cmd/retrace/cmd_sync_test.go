package main

// cmd_sync_test.go drives `retrace sync` through the BUILT binary, same
// harness cmd_ref_test.go and cmd_run_test.go share (buildRetrace/
// runRetrace in cmd_run_test.go) — never `go run`, for the same exit-code
// reason those files document.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/sync"
)

// fakeGHOnPath mirrors retrace/sync's own test double (that package's
// tests can't be imported from here — package main). It answers `gh run
// list` and `gh run download` the same way.
func fakeGHOnPath(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a POSIX shell script")
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
  d=""
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--dir" ]; then d="$2"; fi
    shift
  done
  src="$GH_FAKE_DOWNLOAD_SRC/$runid"
  if [ -d "$src" ]; then
    cp -R "$src/." "$d/"
  fi
  exit 0
fi
if [ "$1" = "api" ]; then
  path="$2"
  case "$path" in
    */artifacts)
      echo "0"
      ;;
    *)
      echo ""
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
}

func TestSyncRequiresRepo(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	res := runRetrace(t, bin, cwd, "", "sync", "--from", "github")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage error); stderr: %s", res.code, exitUsage, res.stderr)
	}
}

func TestSyncFailsFastWithoutGh(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // no gh anywhere on it
	res := runRetrace(t, bin, cwd, "", "sync", "--repo", "org/repo")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", res.code, exitUsage, res.stderr)
	}
}

func TestSyncJSONRoundTripsThroughSyncResult(t *testing.T) {
	bin := buildRetrace(t)
	fakeGHOnPath(t)

	runListPath := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(runListPath, []byte(`[{"databaseId": 1, "workflowName": "retrace-web", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", runListPath)

	downloadRoot := t.TempDir()
	runDir := filepath.Join(downloadRoot, "1", "web", "login", "20260827T090000Z-aaa1111")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("staging download fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte(`{"schema":"retrace/1","app":"web","flow":"login","runId":"20260827T090000Z-aaa1111","mode":"standalone","capture":{"status":"ok","summary":"ok"},"wire":{"recorded":true},"groups":[]}`), 0o644); err != nil {
		t.Fatalf("writing fixture manifest.json: %v", err)
	}
	t.Setenv("GH_FAKE_DOWNLOAD_SRC", downloadRoot)

	cwd := t.TempDir()
	res := runRetrace(t, bin, cwd, "", "sync", "--repo", "org/repo", "--json")
	if res.code != exitOK {
		t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", res.code, exitOK, res.stdout, res.stderr)
	}
	var got sync.Result
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
		t.Fatalf("stdout is not a sync.Result: %v\n%s", err, res.stdout)
	}
	if want := []string{filepath.Join("web", "login", "20260827T090000Z-aaa1111")}; fmt.Sprint(got.Synced) != fmt.Sprint(want) {
		t.Fatalf("Synced = %v, want %v", got.Synced, want)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".retrace", "runs", "web", "login", "20260827T090000Z-aaa1111", "manifest.json")); err != nil {
		t.Errorf("synced manifest.json missing under cwd's .retrace/runs: %v", err)
	}
}

func TestSyncRepoOrReposRequired(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	res := runRetrace(t, bin, cwd, "", "sync", "--from", "github")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage error); stderr: %s", res.code, exitUsage, res.stderr)
	}
}

func TestSyncDryRunListsWithoutPulling(t *testing.T) {
	bin := buildRetrace(t)
	fakeGHOnPath(t)

	runListPath := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(runListPath, []byte(`[{"databaseId": 1, "workflowName": "retrace-web", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", runListPath)
	t.Setenv("GH_FAKE_DOWNLOAD_SRC", t.TempDir())

	cwd := t.TempDir()
	res := runRetrace(t, bin, cwd, "", "sync", "--repo", "org/repo", "--dry-run", "--json")
	if res.code != exitOK {
		t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", res.code, exitOK, res.stdout, res.stderr)
	}
	var got []sync.Candidate
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
		t.Fatalf("stdout is not a []sync.Candidate: %v\n%s", err, res.stdout)
	}
	if len(got) != 1 || got[0].DatabaseID != 1 {
		t.Fatalf("candidates = %+v, want the one fixture run", got)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".retrace", "runs")); !os.IsNotExist(err) {
		t.Fatalf("--dry-run wrote to .retrace/runs (err=%v) — it must not download anything", err)
	}
}

// TestSyncHonorsRepoConfigRoots reproduces the bug: `retrace serve --watch`
// already splits a sync across each retrace.repo.yaml root (cmd_serve_watch.go's
// startWatch), but plain `retrace sync` never consulted repoconfig at all —
// it always merged every app's run directories under cwd's own
// .retrace/runs, even when retrace.repo.yaml maps them to separate roots.
func TestSyncHonorsRepoConfigRoots(t *testing.T) {
	bin := buildRetrace(t)
	fakeGHOnPath(t)

	repoRoot := t.TempDir()
	webRoot := filepath.Join(repoRoot, "web")
	mobileRoot := filepath.Join(repoRoot, "mobile")
	if err := os.MkdirAll(webRoot, 0o755); err != nil {
		t.Fatalf("mkdir web root: %v", err)
	}
	if err := os.MkdirAll(mobileRoot, 0o755); err != nil {
		t.Fatalf("mkdir mobile root: %v", err)
	}
	repoYAML := "repo: org/repo\napps:\n  web:\n    root: ./web\n  mobile:\n    root: ./mobile\n"
	if err := os.WriteFile(filepath.Join(repoRoot, "retrace.repo.yaml"), []byte(repoYAML), 0o644); err != nil {
		t.Fatalf("writing retrace.repo.yaml: %v", err)
	}

	runListPath := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(runListPath, []byte(`[{"databaseId": 1, "workflowName": "retrace-ci", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", runListPath)

	// One CI artifact bundling BOTH apps' run directories — the exact
	// shape Options.Apps's doc comment exists to handle: without a
	// per-root allowlist, every app in the artifact merges into every
	// root that syncs it.
	downloadRoot := t.TempDir()
	webRunDir := filepath.Join(downloadRoot, "1", "web", "login", "20260827T090000Z-aaa1111")
	mobileRunDir := filepath.Join(downloadRoot, "1", "mobile", "onboarding", "20260827T090000Z-aaa1112")
	if err := os.MkdirAll(webRunDir, 0o755); err != nil {
		t.Fatalf("staging web download fixture: %v", err)
	}
	if err := os.MkdirAll(mobileRunDir, 0o755); err != nil {
		t.Fatalf("staging mobile download fixture: %v", err)
	}
	manifest := func(app string) []byte {
		return []byte(fmt.Sprintf(`{"schema":"retrace/1","app":%q,"flow":"f","runId":"r","mode":"standalone","capture":{"status":"ok","summary":"ok"},"wire":{"recorded":true},"groups":[]}`, app))
	}
	if err := os.WriteFile(filepath.Join(webRunDir, "manifest.json"), manifest("web"), 0o644); err != nil {
		t.Fatalf("writing web fixture manifest.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mobileRunDir, "manifest.json"), manifest("mobile"), 0o644); err != nil {
		t.Fatalf("writing mobile fixture manifest.json: %v", err)
	}
	t.Setenv("GH_FAKE_DOWNLOAD_SRC", downloadRoot)

	res := runRetrace(t, bin, repoRoot, "", "sync", "--repo", "org/repo", "--json")
	if res.code != exitOK {
		t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", res.code, exitOK, res.stdout, res.stderr)
	}

	webManifest := filepath.Join(webRoot, ".retrace", "runs", "web", "login", "20260827T090000Z-aaa1111", "manifest.json")
	if _, err := os.Stat(webManifest); err != nil {
		t.Errorf("web app's synced manifest.json missing under its configured root %s: %v", webRoot, err)
	}
	mobileManifest := filepath.Join(mobileRoot, ".retrace", "runs", "mobile", "onboarding", "20260827T090000Z-aaa1112", "manifest.json")
	if _, err := os.Stat(mobileManifest); err != nil {
		t.Errorf("mobile app's synced manifest.json missing under its configured root %s: %v", mobileRoot, err)
	}

	// Neither app's run directory should land directly under repoRoot's
	// own .retrace/runs — that's the bug: cwd (repoRoot) is not either
	// app's configured root.
	if _, err := os.Stat(filepath.Join(repoRoot, ".retrace", "runs", "web")); !os.IsNotExist(err) {
		t.Errorf("web run merged under repoRoot's own .retrace/runs instead of its configured root (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".retrace", "runs", "mobile")); !os.IsNotExist(err) {
		t.Errorf("mobile run merged under repoRoot's own .retrace/runs instead of its configured root (err=%v)", err)
	}
}

func TestSyncListRequiresRepo(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	res := runRetrace(t, bin, cwd, "", "sync", "list")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage error); stderr: %s", res.code, exitUsage, res.stderr)
	}
}

func TestSyncListPrintsCandidatesAsJSON(t *testing.T) {
	bin := buildRetrace(t)
	fakeGHOnPath(t)

	runListPath := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(runListPath, []byte(`[{"databaseId": 1, "workflowName": "retrace-web", "headBranch": "main", "event": "push", "status": "completed", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"}]`), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", runListPath)
	t.Setenv("GH_FAKE_DOWNLOAD_SRC", t.TempDir())

	cwd := t.TempDir()
	res := runRetrace(t, bin, cwd, "", "sync", "list", "--repo", "org/repo", "--json")
	if res.code != exitOK {
		t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", res.code, exitOK, res.stdout, res.stderr)
	}
	var got []sync.Candidate
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
		t.Fatalf("stdout is not a []sync.Candidate: %v\n%s", err, res.stdout)
	}
	if len(got) != 1 || got[0].HeadBranch != "main" || got[0].Event != "push" {
		t.Fatalf("candidates = %+v", got)
	}
}
