package serve

// export_test.go covers `retrace export`'s Go half: the static tree a CI job
// leaves behind. The standing question for every assertion here is sharper
// than it is for the live UI, because nobody can re-query an artifact:
// WHAT DOES THIS REPORT SAY WHEN IT IS WRONG, and does that read as
// reassurance?

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- fixtures -----------------------------------------------------------

// exportTo runs a full export of cwd into a fresh directory and returns the
// result plus that directory. It drives the exported entry point, not an
// internal helper: every property below is a property of what a CI job
// actually leaves on disk.
func exportTo(t *testing.T, cwd string, app, flow string) (ExportResult, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "report")
	res, err := Export(ExportOptions{Deps: deps(t, cwd), OutDir: out, App: app, Flow: flow})
	if err != nil {
		t.Fatalf("Export(%s/%s): %v", app, flow, err)
	}
	return res, out
}

func readText(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Join(parts...), err)
	}
	return string(b)
}

// rowKeys is the order the overview lists its flows in, read out of the
// rendered HTML rather than out of the slice that produced it — the order a
// reader SEES is the one under test.
var rowRe = regexp.MustCompile(`(?s)<section class="row[^"]*"[^>]*data-flow="([^"]+)"[^>]*>(.*?)</section>`)

func rowKeys(html string) []string {
	var out []string
	for _, m := range rowRe.FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

// rowFor returns one flow's <li> from the overview, so an assertion can say
// what that ROW does and does not carry instead of searching the whole page.
func rowFor(t *testing.T, html, key string) string {
	t.Helper()
	for _, m := range rowRe.FindAllStringSubmatch(html, -1) {
		if m[1] == key {
			return m[0]
		}
	}
	t.Fatalf("no row for %q in the overview:\n%s", key, html)
	return ""
}

// refRe pulls every src=/href= out of a document. The self-contained
// property is about the page's REFERENCES, not about a substring appearing
// anywhere in it: a recorded request path really can be "/api/cart", and a
// raw substring scan would both false-fail on that and pass a page whose
// only reference is an absolute one it spelled differently.
var refRe = regexp.MustCompile(`(?:src|href)="([^"]*)"`)

func refsIn(html string) []string {
	var out []string
	for _, m := range refRe.FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

// htmlFilesIn is every .html file the export wrote, so a property that must
// hold for the WHOLE artifact is enumerated mechanically rather than from
// an inventory of the pages the author remembered.
func htmlFilesIn(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".html") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// degradeCapture rewrites one recorded run's capture-trust verdict through
// the manifest writer production uses, so the resulting run is one
// `retrace run` could genuinely have left behind.
func degradeCapture(t *testing.T, cwd, app, flow, runID string, trust runs.CaptureTrust) {
	t.Helper()
	p, err := runs.PathsFor(runs.RunsRoot(cwd), app, flow, runID)
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	m.Capture = trust
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
}

// changedFlow is one app/flow whose newest run differs from its accepted
// reference on every pixel of one checkpoint — the ordinary "something to
// review" shape, and the one that produces all four shot sides.
func changedFlow(t *testing.T, cwd, app, flow, checkpoint string) {
	t.Helper()
	recordRun(t, cwd, app, flow, runA, map[string][]byte{checkpoint: shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/"+flow, 200, `{"hits":1}`)})
	acceptRef(t, cwd, app, flow, runA)
	recordRun(t, cwd, app, flow, runB, map[string][]byte{checkpoint: shotPNG(t, blue)},
		[]trace.Hop{hop(1, "GET", "/"+flow, 200, `{"hits":1}`)})
}

// --- the artifact itself ------------------------------------------------

// A report that silently needs a running server is not an artifact. Every
// reference in every page it wrote must be RELATIVE and must resolve inside
// the export, and nothing may fetch.
func TestExportWritesASelfContainedTreeThatNeedsNoServer(t *testing.T) {
	cwd := threeFlowProject(t)
	_, out := exportTo(t, cwd, "", "")

	pages := htmlFilesIn(t, out)
	if len(pages) < 2 {
		t.Fatalf("expected an overview and at least one item page, got %v", pages)
	}
	for _, page := range pages {
		html := readText(t, page)
		if strings.Contains(html, "<script") {
			t.Fatalf("%s carries a <script> tag — the overlay toggle is a <details>, and a recorded value must never reach a script context", page)
		}
		if strings.Contains(html, "fetch(") {
			t.Fatalf("%s calls fetch() — a file:// page cannot, so this report would render empty next to a reassuring layout", page)
		}
		for _, ref := range refsIn(html) {
			switch {
			case strings.Contains(ref, "://"), strings.HasPrefix(ref, "//"):
				t.Fatalf("%s references %q, an absolute URL: opened from a CI artifact that resolves against nothing", page, ref)
			case strings.HasPrefix(ref, "/"):
				t.Fatalf("%s references %q, a root-relative path: under file:// that leaves the export entirely", page, ref)
			}
			target := filepath.Join(filepath.Dir(page), filepath.FromSlash(ref))
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("%s references %q, which resolves to %s and does not exist: %v", page, ref, target, err)
			}
		}
	}
}

// Every src= in the HTML resolves to a real PNG, and the four sides land
// where summary.json's own image paths say they are. A missing file behind
// an <img> renders as a broken icon or as nothing at all, and a blank
// comparison pane reads as "identical".
func TestExportCopiesEveryReferencedShot(t *testing.T) {
	cwd := t.TempDir()
	changedFlow(t, cwd, "web", "search", "results")
	_, out := exportTo(t, cwd, "", "")

	item := readText(t, out, "web", "search", "index.html")
	var shots []string
	for _, ref := range refsIn(item) {
		if strings.HasSuffix(ref, ".png") {
			shots = append(shots, ref)
		}
	}
	// All four sides: a and b are COPIED in (a CI artifact has no access to
	// the run directories they normally resolve against) and diff/overlay
	// are the generated pair.
	for _, side := range []string{"a/shots/results.png", "b/shots/results.png", "diff/shots/results.png", "overlay/shots/results.png"} {
		if !slicesContains(shots, side) {
			t.Fatalf("the item page does not show %s — it references %v", side, shots)
		}
		b, err := os.ReadFile(filepath.Join(out, "web", "search", filepath.FromSlash(side)))
		if err != nil {
			t.Fatalf("%s was referenced but not written: %v", side, err)
		}
		if !bytes.HasPrefix(b, []byte("\x89PNG")) {
			t.Fatalf("%s is not a PNG (%d bytes)", side, len(b))
		}
	}

	// summary.json ships alongside so an agent reads the same document the
	// UI reads, and its own image paths must resolve INSIDE the export:
	// that is the whole reason the <side>/shots/<name>.png shape survives
	// the copy rather than being re-laid-out.
	var sum diff.Summary
	if err := json.Unmarshal([]byte(readText(t, out, "web", "search", "summary.json")), &sum); err != nil {
		t.Fatalf("summary.json: %v", err)
	}
	if len(sum.Checkpoints) != 1 {
		t.Fatalf("summary.json has %d checkpoints, want 1", len(sum.Checkpoints))
	}
	cp := sum.Checkpoints[0]
	for side, rel := range map[string]string{"a": cp.Images.A, "b": cp.Images.B} {
		if rel == "" {
			t.Fatalf("summary.json names no %s-side image", side)
		}
		if _, err := os.Stat(filepath.Join(out, "web", "search", side, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("summary.json's images.%s = %q does not resolve inside the export: %v", side, rel, err)
		}
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// Recorded bodies and checkpoint names are attacker-influenced data in any
// real stack: the body is whatever the service under test returned, and a
// checkpoint name is a filename an adapter wrote into shots/. Both reach
// the template, one in text context and one in an attribute.
func TestExportEscapesUntrustedStringsIntoHtml(t *testing.T) {
	const payload = `<script>alert(1)</script>`
	cwd := t.TempDir()
	// "re<port" is a checkpoint name capture.Session.Checkpoints can
	// genuinely produce: the name is the shot's filename with .png trimmed,
	// and nothing validates its charset on the way in.
	shots := map[string][]byte{"re<port": shotPNG(t, white)}
	recordRun(t, cwd, "web", "checkout", runA, shots,
		[]trace.Hop{hop(1, "GET", "/api/cart", 200, `{"note":"hello"}`)})
	acceptRef(t, cwd, "web", "checkout", runA)
	recordRun(t, cwd, "web", "checkout", runB, shots,
		[]trace.Hop{hop(1, "GET", "/api/cart", 200, `{"note":"`+payload+`"}`)})

	_, out := exportTo(t, cwd, "", "")
	item := readText(t, out, "web", "checkout", "index.html")

	if !strings.Contains(item, "&lt;script&gt;") {
		t.Fatalf("the recorded body's payload does not appear escaped in the item page:\n%s", item)
	}
	if strings.Contains(item, payload) {
		t.Fatalf("the item page carries the recorded payload VERBATIM — a report that executes what it reports on")
	}
	if strings.Contains(item, `"re<port"`) || strings.Contains(item, `>re<port<`) {
		t.Fatalf("the checkpoint name reached the page unescaped")
	}
}

// A capture verdict that is not "ok" quarantines the comparison (serve
// never passes --allow-degraded), so this row is one where NOTHING was
// compared. The report must say that, in the capture's own words, and must
// not print a counts strip that reads as twelve measured zeros.
func TestExportBannersANonOkCaptureVerdict(t *testing.T) {
	cwd := t.TempDir()
	changedFlow(t, cwd, "web", "search", "results")
	degradeCapture(t, cwd, "web", "search", runB, runs.CaptureTrust{
		Status:  trace.VerdictBroken,
		Summary: "the proxy recorded nothing for 40 seconds",
		Reasons: []runs.TrustReason{{Code: "quiet-stretch", Status: trace.VerdictBroken, Detail: "40s with no traffic"}},
	})

	res, out := exportTo(t, cwd, "", "")
	item := readText(t, out, "web", "search", "index.html")
	if !strings.Contains(item, "the proxy recorded nothing for 40 seconds") {
		t.Fatalf("the item page does not carry the capture's own summary:\n%s", item)
	}
	if !strings.Contains(item, "quiet-stretch") {
		t.Fatalf("the item page drops the machine-readable capture reason:\n%s", item)
	}
	if !strings.Contains(item, "quarantined") {
		t.Fatalf("the item page does not say the comparison was refused:\n%s", item)
	}
	// The exit code is the fourth value diff.ExitCode has, and it is the
	// HIGHEST one: a run nobody could evaluate must not exit like a run
	// that changed or passed.
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3 for a quarantined export", res.ExitCode)
	}

	// And the row for it does not print a measured-and-clean strip.
	index := readText(t, out, "index.html")
	row := rowFor(t, index, "web/search")
	if strings.Contains(row, "row__counts") {
		t.Fatalf("a quarantined row prints a counts strip — nothing was compared, so nothing may be reported as clean:\n%s", row)
	}
}

// R-Z. `<app>__<flow>` collides, because underscores are legal path
// components: web__search/x and web/search__x both join to web__search__x.
// The second export would overwrite the first's report and MERGE ITS SHOTS
// into the first's tree, with the PNG names deciding which wins — nothing
// errors, nothing logs, and the HTML renders perfectly.
//
// Both flows carry a checkpoint called "home", so a merge is silent
// corruption rather than co-location: the assertion below reads the bytes.
func TestTwoFlowsWhoseNamesCollideUnderAJoinKeepTheirOwnReports(t *testing.T) {
	cwd := t.TempDir()
	// web__search/x is white on both sides; web/search__x is blue. Same
	// checkpoint name, different pixels.
	for _, f := range []struct {
		app, flow string
		shot      []byte
	}{
		{"web__search", "x", shotPNG(t, white)},
		{"web", "search__x", shotPNG(t, blue)},
	} {
		recordRun(t, cwd, f.app, f.flow, runA, map[string][]byte{"home": f.shot},
			[]trace.Hop{hop(1, "GET", "/home", 200, `{"ok":true}`)})
		acceptRef(t, cwd, f.app, f.flow, runA)
		recordRun(t, cwd, f.app, f.flow, runB, map[string][]byte{"home": f.shot},
			[]trace.Hop{hop(1, "GET", "/home", 200, `{"ok":true}`)})
	}

	res, out := exportTo(t, cwd, "", "")
	if res.Items != 2 {
		t.Fatalf("exported %d items, want 2", res.Items)
	}
	got := map[string][]byte{}
	for _, f := range []struct{ app, flow string }{{"web__search", "x"}, {"web", "search__x"}} {
		dir := filepath.Join(out, f.app, f.flow)
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			t.Fatalf("%s/%s has no report of its own: %v", f.app, f.flow, err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "a", "shots", "home.png"))
		if err != nil {
			t.Fatalf("%s/%s has no a-side shot: %v", f.app, f.flow, err)
		}
		got[f.app+"/"+f.flow] = b
	}
	if bytes.Equal(got["web__search/x"], got["web/search__x"]) {
		t.Fatalf("the two flows' \"home\" shots are byte-identical — one flow's checkpoint is being served under the other's report")
	}
}

// The guard at the join, delegated to the ONE guard body. app and flow
// reach Export from CLI flags and are joined into OutDir.
func TestExportRefusesAnAppOrFlowThatCouldEscapeTheOutDir(t *testing.T) {
	cwd := threeFlowProject(t)
	for _, bad := range []struct{ app, flow string }{
		{"../../etc", "login"},
		{"web", "../../../tmp/pwned"},
		{"web", ".."},
	} {
		out := filepath.Join(t.TempDir(), "report")
		_, err := Export(ExportOptions{Deps: deps(t, cwd), OutDir: out, App: bad.app, Flow: bad.flow})
		if err == nil {
			t.Fatalf("Export(app=%q flow=%q) was accepted — those values are joined into the out dir", bad.app, bad.flow)
		}
		if !strings.Contains(err.Error(), "invalid path component") {
			t.Fatalf("Export(app=%q flow=%q) error = %v, want the runs guard's own refusal", bad.app, bad.flow, err)
		}
	}
}

// R-AA. An un-evaluable row and a genuinely clean row must not render the
// same thing. The clean arm is the one that already exists, and a test with
// only that arm is the value costume: `0 shots · 0 wire · 0 hops` is what a
// twelve-zero Counts prints for a flow NOBODY COMPARED.
//
// F.15 stays deferred — brokenItem's runId and counts are still vacuous on
// purpose, guarded by TestEveryFieldAnUnEvaluableRowCarriesIsPopulated's
// counter-assertion. This is the consumer refusing to render them, which
// was the stated reason for fixing `capture` at the source.
func TestAnUnEvaluableRowNeverRendersAsMeasuredAndClean(t *testing.T) {
	cwd := t.TempDir()
	// web/onboarding: one run, no reference — it cannot be compared at all.
	recordRun(t, cwd, "web", "onboarding", runA, map[string][]byte{"step1": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/onboarding", 200, `{"step":1}`)})
	// web/login: compared, and genuinely clean.
	shots := map[string][]byte{"login": shotPNG(t, white)}
	recordRun(t, cwd, "web", "login", runA, shots, []trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, cwd, "web", "login", runA)
	recordRun(t, cwd, "web", "login", runB, shots, []trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})

	_, out := exportTo(t, cwd, "", "")
	index := readText(t, out, "index.html")
	broken := rowFor(t, index, "web/onboarding")
	clean := rowFor(t, index, "web/login")

	if broken == clean {
		t.Fatalf("the un-evaluable row and the clean row render identically:\n%s", broken)
	}
	if strings.Contains(broken, "row__counts") {
		t.Fatalf("the un-evaluable row prints a counts strip — twelve zeros on a flow where nothing was counted:\n%s", broken)
	}
	if strings.Contains(broken, "row__runs") {
		t.Fatalf("the un-evaluable row prints a run id it does not have:\n%s", broken)
	}
	if !strings.Contains(broken, "could not be evaluated") {
		t.Fatalf("the un-evaluable row does not say it was never compared:\n%s", broken)
	}
	// The clean arm, which is what keeps the arm above from being satisfied
	// by rendering every row as un-evaluable.
	if !strings.Contains(clean, "row__runs") || !strings.Contains(clean, runB) {
		t.Fatalf("the clean row does not say which run it is:\n%s", clean)
	}
	if !strings.Contains(clean, "row__counts") {
		t.Fatalf("the clean row does not report what it compared:\n%s", clean)
	}
	if strings.Contains(clean, "could not be evaluated") {
		t.Fatalf("a flow that WAS compared is described as un-evaluable:\n%s", clean)
	}

	// The same distinction on the item page: no summary.json is written for
	// a comparison that never ran, exactly as refs.Reject omits one.
	if _, err := os.Stat(filepath.Join(out, "web", "onboarding", "summary.json")); err == nil {
		t.Fatalf("an un-evaluable flow shipped a summary.json — a document asserting a comparison that never happened")
	}
	if _, err := os.Stat(filepath.Join(out, "web", "login", "summary.json")); err != nil {
		t.Fatalf("a compared flow shipped no summary.json: %v", err)
	}
}

// R-AB. "Worst first" is serve.ScoreOf's order, through BuildQueue — not a
// second weighting invented here, in the artifact a human reads when they
// have no other source. The shared fixture's expected order is neither the
// listing order nor its reverse nor alphabetical.
func TestExportOrdersTheOverviewByTheQueuesOwnScore(t *testing.T) {
	cwd := threeFlowProject(t)
	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	_, out := exportTo(t, cwd, "", "")
	index := readText(t, out, "index.html")

	want := queueOrder(items)
	got := rowKeys(index)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("overview order = %v, want the queue's own worst-first order %v", got, want)
	}
	if strings.Join(got, ",") != "web/cart,web/search,admin/login,web/login" {
		t.Fatalf("overview order = %v — the fixture's worst-first order is web/cart,web/search,admin/login,web/login", got)
	}
	// The score itself travels, so the ordering key is legible to an agent
	// reading the artifact rather than implied by the row order.
	for _, m := range rowRe.FindAllStringSubmatch(index, -1) {
		var it *Item
		for i := range items {
			if items[i].App+"/"+items[i].Flow == m[1] {
				it = &items[i]
			}
		}
		if it == nil {
			t.Fatalf("the overview lists %q, which the queue does not", m[1])
		}
		if !strings.Contains(m[0], `data-score="`+formatScore(it.Score)+`"`) {
			t.Fatalf("row %s does not carry ScoreOf's value %v:\n%s", m[1], it.Score, m[0])
		}
	}
}

// The two empty worlds, on the surface with the least context available to
// correct a wrong reading. EmptyReasonFor is the ONE place that decides
// which one it is; re-deriving "all clear" from an empty list would make
// the reassuring answer the one nobody has to earn.
func TestExportOfAProjectWithNoRunsSaysSoRatherThanAllClear(t *testing.T) {
	cwd := t.TempDir()
	res, out := exportTo(t, cwd, "", "")
	if res.Items != 0 {
		t.Fatalf("exported %d items from an empty project", res.Items)
	}
	index := readText(t, out, "index.html")
	if strings.Contains(strings.ToLower(index), "all clear") {
		t.Fatalf("a project with no runs exports an all-clear report:\n%s", index)
	}
	if !strings.Contains(index, "No runs have been recorded") {
		t.Fatalf("the overview does not say nothing has been recorded:\n%s", index)
	}

	// The contrast arm: a project that WAS compared and is clean says so,
	// so "nothing recorded" is not simply the only sentence this page knows.
	clean := t.TempDir()
	shots := map[string][]byte{"login": shotPNG(t, white)}
	recordRun(t, clean, "web", "login", runA, shots, []trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, clean, "web", "login", runA)
	recordRun(t, clean, "web", "login", runB, shots, []trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	_, cleanOut := exportTo(t, clean, "", "")
	cleanIndex := readText(t, cleanOut, "index.html")
	if !strings.Contains(strings.ToLower(cleanIndex), "all clear") {
		t.Fatalf("a fully-compared, clean project does not say so:\n%s", cleanIndex)
	}
}

// The filter names what it could not find. An export that quietly produced
// an empty report for a typo'd --flow would be a green CI job over nothing.
func TestExportOfAnUnknownFlowIsAnErrorNamingIt(t *testing.T) {
	cwd := threeFlowProject(t)
	out := filepath.Join(t.TempDir(), "report")
	_, err := Export(ExportOptions{Deps: deps(t, cwd), OutDir: out, App: "web", Flow: "chekcout"})
	if err == nil {
		t.Fatalf("Export of a flow that does not exist succeeded")
	}
	if !strings.Contains(err.Error(), "chekcout") {
		t.Fatalf("the refusal does not name the flow: %v", err)
	}
	// And it wrote nothing: a half-written report directory left behind by
	// a refused export is an artifact somebody will open.
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("a refused export left %d entries in %s", len(entries), out)
	}
}

// The filter is honoured, and the result reports what it actually wrote.
func TestExportOfOneFlowWritesOnlyThatFlow(t *testing.T) {
	cwd := threeFlowProject(t)
	res, out := exportTo(t, cwd, "web", "search")
	if res.Items != 1 {
		t.Fatalf("items = %d, want 1", res.Items)
	}
	if _, err := os.Stat(filepath.Join(out, "web", "search", "index.html")); err != nil {
		t.Fatalf("the requested flow was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "web", "cart")); err == nil {
		t.Fatalf("--flow search also exported web/cart")
	}
	if got := rowKeys(readText(t, out, "index.html")); strings.Join(got, ",") != "web/search" {
		t.Fatalf("the overview lists %v, want only web/search", got)
	}
	// Files is the manifest of what a CI job is about to upload; it must
	// name every file that is there and nothing that is not.
	for _, rel := range res.Files {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("ExportResult.Files names %q, which was not written: %v", rel, err)
		}
	}
	if !slicesContains(res.Files, "index.html") || !slicesContains(res.Files, "web/search/summary.json") {
		t.Fatalf("ExportResult.Files does not name the overview and the summary: %v", res.Files)
	}
}

// summary.json is "the exact diff.Summary the UI consumed", so an agent
// reading the artifact and an agent reading GET /api/queue/{app}/{flow}
// reach the same conclusions. A re-rendered or trimmed copy would be a
// second document with the same name.
func TestExportShipsTheSameSummaryTheReviewApiServes(t *testing.T) {
	cwd := t.TempDir()
	changedFlow(t, cwd, "web", "search", "results")
	_, out := exportTo(t, cwd, "", "")

	want, err := SummaryFor(deps(t, cwd), "web", "search")
	if err != nil {
		t.Fatalf("SummaryFor: %v", err)
	}
	var got diff.Summary
	if err := json.Unmarshal([]byte(readText(t, out, "web", "search", "summary.json")), &got); err != nil {
		t.Fatalf("summary.json: %v", err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatalf("summary.json is not the document the review API serves:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
