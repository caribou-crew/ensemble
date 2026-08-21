package main

// cmd_ref_test.go drives `retrace ref` through a BUILT binary, never `go
// run` (which collapses every non-zero child to 1 — see
// global-constraints.md), and records its fixture runs with `retrace run`
// itself. The verbs here are the seam between three packages that are each
// unit-tested in isolation; the defects this phase keeps paying for are
// wiring mistakes only the real entry point shows.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	// atomic, not a plain string: the handler runs on the httptest server's
	// own goroutines while the test body swaps the payload between runs, and
	// -race is a gate this repo runs on every commit.
	var body atomic.Value
	body.Store(`{"ok":true}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body.Load().(string)))
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
	body.Store(`{"ok":false}`)
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

// TestRefAcceptRefusesAFatalCaptureUntilForced drives the tier split
// end-to-end through the real assessor rather than a hand-edited verdict: a
// run with no upstream traffic at all grades "degraded", which is
// capture.Fatal. That is the proxy-down run this gate exists for — the
// disaster is a run the capture machinery could not vouch for becoming the
// thing every later diff is judged against, and a warning in a CI log is
// not a gate.
//
// Both arms are here because a refusal nobody can get past is a different
// bug from a gate that never closes.
func TestRefAcceptRefusesAFatalCaptureUntilForced(t *testing.T) {
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

	refused := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web", "--json")
	if refused.code == 0 {
		t.Fatalf("ref accept promoted a degraded capture\nstdout: %s", refused.stdout)
	}
	if !strings.Contains(refused.stderr, "--force") {
		t.Fatalf("stderr = %q, want the refusal to name the flag that overrides it", refused.stderr)
	}
	bundle, err := refs.BundleDir(cwd, "web", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); err == nil {
		t.Fatal("the refused promotion still wrote a bundle")
	}

	res := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web", "--force", "--json")
	if res.code != 0 {
		t.Fatalf("ref accept --force: exit = %d\nstderr: %s — --force exists for exactly this refusal", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "warning") {
		t.Fatalf("stderr = %q, want a warning on the channel a CI log keeps — forcing is not the same as it being fine", res.stderr)
	}
	var out struct {
		CaptureStatus string `json:"captureStatus"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("ref accept --json: %v\n%s", err, res.stdout)
	}
	if out.CaptureStatus == "" || out.CaptureStatus == "ok" {
		t.Fatalf("captureStatus = %q, want the real verdict carried as a typed field, not reconstructible only from the warning text", out.CaptureStatus)
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

// TestRefRuleAppendsToTheOverlayAndSilencesTheDiff drives the verb end to
// end through the same code path the review queue's `rule` verb uses. The
// silencing half is what proves the rule reached the ENGINE and not merely
// the file: an overlay that is written but never merged into Discover's
// config would pass a "the JSON has one entry" assertion and change nothing.
func TestRefRuleAppendsToTheOverlayAndSilencesTheDiff(t *testing.T) {
	// Same reason as above: the counter is touched from the server's
	// goroutines, so it is atomic rather than a bare int.
	var n atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"requestId":%d}`, n.Add(1))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	if res := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web"); res.code != 0 {
		t.Fatalf("ref accept: exit = %d\n%s%s", res.code, res.stdout, res.stderr)
	}
	runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	// requestId differs between the two runs, so the diff has a finding.
	before := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web")
	if before.code != exitDiff {
		t.Fatalf("exit = %d, want %d (a real difference to silence)\nstdout: %s\nstderr: %s", before.code, exitDiff, before.stdout, before.stderr)
	}

	rule := runRetrace(t, bin, cwd, "", "ref", "rule",
		"--field", "requestId", "--matcher", "integer")
	if rule.code != 0 {
		t.Fatalf("ref rule: exit = %d\nstdout: %s\nstderr: %s", rule.code, rule.stdout, rule.stderr)
	}
	// The overlay is the machine-owned file config.OverlayPath names — the
	// same one Discover merges, not a second store.
	overlay, err := os.ReadFile(filepath.Join(cwd, ".retrace", "wire-rules.json"))
	if err != nil {
		t.Fatalf("reading the overlay: %v", err)
	}
	if !strings.Contains(string(overlay), "requestId") || !strings.Contains(string(overlay), "integer") {
		t.Fatalf("overlay = %s, want the appended rule", overlay)
	}
	// What the rule covers is stated POSITIVELY on success, in the same
	// terms the refusal of --flow/--scope uses. A reader of this line must
	// be unable to form the narrower belief.
	for _, want := range []string{"every flow", "request and response"} {
		if !strings.Contains(rule.stdout, want) {
			t.Fatalf("ref rule stdout = %q, want it to say the rule covers %q", rule.stdout, want)
		}
	}

	after := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web")
	if after.code != exitOK {
		t.Fatalf("exit = %d, want 0 — the appended rule must reach the diff engine, not just the file\nstdout: %s\nstderr: %s", after.code, after.stdout, after.stderr)
	}

	// Idempotent: pressing the same rule twice must not grow the file.
	if res := runRetrace(t, bin, cwd, "", "ref", "rule",
		"--field", "requestId", "--matcher", "integer"); res.code != 0 {
		t.Fatalf("second ref rule: exit = %d\n%s", res.code, res.stderr)
	}
	again, err := os.ReadFile(filepath.Join(cwd, ".retrace", "wire-rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(overlay) {
		t.Fatalf("the overlay grew on a repeated rule:\n%s\nvs\n%s", overlay, again)
	}
}

func TestRefRuleRefusesAnUnknownMatcherRatherThanBrickingTheOverlay(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)

	res := runRetrace(t, bin, cwd, "", "ref", "rule",
		"--field", "requestId", "--matcher", "intiger")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d", res.code, exitUsage)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".retrace", "wire-rules.json")); err == nil {
		t.Fatal("a rejected rule still wrote the overlay — an unknown matcher must fail the append, not brick every later Discover")
	}
}

func TestRefRuleRequiresItsFlagsRatherThanDefaultingThem(t *testing.T) {
	// Each flag is omitted in turn from an otherwise-complete command, so a
	// guard that checked only the first would fail here. An empty field glob
	// matches every field, and an empty matcher is the zero Matcher, which
	// classifies as Changed.
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	full := map[string]string{"--field": "requestId", "--matcher": "integer"}
	for omit := range full {
		t.Run("without "+omit, func(t *testing.T) {
			args := []string{"ref", "rule"}
			for k, v := range full {
				if k != omit {
					args = append(args, k, v)
				}
			}
			res := runRetrace(t, bin, cwd, "", args...)
			if res.code != exitUsage {
				t.Fatalf("exit = %d, want %d\nstderr: %s", res.code, exitUsage, res.stderr)
			}
			if !strings.Contains(res.stderr, omit) {
				t.Fatalf("stderr = %q, want it to name the missing flag %s", res.stderr, omit)
			}
		})
	}
}

// TestRefRuleRefusesTheFlagsTheDialectCannotExpress is the only guard these
// two flags have, and it is the reason they are refused rather than warned
// about. `--flow checkout` is the plausible value the zero-value constraint
// forbids: it sails past the person typing it and past the reviewer reading
// the pull request, while the rule silences the field in every flow. A flag
// that was never offered cannot be misread.
//
// The error text is PINNED, because "unknown flag: -flow" would tell a user
// they made a typo when what they actually did was assume this verb
// resembles its three siblings — all of which require --flow.
func TestRefRuleRefusesTheFlagsTheDialectCannotExpress(t *testing.T) {
	bin := buildRetrace(t)
	// Both spellings, because a check that split on "=" only, or on a
	// separate argument only, would let the other form through to
	// flag.Parse and produce the unknown-flag error this exists to avoid.
	for _, c := range []struct {
		name string
		args []string
	}{
		{"--flow as a separate argument", []string{"--flow", "checkout"}},
		{"--flow=value", []string{"--flow=checkout"}},
		{"--scope as a separate argument", []string{"--scope", "resp"}},
		{"--scope=value", []string{"--scope=resp"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cwd := t.TempDir()
			writeConfig(t, cwd, dateRuleConfig)
			args := append([]string{"ref", "rule", "--field", "requestId", "--matcher", "integer"}, c.args...)
			res := runRetrace(t, bin, cwd, "", args...)
			if res.code == exitOK {
				t.Fatalf("exit = 0 — the flag was accepted, so the user believes they scoped a rule that is not scoped\nstdout: %s", res.stdout)
			}
			if strings.Contains(res.stderr, "not defined") {
				t.Fatalf("stderr = %q — this is flag.Parse's unknown-flag error; it tells the user they typo'd when they in fact assumed this verb resembles its siblings", res.stderr)
			}
			// The consequence, not the limitation: what will happen to the
			// rule they are writing.
			for _, want := range []string{"EVERY flow", "BOTH the request and the response body", "--path"} {
				if !strings.Contains(res.stderr, want) {
					t.Fatalf("stderr = %q, want it to state %q", res.stderr, want)
				}
			}
			// And nothing was written: a refused command must not have
			// half-appended the rule it refused to describe honestly.
			if _, err := os.Stat(filepath.Join(cwd, ".retrace", "wire-rules.json")); err == nil {
				t.Fatal("the refused command still wrote the overlay")
			}
		})
	}
}

// TestTheSiblingVerbsStillRequireFlow pins the other side of the asymmetry.
// list, accept and reject each address a bundle directory at
// <app>/<flow>/reference, so --flow is load-bearing for them; only `rule`
// refuses it. Three verbs taking a flag and one refusing it is the
// interface telling the truth about the model, and something that looks
// enough like an inconsistency that a later reader may "fix" it.
func TestTheSiblingVerbsStillRequireFlow(t *testing.T) {
	bin := buildRetrace(t)
	for _, verb := range []string{"accept", "reject"} {
		t.Run(verb, func(t *testing.T) {
			cwd := t.TempDir()
			writeConfig(t, cwd, dateRuleConfig)
			res := runRetrace(t, bin, cwd, "", "ref", verb, "--app", "web")
			if res.code != exitUsage {
				t.Fatalf("exit = %d, want %d — %s addresses a bundle directory and cannot default its flow\nstderr: %s", res.code, exitUsage, verb, res.stderr)
			}
			if !strings.Contains(res.stderr, "--flow") {
				t.Fatalf("stderr = %q, want it to name --flow", res.stderr)
			}
		})
	}
}

// TestRefRejectRefusesToDiffTheRejectedRunAgainstItself — with no committed
// bundle, `reference` falls back to the newest eligible run, which for the
// latest run IS the run being rejected. A self-diff would write a
// summary.json saying "pass" into a bundle whose entire reason for existing
// is that something went wrong: a plausible value, which is worse than an
// absent one.
func TestRefRejectRefusesToDiffTheRejectedRunAgainstItself(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	// No `ref accept`, and the tree is clean, so `reference` resolves to the
	// newest eligible run — the one about to be rejected.
	id := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "ref", "reject", "--flow", "checkout", "--app", "web")
	if res.code != 0 {
		t.Fatalf("ref reject: exit = %d\nstderr: %s", res.code, res.stderr)
	}
	dir := filepath.Join(cwd, ".retrace", "repro", "web__checkout__"+id)
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); err == nil {
		t.Fatal("summary.json holds a self-diff of the rejected run — a \"pass\" in a repro bundle is worse than no summary at all")
	}
	if !strings.Contains(res.stderr, "itself") {
		t.Fatalf("stderr = %q, want it to say the only reference available was the rejected run itself", res.stderr)
	}
}
