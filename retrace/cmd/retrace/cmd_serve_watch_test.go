package main

// cmd_serve_watch_test.go drives `retrace serve`'s retrace.repo.yaml
// aggregation and `--watch` sync through the BUILT binary — buildRetrace/
// startServe from cmd_serve_test.go, fakeGHOnPath from cmd_sync_test.go.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// writeRepoConfig writes a minimal retrace.repo.yaml at dir mapping each
// (app, root) pair given — root is relative to dir, matching how a real
// repo commits this file at its own top level.
func writeRepoConfig(t *testing.T, dir string, apps map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("apps:\n")
	for app, root := range apps {
		fmt.Fprintf(&b, "  %s:\n    root: %s\n", app, root)
	}
	if err := os.WriteFile(filepath.Join(dir, "retrace.repo.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("writing retrace.repo.yaml: %v", err)
	}
}

// stubRun creates a run directory under root's .retrace/runs tree carrying
// just enough to be a real retrace run — a manifest.json, per appIsReal
// (retrace/serve/queue.go) — so the queue carries a (quarantined, no ref
// yet) item naming it rather than filtering the whole app out as a foreign
// artifact tree. BuildQueue's own doc comment is explicit that a flow with
// no reference becomes an item, never a dropped row, so this stays a
// deliberately minimal fixture beyond that: no shots, no wire.jsonl — this
// suite is about WHICH roots' apps show up, not about diff correctness,
// which retrace/serve's own tests already cover.
func stubRun(t *testing.T, root, app, flow string) {
	t.Helper()
	runID := "20260827T090000Z-aaa1111"
	p, err := runs.Create(runs.RunsRoot(root), app, flow, runID)
	if err != nil {
		t.Fatalf("staging run fixture: %v", err)
	}
	m := runs.Manifest{
		App: app, Flow: flow, RunID: runID, Mode: runs.ModeStandalone,
		Git:        runs.Git{SHA: "deadbee", Branch: "main"},
		StartedAt:  time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 8, 27, 9, 0, 5, 0, time.UTC),
		Capture:    runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"},
		Wire:       runs.Counts{Recorded: true},
	}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("writing manifest fixture: %v", err)
	}
}

func getQueueApps(t *testing.T, url string) []string {
	t.Helper()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(url + "/api/queue")
	if err != nil {
		t.Fatalf("GET /api/queue: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/queue: status = %d\n%s", resp.StatusCode, b)
	}
	var got struct {
		Items []struct {
			App string `json:"app"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("/api/queue is not the expected shape: %v\n%s", err, b)
	}
	seen := map[string]bool{}
	var apps []string
	for _, item := range got.Items {
		if !seen[item.App] {
			seen[item.App] = true
			apps = append(apps, item.App)
		}
	}
	return apps
}

func containsAll(got []string, want ...string) bool {
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

// --- 4.1: retrace.repo.yaml aggregation ---------------------------------

// A repo config mapping apps across two roots is served as ONE dashboard
// carrying every mapped app, regardless of which root each app's runs live
// under — design.md's D3, exercised end to end through the built CLI
// rather than through package serve directly.
func TestServeWithRepoConfigAggregatesAppsAcrossRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := buildRetrace(t)

	repoRoot := t.TempDir()
	web := filepath.Join(repoRoot, "web")
	mobile := filepath.Join(repoRoot, "mobile")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	if err := os.MkdirAll(mobile, 0o755); err != nil {
		t.Fatalf("mkdir mobile: %v", err)
	}
	stubRun(t, web, "app-web", "login")
	stubRun(t, mobile, "app-ios", "onboarding")
	stubRun(t, mobile, "app-android", "onboarding")
	writeRepoConfig(t, repoRoot, map[string]string{
		"app-web":     "web",
		"app-ios":     "mobile",
		"app-android": "mobile",
	})

	p, _ := startServe(t, bin, repoRoot, "--addr", "127.0.0.1:0")
	if p == nil {
		t.Fatalf("serve was refused inside a repo-config tree")
	}
	defer p.stop(t)

	apps := getQueueApps(t, p.url)
	if !containsAll(apps, "app-web", "app-ios", "app-android") {
		t.Fatalf("/api/queue apps = %v, want app-web, app-ios and app-android from both roots", apps)
	}
	if !strings.Contains(p.stderr.String(), "retrace.repo.yaml") {
		t.Fatalf("serve did not report finding retrace.repo.yaml:\n%s", p.stderr.String())
	}
}

// With no retrace.repo.yaml anywhere above cwd, `retrace serve` behaves
// exactly as it always has: only the current directory's own apps, even
// when a sibling directory has runs of its own — nothing implicitly
// widens what gets served.
func TestServeWithNoRepoConfigServesOnlyCwd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := buildRetrace(t)

	parent := t.TempDir()
	cwd := filepath.Join(parent, "web")
	sibling := filepath.Join(parent, "mobile")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	stubRun(t, cwd, "app-web", "login")
	stubRun(t, sibling, "app-ios", "onboarding")
	// No retrace.repo.yaml anywhere: parent has none, and t.TempDir()'s own
	// ancestry has no .git either, so Discover legitimately walks past
	// parent — this asserts the OUTCOME (sibling's app never appears)
	// rather than depending on where the search happens to stop.

	p, _ := startServe(t, bin, cwd, "--addr", "127.0.0.1:0")
	if p == nil {
		t.Fatalf("serve was refused with no repo config present")
	}
	defer p.stop(t)

	apps := getQueueApps(t, p.url)
	if !containsAll(apps, "app-web") {
		t.Fatalf("/api/queue apps = %v, want app-web (this cwd's own app)", apps)
	}
	if containsAll(apps, "app-ios") {
		t.Fatalf("/api/queue apps = %v included app-ios, a sibling directory's app that no repo config named", apps)
	}
}

// --- 4.2/4.3: --watch ----------------------------------------------------

// --watch --interval, against a two-root repo config, syncs each root
// scoped to only that root's own apps within one interval — no manual
// `retrace sync` call, and no cross-root leakage of apps (design.md D4).
func TestServeWatchSyncsEachRootWithItsOwnAppsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := buildRetrace(t)
	fakeGHOnPath(t)

	repoRoot := t.TempDir()
	web := filepath.Join(repoRoot, "web")
	mobile := filepath.Join(repoRoot, "mobile")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	if err := os.MkdirAll(mobile, 0o755); err != nil {
		t.Fatalf("mkdir mobile: %v", err)
	}
	writeRepoConfig(t, repoRoot, map[string]string{
		"app-web": "web",
		"app-ios": "mobile",
	})

	// One `gh run list` fixture, shared by both roots' sync calls (the fake
	// gh answers identically regardless of --repo/--branch/etc.) — its
	// downloaded artifact carries BOTH apps' run directories, exactly the
	// shape design.md D4 exists for: without the allowlist, either root's
	// sync would merge the other's app too.
	runListPath := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(runListPath, []byte(`[{"databaseId": 1, "workflowName": "retrace", "headSha": "aaa1111", "url": "https://github.com/acme/sample-app/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", runListPath)

	downloadRoot := t.TempDir()
	for _, entry := range []struct{ app, flow string }{
		{"app-web", "login"},
		{"app-ios", "onboarding"},
	} {
		dir := filepath.Join(downloadRoot, "1", entry.app, entry.flow, "20260827T090000Z-aaa1111")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("staging download fixture: %v", err)
		}
		manifest := fmt.Sprintf(`{"schema":"retrace/1","app":%q,"flow":%q,"runId":"20260827T090000Z-aaa1111","mode":"standalone","capture":{"status":"ok","summary":"ok"},"wire":{"recorded":true},"groups":[]}`, entry.app, entry.flow)
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("writing fixture manifest.json: %v", err)
		}
	}
	t.Setenv("GH_FAKE_DOWNLOAD_SRC", downloadRoot)

	p, _ := startServe(t, bin, repoRoot, "--addr", "127.0.0.1:0", "--watch", "--interval", "50ms", "--repo", "acme/sample-app")
	if p == nil {
		t.Fatalf("serve --watch was refused")
	}
	defer p.stop(t)

	// The immediate on-start sync (startWatch's doc comment: "so a fresh
	// retrace serve --watch shows CI data without waiting a full interval")
	// means this does not need to wait out an interval at all — poll
	// briefly for it to land, rather than asserting instantly, since the
	// sync itself runs in a goroutine.
	deadline := time.Now().Add(5 * time.Second)
	var webOK, iosOK bool
	for time.Now().Before(deadline) {
		webOK = fileExists(filepath.Join(web, ".retrace", "runs", "app-web", "login", "20260827T090000Z-aaa1111", "manifest.json"))
		iosOK = fileExists(filepath.Join(mobile, ".retrace", "runs", "app-ios", "onboarding", "20260827T090000Z-aaa1111", "manifest.json"))
		if webOK && iosOK {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !webOK {
		t.Fatalf("web root never synced its own app-web run\nstderr: %s", p.stderr.String())
	}
	if !iosOK {
		t.Fatalf("mobile root never synced its own app-ios run\nstderr: %s", p.stderr.String())
	}
	// Cross-root leakage: web's root must never gain app-ios's run, and
	// vice versa — this is the allowlist's own assertion, not merely "both
	// eventually synced something".
	if fileExists(filepath.Join(web, ".retrace", "runs", "app-ios")) {
		t.Fatalf("web root's sync merged app-ios, which is not its own app")
	}
	if fileExists(filepath.Join(mobile, ".retrace", "runs", "app-web")) {
		t.Fatalf("mobile root's sync merged app-web, which is not its own app")
	}
}

// flakyGHOnPath is fakeGHOnPath's `gh run list` gated behind a marker file:
// absent, it fails (simulating a transient GitHub/gh error); once ready
// marks it present, it answers exactly as fakeGHOnPath's script does. This
// lets one long-running `retrace serve --watch` process see a tick fail and
// a LATER tick (same process, same ticker) succeed, without needing to
// change the child process's own PATH after it has already started —
// which exec.Cmd cannot do, since a child's environment is fixed at
// Start().
func flakyGHOnPath(t *testing.T) (ready string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a POSIX shell script")
	}
	dir := t.TempDir()
	readyDir := t.TempDir()
	ready = filepath.Join(readyDir, "ready")
	script := `#!/bin/sh
set -e
if [ "$1" = "run" ] && [ "$2" = "list" ]; then
  if [ ! -f "$GH_FAKE_READY" ]; then
    echo "fake gh: run list: not ready yet" >&2
    exit 1
  fi
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
		t.Fatalf("writing flaky gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_FAKE_READY", ready)
	return ready
}

// A sync failure on one tick is written to stderr and does not stop the
// server or the ticker: the HTTP server keeps answering while a tick is
// failing, and a LATER tick — same process, once `gh` starts succeeding —
// still syncs. This is --watch's whole reason for existing: a transient
// sync failure must never cost a developer the dashboard they are actively
// looking at.
func TestServeWatchSurvivesASyncFailureAndRecoversOnALaterTick(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := buildRetrace(t)
	ready := flakyGHOnPath(t)

	cwd := t.TempDir()
	runListPath := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(runListPath, []byte(`[{"databaseId": 1, "workflowName": "retrace", "headSha": "aaa1111", "url": "https://github.com/acme/sample-app/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`), 0o644); err != nil {
		t.Fatalf("writing run list fixture: %v", err)
	}
	t.Setenv("GH_FAKE_RUN_LIST_JSON", runListPath)
	downloadRoot := t.TempDir()
	runDir := filepath.Join(downloadRoot, "1", "app-web", "login", "20260827T090000Z-aaa1111")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("staging download fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte(`{"schema":"retrace/1","app":"app-web","flow":"login","runId":"20260827T090000Z-aaa1111","mode":"standalone","capture":{"status":"ok","summary":"ok"},"wire":{"recorded":true},"groups":[]}`), 0o644); err != nil {
		t.Fatalf("writing fixture manifest.json: %v", err)
	}
	t.Setenv("GH_FAKE_DOWNLOAD_SRC", downloadRoot)

	p, _ := startServe(t, bin, cwd, "--addr", "127.0.0.1:0", "--watch", "--interval", "50ms", "--repo", "acme/sample-app")
	if p == nil {
		t.Fatalf("serve --watch was refused")
	}
	defer p.stop(t)

	// The immediate on-start tick runs before "ready" exists, so it fails —
	// and the server must still answer while that is happening.
	deadline := time.Now().Add(5 * time.Second)
	var healthy bool
	for time.Now().Before(deadline) {
		if code, _ := healthWithHost(t, p.url, ""); code == http.StatusOK {
			healthy = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("GET /api/health never succeeded while a sync tick was failing\nstderr: %s", p.stderr.String())
	}
	if !strings.Contains(p.stderr.String(), "sync") {
		t.Fatalf("the failing sync tick was not reported to stderr:\n%s", p.stderr.String())
	}
	if fileExists(filepath.Join(cwd, ".retrace", "runs", "app-web")) {
		t.Fatalf("a run was merged despite every tick so far having failed")
	}

	// Now let `gh run list` succeed — the SAME process's ticker (interval
	// 50ms) must pick this up on its very next tick, with no restart.
	if err := os.WriteFile(ready, []byte("1"), 0o644); err != nil {
		t.Fatalf("marking gh ready: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	var synced bool
	for time.Now().Before(deadline) {
		if fileExists(filepath.Join(cwd, ".retrace", "runs", "app-web", "login", "20260827T090000Z-aaa1111", "manifest.json")) {
			synced = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !synced {
		t.Fatalf("a later tick, with a working gh, never synced\nstderr: %s", p.stderr.String())
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// --- 4.4: usage text ------------------------------------------------------

// -h documents --watch/--interval and the retrace.repo.yaml discovery
// behavior — the two things this change adds that no per-flag usage
// string alone would make discoverable.
func TestServeUsageDocumentsWatchAndRepoConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := buildRetrace(t)
	cmdOut := runRetrace(t, bin, t.TempDir(), "", "serve", "-h")
	if cmdOut.code != exitUsage {
		t.Fatalf("serve -h exit = %d, want %d (usage)", cmdOut.code, exitUsage)
	}
	for _, want := range []string{"--watch", "--interval", "retrace.repo.yaml"} {
		if !strings.Contains(cmdOut.stderr, want) {
			t.Fatalf("serve -h output missing %q:\n%s", want, cmdOut.stderr)
		}
	}
}
