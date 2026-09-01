package serve

import (
	"bytes"
	"encoding/json"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff/pixel"
	"github.com/caribou-crew/ensemble/retrace/refs"
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

// pixelAt decodes a served shot and returns one pixel. "200 image/png with
// a non-empty body" is true of all four comparison panes at once, so it
// cannot tell them apart; the contract this surface actually has is WHICH
// image, and only a decode can assert it.
func pixelAt(t *testing.T, r response, side string, x, y int) [4]uint32 {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(r.body))
	if err != nil {
		t.Fatalf("side %q did not serve a decodable PNG: %v", side, err)
	}
	cr, cg, cb, ca := img.At(x, y).RGBA()
	return [4]uint32{cr, cg, cb, ca}
}

// mustOK also pins the Content-Type on every JSON answer it is used for —
// the shot handler's image/png is asserted and writeJSON's header was not,
// so dropping it left every JSON response to be sniffed. Checking it here
// covers each verb that answers 200 rather than a list someone maintains.
func mustOK(t *testing.T, r response, what string) map[string]any {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200\n%s", what, r.status, r.body)
	}
	if !strings.HasPrefix(r.ctype, "application/json") {
		t.Fatalf("%s: content-type = %q, want application/json — an unlabelled JSON body is sniffed", what, r.ctype)
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
	if !strings.HasPrefix(r.ctype, "application/json") {
		t.Fatalf("GET /api/queue: content-type = %q, want application/json", r.ctype)
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
// summaryFromREST reads one flow's detail document over HTTP and returns
// the MARSHALLED summary — the JSON Task 15's UI actually parses, not a
// struct round-trip that would hide a field the handler never wrote.
func summaryFromREST(t *testing.T, ts *httptest.Server, app, flow string) map[string]any {
	t.Helper()
	doc := mustOK(t, get(t, ts, "/api/queue/"+app+"/"+flow), "GET /api/queue/"+app+"/"+flow)
	sum, ok := doc["summary"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/queue/%s/%s has no summary object: %v", app, flow, doc)
	}
	return sum
}

// The item route is the DETAIL PANE, and it carries strictly more than a
// queue row does: the whole diff.Summary — the per-checkpoint list and each
// checkpoint's Images.Diff / Images.Overlay, which are the fields the UI
// reads to know WHICH shot URLs to build.
//
// F3 pinned exactly this property on the queue row
// (TestEveryFieldTheWorstRowCarriesIsPopulated) and the sibling route was
// never mutated in the same breath — mutation-set symmetry verbatim from
// global-constraints.md, a fix applied to one member of a set while its
// siblings go untested. Until this test, verdictOf was the ONLY reader of
// this body and it decodes one field, so both of these left the package
// green:
//
//	writeJSON(w, 200, map[string]any{"summary": diff.Summary{Verdict: sum.Verdict}})
//	sum.Counts = diff.Counts{}; sum.Gates = nil   // before writing
//
// Under either the pane says "failed" and lists nothing: no counts, no
// reason, no checkpoints, no image paths. That is the blank-comparison-pane
// failure handleShot's own comment guards against, one route over, on the
// surface whose entire job is to make a human look — and it is worse here,
// because handleShot's 404 at least SAYS the image is absent while an empty
// detail document renders as "nothing to see".
func TestTheItemRouteServesEveryFieldTheDetailPaneMustRead(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)

	// Two flows, because the required set is not the same on both and a
	// single-flow fixture cannot tell the two apart: "failed" must carry a
	// gates reason, "changed" must carry the four image paths, and each
	// would be vacuous on the other.
	for _, tc := range []struct {
		app, flow string
		verdict   string
		want      []string
	}{
		// gates is the failing flow's own field: a red row with no reason
		// is the defect R-M and brokenItem both exist to prevent.
		{"web", "cart", "failed", []string{"schema", "app", "flow", "a", "b", "verdict", "checkpoints", "counts", "capture", "gates"}},
		{"web", "search", "changed", []string{"schema", "app", "flow", "a", "b", "verdict", "checkpoints", "counts", "capture"}},
	} {
		t.Run(tc.app+"/"+tc.flow, func(t *testing.T) {
			sum := summaryFromREST(t, ts, tc.app, tc.flow)
			if sum["verdict"] != tc.verdict {
				t.Fatalf("verdict = %v, want %q", sum["verdict"], tc.verdict)
			}
			for _, key := range tc.want {
				val, ok := sum[key]
				if !ok {
					t.Fatalf("the detail document for %s/%s has no %q — these json tags are the REST contract Task 15's UI reads: %v", tc.app, tc.flow, key, sum)
				}
				// informative is the queue row's own walk (queue_test.go):
				// a key present with the type's zero value reads as
				// "measured, and fine", which on a non-passing flow is
				// affirmatively reassuring and wrong.
				if !informative(val) {
					t.Fatalf("the %s detail document's %q is %v — the pane says %q and reports that there is nothing to look at", tc.verdict, key, val, tc.verdict)
				}
			}
		})
	}

	// The image paths, checkpoint by checkpoint. These are not decoration:
	// they are how the UI knows a diff pane exists at all, and a summary
	// that served the verdict without them would send a reviewer to a
	// detail view with no pictures in it.
	changed := summaryFromREST(t, ts, "web", "search")
	cps, ok := changed["checkpoints"].([]any)
	if !ok || len(cps) == 0 {
		t.Fatalf("the changed flow's checkpoint list is %v — the detail pane has nothing to render", changed["checkpoints"])
	}
	cp := cps[0].(map[string]any)
	if cp["name"] != "results" || cp["verdict"] != "changed" {
		t.Fatalf("checkpoint = %v/%v, want results/changed", cp["name"], cp["verdict"])
	}
	if n, _ := cp["diffPct"].(float64); n <= 0 {
		t.Fatalf("the changed checkpoint reports diffPct = %v — every pixel of this fixture's shot differs", cp["diffPct"])
	}
	images, _ := cp["images"].(map[string]any)
	for _, side := range shotSides {
		if p, _ := images[side].(string); p == "" {
			t.Fatalf("the changed checkpoint carries no images.%s: %v — the UI builds /api/shots/web/search/%s/results from this field, and without it the pane is blank", side, images, side)
		}
	}

	// The mirror, and it is what stops "always emit four paths" from
	// satisfying the loop above: an UNCHANGED checkpoint has no generated
	// sides, exactly as GET /api/shots/.../diff answers 404 for it. Claiming
	// a diff image that does not exist is the same lie pointing the other
	// way.
	passing := summaryFromREST(t, ts, "web", "login")
	pcp := passing["checkpoints"].([]any)[0].(map[string]any)
	pimg, _ := pcp["images"].(map[string]any)
	for _, side := range []string{"diff", "overlay"} {
		if p, _ := pimg[side].(string); p != "" {
			t.Fatalf("an unchanged checkpoint claims images.%s = %q — nothing was generated for it and the shot route answers 404", side, p)
		}
	}
	for _, side := range []string{"a", "b"} {
		if p, _ := pimg[side].(string); p == "" {
			t.Fatalf("an unchanged checkpoint carries no images.%s — it still has two screenshots to look at", side)
		}
	}
}

// The route is a faithful pass-through of diff.Summary, asserted
// mechanically rather than against an inventory someone maintains: a field
// added to Summary after this test was written is covered the day it
// appears, which is the half the explicit list above cannot do.
func TestTheItemRouteDropsNothingSummaryForProduced(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)

	for _, flow := range []string{"cart", "search", "login"} {
		want, err := SummaryFor(deps(t, cwd), "web", flow)
		if err != nil {
			t.Fatalf("SummaryFor(web/%s): %v", flow, err)
		}
		wb, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshalling the summary for web/%s: %v", flow, err)
		}
		var wantDoc map[string]any
		if err := json.Unmarshal(wb, &wantDoc); err != nil {
			t.Fatalf("the summary for web/%s is not an object: %v", flow, err)
		}
		got := summaryFromREST(t, ts, "web", flow)
		// Key by key, so the failure names the field that was dropped
		// instead of printing two whole documents at whoever reads it.
		for _, key := range sortedKeys(wantDoc) {
			w, g := mustJSON(t, wantDoc[key]), mustJSON(t, got[key])
			if w != g {
				t.Fatalf("GET /api/queue/web/%s serves a %q the engine did not produce — the detail pane is reading a document diff.Build did not write.\nserved: %s\nwant:   %s", flow, key, g, w)
			}
		}
		for _, key := range sortedKeys(got) {
			if _, ok := wantDoc[key]; !ok {
				t.Fatalf("GET /api/queue/web/%s serves a %q that is not part of diff.Summary at all: %v", flow, key, got[key])
			}
		}
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling %v: %v", v, err)
	}
	return string(b)
}

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
	// files and bytes carry VALUES, not merely a key. The reject verb's
	// repro.files was pinned to its contents and accept's was not — the
	// same field name on the same kind of response, killed on one arm of
	// the pair and alive on the other, which is the mutation-set symmetry
	// global-constraints.md names. `"files": nil` and `"bytes": 0` both
	// left the suite green, so accept could report that it had promoted an
	// empty, zero-byte reference bundle and nothing would say otherwise.
	//
	// This is what an agent reads to know the promotion worked, and it is
	// the same claim `retrace ref accept` prints at the CLI; the bundle
	// budget (refs.MaxBundleBytes) is denominated in exactly this number.
	dir := bundle["dir"].(string)
	files, ok := bundle["files"].([]any)
	if !ok {
		t.Fatalf("bundle.files is %v, not a list of the files promoted", bundle["files"])
	}
	listed := map[string]bool{}
	var total int64
	for _, f := range files {
		rel, _ := f.(string)
		if rel == "" {
			t.Fatalf("bundle.files carries an unnamed entry: %v", files)
		}
		listed[rel] = true
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("bundle.files lists %s but it is not on disk: %v", rel, err)
		}
		total += st.Size()
	}
	for _, want := range []string{"manifest.json", "wire.jsonl", "shots/results.png"} {
		if !listed[want] {
			t.Fatalf("the promoted bundle does not list %s: %v — a reference every later diff is judged against must say what is in it", want, files)
		}
	}
	// bytes is the SUM of what was promoted, not merely non-zero: a
	// hard-coded constant, or a count of files, would satisfy "> 0".
	if got, _ := bundle["bytes"].(float64); int64(got) != total {
		t.Fatalf("bundle.bytes = %v, want %d — the total size of the files this accept actually wrote", bundle["bytes"], total)
	}
	if total == 0 {
		t.Fatalf("the promoted bundle measures zero bytes on disk: %v", files)
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
//
// THREE calls carry that same noisy field, and the two extra ones are the
// point. A rule the caller narrowed to `GET /cart` must silence the first
// and neither of the others; with only one call in the fixture, a
// project-wide rule and a scoped one produce the identical verdict, so the
// fixture cannot tell the correct rule from the incorrect one (rule
// symmetry) and dropping req.Method/req.Path at the handler is invisible.
// POST /cart differs from the rule in METHOD alone and GET /checkout in
// PATH alone, so each dimension is pinned by its own surviving call rather
// than by the pair together.
func ruleProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	hops := func(stamp string) []trace.Hop {
		body := `{"cart":{"updatedAt":"` + stamp + `","items":1}}`
		return []trace.Hop{
			hop(1, "GET", "/cart", 200, body),
			hop(2, "POST", "/cart", 200, body),
			hop(3, "GET", "/checkout", 200, body),
		}
	}
	recordRun(t, cwd, "web", "checkout", runA, map[string][]byte{"cart": shotPNG(t, white)},
		hops("2026-08-21T10:00:00Z"))
	acceptRef(t, cwd, "web", "checkout", runA)
	recordRun(t, cwd, "web", "checkout", runB, map[string][]byte{"cart": shotPNG(t, white)},
		hops("2026-08-21T11:30:00Z"))
	return cwd
}

// itemFor reads one flow's queue row over REST.
func itemFor(t *testing.T, ts *httptest.Server, app, flow string) Item {
	t.Helper()
	for _, it := range queueFromREST(t, ts) {
		if it.App == app && it.Flow == flow {
			return it
		}
	}
	t.Fatalf("%s/%s is not in the queue", app, flow)
	return Item{}
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
	echoed, ok := doc["rule"].(map[string]any)
	if !ok {
		t.Fatalf("POST rule did not echo the rule it wrote: %v", doc)
	}
	if echoed["method"] != "GET" || echoed["path"] != "/cart" {
		t.Fatalf("the echoed rule dropped the dimensions the caller narrowed it with: %v", echoed)
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
	// The two dimensions the rule dialect HAS, and the two this endpoint's
	// own refusal tells the caller to narrow with ("narrow it with `path`
	// and `method` instead — those are the only dimensions the rule dialect
	// has"). rules.MatchPathGlob("", x) is true and an empty Rule.Method
	// matches every method, so dropping either one here widens a rule the
	// caller scoped to one call into one that silences that field on every
	// call in the project and in both bodies — while still answering
	// {"ok":true} and still writing to a committed file. This is R-N's own
	// defect on R-N's own endpoint.
	if raw[0].Method != "GET" || raw[0].Path != "/cart" {
		t.Fatalf("the written rule is method=%q path=%q, want GET /cart — the endpoint accepted the two dimensions it promises are the only ones it has, and wrote a rule that ignores them", raw[0].Method, raw[0].Path)
	}

	// And the very next read re-evaluates WITH it — a server that kept its
	// startup config would tell the reviewer the rule had no effect.
	//
	// "changed", not "pass": the SCOPED rule excuses GET /cart and must
	// leave POST /cart and GET /checkout alone. Two calls still differ, so
	// this assertion is what a rule widened to the whole project fails —
	// it would report a clean flow, which is the whole failure mode.
	if _, v := verdictOf(t, ts, "web", "checkout"); v != "changed" {
		t.Fatalf("after the scoped rule: verdict = %q, want \"changed\" — GET /cart is excused, POST /cart and GET /checkout are not", v)
	}
	if got := itemFor(t, ts, "web", "checkout").Counts.WireChanged; got != 2 {
		t.Fatalf("wireChanged = %d after a rule scoped to GET /cart, want 2 — the rule reached calls it does not name", got)
	}

	// The over-refusal mirror, and the scenario's payoff: the SAME field
	// with no method/path is a project-wide rule, which does excuse all
	// three, and the queue re-evaluates to a collapsed row.
	mustOK(t, post(t, ts, "/api/queue/web/checkout/rule",
		`{"field":"cart.updatedAt","matcher":"iso8601"}`), "POST an unscoped rule")
	if _, v := verdictOf(t, ts, "web", "checkout"); v != "pass" {
		t.Fatalf("after the project-wide rule: verdict = %q, want \"pass\" — the tolerated timestamp is no longer a difference anywhere", v)
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

// acceptRequest.Run decides WHICH run becomes the reference every later
// diff is judged against, and the response reports bundle.runId — which,
// if the field were accepted and ignored, would faithfully report the wrong
// run it had just promoted. Every other accept test posts an empty body, so
// "latest" was the only selector this surface had ever exercised, and
// `retrace ref accept --run` (which IS pinned) and its REST twin would have
// been two different operations — the API-first parity constraint, and the
// same class R-N and R-F rule against, on the accept verb.
//
// Run, unlike reject's Out (R-P), is safe to honour: it is not a path, and
// runs.FindRun resolves it against the run ids already present under the
// validated root rather than joining it into one.
func TestAcceptHonoursTheRunTheCallerNamed(t *testing.T) {
	cwd := threeFlowProject(t)
	// A THIRD run, so "latest" and the run being named are unambiguously
	// different: runC is newest, runB is the one the caller pins.
	const runC = "20260821T102000Z-ccccccc"
	recordRun(t, cwd, "web", "search", runC, map[string][]byte{"results": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/search", 200, `{"hits":1}`)})
	ts := newServer(t, cwd)

	doc := mustOK(t, post(t, ts, "/api/queue/web/search/accept", `{"run":"`+runB+`"}`), "POST accept --run")
	bundle := doc["bundle"].(map[string]any)
	if bundle["runId"] != runB {
		t.Fatalf("accept promoted %v, want %q — the run the caller named, not the newest one", bundle["runId"], runB)
	}
	// And on disk, not merely in the response: the bundle every later diff
	// is judged against is the run that was asked for.
	m, err := runs.ReadManifest(filepath.Join(bundle["dir"].(string), "manifest.json"))
	if err != nil {
		t.Fatalf("reading the promoted manifest: %v", err)
	}
	if m.RunID != runB {
		t.Fatalf("the promoted bundle on disk is run %q, want %q", m.RunID, runB)
	}
}

// R-R. The reject verb's OWN half of F4: rejectRequest.Run decides which
// run is bundled as the evidence for the rejection, and it must be honoured.
//
// This pin existed once, in TestAcceptAndRejectHonourTheRunAndOutTheCaller-
// Named, and it was lost the way pins are lost with a green suite: R-P
// resolved that test's `out` half by DELETING the behaviour, correctly, and
// the test went with it — taking the reject verb's `run` arm, which R-P did
// not touch and which the product still honours. A test that pins two
// behaviours has two pins; deleting it for one obsolete behaviour silently
// drops the other, and nothing goes red because a deleted assertion cannot
// fail. Every other reject test in this package posts an empty body, so
// "latest" was the only selector this verb had ever exercised.
//
// The failure it guards is quiet and on a SUCCESS path: a reviewer rejecting
// a named older run gets a repro bundle built from the newest one — the
// wrong recording, handed over as the evidence, with runId faithfully
// reporting whatever it did bundle. The artifact outlives the session that
// made it.
//
// Run, unlike Out (R-P), is safe to honour for the same reason it is on
// accept: it is not a path, and runs.FindRun resolves it against the run ids
// already under the validated root rather than joining it into one.
func TestRejectBundlesTheRunTheCallerNamed(t *testing.T) {
	cwd := threeFlowProject(t)
	// A THIRD, NEWER run, so "latest" and the named run are unambiguously
	// different. Without it the fixture is symmetric in the dimension under
	// test: one run means every selector resolves to the same id and
	// ignoring req.Run entirely would be invisible.
	const runC = "20260821T102000Z-ccccccc"
	recordRun(t, cwd, "web", "cart", runC, map[string][]byte{"cart": shotPNG(t, blue)},
		[]trace.Hop{hop(1, "GET", "/cart", 500, `{"error":"boom"}`)})
	ts := newServer(t, cwd)

	doc := mustOK(t, post(t, ts, "/api/queue/web/cart/reject", `{"run":"`+runB+`"}`), "POST reject --run")
	repro := doc["repro"].(map[string]any)
	if repro["runId"] != runB {
		t.Fatalf("reject reported runId %v, want %q — the run the caller named, not the newest one", repro["runId"], runB)
	}
	// And on disk, not merely in the response: the echo and the artifact can
	// disagree, and the artifact is the half that outlives the request.
	dir := repro["dir"].(string)
	m, err := runs.ReadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading the repro manifest: %v", err)
	}
	if m.RunID != runB {
		t.Fatalf("the repro bundle on disk is run %q, want %q — the evidence for this rejection is the wrong recording", m.RunID, runB)
	}
	// The summary inside it was computed for the SAME run. summaryFor takes
	// the selector separately, so a bundle can carry the named run's files
	// under a diff of a different one — which would be a repro bundle whose
	// own explanation does not describe it.
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatalf("reading the repro summary.json: %v", err)
	}
	var sum map[string]any
	if err := json.Unmarshal(b, &sum); err != nil {
		t.Fatalf("the repro summary.json is not JSON: %v", err)
	}
	if bside, _ := sum["b"].(map[string]any); bside["runId"] != runB {
		t.Fatalf("the repro summary.json diffed run %v, want %q — the bundle's own explanation describes a different run than the bundle", bside["runId"], runB)
	}

	// The mirror, and it is what stops "always take the oldest" or any other
	// fixed choice from satisfying the assertions above: an empty body still
	// selects latest, which is runC and not runB.
	def := mustOK(t, post(t, ts, "/api/queue/web/cart/reject", ""), "POST reject with no run")
	drepro := def["repro"].(map[string]any)
	if drepro["runId"] != runC {
		t.Fatalf("reject with no run reported runId %v, want the newest run %q", drepro["runId"], runC)
	}
	dm, err := runs.ReadManifest(filepath.Join(drepro["dir"].(string), "manifest.json"))
	if err != nil {
		t.Fatalf("reading the default repro manifest: %v", err)
	}
	if dm.RunID != runC {
		t.Fatalf("the default repro bundle on disk is run %q, want the newest run %q", dm.RunID, runC)
	}
}

// R-P. POST .../reject REFUSES a body carrying "out", and refuses it before
// anything touches the filesystem.
//
// refs.Reject joins OutDir into a path, os.RemoveAll's the result and then
// writes into it. App, Flow and RunID go through runs.ValidateComponents on
// the way there; OutDir does not, and it needs no traversal to escape,
// because filepath.Join honours an absolute path. Honouring a
// request-supplied "out" would make this verb an arbitrary-directory
// delete-and-write on a control plane that is unauthenticated and that
// `--allow-host` can bind beyond loopback.
//
// The pin is on the SIDE EFFECT, not on the status code: the directory the
// request named is created here first, with a file in it, so a refusal that
// arrived after refs.Reject's RemoveAll would be caught even though the
// response body looked identical.
func TestPostRejectRefusesAnOutDirectoryAndTouchesNothingBeforeItDoes(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)

	// The directory refs.Reject would have removed and recreated, standing
	// where a caller pointed it: outside the project, with a file inside.
	outside := t.TempDir()
	victim := filepath.Join(outside, "web__cart__"+runB)
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatalf("creating the victim directory: %v", err)
	}
	canary := filepath.Join(victim, "important.txt")
	if err := os.WriteFile(canary, []byte("DO NOT DELETE"), 0o600); err != nil {
		t.Fatalf("writing the canary: %v", err)
	}

	r := post(t, ts, "/api/queue/web/cart/reject", `{"out":`+strconv.Quote(outside)+`}`)
	if r.status != http.StatusBadRequest {
		t.Fatalf("a reject body carrying \"out\": status = %d, want 400\n%s", r.status, r.body)
	}
	msg, _ := r.json(t)["error"].(string)
	if !strings.Contains(msg, `"out"`) || !strings.Contains(msg, ".retrace/repro") {
		t.Fatalf("the refusal does not name the field or say where the bundle actually goes: %q", msg)
	}

	// NOTHING WAS REMOVED. This is the assertion the ruling is about: a
	// refusal that ran after the RemoveAll would answer 400 too.
	b, err := os.ReadFile(canary)
	if err != nil || string(b) != "DO NOT DELETE" {
		t.Fatalf("the directory the request named was deleted or rewritten before the refusal: %v %q", err, b)
	}
	// And nothing was created — not the bundle at the named path, and not
	// the bundle at the server's own default either. The refusal precedes
	// every write on this path, including the diff images summaryFor
	// generates on its way past.
	for _, p := range []string{
		filepath.Join(outside, "web__cart__"+runB, "manifest.json"),
		filepath.Join(cwd, ".retrace", "repro"),
		filepath.Join(cwd, ".retrace", "diffs"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s exists — the refusal happened after the filesystem work, not before it", p)
		}
	}

	// The over-refusal mirror: the same verb WITHOUT "out" still succeeds,
	// and still writes — where the server chose, under the project it was
	// started in. A handler that refused every reject body would satisfy
	// everything above.
	doc := mustOK(t, post(t, ts, "/api/queue/web/cart/reject", ""), "POST reject with no out")
	dir, _ := doc["repro"].(map[string]any)["dir"].(string)
	want := filepath.Join(cwd, ".retrace", "repro")
	rel, err := filepath.Rel(want, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("reject wrote the repro bundle to %q, which is not under the server's own %q", dir, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("the repro bundle is not on disk: %v", err)
	}
	// An empty "out" is not a caller asking for anything, so it is not
	// refused: the refusal is on the value, not on the key's presence in a
	// serialiser that always emits it.
	mustOK(t, post(t, ts, "/api/queue/web/cart/reject", `{"out":""}`), "POST reject with an empty out")
}

// A flow with a committed reference bundle and no local run directory is
// the fresh-clone case R-O describes: .retrace/runs/ is gitignored, so a
// clone legitimately has references and nothing recorded. It must answer
// 409 "record a run first", never 404 "no such flow" — a typo'd flow name
// and a flow the repository demonstrably contains are different answers,
// and every other fixture flow has a run directory, so the bundle branch of
// flowKnown is otherwise never the deciding one.
func TestAFlowKnownOnlyByItsCommittedBundleIs409NotAMissingFlow(t *testing.T) {
	cwd := threeFlowProject(t)
	// The clone: the reference bundle is committed, the runs are not.
	if err := os.RemoveAll(filepath.Join(runs.RunsRoot(cwd), "web", "search")); err != nil {
		t.Fatalf("removing the gitignored runs: %v", err)
	}
	ts := newServer(t, cwd)

	r := get(t, ts, "/api/queue/web/search")
	if r.status != http.StatusConflict {
		t.Fatalf("a flow with a bundle and no runs: status = %d, want 409\n%s", r.status, r.body)
	}
	if msg, _ := r.json(t)["error"].(string); !strings.Contains(msg, "no run matches") {
		t.Fatalf("the 409 does not say a run needs recording: %q", msg)
	}
	// The mirror: a flow that really does not exist is still a 404, so this
	// is not a 409 handed to everything.
	if r := get(t, ts, "/api/queue/web/nosuch"); r.status != http.StatusNotFound {
		t.Fatalf("a flow with neither runs nor a bundle: status = %d, want 404\n%s", r.status, r.body)
	}
}

// A repro bundle whose diff could not be computed carries no summary.json,
// and the response SAYS so.
//
// refs.Reject omits the file for a nil Summary — deliberately, because a
// summary.json asserting a comparison that never ran is worse than no file
// at all. That leaves the caller holding a bundle that is quietly missing
// the one document explaining why the run was rejected, which is why
// handleReject's own comment says the warning exists "rather than leaving
// the caller to notice the missing file". Nothing constructed the flow that
// produces it, so replacing the whole clause with `_ = warning` left the
// package green: an explanation offered by the code and silently dropped on
// the wire.
//
// The flow here has runs and no accepted reference, which is the ordinary
// way to reach it — summaryFor refuses to diff a run against itself, so
// there is nothing to compare, while the bundle itself is still worth
// having.
func TestARejectThatCouldNotDiffSaysWhyItsBundleHasNoSummary(t *testing.T) {
	cwd := threeFlowProject(t)
	// Recorded once, never accepted: a reference does not exist and the
	// only candidate is the run under review.
	recordRun(t, cwd, "web", "profile", runB, map[string][]byte{"profile": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/profile", 200, `{"ok":true}`)})
	ts := newServer(t, cwd)

	doc := mustOK(t, post(t, ts, "/api/queue/web/profile/reject", ""), "POST reject on an undiffable flow")
	warn, _ := doc["warning"].(string)
	if warn == "" {
		t.Fatalf("the reject reported no warning: %v — the bundle it just wrote has no summary.json and nothing in the response says so", doc)
	}
	if !strings.Contains(warn, "summary.json") {
		t.Fatalf("the warning does not name the missing file: %q", warn)
	}
	// It names the REASON too, not just the absence: "there is no
	// summary.json" and "there is no summary.json because this flow has no
	// accepted reference" are different messages to a human, and the second
	// is the one that says what to do next.
	if !strings.Contains(warn, "run `retrace ref accept") {
		t.Fatalf("the warning says the file is missing but not why, so the reviewer cannot act on it: %q", warn)
	}
	// And the warning is true: the bundle really has no summary.json, so
	// this is not a sentence emitted unconditionally.
	repro := doc["repro"].(map[string]any)
	dir := repro["dir"].(string)
	for _, f := range repro["files"].([]any) {
		if f.(string) == "summary.json" {
			t.Fatalf("the response warns about a missing summary.json that the bundle actually contains: %v", repro["files"])
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); !os.IsNotExist(err) {
		t.Fatalf("summary.json is on disk (%v) while the response warns it is absent", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("the bundle was not written at all: %v — a repro bundle is worth having even when the diff that explains it cannot be computed", err)
	}

	// The mirror, and it is the half that stops "always warn" from passing:
	// a reject that COULD diff carries no warning at all, because there is
	// nothing missing from its bundle.
	ok := mustOK(t, post(t, ts, "/api/queue/web/cart/reject", ""), "POST reject on a diffable flow")
	if w, present := ok["warning"]; present {
		t.Fatalf("a reject whose bundle has a summary.json still warned: %v — a warning that is always there says nothing", w)
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
	r := strings.NewReplacer("{app}", "web", "{flow}", "search", "{runId}", "20260821T101000Z-bbbbbbb", "{side}", "a", "{name}", "results", "{path...}", "index.html")
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

	px := map[string][4]uint32{}
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
		px[side] = pixelAt(t, r, side, 20, 20)
	}

	// WHICH image, not merely "an image". This fixture has always carried
	// the asymmetry that decides it — web/search's A side is solid white
	// and its B side solid blue — and nothing had ever asserted it, which
	// is worse than no asymmetry at all: it makes this test look
	// discriminating to every future reader while discriminating nothing.
	// Swapping the "a" and "b" arms of shotDirFor, or returning the diff
	// directory for "overlay", leaves every assertion above green and
	// answers a valid 200 image/png for all four sides.
	//
	// This is the quietest failure on this surface: a reviewer sees the
	// blue shot labelled "reference" and the white one labelled "latest",
	// accepts a regression, and the wrong answer is promoted into the
	// committed bundle every later diff is judged against.
	if want := ([4]uint32{0xffff, 0xffff, 0xffff, 0xffff}); px["a"] != want {
		t.Fatalf("side a is rgba%v, want the REFERENCE's solid white %v — the a and b panes are swapped", px["a"], want)
	}
	if want := ([4]uint32{0, 0, 0xffff, 0xffff}); px["b"] != want {
		t.Fatalf("side b is rgba%v, want the LATEST run's solid blue %v — the a and b panes are swapped", px["b"], want)
	}
	// diff paints every changed pixel pure red (pixel.Match); overlay is a
	// copy of side B with magenta washed over it, so it still carries B's
	// blue. Serving one for the other is a valid 200 PNG showing the
	// reviewer the wrong picture.
	if want := ([4]uint32{0xffff, 0, 0, 0xffff}); px["diff"] != want {
		t.Fatalf("side diff is rgba%v, want pure red %v — every pixel of this checkpoint changed", px["diff"], want)
	}
	if px["overlay"] == px["diff"] || px["overlay"][2] == 0 {
		t.Fatalf("side overlay is rgba%v — the overlay is side B under a magenta wash and must be neither the diff image nor a repeat of another pane", px["overlay"])
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

// TestQuarantinedRunsCapturedShotsStillServe pins the a/b half of
// writeShotImage's quarantine fix: a run whose capture quarantineCheck
// refuses to compare (the proxy died mid-recording, here) still recorded its
// screenshots on both sides, and a reviewer must be able to look at them
// even though sum.Checkpoints (the comparison) is empty for a quarantined
// verdict. The generated sides (diff/overlay), which only ever exist when a
// comparison actually ran, must keep 404ing.
func TestQuarantinedRunsCapturedShotsStillServe(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "onboarding", runA, map[string][]byte{"step1": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/onboarding", 200, `{"step":1}`)})
	acceptRef(t, cwd, "web", "onboarding", runA)
	p := recordRun(t, cwd, "web", "onboarding", runB, map[string][]byte{"step1": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/onboarding", 200, `{"step":1}`)})
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}
	m.Capture = runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the proxy died 12s in"}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("rewriting the manifest: %v", err)
	}
	ts := newServer(t, cwd)

	// Confirm the fixture actually reaches a quarantined verdict, so a
	// regression that made the comparison run again (and this test pass for
	// the wrong reason) would be caught here first.
	doc := mustOK(t, get(t, ts, "/api/queue/web/onboarding"), "GET item")
	summary := doc["summary"].(map[string]any)
	if summary["verdict"] != "quarantined" {
		t.Fatalf("fixture verdict = %v, want quarantined", summary["verdict"])
	}

	// Both sides captured "step1" even though the comparison itself never
	// ran — a broken CAPTURE does not mean an absent SCREENSHOT.
	for _, side := range []string{"a", "b"} {
		r := get(t, ts, "/api/shots/web/onboarding/"+side+"/step1")
		if r.status != http.StatusOK {
			t.Fatalf("side %q of a quarantined run's captured checkpoint: status = %d\n%s", side, r.status, r.body)
		}
		if !strings.HasPrefix(r.ctype, "image/png") {
			t.Fatalf("side %q: content-type = %q, want image/png", side, r.ctype)
		}
	}

	// The generated sides never ran a comparison at all, so they stay 404s.
	for _, side := range []string{"diff", "overlay"} {
		if r := get(t, ts, "/api/shots/web/onboarding/"+side+"/step1"); r.status != http.StatusNotFound {
			t.Fatalf("side %q of a quarantined run: status = %d, want 404\n%s", side, r.status, r.body)
		}
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

// R-O's empty state. An empty review screen has two causes and they are
// different worlds: "no runs have been recorded yet" is a setup step nobody
// has done, and "everything was reviewed and nothing needs attention" is
// reassurance. Rendered identically, the first is read as the second, and a
// reviewer concludes a project is clean on the strength of never having
// recorded anything.
//
// All three arms are pinned, including the one that says nothing: "" is the
// zero value and must be what a queue with work in it reports, so the
// reassuring answer is the one that has to be earned.
func TestAnEmptyReviewQueueSaysWhichOfItsTwoCausesItIs(t *testing.T) {
	t.Run("nothing has ever been recorded", func(t *testing.T) {
		doc := mustOK(t, get(t, newServer(t, t.TempDir()), "/api/queue"), "GET /api/queue")
		if len(doc["items"].([]any)) != 0 {
			t.Fatalf("items = %v, want empty", doc["items"])
		}
		if doc["empty"] != EmptyNoRuns {
			t.Fatalf("empty = %v, want %q — an empty list on a project with no runs must not read as \"nothing needs attention\"", doc["empty"], EmptyNoRuns)
		}
	})

	t.Run("every recorded flow was compared and passed", func(t *testing.T) {
		cwd := t.TempDir()
		for _, flow := range []string{"login", "search"} {
			recordRun(t, cwd, "web", flow, runA, map[string][]byte{"cp": shotPNG(t, white)},
				[]trace.Hop{hop(1, "GET", "/"+flow, 200, `{"ok":true}`)})
			acceptRef(t, cwd, "web", flow, runA)
			recordRun(t, cwd, "web", flow, runB, map[string][]byte{"cp": shotPNG(t, white)},
				[]trace.Hop{hop(1, "GET", "/"+flow, 200, `{"ok":true}`)})
		}
		doc := mustOK(t, get(t, newServer(t, cwd), "/api/queue"), "GET /api/queue")
		// The rows exist; every one of them collapses, so the reviewer's
		// screen is as empty as the arm above — which is exactly why the
		// document has to say which world this is.
		if len(doc["items"].([]any)) != 2 {
			t.Fatalf("items = %v, want the two passing rows", doc["items"])
		}
		if doc["empty"] != EmptyAllClear {
			t.Fatalf("empty = %v, want %q", doc["empty"], EmptyAllClear)
		}
	})

	t.Run("something needs attention", func(t *testing.T) {
		doc := mustOK(t, get(t, newServer(t, threeFlowProject(t)), "/api/queue"), "GET /api/queue")
		if doc["empty"] != "" {
			t.Fatalf("empty = %v, want \"\" — a queue with a failing flow in it must never report either empty state", doc["empty"])
		}
	})
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

// The PROJECT-WIDE mask arm of the same verb. The flow arm above refuses;
// this one cannot (a top-level entry matching nothing in web/search may be
// doing its job in another flow, and checkpoints are discovered from run
// manifests rather than declared), so refs.Accept REPORTS it in
// AcceptResult.UnmatchedMasks and this handler deliberately puts it on the
// wire — "an agent accepting through REST must be able to see ... that a
// project-wide mask entry matched nothing, without parsing prose".
//
// Nothing asserted bundle.unmatchedMasks at all, so passing nil for
// ProjectMaskedCheckpoints was a one-line edit with the package green: the
// CLI would print a warning about a typo'd entry while the agent using REST
// read "clean" and committed a bundle with unredacted pixels. This is
// mutation-set symmetry verbatim — the flow arm was mutated and died, the
// project arm was not mutated in the same breath and survived.
func TestPostAcceptReportsAProjectWideMaskEntryThatMatchedNothing(t *testing.T) {
	cwd := threeFlowProject(t)
	yaml := "app: web\n" +
		"masks:\n" +
		"  reslts:\n" + // a typo: web/search's checkpoint is "results"
		"    - {x: 0, y: 0, width: 10, height: 10, why: \"the clock\"}\n" +
		"  results:\n" + // and one that does match, so this is not "report everything"
		"    - {x: 0, y: 0, width: 10, height: 10, why: \"the clock\"}\n"
	if err := os.WriteFile(filepath.Join(cwd, "retrace.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("writing retrace.yaml: %v", err)
	}
	ts := newServer(t, cwd)

	doc := mustOK(t, post(t, ts, "/api/queue/web/search/accept", ""), "POST accept")
	reported, ok := doc["bundle"].(map[string]any)["unmatchedMasks"].([]any)
	if !ok {
		t.Fatalf("the accept response carries no unmatchedMasks value: %v", doc["bundle"])
	}
	var got []string
	for _, v := range reported {
		got = append(got, v.(string))
	}
	// Exactly the typo, and not the entry that matched: a report naming
	// every declared entry is a report naming a config that is fine, and a
	// caller would learn to ignore it.
	if strings.Join(got, ",") != "reslts" {
		t.Fatalf("bundle.unmatchedMasks = %v, want exactly [reslts] — the typo is reported and the entry that matched is not", got)
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

// TestPostRuleCarriesTheWhyIntoTheOverlay pins the UI half of the `why`
// ratchet. The overlay is the file a reviewer reads in a pull request, and a
// rule written from a dashboard button is the least reviewable tolerance in
// the product — nobody was present when it was authored. Dropping the field
// here would still answer {"ok":true} and still write a committed file,
// which is the failure shape this endpoint's own scope tests exist for.
func TestPostRuleCarriesTheWhyIntoTheOverlay(t *testing.T) {
	cwd := ruleProject(t)
	ts := newServer(t, cwd)

	mustOK(t, post(t, ts, "/api/queue/web/checkout/rule",
		`{"field":"cart.updatedAt","matcher":"iso8601","why":"stamped by the cart service on every read"}`), "POST rule")

	b, err := os.ReadFile(filepath.Join(cwd, config.OverlayPath))
	if err != nil {
		t.Fatal(err)
	}
	var raw []rules.Raw
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("the overlay is not a rules.Raw list: %v\n%s", err, b)
	}
	if len(raw) != 1 || raw[0].Why != "stamped by the cart service on every read" {
		t.Fatalf("the overlay dropped the why: %+v", raw)
	}
}

// TestRunScopedItemRouteComparesTheNamedRunNotLatest is the HTTP surface
// for a run-detail view: a reviewer picking an older CI run out of the
// candidate list must see THAT run's comparison at GET
// .../runs/{runId}, exactly as SummaryForRun already guarantees at the Go
// level — this pins the route wiring on top of it.
func TestRunScopedItemRouteComparesTheNamedRunNotLatest(t *testing.T) {
	cwd := threeFlowProject(t)
	const runC = "20260821T102000Z-ccccccc"
	recordRun(t, cwd, "web", "search", runC, map[string][]byte{"results": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/search", 200, `{"hits":1}`)})
	ts := newServer(t, cwd)

	latest := mustOK(t, get(t, ts, "/api/queue/web/search"), "GET item")["summary"].(map[string]any)
	if b := latest["b"].(map[string]any); b["runId"] != runC {
		t.Fatalf("GET /api/queue/web/search's b.runId = %v, want latest %q", b["runId"], runC)
	}

	pinned := mustOK(t, get(t, ts, "/api/queue/web/search/runs/"+runB), "GET run-scoped item")["summary"].(map[string]any)
	if b := pinned["b"].(map[string]any); b["runId"] != runB {
		t.Fatalf("GET /api/queue/web/search/runs/%s's b.runId = %v, want %q", runB, b["runId"], runB)
	}

	// Every SummaryFor/SummaryForRun error maps to 409 (statusForSummaryErr)
	// — the same status the plain "latest" item route answers when it
	// cannot diff, e.g. a flow with no accepted reference yet.
	if r := get(t, ts, "/api/queue/web/search/runs/nosuchrun"); r.status != http.StatusConflict {
		t.Fatalf("an unknown run id: status = %d, want 409\n%s", r.status, r.body)
	}
}

// TestRunScopedShotRouteServesTheNamedRunsOwnDiffImage pins the run-scoped
// shots route's isolation: two runs of the same flow that generate
// DIFFERENT diff images for the SAME checkpoint name must each serve their
// own picture, not whichever one was diffed most recently into the shared
// "latest" cache.
func TestRunScopedShotRouteServesTheNamedRunsOwnDiffImage(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "login", runA, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, cwd, "web", "login", runA)
	// runB changed; runC (latest) did not.
	const runC = "20260821T102000Z-ccccccc"
	recordRun(t, cwd, "web", "login", runB, map[string][]byte{"login": shotPNG(t, blue)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	recordRun(t, cwd, "web", "login", runC, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	ts := newServer(t, cwd)

	// Warm the "latest" cache first, the way a reviewer opening the queue
	// before drilling into an older run would.
	mustOK(t, get(t, ts, "/api/queue/web/login"), "GET item")

	r := get(t, ts, "/api/shots/web/login/runs/"+runB+"/diff/login")
	if r.status != http.StatusOK {
		t.Fatalf("run-scoped diff shot for the changed run: status = %d\n%s", r.status, r.body)
	}
	if px := pixelAt(t, r, "diff", 20, 20); px != ([4]uint32{0xffff, 0, 0, 0xffff}) {
		t.Fatalf("run-scoped diff pixel = %v, want pure red — runB's own checkpoint changed on every pixel", px)
	}

	// "latest" (runC) never changed, so it has no diff image at all — the
	// run-scoped route for THAT run must say so, not fall back to runB's.
	if r := get(t, ts, "/api/shots/web/login/runs/"+runC+"/diff/login"); r.status != http.StatusNotFound {
		t.Fatalf("run-scoped diff shot for the unchanged latest run: status = %d, want 404\n%s", r.status, r.body)
	}
}

// TestAcceptUsesCfgForWhenSet pins that promoting a reference through the
// REST verb masks with the SAME per-app config the diff routes now use
// (Task 6) — not the dashboard-wide default. Without the app-specific
// config's mask, the promoted shot would carry the unredacted region.
func TestAcceptUsesCfgForWhenSet(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "home", runA,
		map[string][]byte{"home": patchedPNG(t, color.RGBA{A: 255}, color.RGBA{R: 255}, 10)}, nil)

	appDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "retrace.yaml"),
		[]byte("masks:\n  home:\n    - { x: 0, y: 0, width: 10, height: 10, why: \"test\" }\n"), 0o644); err != nil {
		t.Fatalf("writing app retrace.yaml: %v", err)
	}
	appCfg, err := config.Discover(appDir)
	if err != nil {
		t.Fatalf("config.Discover(appDir): %v", err)
	}

	d := deps(t, cwd)
	d.CfgFor = func(app string) (*config.Config, error) { return appCfg, nil }
	ts := httptest.NewServer(New(d))
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/queue/web/home/accept", "application/json", nil)
	if err != nil {
		t.Fatalf("POST accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	bundleDir, err := refs.BundleDir(cwd, "web", "home")
	if err != nil {
		t.Fatalf("refs.BundleDir: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(bundleDir, "shots", "home.png"))
	if err != nil {
		t.Fatalf("reading promoted shot: %v", err)
	}
	img, err := pixel.Decode(b)
	if err != nil {
		t.Fatalf("decoding promoted shot: %v", err)
	}
	if got := img.RGBAAt(5, 5); got != (color.RGBA{A: 255}) {
		t.Fatalf("(5,5) = %+v, want masked black — accept should have used the CfgFor app config's mask", got)
	}
}

// TestPostAcceptSurfacesTheSecretScanAndAllowsForcedAccept is the REST face
// of refs' accept-time secret scan: the refusal carries the findings as
// VALUES (field path, kind, suggestion) plus a `forcible` marker, so the
// review UI can render exactly what the CLI's stderr says and offer the same
// --force — and a forced accept answers with the findings it pushed past.
func TestPostAcceptSurfacesTheSecretScanAndAllowsForcedAccept(t *testing.T) {
	cwd := threeFlowProject(t)
	recordRun(t, cwd, "web", "search", "20260821T103000Z-ddddddd",
		map[string][]byte{"results": shotPNG(t, blue)},
		[]trace.Hop{hop(1, "GET", "/search", 200, `{"session_key":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig-part"}`)})
	ts := newServer(t, cwd)

	r := post(t, ts, "/api/queue/web/search/accept", "")
	if r.status != http.StatusConflict {
		t.Fatalf("accept of a bundle with a likely credential = %d, want 409:\n%s", r.status, r.body)
	}
	doc := r.json(t)
	if doc["forcible"] != true {
		t.Fatalf("the secret-scan refusal must be marked forcible: %v", doc)
	}
	findings, _ := doc["secretFindings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("secretFindings = %v, want exactly the one finding", doc["secretFindings"])
	}
	f := findings[0].(map[string]any)
	if f["path"] != "resp.body.session_key" || f["kind"] != "jwt" {
		t.Fatalf("finding = %v, want resp.body.session_key/jwt", f)
	}

	forced := mustOK(t, post(t, ts, "/api/queue/web/search/accept", `{"force":true}`), "POST accept --force past the scan")
	bundle := forced["bundle"].(map[string]any)
	if got, _ := bundle["secretFindings"].([]any); len(got) != 1 {
		t.Fatalf("the forced promotion must report what it pushed past: %v", bundle)
	}
	m, err := runs.ReadManifest(filepath.Join(bundle["dir"].(string), "manifest.json"))
	if err != nil {
		t.Fatalf("reading the bundle manifest: %v", err)
	}
	if !m.AcceptedWithSecrets {
		t.Fatal("the promoted bundle's manifest must record acceptedWithSecrets: true")
	}
}
