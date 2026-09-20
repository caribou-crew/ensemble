package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

func TestRetraceSuitesRoute(t *testing.T) {
	cwd := t.TempDir()
	h := New(Deps{Cfg: &config.Config{Dir: cwd}})
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/retrace/suites", nil))
		return w
	}
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest("POST", "http://localhost/api/retrace/suites", nil))
	if post.Code != 405 {
		t.Fatalf("suite POST accepted: %d %s", post.Code, post.Body)
	}
	w := request()
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"suites":[]}` {
		t.Fatalf("empty: %d %s", w.Code, w.Body)
	}
	if err := os.WriteFile(filepath.Join(cwd, "retrace.suites.json"), []byte(`{"schema":"wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w = request()
	if w.Code != 500 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("configured root not read or invalid inventory hidden: %d %s", w.Code, w.Body)
	}
}
