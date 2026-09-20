package serve

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuitesRoute(t *testing.T) {
	cwd := t.TempDir()
	appRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(appRoot, "retrace.suites.json"), []byte(`{"schema":"wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := NewSources(map[string]Deps{appRoot: deps(t, appRoot)}, map[string]string{"web": appRoot})
	if err != nil {
		t.Fatal(err)
	}
	h := NewWithSources(deps(t, cwd), &sources)
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest("POST", "http://localhost/api/suites", nil))
	if post.Code != 405 {
		t.Fatalf("suite POST accepted: %d %s", post.Code, post.Body)
	}
	getSuites := func() response {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/suites", nil))
		return response{status: w.Code, body: w.Body.Bytes(), ctype: w.Header().Get("Content-Type")}
	}
	r := getSuites()
	if r.status != 200 || strings.TrimSpace(string(r.body)) != `{"suites":[]}` {
		t.Fatalf("missing inventory: %d %s", r.status, r.body)
	}
	inventory := `{"schema":"retrace/suites/1","suites":[{"id":"taxi","title":"Taxi","version":"v1","platforms":["web","ios","android"],"features":[{"id":"login","title":"Login","flows":[{"id":"sign-in","title":"Sign in","requiredPlanes":["functional"]}]}]}]}`
	if err := os.WriteFile(filepath.Join(cwd, "retrace.suites.json"), []byte(inventory), 0600); err != nil {
		t.Fatal(err)
	}
	r = getSuites()
	if r.status != 200 {
		t.Fatalf("configured: %d %s", r.status, r.body)
	}
	items := r.json(t)["suites"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "taxi" || len(items[0].(map[string]any)["builds"].([]any)) != 0 {
		t.Fatalf("configured inventory lost: %s", r.body)
	}
	reports := filepath.Join(cwd, ".retrace", "suites", "taxi")
	if err := os.MkdirAll(reports, 0700); err != nil {
		t.Fatal(err)
	}
	report := `{"schema":"retrace/suite-attempt/1","suiteId":"taxi","suiteVersion":"v1","attemptId":"web-1","platform":"web","git":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","branch":"main","dirty":false},"baselineId":"legacy","policyId":"p1","startedAt":"2026-09-20T00:00:00Z","finishedAt":"2026-09-20T00:01:00Z","results":[{"flowId":"sign-in","planes":{"functional":"pass","wire":"not-applicable","visual":"not-applicable"}}]}`
	if err := os.WriteFile(filepath.Join(reports, "web-1.json"), []byte(report), 0600); err != nil {
		t.Fatal(err)
	}
	r = getSuites()
	if r.status != 200 {
		t.Fatalf("valid report: %d %s", r.status, r.body)
	}
	items = r.json(t)["suites"].([]any)
	builds := items[0].(map[string]any)["builds"].([]any)
	if len(builds) != 1 {
		t.Fatalf("report not aggregated: %s", r.body)
	}
	counts := builds[0].(map[string]any)["counts"].(map[string]any)
	if counts["total"] != float64(3) || counts["passed"] != float64(1) || counts["notRun"] != float64(2) {
		t.Fatalf("missing platform coverage hidden: %s", r.body)
	}
	badReport := filepath.Join(reports, "broken.json")
	if err := os.WriteFile(badReport, []byte(`{"schema":"wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	r = getSuites()
	if r.status != 500 || r.json(t)["error"] == nil {
		t.Fatalf("invalid report hidden: %d %s", r.status, r.body)
	}
	if err := os.Remove(badReport); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "retrace.suites.json"), []byte(`{"schema":"wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	r = getSuites()
	if r.status != 500 || r.json(t)["error"] == nil {
		t.Fatalf("invalid inventory hidden: %d %s", r.status, r.body)
	}
}
