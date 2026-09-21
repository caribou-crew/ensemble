package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
	retraceconfig "github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/serve"
	"github.com/caribou-crew/ensemble/retrace/suites"
)

// The embedded dashboard must serve the gallery and stored screens exactly as
// standalone Retrace does, from the ensemble project root.
func TestEmbeddedSuiteGalleryAndScreensMatchStandalone(t *testing.T) {
	cwd := t.TempDir()
	inv := `{"schema":"retrace/suites/1","suites":[{"id":"taxi","title":"Taxi","version":"v1","platforms":["web","ios"],"features":[{"id":"w","title":"Wallet","flows":[{"id":"atm","title":"ATM","requiredPlanes":["functional"]}]}]}]}`
	if err := os.WriteFile(filepath.Join(cwd, "retrace.suites.json"), []byte(inv), 0o600); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa7\x9a\xa0\xa0\x00\x00\x00\x00IEND\xaeB`\x82")
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "final.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	report := suites.Attempt{Schema: suites.AttemptSchema, SuiteID: "taxi", SuiteVersion: "v1", AttemptID: "ios-1", Platform: "ios",
		Git: suites.Git{SHA: strings.Repeat("a", 40), Branch: "main"}, BaselineID: "b", PolicyID: "p", StartedAt: "2026-09-20T00:00:00Z", FinishedAt: "2026-09-20T00:01:00Z",
		Results: []suites.Result{{FlowID: "atm", Planes: suites.Planes{Functional: "pass", Wire: "not-applicable", Visual: "not-applicable"}, Screens: []suites.Screen{{Label: "Final", File: "final.png"}}}}}
	b, _ := json.Marshal(report)
	if err := os.WriteFile(filepath.Join(src, "r.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	imported, err := suites.Import(cwd, filepath.Join(src, "r.json"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := suites.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := retraceconfig.Discover(cwd)
	if err != nil {
		t.Fatal(err)
	}
	embedded := New(Deps{Cfg: &config.Config{Dir: cwd}})
	standalone := serve.New(serve.Deps{Cwd: cwd, Cfg: cfg})
	for _, path := range []string{
		"/suites/taxi/builds/" + items[0].Builds[0].ID + "/gallery",
		"/suites/taxi/builds/nope/gallery",
		"/suites/taxi/attempts/ios-1/screens/" + imported.Results[0].Screens[0].SHA256,
		"/suites/taxi/attempts/ios-1/screens/" + strings.Repeat("0", 64),
	} {
		t.Run(path, func(t *testing.T) {
			want, got := httptest.NewRecorder(), httptest.NewRecorder()
			standalone.ServeHTTP(want, httptest.NewRequest("GET", "http://localhost/api"+path, nil))
			embedded.ServeHTTP(got, httptest.NewRequest("GET", "http://localhost/api/retrace"+path, nil))
			if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
				t.Fatalf("embedded %d %q differs from standalone %d %q", got.Code, got.Body, want.Code, want.Body)
			}
		})
	}
	ok := httptest.NewRecorder()
	embedded.ServeHTTP(ok, httptest.NewRequest("GET", "http://localhost/api/retrace/suites/taxi/attempts/ios-1/screens/"+imported.Results[0].Screens[0].SHA256, nil))
	if ok.Code != 200 || ok.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("embedded screen not served: %d %q", ok.Code, ok.Header().Get("Content-Type"))
	}
}
