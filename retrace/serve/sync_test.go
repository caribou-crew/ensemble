package serve

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
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
	// A real retrace manifest, not a bare {app,flow} stub: sync ingest now
	// requires runs.ReadManifest to parse it (the guard that keeps Maestro
	// artifact-manifests out), so a stub would be skipped as non-retrace.
	m := runs.Manifest{
		App: app, Flow: flow, RunID: runID, Mode: runs.ModeStandalone,
		Capture: runs.CaptureTrust{Status: "ok", Summary: "capture looks complete"},
		Wire:    runs.Counts{Recorded: true},
	}
	if err := runs.WriteManifest(runs.Paths{RunDir: dir, ManifestPath: filepath.Join(dir, "manifest.json")}, &m); err != nil {
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

// TestGetSyncCandidatesReportsLocalRunsForAnAlreadyPulledCandidate is the
// join a click-to-view sync panel depends on: a candidate already pulled
// (its RunURL matches a local run's source.json) must say so via
// localRuns, without gh being asked to download anything again, and a
// candidate never pulled must report an empty array — never null, so a
// UI can iterate it with no special case.
func TestGetSyncCandidatesReportsLocalRunsForAnAlreadyPulledCandidate(t *testing.T) {
	fakeGH(t)
	writeRunListJSON(t, `[
		{"databaseId": 1, "workflowName": "Retrace Web", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed", "conclusion": "success"},
		{"databaseId": 2, "workflowName": "Retrace Web", "headSha": "bbb2222", "url": "https://github.com/org/repo/actions/runs/2", "createdAt": "2026-08-27T11:00:00Z", "status": "completed", "conclusion": "success"}
	]`)
	ts, cwd := newSyncServer(t)

	p := recordRun(t, cwd, "web", "checkout", "20260827T090000Z-aaa1111", map[string][]byte{"cart": shotPNG(t, white)}, nil)
	if err := runs.WriteSource(p, runs.Source{
		Kind: runs.SourceKindCI, RunURL: "https://github.com/org/repo/actions/runs/1", SyncedAt: time.Date(2026, 8, 27, 9, 5, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("runs.WriteSource: %v", err)
	}

	r := get(t, ts, "/api/sync/candidates?repo=org/repo")
	body := mustOK(t, r, "GET /api/sync/candidates")
	candidates, ok := body["candidates"].([]any)
	if !ok || len(candidates) != 2 {
		t.Fatalf("candidates = %v, want 2 entries", body["candidates"])
	}

	byDatabaseID := map[float64]map[string]any{}
	for _, c := range candidates {
		row := c.(map[string]any)
		byDatabaseID[row["databaseId"].(float64)] = row
	}

	pulled := byDatabaseID[1]
	local, ok := pulled["localRuns"].([]any)
	if !ok || len(local) != 1 || local[0] != "web/checkout/20260827T090000Z-aaa1111" {
		t.Fatalf("pulled candidate's localRuns = %v, want [\"web/checkout/20260827T090000Z-aaa1111\"]", pulled["localRuns"])
	}

	unpulled := byDatabaseID[2]
	local, ok = unpulled["localRuns"].([]any)
	if !ok {
		t.Fatalf("unpulled candidate's localRuns is not an array: %v", unpulled["localRuns"])
	}
	if len(local) != 0 {
		t.Fatalf("unpulled candidate's localRuns = %v, want empty", local)
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

// TestPostSyncRoutesEachAppToItsOwnRoot is the multi-root fix: when the
// server is built with Sources (a retrace.repo.yaml mapping apps to
// different roots), one artifact carrying two apps' run dirs must land each
// app's run under the ROOT that app maps to — not all under the serve
// process's single cwd. Writing them to one cwd orphans the app whose
// reference lives in the other root (the queue shows it quarantined with no
// source), which is exactly what this routing fixes.
func TestPostSyncRoutesEachAppToItsOwnRoot(t *testing.T) {
	fakeGH(t)
	writeRunListJSON(t, `[{"databaseId": 7, "workflowName": "E2E Android", "headSha": "feac1c8", "url": "https://github.com/org/repo/actions/runs/7", "createdAt": "2026-08-27T10:00:00Z", "status": "completed", "conclusion": "success"}]`)
	// One artifact (databaseID 7) carries BOTH apps' run dirs.
	stageDownload(t, 7, "uxt-web", "card-views", "20260827T090000Z-feac1c8")
	stageDownload(t, 7, "uxt-rn-android", "card-views", "20260827T090000Z-feac1c8")

	rootWeb := t.TempDir()          // uxt-web lives here (the default/serve cwd)
	rootMobile := t.TempDir()       // uxt-rn-android lives under a different root
	defaultDeps := deps(t, rootWeb)
	byRoot := map[string]Deps{rootWeb: defaultDeps, rootMobile: deps(t, rootMobile)}
	appRoot := map[string]string{"uxt-web": rootWeb, "uxt-rn-android": rootMobile}
	sources, err := NewSources(byRoot, appRoot)
	if err != nil {
		t.Fatalf("NewSources: %v", err)
	}
	ts := httptest.NewServer(NewWithSourcesAndSync(defaultDeps, &sources, SyncConfig{Repo: "org/repo"}))
	t.Cleanup(ts.Close)

	r := post(t, ts, "/api/sync", `{"repo":"org/repo","selections":[{"repo":"org/repo","databaseId":7}]}`)
	mustOK(t, r, "POST /api/sync")

	// uxt-web's run must land under rootWeb; uxt-rn-android's under rootMobile.
	if _, err := os.Stat(filepath.Join(rootWeb, ".retrace", "runs", "uxt-web", "card-views", "20260827T090000Z-feac1c8", "manifest.json")); err != nil {
		t.Errorf("uxt-web run not under its own root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootMobile, ".retrace", "runs", "uxt-rn-android", "card-views", "20260827T090000Z-feac1c8", "manifest.json")); err != nil {
		t.Errorf("uxt-rn-android run not under its own root: %v", err)
	}
	// The mobile app must NOT have been written to the web root (the bug).
	if _, err := os.Stat(filepath.Join(rootWeb, ".retrace", "runs", "uxt-rn-android")); err == nil {
		t.Errorf("uxt-rn-android leaked into the web root — per-root routing failed")
	}
}
