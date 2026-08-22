package ui_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/serve/ui"
)

// TestUIServesIndexAndSPAFallback pins ui.Handler's contract, which is the
// same contract ensemble/server/ui carries and deliberately not a second
// answer to it: real files embedded under dist/ are served as themselves,
// and any other path is assumed to be a client-side route, so it falls back
// to index.html rather than 404ing on a hard refresh of a deep link — the
// review UI puts ?app=&flow= in the URL, so a deep link IS the normal way a
// reviewer arrives.
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
}

// TestAssetsMissIs404 is the exception that keeps the fallback honest: a
// missing bundled asset must 404, not silently degrade into the app shell.
// A <script src="/assets/index-abc.js"> answered with HTML is a blank page
// and a syntax error in the console, which is a far worse report than a 404.
func TestAssetsMissIs404(t *testing.T) {
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/assets/missing.js")
	if err != nil {
		t.Fatalf("GET /assets/missing.js: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /assets/missing.js status = %d, want 404 (real asset paths must not fall back)", resp.StatusCode)
	}
}

// TestTheServedShellIsEitherARealBundleOrSaysItIsNotBuilt.
//
// dist/index.html is one file in two states: the committed placeholder, and
// the real Vite bundle `pnpm -r build` overwrites it with. A Go-only build
// embeds the placeholder, so `retrace serve` answers 200 with whatever that
// file says — and it used to say `<div id="root"></div>` and nothing else.
// A blank white page under a healthy API reads as "nothing to review" or
// "the tool is broken", which is the plausible-value hazard exactly: an
// empty page is a worse report than an honest refusal.
//
// "UI not built" is not a phrasing preference. README.md documents the
// recovery keyed on that literal string ("If you ever see \"UI not built\"
// in the dashboard…"), so a user who cannot find it in the page cannot find
// the fix either — and ensemble's placeholder, one binary over, has said it
// all along.
//
// Both states are ASSERTED, neither is skipped: a guard that silently
// removes the assertion when the bundle happens to be built is this phase's
// signature defect, and CI builds the bundle.
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

	// The real bundle is the only thing that references the hashed asset
	// tree; the placeholder cannot, because those files do not exist.
	if strings.Contains(page, "/assets/") {
		if !strings.Contains(page, "<script") {
			t.Fatalf("the built bundle references /assets/ but loads no script — this shell renders nothing:\n%s", page)
		}
		return
	}
	if !strings.Contains(page, "UI not built") {
		t.Fatalf("a Go-only build serves a shell that never says the UI is missing, so README's documented recovery cannot be found by the user who needs it:\n%s", page)
	}
	if !strings.Contains(page, "pnpm -r build") {
		t.Fatalf("the placeholder names no recovery command:\n%s", page)
	}
}
