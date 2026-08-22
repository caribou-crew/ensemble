package ui_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/server/ui"
)

// TestUIServesIndexAndSPAFallback pins ui.Handler's contract: real files
// embedded under dist/ are served as themselves, and any other path is
// assumed to be a client-side route, so it falls back to index.html rather
// than 404ing on a hard refresh of a deep link. A path under /assets/ that
// doesn't correspond to a real embedded file is the one exception — a
// missing bundled asset must 404, not silently degrade into the shell.
func TestUIServesIndexAndSPAFallback(t *testing.T) {
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	get := func(t *testing.T, path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body for %s: %v", path, err)
		}
		return resp, string(body)
	}

	rootResp, rootBody := get(t, "/")
	if rootResp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rootResp.StatusCode)
	}
	if !strings.Contains(rootBody, "<html") {
		t.Fatalf("GET / body doesn't look like HTML: %q", rootBody)
	}

	deepResp, deepBody := get(t, "/some/deep/link")
	if deepResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /some/deep/link status = %d, want 200 (SPA fallback)", deepResp.StatusCode)
	}
	if deepBody != rootBody {
		t.Fatalf("GET /some/deep/link body != GET / body (fallback should serve the same index.html)")
	}

	missingResp, _ := get(t, "/assets/missing.js")
	if missingResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /assets/missing.js status = %d, want 404 (real asset paths must not fall back)", missingResp.StatusCode)
	}
}

// TestTheServedShellIsEitherARealBundleOrSaysItIsNotBuilt is the mirror of
// retrace/serve/ui's test of the same name, and it is here because the
// review that found retrace's placeholder blank also found that nothing
// would have gone red if THIS one had drifted the same way.
//
// This placeholder is the one README.md's recovery instruction is written
// against ("If you ever see \"UI not built\" in the dashboard…"), so its
// text is a documented contract, not a message.
//
// Both states are asserted, neither is skipped: `pnpm -r build` overwrites
// this file with the real bundle, and CI builds it.
func TestTheServedShellIsEitherARealBundleOrSaysItIsNotBuilt(t *testing.T) {
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)

	if strings.Contains(page, "/assets/") {
		if !strings.Contains(page, "<script") {
			t.Fatalf("the built bundle references /assets/ but loads no script — this shell renders nothing:\n%s", page)
		}
		return
	}
	if !strings.Contains(page, "UI not built") {
		t.Fatalf("a Go-only build serves a shell that never says the UI is missing, and README's recovery is keyed on that exact string:\n%s", page)
	}
	if !strings.Contains(page, "pnpm -r build") {
		t.Fatalf("the placeholder names no recovery command:\n%s", page)
	}
}
