package server_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/server"
	retraceconfig "github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
	retraceserve "github.com/caribou-crew/ensemble/retrace/serve"
)

// --- fixtures -------------------------------------------------------
//
// These mirror retrace/serve/queue_test.go's recordRun/acceptRef/shotPNG
// helpers (unexported there, package serve) closely enough that a queue
// built from this fixture is directly comparable to one retrace/serve
// itself would build over the same directory.

func retraceShotPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding a fixture shot: %v", err)
	}
	return buf.Bytes()
}

func recordRetraceRun(t *testing.T, cwd, app, flow, runID string) runs.Paths {
	t.Helper()
	p, err := runs.Create(runs.RunsRoot(cwd), app, flow, runID)
	if err != nil {
		t.Fatalf("runs.Create(%s/%s/%s): %v", app, flow, runID, err)
	}
	shot := retraceShotPNG(t, color.RGBA{255, 255, 255, 255})
	if err := os.WriteFile(filepath.Join(p.RunDir, "shots", "home.png"), shot, 0o644); err != nil {
		t.Fatalf("writing fixture shot: %v", err)
	}
	h := trace.Hop{
		Schema: trace.SchemaVersion, Seq: 1, From: "web", To: "api",
		Method: "GET", Path: "/home", Status: 200,
		T:    trace.Timings{Start: time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC), DoneMs: 10},
		Resp: trace.Payload{Headers: map[string]string{"content-type": "application/json"}, Body: `{"ok":true}`},
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshalling fixture hop: %v", err)
	}
	if err := os.WriteFile(p.WirePath, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("writing wire.jsonl: %v", err)
	}
	m := runs.Manifest{
		App: app, Flow: flow, RunID: runID, Mode: runs.ModeStandalone,
		Git:         runs.Git{SHA: "deadbee", Branch: "main"},
		StartedAt:   time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		FinishedAt:  time.Date(2026, 8, 21, 10, 0, 5, 0, time.UTC),
		Capture:     runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"},
		Wire:        runs.Counts{Calls: 1, Recorded: true},
		Checkpoints: []runs.Checkpoint{{Name: "home", File: "shots/home.png", Width: 10, Height: 10}},
	}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest(%s): %v", runID, err)
	}
	return p
}

func acceptRetraceRef(t *testing.T, cwd, app, flow, runID string) {
	t.Helper()
	if _, err := refs.Accept(refs.AcceptOptions{
		Cwd: cwd, RunsRoot: runs.RunsRoot(cwd), App: app, Flow: flow, RunID: runID,
	}); err != nil {
		t.Fatalf("refs.Accept(%s/%s/%s): %v", app, flow, runID, err)
	}
}

// onePassingFlow builds a minimal `.retrace/` tree under a fresh temp dir
// with one accepted reference and one identical run — a flow that reports
// PASS end to end, which is enough to exercise the queue/item/shot routes
// without needing this package to duplicate retrace/diff's verdict logic.
func onePassingFlow(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	recordRetraceRun(t, cwd, "web", "home", "20260821T100000Z-aaaaaaa")
	acceptRetraceRef(t, cwd, "web", "home", "20260821T100000Z-aaaaaaa")
	recordRetraceRun(t, cwd, "web", "home", "20260821T101000Z-bbbbbbb")
	return cwd
}

func newRetraceTestEnv(t *testing.T, retrace *config.RetraceConfig) (*httptest.Server, string) {
	t.Helper()
	cwd := onePassingFlow(t)
	cfg := &config.Config{Dir: cwd, Retrace: retrace}
	ts := httptest.NewServer(server.New(server.Deps{Cfg: cfg, Version: "test"}))
	t.Cleanup(ts.Close)
	return ts, cwd
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body of %s: %v", url, err)
	}
	if len(body) == 0 {
		return resp.StatusCode, nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("GET %s: body is not a JSON object (%d): %v\n%s", url, resp.StatusCode, err, body)
	}
	return resp.StatusCode, out
}

// --- tests ------------------------------------------------------------

func TestRetraceRoutesAre501WithoutARetraceBlock(t *testing.T) {
	cwd := onePassingFlow(t)
	cfg := &config.Config{Dir: cwd} // no Retrace block at all
	ts := httptest.NewServer(server.New(server.Deps{Cfg: cfg, Version: "test"}))
	t.Cleanup(ts.Close)

	// The dashboard's SPA fallback answers any unmatched GET with a 200
	// app shell, so an unconfigured retrace route must say so itself in
	// JSON (501) rather than relying on a raw 404 the client could
	// otherwise mistake for "the page doesn't exist".
	resp, err := http.Get(ts.URL + "/api/retrace/queue")
	if err != nil {
		t.Fatalf("GET /api/retrace/queue: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when no retrace: block is configured; body = %s", resp.StatusCode, body)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("501 body is not JSON: %v\n%s", err, body)
	}
}

func TestRetraceQueueMatchesDirectServeQueue(t *testing.T) {
	ts, cwd := newRetraceTestEnv(t, &config.RetraceConfig{})

	status, got := getJSON(t, ts.URL+"/api/retrace/queue")
	if status != http.StatusOK {
		t.Fatalf("GET /api/retrace/queue status = %d, body = %v", status, got)
	}

	rcfg, err := retraceconfig.Discover(cwd)
	if err != nil {
		t.Fatalf("retraceconfig.Discover: %v", err)
	}
	rec := httptest.NewRecorder()
	retraceserve.WriteQueue(rec, retraceserve.Deps{Cwd: cwd, Cfg: rcfg, Version: "test"})
	var want map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &want); err != nil {
		t.Fatalf("unmarshalling direct WriteQueue output: %v", err)
	}

	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("ensemble's /api/retrace/queue diverges from retrace/serve.WriteQueue:\n got:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

func TestRetraceItemRoute(t *testing.T) {
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{})

	status, got := getJSON(t, ts.URL+"/api/retrace/queue/web/home")
	if status != http.StatusOK {
		t.Fatalf("GET /api/retrace/queue/web/home status = %d, body = %v", status, got)
	}
	sum, ok := got["summary"].(map[string]any)
	if !ok {
		t.Fatalf("item response has no summary object: %v", got)
	}
	if sum["app"] != "web" || sum["flow"] != "home" {
		t.Fatalf("summary missing app/flow: %v", sum)
	}
}

func TestRetraceItemRouteUnknownFlowIs404(t *testing.T) {
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{})

	status, _ := getJSON(t, ts.URL+"/api/retrace/queue/web/nope")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown flow", status)
	}
}

func TestRetraceShotRoute(t *testing.T) {
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{})

	resp, err := http.Get(ts.URL + "/api/retrace/shots/web/home/b/home")
	if err != nil {
		t.Fatalf("GET shot: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
}

func writeRetraceEvidenceFixture(t *testing.T, p runs.Paths) {
	t.Helper()
	videos := filepath.Join(p.RunDir, "videos")
	if err := os.MkdirAll(videos, 0o755); err != nil {
		t.Fatalf("mkdir videos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(videos, "home.webm"), []byte("fake webm"), 0o644); err != nil {
		t.Fatalf("writing fixture video: %v", err)
	}
	report := filepath.Join(p.RunDir, "report")
	if err := os.MkdirAll(report, 0o755); err != nil {
		t.Fatalf("mkdir report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(report, "index.html"), []byte("<html>report</html>"), 0o644); err != nil {
		t.Fatalf("writing fixture report: %v", err)
	}
}

func TestRetraceEvidenceRoute(t *testing.T) {
	ts, cwd := newRetraceTestEnv(t, &config.RetraceConfig{})
	p, err := runs.PathsFor(runs.RunsRoot(cwd), "web", "home", "20260821T101000Z-bbbbbbb")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	writeRetraceEvidenceFixture(t, p)

	status, got := getJSON(t, ts.URL+"/api/retrace/evidence/web/home")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, got)
	}
	videos, ok := got["videos"].([]any)
	if !ok || len(videos) != 1 || videos[0] != "home.webm" {
		t.Fatalf("videos = %v", got["videos"])
	}
	if got["hasReport"] != true {
		t.Fatalf("hasReport = %v, want true", got["hasReport"])
	}
}

func TestRetraceVideoRoute(t *testing.T) {
	ts, cwd := newRetraceTestEnv(t, &config.RetraceConfig{})
	p, err := runs.PathsFor(runs.RunsRoot(cwd), "web", "home", "20260821T101000Z-bbbbbbb")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	writeRetraceEvidenceFixture(t, p)

	resp, err := http.Get(ts.URL + "/api/retrace/videos/web/home/home.webm")
	if err != nil {
		t.Fatalf("GET video: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if string(body) != "fake webm" {
		t.Fatalf("body = %q", body)
	}
}

func TestRetraceReportRoute(t *testing.T) {
	ts, cwd := newRetraceTestEnv(t, &config.RetraceConfig{})
	p, err := runs.PathsFor(runs.RunsRoot(cwd), "web", "home", "20260821T101000Z-bbbbbbb")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	writeRetraceEvidenceFixture(t, p)

	resp, err := http.Get(ts.URL + "/api/retrace/report/web/home/")
	if err != nil {
		t.Fatalf("GET report: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if string(body) != "<html>report</html>" {
		t.Fatalf("body = %q", body)
	}
}

func TestRetraceEvidenceRoutesAre501WithoutARetraceBlock(t *testing.T) {
	cwd := onePassingFlow(t)
	cfg := &config.Config{Dir: cwd}
	ts := httptest.NewServer(server.New(server.Deps{Cfg: cfg, Version: "test"}))
	t.Cleanup(ts.Close)

	for _, path := range []string{
		"/api/retrace/evidence/web/home",
		"/api/retrace/videos/web/home/home.webm",
		"/api/retrace/report/web/home/",
		"/api/retrace/sync/candidates",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("GET %s status = %d, want 501", path, resp.StatusCode)
		}
	}
}

func TestRetraceSyncCandidatesRequiresRepo(t *testing.T) {
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{}) // Repo left empty

	resp, err := http.Get(ts.URL + "/api/retrace/sync/candidates")
	if err != nil {
		t.Fatalf("GET /api/retrace/sync/candidates: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400 when retrace.repo/repos is unset; body = %s", resp.StatusCode, body)
	}
}

func TestRetraceSyncCandidatesFailsFastWithoutGh(t *testing.T) {
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{Repo: "org/repo"})
	t.Setenv("PATH", t.TempDir()) // no gh anywhere on it

	resp, err := http.Get(ts.URL + "/api/retrace/sync/candidates")
	if err != nil {
		t.Fatalf("GET /api/retrace/sync/candidates: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("candidates succeeded with no gh on PATH: %s", body)
	}
	if !strings.Contains(string(body), "gh auth login") {
		t.Fatalf("error body does not name the CLI's own remedy (gh auth login): %s", body)
	}
}

func TestRetraceSyncWithEmptyBodyStillWorks(t *testing.T) {
	// Guards against a regression where decoding a JSON body starts
	// requiring one — the CLI and any pre-existing client post no body
	// at all (see TestRetraceSyncRequiresRepo's own http.Post(..., nil)).
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{Repo: "org/repo"})
	t.Setenv("PATH", t.TempDir()) // no gh: proves the request was parsed and reached sync.Run, not rejected as bad JSON

	resp, err := http.Post(ts.URL+"/api/retrace/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/retrace/sync: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("an empty body was rejected as bad JSON: %s", body)
	}
}

func TestRetraceSyncRejectsMalformedBody(t *testing.T) {
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{Repo: "org/repo"})

	resp, err := http.Post(ts.URL+"/api/retrace/sync", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("POST /api/retrace/sync: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400 for a malformed JSON body; body = %s", resp.StatusCode, body)
	}
}

func TestRetraceSyncRequiresRepo(t *testing.T) {
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{}) // Repo left empty

	resp, err := http.Post(ts.URL+"/api/retrace/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/retrace/sync: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when retrace.repo is unset; body = %s", resp.StatusCode, body)
	}
}

func TestRetraceSyncFailsFastWithoutGh(t *testing.T) {
	ts, _ := newRetraceTestEnv(t, &config.RetraceConfig{Repo: "org/repo"})
	t.Setenv("PATH", t.TempDir()) // no gh anywhere on it

	resp, err := http.Post(ts.URL+"/api/retrace/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/retrace/sync: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("sync succeeded with no gh on PATH: %s", body)
	}
	if !strings.Contains(string(body), "gh auth login") {
		t.Fatalf("error body does not name the CLI's own remedy (gh auth login): %s", body)
	}
}
