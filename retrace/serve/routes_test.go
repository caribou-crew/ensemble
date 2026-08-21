package serve

import (
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/rules"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- harness ------------------------------------------------------------

func newServer(t *testing.T, cwd string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(New(deps(t, cwd)))
	t.Cleanup(ts.Close)
	return ts
}

type response struct {
	status int
	body   []byte
	ctype  string
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("body is not a JSON object (%d): %v\n%s", r.status, err, r.body)
	}
	return out
}

// do sends one request, leaving RawPath intact so an escaped traversal
// reaches the handler exactly as a browser would send it.
func do(t *testing.T, ts *httptest.Server, method, path, body string, hdr map[string]string) response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s %s: %v", method, path, err)
	}
	return response{status: resp.StatusCode, body: b, ctype: resp.Header.Get("Content-Type")}
}

func get(t *testing.T, ts *httptest.Server, path string) response {
	t.Helper()
	return do(t, ts, http.MethodGet, path, "", nil)
}

func post(t *testing.T, ts *httptest.Server, path, body string) response {
	t.Helper()
	return do(t, ts, http.MethodPost, path, body, nil)
}

func mustOK(t *testing.T, r response, what string) map[string]any {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200\n%s", what, r.status, r.body)
	}
	return r.json(t)
}

// queueFromREST reads the queue over HTTP and returns the decoded items.
func queueFromREST(t *testing.T, ts *httptest.Server) []Item {
	t.Helper()
	r := get(t, ts, "/api/queue")
	if r.status != http.StatusOK {
		t.Fatalf("GET /api/queue: status = %d\n%s", r.status, r.body)
	}
	var doc struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(r.body, &doc); err != nil {
		t.Fatalf("GET /api/queue is not the expected document: %v\n%s", err, r.body)
	}
	return doc.Items
}

func verdictOf(t *testing.T, ts *httptest.Server, app, flow string) (int, string) {
	t.Helper()
	r := get(t, ts, "/api/queue/"+app+"/"+flow)
	if r.status != http.StatusOK {
		return r.status, ""
	}
	var doc struct {
		Summary struct {
			Verdict string `json:"verdict"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(r.body, &doc); err != nil {
		t.Fatalf("GET item is not the expected document: %v\n%s", err, r.body)
	}
	return r.status, doc.Summary.Verdict
}

// --- tests --------------------------------------------------------------

func TestGetQueueReturnsItemsWorstFirst(t *testing.T) {
	ts := newServer(t, threeFlowProject(t))
	items := queueFromREST(t, ts)
	got := queueOrder(items)
	want := []string{"web/cart", "web/search", "admin/login", "web/login"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("REST queue order = %v, want %v", got, want)
	}
	// The score is ON THE WIRE. The UI sorts and renders by it and never
	// re-derives the formula, so a row without it would push that formula
	// into TypeScript, where it would drift.
	if items[0].Score <= items[len(items)-1].Score {
		t.Fatalf("scores did not survive the JSON round trip: %v", items)
	}
	raw := get(t, ts, "/api/queue").json(t)
	first := raw["items"].([]any)[0].(map[string]any)
	for _, key := range []string{"app", "flow", "verdict", "score", "runId", "counts", "capture"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("item JSON is missing %q — the tags are the REST contract: %v", key, first)
		}
	}
}

// The spec's "LLM walks the queue" scenario, asserted through REST only:
// after POST accept, a fresh GET of the same item says "pass".
func TestPostAcceptUpdatesTheReferenceExactlyAsTheUiWould(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)

	if _, v := verdictOf(t, ts, "web", "search"); v != "changed" {
		t.Fatalf("before accept: verdict = %q, want \"changed\"", v)
	}
	doc := mustOK(t, post(t, ts, "/api/queue/web/search/accept", ""), "POST accept")
	if doc["ok"] != true {
		t.Fatalf("POST accept did not report ok: %v", doc)
	}
	bundle, ok := doc["bundle"].(map[string]any)
	if !ok {
		t.Fatalf("POST accept has no bundle: %v", doc)
	}
	for _, key := range []string{"dir", "files", "bytes"} {
		if _, ok := bundle[key]; !ok {
			t.Fatalf("the accept response is missing bundle.%s: %v", key, bundle)
		}
	}
	if _, err := os.Stat(filepath.Join(bundle["dir"].(string), "manifest.json")); err != nil {
		t.Fatalf("the promoted bundle has no manifest on disk: %v", err)
	}

	if _, v := verdictOf(t, ts, "web", "search"); v != "pass" {
		t.Fatalf("after accept: verdict = %q, want \"pass\"", v)
	}
	// And the queue agrees — the item route and the queue route must not be
	// two answers for one flow.
	for _, it := range queueFromREST(t, ts) {
		if it.App == "web" && it.Flow == "search" && (it.Verdict != "pass" || it.Score != 0) {
			t.Fatalf("the queue still shows %s/%s as %q (score %v) after the accept", it.App, it.Flow, it.Verdict, it.Score)
		}
	}
}

// ruleProject is a flow whose only difference between the reference and the
// latest run is a timestamp — the noise a wire rule exists to excuse.
func ruleProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "checkout", runA, map[string][]byte{"cart": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/cart", 200, `{"cart":{"updatedAt":"2026-08-21T10:00:00Z","items":1}}`)})
	acceptRef(t, cwd, "web", "checkout", runA)
	recordRun(t, cwd, "web", "checkout", runB, map[string][]byte{"cart": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/cart", 200, `{"cart":{"updatedAt":"2026-08-21T11:30:00Z","items":1}}`)})
	return cwd
}

// The spec's "Rule from the UI" scenario, asserted through REST only.
func TestPostRuleAppendsAWireRuleAndTheQueueReEvaluatesWithoutThatNoise(t *testing.T) {
	cwd := ruleProject(t)
	ts := newServer(t, cwd)

	if _, v := verdictOf(t, ts, "web", "checkout"); v != "changed" {
		t.Fatalf("before the rule: verdict = %q, want \"changed\" (the timestamp moved)", v)
	}

	doc := mustOK(t, post(t, ts, "/api/queue/web/checkout/rule",
		`{"field":"cart.updatedAt","matcher":"iso8601","method":"GET","path":"/cart"}`), "POST rule")
	if doc["ok"] != true {
		t.Fatalf("POST rule did not report ok: %v", doc)
	}
	if _, ok := doc["rule"].(map[string]any); !ok {
		t.Fatalf("POST rule did not echo the rule it wrote: %v", doc)
	}
	list, ok := doc["rules"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("POST rule did not return the rules now in effect: %v", doc["rules"])
	}

	// It landed in the machine-owned overlay, which is the file `retrace
	// ref rule` writes and the one that IS committed.
	overlay := filepath.Join(cwd, config.OverlayPath)
	b, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatalf("reading %s: %v", overlay, err)
	}
	var raw []rules.Raw
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("the overlay is not a rules.Raw list: %v\n%s", err, b)
	}
	if len(raw) != 1 || raw[0].Body["cart.updatedAt"] != "iso8601" {
		t.Fatalf("the overlay does not carry the rule: %+v", raw)
	}

	// And the very next read re-evaluates WITH it — a server that kept its
	// startup config would tell the reviewer the rule had no effect.
	if _, v := verdictOf(t, ts, "web", "checkout"); v != "pass" {
		t.Fatalf("after the rule: verdict = %q, want \"pass\" — the tolerated timestamp is no longer a difference", v)
	}
	items := queueFromREST(t, ts)
	if len(items) != 1 || items[0].Score != 0 {
		t.Fatalf("the queue did not re-evaluate: %+v", items)
	}
}

// A rule cannot be scoped to a flow or to one side of a call: the dialect
// has no such dimension (rules.Raw carries method/path/headers/body, and
// rules.Resolve keys on method plus normalized path alone). Accepting the
// field and ignoring it is the plausible-value trap — "scope":"resp" would
// SAIL, and the caller would believe the rule was narrower than it is.
// `retrace ref rule` refuses --scope for the same reason.
func TestPostRuleRefusesAScopeItCannotHonour(t *testing.T) {
	cwd := ruleProject(t)
	ts := newServer(t, cwd)

	for _, body := range []string{
		`{"scope":"resp","field":"cart.updatedAt","matcher":"iso8601"}`,
		`{"flow":"checkout","field":"cart.updatedAt","matcher":"iso8601"}`,
	} {
		r := post(t, ts, "/api/queue/web/checkout/rule", body)
		if r.status != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400\n%s", body, r.status, r.body)
		}
		msg, _ := r.json(t)["error"].(string)
		if !strings.Contains(msg, "EVERY flow") || !strings.Contains(msg, "BOTH") {
			t.Fatalf("the refusal does not teach what a rule actually covers: %q", msg)
		}
		if _, err := os.Stat(filepath.Join(cwd, config.OverlayPath)); err == nil {
			t.Fatalf("a refused rule was written to the overlay anyway")
		}
	}

	// The over-refusal mirror: the same rule WITHOUT the unhonourable field
	// still lands. A refusal that swallowed every rule would satisfy every
	// assertion above.
	mustOK(t, post(t, ts, "/api/queue/web/checkout/rule",
		`{"field":"cart.updatedAt","matcher":"iso8601"}`), "POST rule with no scope")

	// And a field the request struct does not have is refused too, rather
	// than being silently dropped into a default.
	if r := post(t, ts, "/api/queue/web/checkout/rule",
		`{"field":"x","matcher":"ignore","scopes":"resp"}`); r.status != http.StatusBadRequest {
		t.Fatalf("an unknown body field was accepted: status = %d\n%s", r.status, r.body)
	}
	// A rule with no field or no matcher is refused rather than appended as
	// an empty, match-everything rule.
	for _, body := range []string{`{"matcher":"ignore"}`, `{"field":"x"}`, `{}`} {
		if r := post(t, ts, "/api/queue/web/checkout/rule", body); r.status != http.StatusBadRequest {
			t.Fatalf("body %s was accepted: status = %d\n%s", body, r.status, r.body)
		}
	}
}

func TestPostRejectEmitsAReproBundle(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)

	doc := mustOK(t, post(t, ts, "/api/queue/web/cart/reject", ""), "POST reject")
	if doc["ok"] != true {
		t.Fatalf("POST reject did not report ok: %v", doc)
	}
	repro, ok := doc["repro"].(map[string]any)
	if !ok {
		t.Fatalf("POST reject has no repro: %v", doc)
	}
	dir, _ := repro["dir"].(string)
	if dir == "" {
		t.Fatalf("POST reject did not name the bundle directory: %v", repro)
	}
	files := map[string]bool{}
	for _, f := range repro["files"].([]any) {
		files[f.(string)] = true
	}
	for _, want := range []string{"manifest.json", "wire.jsonl", "summary.json", "shots/cart.png"} {
		if !files[want] {
			t.Fatalf("the repro bundle is missing %s: %v", want, repro["files"])
		}
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Fatalf("%s is listed but not on disk: %v", want, err)
		}
	}
	// The summary that motivated the rejection is the real one, not an
	// empty document asserting a comparison that never ran.
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatalf("reading summary.json: %v", err)
	}
	var sum map[string]any
	if err := json.Unmarshal(b, &sum); err != nil {
		t.Fatalf("summary.json is not JSON: %v", err)
	}
	if sum["verdict"] != "failed" {
		t.Fatalf("summary.json verdict = %v, want \"failed\"", sum["verdict"])
	}
}

// routePattern matches the registrations in routes.go. The test enumerates
// the surface MECHANICALLY from the source rather than listing patterns by
// hand: a test that lists what it checks can only ever cover the routes its
// author remembered, and this one covers a route added years from now on
// the day it appears (global-constraints.md).
var routePattern = regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`)

func registeredRoutes(t *testing.T) [][2]string {
	t.Helper()
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("reading routes.go: %v", err)
	}
	var out [][2]string
	for _, m := range routePattern.FindAllStringSubmatch(string(src), -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	if len(out) < 7 {
		t.Fatalf("found only %d routes in routes.go — the inventory regexp has stopped matching: %v", len(out), out)
	}
	// The UI fallback is the one route not registered in the mux (see New:
	// a catch-all there would swallow every unmatched API path and turn a
	// wrong-method call into a 200 carrying the app shell). It is listed
	// here explicitly so it gets the same guard and per-method treatment as
	// the rest, rather than being the one surface nobody checked.
	return append(out, [2]string{"GET", "/"})
}

// fill substitutes a real app/flow/side/name for a pattern's wildcards.
func fill(pattern string) string {
	r := strings.NewReplacer("{app}", "web", "{flow}", "search", "{side}", "a", "{name}", "results")
	return r.Replace(pattern)
}

func TestEveryRouteIsRegisteredPerMethodAndRejectsCrossOrigin(t *testing.T) {
	ts := newServer(t, threeFlowProject(t))

	for _, rt := range registeredRoutes(t) {
		method, path := rt[0], fill(rt[1])
		t.Run(method+" "+path, func(t *testing.T) {
			// Cross-site is refused on EVERY route, GET included: reading
			// captured traffic and screenshots is exactly what a rebinding
			// or CSRF attack against this plane is after.
			r := do(t, ts, method, path, "", map[string]string{"Sec-Fetch-Site": "cross-site"})
			if r.status != http.StatusForbidden {
				t.Fatalf("cross-site %s %s: status = %d, want 403", method, path, r.status)
			}
			r = do(t, ts, method, path, "", map[string]string{"Origin": "https://evil.example"})
			if r.status != http.StatusForbidden {
				t.Fatalf("cross-origin %s %s: status = %d, want 403", method, path, r.status)
			}

			// Registered PER METHOD: the wrong verb is a 405, not a 404 and
			// not a silent match. A method-less registration would also
			// PANIC against the "GET /" fallback at startup — this asserts
			// the shape that avoids it.
			other := http.MethodPost
			if method == http.MethodPost {
				other = http.MethodGet
			}
			r = do(t, ts, other, path, "", nil)
			if r.status != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s (registered for %s only): status = %d, want 405", other, path, method, r.status)
			}
		})
	}
}

// GET /api/shots/web/checkout/a/%2e%2e%2f%2e%2e%2fetc%2fpasswd — ServeMux's
// cleaning runs on the STILL-ESCAPED path, so this arrives as literal
// "../../etc/passwd". It must 400, not read the file.
func TestShotPathsCannotEscapeTheRunDirectory(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)

	// A file that a successful traversal would reach, so "it 400ed" is not
	// the only thing being asserted.
	secret := filepath.Join(cwd, "secret.png")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatalf("writing the bait file: %v", err)
	}

	// The brief's case, exactly: it arrives as literal "../../etc/passwd",
	// whose cleaned form still carries a separator, and it must be REFUSED
	// as malformed rather than answered from whatever is on disk.
	r := get(t, ts, "/api/shots/web/search/a/%2e%2e%2f%2e%2e%2fetc%2fpasswd")
	if r.status != http.StatusBadRequest {
		t.Fatalf("the escaped traversal: status = %d, want 400\n%s", r.status, r.body)
	}
	if !strings.Contains(string(r.body), "invalid checkpoint name") {
		t.Fatalf("the refusal does not name the reason: %s", r.body)
	}

	// Every other shape a traversal can take. Some collapse to a harmless
	// basename under path.Clean (which is the design: rooting at "/" before
	// Clean is what makes a leading "../" inert) and some are cleaned away
	// by ServeMux before routing — so the property asserted here is the one
	// that actually matters and holds for all of them: nothing outside the
	// run directory is ever served.
	for _, name := range []string{
		"%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"..%2f..%2f..%2f..%2fsecret",
		"%2e%2e%2f%2e%2e%2f%2e%2e%2fsecret",
		"%2e%2e%2fsecret",
		"..%5c..%5csecret",
		".",
		"..",
		"%2e%2e",
	} {
		t.Run(name, func(t *testing.T) {
			r := get(t, ts, "/api/shots/web/search/a/"+name)
			if r.status == http.StatusOK {
				t.Fatalf("status = 200 for %q: %s\n%s", name, r.ctype, r.body)
			}
			if strings.Contains(string(r.body), "TOP SECRET") || strings.HasPrefix(r.ctype, "image/") {
				t.Fatalf("the handler read a file outside the run directory: %s %q", r.ctype, r.body)
			}
		})
	}

	// The over-refusal mirror: an ordinary checkpoint name still serves its
	// PNG. A guard that rejected every name would satisfy the loop above.
	r = get(t, ts, "/api/shots/web/search/a/results")
	if r.status != http.StatusOK || !strings.HasPrefix(r.ctype, "image/png") {
		t.Fatalf("an ordinary checkpoint name: status = %d, content-type = %q\n%s", r.status, r.ctype, r.body)
	}
}

// The four sides are one contract: a and b are the captured shots, diff and
// overlay are generated by diff.Build into .retrace/diffs. Serving a
// generated side for a checkpoint that did not change is a 404 with a
// reason, NEVER an empty 200 — a blank comparison pane reads as
// "identical", which is the opposite of what it would mean.
func TestEverySideOfAChangedCheckpointServesAndAnUnchangedOneSaysSo(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)

	for _, side := range shotSides {
		r := get(t, ts, "/api/shots/web/search/"+side+"/results")
		if r.status != http.StatusOK {
			t.Fatalf("side %q of a CHANGED checkpoint: status = %d\n%s", side, r.status, r.body)
		}
		if !strings.HasPrefix(r.ctype, "image/png") {
			t.Fatalf("side %q: content-type = %q, want image/png", side, r.ctype)
		}
		if len(r.body) == 0 {
			t.Fatalf("side %q served an empty body, which renders as a blank pane", side)
		}
	}

	// web/login passes, so no diff image was written for it.
	for _, side := range []string{"diff", "overlay"} {
		r := get(t, ts, "/api/shots/web/login/"+side+"/login")
		if r.status != http.StatusNotFound {
			t.Fatalf("side %q of an UNCHANGED checkpoint: status = %d, want 404\n%s", side, r.status, r.body)
		}
		if msg, _ := r.json(t)["error"].(string); msg != "no diff image: this checkpoint did not change" {
			t.Fatalf("side %q: error = %q, want the did-not-change message", side, msg)
		}
	}
	// The captured sides of that same passing checkpoint DO serve — an
	// unchanged checkpoint still has two screenshots to look at.
	for _, side := range []string{"a", "b"} {
		if r := get(t, ts, "/api/shots/web/login/"+side+"/login"); r.status != http.StatusOK {
			t.Fatalf("side %q of an unchanged checkpoint: status = %d\n%s", side, r.status, r.body)
		}
	}
	// An unknown side is refused rather than falling through to some
	// default pane.
	if r := get(t, ts, "/api/shots/web/search/sideways/results"); r.status != http.StatusBadRequest {
		t.Fatalf("an unknown side: status = %d, want 400\n%s", r.status, r.body)
	}
	// An unknown checkpoint is a 404, not an empty image.
	if r := get(t, ts, "/api/shots/web/search/a/nosuch"); r.status != http.StatusNotFound {
		t.Fatalf("an unknown checkpoint: status = %d, want 404\n%s", r.status, r.body)
	}
}

func TestAnUnknownAppOrFlowIs404NotAPanic(t *testing.T) {
	ts := newServer(t, threeFlowProject(t))

	for _, path := range []string{
		"/api/queue/nosuch/flow",
		"/api/queue/web/nosuch",
		"/api/shots/nosuch/flow/a/x",
	} {
		if r := get(t, ts, path); r.status != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, want 404\n%s", path, r.status, r.body)
		}
	}
	for _, path := range []string{
		"/api/queue/web/nosuch/accept",
		"/api/queue/web/nosuch/reject",
		"/api/queue/web/nosuch/rule",
	} {
		if r := post(t, ts, path, ""); r.status != http.StatusNotFound {
			t.Fatalf("POST %s: status = %d, want 404\n%s", path, r.status, r.body)
		}
	}
	// A component that would escape the runs root is malformed, not
	// missing: 400, and never a directory listing from outside the root.
	// Both forms stay escaped through ServeMux's cleaning (which runs on the
	// STILL-ESCAPED path), so they reach the handler as literal ".." and are
	// refused there. A bare "/api/queue/web/.." would instead be CLEANED
	// away by the mux and never reach a handler at all, which is why the
	// encoded form is the one worth pinning.
	for _, path := range []string{"/api/queue/..%2f..%2fetc/flow", "/api/queue/web/%2e%2e", "/api/queue/%2e%2e/flow"} {
		if r := get(t, ts, path); r.status != http.StatusBadRequest {
			t.Fatalf("GET %s: status = %d, want 400\n%s", path, r.status, r.body)
		}
	}
	// A flow that EXISTS but cannot be evaluated is neither: 409, with the
	// reason. A 404 there would read as "no such flow" and a 200 as
	// "nothing differed".
	cwd := threeFlowProject(t)
	recordRun(t, cwd, "web", "onboarding", runA, map[string][]byte{"step1": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/onboarding", 200, `{"step":1}`)})
	ts2 := newServer(t, cwd)
	r := get(t, ts2, "/api/queue/web/onboarding")
	if r.status != http.StatusConflict {
		t.Fatalf("an unevaluable flow: status = %d, want 409\n%s", r.status, r.body)
	}
	if msg, _ := r.json(t)["error"].(string); !strings.Contains(msg, "ref accept") {
		t.Fatalf("the 409 does not say what to do about it: %q", msg)
	}
}

func TestHealthReportsTheVersionAndTheQueueIsAlwaysAnArray(t *testing.T) {
	cwd := t.TempDir() // a project with no runs at all
	ts := newServer(t, cwd)

	doc := mustOK(t, get(t, ts, "/api/health"), "GET /api/health")
	if doc["ok"] != true || doc["version"] != "test" {
		t.Fatalf("health = %v, want ok:true and the injected version", doc)
	}

	// An empty queue encodes as [] and never as null: a client that renders
	// null as "not loaded yet" and [] as "nothing to review" must not have
	// to guess which one it got.
	raw := mustOK(t, get(t, ts, "/api/queue"), "GET /api/queue")
	items, ok := raw["items"].([]any)
	if !ok {
		t.Fatalf("items is %T, want an array even when empty: %s", raw["items"], get(t, ts, "/api/queue").body)
	}
	if len(items) != 0 {
		t.Fatalf("items = %v, want empty", items)
	}
}

// A nil Deps.Now is the ordinary production case (nobody injects a clock),
// so it must resolve to the real one rather than panicking or reporting the
// zero time.Time — a year-1 timestamp that would read as a real reading.
func TestANilNowIsTheRealClockNotAZeroTimestamp(t *testing.T) {
	ts := newServer(t, t.TempDir())
	doc := mustOK(t, get(t, ts, "/api/health"), "GET /api/health")
	stamp, _ := doc["time"].(string)
	if strings.HasPrefix(stamp, "0001-01-01") || stamp == "" {
		t.Fatalf("health time = %q — a nil Now must resolve to the real clock", stamp)
	}
}

// The REST accept must be the SAME operation `retrace ref accept` is, down
// to the refusals. A mask entry naming a checkpoint the run does not have
// redacts nothing and looks exactly like a mask that worked, so the CLI
// refuses the promotion; if this handler forgot to pass
// MaskedCheckpoints, the identical promotion would SUCCEED through the UI
// and publish the pixels the entry was written to hide — into a bundle that
// is committed and cannot be un-published.
//
// This drives the real path end to end (a config on disk, config.Discover,
// New, one POST), because the defect being pinned is the WIRING, not the
// logic: refs.Accept's own tests already cover the logic, and they pass
// whether or not this handler fills the field.
func TestPostAcceptRefusesAMaskEntryThatMatchesNoCheckpointJustAsTheCliDoes(t *testing.T) {
	cwd := threeFlowProject(t)
	yaml := "app: web\n" +
		"flows:\n" +
		"  search:\n" +
		"    masks:\n" +
		"      reslts:\n" + // a typo for the "results" checkpoint
		"        - {x: 0, y: 0, width: 10, height: 10, why: \"the clock\"}\n"
	if err := os.WriteFile(filepath.Join(cwd, "retrace.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("writing retrace.yaml: %v", err)
	}
	ts := newServer(t, cwd)

	r := post(t, ts, "/api/queue/web/search/accept", "")
	if r.status == http.StatusOK {
		t.Fatalf("the promotion was accepted with a mask entry that matches no checkpoint:\n%s", r.body)
	}
	msg, _ := r.json(t)["error"].(string)
	if !strings.Contains(msg, "reslts") {
		t.Fatalf("the refusal does not name the entry that matched nothing: %q", msg)
	}

	// The over-refusal mirror: spelled correctly, the same promotion goes
	// through — and the mask is actually applied, so this is not a refusal
	// that was merely relaxed.
	fixed := strings.Replace(yaml, "reslts", "results", 1)
	if err := os.WriteFile(filepath.Join(cwd, "retrace.yaml"), []byte(fixed), 0o644); err != nil {
		t.Fatalf("rewriting retrace.yaml: %v", err)
	}
	ts2 := newServer(t, cwd)
	doc := mustOK(t, post(t, ts2, "/api/queue/web/search/accept", ""), "POST accept with a correct mask entry")

	// And the mask was APPLIED, not merely tolerated: the promoted shot's
	// masked corner is opaque black. Without this, dropping MasksFor from
	// the handler entirely — the severe form of the same wiring defect,
	// where NO mask applies at all — promotes every screenshot unredacted
	// with the whole suite green.
	dir := doc["bundle"].(map[string]any)["dir"].(string)
	raw, err := os.ReadFile(filepath.Join(dir, "shots", "results.png"))
	if err != nil {
		t.Fatalf("reading the promoted shot: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding the promoted shot: %v", err)
	}
	if r, g, b, a := img.At(2, 2).RGBA(); r != 0 || g != 0 || b != 0 || a != 0xffff {
		t.Fatalf("the masked corner of the promoted shot is rgba(%d,%d,%d,%d), want opaque black — the mask was not applied", r, g, b, a)
	}
	// The mirror for the mirror: a pixel OUTSIDE the mask still carries the
	// capture's own colour (the latest run's shot is solid blue), so this
	// cannot be satisfied by blacking out the whole image.
	if r, g, b, a := img.At(30, 30).RGBA(); r != 0 || g != 0 || b != 0xffff || a != 0xffff {
		t.Fatalf("a pixel outside the mask is rgba(%d,%d,%d,%d), want the captured blue — the promoted shot is not the capture", r, g, b, a)
	}
}

// force is the ONE refusal a human may override, and its zero value —
// false, an accept body that never mentions it — must be the protective
// one. A run the capture machinery could not vouch for becoming the thing
// every later diff is judged against is exactly how a proxy-down run
// becomes the source of truth, and over REST there is no operator reading a
// warning on stderr to catch it.
func TestPostAcceptWillNotPromoteAFatalCaptureUnlessTheRequestSaysForce(t *testing.T) {
	cwd := threeFlowProject(t)
	p := recordRun(t, cwd, "web", "search", "20260821T102000Z-ccccccc",
		map[string][]byte{"results": shotPNG(t, blue)},
		[]trace.Hop{hop(1, "GET", "/search", 200, `{"hits":1}`)})
	// Rewritten through the manifest reader/writer rather than hand-edited,
	// so the fixture is a manifest production could actually have written.
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}
	m.Capture = runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the proxy died 40s in"}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("rewriting the manifest: %v", err)
	}
	ts := newServer(t, cwd)

	r := post(t, ts, "/api/queue/web/search/accept", "")
	if r.status == http.StatusOK {
		t.Fatalf("a broken capture was promoted by a request that never asked to force it:\n%s", r.body)
	}
	if msg, _ := r.json(t)["error"].(string); !strings.Contains(msg, "broken") {
		t.Fatalf("the refusal does not name the capture verdict: %q", msg)
	}

	// The over-refusal mirror: an explicit force still promotes, and the
	// verdict it promoted travels back as a VALUE so the caller knows what
	// it just did.
	doc := mustOK(t, post(t, ts, "/api/queue/web/search/accept", `{"force":true}`), "POST accept --force")
	bundle := doc["bundle"].(map[string]any)
	if bundle["captureStatus"] != "broken" {
		t.Fatalf("the forced promotion did not report the capture verdict it accepted: %v", bundle)
	}
}
