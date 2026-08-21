package main

// cmd_ref_test.go drives `retrace ref` through a BUILT binary, never `go
// run` (which collapses every non-zero child to 1 — see
// global-constraints.md), and records its fixture runs with `retrace run`
// itself. The verbs here are the seam between three packages that are each
// unit-tested in isolation; the defects this phase keeps paying for are
// wiring mistakes only the real entry point shows.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// dateRuleConfig tolerates the upstream's auto-set Date header, which
// genuinely differs between two runs seconds apart. Without it every diff
// in this file reports a change that is not one.
const dateRuleConfig = "app: web\nwire_rules:\n  - headers:\n      date: http-date\n"

func TestRefAcceptThenDiffAgainstReferenceExitsZero(t *testing.T) {
	// The round trip that proves the whole chain: accept promotes a run into
	// the committed bundle, and the DEFAULT diff selector (`--a reference`)
	// resolves to it. It cannot pass while Task 10's stub stands, because
	// the stub made that selector error.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	acc := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web")
	if acc.code != 0 {
		t.Fatalf("ref accept: exit = %d\nstdout: %s\nstderr: %s", acc.code, acc.stdout, acc.stderr)
	}
	bundle, err := refs.BundleDir(cwd, "web", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bundle, "manifest.json")); err != nil {
		t.Fatalf("ref accept wrote no bundle manifest: %v", err)
	}

	// A second, identical run — so the only thing under test is that the
	// bundle is what side A resolved to.
	runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	// --a omitted on purpose: the default IS "reference".
	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--json")
	if res.code != 0 {
		t.Fatalf("diff against the accepted reference: exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	var s struct {
		A struct {
			Kind  string `json:"kind"`
			Dir   string `json:"dir"`
			RunID string `json:"runId"`
		} `json:"a"`
		B struct {
			Kind string `json:"kind"`
		} `json:"b"`
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &s); err != nil {
		t.Fatalf("diff --json: %v\n%s", err, res.stdout)
	}
	if s.A.Kind != "bundle" {
		t.Fatalf("summary.a.kind = %q, want \"bundle\" — the default selector must resolve to the committed bundle, not fall back to a run", s.A.Kind)
	}
	if !samePath(t, s.A.Dir, bundle) {
		t.Fatalf("summary.a.dir = %q, want the bundle %q", s.A.Dir, bundle)
	}
	if s.B.Kind != "run" {
		t.Fatalf("summary.b.kind = %q, want \"run\" — side B's default is the latest run, never the reference", s.B.Kind)
	}
	if s.Verdict != "pass" {
		t.Fatalf("verdict = %q, want \"pass\"\n%s", s.Verdict, res.stdout)
	}
}

func TestDiffAgainstAMissingReferenceExplainsHowToCreateOne(t *testing.T) {
	// The good half of Task 10's stub, kept deliberately: Kind == "none"
	// means "I could not compare", never "nothing differed". Exit 3, naming
	// the verb that fixes it — and naming the candidates it tried, so the
	// operator is not told merely that there is no reference.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	gitInitDirty(t, cwd) // every run is dirty-tree ineligible; see the helper
	id := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d (could not evaluate)\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "retrace ref accept") {
		t.Fatalf("stderr = %q, want it to name `retrace ref accept`", res.stderr)
	}
	if !strings.Contains(res.stderr, id) || !strings.Contains(res.stderr, "dirty") {
		t.Fatalf("stderr = %q, want it to name the run it tried (%s) and why it was rejected", res.stderr, id)
	}
	if strings.Contains(res.stdout, "VERDICT") {
		t.Fatalf("stdout = %q, want NO verdict — nothing was compared, so there is nothing to report as clean", res.stdout)
	}
}

func TestRefListSaysWhatEachFlowResolvesToAndWhy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	// Three flows in three different states, so a listing that reported one
	// state for all of them cannot pass: checkout has a bundle, cart has an
	// eligible run only, and browse is configured but never recorded.
	writeConfig(t, cwd, dateRuleConfig+"flows:\n  browse: {}\n")
	runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	runOnce(t, bin, cwd, "web", "cart", upstream.URL)
	if res := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web"); res.code != 0 {
		t.Fatalf("ref accept: exit = %d\n%s%s", res.code, res.stdout, res.stderr)
	}

	res := runRetrace(t, bin, cwd, "", "ref", "list", "--app", "web", "--json")
	if res.code != 0 {
		t.Fatalf("ref list: exit = %d\nstderr: %s", res.code, res.stderr)
	}
	var got []struct {
		App       string         `json:"app"`
		Flow      string         `json:"flow"`
		Reference refs.Reference `json:"reference"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
		t.Fatalf("ref list --json: %v\n%s", err, res.stdout)
	}
	kinds := map[string]string{}
	reasons := map[string]string{}
	for _, e := range got {
		kinds[e.Flow] = e.Reference.Kind
		reasons[e.Flow] = e.Reference.Reason
	}
	for flow, want := range map[string]string{"checkout": "bundle", "cart": "run", "browse": "none"} {
		if kinds[flow] != want {
			t.Fatalf("ref list: %s resolved to %q, want %q (all: %+v)", flow, kinds[flow], want, kinds)
		}
	}
	if !strings.Contains(reasons["browse"], "no runs captured") {
		t.Fatalf("browse reason = %q, want it to distinguish \"never recorded\" from \"nothing eligible\"", reasons["browse"])
	}

	text := runRetrace(t, bin, cwd, "", "ref", "list", "--app", "web")
	if text.code != 0 {
		t.Fatalf("ref list (text): exit = %d\nstderr: %s", text.code, text.stderr)
	}
	for _, want := range []string{"web/checkout", "bundle", "web/cart", "run", "web/browse", "none"} {
		if !strings.Contains(text.stdout, want) {
			t.Fatalf("ref list stdout = %q, want it to contain %q", text.stdout, want)
		}
	}
}

func TestRefListNamesEveryCandidateItRejected(t *testing.T) {
	// A "none" that lists nothing reads as "there were no candidates" when
	// the truth is "there were two and both were dirty".
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	gitInitDirty(t, cwd)
	first := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	second := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "ref", "list", "--app", "web", "--flow", "checkout")
	if res.code != 0 {
		t.Fatalf("ref list: exit = %d\nstderr: %s", res.code, res.stderr)
	}
	for _, want := range []string{"none", first, second, "dirty"} {
		if !strings.Contains(res.stdout, want) {
			t.Fatalf("ref list stdout = %q, want it to contain %q", res.stdout, want)
		}
	}
}

func TestRefRejectWritesAReproBundleCarryingTheDiffThatMotivatedIt(t *testing.T) {
	body := `{"ok":true}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	if res := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web"); res.code != 0 {
		t.Fatalf("ref accept: exit = %d\n%s%s", res.code, res.stdout, res.stderr)
	}
	// The candidate run genuinely differs from the reference, so the
	// summary this bundle carries is a real finding rather than an empty
	// document that would pass whatever the diff did.
	body = `{"ok":false}`
	bad := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "ref", "reject", "--flow", "checkout", "--app", "web", "--json")
	if res.code != 0 {
		t.Fatalf("ref reject: exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	var out struct {
		Dir   string   `json:"dir"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("ref reject --json: %v\n%s", err, res.stdout)
	}
	if want := filepath.Join(cwd, ".retrace", "repro", "web__checkout__"+bad); !samePath(t, out.Dir, want) {
		t.Fatalf("repro dir = %q, want %q", out.Dir, want)
	}
	b, err := os.ReadFile(filepath.Join(out.Dir, "summary.json"))
	if err != nil {
		t.Fatalf("reading summary.json: %v\nstderr: %s", err, res.stderr)
	}
	var s struct {
		Verdict string `json:"verdict"`
		B       struct {
			RunID string `json:"runId"`
		} `json:"b"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("summary.json: %v", err)
	}
	if s.Verdict == "pass" {
		t.Fatalf("summary.json verdict = %q — the rejected run differs from the reference, so a pass means the diff never looked at it", s.Verdict)
	}
	if s.B.RunID != bad {
		t.Fatalf("summary.json b.runId = %q, want the rejected run %q", s.B.RunID, bad)
	}
}

func TestRefRejectWithoutAReferenceStillWritesTheBundleAndSaysWhyThereIsNoSummary(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	gitInitDirty(t, cwd)
	id := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "ref", "reject", "--flow", "checkout", "--app", "web")
	if res.code != 0 {
		t.Fatalf("ref reject: exit = %d\nstderr: %s", res.code, res.stderr)
	}
	dir := filepath.Join(cwd, ".retrace", "repro", "web__checkout__"+id)
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("repro bundle missing its manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); err == nil {
		t.Fatal("summary.json exists with no reference to diff against — an empty summary asserts a comparison that never ran")
	}
	if !strings.Contains(res.stderr, "no summary.json") {
		t.Fatalf("stderr = %q, want it to say why the bundle carries no summary", res.stderr)
	}
}

func TestRefAcceptWarnsOnStderrWhenPromotingANonOkCapture(t *testing.T) {
	// A run with no upstream traffic at all grades "degraded", so this
	// drives the real assessor rather than hand-editing a verdict onto a
	// manifest production would never write.
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	settlePastRunIDResolution()
	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", "http://127.0.0.1:1"},
		selfCmd(t, "TestHelperPostsMarkers")...)
	if r := runRetrace(t, bin, cwd, "markers", args...); r.code != 0 {
		t.Fatalf("retrace run: exit = %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout")
	if len(ids) != 1 {
		t.Fatalf("run dirs = %v, want one", ids)
	}

	res := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web", "--json")
	if res.code != 0 {
		t.Fatalf("ref accept refused a non-ok capture: exit = %d\nstderr: %s — promotion is explicit, so this is the human's call", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "warning") {
		t.Fatalf("stderr = %q, want a warning on the channel a CI log keeps", res.stderr)
	}
	var out struct {
		CaptureStatus string `json:"captureStatus"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("ref accept --json: %v\n%s", err, res.stdout)
	}
	if out.CaptureStatus == "" || out.CaptureStatus == "ok" {
		t.Fatalf("captureStatus = %q, want the real non-ok verdict carried as a typed field, not reconstructible only from the warning text", out.CaptureStatus)
	}
}

// samePath compares two filesystem paths through EvalSymlinks. The child
// process resolves its own working directory (macOS maps /var to
// /private/var), so a literal string comparison against the path the test
// built would fail for a reason that has nothing to do with the code.
func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", a, err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", b, err)
	}
	return ra == rb
}
