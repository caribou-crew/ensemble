package refs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- fixtures ----------------------------------------------------------
//
// Fixtures are deliberately ASYMMETRIC in the dimension each test measures
// (see global-constraints.md): run ids differ, the bundle's provenance run
// id differs from every local run id, and the eligible run is never also
// the newest, so "prefer the bundle", "prefer the newest" and "prefer the
// eligible" are three distinguishable behaviours rather than one.

type runOpt func(*runs.Manifest)

func withCapture(status trace.Verdict, summary string) runOpt {
	return func(m *runs.Manifest) { m.Capture = runs.CaptureTrust{Status: status, Summary: summary} }
}

func withDirty() runOpt { return func(m *runs.Manifest) { m.Git.Dirty = true } }

func withCheckpoint(name, file string) runOpt {
	return func(m *runs.Manifest) {
		m.Checkpoints = append(m.Checkpoints, runs.Checkpoint{Name: name, File: file, Width: 2, Height: 2})
	}
}

// writeRun creates a real run directory through runs.Create/WriteManifest —
// never a hand-placed manifest.json — so every fixture here is a value
// production can actually construct.
func writeRun(t *testing.T, root, app, flow, runID string, opts ...runOpt) runs.Paths {
	t.Helper()
	p, err := runs.Create(root, app, flow, runID)
	if err != nil {
		t.Fatalf("runs.Create(%s): %v", runID, err)
	}
	m := runs.Manifest{
		App: app, Flow: flow, RunID: runID, Mode: runs.ModeStandalone,
		Git:       runs.Git{SHA: "deadbee", Branch: "main", Dirty: false},
		StartedAt: time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		Capture:   runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"},
		Wire:      runs.Counts{Calls: 1, Recorded: true},
	}
	for _, o := range opts {
		o(&m)
	}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest(%s): %v", runID, err)
	}
	return p
}

// writeBundle fakes an already-accepted bundle by hand, because Accept is
// not what these tests are measuring. provenance is the runId the bundle
// records — deliberately unlike any local run id, so a Resolve that
// returned a run instead of the bundle cannot pass by coincidence.
func writeBundle(t *testing.T, cwd, app, flow, provenance string) string {
	t.Helper()
	dir, err := BundleDir(cwd, app, flow)
	if err != nil {
		t.Fatalf("BundleDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "shots"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := runs.Manifest{
		App: app, Flow: flow, RunID: provenance, Mode: runs.ModeStandalone,
		Capture: runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"},
		Wire:    runs.Counts{Calls: 1, Recorded: true},
	}
	if err := runs.WriteManifest(runs.Paths{ManifestPath: filepath.Join(dir, "manifest.json")}, &m); err != nil {
		t.Fatalf("WriteManifest(bundle): %v", err)
	}
	return dir
}

func historyString(r Reference) string {
	var b strings.Builder
	for _, c := range r.History {
		b.WriteString(c.RunID + "=" + c.Reason + "|" + c.Detail + "; ")
	}
	return b.String()
}

// --- Step 1: resolve ---------------------------------------------------

func TestResolvePrefersTheCommittedBundle(t *testing.T) {
	cwd := t.TempDir()
	root := runs.RunsRoot(cwd)
	writeRun(t, root, "web", "checkout", "20260821T100000Z-aaa1111")
	dir := writeBundle(t, cwd, "web", "checkout", "20260101T000000Z-bbb2222")

	got := Resolve(cwd, root, "web", "checkout")
	if got.Kind != "bundle" {
		t.Fatalf("Kind = %q, want \"bundle\" — an eligible local run must not beat the committed bundle (%s)", got.Kind, got.Reason)
	}
	if got.Dir != dir {
		t.Fatalf("Dir = %q, want %q", got.Dir, dir)
	}
	if got.RunID != "20260101T000000Z-bbb2222" {
		t.Fatalf("RunID = %q, want the bundle's recorded provenance 20260101T000000Z-bbb2222", got.RunID)
	}
	if got.Manifest.RunID != "20260101T000000Z-bbb2222" {
		t.Fatalf("Manifest.RunID = %q, want the bundle's own manifest", got.Manifest.RunID)
	}
}

func TestResolveFallsBackToTheNewestEligibleRun(t *testing.T) {
	cwd := t.TempDir()
	root := runs.RunsRoot(cwd)
	// Oldest eligible, middle eligible, newest INELIGIBLE: the answer is the
	// middle one, so neither "newest, whatever it is" nor "the first
	// eligible in ascending order" can pass.
	writeRun(t, root, "web", "checkout", "20260821T100000Z-aaa1111")
	writeRun(t, root, "web", "checkout", "20260821T110000Z-bbb2222")
	writeRun(t, root, "web", "checkout", "20260821T120000Z-ccc3333", withDirty())

	got := Resolve(cwd, root, "web", "checkout")
	if got.Kind != "run" {
		t.Fatalf("Kind = %q, want \"run\" (%s)", got.Kind, got.Reason)
	}
	if got.RunID != "20260821T110000Z-bbb2222" {
		t.Fatalf("RunID = %q, want 20260821T110000Z-bbb2222 (newest ELIGIBLE)", got.RunID)
	}
	if want := filepath.Join(root, "web", "checkout", got.RunID); got.Dir != want {
		t.Fatalf("Dir = %q, want %q", got.Dir, want)
	}
	if got.Manifest.RunID != got.RunID {
		t.Fatalf("Manifest.RunID = %q, want the resolved run's own manifest %q", got.Manifest.RunID, got.RunID)
	}
	// The rejected newer run must still be named — an answer that silently
	// skipped it is indistinguishable from one that never saw it.
	var sawRejected bool
	for _, c := range got.History {
		if c.RunID == "20260821T120000Z-ccc3333" && !c.Eligible {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Fatalf("History = %s, want the skipped dirty run named", historyString(got))
	}
}

func TestARunWithANonOkCaptureIsIneligibleAndSaysWhy(t *testing.T) {
	// "unknown capture is not ok: a run predating the verdict cannot vouch
	// for itself" — a manifest with no capture block is ineligible too.
	t.Run("an assessed but not-ok capture", func(t *testing.T) {
		cwd := t.TempDir()
		root := runs.RunsRoot(cwd)
		writeRun(t, root, "web", "checkout", "20260821T100000Z-aaa1111",
			withCapture(trace.VerdictDegraded, "the proxy recorded no calls"))

		got := Resolve(cwd, root, "web", "checkout")
		if got.Kind != "none" {
			t.Fatalf("Kind = %q, want \"none\" — a degraded capture cannot be a reference", got.Kind)
		}
		if len(got.History) != 1 {
			t.Fatalf("History = %s, want exactly the one run it tried", historyString(got))
		}
		c := got.History[0]
		if c.Eligible || !strings.Contains(c.Reason, "degraded") {
			t.Fatalf("candidate = %+v, want ineligible with a reason naming the degraded verdict", c)
		}
		if !strings.Contains(c.Detail, "the proxy recorded no calls") {
			t.Fatalf("Detail = %q, want the capture summary carried through", c.Detail)
		}
	})

	t.Run("a manifest with no capture block at all", func(t *testing.T) {
		cwd := t.TempDir()
		root := runs.RunsRoot(cwd)
		p, err := runs.Create(root, "web", "checkout", "20260821T100000Z-aaa1111")
		if err != nil {
			t.Fatal(err)
		}
		// Hand-written on purpose: runs.WriteManifest REFUSES to emit a
		// manifest with an empty capture status, so a pre-verdict manifest
		// can only arrive from an older build or a hand-edited (committed)
		// bundle. That is exactly the input this branch exists for.
		raw := map[string]any{
			"schema": runs.Schema, "app": "web", "flow": "checkout",
			"runId": "20260821T100000Z-aaa1111",
			"git":   map[string]any{"sha": "deadbee", "dirty": false},
			"wire":  map[string]any{"calls": 1, "recorded": true},
		}
		b, _ := json.MarshalIndent(raw, "", "  ")
		if err := os.WriteFile(p.ManifestPath, b, 0o644); err != nil {
			t.Fatal(err)
		}

		got := Resolve(cwd, root, "web", "checkout")
		if got.Kind != "none" {
			t.Fatalf("Kind = %q, want \"none\" — an unassessed capture must never rank as ok", got.Kind)
		}
		if len(got.History) != 1 || got.History[0].Eligible {
			t.Fatalf("History = %s, want the run named as ineligible", historyString(got))
		}
		if !strings.Contains(got.History[0].Reason, "unknown") {
			t.Fatalf("Reason = %q, want it to say the capture verdict is unknown", got.History[0].Reason)
		}
		if !strings.Contains(got.History[0].Detail, "cannot vouch for itself") {
			t.Fatalf("Detail = %q, want it to explain that a run predating the verdict cannot vouch for itself", got.History[0].Detail)
		}
	})
}

func TestADirtyTreeRunIsIneligible(t *testing.T) {
	cwd := t.TempDir()
	root := runs.RunsRoot(cwd)
	writeRun(t, root, "web", "checkout", "20260821T100000Z-aaa1111", withDirty())

	got := Resolve(cwd, root, "web", "checkout")
	if got.Kind != "none" {
		t.Fatalf("Kind = %q, want \"none\" — a dirty tree is not reproducible from a sha", got.Kind)
	}
	if len(got.History) != 1 || got.History[0].Eligible {
		t.Fatalf("History = %s, want the dirty run named as ineligible", historyString(got))
	}
	if !strings.Contains(got.History[0].Reason, "dirty") {
		t.Fatalf("Reason = %q, want it to say the tree was dirty", got.History[0].Reason)
	}
}

func TestNoEligibleRunReportsTheCandidatesItTried(t *testing.T) {
	// Reference.History must name the runs and the reason each was
	// rejected — an empty state that says only "no reference" is useless.
	// Each of the three is rejected for a DIFFERENT reason, so a History
	// that carried one reason for all of them would not pass either.
	cwd := t.TempDir()
	root := runs.RunsRoot(cwd)
	writeRun(t, root, "web", "checkout", "20260821T100000Z-aaa1111", withDirty())
	writeRun(t, root, "web", "checkout", "20260821T110000Z-bbb2222",
		withCapture(trace.VerdictBroken, "the proxy died mid-run"))
	if _, err := runs.Create(root, "web", "checkout", "20260821T120000Z-ccc3333"); err != nil {
		t.Fatal(err) // a run directory with no manifest: it never finished
	}

	got := Resolve(cwd, root, "web", "checkout")
	if got.Kind != "none" {
		t.Fatalf("Kind = %q, want \"none\"", got.Kind)
	}
	if len(got.History) != 3 {
		t.Fatalf("History = %s, want all three candidates named — a present-but-empty History reads as \"there were no runs\"", historyString(got))
	}
	seen := map[string]string{}
	for _, c := range got.History {
		if c.Eligible {
			t.Fatalf("candidate %s is eligible, but Kind is none", c.RunID)
		}
		if c.Reason == "" {
			t.Fatalf("candidate %s has no reason", c.RunID)
		}
		seen[c.RunID] = c.Reason
	}
	for _, want := range []struct{ runID, substr string }{
		{"20260821T100000Z-aaa1111", "dirty"},
		{"20260821T110000Z-bbb2222", "broken"},
		{"20260821T120000Z-ccc3333", "manifest"},
	} {
		if !strings.Contains(seen[want.runID], want.substr) {
			t.Fatalf("History[%s] = %q, want a reason containing %q", want.runID, seen[want.runID], want.substr)
		}
	}
	// The one-line Reason must carry the same evidence, for the surfaces
	// that only render a string.
	for _, id := range []string{"20260821T120000Z-ccc3333", "20260821T110000Z-bbb2222", "20260821T100000Z-aaa1111"} {
		if !strings.Contains(got.Reason, id) {
			t.Fatalf("Reason = %q, want it to name %s", got.Reason, id)
		}
	}
}

func TestResolveWithNoRunsAtAllSaysSo(t *testing.T) {
	cwd := t.TempDir()
	got := Resolve(cwd, runs.RunsRoot(cwd), "web", "checkout")
	if got.Kind != "none" {
		t.Fatalf("Kind = %q, want \"none\"", got.Kind)
	}
	if !strings.Contains(got.Reason, "no runs captured") {
		t.Fatalf("Reason = %q, want it to distinguish \"nothing recorded\" from \"nothing eligible\"", got.Reason)
	}
	if len(got.History) != 0 {
		t.Fatalf("History = %s, want empty — there were genuinely no candidates", historyString(got))
	}
}

func TestBundleDirRejectsAnAppOrFlowThatWouldEscapeTheRefsRoot(t *testing.T) {
	cwd := t.TempDir()
	for _, c := range []struct{ app, flow string }{
		{"..", "checkout"}, {"web", ".."}, {"../../etc", "pwn"},
		{"web", "che/ckout"}, {"", "checkout"}, {"web", ""}, {".hidden", "checkout"},
	} {
		got, err := BundleDir(cwd, c.app, c.flow)
		if err == nil {
			t.Fatalf("BundleDir(%q,%q) = %q, nil — want a rejection", c.app, c.flow, got)
		}
		if got != "" {
			t.Fatalf("BundleDir(%q,%q) returned %q alongside its error — a rejected constructor must return no path", c.app, c.flow, got)
		}
	}
	good, err := BundleDir(cwd, "web", "checkout")
	if err != nil {
		t.Fatalf("BundleDir(web,checkout): %v", err)
	}
	if want := filepath.Join(runs.RefsRoot(cwd), "web", "checkout", runs.RefRunID); good != want {
		t.Fatalf("BundleDir = %q, want %q", good, want)
	}
}

func TestResolveWithAnInvalidAppOrFlowIsNoneNotAPanic(t *testing.T) {
	cwd := t.TempDir()
	got := Resolve(cwd, runs.RunsRoot(cwd), "..", "checkout")
	if got.Kind != "none" {
		t.Fatalf("Kind = %q, want \"none\"", got.Kind)
	}
	if got.Reason == "" {
		t.Fatal("Reason is empty — a rejected component must say what was wrong")
	}
}
