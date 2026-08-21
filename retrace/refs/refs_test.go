package refs

import (
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/diff/pixel"
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

// --- Step 4: accept -----------------------------------------------------

// shot builds a distinctive PNG: a `fill` background with a `mark`-coloured
// rectangle at (0,0)-(4,4). Two colours, in two regions, is what makes a
// mask assertion meaningful — an all-one-colour fixture cannot tell "masked
// the right rectangle" from "masked everything" or "masked nothing".
func shot(t *testing.T, fill, mark color.RGBA, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{fill}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, 4, 4), &image.Uniform{mark}, image.Point{}, draw.Src)
	b, err := pixel.Encode(img)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pixelAt(t *testing.T, png []byte, x, y int) color.RGBA {
	t.Helper()
	img, err := pixel.Decode(png)
	if err != nil {
		t.Fatalf("decoding a stored shot: %v", err)
	}
	i := img.PixOffset(x, y)
	return color.RGBA{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
}

var (
	white = color.RGBA{255, 255, 255, 255}
	red   = color.RGBA{255, 0, 0, 255}
	black = color.RGBA{0, 0, 0, 255}
)

// acceptFixture records one run with two checkpoints, a wire plane, a hop
// chain, a misses file and a log — so what Accept DOESN'T carry is as
// testable as what it does.
func acceptFixture(t *testing.T, cwd string, opts ...runOpt) (root, runID string) {
	t.Helper()
	root = runs.RunsRoot(cwd)
	runID = "20260821T100000Z-aaa1111"
	opts = append([]runOpt{withCheckpoint("cart", "shots/cart.png"), withCheckpoint("receipt", "shots/receipt.png")}, opts...)
	p := writeRun(t, root, "web", "checkout", runID, opts...)
	writeFile(t, filepath.Join(p.ShotsDir, "cart.png"), shot(t, white, red, 10, 10))
	writeFile(t, filepath.Join(p.ShotsDir, "receipt.png"), shot(t, white, red, 10, 10))
	writeFile(t, p.WirePath, []byte(`{"schema":"ensemble/1"}`+"\n"))
	writeFile(t, p.HopsPath, []byte(`{"schema":"ensemble/1"}`+"\n"))
	writeFile(t, p.MissesPath, []byte(`{"miss":true}`+"\n"))
	writeFile(t, p.GroupsPath, []byte(`{"name":"browse"}`+"\n"))
	writeFile(t, filepath.Join(p.RunDir, "proxy.log"), []byte("noisy\n"))
	return root, runID
}

func acceptOpts(cwd, root, runID string) AcceptOptions {
	return AcceptOptions{Cwd: cwd, RunsRoot: root, App: "web", Flow: "checkout", RunID: runID}
}

func TestAcceptWritesACompactCommittableBundle(t *testing.T) {
	// bundle dir contains manifest.json, wire.jsonl, hops.jsonl, shots/;
	// misses.jsonl and any logs are NOT carried — they are not reference
	// material. RunID in the manifest keeps the provenance of the run it
	// was promoted from, while the directory is the literal "reference".
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)

	res, err := Accept(acceptOpts(cwd, root, runID))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	want, _ := BundleDir(cwd, "web", "checkout")
	if res.Dir != want {
		t.Fatalf("Dir = %q, want %q", res.Dir, want)
	}
	if filepath.Base(res.Dir) != runs.RefRunID {
		t.Fatalf("bundle dir is %q, want the literal %q — a churning directory name makes git show a promotion as a delete plus an add", filepath.Base(res.Dir), runs.RefRunID)
	}

	// Assert over what is ON DISK, not over the list the implementation
	// chose to report — a walk covers a file added long after this test.
	var got []string
	if err := filepath.WalkDir(res.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(res.Dir, p)
		got = append(got, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	wantFiles := []string{"hops.jsonl", "manifest.json", "shots/cart.png", "shots/receipt.png", "wire.jsonl"}
	if strings.Join(got, ",") != strings.Join(wantFiles, ",") {
		t.Fatalf("bundle holds %v, want exactly %v — misses.jsonl, groups.jsonl and logs are not reference material", got, wantFiles)
	}
	sorted := append([]string(nil), res.Files...)
	sort.Strings(sorted)
	if strings.Join(sorted, ",") != strings.Join(wantFiles, ",") {
		t.Fatalf("AcceptResult.Files = %v, want it to match what landed on disk %v", res.Files, got)
	}

	if res.RunID != runID {
		t.Fatalf("AcceptResult.RunID = %q, want the source run %q", res.RunID, runID)
	}
	m, err := runs.ReadManifest(filepath.Join(res.Dir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading the bundle manifest: %v", err)
	}
	if m.RunID != runID {
		t.Fatalf("bundle manifest runId = %q, want the provenance of the run it was promoted from (%q)", m.RunID, runID)
	}
	if res.Bytes <= 0 {
		t.Fatalf("AcceptResult.Bytes = %d, want the real bundle size", res.Bytes)
	}
	if res.CaptureStatus != trace.VerdictOK {
		t.Fatalf("CaptureStatus = %q, want the promoted run's verdict carried through as a typed value", res.CaptureStatus)
	}
	// And the bundle resolves — the round trip Resolve depends on.
	if ref := Resolve(cwd, root, "web", "checkout"); ref.Kind != "bundle" {
		t.Fatalf("after Accept, Resolve = %+v, want Kind \"bundle\"", ref)
	}
}

func TestAcceptRedactsMaskedRegionsIntoTheStoredShots(t *testing.T) {
	// masks previously only gated comparison; a blessed shot once reached a
	// reference bundle with legible card data. Accept is the ONLY place
	// this can be fixed, so it re-encodes each masked shot.
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)

	o := acceptOpts(cwd, root, runID)
	// Only ONE of the two checkpoints is masked, and the mask covers only
	// part of that shot: "masked everything" and "masked nothing" both fail.
	o.MasksFor = func(checkpoint string) []pixel.Rect {
		if checkpoint == "cart" {
			return []pixel.Rect{{X: 0, Y: 0, Width: 4, Height: 4}}
		}
		return nil
	}
	res, err := Accept(o)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	masked, err := os.ReadFile(filepath.Join(res.Dir, "shots", "cart.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, masked, 1, 1); got != black {
		t.Fatalf("cart.png at (1,1) = %v, want opaque black — the masked region reached the committed bundle unredacted", got)
	}
	if got := pixelAt(t, masked, 8, 8); got != white {
		t.Fatalf("cart.png at (8,8) = %v, want the original %v — Accept redacted more than the mask", got, white)
	}
	unmasked, err := os.ReadFile(filepath.Join(res.Dir, "shots", "receipt.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, unmasked, 1, 1); got != red {
		t.Fatalf("receipt.png at (1,1) = %v, want the original %v — an unmasked checkpoint must be carried as captured", got, red)
	}
}

func TestAcceptRefusesAnUnreadableShotRatherThanPromotingItUnredacted(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	p, err := runs.PathsFor(root, "web", "checkout", runID)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(p.ShotsDir, "cart.png"), []byte("this is not a PNG"))

	o := acceptOpts(cwd, root, runID)
	o.MasksFor = func(string) []pixel.Rect { return []pixel.Rect{{X: 0, Y: 0, Width: 4, Height: 4}} }
	if _, err := Accept(o); err == nil {
		t.Fatal("Accept promoted a shot it could not decode — a mask it cannot apply must refuse, never copy the bytes through unredacted")
	}
	dir, _ := BundleDir(cwd, "web", "checkout")
	if _, err := os.Stat(filepath.Join(dir, "shots", "cart.png")); err == nil {
		t.Fatal("a refused Accept left the undecodable shot in the bundle")
	}
}

func TestAcceptReplacesRatherThanMergesTheBundle(t *testing.T) {
	// a screen deleted from the flow must not linger in the reference.
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	if _, err := Accept(acceptOpts(cwd, root, runID)); err != nil {
		t.Fatalf("first Accept: %v", err)
	}

	// A second run of the same flow, with `receipt` deleted and a new
	// checkpoint added.
	next := "20260821T110000Z-bbb2222"
	p := writeRun(t, root, "web", "checkout", next, withCheckpoint("cart", "shots/cart.png"), withCheckpoint("thanks", "shots/thanks.png"))
	writeFile(t, filepath.Join(p.ShotsDir, "cart.png"), shot(t, white, red, 10, 10))
	writeFile(t, filepath.Join(p.ShotsDir, "thanks.png"), shot(t, white, red, 10, 10))
	writeFile(t, p.WirePath, []byte(`{"schema":"ensemble/1"}`+"\n"))

	res, err := Accept(acceptOpts(cwd, root, next))
	if err != nil {
		t.Fatalf("second Accept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "shots", "receipt.png")); err == nil {
		t.Fatal("receipt.png survived a promotion that no longer captures it — the bundle merged instead of replacing")
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "shots", "thanks.png")); err != nil {
		t.Fatalf("thanks.png missing from the replaced bundle: %v", err)
	}
	// hops.jsonl existed in the first run and not the second: a stale plane
	// is the same defect as a stale screenshot.
	if _, err := os.Stat(filepath.Join(res.Dir, "hops.jsonl")); err == nil {
		t.Fatal("hops.jsonl survived from the previous promotion — the bundle merged instead of replacing")
	}
}

func TestAcceptRefusesToExceedTheSizeBudgetNamingTheOffender(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	p, err := runs.PathsFor(root, "web", "checkout", runID)
	if err != nil {
		t.Fatal(err)
	}
	// Incompressible noise, so the PNG on disk really is over budget.
	big := make([]byte, MaxBundleBytes+1)
	for i := range big {
		big[i] = byte(i * 7919 % 251)
	}
	writeFile(t, p.WirePath, big)

	_, err = Accept(acceptOpts(cwd, root, runID))
	if err == nil {
		t.Fatal("Accept exceeded MaxBundleBytes without refusing — a reference bundle is committed, so its size is a cost every clone pays forever")
	}
	msg := err.Error()
	for _, want := range []string{"wire.jsonl", "web/checkout", "MaxBundleBytes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want it to name %q", msg, want)
		}
	}
}

// TestAcceptProceedsOnASuspectCaptureCarryingTheVerdict pins the MIDDLE
// tier, and is named for it. `suspect` is a heuristic doubt — unattributed
// traffic mid-run — not a proven gap, so promotion proceeds and the caller
// warns. The tier above it (capture.Fatal) is refused instead; see
// TestAcceptRefusesAFatalCaptureUnlessForced. The two together are what
// stop "a warning in a CI log" from being mistaken for a gate.
func TestAcceptProceedsOnASuspectCaptureCarryingTheVerdict(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd, withCapture(trace.VerdictSuspect, "unattributed traffic mid-run"))

	res, err := Accept(acceptOpts(cwd, root, runID))
	if err != nil {
		t.Fatalf("Accept refused a SUSPECT capture: %v — a heuristic doubt is the human's call to make, and only capture.Fatal is gated", err)
	}
	if res.CaptureStatus != trace.VerdictSuspect {
		t.Fatalf("CaptureStatus = %q, want %q carried through as a typed value, not reconstructible only from warning text",
			res.CaptureStatus, trace.VerdictSuspect)
	}
	// The verdict must survive INTO the bundle, so every later diff banners
	// it too — a warning printed once at accept time is not a record.
	m, err := runs.ReadManifest(filepath.Join(res.Dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Capture.Status != trace.VerdictSuspect {
		t.Fatalf("bundle manifest capture status = %q, want the promoted run's own verdict %q", m.Capture.Status, trace.VerdictSuspect)
	}
}

// TestAcceptRefusesAFatalCaptureUnlessForced pins the TOP tier and both
// sides of the override. The brief's own words for this case are "that is
// how a proxy-down run becomes the source of truth" — a proxy-down run is
// degraded, i.e. capture.Fatal — so this tier gets a gate rather than a
// warning, and Force is how an operator says they meant it.
//
// Both arms are asserted because a one-armed test cannot tell "Force works"
// from "there was never a refusal": with the gate deleted the refusal arm
// dies, and with the `&& !o.Force` deleted the override arm dies.
func TestAcceptRefusesAFatalCaptureUnlessForced(t *testing.T) {
	for _, v := range []trace.Verdict{trace.VerdictDegraded, trace.VerdictBroken, trace.VerdictFailed} {
		t.Run(string(v), func(t *testing.T) {
			cwd := t.TempDir()
			root, runID := acceptFixture(t, cwd, withCapture(v, "the proxy died mid-run"))

			o := acceptOpts(cwd, root, runID)
			_, err := Accept(o)
			if err == nil {
				t.Fatalf("Accept promoted a %q capture — a run the capture machinery could not vouch for must not become the thing every later diff is judged against", v)
			}
			if !strings.Contains(err.Error(), string(v)) {
				t.Fatalf("error = %v, want it to name the verdict %q it refused on", err, v)
			}
			// The refusal must leave no bundle behind: a half-promoted
			// reference is worse than none.
			dir, derr := BundleDir(cwd, "web", "checkout")
			if derr != nil {
				t.Fatal(derr)
			}
			if _, serr := os.Stat(dir); !errors.Is(serr, fs.ErrNotExist) {
				t.Fatalf("os.Stat(%s) = %v, want the bundle never created by a refused promotion", dir, serr)
			}

			o.Force = true
			res, err := Accept(o)
			if err != nil {
				t.Fatalf("Accept with Force refused a %q capture: %v — Force exists for exactly this refusal", v, err)
			}
			if res.CaptureStatus != v {
				t.Fatalf("CaptureStatus = %q, want the forced promotion to still RECORD the verdict %q it was forced past", res.CaptureStatus, v)
			}
		})
	}
}

// TestForceDoesNotOverrideTheOtherRefusals is the negative half of the
// ruling: Force gates capture.Fatal and NOTHING else. Without this, Force
// would drift into a general "do it anyway", which is how a size budget
// stops being a budget — and how a redaction that cannot be proven to have
// happened becomes optional.
func TestForceDoesNotOverrideTheOtherRefusals(t *testing.T) {
	t.Run("the size budget", func(t *testing.T) {
		cwd := t.TempDir()
		root, runID := acceptFixture(t, cwd, withCheckpoint("huge", "huge.png"))
		p, err := runs.PathsFor(root, "web", "checkout", runID)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(p.RunDir, "shots", "huge.png"), make([]byte, MaxBundleBytes+1))

		o := acceptOpts(cwd, root, runID)
		o.Force = true
		if _, err := Accept(o); err == nil {
			t.Fatal("Force pushed a bundle past MaxBundleBytes — over-budget is a fix-your-flow signal, and forcing it moves the cost to everyone who clones the repo")
		}
	})

	t.Run("an undecodable masked shot", func(t *testing.T) {
		cwd := t.TempDir()
		root, runID := acceptFixture(t, cwd, withCheckpoint("broken", "broken.png"))
		p, err := runs.PathsFor(root, "web", "checkout", runID)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(p.RunDir, "shots", "broken.png"), []byte("not a png"))

		o := acceptOpts(cwd, root, runID)
		o.Force = true
		o.MasksFor = func(string) []pixel.Rect { return []pixel.Rect{{X: 0, Y: 0, Width: 1, Height: 1}} }
		if _, err := Accept(o); err == nil {
			t.Fatal("Force promoted a shot that could not be decoded to be masked — a redaction that cannot be proven to have happened must never be forcible")
		}
	})

	t.Run("a mask that covers none of its image", func(t *testing.T) {
		cwd := t.TempDir()
		root, runID := acceptFixture(t, cwd)

		o := acceptOpts(cwd, root, runID)
		o.Force = true
		o.MasksFor = func(string) []pixel.Rect { return []pixel.Rect{{X: 0, Y: 1400, Width: 200, Height: 40}} }
		if _, err := Accept(o); err == nil {
			t.Fatal("Force promoted a shot whose mask painted zero pixels — a redaction that did not happen must never be forcible")
		}
	})

	t.Run("a mask entry that matches no checkpoint", func(t *testing.T) {
		cwd := t.TempDir()
		root, runID := acceptFixture(t, cwd)

		o := acceptOpts(cwd, root, runID)
		o.Force = true
		o.MaskedCheckpoints = []string{"chekout-summary"}
		if _, err := Accept(o); err == nil {
			t.Fatal("Force promoted a run whose configured mask matched no checkpoint — Force gates the capture verdict, nothing else")
		}
	})
}

func TestAcceptRejectsAnAppOrFlowThatWouldEscapeTheRefsRoot(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	o := acceptOpts(cwd, root, runID)
	o.Flow = ".."
	if _, err := Accept(o); err == nil {
		t.Fatal("Accept accepted a traversal flow")
	}
}

// --- Step 6: reject -----------------------------------------------------

func TestRejectEmitsASelfContainedReproBundle(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	out := filepath.Join(t.TempDir(), "repro")

	s := &diff.Summary{Schema: diff.SummarySchema, App: "web", Flow: "checkout", Verdict: "changed"}
	res, err := Reject(RejectOptions{Cwd: cwd, RunsRoot: root, App: "web", Flow: "checkout", RunID: runID, OutDir: out, Summary: s})
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if want := filepath.Join(out, "web__checkout__"+runID); res.Dir != want {
		t.Fatalf("Dir = %q, want %q", res.Dir, want)
	}
	for _, name := range []string{"manifest.json", "wire.jsonl", "hops.jsonl", "shots/cart.png", "summary.json"} {
		if _, err := os.Stat(filepath.Join(res.Dir, filepath.FromSlash(name))); err != nil {
			t.Fatalf("repro bundle is missing %s: %v", name, err)
		}
	}
	var back diff.Summary
	b, err := os.ReadFile(filepath.Join(res.Dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("summary.json does not round-trip: %v", err)
	}
	if back.Verdict != "changed" || back.Flow != "checkout" {
		t.Fatalf("summary.json = %+v, want the diff that motivated the rejection", back)
	}
	if len(res.Files) == 0 {
		t.Fatal("RejectResult.Files is empty")
	}
}

func TestRejectWithoutASummaryStillEmitsTheRun(t *testing.T) {
	// A run can be rejected before any diff exists (no reference yet). The
	// bundle is still worth having; summary.json is simply absent, which is
	// honest — an empty summary.json would assert a diff that never ran.
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	res, err := Reject(RejectOptions{Cwd: cwd, RunsRoot: root, App: "web", Flow: "checkout", RunID: runID})
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if want := filepath.Join(cwd, ".retrace", "repro"); filepath.Dir(res.Dir) != want {
		t.Fatalf("default OutDir = %q, want %q", filepath.Dir(res.Dir), want)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "summary.json")); err == nil {
		t.Fatal("summary.json exists for a rejection with no diff — an empty summary asserts a comparison that never happened")
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "manifest.json")); err != nil {
		t.Fatalf("repro bundle is missing manifest.json: %v", err)
	}
}

// TestRejectCarriesTheMissesFileAcceptDrops — the two bundles have opposite
// purposes and must not converge on one file list: a repro bundle is for
// debugging, so replay misses are exactly what someone needs.
func TestRejectCarriesTheMissesFileAcceptDrops(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	res, err := Reject(RejectOptions{Cwd: cwd, RunsRoot: root, App: "web", Flow: "checkout", RunID: runID})
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "misses.jsonl")); err != nil {
		t.Fatalf("repro bundle is missing misses.jsonl: %v", err)
	}
}

// TestACorruptCommittedBundleIsRefusedNotSilentlySkipped — a bundle is a
// committed, hand-editable artifact, so "present but unreadable" is a
// reachable state. Falling back to a local run there would let a diff
// compare against something other than what is in git while reporting a
// perfectly ordinary "run", which is the same class of silent substitution
// as a zero value reading as "fine".
//
// Both arms are pinned because corruption has two shapes and the DIRECTORY
// is the boundary, not the manifest: a malformed manifest.json, and a
// manifest.json deleted outright by a bad merge, a partial checkout or an
// LFS smudge that never ran. The second is the likelier one — deleting a
// file is easier than corrupting one — and it is exactly the arm a
// manifest-shaped boundary lets through.
func TestACorruptCommittedBundleIsRefusedNotSilentlySkipped(t *testing.T) {
	for _, c := range []struct{ name, breakage string }{
		{"a malformed manifest", "malform"},
		{"the manifest deleted from a bundle that is otherwise intact", "delete"},
		{"the bundle path blocked by a file where a directory belongs", "notdir"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cwd := t.TempDir()
			root := runs.RunsRoot(cwd)
			writeRun(t, root, "web", "checkout", "20260821T100000Z-aaa1111") // an eligible fallback exists
			dir := writeBundle(t, cwd, "web", "checkout", "20260101T000000Z-bbb2222")
			manifest := filepath.Join(dir, "manifest.json")
			switch c.breakage {
			case "malform":
				if err := os.WriteFile(manifest, []byte(`{"schema":"retrace/0"}`), 0o644); err != nil {
					t.Fatal(err)
				}
			case "notdir":
				// A stat failure that is not ErrNotExist: something is at
				// the path and it is not a bundle. This arm exists because
				// "the error was not ErrNotExist" is otherwise an unpinned
				// branch, and an unpinned branch is where a later `else`
				// quietly becomes a fallback.
				parent := filepath.Dir(dir)
				if err := os.RemoveAll(parent); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(parent, []byte("not a directory"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := os.Remove(manifest); err != nil {
					t.Fatal(err)
				}
				// The rest of the bundle is still committed and still
				// there; that is what makes this corrupt and not absent.
				if _, err := os.Stat(dir); err != nil {
					t.Fatalf("the bundle directory must survive the removal for this arm to mean anything: %v", err)
				}
			}

			got := Resolve(cwd, root, "web", "checkout")
			if got.Kind != "none" {
				t.Fatalf("Kind = %q, want \"none\" — a corrupt bundle must not silently degrade into a local-run comparison", got.Kind)
			}
			if !strings.Contains(got.Reason, dir) {
				t.Fatalf("Reason = %q, want it to name the bundle that cannot be read", got.Reason)
			}
		})
	}
}

// TestAnAbsentBundleDirectoryStillFallsBack is the other side of the
// boundary above: "there is no bundle" is the DESIGNED path, not a
// corruption, and moving the boundary must not have turned every project
// without a committed reference into an exit-3.
func TestAnAbsentBundleDirectoryStillFallsBack(t *testing.T) {
	cwd := t.TempDir()
	root := runs.RunsRoot(cwd)
	writeRun(t, root, "web", "checkout", "20260821T100000Z-aaa1111")

	got := Resolve(cwd, root, "web", "checkout")
	if got.Kind != "run" {
		t.Fatalf("Kind = %q (reason %q), want \"run\" — no bundle directory is the designed path, not a corrupt bundle", got.Kind, got.Reason)
	}
}

// --- masks that protect nothing ----------------------------------------
//
// The three tests below and their three mirrors are one finding: a
// promotion that reports success having redacted nothing is the same defect
// as one that copies an undecodable shot through, and it reaches a
// COMMITTED bundle. Each refusal has a mirror asserting the innocent case
// still promotes, because an over-refusal here breaks every project whose
// masks were authored against a different device.

func TestAMaskThatCoversNoneOfItsImageIsRefused(t *testing.T) {
	// The rectangle is authored for a taller device and lands entirely
	// below a 10px shot. pixel.ApplyMasks CLAMPS rather than complaining,
	// so without a check the shot is decoded, re-encoded, stored and
	// reported as promoted, having been redacted in zero pixels.
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)

	o := acceptOpts(cwd, root, runID)
	o.MasksFor = func(checkpoint string) []pixel.Rect {
		if checkpoint == "cart" {
			return []pixel.Rect{{X: 0, Y: 1400, Width: 200, Height: 40}}
		}
		return nil
	}
	_, err := Accept(o)
	if err == nil {
		t.Fatal("Accept promoted a shot whose mask covered none of it — a mask applied to nothing is a mask that is not protecting anything, and the bundle is committed")
	}
	for _, want := range []string{"cart", "1400"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name the checkpoint and the rectangle (%q)", err, want)
		}
	}
	dir, _ := BundleDir(cwd, "web", "checkout")
	if _, serr := os.Stat(dir); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("a refused promotion still wrote a bundle at %s (stat err %v)", dir, serr)
	}
}

// TestAPartiallyClampedMaskStillPromotes is the over-refusal mirror. A rect
// authored on a taller device that OVERLAPS the shot covers all of the
// region that exists; refusing it would break every such project, and the
// clamp in pixel.ApplyMasks stays right for comparison either way.
func TestAPartiallyClampedMaskStillPromotes(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)

	o := acceptOpts(cwd, root, runID)
	o.MasksFor = func(checkpoint string) []pixel.Rect {
		if checkpoint == "cart" {
			// Starts inside the 10x10 shot, runs far past its edges.
			return []pixel.Rect{{X: 6, Y: 6, Width: 900, Height: 900}}
		}
		return nil
	}
	res, err := Accept(o)
	if err != nil {
		t.Fatalf("Accept refused a mask that clamps to a partial region: %v — that rect covers every pixel of the region that exists", err)
	}
	masked, err := os.ReadFile(filepath.Join(res.Dir, "shots", "cart.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, masked, 8, 8); got != black {
		t.Fatalf("cart.png at (8,8) = %v, want black — the part of the mask that IS on the image must still be applied", got)
	}
	if got := pixelAt(t, masked, 1, 1); got != red {
		t.Fatalf("cart.png at (1,1) = %v, want the original %v — the clamp must not spread the mask over the whole shot", got, red)
	}
}

func TestAMaskEntryMatchingNoCheckpointIsRefusedAndNamesWhatExists(t *testing.T) {
	// The typo route: `masks: { chekout-summary: [...] }` against a run
	// whose checkpoints are cart and receipt. MasksFor is a lookup, so it
	// returns nil for the misspelt name and the shot takes the plain-copy
	// path — success, exit 0, and the pixels the entry was written to hide
	// are now in git. Only the declared ENTRIES can catch it.
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)

	o := acceptOpts(cwd, root, runID)
	o.MasksFor = func(checkpoint string) []pixel.Rect {
		if checkpoint == "chekout-summary" {
			return []pixel.Rect{{X: 0, Y: 0, Width: 4, Height: 4}}
		}
		return nil
	}
	o.MaskedCheckpoints = []string{"chekout-summary"}
	_, err := Accept(o)
	if err == nil {
		t.Fatal("Accept promoted a run whose configured mask matched no checkpoint — that mask redacted nothing and looks exactly like one that worked")
	}
	for _, want := range []string{"chekout-summary", "cart", "receipt"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name the unmatched entry AND the checkpoints that do exist (%q), so the fix is obvious from the message", err, want)
		}
	}
	dir, _ := BundleDir(cwd, "web", "checkout")
	if _, serr := os.Stat(dir); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("a refused promotion still wrote a bundle at %s (stat err %v)", dir, serr)
	}
}

// TestMaskingOneCheckpointOfSeveralStillPromotes is the over-refusal
// mirror for the entry check: the refusal keys on the ENTRY, never on the
// checkpoint, because a flow may legitimately mask one screen of five.
func TestMaskingOneCheckpointOfSeveralStillPromotes(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)

	o := acceptOpts(cwd, root, runID)
	o.MasksFor = func(checkpoint string) []pixel.Rect {
		if checkpoint == "cart" {
			return []pixel.Rect{{X: 0, Y: 0, Width: 4, Height: 4}}
		}
		return nil
	}
	// cart is masked, receipt is not, and that is a correct config.
	o.MaskedCheckpoints = []string{"cart"}
	res, err := Accept(o)
	if err != nil {
		t.Fatalf("Accept refused a flow that masks one checkpoint of two: %v", err)
	}
	masked, err := os.ReadFile(filepath.Join(res.Dir, "shots", "cart.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, masked, 1, 1); got != black {
		t.Fatalf("cart.png at (1,1) = %v, want black", got)
	}
	unmasked, err := os.ReadFile(filepath.Join(res.Dir, "shots", "receipt.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, unmasked, 1, 1); got != red {
		t.Fatalf("receipt.png at (1,1) = %v, want the original %v — an unmasked checkpoint is carried as captured", got, red)
	}
}

// TestScopeDecidesTheVerdictForAnUnmatchedMaskEntry pins ruling R-H, which
// is a DISTINCTION and not a behaviour: the same unmatched entry, against
// the same run, is refused when it is flow-scoped and merely reported when
// it is project-wide. The two subtests below are deliberately one test over
// one fixture with ONE line different between them — the field the entry is
// spelled into — because two separate tests would each stay green if the
// two scopes were folded back into a single refusing map, which is exactly
// the implementation R-H reversed.
//
// Why the scopes differ: a flow-scoped entry can only ever apply to this
// flow, so matching nothing here means it protects nothing anywhere, ever —
// no innocent reading, so refuse. A top-level entry means "every flow", so
// one naming a screen this flow does not have is very likely doing its job
// in another flow; refusing it would reject a correct configuration, which
// is not a safer failure, just a louder one landing on people whose config
// is fine.
func TestScopeDecidesTheVerdictForAnUnmatchedMaskEntry(t *testing.T) {
	// Matches neither checkpoint the fixture records (cart, receipt), and
	// reads like a screen that plausibly lives in another flow.
	const entry = "login-form"

	// opts builds the identical promotion both subtests run: same run, same
	// checkpoints, same lookup carrying real rects for the entry. Only the
	// SCOPE the entry is declared in differs below.
	opts := func(t *testing.T) (AcceptOptions, string) {
		t.Helper()
		cwd := t.TempDir()
		root, runID := acceptFixture(t, cwd)
		o := acceptOpts(cwd, root, runID)
		o.MasksFor = func(checkpoint string) []pixel.Rect {
			if checkpoint == entry {
				return []pixel.Rect{{X: 0, Y: 0, Width: 4, Height: 4}}
			}
			return nil
		}
		return o, cwd
	}

	t.Run("project-wide: promotes, and the entry is reported as a value", func(t *testing.T) {
		o, _ := opts(t)
		o.ProjectMaskedCheckpoints = []string{entry} // <- the one line

		res, err := Accept(o)
		if err != nil {
			t.Fatalf("Accept refused a promotion over a PROJECT-WIDE mask entry that matches no checkpoint here: %v\nThat entry means \"every flow\", so matching nothing in this flow has an innocent reading — it may be masking that screen in another flow. Refusing it rejects a correct configuration.", err)
		}
		// Contents, not length: "reports something" and "reports the
		// entries that matched nothing" are different claims, and only the
		// second one is the contract.
		if got := strings.Join(res.UnmatchedMasks, ","); got != entry {
			t.Fatalf("AcceptResult.UnmatchedMasks = %v, want exactly [%q] — the report is carried as a VALUE a caller can act on, not left to a printed sentence", res.UnmatchedMasks, entry)
		}
	})

	t.Run("flow-scoped: refused", func(t *testing.T) {
		o, cwd := opts(t)
		o.MaskedCheckpoints = []string{entry} // <- the one line

		_, err := Accept(o)
		if err == nil {
			t.Fatalf("Accept promoted a run whose FLOW-SCOPED mask entry %q matched no checkpoint — an entry that can only ever apply to this flow, matching nothing in this flow, protects nothing anywhere; it redacts nothing and looks exactly like a mask that worked", entry)
		}
		dir, _ := BundleDir(cwd, "web", "checkout")
		if _, serr := os.Stat(dir); !errors.Is(serr, fs.ErrNotExist) {
			t.Fatalf("a refused promotion still wrote a bundle at %s (stat err %v)", dir, serr)
		}
	})
}

// TestAMatchedProjectMaskReportsNothingAndTheReportIsAlwaysAnArray is the
// mirror that stops "report everything" passing as "report the ones that
// matched nothing", and it pins UnmatchedMasks' documented wire contract at
// the same time: never nil, so the --json field is [] and not null. That
// contract is asserted THROUGH the marshalled JSON because that is the form
// it is about — `null` and `[]` are different answers to "which entries
// matched nothing", and only one of them survives a caller doing len().
func TestAMatchedProjectMaskReportsNothingAndTheReportIsAlwaysAnArray(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared []string
	}{
		// Every declared entry matches a checkpoint: the comparison runs
		// and finds nothing.
		{"every project-wide entry matches a checkpoint", []string{"cart", "receipt"}},
		// No configuration at all: the comparison short-circuits. Both
		// paths owe the same empty, non-nil answer.
		{"the project declares no masks at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			root, runID := acceptFixture(t, cwd)
			o := acceptOpts(cwd, root, runID)
			o.ProjectMaskedCheckpoints = tc.declared

			res, err := Accept(o)
			if err != nil {
				t.Fatalf("Accept: %v", err)
			}
			if len(res.UnmatchedMasks) != 0 {
				t.Fatalf("AcceptResult.UnmatchedMasks = %v, want empty — every declared entry names a checkpoint this run has, so reporting any of them is reporting a config that is fine", res.UnmatchedMasks)
			}
			b, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), `"unmatchedMasks":[]`) {
				t.Fatalf("--json emitted %s, want an empty ARRAY for unmatchedMasks — a nil slice marshals to null, and a consumer that iterates the field then has to special-case a second spelling of \"nothing\"", b)
			}
		})
	}
}

// TestAManifestCheckpointThatEscapesTheRunDirectoryIsRefused pins
// shotFor's filepath.Rel guard. runs.ReadManifest validates schema,
// capture status and the wire/hop counts — it does NOT validate
// Checkpoint.File, so this guard is the only thing between a manifest
// naming "../../../secret.txt" and Accept reading that file and writing it
// OUTSIDE the bundle tree (writeBundleFile joins the same traversing
// relative path onto the staging directory).
//
// The second assertion is a filesystem check, not a reading of the error:
// "it returned an error" and "it wrote nothing outside the bundle" are two
// different claims, and only the second one is the finding.
func TestAManifestCheckpointThatEscapesTheRunDirectoryIsRefused(t *testing.T) {
	cwd := t.TempDir()
	root := runs.RunsRoot(cwd)
	runID := "20260821T100000Z-aaa1111"
	p, err := runs.PathsFor(root, "web", "checkout", runID)
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(cwd, "secret.txt")
	writeFile(t, secret, []byte("-----BEGIN PRIVATE KEY-----"))
	escape, err := filepath.Rel(p.RunDir, secret)
	if err != nil {
		t.Fatal(err)
	}
	writeRun(t, root, "web", "checkout", runID, withCheckpoint("escape", filepath.ToSlash(escape)))
	before := treeOf(t, cwd)

	if _, err := Accept(acceptOpts(cwd, root, runID)); err == nil {
		t.Fatalf("Accept promoted a checkpoint whose file (%q) resolves outside the run directory", escape)
	}
	after := treeOf(t, cwd)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("a refused Accept changed the tree:\nbefore:\n%s\nafter:\n%s\n— a traversing checkpoint must not be read out of the run directory and written anywhere", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

// treeOf lists every path under root, so "nothing was written outside the
// bundle" is asserted over the whole tree rather than over the handful of
// destinations the test author thought of.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}
