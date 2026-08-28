package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// writeEvidenceFixture writes a video and a two-file report under web/search's
// latest run (runB, from threeFlowProject) — the candidate ("b") run every
// evidence route resolves to.
func writeEvidenceFixture(t *testing.T, cwd string) {
	t.Helper()
	dir := filepath.Join(runs.RunsRoot(cwd), "web", "search", runB)
	if err := os.MkdirAll(filepath.Join(dir, "videos"), 0o755); err != nil {
		t.Fatalf("mkdir videos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "videos", "results.webm"), []byte("fake webm"), 0o644); err != nil {
		t.Fatalf("writing fixture video: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "report", "assets"), 0o755); err != nil {
		t.Fatalf("mkdir report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report", "index.html"), []byte("<html>report</html>"), 0o644); err != nil {
		t.Fatalf("writing fixture report index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report", "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("writing fixture report asset: %v", err)
	}
}

func TestWriteEvidenceListsVideosAndReportPresence(t *testing.T) {
	cwd := threeFlowProject(t)
	writeEvidenceFixture(t, cwd)
	d := deps(t, cwd)

	rec := httptest.NewRecorder()
	WriteEvidence(rec, d, "web", "search")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got == "" {
		t.Fatalf("empty body")
	}
	var got Evidence
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Videos) != 1 || got.Videos[0] != "results.webm" {
		t.Fatalf("Videos = %v, want [results.webm]", got.Videos)
	}
	if !got.HasReport {
		t.Fatalf("HasReport = false, want true")
	}
}

func TestWriteEvidenceReportsNoneWhenNothingAttached(t *testing.T) {
	cwd := threeFlowProject(t) // web/login has no videos/report subdirs at all
	d := deps(t, cwd)

	rec := httptest.NewRecorder()
	WriteEvidence(rec, d, "web", "login")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got Evidence
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Videos) != 0 {
		t.Fatalf("Videos = %v, want empty", got.Videos)
	}
	if got.HasReport {
		t.Fatalf("HasReport = true, want false")
	}
}

func TestWriteEvidenceWorksWithNoAcceptedReference(t *testing.T) {
	// A flow with a run but no `retrace ref accept` yet must still show
	// evidence — SummaryFor would refuse this flow with "no reference", but
	// evidence describes the raw run, not the diff (design doc D4).
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "fresh", runA, map[string][]byte{"home": shotPNG(t, white)}, nil)
	dir := filepath.Join(runs.RunsRoot(cwd), "web", "fresh", runA, "videos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "home.webm"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing fixture video: %v", err)
	}
	d := deps(t, cwd)

	rec := httptest.NewRecorder()
	WriteEvidence(rec, d, "web", "fresh")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got Evidence
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Videos) != 1 || got.Videos[0] != "home.webm" {
		t.Fatalf("Videos = %v, want [home.webm]", got.Videos)
	}
}

func TestWriteVideoServesTheFileWithRangeSupport(t *testing.T) {
	cwd := threeFlowProject(t)
	writeEvidenceFixture(t, cwd)
	d := deps(t, cwd)

	req := httptest.NewRequest(http.MethodGet, "/api/videos/web/search/results.webm", nil)
	rec := httptest.NewRecorder()
	WriteVideo(rec, req, d, "web", "search", "results.webm")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != "fake webm" {
		t.Fatalf("body = %q, want %q", body, "fake webm")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/web/search/results.webm", nil)
	req.Header.Set("Range", "bytes=0-3")
	rec = httptest.NewRecorder()
	WriteVideo(rec, req, d, "web", "search", "results.webm")
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("ranged request status = %d, want 206", rec.Code)
	}
	if got, _ := io.ReadAll(rec.Body); string(got) != "fake" {
		t.Fatalf("ranged body = %q, want %q", got, "fake")
	}
}

func TestWriteVideoUnknownNameIs404(t *testing.T) {
	cwd := threeFlowProject(t)
	writeEvidenceFixture(t, cwd)
	d := deps(t, cwd)

	req := httptest.NewRequest(http.MethodGet, "/api/videos/web/search/nope.webm", nil)
	rec := httptest.NewRecorder()
	WriteVideo(rec, req, d, "web", "search", "nope.webm")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// This goes through a REAL httptest.Server (newServer/get, the same
// helpers TestShotPathsCannotEscapeTheRunDirectory uses), not a direct
// WriteVideo call: ServeMux's path cleaning runs on the still-ESCAPED
// path, so "%2e%2e%2f" reaches the handler's {name} value as the literal
// string "../../nested/secret.webm" rather than being collapsed by
// path.Clean the way an already-decoded string passed directly to
// safeBase would be.
//
// The bait file is deliberately nested (not "../../secret.webm" at the
// root): Clean's rooted-path rule strips LEADING ".." segments entirely,
// so "/../../secret.webm" cleans to "/secret.webm" — a single segment
// with no surviving "/", which safeBase would wrongly accept. Nesting the
// bait one directory deeper ("../../nested/secret.webm") leaves a "/"
// after the leading ".." segments are stripped ("/nested/secret.webm"),
// which is what safeBase's separator check actually catches — mirroring
// TestShotPathsCannotEscapeTheRunDirectory's own "etc/passwd" bait shape.
func TestWriteVideoRejectsTraversal(t *testing.T) {
	cwd := threeFlowProject(t)
	writeEvidenceFixture(t, cwd)
	ts := newServer(t, cwd)

	nested := filepath.Join(cwd, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	secret := filepath.Join(nested, "secret.webm")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatalf("writing the bait file: %v", err)
	}

	r := get(t, ts, "/api/videos/web/search/%2e%2e%2f%2e%2e%2fnested%2fsecret.webm")
	if r.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\n%s", r.status, r.body)
	}
}

func TestWriteReportServesIndexAndAssets(t *testing.T) {
	cwd := threeFlowProject(t)
	writeEvidenceFixture(t, cwd)
	d := deps(t, cwd)

	req := httptest.NewRequest(http.MethodGet, "/api/report/web/search/", nil)
	rec := httptest.NewRecorder()
	WriteReport(rec, req, d, "web", "search")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "<html>report</html>" {
		t.Fatalf("index body = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/report/web/search/assets/app.js", nil)
	req.SetPathValue("path", "assets/app.js")
	rec = httptest.NewRecorder()
	WriteReport(rec, req, d, "web", "search")
	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "console.log(1)" {
		t.Fatalf("asset body = %q", got)
	}
}

func TestWriteReportMissingIs404(t *testing.T) {
	cwd := threeFlowProject(t) // web/login has no report/ at all
	d := deps(t, cwd)

	req := httptest.NewRequest(http.MethodGet, "/api/report/web/login/", nil)
	rec := httptest.NewRecorder()
	WriteReport(rec, req, d, "web", "login")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
