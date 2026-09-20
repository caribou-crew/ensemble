package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
	retraceconfig "github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/pairs"
	"github.com/caribou-crew/ensemble/retrace/runs"
	"github.com/caribou-crew/ensemble/retrace/serve"
)

func TestEmbeddedRetraceEvidenceReadParity(t *testing.T) {
	cwd := t.TempDir()
	cfg, err := retraceconfig.Discover(cwd)
	if err != nil {
		t.Fatal(err)
	}
	embedded := New(Deps{Cfg: &config.Config{Dir: cwd}})
	standalone := serve.New(serve.Deps{Cwd: cwd, Cfg: cfg})
	for _, path := range []string{
		"/queue/web/sign-in/runs/run-1",
		"/pairs/web/sign-in/run-1/pair-1",
		"/shots/web/sign-in/runs/run-1/a/login.png",
		"/pairs/web/sign-in/run-1/pair-1/shots/a/login.png",
		"/evidence/web/sign-in",
		"/videos/web/sign-in/session.mp4",
		"/report/web/sign-in",
		"/report/web/sign-in/index.html",
		"/queue/web/%2e%2e/runs/run-1",
	} {
		t.Run(path, func(t *testing.T) {
			want := httptest.NewRecorder()
			standalone.ServeHTTP(want, httptest.NewRequest("GET", "http://localhost/api"+path, nil))
			got := httptest.NewRecorder()
			embedded.ServeHTTP(got, httptest.NewRequest("GET", "http://localhost/api/retrace"+path, nil))
			if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
				t.Fatalf("embedded %d %s differs from standalone %d %s", got.Code, got.Body, want.Code, want.Body)
			}
			if !strings.Contains(got.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("API fell through to UI: %s", got.Body)
			}
		})
	}
	// A persisted comparison is served from the candidate identity in the URL.
	pairDir := filepath.Join(cwd, ".retrace", "runs", "web", "sign-in", "run-1", "diffs", "pair-1")
	if err := os.MkdirAll(pairDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pairDir, "summary.json"), []byte(`{"flow":"sign-in","verdict":"failed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	pair := httptest.NewRecorder()
	embedded.ServeHTTP(pair, httptest.NewRequest("GET", "http://localhost/api/retrace/pairs/web/sign-in/run-1/pair-1", nil))
	if pair.Code != 200 || !strings.Contains(pair.Body.String(), `"verdict":"failed"`) {
		t.Fatalf("persisted pair not served: %d %s", pair.Code, pair.Body)
	}
	// Configuration is read from the ensemble project root, and a bad config
	// cannot silently select default comparison rules.
	if err := os.WriteFile(filepath.Join(cwd, "retrace.yaml"), []byte("not: [valid"), 0600); err != nil {
		t.Fatal(err)
	}
	got := httptest.NewRecorder()
	embedded.ServeHTTP(got, httptest.NewRequest("GET", "http://localhost/api/retrace/evidence/web/sign-in", nil))
	if got.Code != 500 || !strings.Contains(got.Body.String(), `"error"`) {
		t.Fatalf("invalid project config hidden: %d %s", got.Code, got.Body)
	}
}

func TestEmbeddedRetraceEvidenceRejectsWrites(t *testing.T) {
	cwd := t.TempDir()
	h := New(Deps{Cfg: &config.Config{Dir: cwd}})
	for _, path := range []string{
		"/queue/web/sign-in/accept", "/queue/web/sign-in/reject",
		"/queue/web/sign-in/rule", "/queue/web/sign-in/redact", "/sync",
		"/queue/web/sign-in/runs/run-1", "/pairs/web/sign-in/run-1/pair-1",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "http://localhost/api/retrace"+path, strings.NewReader(`{}`)))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s reached handler: %d %s", path, w.Code, w.Body)
		}
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only mount wrote files: %v %v", entries, err)
	}
}

func TestEmbeddedRetraceEvidenceMappedRootsAndGuards(t *testing.T) {
	project, aRoot, bRoot := t.TempDir(), t.TempDir(), t.TempDir()
	repo := fmt.Sprintf("apps:\n  legacy:\n    root: %q\n  taxi:\n    root: %q\n", aRoot, bRoot)
	if err := os.WriteFile(filepath.Join(project, "retrace.repo.yaml"), []byte(repo), 0600); err != nil {
		t.Fatal(err)
	}
	aDir := filepath.Join(aRoot, ".retrace", "runs", "legacy", "login", "a-run")
	bDir := filepath.Join(bRoot, ".retrace", "runs", "taxi", "login", "b-run")
	for dir, data := range map[string][]byte{aDir: []byte("legacy-image"), bDir: []byte("taxi-image")} {
		if err := os.MkdirAll(filepath.Join(dir, "shots"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "shots", "login.png"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := diff.RunRef{Kind: "run", RunID: "a-run", Dir: "/stale/a", Manifest: runs.Manifest{App: "legacy", Checkpoints: []runs.Checkpoint{{Name: "login", File: "shots/login.png"}}}}
	b := diff.RunRef{Kind: "run", RunID: "b-run", Dir: "/stale/b", Manifest: runs.Manifest{App: "taxi", Checkpoints: []runs.Checkpoint{{Name: "login", File: "shots/login.png"}}}}
	pair, err := pairs.Persist(pairs.DirFor(bDir, a), diff.Summary{Schema: diff.SummarySchema, Flow: "login", Verdict: "changed", A: a, B: b}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Cfg: &config.Config{Dir: project}, AllowedHosts: []string{"review.test"}})
	path := "/api/retrace/pairs/taxi/login/b-run/" + pair.PairID
	request := func(host, route string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+route, nil))
		return w
	}
	if got := request("review.test", path); got.Code != 200 || !strings.Contains(got.Body.String(), `"verdict":"changed"`) {
		t.Fatalf("mapped pair: %d %s", got.Code, got.Body)
	}
	for side, want := range map[string]string{"a": "legacy-image", "b": "taxi-image"} {
		got := request("review.test", path+"/shots/"+side+"/login")
		if got.Code != 200 || got.Body.String() != want {
			t.Fatalf("mapped %s screenshot: %d %s", side, got.Code, got.Body)
		}
	}
	if got := request("attacker.example", path); got.Code != 403 {
		t.Fatalf("Host guard bypassed: %d", got.Code)
	}
	req := httptest.NewRequest("GET", "http://review.test"+path, nil)
	req.Header.Set("Origin", "http://attacker.example")
	guarded := httptest.NewRecorder()
	h.ServeHTTP(guarded, req)
	if guarded.Code != 403 {
		t.Fatalf("Origin guard bypassed: %d", guarded.Code)
	}
	if err := os.WriteFile(filepath.Join(project, "retrace.repo.yaml"), []byte("apps: [invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := request("review.test", path); got.Code != 500 || !strings.Contains(got.Body.String(), `"error"`) {
		t.Fatalf("invalid mapped config hidden: %d %s", got.Code, got.Body)
	}
}
