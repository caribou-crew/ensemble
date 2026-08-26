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
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
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

	// Layout-INDEPENDENT, and this is the arm that bites on the collision
	// itself rather than on the directory scheme: whatever scheme is used,
	// two flows must produce TWO DISTINCT report paths and TWO DISTINCT
	// checkpoint files. Under an <app>__<flow> join both pairs are one path
	// written twice, and the second write silently replaces the first.
	distinct := func(suffix string) map[string]bool {
		out := map[string]bool{}
		for _, f := range res.Files {
			if strings.HasSuffix(f, suffix) && f != suffix {
				out[f] = true
			}
		}
		return out
	}
	if pages := distinct("index.html"); len(pages) != 2 {
		t.Fatalf("two flows produced %d distinct report pages, want 2 — one is overwriting the other: %v", len(pages), res.Files)
	}
	if pngs := distinct("a/shots/home.png"); len(pngs) != 2 {
		t.Fatalf("two flows produced %d distinct home checkpoint files, want 2 — their shots are merging into one tree: %v", len(pngs), res.Files)
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
	// And the reason came from the capture banner's own reason code, not
	// from an export that re-ran the comparison and happened to fail the
	// same way. Those are different situations — "nobody ever looked" and
	// "somebody looked and this report could not reproduce it" — and they
	// carry different sentences, so a report that reached this row the
	// second way says so instead of borrowing the first way's words.
	if strings.Contains(broken, "could not reproduce that comparison") {
		t.Fatalf("the un-evaluable row's reason came from a failed second comparison rather than from the capture's own capture-not-assessed code:\n%s", broken)
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
	cwd := scoreOrderProject(t)
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
	if strings.Join(got, ",") != "web/zzz,web/bbb,web/aaa,web/ccc,web/ddd" {
		t.Fatalf("overview order = %v — the fixture's worst-first order is web/zzz,web/bbb,web/aaa,web/ccc,web/ddd", got)
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

// scoreOrderProject is the ordering fixture, and it is built so that the
// correct rule and the plausible WRONG rules give DIFFERENT answers — the
// subtlest fixture-symmetry costume, and one the shared three-flow fixture
// walked straight into: there, sorting by verdict class produces exactly the
// order ScoreOf produces, so a second ordering keyed on the verdict would
// have sorted this report and gone unnoticed.
//
//	web/zzz  failed, TWO unexpected statuses  — two gates, the worst score
//	web/bbb  failed, ONE unexpected status    — one gate
//	web/aaa  quarantined                      — 1000 flat: no Gates at all,
//	                                            because diff.Build returns
//	                                            before any plane is computed
//	web/ccc  changed                          — one changed checkpoint
//	web/ddd  pass                             — zero
//
// Worst-first is zzz, bbb, aaa, ccc, ddd. Alphabetical order is not that.
// Neither is ANY sort keyed on the verdict class alone: whichever way such a
// sort ranks the classes, bbb and zzz tie inside "failed" and fall back to
// listing order, which puts bbb first.
func scoreOrderProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	ok200 := []trace.Hop{hop(1, "GET", "/one", 200, `{"ok":1}`), hop(2, "GET", "/two", 200, `{"ok":2}`)}

	// web/zzz — two 500s, so two gates.
	recordRun(t, cwd, "web", "zzz", runA, map[string][]byte{"s": shotPNG(t, white)}, ok200)
	acceptRef(t, cwd, "web", "zzz", runA)
	recordRun(t, cwd, "web", "zzz", runB, map[string][]byte{"s": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/one", 500, `{"e":1}`), hop(2, "GET", "/two", 500, `{"e":2}`)})

	// web/bbb — one 500.
	recordRun(t, cwd, "web", "bbb", runA, map[string][]byte{"s": shotPNG(t, white)}, ok200)
	acceptRef(t, cwd, "web", "bbb", runA)
	recordRun(t, cwd, "web", "bbb", runB, map[string][]byte{"s": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/one", 500, `{"e":1}`), hop(2, "GET", "/two", 200, `{"ok":2}`)})

	// web/aaa — quarantined: the capture verdict is not ok, so nothing is
	// compared and the Summary carries no Gates to weight.
	recordRun(t, cwd, "web", "aaa", runA, map[string][]byte{"s": shotPNG(t, white)}, ok200)
	acceptRef(t, cwd, "web", "aaa", runA)
	recordRun(t, cwd, "web", "aaa", runB, map[string][]byte{"s": shotPNG(t, white)}, ok200)
	degradeCapture(t, cwd, "web", "aaa", runB, runs.CaptureTrust{
		Status: trace.VerdictBroken, Summary: "the proxy recorded nothing for 40 seconds",
	})

	// web/ccc — changed on every pixel.
	recordRun(t, cwd, "web", "ccc", runA, map[string][]byte{"s": shotPNG(t, white)}, ok200)
	acceptRef(t, cwd, "web", "ccc", runA)
	recordRun(t, cwd, "web", "ccc", runB, map[string][]byte{"s": shotPNG(t, blue)}, ok200)

	// web/ddd — identical.
	recordRun(t, cwd, "web", "ddd", runA, map[string][]byte{"s": shotPNG(t, white)}, ok200)
	acceptRef(t, cwd, "web", "ddd", runA)
	recordRun(t, cwd, "web", "ddd", runB, map[string][]byte{"s": shotPNG(t, white)}, ok200)
	return cwd
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
	// R-Y's gap: max over an EMPTY SET is 0, so an export that compared
	// nothing would exit as a pass — on the command whose stated design is
	// to be the only step in a CI job, where the number is the build result
	// and the prose on the page is not. An export with no rows is an
	// INABILITY TO RUN, not a finding, and it carries the code the rest of
	// this CLI already uses for that.
	if res.ExitCode != 3 {
		t.Fatalf("an export that compared nothing exits %d — CI reads that as a pass over a report with nothing in it", res.ExitCode)
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

// An app whose flow listing fails is a row BuildQueue emits with no flow at
// all — brokenItem(app, "", err). It is a reachable state (an unreadable app
// directory), and it must not take the whole report down: one app that
// cannot be read is not a reason to publish nothing, and a row silently
// missing from a report is indistinguishable from a flow that passed.
//
// It also has no directory the export can safely name, so it appears on the
// overview and links to nothing — rather than linking to a page that is not
// there, which under file:// is a dead click with no explanation.
func TestExportKeepsARowForAnAppWhoseFlowsCannotBeListed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny anything")
	}
	cwd := threeFlowProject(t)
	appDir := filepath.Join(runs.RunsRoot(cwd), "web")
	if err := os.Chmod(appDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(appDir, 0o755) })

	res, out := exportTo(t, cwd, "", "")
	index := readText(t, out, "index.html")
	row := rowFor(t, index, "web/")
	if !strings.Contains(row, "could not be evaluated") {
		t.Fatalf("the unreadable app's row does not say it was never compared:\n%s", row)
	}
	if strings.Contains(row, "<a class=\"row__flow\"") {
		t.Fatalf("the unreadable app's row links to a page that was never written:\n%s", row)
	}
	if strings.Contains(row, "row__counts") || strings.Contains(row, "row__runs") {
		t.Fatalf("a row for an app nobody could read prints measurements:\n%s", row)
	}
	// The rest of the project still published.
	if _, err := os.Stat(filepath.Join(out, "admin", "login", "index.html")); err != nil {
		t.Fatalf("one unreadable app took the whole report down: %v", err)
	}
	// 3, not 2: an app nobody could read produced no comparison, which is
	// "could not evaluate" and not "a gate failed". `retrace diff` answers
	// the same fact with the same number.
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3 — an app nobody could read was not evaluated, and that is not a gate failure", res.ExitCode)
	}
}

// --- item pages ---------------------------------------------------------
//
// Everything above this line reads index.html. The lower half of an ITEM
// page — checkpoints, wire, hops, conformance, performance, gate budgets —
// is where this report says the most, and where a wrong sentence is least
// checkable: a reader of an artifact cannot re-run anything to correct it.
//
// Every assertion below is TWO-ARMED on purpose: the same section rendered
// from a comparison that evaluated the plane, and from one that did not.
// Each of these sentences is TRUE of one of the two arms, so a one-armed
// fixture pins neither reading — it passes just as happily when the page
// prints the evaluated wording over an unevaluated plane.

// itemPages exports cwd whole and returns every flow's page body keyed
// "<app>/<flow>". Two arms out of ONE export, rather than two exports that
// could differ for a second reason.
func itemPages(t *testing.T, cwd string) map[string]string {
	t.Helper()
	res, out := exportTo(t, cwd, "", "")
	pages := map[string]string{}
	for _, f := range res.Files {
		if !strings.HasSuffix(f, "/index.html") || strings.Count(f, "/") != 2 {
			continue
		}
		pages[strings.TrimSuffix(f, "/index.html")] = readText(t, out, filepath.FromSlash(f))
	}
	if len(pages) == 0 {
		t.Fatalf("the export wrote no item pages at all: %v", res.Files)
	}
	return pages
}

// pageSection is one <h2> section of an item page, from that heading to the
// next. A "the hops plane says X" assertion that searched the whole page
// would pass on an X rendered under Wire.
func pageSection(t *testing.T, page, heading string) string {
	t.Helper()
	h := "<h2>" + heading + "</h2>"
	start := strings.Index(page, h)
	if start < 0 {
		t.Fatalf("no %s section on this page:\n%s", h, page)
	}
	rest := page[start+len(h):]
	if next := strings.Index(rest, "<h2>"); next >= 0 {
		rest = rest[:next]
	}
	return rest
}

// checkpointBlock is one checkpoint's block inside the Checkpoints section.
func checkpointBlock(t *testing.T, page, name string) string {
	t.Helper()
	sec := pageSection(t, page, "Checkpoints")
	start := strings.Index(sec, "<h3>"+name+" ")
	if start < 0 {
		t.Fatalf("no block for checkpoint %q in:\n%s", name, sec)
	}
	rest := sec[start:]
	if next := strings.Index(rest[1:], "<h3>"); next >= 0 {
		rest = rest[:next+1]
	}
	return rest
}

func writeFileAt(t *testing.T, cwd, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, name), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// writeChain adds hops.jsonl — the full cross-service chain `retrace run`
// records in ensemble mode — to an already-recorded run, through the
// manifest writer production uses. The hop plane is absent on a standalone
// run and present on an ensemble one, which is what makes it the cleanest
// plane on which to build both arms out of real recordings.
func writeChain(t *testing.T, p runs.Paths, hops []trace.Hop) {
	t.Helper()
	var buf bytes.Buffer
	for _, h := range hops {
		b, err := json.Marshal(h)
		if err != nil {
			t.Fatalf("marshalling a fixture chain hop: %v", err)
		}
		buf.Write(append(b, '\n'))
	}
	if err := os.WriteFile(filepath.Join(p.RunDir, "hops.jsonl"), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing hops.jsonl: %v", err)
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	m.Mode = runs.ModeEnsemble
	m.Hops = &runs.Counts{Calls: len(hops), Recorded: true}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
}

// gatedProject is the fixture the item-page assertions share: ONE config,
// gating pixel and wire, configuring an OpenAPI spec and a per-flow perf
// budget — and three flows that give that one config three different
// amounts of evidence to work with.
//
//	web/paired  — every plane evaluated and CLEAN: a checkpoint's worth of
//	              pixels, a paired wire call, a recorded hop chain, a perf
//	              budget it meets, and a spec its call conforms to.
//	web/silent  — shots and nothing else: no wire call to pair, no hop
//	              chain, and no perf budget configured for this flow.
//	web/quiet   — shots and nothing else, but WITH a perf budget and the
//	              project's spec: the two planes that are configured here
//	              have no evidence to say anything about.
//	web/nothing — no evidence at all, so not even the pixel plane (which
//	              every project gates, by config default) is measurable.
//
// One config across all three flows is the whole point. "This plane is not
// gated" is a claim about the CONFIG; a fixture with one flow per config
// cannot tell a config claim from a run claim, and would call the false
// sentence true.
func gatedProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	writeFileAt(t, cwd, "openapi.json", `{"paths": {"/paired": {"get": {"responses": {"200": {}}}}}}`)
	writeFileAt(t, cwd, "retrace.yaml", `app: web
openapi: openapi.json
gates:
  pixel:
    budget_pct: 5
  wire:
    budget_pct: 5
flows:
  paired:
    perf_budget_ms: 5000
  quiet:
    perf_budget_ms: 5000
`)
	calls := []trace.Hop{hop(1, "GET", "/paired", 200, `{"ok":true}`)}
	for _, id := range []string{runA, runB} {
		p := recordRun(t, cwd, "web", "paired", id, map[string][]byte{"home": shotPNG(t, white)}, calls)
		writeChain(t, p, calls)
		recordRun(t, cwd, "web", "silent", id, map[string][]byte{"home": shotPNG(t, white)}, nil)
		recordRun(t, cwd, "web", "quiet", id, map[string][]byte{"home": shotPNG(t, white)}, nil)
		recordRun(t, cwd, "web", "nothing", id, nil, nil)
		if id == runA {
			for _, flow := range []string{"paired", "silent", "quiet", "nothing"} {
				acceptRef(t, cwd, "web", flow, runA)
			}
		}
	}
	return cwd
}

// F-1. budgetsOf emits NO Gate for a plane it could not measure, by the
// same code path as a plane nobody configured — so an absent row means one
// of two very different things and only the config separates them. The page
// used to state the reassuring one in prose the Summary never claimed:
// "a plane with no row here is not gated at all". A reader asking "did my
// wire gate run on this build?" was told the wire plane is ungated.
//
// This is the inverted shape of this phase's defect class: not a
// distinction dying in the transcribing layer, but the transcribing layer
// ASSERTING ONE AWAY. It ranks highest because it is an affirmative claim
// about configuration, and configuration is the one thing a reader of a CI
// artifact cannot check from the artifact.
func TestAGatedPlaneThatCouldNotBeMeasuredIsNeverReportedAsUngated(t *testing.T) {
	pages := itemPages(t, gatedProject(t))

	measured := pageSection(t, pages["web/paired"], "Gate budgets")
	for _, plane := range []string{"pixel", "wire"} {
		if !strings.Contains(measured, "<td>"+plane+"</td>") {
			t.Fatalf("test setup: the %s gate has no budget row on a flow with evidence for it:\n%s", plane, measured)
		}
	}
	if strings.Contains(measured, "NOT evaluated") {
		t.Fatalf("a flow that evaluated every gate it configures says a gate was not evaluated:\n%s", measured)
	}

	// web/silent gates wire and paired no calls, so wire is CONFIGURED and
	// UNMEASURED. The page must say that, in those terms.
	partial := pageSection(t, pages["web/silent"], "Gate budgets")
	if !strings.Contains(partial, "<td>pixel</td>") {
		t.Fatalf("test setup: pixel was measurable here and has no row:\n%s", partial)
	}
	if strings.Contains(partial, "<td>wire</td>") {
		t.Fatalf("test setup: wire paired nothing here, so diff must emit no budget row for it:\n%s", partial)
	}
	if !strings.Contains(partial, "NOT evaluated") || !strings.Contains(partial, "<code>wire</code>") {
		t.Fatalf("this project GATES wire and this run could not measure it, and the page does not say so — a reader asking whether their wire gate ran on this build reads the absent row as \"wire is not gated\":\n%s", partial)
	}

	// web/nothing measures nothing at all, so the "no plane is gated"
	// wording must not appear either: pixel is gated in every project, by
	// config default, whether or not a run gave it anything to measure.
	none := pageSection(t, pages["web/nothing"], "Gate budgets")
	if strings.Contains(none, "<td>") {
		t.Fatalf("test setup: nothing was measurable here and a budget row was emitted:\n%s", none)
	}
	if strings.Contains(none, "No plane is gated") {
		t.Fatalf("this project gates pixel and wire and this page says no plane is gated:\n%s", none)
	}
	for _, plane := range []string{"<code>pixel</code>", "<code>wire</code>"} {
		if !strings.Contains(none, plane) {
			t.Fatalf("a gate this run could not evaluate (%s) is not named on the page at all:\n%s", plane, none)
		}
	}

	// The premise the "no gated plane at all" arm rests on, pinned:
	// applyDefaults gates "pixel" in EVERY project, so at least one of the
	// two lists above is always non-empty and that arm is unreachable.
	// Mutating its wording survives — an equivalent mutant for as long as
	// this holds, and this is what makes it hold.
	cfg, err := config.Discover(t.TempDir())
	if err != nil {
		t.Fatalf("config.Discover: %v", err)
	}
	if g, ok := cfg.Gates["pixel"]; !ok || g.BudgetPct == nil {
		t.Fatalf("a project with no config gates no plane (%+v) — the Gate budgets section can now render its unreachable arm, and nothing asserts what that arm says", cfg.Gates)
	}

	// The clarifying sentence is itself load-bearing: without it, an
	// absent row is once again just an absence, and the reassuring reading
	// is the one a reader supplies.
	for _, key := range []string{"web/paired", "web/silent"} {
		sec := pageSection(t, pages[key], "Gate budgets")
		if !strings.Contains(sec, "A plane named in neither list above is not gated at all") {
			t.Fatalf("%s lists gates and never says what an absent plane means:\n%s", key, sec)
		}
	}

	// The sentence itself, in the words that made it false. Pinned as a
	// string because deleting the fix must turn this red, and a reworded
	// version of the same claim would otherwise slip back in unnoticed.
	for key, page := range pages {
		if strings.Contains(page, "A plane with no row here is not gated at all") {
			t.Fatalf("%s states that an absent budget row means the plane is ungated; budgetsOf withholds a row from a gated-but-unmeasurable plane too", key)
		}
	}
}

// F-4. Every other plane on this page has an explicit "nothing was
// evaluated here" arm. Hops rendered its <h2> and then, for a standalone
// run, nothing at all — and a heading over an empty section reads as
// "nothing to report", which is the measured-and-clean reading again.
// observedFor draws exactly this line at len(ServiceCounts) == 0.
func TestAStandaloneRunSaysItsHopPlaneWasNeverRecorded(t *testing.T) {
	pages := itemPages(t, gatedProject(t))

	recorded := pageSection(t, pages["web/paired"], "Hops")
	if !strings.Contains(recorded, "<td>api</td>") {
		t.Fatalf("test setup: a run with a recorded chain has no per-service counts:\n%s", recorded)
	}
	if strings.Contains(recorded, "No hop chain was recorded") {
		t.Fatalf("a run WITH a recorded hop chain says it has none:\n%s", recorded)
	}

	absent := pageSection(t, pages["web/silent"], "Hops")
	if strings.Contains(absent, "<td>") {
		t.Fatalf("test setup: a standalone run produced per-service counts:\n%s", absent)
	}
	if !strings.Contains(absent, "No hop chain was recorded") {
		t.Fatalf("no hop chain was recorded on either side and this section is a heading over nothing, which reads as \"every service behaved\":\n%s", absent)
	}
	// The hopRequire gate is part of the hop plane: with no chain there is
	// nothing to confirm a required route against, so the section must not
	// carry the wording that belongs to a chain it does not have.
	if strings.Contains(absent, "was confirmed on this run") {
		t.Fatalf("a run with no hop chain claims a required route was confirmed:\n%s", absent)
	}
}

// F-5, the planes whose "not evaluated" arms had no fixture at all. Each
// pair is the same section from the flow that evaluated the plane and the
// flow that did not; the sentence under test is true of exactly one of them.
func TestAPlaneThatWasNotEvaluatedNeverRendersAsEvaluatedAndClean(t *testing.T) {
	gated := itemPages(t, gatedProject(t))

	bare := t.TempDir() // no retrace.yaml at all, so no spec is configured
	changedFlow(t, bare, "web", "cart", "home")
	unconfigured := itemPages(t, bare)["web/cart"]

	for _, tc := range []struct {
		section          string
		evaluated, blank string
		want, notWant    string
	}{{
		// M3: a flow with no perf budget must not share the wording of one
		// that met its budget.
		section:   "Performance",
		evaluated: gated["web/paired"],
		blank:     gated["web/silent"],
		want:      "No performance budget is configured",
		notWant:   "ms budget",
	}, {
		// M1: OpenAPIConfigured is the ONLY thing separating "no spec" from
		// "a spec, and every call conformed" — both encode as an empty list.
		section:   "OpenAPI conformance",
		evaluated: gated["web/paired"],
		blank:     unconfigured,
		want:      "No OpenAPI spec is configured",
		notWant:   "conformed to the configured spec",
	}, {
		// M8: a flow with no checkpoints on either side must say so rather
		// than render an empty Checkpoints section.
		section:   "Checkpoints",
		evaluated: gated["web/paired"],
		blank:     gated["web/nothing"],
		want:      "No checkpoints were captured on either side",
		notWant:   "% of pixels differ",
	}} {
		t.Run(tc.section, func(t *testing.T) {
			yes := pageSection(t, tc.evaluated, tc.section)
			if !strings.Contains(yes, tc.notWant) {
				t.Fatalf("test setup: the evaluated arm of %q does not contain %q:\n%s", tc.section, tc.notWant, yes)
			}
			if strings.Contains(yes, tc.want) {
				t.Fatalf("the arm that DID evaluate %q says it did not:\n%s", tc.section, yes)
			}
			no := pageSection(t, tc.blank, tc.section)
			if strings.Contains(no, tc.notWant) {
				t.Fatalf("%q was never evaluated for this flow and the page reports it as evaluated (%q):\n%s", tc.section, tc.notWant, no)
			}
			if !strings.Contains(no, tc.want) {
				t.Fatalf("%q was never evaluated for this flow and the page does not say so — an empty section reads as \"nothing to report\":\n%s", tc.section, no)
			}
		})
	}
}

// missingCheckpointProject records two checkpoints, accepts them, then
// records a run that captured only one of them. Every other pixel fixture
// in this suite records the SAME checkpoint names on both sides, so the
// non-comparing verdicts ("missing", "added", "unreadable") were never
// constructed and their rendering was never seen.
func missingCheckpointProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	calls := []trace.Hop{hop(1, "GET", "/receipts", 200, `{"ok":true}`)}
	recordRun(t, cwd, "web", "receipts", runA, map[string][]byte{
		"home": shotPNG(t, white), "receipt": shotPNG(t, white),
	}, calls)
	acceptRef(t, cwd, "web", "receipts", runA)
	recordRun(t, cwd, "web", "receipts", runB, map[string][]byte{"home": shotPNG(t, white)}, calls)
	return cwd
}

// F-2. "missing", "added" and "unreadable" leave DiffPct and NumDiff at
// zero, and the page printed "0.00% of pixels differ · 0 pixels" for all of
// them: a measurement this report invented, and the most reassuring one
// available, for a checkpoint no pixel of which was ever read. RenderText —
// the other face of the same Summary — has always drawn this line, printing
// the word for the non-comparing verdicts and the number only for
// ok/changed. Two faces of one report must not disagree.
func TestACheckpointThatWasNeverComparedShowsNoMeasurement(t *testing.T) {
	pages := itemPages(t, missingCheckpointProject(t))
	page := pages["web/receipts"]

	never := checkpointBlock(t, page, "receipt")
	if !strings.Contains(never, "<h3>receipt — missing</h3>") {
		t.Fatalf("test setup: the checkpoint dropped on side B is not rendered as missing:\n%s", never)
	}
	if strings.Contains(never, "% of pixels differ") || strings.Contains(never, "pixels</p>") {
		t.Fatalf("a checkpoint that was never compared prints a pixel measurement, and every number in it is a zero this report supplied:\n%s", never)
	}
	if !strings.Contains(never, "was not captured on this run") {
		t.Fatalf("a checkpoint that was never compared does not say what happened instead:\n%s", never)
	}
	// A blank comparison pane reads as "identical" — the hazard shots()'s
	// own Missing list exists to prevent, reached here by a path that does
	// not populate it.
	if strings.Contains(never, `<div class="shots">`) {
		t.Fatalf("an uncompared checkpoint renders a shots container with nothing in it, and an empty pane reads as \"identical\":\n%s", never)
	}
	if !strings.Contains(never, "No image is shown for this checkpoint") {
		t.Fatalf("an uncompared checkpoint shows no images and does not say why:\n%s", never)
	}

	// The counter-arm: the checkpoint that WAS compared still measures.
	compared := checkpointBlock(t, page, "home")
	if !strings.Contains(compared, "<h3>home — ok</h3>") {
		t.Fatalf("test setup: the checkpoint captured on both sides is not rendered as ok:\n%s", compared)
	}
	if !strings.Contains(compared, "% of pixels differ") {
		t.Fatalf("a checkpoint that WAS compared shows no measurement:\n%s", compared)
	}
	if !strings.Contains(compared, `<div class="shots">`) {
		t.Fatalf("a compared checkpoint shows no shots:\n%s", compared)
	}

}

// The other half of F-2, on the overview: Counts.Checkpoints is the UNION
// of both manifests, so a strip built from it reports the pixel plane as
// having covered a checkpoint no pixel of which was read.
func TestTheOverviewCountsOnlyTheCheckpointsThatWereActuallyCompared(t *testing.T) {
	_, out := exportTo(t, missingCheckpointProject(t), "", "")
	row := rowFor(t, readText(t, out, "index.html"), "web/receipts")
	if !strings.Contains(row, "1 of 2 checkpoints compared") {
		t.Fatalf("this run captured one of the reference's two checkpoints, and the row does not say so:\n%s", row)
	}
}

// paneRe and detailsRe read the caption a pane actually carries next to the
// file it actually points at. Asserting the two together is the point: a
// label swap and a file swap are separately silent, and either one inverts
// what a reviewer concludes.
var (
	paneRe    = regexp.MustCompile(`<figure class="shot"><img src="([^"]+)" alt="[^"]*"><figcaption>([^<]+)</figcaption></figure>`)
	detailsRe = regexp.MustCompile(`<details class="shot"><summary>([^<]+)</summary><img src="([^"]+)"`)
)

func panesIn(block string) map[string]string {
	out := map[string]string{}
	for _, m := range paneRe.FindAllStringSubmatch(block, -1) {
		out[m[1]] = m[2]
	}
	for _, m := range detailsRe.FindAllStringSubmatch(block, -1) {
		out[m[2]] = m[1]
	}
	return out
}

// F-3. Nothing said which run's pixels are in the `a` pane. Task 13's
// swapped-shot-panes finding lives in a document this artifact's reader
// cannot re-query, and summary.json's own images.a resolves into this
// export's a/ — so a swap inverts the human reading AND the machine one,
// and an agent reaches the same wrong conclusion with more confidence.
//
// The fixture already discriminated and the assertion threw it away:
// changedFlow records side A white and side B blue, and the shot test
// asserted only the PNG magic. Bytes and captions, together.
func TestTheReferencePaneHoldsTheReferenceRunsPixels(t *testing.T) {
	cwd := t.TempDir()
	changedFlow(t, cwd, "web", "cart", "home")
	res, out := exportTo(t, cwd, "", "")
	if res.Items != 1 {
		t.Fatalf("test setup: exported %d items, want 1", res.Items)
	}

	// A is the accepted reference (white); B is the run under review (blue).
	for _, tc := range []struct {
		side string
		want []byte
	}{{"a", shotPNG(t, white)}, {"b", shotPNG(t, blue)}} {
		got, err := os.ReadFile(filepath.Join(out, "web", "cart", tc.side, "shots", "home.png"))
		if err != nil {
			t.Fatalf("reading the %s pane: %v", tc.side, err)
		}
		if !bytes.Equal(got, tc.want) {
			other := "b"
			if tc.side == "b" {
				other = "a"
			}
			t.Fatalf("the %q pane does not hold the pixels of the run it names; the two sides are swapped, so every reviewer reads this flow's change backwards (and so does summary.json, whose images.%s resolves here). It holds the %s side's bytes: %v", tc.side, tc.side, other, bytes.Equal(got, shotPNG(t, blue)))
		}
	}

	block := checkpointBlock(t, readText(t, out, "web", "cart", "index.html"), "home")
	want := map[string]string{
		"a/shots/home.png":       "reference",
		"b/shots/home.png":       "this run",
		"diff/shots/home.png":    "changed pixels",
		"overlay/shots/home.png": "overlay",
	}
	got := panesIn(block)
	for src, label := range want {
		if got[src] != label {
			t.Fatalf("the pane pointing at %s is captioned %q, want %q — a caption is the only thing telling a reviewer which run they are looking at:\n%s", src, got[src], label, block)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("this checkpoint rendered %d panes, want %d: %v", len(got), len(want), got)
	}
}

// hopRequireProject configures a required route AND records the hop chain
// that can confirm it — the counter-arm to gatedProject, where no
// hopRequire route is configured at all.
func hopRequireProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	writeFileAt(t, cwd, "retrace.yaml", `app: web
hop_require:
  - method: GET
    path: /orders
    status: 200
`)
	calls := []trace.Hop{hop(1, "GET", "/orders", 200, `{"ok":true}`)}
	for _, id := range []string{runA, runB} {
		p := recordRun(t, cwd, "web", "orders", id, map[string][]byte{"home": shotPNG(t, white)}, calls)
		writeChain(t, p, calls)
		if id == runA {
			acceptRef(t, cwd, "web", "orders", runA)
		}
	}
	return cwd
}

// uncheckedProject records a call whose response body was truncated at
// capture against a spec that documents a required field. The
// required-field check then genuinely cannot run, which Task 9 gave its own
// finding kind ("unchecked") precisely so it could never be counted as a
// pass — and the same recording carries the wire plane's truncation note.
func uncheckedProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	writeFileAt(t, cwd, "openapi.json", `{"paths": {"/orders": {"get": {"responses": {"200": {"content": {"application/json": {"schema": {"type": "object", "required": ["id"]}}}}}}}}}`)
	writeFileAt(t, cwd, "retrace.yaml", "app: web\nopenapi: openapi.json\n")
	h := hop(1, "GET", "/orders", 200, `{"id":1,`)
	h.Resp.Truncated = true
	for _, id := range []string{runA, runB} {
		recordRun(t, cwd, "web", "orders", id, map[string][]byte{"home": shotPNG(t, white)}, []trace.Hop{h})
		if id == runA {
			acceptRef(t, cwd, "web", "orders", runA)
		}
	}
	return cwd
}

// The remaining F-5 arms: a configured hopRequire gate, a conformance check
// that could not run, and a body the capture truncated. Each is a state the
// Go models explicitly and the page had no fixture for, so the arm that
// says "this was not checked" and the arm that says "this was checked and
// is fine" were interchangeable from inside the suite.
func TestTheReportDistinguishesACheckThatRanFromOneThatCouldNot(t *testing.T) {
	t.Run("a configured hopRequire route versus none", func(t *testing.T) {
		configured := pageSection(t, itemPages(t, hopRequireProject(t))["web/orders"], "Hops")
		if !strings.Contains(configured, "was confirmed on this run") {
			t.Fatalf("a configured hopRequire route was confirmed and the page does not say so:\n%s", configured)
		}
		if strings.Contains(configured, "No <code>hopRequire</code> routes are configured") {
			t.Fatalf("this project configures a hopRequire route and the page says none is configured:\n%s", configured)
		}

		none := pageSection(t, itemPages(t, gatedProject(t))["web/paired"], "Hops")
		if strings.Contains(none, "was confirmed on this run") {
			t.Fatalf("no hopRequire route is configured here and the page reports one confirmed — an ungated build reading as a gate that passed:\n%s", none)
		}
		if !strings.Contains(none, "No <code>hopRequire</code> routes are configured") {
			t.Fatalf("no hopRequire route is configured here and the page does not say so:\n%s", none)
		}
	})

	t.Run("a conformance check that could not run is not a pass", func(t *testing.T) {
		page := itemPages(t, uncheckedProject(t))["web/orders"]
		conformance := pageSection(t, page, "OpenAPI conformance")
		if !strings.Contains(conformance, `<span class="note">unchecked</span>`) {
			t.Fatalf("the required-field check could not run for this call and the finding is not marked unchecked:\n%s", conformance)
		}
		if !strings.Contains(conformance, "is not a pass") {
			t.Fatalf("an unchecked conformance entry is listed with no note saying it is not a pass — a reader counts it as one:\n%s", conformance)
		}
		if strings.Contains(conformance, "conformed to the configured spec") {
			t.Fatalf("a call the spec check could not run on is reported as conforming:\n%s", conformance)
		}
	})

	t.Run("a body truncated at capture says so on the wire row", func(t *testing.T) {
		truncated := pageSection(t, itemPages(t, uncheckedProject(t))["web/orders"], "Wire")
		if !strings.Contains(truncated, "body truncated at capture") {
			t.Fatalf("both payloads were truncated at capture and the wire row does not say so — a row with no differences over a body nobody has all of:\n%s", truncated)
		}
		whole := pageSection(t, itemPages(t, gatedProject(t))["web/paired"], "Wire")
		if strings.Contains(whole, "body truncated at capture") {
			t.Fatalf("no payload here was truncated and the wire row says one was:\n%s", whole)
		}
	})
}

// M7. A side the Summary named and the export could not copy must be
// CALLED OUT, never quietly dropped: an omitted pane and a pane that
// matched are the same empty space on the page.
//
// The state is reachable because the two steps are not atomic: SummaryFor
// reads the reference bundle to build the comparison, and the copy happens
// afterwards, so anything that removes a committed PNG in between (a
// checkout on the same worktree, an LFS prune — the shape refs.go's own
// comment names) leaves a summary naming an image that is no longer there.
// It is driven here at the seam rather than through Export, because a
// fixture cannot sit inside that window from the outside.
func TestAShotSideTheExportCouldNotCopyIsNamedRatherThanOmitted(t *testing.T) {
	cwd := t.TempDir()
	changedFlow(t, cwd, "web", "cart", "home")
	d := deps(t, cwd)
	sum, err := SummaryFor(d, "web", "cart")
	if err != nil {
		t.Fatalf("SummaryFor: %v", err)
	}
	if sum.Checkpoints[0].Images.A == "" {
		t.Fatalf("test setup: the summary names no reference image: %+v", sum.Checkpoints[0])
	}
	if err := os.Remove(filepath.Join(sum.A.Dir, "shots", "home.png")); err != nil {
		t.Fatalf("removing the reference shot: %v", err)
	}

	out := filepath.Join(t.TempDir(), "report")
	e := &exporter{opts: ExportOptions{Deps: d, OutDir: out}, root: out}
	shots, err := e.shots("web/cart", sum)
	if err != nil {
		t.Fatalf("shots: %v", err)
	}
	if len(shots) != 1 || !slicesContains(shots[0].Missing, "a") {
		t.Fatalf("the reference image was gone at copy time and the export does not record it as missing: %+v", shots)
	}
	for _, side := range shots[0].Sides {
		if side.Side == "a" {
			t.Fatalf("the export references a reference pane whose file it never copied — a broken <img> renders as a blank pane, and a blank comparison pane reads as \"identical\"")
		}
	}

	var page bytes.Buffer
	if err := reportTemplate.ExecuteTemplate(&page, "item", reportItem{
		Row:   reportRow{Key: "web/cart", Verdict: "changed", Compared: true},
		Shots: shots, Summary: &sum,
	}); err != nil {
		t.Fatalf("rendering the item page: %v", err)
	}
	block := checkpointBlock(t, page.String(), "home")
	if !strings.Contains(block, "Not in this export: a ") {
		t.Fatalf("the page shows three of four panes and never says the fourth is absent:\n%s", block)
	}
}

// An artifact is read out of a build store weeks later, frequently beside
// another copy of itself, and nothing in it says which run produced it. An
// undated report is one a reader takes for the current one.
func TestEveryPageSaysWhenTheExportWasProduced(t *testing.T) {
	cwd := t.TempDir()
	changedFlow(t, cwd, "web", "cart", "home")
	out := filepath.Join(t.TempDir(), "report")
	d := deps(t, cwd)
	d.Now = func() time.Time { return time.Date(2026, 8, 21, 9, 30, 0, 0, time.UTC) }
	res, err := Export(ExportOptions{Deps: d, OutDir: out})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	_ = res
	for _, page := range htmlFilesIn(t, out) {
		body := readText(t, page)
		if !strings.Contains(body, "2026-08-21T09:30:00Z") {
			t.Fatalf("%s carries no produced-at stamp, so a reader cannot tell this report from a newer one:\n%s", page, body)
		}
	}
}

// Fix round 2. Two sentences claimed a verified result over evidence the
// run does not contain — F-1's shape one plane down, twice:
//
//   - conformance: "Every recorded call conformed to the configured spec"
//     is VACUOUSLY true over zero recorded calls, and reads as a pass.
//   - performance: MeasuredMs sums side B's call durations, so a run with
//     no calls measures 0ms, comes in under any budget, and CheckPerfBudget
//     calls that "ok".
//
// Both are two-armed against a flow that did record calls, because the
// sentence each replaces is the correct one for that flow.
func TestAPlaneWithNoCallsToExamineDoesNotReportAPass(t *testing.T) {
	pages := itemPages(t, gatedProject(t))
	for _, tc := range []struct {
		section       string
		want, notWant string
	}{{
		section: "OpenAPI conformance",
		want:    "No call was recorded on this run, so nothing was checked against the configured spec",
		notWant: "Every recorded call conformed to the configured spec",
	}, {
		section: "Performance",
		want:    "this run recorded no calls, so there was no backend work to measure against it",
		notWant: "ms of backend work against a",
	}} {
		t.Run(tc.section, func(t *testing.T) {
			// The arm that DID have calls to examine keeps its sentence.
			examined := pageSection(t, pages["web/paired"], tc.section)
			if !strings.Contains(examined, tc.notWant) {
				t.Fatalf("test setup: the flow that recorded calls does not carry %q:\n%s", tc.notWant, examined)
			}
			if strings.Contains(examined, tc.want) {
				t.Fatalf("a flow that recorded calls says it recorded none:\n%s", examined)
			}
			// web/quiet configures BOTH planes and recorded no calls.
			quiet := pageSection(t, pages["web/quiet"], tc.section)
			if strings.Contains(quiet, tc.notWant) {
				t.Fatalf("this run recorded no call at all and %q reports a verified result over it:\n%s", tc.section, quiet)
			}
			if !strings.Contains(quiet, tc.want) {
				t.Fatalf("this run recorded no call at all and %q does not say so:\n%s", tc.section, quiet)
			}
		})
	}
}

// TestEveryComparedFlowSaysWhoseProblemItIs. The four planes each report
// what moved; none of them reports what to do about it, and this artifact is
// read by someone who was not there for the run. The label is the answer, and
// the signal vector beside it is what lets them check the answer — a
// classification a reader must take on faith is one they cannot act on.
func TestEveryComparedFlowSaysWhoseProblemItIs(t *testing.T) {
	pages := itemPages(t, gatedProject(t))
	for key, page := range pages {
		sec := pageSection(t, page, "Triage")
		if !strings.Contains(sec, "by rule <code>") {
			t.Errorf("%s has a Triage section that names no rule — the label is then an assertion the reader cannot trace:\n%s", key, sec)
		}
		if !strings.Contains(sec, "signals moved:") {
			t.Errorf("%s prints a triage label with none of the evidence behind it:\n%s", key, sec)
		}
	}
	// Every flow in this fixture records the same run twice, so nothing
	// moved anywhere — and "nothing moved" has its own label, distinct from
	// every plane-attribution label.
	paired := pageSection(t, pages["web/paired"], "Triage")
	if !strings.Contains(paired, "<strong>none</strong>") {
		t.Errorf("a flow where nothing moved is not labelled `none`:\n%s", paired)
	}
	if !strings.Contains(paired, "signals moved:\nnone") && !strings.Contains(paired, "signals moved: none") {
		t.Errorf("a flow where nothing moved does not say its signal vector is empty:\n%s", paired)
	}
}

// TestTheOverviewCarriesTheTriageLabel. The overview is the page a reader
// opens first and, for a passing build, the only one they open. A label
// available only one click in is a label that does not reach them.
func TestTheOverviewCarriesTheTriageLabel(t *testing.T) {
	_, out := exportTo(t, gatedProject(t), "", "")
	index := readText(t, out, "index.html")
	if strings.Count(index, `class="row__triage"`) != 4 {
		t.Errorf("the overview carries %d triage labels across 4 compared flows:\n%s", strings.Count(index, `class="row__triage"`), index)
	}
}
