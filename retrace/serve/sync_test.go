package serve

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeGH puts a minimal `gh` on PATH for the duration of the test — the
// same three subcommands retrace/sync's own test double answers (run list,
// run download, api), just re-implemented here since retrace/sync's
// unexported test helpers cannot be imported across packages.
func fakeGH(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a POSIX shell script; retrace's own suite already assumes a unix shell")
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
if [ "$1" = "api" ]; then
  path="$2"
  case "$path" in
    */artifacts)
      echo "1"
      ;;
    *)
      echo "octocat"
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

func writeRunListJSON(t *testing.T, runsJSON string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(path, []byte(runsJSON), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", path)
}

// stageDownload prepares what `gh run download <databaseID>` should
// produce.
func stageDownload(t *testing.T, databaseID int64, app, flow, runID string) {
	t.Helper()
	root := os.Getenv("GH_FAKE_DOWNLOAD_SRC")
	if root == "" {
		root = t.TempDir()
		t.Setenv("GH_FAKE_DOWNLOAD_SRC", root)
	}
	dir := filepath.Join(root, itoa64(databaseID), app, flow, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("staging download fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"app":"`+app+`","flow":"`+flow+`"}`), 0o644); err != nil {
		t.Fatalf("writing fixture manifest.json: %v", err)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func newSyncServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	cwd := t.TempDir()
	return newServer(t, cwd), cwd
}

func TestGetSyncCandidatesRequiresRepo(t *testing.T) {
	fakeGH(t)
	ts, _ := newSyncServer(t)
	r := get(t, ts, "/api/sync/candidates")
	if r.status != 400 {
		t.Fatalf("status = %d, want 400\n%s", r.status, r.body)
	}
}

func TestGetSyncCandidatesListsWithoutDownloading(t *testing.T) {
	fakeGH(t)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "Retrace Web", "headSha": "aaa1111", "headBranch": "main", "event": "push", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed", "conclusion": "success"}]`)
	ts, _ := newSyncServer(t)

	r := get(t, ts, "/api/sync/candidates?repo=org/repo")
	body := mustOK(t, r, "GET /api/sync/candidates")
	candidates, ok := body["candidates"].([]any)
	if !ok || len(candidates) != 1 {
		t.Fatalf("candidates = %v, want 1 entry", body["candidates"])
	}
	first := candidates[0].(map[string]any)
	if first["repo"] != "org/repo" {
		t.Errorf("repo = %v, want org/repo", first["repo"])
	}
	if first["actor"] != "octocat" {
		t.Errorf("actor = %v, want octocat", first["actor"])
	}
}

func TestPostSyncRequiresRepo(t *testing.T) {
	fakeGH(t)
	ts, _ := newSyncServer(t)
	r := post(t, ts, "/api/sync", "{}")
	if r.status != 400 {
		t.Fatalf("status = %d, want 400\n%s", r.status, r.body)
	}
}

func TestPostSyncPullsSelectedCandidates(t *testing.T) {
	fakeGH(t)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "Retrace Web", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`)
	stageDownload(t, 1, "web", "checkout", "20260827T090000Z-aaa1111")
	ts, cwd := newSyncServer(t)

	r := post(t, ts, "/api/sync", `{"repo":"org/repo","selections":[{"repo":"org/repo","databaseId":1}]}`)
	body := mustOK(t, r, "POST /api/sync")
	synced, ok := body["synced"].([]any)
	if !ok || len(synced) != 1 {
		t.Fatalf("synced = %v, want 1 entry", body["synced"])
	}

	if _, err := os.Stat(filepath.Join(cwd, ".retrace", "runs", "web", "checkout", "20260827T090000Z-aaa1111", "manifest.json")); err != nil {
		t.Errorf("manifest.json missing after sync: %v", err)
	}
}
