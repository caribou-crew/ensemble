package sync

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// stagePNG writes a minimal valid PNG at path, sized w×h — enough for
// image.DecodeConfig to read real geometry from the header.
func stagePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stageWebReplayDownload prepares what a `gh run download <databaseID>`
// call should produce for a manifest-less pixel-replay bundle: shots/ and
// groups.jsonl directly under <app>/<flow>/<runID>, with no manifest.json
// anywhere — mirroring a Playwright `retrace replay` artifact's shape.
func stageWebReplayDownload(t *testing.T, databaseID int64, app, flow, runID string) {
	t.Helper()
	root := os.Getenv("GH_FAKE_DOWNLOAD_SRC")
	if root == "" {
		root = t.TempDir()
		t.Setenv("GH_FAKE_DOWNLOAD_SRC", root)
	}
	runDir := filepath.Join(root, itoa(databaseID), app, flow, runID)
	stagePNG(t, filepath.Join(runDir, "shots", "pan-loaded.png"), 20, 30)
	stagePNG(t, filepath.Join(runDir, "shots", "pan-revealed.png"), 20, 30)
	if err := os.WriteFile(filepath.Join(runDir, "groups.jsonl"), []byte(`{"phase":"start","name":"pan","ts":"2026-08-28T23:41:54Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestWireOnlyReplayBundleIsMergedAsARealRun(t *testing.T) {
	// The FLAG_SECURE mobile case: an Android RN/native card flow whose PCI
	// views cannot be screenshotted, so the replay bundle has wire.jsonl but
	// NO shots/. It must still sync as a real, wire-comparable run.
	fakeGH(t)
	now := time.Date(2026, 9, 1, 23, 45, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 7, "workflowName": "E2E Android", "headSha": "abc1234", "headBranch": "main", "event": "push", "url": "https://github.com/org/repo/actions/runs/7", "createdAt": "2026-09-01T22:00:00Z", "status": "completed"}]`)
	root := t.TempDir()
	t.Setenv("GH_FAKE_DOWNLOAD_SRC", root)
	runDir := filepath.Join(root, "7", "uxt-rn-android", "card-views", "20260901T221845Z-abc1234")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A wire.jsonl with one hop — no shots dir at all.
	if err := os.WriteFile(filepath.Join(runDir, "wire.jsonl"),
		[]byte(`{"schema":"ensemble/1","seq":1,"to":"edge","method":"GET","path":"/api/v1/cards/x/showpan","status":200}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageActor(t, 7, "octocat")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 || res.Synced[0] != "uxt-rn-android/card-views/20260901T221845Z-abc1234" {
		t.Fatalf("Synced = %v, want the wire-only run", res.Synced)
	}
	dest := filepath.Join(runs.RunsRoot(cwd), "uxt-rn-android", "card-views", "20260901T221845Z-abc1234")
	m, err := runs.ReadManifest(filepath.Join(dest, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if !m.Wire.Recorded {
		t.Error("Wire.Recorded = false, want true — the run carries a wire plane")
	}
	if len(m.Checkpoints) != 0 {
		t.Errorf("Checkpoints = %+v, want none (shots are FLAG_SECURE-blocked)", m.Checkpoints)
	}
	if m.Capture.Status != trace.VerdictOK {
		t.Errorf("Capture.Status = %q, want ok — a wire-only run is diffable", m.Capture.Status)
	}
	if _, err := os.Stat(filepath.Join(dest, "wire.jsonl")); err != nil {
		t.Errorf("wire.jsonl did not travel with the synced run: %v", err)
	}
}

func TestWebReplayBundleIsMergedAsPixelOnlyRun(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 28, 23, 45, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "Retrace Web Replay", "headSha": "2bf33e4", "headBranch": "main", "event": "push", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-28T23:00:00Z", "status": "completed"}]`)
	stageWebReplayDownload(t, 1, "web", "card-views", "20260828T234154Z-2bf33e4")
	stageActor(t, 1, "octocat")

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped = %v, want none", res.Skipped)
	}
	if len(res.Synced) != 1 || res.Synced[0] != "web/card-views/20260828T234154Z-2bf33e4" {
		t.Fatalf("Synced = %v, want [web/card-views/20260828T234154Z-2bf33e4]", res.Synced)
	}

	dest := filepath.Join(runs.RunsRoot(cwd), "web", "card-views", "20260828T234154Z-2bf33e4")
	m, err := runs.ReadManifest(filepath.Join(dest, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Mode != runs.ModePixel {
		t.Errorf("Mode = %q, want %q", m.Mode, runs.ModePixel)
	}
	if len(m.Checkpoints) != 2 {
		t.Fatalf("Checkpoints = %+v, want 2", m.Checkpoints)
	}
	if m.Capture.Status != trace.VerdictOK {
		t.Errorf("Capture.Status = %q, want %q", m.Capture.Status, trace.VerdictOK)
	}
	if m.Wire.Recorded {
		t.Errorf("Wire.Recorded = true, want false — a pixel-only replay has no wire plane")
	}

	// groups.jsonl travels with the copied tree even though it is not
	// folded into Manifest.Groups (pixel-only runs have no wire diff to
	// bucket into named sections).
	if _, err := os.Stat(filepath.Join(dest, "groups.jsonl")); err != nil {
		t.Errorf("groups.jsonl missing after merge: %v", err)
	}

	src, err := runs.ReadSource(runs.Paths{RunDir: dest})
	if err != nil || src == nil {
		t.Fatalf("ReadSource: %v, %+v", err, src)
	}
	if src.Actor != "octocat" {
		t.Errorf("Actor = %q, want octocat", src.Actor)
	}

	done, err := runs.ReadFinalized(runs.Paths{RunDir: dest})
	if err != nil {
		t.Fatalf("ReadFinalized: %v", err)
	}
	if done == nil {
		t.Fatal("run was not finalized — `retrace runs` would report it abandoned")
	}
}

// The Playwright reporter (adapters/playwright) writes video and its HTML
// report into the ACTIVE run's videos/ and report/ dirs at capture time,
// alongside shots/ — so by the time this bundle is a downloaded CI
// artifact, videos/report are already part of the tree the web-replay path
// copies. This pins that copyTree(b.runDir, ...) carries them through
// unchanged, with no separate video-routing step needed in sync itself.
func TestWebReplayBundleCarriesVideoAndReportThrough(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 28, 23, 45, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "Retrace Web Replay", "headSha": "2bf33e4", "headBranch": "main", "event": "push", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-28T23:00:00Z", "status": "completed"}]`)
	stageWebReplayDownload(t, 1, "web", "card-views", "20260828T234154Z-2bf33e4")
	stageActor(t, 1, "octocat")

	root := os.Getenv("GH_FAKE_DOWNLOAD_SRC")
	runDir := filepath.Join(root, itoa(1), "web", "card-views", "20260828T234154Z-2bf33e4")
	if err := os.MkdirAll(filepath.Join(runDir, "videos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "videos", "pan.webm"), []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runDir, "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "report", "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	if _, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	dest := filepath.Join(runs.RunsRoot(cwd), "web", "card-views", "20260828T234154Z-2bf33e4")
	if b, err := os.ReadFile(filepath.Join(dest, "videos", "pan.webm")); err != nil || string(b) != "fake-video" {
		t.Errorf("videos/pan.webm after merge = %q, %v, want \"fake-video\", nil", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "report", "index.html")); err != nil || string(b) != "<html></html>" {
		t.Errorf("report/index.html after merge = %q, %v, want \"<html></html>\", nil", b, err)
	}
}

// A run whose artifact carries BOTH a manifest.json and a shots/ dir is a
// native run; the web-replay merge path must never double-merge it.
func TestNativeRunWithShotsIsNotDoubleMerged(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "retrace-ios", "headSha": "aaa1111", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-27T10:00:00Z", "status": "completed"}]`)
	stageDownload(t, 1, "ios", "checkout", "20260827T090000Z-aaa1111")
	root := os.Getenv("GH_FAKE_DOWNLOAD_SRC")
	stagePNG(t, filepath.Join(root, "1", "ios", "checkout", "20260827T090000Z-aaa1111", "shots", "cart.png"), 10, 10)

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 1 {
		t.Fatalf("Synced = %v, want exactly 1 (the manifest-based merge, not a duplicate pixel merge)", res.Synced)
	}
}

func TestZeroShotReplayIsQuarantinedNotOk(t *testing.T) {
	fakeGH(t)
	now := time.Date(2026, 8, 28, 23, 45, 0, 0, time.UTC)
	writeRunListJSON(t, `[{"databaseId": 1, "workflowName": "Retrace Web Replay", "headSha": "2bf33e4", "url": "https://github.com/org/repo/actions/runs/1", "createdAt": "2026-08-28T23:00:00Z", "status": "completed"}]`)
	root := os.Getenv("GH_FAKE_DOWNLOAD_SRC")
	if root == "" {
		root = t.TempDir()
		t.Setenv("GH_FAKE_DOWNLOAD_SRC", root)
	}
	// shots/ exists but is empty — dirHasPNG must say no, so this bundle is
	// never even recognized as a web replay (there is nothing to diff).
	runDir := filepath.Join(root, "1", "web", "card-views", "20260828T234154Z-2bf33e4")
	if err := os.MkdirAll(filepath.Join(runDir, "shots"), 0o755); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	res, err := Run(Options{Cwd: cwd, From: "github", Repo: "org/repo", Now: fixedNow(t, now)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Synced) != 0 {
		t.Fatalf("Synced = %v, want none — an empty shots dir has nothing to diff", res.Synced)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want 1", res.Skipped)
	}
}

func TestAssessReplay(t *testing.T) {
	// Neither shots nor wire → fail closed (nothing to diff).
	if got := assessReplay(0, false); got.Status != trace.VerdictFailed {
		t.Errorf("no shots/no wire: Status = %q, want %q", got.Status, trace.VerdictFailed)
	}
	// Shots only (browser/Playwright) → ok.
	if got := assessReplay(3, false); got.Status != trace.VerdictOK || got.Summary == "" {
		t.Errorf("3 shots: got %+v, want ok with summary", got)
	}
	// Wire only (FLAG_SECURE mobile) → ok, judged on the wire plane.
	got := assessReplay(0, true)
	if got.Status != trace.VerdictOK {
		t.Errorf("wire-only: Status = %q, want %q", got.Status, trace.VerdictOK)
	}
	if got.Summary == "" {
		t.Error("wire-only: Summary is empty")
	}
	// Both → ok.
	if got := assessReplay(5, true); got.Status != trace.VerdictOK {
		t.Errorf("shots+wire: Status = %q, want %q", got.Status, trace.VerdictOK)
	}
}
