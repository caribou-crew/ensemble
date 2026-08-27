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
	if err := os.WriteFile(runListPath, []byte(`[{"databaseId": 1, "workflowName": "retrace-web", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z"}]`), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", runListPath)

	downloadRoot := t.TempDir()
	runDir := filepath.Join(downloadRoot, "1", "web", "login", "20260827T090000Z-aaa1111")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("staging download fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte(`{"app":"web","flow":"login"}`), 0o644); err != nil {
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
