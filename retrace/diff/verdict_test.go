package diff

// verdict_test.go isolates the verdict rules the Task 10 review found
// unpinned: five of the brief's own bullets, three of the four Observed
// derivations, both arms of every two-sided check, and the wiring Build
// hands down to DiffHops / FindUnexpectedStatuses / OptionsFor.
//
// The organising rule here is ISOLATION, not coverage. A fixture on which
// two mechanisms independently produce the same verdict pins NEITHER — each
// hides the other's mutation, which is how Task 10 shipped a live
// exit-0-on-a-failing-run bug behind a fixture that was "obviously" testing
// it. Every fixture below is built so exactly one mechanism can move the
// value it asserts, and every two-sided rule is exercised on BOTH arms in
// the same test rather than on whichever arm was convenient.

import (
	"encoding/json"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff/pixel"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// loadConfig writes a real retrace.yaml and loads it through config.Load,
// so anything that only works after Load (a compiled path_normalize regexp,
// applyDefaults) behaves the way it does in production. A hand-built
// config.Config with an uncompiled Normalize is a test of a value no
// production path can construct: Normalize.Apply is a silent no-op unless
// Load compiled its pattern.
func loadConfig(t *testing.T, body string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "retrace.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// chainHop is one downstream leg of a hop tree.
func chainHop(seq uint64, traceID, from, to, method, path string, status int) trace.Hop {
	return trace.Hop{Seq: seq, TraceID: traceID, From: from, To: to, Method: method, Path: path, Status: status}
}

// twoRuns writes the wire (and optionally chain) files for both sides and
// returns the two RunRefs.
func twoRuns(t *testing.T, wireA, wireB, chainA, chainB []trace.Hop) (RunRef, RunRef) {
	t.Helper()
	dirA, dirB := t.TempDir(), t.TempDir()
	writeWireFile(t, dirA, wireA)
	writeWireFile(t, dirB, wireB)
	if chainA != nil {
		writeChainFile(t, dirA, chainA)
	}
	if chainB != nil {
		writeChainFile(t, dirB, chainB)
	}
	return RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())},
		RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
}

func closeTo(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

// --- C1: an unreadable checkpoint is never "ok" ---------------------------

// TestACheckpointWhosePngCannotBeReadIsUnreadableNotOk is the Global
// Constraint's third clause in its purest form: "I could not compare these
// images" must not render as "these images did not change". `ok` is worse
// than `""` — an empty verdict is rejected at the first seam it reaches,
// while `ok` sails through every one of them and out the other side as
// exit 0.
//
// Both arms: the missing PNG on side A, then on side B. A one-sided fixture
// would leave the other side's errX check deletable.
func TestACheckpointWhosePngCannotBeReadIsUnreadableNotOk(t *testing.T) {
	for _, tc := range []struct{ name, missing string }{
		{"png missing on side a", "a"},
		{"png missing on side b", "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			h := hop(1, "GET", "/cart", 200, "", `{}`)
			writeWireFile(t, dirA, []trace.Hop{h})
			writeWireFile(t, dirB, []trace.Hop{h})

			png := solidPNG(t, 40, 40, color.RGBA{R: 10, G: 20, B: 30, A: 255})
			// The manifest names shots/cart.png on BOTH sides; only one
			// side actually has the file. That is what a truncated copy, a
			// half-synced artifact download or a deleted shot looks like.
			var cpA, cpB runs.Checkpoint
			if tc.missing == "a" {
				cpA = runs.Checkpoint{Name: "cart", File: "shots/cart.png"}
				cpB = writeShot(t, dirB, "cart", png)
			} else {
				cpA = writeShot(t, dirA, "cart", png)
				cpB = runs.Checkpoint{Name: "cart", File: "shots/cart.png"}
			}

			aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
			bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})
			if len(s.Checkpoints) != 1 {
				t.Fatalf("Checkpoints = %+v, want exactly one", s.Checkpoints)
			}
			if s.Checkpoints[0].Verdict != "unreadable" {
				t.Fatalf("checkpoint verdict = %q, want \"unreadable\" — an image that could not be read must never report as a comparison that found nothing", s.Checkpoints[0].Verdict)
			}
			if s.Verdict == "pass" || ExitCode(s) == 0 {
				t.Fatalf("verdict = %q / exit %d — a run with an uncomparable checkpoint must not pass", s.Verdict, ExitCode(s))
			}
			if s.Counts.PixelChanged != 1 {
				t.Fatalf("Counts.PixelChanged = %d, want 1 — an unreadable checkpoint is not a clean one", s.Counts.PixelChanged)
			}
		})
	}
}

// --- checkpoint added / missing / mismatch / trim -------------------------

// TestACheckpointOnOnlySideAIsMissingAndOnOnlySideBIsAdded: with no
// asymmetric checkpoint set anywhere in the suite, the two verdicts were
// interchangeable — swapping them was green. The names are from the
// CANDIDATE's point of view: a checkpoint side B lost is "missing", one it
// gained is "added".
func TestACheckpointOnOnlySideAIsMissingAndOnOnlySideBIsAdded(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	png := solidPNG(t, 20, 20, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	cpOnlyA := writeShot(t, dirA, "only-on-a", png)
	cpOnlyB := writeShot(t, dirB, "only-on-b", png)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpOnlyA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpOnlyB}, nil, okCapture())}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})
	// Order matters as much as the verdicts: checkpointUnion is A's names
	// first, then any B-only name, so a reader scanning the report sees the
	// reference's checkpoints in the order the reference took them.
	if len(s.Checkpoints) != 2 || s.Checkpoints[0].Name != "only-on-a" || s.Checkpoints[1].Name != "only-on-b" {
		t.Fatalf("checkpoint order = %+v, want side A's names first then side B's", s.Checkpoints)
	}
	got := map[string]string{}
	for _, cp := range s.Checkpoints {
		got[cp.Name] = cp.Verdict
	}
	if got["only-on-a"] != "missing" {
		t.Errorf("a checkpoint present only on side A = %q, want \"missing\" (side B lost it)", got["only-on-a"])
	}
	if got["only-on-b"] != "added" {
		t.Errorf("a checkpoint present only on side B = %q, want \"added\" (side B gained it)", got["only-on-b"])
	}
	if s.Verdict != "changed" {
		t.Errorf("verdict = %q, want changed", s.Verdict)
	}
}

// TestASizeMismatchChangesTheCheckpointEvenWhenTheDiffIsUnderTheGate pins
// the `|| res.Mismatch` arm of the checkpoint verdict, which was droppable
// green. Two shots of DIFFERENT geometry are not a comparison that found a
// small difference — they are a page that changed shape, and the tiny
// DiffPct is an artefact of how little padding a one-pixel-taller shot
// needs, not evidence of similarity.
func TestASizeMismatchChangesTheCheckpointEvenWhenTheDiffIsUnderTheGate(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})

	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 200, 200, base))
	cpB := writeShot(t, dirB, "cart", solidPNG(t, 200, 201, base))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t)
	// Thresholds.Gate is BOTH the per-pixel YIQ match threshold inside
	// pixel.Compare and the percent-of-pixels checkpoint gate outside it
	// (see Build), so it cannot be cranked arbitrarily high: at >= 1 no two
	// pixels ever differ and the fixture would measure nothing. 0.6 is
	// generous as a percentage — the padded row is 0.4975% of the union —
	// while still detecting a real colour change.
	cfg.Thresholds.Gate = 0.6

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Checkpoints) != 1 {
		t.Fatalf("Checkpoints = %+v, want one", s.Checkpoints)
	}
	cp := s.Checkpoints[0]
	if !cp.Mismatch {
		t.Fatalf("test setup: Mismatch = false, want true for 200x200 vs 200x201")
	}
	if cp.DiffPct > cfg.Thresholds.Gate {
		t.Fatalf("test setup: DiffPct = %v exceeds the %v gate, so this fixture no longer isolates the Mismatch arm", cp.DiffPct, cfg.Thresholds.Gate)
	}
	if cp.Verdict != "changed" {
		t.Fatalf("checkpoint verdict = %q, want changed — a geometry mismatch is a change however small the padded diff is", cp.Verdict)
	}
}

// TestEitherSideAskingForATrimTrimsBoth pins the `cpA.Trim || cpB.Trim`
// disjunction the brief calls out explicitly. Flipping it to `&&` was
// green because no fixture asked for a trim on one side only.
//
// The fixture: identical content inside a uniform border that is a
// DIFFERENT COLOUR on each side — a chrome/scrollbar change, the exact
// thing trimming exists to discount. Untrimmed it is a 43% diff; trimmed,
// the two crops are pixel-identical.
func TestEitherSideAskingForATrimTrimsBoth(t *testing.T) {
	borderA := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	borderB := color.RGBA{R: 40, G: 40, B: 40, A: 255}
	// A two-tone centre, so TrimUniformBorder stops at the border instead
	// of eating the content.
	inkOne := color.RGBA{R: 250, A: 255}
	inkTwo := color.RGBA{B: 250, A: 255}
	framed := func(t *testing.T, border color.RGBA) []byte {
		t.Helper()
		img := newRGBA(40, 40, border)
		for y := 5; y < 35; y++ {
			for x := 5; x < 35; x++ {
				c := inkOne
				if x >= 20 {
					c = inkTwo
				}
				img.SetRGBA(x, y, c)
			}
		}
		return encodePNG(t, img)
	}

	for _, tc := range []struct {
		name         string
		trimA, trimB bool
		want         string
	}{
		{"only side a asks for a trim", true, false, "ok"},
		{"only side b asks for a trim", false, true, "ok"},
		{"neither side asks for a trim", false, false, "changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			h := hop(1, "GET", "/cart", 200, "", `{}`)
			writeWireFile(t, dirA, []trace.Hop{h})
			writeWireFile(t, dirB, []trace.Hop{h})
			cpA := writeShot(t, dirA, "cart", framed(t, borderA))
			cpB := writeShot(t, dirB, "cart", framed(t, borderB))
			cpA.Trim, cpB.Trim = tc.trimA, tc.trimB

			aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
			bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})
			if len(s.Checkpoints) != 1 {
				t.Fatalf("Checkpoints = %+v, want one", s.Checkpoints)
			}
			if got := s.Checkpoints[0].Verdict; got != tc.want {
				t.Fatalf("checkpoint verdict = %q, want %q (trimA=%v trimB=%v); diffPct=%v — with `&&` instead of `||`, one side asking would not be enough",
					got, tc.want, tc.trimA, tc.trimB, s.Checkpoints[0].DiffPct)
			}
		})
	}
}

// --- C3: the image-writing path and its exact on-disk layout --------------

// TestWantImagesWritesBothImagesAtTheExactContractPaths pins the layout
// Tasks 12, 13 and 16 read. writeCheckpointImages and writePNG were both at
// 0.0% coverage: the contract existed only as a comment.
//
// `shots/` is the SECOND component. Task 13's safeShotPath joins
// (dir, "shots", base+".png") and rejects any name containing a separator,
// so each of diff/ and overlay/ must be a directory with a shots/ child —
// the same shape a run directory already has.
func TestWantImagesWritesBothImagesAtTheExactContractPaths(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 40, 40, base))
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 40, 40, base, 5, 5, 10, 10, color.RGBA{R: 250, A: 255}))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	outDir := t.TempDir()

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef,
		Cfg: baseConfig(t), WantImages: true, OutDir: outDir})
	if len(s.Checkpoints) != 1 {
		t.Fatalf("Checkpoints = %+v, want one", s.Checkpoints)
	}
	imgs := s.Checkpoints[0].Images
	if imgs.Diff != "diff/shots/cart.png" {
		t.Errorf("Images.Diff = %q, want %q", imgs.Diff, "diff/shots/cart.png")
	}
	if imgs.Overlay != "overlay/shots/cart.png" {
		t.Errorf("Images.Overlay = %q, want %q", imgs.Overlay, "overlay/shots/cart.png")
	}
	// A and B are each capture's OWN run-dir-relative path, resolved
	// against Summary.A.Dir / Summary.B.Dir. Build does not write them and
	// must not rewrite them into the diff layout.
	if imgs.A != "shots/cart.png" || imgs.B != "shots/cart.png" {
		t.Errorf("Images.A/B = %q/%q, want the capture's own %q on both sides", imgs.A, imgs.B, "shots/cart.png")
	}
	for _, rel := range []string{imgs.Diff, imgs.Overlay} {
		full := filepath.Join(outDir, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil {
			t.Errorf("%s was not written under OutDir: %v", rel, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s was written empty", rel)
		}
	}
}

// TestWithoutWantImagesNoImagesAreWrittenOrReported is the mirror: an
// assertion that only checks the images appear passes just as well against
// a Build that ignores WantImages and always writes them.
func TestWithoutWantImagesNoImagesAreWrittenOrReported(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 40, 40, base))
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 40, 40, base, 5, 5, 10, 10, color.RGBA{R: 250, A: 255}))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	outDir := t.TempDir()

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef,
		Cfg: baseConfig(t), WantImages: false, OutDir: outDir})
	if got := s.Checkpoints[0].Images; got.Diff != "" || got.Overlay != "" {
		t.Errorf("Images = %+v, want no Diff/Overlay when WantImages is false", got)
	}
	if _, err := os.Stat(filepath.Join(outDir, "diff")); err == nil {
		t.Errorf("a diff/ directory was created under OutDir though WantImages was false")
	}
}

// --- the five verdict bullets, each isolated ------------------------------

// TestACheckpointChangeAloneMarksTheRunChanged isolates changed()'s
// checkpoint loop. Dropping that loop was green, because the only fixture
// that exercised it (TestFailOnDeterminesWhichBudgetCanFailTheBuild's cfg1)
// ALSO had a failing budget — either mechanism produced "changed" on its
// own, so neither was pinned and the suite could not say which one was
// doing the work. Here there are no configured gates at all, so the
// checkpoint loop is the only thing that can move the verdict.
func TestACheckpointChangeAloneMarksTheRunChanged(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 40, 40, base))
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 40, 40, base, 5, 5, 10, 10, color.RGBA{R: 250, A: 255}))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t) // Gates is EMPTY: no budget can reach the verdict

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Budgets) != 0 {
		t.Fatalf("test setup: Budgets = %+v, want empty so only the checkpoint loop can move the verdict", s.Budgets)
	}
	if s.Counts.WireChanged != 0 || s.Counts.WireMissing != 0 || s.Counts.WireExtra != 0 || s.Counts.WireMoved != 0 {
		t.Fatalf("test setup: the wire plane must be clean, got %+v", s.Counts)
	}
	if s.Checkpoints[0].Verdict != "changed" {
		t.Fatalf("test setup: checkpoint verdict = %q, want changed", s.Checkpoints[0].Verdict)
	}
	if s.Verdict != "changed" || ExitCode(s) != 1 {
		t.Fatalf("verdict = %q / exit %d, want changed / 1 — a changed checkpoint alone must move the run", s.Verdict, ExitCode(s))
	}
}

// TestAFailingBudgetOnANonFailOnPlaneAloneMarksTheRunChanged isolates the
// OTHER half of that masked pair — the live defect the review found. With
// `|| (len(s.Budgets) > 0 && anyFailed(s.Budgets))` dropped from Build's
// verdict switch, a gates.pixel.budget_pct BELOW thresholds.gate exits 0 on
// a run that should fail: the checkpoint verdicts "ok" (its DiffPct is
// under the pixel gate) while the configured budget it blows past goes
// unreported in the verdict.
//
// fail_on does not name pixel, so failingBudget cannot fire either. anyFailed
// is the only mechanism left.
func TestAFailingBudgetOnANonFailOnPlaneAloneMarksTheRunChanged(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 200, 200, base))
	// 25 of 40000 pixels = 0.0625%: under the 0.1% checkpoint gate, well
	// over a 0.01% CI budget. Thresholds.Gate cannot simply be raised to
	// widen that window — Build passes the same number into pixel.Compare
	// as its per-pixel YIQ threshold, and at >= 1 no two pixels ever differ.
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 200, 200, base, 10, 10, 5, 5, color.RGBA{R: 250, A: 255}))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["pixel"] = gatePct(0.01)
	cfg.FailOn = []string{"wire"} // NOT pixel: failingBudget cannot fire

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Checkpoints[0].Verdict != "ok" {
		t.Fatalf("test setup: checkpoint verdict = %q (diffPct %v), want ok — otherwise changed()'s checkpoint loop masks this rule",
			s.Checkpoints[0].Verdict, s.Checkpoints[0].DiffPct)
	}
	g := gateFor(s, "pixel")
	if g == nil || !g.Failed {
		t.Fatalf("test setup: pixel Gate = %+v, want one that failed", g)
	}
	if len(s.Gates) != 0 {
		t.Fatalf("test setup: Gates = %+v, want empty so nothing else can fail the build", s.Gates)
	}
	if s.Verdict != "changed" || ExitCode(s) != 1 {
		t.Fatalf("verdict = %q / exit %d, want changed / 1 — a failed budget on a plane fail_on does not name must still be reported as a change, not as a pass",
			s.Verdict, ExitCode(s))
	}
}

// TestEachWireSignalAloneMarksTheRunChanged covers the four arms of
// changed()'s wire clause, each in a fixture where the other three are
// zero. Dropping WireMoved, WireMissing (and, by construction, WireExtra)
// was green; the surviving assertion also pins countOf, since a countOf
// that stopped counting one of them would produce the same "pass" — so
// each case asserts the COUNT and the VERDICT separately, and a mutation of
// either mechanism fails a different assertion.
func TestEachWireSignalAloneMarksTheRunChanged(t *testing.T) {
	same := func(seq uint64, path string) trace.Hop { return hop(seq, "GET", path, 200, "", `{"v":1}`) }

	for _, tc := range []struct {
		name         string
		wireA, wireB []trace.Hop
		want         func(Counts) (int, string)
	}{
		{
			name:  "a changed body",
			wireA: []trace.Hop{hop(1, "GET", "/cart", 200, "", `{"v":1}`)},
			wireB: []trace.Hop{hop(1, "GET", "/cart", 200, "", `{"v":2}`)},
			want:  func(c Counts) (int, string) { return c.WireChanged, "WireChanged" },
		},
		{
			// Same two calls, opposite order: paired, bodies identical, so
			// "moved" is the only signal in the whole document.
			name:  "a moved call",
			wireA: []trace.Hop{same(1, "/one"), same(2, "/two")},
			wireB: []trace.Hop{same(1, "/two"), same(2, "/one")},
			want:  func(c Counts) (int, string) { return c.WireMoved, "WireMoved" },
		},
		{
			name:  "a call side B never made",
			wireA: []trace.Hop{same(1, "/one"), same(2, "/two")},
			wireB: []trace.Hop{same(1, "/one")},
			want:  func(c Counts) (int, string) { return c.WireMissing, "WireMissing" },
		},
		{
			name:  "a call only side B made",
			wireA: []trace.Hop{same(1, "/one")},
			wireB: []trace.Hop{same(1, "/one"), same(2, "/two")},
			want:  func(c Counts) (int, string) { return c.WireExtra, "WireExtra" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aRef, bRef := twoRuns(t, tc.wireA, tc.wireB, nil, nil)
			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})

			n, field := tc.want(s.Counts)
			if n == 0 {
				t.Fatalf("Counts.%s = 0, want nonzero — countOf must tally this signal; Counts = %+v", field, s.Counts)
			}
			// Every OTHER wire signal must be zero, or this fixture pins
			// nothing: a second live signal would produce the same verdict.
			others := map[string]int{
				"WireChanged": s.Counts.WireChanged, "WireMoved": s.Counts.WireMoved,
				"WireMissing": s.Counts.WireMissing, "WireExtra": s.Counts.WireExtra,
			}
			for name, v := range others {
				if name != field && v != 0 {
					t.Fatalf("test setup: Counts.%s = %d, but this fixture must isolate %s; Counts = %+v", name, v, field, s.Counts)
				}
			}
			if s.Verdict != "changed" || ExitCode(s) != 1 {
				t.Fatalf("verdict = %q / exit %d, want changed / 1 — %s alone must move the verdict", s.Verdict, ExitCode(s), field)
			}
		})
	}
}

// TestEachHopSignalAloneMarksTheRunChanged does the same for the hop plane's
// three arms. Dropping HopGone and dropping the ServiceCounts.Deviates loop
// were both green — HopGone because every fixture with a gone route also had
// a new one (they share a single `||`), Deviates because no fixture ever had
// a count deviation without a route delta beside it.
func TestEachHopSignalAloneMarksTheRunChanged(t *testing.T) {
	edge := hop(1, "GET", "/cart", 200, "", `{}`)
	call := func(seq uint64, traceID, path string) trace.Hop {
		return chainHop(seq, traceID, "client", "pricing", "GET", path, 200)
	}

	for _, tc := range []struct {
		name           string
		chainA, chainB []trace.Hop
		check          func(t *testing.T, s Summary)
	}{
		{
			// Same service, same call count, one route replaced by a
			// repeat of another: gone without new, and without deviation.
			name:   "a route side B stopped calling",
			chainA: []trace.Hop{call(1, "t1", "/price"), call(2, "t2", "/other")},
			chainB: []trace.Hop{call(1, "t1", "/price"), call(2, "t2", "/price")},
			check: func(t *testing.T, s Summary) {
				if s.Counts.HopGone != 1 {
					t.Fatalf("Counts.HopGone = %d, want 1; hops=%+v", s.Counts.HopGone, s.Hops)
				}
				if s.Counts.HopNew != 0 {
					t.Fatalf("test setup: Counts.HopNew = %d, want 0 — a new route would mask the gone one", s.Counts.HopNew)
				}
			},
		},
		{
			name:   "a route side B started calling",
			chainA: []trace.Hop{call(1, "t1", "/price"), call(2, "t2", "/price")},
			chainB: []trace.Hop{call(1, "t1", "/price"), call(2, "t2", "/other")},
			check: func(t *testing.T, s Summary) {
				if s.Counts.HopNew != 1 {
					t.Fatalf("Counts.HopNew = %d, want 1; hops=%+v", s.Counts.HopNew, s.Hops)
				}
				if s.Counts.HopGone != 0 {
					t.Fatalf("test setup: Counts.HopGone = %d, want 0", s.Counts.HopGone)
				}
			},
		},
		{
			// Identical route sets; only the CALL COUNT moved, by more
			// than DefaultCountTolerance.
			name:   "a service called far more often",
			chainA: []trace.Hop{call(1, "t1", "/price")},
			chainB: []trace.Hop{call(1, "t1", "/price"), call(2, "t2", "/price"), call(3, "t3", "/price")},
			check: func(t *testing.T, s Summary) {
				if s.Counts.HopNew != 0 || s.Counts.HopGone != 0 {
					t.Fatalf("test setup: route sets must be identical, got new=%d gone=%d", s.Counts.HopNew, s.Counts.HopGone)
				}
				deviating := 0
				for _, svc := range s.Hops.ServiceCounts {
					if svc.Deviates {
						deviating++
					}
				}
				if deviating != 1 {
					t.Fatalf("ServiceCounts = %+v, want exactly one deviating entry", s.Hops.ServiceCounts)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aRef, bRef := twoRuns(t, []trace.Hop{edge}, []trace.Hop{edge}, tc.chainA, tc.chainB)
			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})
			if s.Counts.WireChanged != 0 || s.Counts.WireMissing != 0 || s.Counts.WireExtra != 0 || s.Counts.WireMoved != 0 {
				t.Fatalf("test setup: the wire plane must be clean, got %+v", s.Counts)
			}
			tc.check(t, s)
			if s.Verdict != "changed" || ExitCode(s) != 1 {
				t.Fatalf("verdict = %q / exit %d, want changed / 1; hops = %+v", s.Verdict, ExitCode(s), s.Hops)
			}
		})
	}
}

// TestAFailedHopRequireAloneFailsTheBuild isolates the RequiredRouteFailures
// gate — an exit-2 bullet whose loop was droppable green, which also meant
// `hop_require` was not wired-pinned anywhere. Both arms: the required route
// absent (a failure) and present (no failure), so the fixture distinguishes
// the rule from "hopRequire never fires".
func TestAFailedHopRequireAloneFailsTheBuild(t *testing.T) {
	edge := hop(1, "GET", "/cart", 200, "", `{}`)
	for _, tc := range []struct {
		name        string
		chainPath   string
		wantVerdict string
		wantExit    int
	}{
		{"the required route was never called", "/cart", "failed", 2},
		{"the required route was called as configured", "/checkout", "pass", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := []trace.Hop{chainHop(1, "t1", "client", "bff", "POST", tc.chainPath, 201)}
			aRef, bRef := twoRuns(t, []trace.Hop{edge}, []trace.Hop{edge}, chain, chain)
			cfg := baseConfig(t)
			cfg.HopRequire = []config.RequiredRoute{{Method: "POST", Path: "/checkout", Status: 201}}

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
			if !s.Hops.HopRequireConfigured {
				t.Fatalf("HopRequireConfigured = false — cfg.HopRequire never reached DiffHops")
			}
			if s.Verdict != tc.wantVerdict || ExitCode(s) != tc.wantExit {
				t.Fatalf("verdict = %q / exit %d, want %q / %d; requiredFailures = %+v",
					s.Verdict, ExitCode(s), tc.wantVerdict, tc.wantExit, s.Hops.RequiredRouteFailures)
			}
			if tc.wantVerdict == "failed" {
				if len(s.Hops.RequiredRouteFailures) != 1 {
					t.Fatalf("RequiredRouteFailures = %+v, want one", s.Hops.RequiredRouteFailures)
				}
				if !containsSubstring(s.Gates, "hopRequire failed") {
					t.Fatalf("Gates = %+v, want one naming the hopRequire failure", s.Gates)
				}
			}
		})
	}
}

// TestAnOverBudgetPerfPlaneAloneFailsTheBuild isolates the perf gate — the
// other exit-2 bullet that was droppable green. Both arms: over budget
// fails, at/under budget does not, so the fixture pins the direction of the
// comparison too.
func TestAnOverBudgetPerfPlaneAloneFailsTheBuild(t *testing.T) {
	slow := trace.Hop{Seq: 1, Method: "GET", Path: "/cart", Status: 200, T: trace.Timings{DoneMs: 150}}
	for _, tc := range []struct {
		name        string
		budgetMs    float64
		wantStatus  string
		wantVerdict string
		wantExit    int
	}{
		{"measured over the flow's budget", 100, "over", "failed", 2},
		{"measured under the flow's budget", 200, "ok", "pass", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aRef, bRef := twoRuns(t, []trace.Hop{slow}, []trace.Hop{slow}, nil, nil)
			cfg := baseConfig(t)
			cfg.Flows = map[string]config.Flow{"flow": {PerfBudgetMs: tc.budgetMs}}
			// No gates.perf: this is gatesOf's own Perf.Status rule, not a
			// configured budget, and the two must stay distinguishable.

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
			if s.Perf.Status != tc.wantStatus {
				t.Fatalf("test setup: Perf = %+v, want status %q", s.Perf, tc.wantStatus)
			}
			if len(s.Budgets) != 0 {
				t.Fatalf("test setup: Budgets = %+v, want empty — a configured budget would mask the gatesOf rule", s.Budgets)
			}
			if s.Verdict != tc.wantVerdict || ExitCode(s) != tc.wantExit {
				t.Fatalf("verdict = %q / exit %d, want %q / %d; gates = %+v", s.Verdict, ExitCode(s), tc.wantVerdict, tc.wantExit, s.Gates)
			}
			if tc.wantVerdict == "failed" && !containsSubstring(s.Gates, "perf budget exceeded") {
				t.Fatalf("Gates = %+v, want one naming the perf budget", s.Gates)
			}
		})
	}
}

// TestAFatalCaptureOnEitherSideFailsTheBuildOnceCompared covers BOTH arms of
// gatesOf's capture check. Every existing fixture put the fatal side on B,
// so dropping the A-side check alone was green — the review's own example of
// structural symmetry, and the mutation the previous pass missed by dropping
// both at once and reporting a kill.
func TestAFatalCaptureOnEitherSideFailsTheBuildOnceCompared(t *testing.T) {
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	broken := runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the capture listener stopped during the run"}

	for _, tc := range []struct{ name, side string }{
		{"fatal on side a, the reference", "a"},
		{"fatal on side b, the candidate", "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			writeWireFile(t, dirA, []trace.Hop{h})
			writeWireFile(t, dirB, []trace.Hop{h})
			capA, capB := okCapture(), okCapture()
			if tc.side == "a" {
				capA = broken
			} else {
				capB = broken
			}
			aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, capA)}
			bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, capB)}

			// AllowDegraded, or quarantineCheck would refuse before gatesOf
			// ever ran — which is exactly why this rule went unpinned.
			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t), AllowDegraded: true})
			if s.Verdict != "failed" || ExitCode(s) != 2 {
				t.Fatalf("verdict = %q / exit %d, want failed / 2 — a fatal capture on side %s must fail the build once compared; gates = %+v",
					s.Verdict, ExitCode(s), tc.side, s.Gates)
			}
			want := "capture side " + tc.side + " is not trustworthy"
			if !containsSubstring(s.Gates, want) {
				t.Fatalf("Gates = %+v, want one containing %q — the reason must name the side it came from", s.Gates, want)
			}
			other := "capture side a is not trustworthy"
			if tc.side == "a" {
				other = "capture side b is not trustworthy"
			}
			if containsSubstring(s.Gates, other) {
				t.Fatalf("Gates = %+v names the WRONG side; the two arms must not be interchangeable", s.Gates)
			}
		})
	}
}

// --- the Observed derivations ---------------------------------------------

// TestPixelObservedIsTheWorstCheckpointNotTheFirstLastOrMean: every pixel
// fixture in the suite had exactly ONE checkpoint, so worst / first / last /
// mean all agreed and the fixture could not distinguish the formula it was
// testing. Three checkpoints with distinct DiffPct, arranged so the worst is
// the MIDDLE one, tells all four readings apart.
func TestPixelObservedIsTheWorstCheckpointNotTheFirstLastOrMean(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	red := color.RGBA{R: 250, A: 255}

	// Checkpoint order follows side A's manifest: small, LARGEST, medium.
	type shot struct {
		name string
		side int // rect side length; area/1600 is the DiffPct
	}
	shots := []shot{{"cart", 20}, {"hero", 40}, {"foot", 30}} // 1%, 4%, 2.25% of 200x200
	var cpsA, cpsB []runs.Checkpoint
	for _, sh := range shots {
		cpsA = append(cpsA, writeShot(t, dirA, sh.name, solidPNG(t, 200, 200, base)))
		cpsB = append(cpsB, writeShot(t, dirB, sh.name, rectPNG(t, 200, 200, base, 10, 10, sh.side, sh.side, red)))
	}

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", cpsA, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", cpsB, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["pixel"] = gatePct(50) // no gate can fail here; this test is about Observed

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Checkpoints) != 3 {
		t.Fatalf("Checkpoints = %+v, want three", s.Checkpoints)
	}
	d := []float64{s.Checkpoints[0].DiffPct, s.Checkpoints[1].DiffPct, s.Checkpoints[2].DiffPct}
	// The fixture is only load-bearing if the four candidate readings
	// genuinely disagree on it.
	if !(d[1] > d[2] && d[2] > d[0]) {
		t.Fatalf("test setup: DiffPcts = %v, want the worst in the MIDDLE and all three distinct", d)
	}
	mean := (d[0] + d[1] + d[2]) / 3
	g := gateFor(s, "pixel")
	if g == nil {
		t.Fatalf("no pixel Gate: %+v", s.Budgets)
	}
	if !closeTo(g.Observed, d[1], 1e-9) {
		t.Fatalf("pixel Gate.Observed = %v, want the WORST checkpoint %v (first %v, last %v, mean %v)", g.Observed, d[1], d[0], d[2], mean)
	}
	for label, wrong := range map[string]float64{"first": d[0], "last": d[2], "mean": mean} {
		if closeTo(g.Observed, wrong, 1e-9) {
			t.Fatalf("pixel Gate.Observed = %v is indistinguishable from the %s reading — this fixture pins nothing", g.Observed, label)
		}
	}
}

// TestHopObservedIsThePercentOfServicesThatDeviated: the hop plane's
// Observed had no test at all, and replacing the whole derivation with
// `return 0` was green. One of four services deviating is 25% — a value
// that is not the raw count (1), not the total (4), and not its complement
// (75).
func TestHopObservedIsThePercentOfServicesThatDeviated(t *testing.T) {
	edge := hop(1, "GET", "/cart", 200, "", `{}`)
	var chainA, chainB []trace.Hop
	seq := uint64(0)
	next := func(to, traceID string) trace.Hop {
		seq++
		return chainHop(seq, traceID, "client", to, "GET", "/x", 200)
	}
	for i, svc := range []string{"one", "two", "three", "four"} {
		chainA = append(chainA, next(svc, "a"+string(rune('0'+i))))
		chainB = append(chainB, next(svc, "b"+string(rune('0'+i))))
	}
	// "one" is called three MORE times on side B: 1 vs 4 is 75% drift,
	// past DefaultCountTolerance. The other three services are unchanged.
	for i := range 3 {
		chainB = append(chainB, next("one", "extra"+string(rune('0'+i))))
	}

	aRef, bRef := twoRuns(t, []trace.Hop{edge}, []trace.Hop{edge}, chainA, chainB)
	cfg := baseConfig(t)
	cfg.Gates["hop"] = gatePct(50) // 25% observed must pass a 50% budget

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Hops.ServiceCounts) != 4 {
		t.Fatalf("test setup: ServiceCounts = %+v, want four services", s.Hops.ServiceCounts)
	}
	deviating := 0
	for _, svc := range s.Hops.ServiceCounts {
		if svc.Deviates {
			deviating++
		}
	}
	if deviating != 1 {
		t.Fatalf("test setup: %d services deviate, want exactly 1 of 4; ServiceCounts = %+v", deviating, s.Hops.ServiceCounts)
	}
	g := gateFor(s, "hop")
	if g == nil {
		t.Fatalf("no hop Gate: %+v", s.Budgets)
	}
	if !closeTo(g.Observed, 25, 1e-9) {
		t.Fatalf("hop Gate.Observed = %v, want 25 (1 of 4 services deviating, as a percentage) — not the raw count 1, the total 4, or the complement 75", g.Observed)
	}
	if g.Failed {
		t.Fatalf("hop Gate = %+v, want Failed=false (25%% observed against a 50%% budget)", *g)
	}
}

// TestWireObservedIsChangedOverPairedAndNothingElse pins wire's NUMERATOR
// and DENOMINATOR, which the 1000-entry percentage fixture could not: it had
// zero missing, extra and moved, so widening the denominator to
// paired+missing+extra or the numerator to changed+moved both stayed green.
// This fixture gives all five counts distinct nonzero values.
func TestWireObservedIsChangedOverPairedAndNothingElse(t *testing.T) {
	const paired, changed, missing, extra = 10, 2, 4, 5
	var wireA, wireB []trace.Hop
	seq := uint64(0)
	add := func(dst *[]trace.Hop, path, body string) {
		seq++
		*dst = append(*dst, hop(seq, "GET", path, 200, "", body))
	}
	// Ten paired paths. Two of them (/p8, /p9) carry a changed body; two
	// others (/p1, /p2) appear in the opposite order on side B, so they
	// classify "moved" WITHOUT also being "changed".
	orderB := []int{0, 2, 1, 3, 4, 5, 6, 7, 8, 9}
	for i := range paired {
		add(&wireA, pathN("/p", i), `{"v":1}`)
	}
	for _, i := range orderB {
		body := `{"v":1}`
		if i >= paired-changed {
			body = `{"v":2}`
		}
		add(&wireB, pathN("/p", i), body)
	}
	for i := range missing {
		add(&wireA, pathN("/m", i), `{"v":1}`)
	}
	for i := range extra {
		add(&wireB, pathN("/e", i), `{"v":1}`)
	}

	aRef, bRef := twoRuns(t, wireA, wireB, nil, nil)
	cfg := baseConfig(t)
	cfg.Gates["wire"] = gatePct(90)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	c := s.Counts
	if c.WirePaired != paired || c.WireChanged != changed || c.WireMissing != missing || c.WireExtra != extra {
		t.Fatalf("test setup: Counts = %+v, want paired %d / changed %d / missing %d / extra %d", c, paired, changed, missing, extra)
	}
	if c.WireMoved == 0 {
		t.Fatalf("test setup: Counts.WireMoved = 0, so widening the numerator to changed+moved would be undetectable; Counts = %+v", c)
	}
	g := gateFor(s, "wire")
	if g == nil {
		t.Fatalf("no wire Gate: %+v", s.Budgets)
	}
	want := 100 * float64(changed) / float64(paired) // 20%
	if !closeTo(g.Observed, want, 1e-9) {
		t.Fatalf("wire Gate.Observed = %v, want %v (changed/paired)", g.Observed, want)
	}
	wrong := map[string]float64{
		"changed / (paired+missing+extra)":         100 * float64(changed) / float64(paired+missing+extra),
		"(changed+moved) / paired":                 100 * float64(changed+c.WireMoved) / float64(paired),
		"(changed+moved) / (paired+missing+extra)": 100 * float64(changed+c.WireMoved) / float64(paired+missing+extra),
	}
	for label, v := range wrong {
		if closeTo(g.Observed, v, 1e-9) {
			t.Fatalf("wire Gate.Observed = %v is indistinguishable from the %q reading — this fixture pins nothing", g.Observed, label)
		}
	}
}

// --- Build's wiring down into the engines ---------------------------------

// TestBuildWiresPathNormalizeIntoBothTheWireAndHopDiffs: dropping Normalize
// from OptionsFor and dropping it from Build's DiffHops call were BOTH
// green. Each arm is checked here, and each is checked in both directions —
// with the rule configured and without — because a fixture that only ever
// asserts "no delta" would pass just as happily against a diff engine that
// never reports one.
func TestBuildWiresPathNormalizeIntoBothTheWireAndHopDiffs(t *testing.T) {
	wireA := []trace.Hop{hop(1, "GET", "/cart/111", 200, "", `{"v":1}`)}
	wireB := []trace.Hop{hop(1, "GET", "/cart/222", 200, "", `{"v":1}`)}
	chainA := []trace.Hop{chainHop(1, "t1", "client", "bff", "GET", "/cart/111", 200)}
	chainB := []trace.Hop{chainHop(1, "t1", "client", "bff", "GET", "/cart/222", 200)}

	normalizing := "app: web\npath_normalize:\n  - pattern: '/cart/[0-9]+'\n    replacement: '/cart/{id}'\n"

	t.Run("with path_normalize the two ids are one call and one route", func(t *testing.T) {
		aRef, bRef := twoRuns(t, wireA, wireB, chainA, chainB)
		cfg := loadConfig(t, normalizing)
		opts, err := OptionsFor(cfg, aRef.Manifest, bRef.Manifest)
		if err != nil {
			t.Fatalf("OptionsFor: %v", err)
		}
		s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})
		if s.Counts.WirePaired != 1 || s.Counts.WireMissing != 0 || s.Counts.WireExtra != 0 {
			t.Fatalf("wire: Counts = %+v, want the two ids paired as one call — Options.Normalize never reached DiffWire", s.Counts)
		}
		if s.Counts.HopNew != 0 || s.Counts.HopGone != 0 {
			t.Fatalf("hops: new=%d gone=%d, want 0/0 — HopOptions.Normalize never reached DiffHops; hops = %+v", s.Counts.HopNew, s.Counts.HopGone, s.Hops)
		}
		if s.Verdict != "pass" {
			t.Fatalf("verdict = %q, want pass", s.Verdict)
		}
	})

	t.Run("without path_normalize the same two ids are separate calls and routes", func(t *testing.T) {
		aRef, bRef := twoRuns(t, wireA, wireB, chainA, chainB)
		cfg := loadConfig(t, "app: web\n")
		opts, err := OptionsFor(cfg, aRef.Manifest, bRef.Manifest)
		if err != nil {
			t.Fatalf("OptionsFor: %v", err)
		}
		s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})
		if s.Counts.WireMissing != 1 || s.Counts.WireExtra != 1 {
			t.Fatalf("wire: Counts = %+v, want one missing and one extra without normalization — otherwise the normalizing case above pins nothing", s.Counts)
		}
		if s.Counts.HopNew != 1 || s.Counts.HopGone != 1 {
			t.Fatalf("hops: new=%d gone=%d, want 1/1 without normalization; hops = %+v", s.Counts.HopNew, s.Counts.HopGone, s.Hops)
		}
	})
}

// TestBuildWiresWireIgnoreIntoTheWireDiff: dropping WireIgnore from
// OptionsFor was green. wire_ignore silences a field diff everywhere
// without needing a per-call rule, and it is the config key a human reaches
// for first.
func TestBuildWiresWireIgnoreIntoTheWireDiff(t *testing.T) {
	wireA := []trace.Hop{hop(1, "GET", "/cart", 200, "", `{"total":1}`)}
	wireB := []trace.Hop{hop(1, "GET", "/cart", 200, "", `{"total":2}`)}

	t.Run("an ignored field produces no diff entry", func(t *testing.T) {
		aRef, bRef := twoRuns(t, wireA, wireB, nil, nil)
		cfg := loadConfig(t, "app: web\nwire_ignore:\n  - path: total\n    why: volatile in this fixture\n")
		opts, err := OptionsFor(cfg, aRef.Manifest, bRef.Manifest)
		if err != nil {
			t.Fatalf("OptionsFor: %v", err)
		}
		s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})
		if s.Counts.WireChanged != 0 {
			t.Fatalf("Counts.WireChanged = %d, want 0 — Options.WireIgnore never reached DiffWire; paired = %+v", s.Counts.WireChanged, s.Wire.Paired)
		}
		if s.Verdict != "pass" {
			t.Fatalf("verdict = %q, want pass", s.Verdict)
		}
	})

	t.Run("the same field without the ignore rule does produce one", func(t *testing.T) {
		aRef, bRef := twoRuns(t, wireA, wireB, nil, nil)
		cfg := loadConfig(t, "app: web\n")
		opts, err := OptionsFor(cfg, aRef.Manifest, bRef.Manifest)
		if err != nil {
			t.Fatalf("OptionsFor: %v", err)
		}
		s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})
		if s.Counts.WireChanged != 1 {
			t.Fatalf("Counts.WireChanged = %d, want 1 without the ignore rule — otherwise the ignoring case above pins nothing", s.Counts.WireChanged)
		}
	})
}

// TestBuildWiresExpectedStatusesIntoDiffHopsErrorSignatures: Build passes
// cfg.ExpectedStatuses into HopOptions.Expected so an allowlisted status
// appearing for the first time on side B is not reported as a new error
// signature — the rule doing its job, not a regression. Dropping every
// HopOptions field at once was green.
func TestBuildWiresExpectedStatusesIntoDiffHopsErrorSignatures(t *testing.T) {
	edge := hop(1, "GET", "/cart", 200, "", `{}`)
	chainA := []trace.Hop{chainHop(1, "t1", "client", "bff", "GET", "/cart", 200)}
	chainB := []trace.Hop{
		chainHop(1, "t1", "client", "bff", "GET", "/cart", 200),
		chainHop(2, "t2", "client", "bff", "GET", "/optional", 404),
	}

	t.Run("an allowlisted 404 is not a new error signature", func(t *testing.T) {
		aRef, bRef := twoRuns(t, []trace.Hop{edge}, []trace.Hop{edge}, chainA, chainB)
		cfg := baseConfig(t)
		cfg.ExpectedStatuses = []config.StatusRule{{Path: "/optional", Status: 404}}

		s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
		if len(s.Hops.NewErrors) != 0 {
			t.Fatalf("NewErrors = %+v, want empty — HopOptions.Expected never reached DiffHops", s.Hops.NewErrors)
		}
		if len(s.UnexpectedStatuses) != 0 {
			t.Fatalf("UnexpectedStatuses = %+v, want empty — cfg.ExpectedStatuses never reached FindUnexpectedStatuses", s.UnexpectedStatuses)
		}
	})

	t.Run("the same 404 without the allowlist is both a new error and a gate", func(t *testing.T) {
		aRef, bRef := twoRuns(t, []trace.Hop{edge}, []trace.Hop{edge}, chainA, chainB)
		s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})
		if len(s.Hops.NewErrors) != 1 {
			t.Fatalf("NewErrors = %+v, want one without the allowlist — otherwise the allowlisted case above pins nothing", s.Hops.NewErrors)
		}
		if s.Verdict != "failed" {
			t.Fatalf("verdict = %q, want failed (an unexpected 404)", s.Verdict)
		}
	})
}

// TestUnexpectedStatusesComeFromTheChainExactlyOnce pins the statusHops
// choice, and the reason for it stated in Build's own comment. hops.jsonl is
// a SUPERSET of wire.jsonl (the client edge is part of the chain), so:
//
//   - using hopsB when a chain exists loses every downstream finding;
//   - concatenating hopsB with chainB double-reports every client-edge one,
//     which is the exact defect Build's comment names.
//
// Both were green. This fixture has an unexpected status on the client edge
// AND one only in the chain, so the correct source yields exactly two
// findings, the "always hopsB" mutant one, and the concatenating mutant three.
func TestUnexpectedStatusesComeFromTheChainExactlyOnce(t *testing.T) {
	edge := hop(1, "GET", "/cart", 500, "", `{}`)
	chain := []trace.Hop{
		chainHop(1, "t1", "client", "bff", "GET", "/cart", 500),
		chainHop(2, "t1", "bff", "pricing", "GET", "/price", 503),
	}
	aRef, bRef := twoRuns(t, []trace.Hop{edge}, []trace.Hop{edge}, chain, chain)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})
	if len(s.UnexpectedStatuses) != 2 {
		t.Fatalf("UnexpectedStatuses = %+v, want exactly two (the /cart 500 once, the downstream /price 503 once) — one means the chain was ignored, three means the client edge was counted twice", s.UnexpectedStatuses)
	}
	seen := map[string]int{}
	for _, f := range s.UnexpectedStatuses {
		seen[f.Path]++
	}
	if seen["/cart"] != 1 {
		t.Fatalf("the client-edge 500 appears %d times, want exactly one — hops.jsonl already contains it, so append(hopsB, chainB...) double-reports it", seen["/cart"])
	}
	if seen["/price"] != 1 {
		t.Fatalf("the downstream 503 appears %d times, want one — a statusHops fixed to hopsB never sees it at all", seen["/price"])
	}
	if s.Counts.UnexpectedStatuses != 2 {
		t.Fatalf("Counts.UnexpectedStatuses = %d, want 2", s.Counts.UnexpectedStatuses)
	}
}

// --- local fixture helpers ------------------------------------------------

func newRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func encodePNG(t *testing.T, img *image.RGBA) []byte {
	t.Helper()
	b, err := pixel.Encode(img)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pathN(prefix string, i int) string { return prefix + strconv.Itoa(i) }

func containsSubstring(hay []string, needle string) bool {
	for _, s := range hay {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// TestEachSidesOwnGroupsAreCarriedSeparately is a finding of this round's
// two-sided sweep, not of the review: OptionsFor's GroupsA and GroupsB were
// each individually droppable green, because the only groups fixture in the
// tree (TestSectionsComeFromTheManifestsGroups, in both the unit and the CLI
// suite) declared the SAME group names on both sides. That is value
// symmetry in its plainest form — with identical names, losing one side's
// list changes neither the section union nor any entry's section, since
// BuildSections falls back from GroupA to GroupB.
//
// The asymmetric case is a real one and not a contrivance: renaming a flow
// part between the reference run and the candidate is exactly what a
// refactor does, and it is the case where "which side did this name come
// from" stops being rhetorical.
func TestEachSidesOwnGroupsAreCarriedSeparately(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(seq uint64, path string, d time.Duration) trace.Hop {
		return trace.Hop{Seq: seq, Method: "GET", Path: path, Status: 200, T: trace.Timings{Start: t0.Add(d)}}
	}
	// Paired on both sides, plus one call only A made and one only B made,
	// so Wire.Missing and Wire.Extra each resolve their group from a
	// DIFFERENT side's list.
	wireA := []trace.Hop{at(1, "/session", time.Second), at(2, "/only-a", 2*time.Second), at(3, "/pay", 11*time.Second)}
	wireB := []trace.Hop{at(1, "/session", time.Second), at(2, "/pay", 11*time.Second), at(3, "/only-b", 12*time.Second)}

	dirA, dirB := t.TempDir(), t.TempDir()
	writeWireFile(t, dirA, wireA)
	writeWireFile(t, dirB, wireB)
	// The flow parts were RENAMED between the two runs.
	groupsA := []runs.Group{
		{Name: "signin", StartedAt: t0, EndedAt: t0.Add(10 * time.Second)},
		{Name: "pay", StartedAt: t0.Add(10 * time.Second), EndedAt: t0.Add(20 * time.Second)},
	}
	groupsB := []runs.Group{
		{Name: "login", StartedAt: t0, EndedAt: t0.Add(10 * time.Second)},
		{Name: "checkout", StartedAt: t0.Add(10 * time.Second), EndedAt: t0.Add(20 * time.Second)},
	}
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, groupsA, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, groupsB, okCapture())}
	cfg := baseConfig(t)
	opts, err := OptionsFor(cfg, aRef.Manifest, bRef.Manifest)
	if err != nil {
		t.Fatalf("OptionsFor: %v", err)
	}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})

	if s.Wire.Groups == nil {
		t.Fatalf("Wire.Groups is nil though both manifests declare groups")
	}
	if got := s.Wire.Groups.A; len(got) != 2 || got[0] != "signin" || got[1] != "pay" {
		t.Errorf("Wire.Groups.A = %v, want [signin pay] — side A's OWN declared names", got)
	}
	if got := s.Wire.Groups.B; len(got) != 2 || got[0] != "login" || got[1] != "checkout" {
		t.Errorf("Wire.Groups.B = %v, want [login checkout] — side B's OWN declared names", got)
	}

	var names []string
	for _, sec := range s.Sections {
		names = append(names, sec.Name)
	}
	if len(names) != 4 || names[0] != "signin" || names[1] != "pay" || names[2] != "login" || names[3] != "checkout" {
		t.Errorf("section names = %v, want [signin pay login checkout] (A's names, then B's) — losing either side's list silently halves this", names)
	}

	byPath := map[string]Entry{}
	for _, e := range s.Wire.Paired {
		byPath[e.NormalizedPath] = e
	}
	if e := byPath["/session"]; e.GroupA != "signin" || e.GroupB != "login" {
		t.Errorf("/session entry groups = %q/%q, want signin/login — each side's group comes from that side's own list", e.GroupA, e.GroupB)
	}
	if e := byPath["/pay"]; e.GroupA != "pay" || e.GroupB != "checkout" {
		t.Errorf("/pay entry groups = %q/%q, want pay/checkout", e.GroupA, e.GroupB)
	}

	// Wire.Missing is side A's call, so its group must come from side A's
	// list; Wire.Extra is side B's, from side B's. Swapping those two
	// arguments is invisible on any fixture with matching group names.
	if len(s.Wire.Missing) != 1 || s.Wire.Missing[0].Group != "signin" {
		t.Errorf("Wire.Missing = %+v, want one call in side A's group %q", s.Wire.Missing, "signin")
	}
	if len(s.Wire.Extra) != 1 || s.Wire.Extra[0].Group != "checkout" {
		t.Errorf("Wire.Extra = %+v, want one call in side B's group %q", s.Wire.Extra, "checkout")
	}
}

// --- pairs found by this round's two-sided sweep --------------------------
//
// Everything below closes an arm the review did not list. Each was found by
// mutating BOTH sides of a two-sided construct rather than whichever side a
// fixture happened to use.

// TestAChainCapturedOnOnlyOneSideIsStillDiffed pins `chainA != nil ||
// chainB != nil`. Narrowing that disjunction to EITHER side alone was green:
// every hop fixture in the tree wrote hops.jsonl on both sides, so the
// asymmetric case — a reference bundle recorded before hop capture existed,
// or a candidate run where the collector never came up — silently skipped
// DiffHops altogether and reported "no hop differences" on a run that had
// gained or lost every downstream route it has.
func TestAChainCapturedOnOnlyOneSideIsStillDiffed(t *testing.T) {
	edge := hop(1, "GET", "/cart", 200, "", `{}`)
	chain := []trace.Hop{chainHop(1, "t1", "client", "pricing", "GET", "/price", 200)}

	for _, tc := range []struct {
		name           string
		chainA, chainB []trace.Hop
		wantNew        int
		wantGone       int
	}{
		{"only the reference captured a chain", chain, nil, 0, 1},
		{"only the candidate captured a chain", nil, chain, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aRef, bRef := twoRuns(t, []trace.Hop{edge}, []trace.Hop{edge}, tc.chainA, tc.chainB)
			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})
			if len(s.Hops.ServiceCounts) == 0 {
				t.Fatalf("ServiceCounts is empty — DiffHops never ran, so a one-sided chain reports as \"no hop differences\"")
			}
			if s.Counts.HopNew != tc.wantNew || s.Counts.HopGone != tc.wantGone {
				t.Fatalf("new=%d gone=%d, want %d/%d; hops = %+v", s.Counts.HopNew, s.Counts.HopGone, tc.wantNew, tc.wantGone, s.Hops)
			}
			if s.Verdict != "changed" {
				t.Fatalf("verdict = %q, want changed", s.Verdict)
			}
		})
	}
}

// TestTrimmedRectsAreReportedPerSideInTheOriginalsCoordinates pins the
// argument ORDER of Build's pixel.Compare call. Swapping aPNG and bPNG was
// green: every trim fixture gave both sides the same border width, so
// TrimA and TrimB were equal and the swap was invisible. Trimmed.A and
// Trimmed.B are what Task 13's review UI draws crop boxes from, so getting
// them the wrong way round would put each side's box on the other side's
// screenshot.
func TestTrimmedRectsAreReportedPerSideInTheOriginalsCoordinates(t *testing.T) {
	inkOne := color.RGBA{R: 250, A: 255}
	inkTwo := color.RGBA{B: 250, A: 255}
	framed := func(border color.RGBA, inset int) []byte {
		img := newRGBA(40, 40, border)
		for y := inset; y < 40-inset; y++ {
			for x := inset; x < 40-inset; x++ {
				c := inkOne
				if x >= 20 {
					c = inkTwo
				}
				img.SetRGBA(x, y, c)
			}
		}
		return encodePNG(t, img)
	}

	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	// Deliberately DIFFERENT border widths, so the two kept rects differ.
	cpA := writeShot(t, dirA, "cart", framed(color.RGBA{R: 200, G: 200, B: 200, A: 255}, 5))
	cpB := writeShot(t, dirB, "cart", framed(color.RGBA{R: 40, G: 40, B: 40, A: 255}, 8))
	cpA.Trim, cpB.Trim = true, true

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: baseConfig(t)})
	if len(s.Checkpoints) != 1 || s.Checkpoints[0].Trimmed == nil {
		t.Fatalf("Checkpoints = %+v, want one carrying Trimmed rects", s.Checkpoints)
	}
	tr := s.Checkpoints[0].Trimmed
	if tr.A == nil || tr.B == nil {
		t.Fatalf("Trimmed = %+v, want both sides' rects", tr)
	}
	if tr.A.X != 5 || tr.A.Y != 5 || tr.A.Width != 30 || tr.A.Height != 30 {
		t.Errorf("Trimmed.A = %+v, want the 5px-border side's rect {5 5 30 30}", *tr.A)
	}
	if tr.B.X != 8 || tr.B.Y != 8 || tr.B.Width != 24 || tr.B.Height != 24 {
		t.Errorf("Trimmed.B = %+v, want the 8px-border side's rect {8 8 24 24}", *tr.B)
	}
}

// TestConformanceIsCheckedAgainstTheCandidateSideOnly pins which side's hops
// reach CheckOpenAPI. Pointing it at side A was green, because every
// conformance fixture in the tree sent the SAME calls on both sides — value
// symmetry. Conformance is a property of the CANDIDATE: side A is the
// accepted reference, and re-reporting its spec violations on every run
// would make an accepted baseline permanently red.
func TestConformanceIsCheckedAgainstTheCandidateSideOnly(t *testing.T) {
	specPath := writeSpecFile(t, `{"paths": {"/known": {"get": {"responses": {"200": {}}}}}}`)
	aRef, bRef := twoRuns(t,
		[]trace.Hop{hop(1, "GET", "/known", 200, "", `{}`)},
		[]trace.Hop{hop(1, "GET", "/only-the-candidate-calls-this", 200, "", `{}`)},
		nil, nil)
	cfg := baseConfig(t)
	cfg.Dir = ""
	cfg.OpenAPI = specPath

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Conformance) != 1 {
		t.Fatalf("Conformance = %+v, want exactly one finding — side A's call is documented, side B's is not", s.Conformance)
	}
	if s.Conformance[0].Path != "/only-the-candidate-calls-this" {
		t.Fatalf("Conformance finding names %q, want the CANDIDATE's undocumented call", s.Conformance[0].Path)
	}
}

// TestTheSummaryCarriesEachSidesOwnIdentity: swapping A and B in the Summary
// header was green — nothing asserted RunID, Kind or Dir at all. Every
// consumer downstream (the review queue, the static export, `retrace ref`)
// resolves image paths against Summary.A.Dir / Summary.B.Dir, so a swap here
// would serve each side's screenshots from the other side's directory.
func TestTheSummaryCarriesEachSidesOwnIdentity(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	aRef := RunRef{RunID: "reference-bundle", Kind: "bundle", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{RunID: "20240101T000000Z-abc1234", Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}

	s := mustBuild(t, BuildInput{App: "shop", Flow: "checkout", A: aRef, B: bRef, Cfg: baseConfig(t)})
	if s.App != "shop" || s.Flow != "checkout" || s.Schema != SummarySchema {
		t.Errorf("Summary header = app %q / flow %q / schema %q", s.App, s.Flow, s.Schema)
	}
	if s.A.RunID != "reference-bundle" || s.A.Kind != "bundle" || s.A.Dir != dirA {
		t.Errorf("Summary.A = %+v, want side A's own identity (%q, bundle, %s)", s.A, "reference-bundle", dirA)
	}
	if s.B.RunID != "20240101T000000Z-abc1234" || s.B.Kind != "run" || s.B.Dir != dirB {
		t.Errorf("Summary.B = %+v, want side B's own identity", s.B)
	}
	if s.A.Manifest.RunID != "a" || s.B.Manifest.RunID != "b" {
		t.Errorf("manifests = %q / %q, want a / b — each side carries its own", s.A.Manifest.RunID, s.B.Manifest.RunID)
	}
}

// TestAPassingBudgetOnAFailOnPlaneDoesNotFailTheBuild pins the `g.Failed`
// half of failingBudget's condition, which was droppable green: with it
// gone, merely NAMING a plane in fail_on would fail every build that
// configured a budget for it, however comfortably that budget passed. (The
// other half, `allowed[g.Plane]`, is pinned by
// TestFailOnDeterminesWhichBudgetCanFailTheBuild.)
func TestAPassingBudgetOnAFailOnPlaneDoesNotFailTheBuild(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	png := solidPNG(t, 40, 40, base)
	cpA := writeShot(t, dirA, "cart", png)
	cpB := writeShot(t, dirB, "cart", png) // pixel-identical

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["pixel"] = gatePct(2)
	cfg.FailOn = []string{"pixel"}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	g := gateFor(s, "pixel")
	if g == nil {
		t.Fatalf("test setup: no pixel Gate: %+v", s.Budgets)
	}
	if g.Failed {
		t.Fatalf("test setup: pixel Gate = %+v, want Failed=false (the shots are identical)", *g)
	}
	if s.Verdict != "pass" || ExitCode(s) != 0 {
		t.Fatalf("verdict = %q / exit %d, want pass / 0 — naming a plane in fail_on says WHICH failures may break the build, not that its presence does",
			s.Verdict, ExitCode(s))
	}
}

// TestCheckpointImagesCarryEachSidesOwnRecordedFile pins the A/B arms of
// writeCheckpointImages' CheckpointImages{A, B}. Every fixture had both
// sides recording the same "shots/<name>.png", so swapping the two was
// unobservable — a manifest is read off disk, though, and nothing obliges a
// reference bundle written by an older retrace to use the same file name for
// a checkpoint as the candidate does.
func TestCheckpointImagesCarryEachSidesOwnRecordedFile(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	// Same checkpoint NAME, different recorded FILE on each side.
	cpA := writeShotAt(t, dirA, "cart", "shots/cart.png", solidPNG(t, 40, 40, base))
	cpB := writeShotAt(t, dirB, "cart", "shots/cart-v2.png", rectPNG(t, 40, 40, base, 5, 5, 10, 10, color.RGBA{R: 250, A: 255}))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef,
		Cfg: baseConfig(t), WantImages: true, OutDir: t.TempDir()})
	imgs := s.Checkpoints[0].Images
	if imgs.A != "shots/cart.png" {
		t.Errorf("Images.A = %q, want side A's own recorded file", imgs.A)
	}
	if imgs.B != "shots/cart-v2.png" {
		t.Errorf("Images.B = %q, want side B's own recorded file", imgs.B)
	}
}

// writeShotAt writes a checkpoint PNG at an explicit run-dir-relative path,
// so a fixture can give the two sides different recorded file names for the
// same checkpoint.
func writeShotAt(t *testing.T, runDir, name, rel string, png []byte) runs.Checkpoint {
	t.Helper()
	full := filepath.Join(runDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, png, 0o644); err != nil {
		t.Fatal(err)
	}
	return runs.Checkpoint{Name: name, File: rel}
}

// --- an unmeasurable gate is not a gate that passed ------------------------
//
// The three tests below are one finding: budgetsOf rightly emits no Gate row
// for a plane it cannot measure, and every consumer read that absence as "no
// gate failed". The absence is now stated as UnmeasuredGates, and each of
// these pins a different half of what the statement is for — the verdict,
// the fail_on scoping, and the wire shape agents read.

// unmeasurablePerfGate builds the exact reproduction from the review: a
// project that gates the perf plane but never configures a
// flows.<flow>.perf_budget_ms, so DerivePerfBudget leaves BudgetMs at 0 and
// observedFor calls the plane unmeasurable. Everything else is identical on
// both sides, so no other mechanism can move the verdict.
func unmeasurablePerfGate(t *testing.T, failOn []string) Summary {
	t.Helper()
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["perf"] = gatePct(10) // gated...
	cfg.FailOn = failOn
	// ...and cfg.Flows carries no perf_budget_ms, so there is no budget for
	// a percentage to be over.
	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if gateFor(s, "perf") != nil {
		t.Fatalf("test setup: Budgets = %+v, want NO perf row — the whole finding is that the row is absent", s.Budgets)
	}
	if s.Perf.BudgetMs != 0 {
		t.Fatalf("test setup: Perf.BudgetMs = %v, want 0 (unmeasurable)", s.Perf.BudgetMs)
	}
	return s
}

// TestAGateThatCouldNotBeEvaluatedFailsTheBuildWhenFailOnNamesIt is the
// finding itself: `gates.perf.budget_pct` + `fail_on: [perf]` with no
// perf_budget_ms reported VERDICT pass / exit 0, permanently, on every run.
// An operator who configured that gate believes perf regressions break the
// build; they never can.
func TestAGateThatCouldNotBeEvaluatedFailsTheBuildWhenFailOnNamesIt(t *testing.T) {
	s := unmeasurablePerfGate(t, []string{"perf"})

	if got := s.UnmeasuredGates; len(got) != 1 || got[0] != "perf" {
		t.Fatalf("UnmeasuredGates = %v, want [perf] — the plane is gated and no row was emitted for it", got)
	}
	if len(s.Gates) != 1 || !strings.Contains(s.Gates[0], "perf") || !strings.Contains(s.Gates[0], "not evaluated") {
		t.Fatalf("Gates = %v, want one reason naming perf and saying it was not evaluated", s.Gates)
	}
	if s.Verdict != "failed" || ExitCode(s) != 2 {
		t.Fatalf("verdict = %q / exit %d, want failed / 2 — a gate the project asked to break the build, which could not be evaluated, must never read as a pass",
			s.Verdict, ExitCode(s))
	}
}

// TestAnUnevaluatedGateOutsideFailOnIsNamedButNotFatal pins the other arm of
// the fix, but the pass/0 it asserts is a KNOWN LIMITATION, not the intended
// end state — do not read it as design.
//
// The verdict is reached by two routes. failingBudget is scoped to fail_on,
// and this fix's naming/fatality mirrors that scoping exactly. anyFailed is
// NOT fail_on-scoped, and it is the route a *measured* breach on this same
// plane goes through: TestAFailingBudgetOnANonFailOnPlaneAloneMarksTheRunChanged
// pins changed/1 for a user-written gate outside fail_on that was measured
// and failed. So the fixture here — the identical gate, just never measured
// — scores strictly better (pass/0) than that measured-and-breached case
// (changed/1): a less-informed run outranks a more-informed one.
//
// This is not fixed here because it is not safely fixable by mirroring
// anyFailed alone: config.applyDefaults auto-inserts a pixel gate into
// EVERY project, and applyDefaults erases provenance while doing it —
// config.Gate is just {BudgetPct *float64}, so Build has no way to tell a
// gate the user wrote from one applyDefaults inserted. Routing unmeasured-
// and-outside-fail_on through anyFailed's mapping without that distinction
// would flip every screenshot-less default-config build from green to red.
// The real fix needs a provenance field on config.Gate; the eve of a hold
// is the wrong moment for that data-model change. Parked as F.23.
func TestAnUnevaluatedGateOutsideFailOnIsNamedButNotFatal(t *testing.T) {
	s := unmeasurablePerfGate(t, nil) // gated, but fail_on names nothing

	if got := s.UnmeasuredGates; len(got) != 1 || got[0] != "perf" {
		t.Fatalf("UnmeasuredGates = %v, want [perf] — fail_on does not decide whether the fact is REPORTED", got)
	}
	if len(s.Gates) != 0 {
		t.Fatalf("Gates = %v, want empty — fail_on names no plane, so nothing may fail the build", s.Gates)
	}
	if s.Verdict != "pass" || ExitCode(s) != 0 {
		t.Fatalf("verdict = %q / exit %d, want pass / 0 — a plane outside fail_on does not break the build whether or not it was measured",
			s.Verdict, ExitCode(s))
	}
}

// TestTheJsonSurfaceNamesAGateItCouldNotEvaluate pins the `--json` face —
// the agent contract, and the one this finding's own report shows saying
// `"budgets": []`, `"verdict":"pass"` and nothing else. `retrace diff
// --json` marshals this Summary verbatim, so the tag is the surface.
//
// It also pins the array-ness: a nil here decodes to `null`, and the TS
// mirror declares `unmeasuredGates: string[]`, so `.map` on it would throw
// in the review UI on every flow that has none.
func TestTheJsonSurfaceNamesAGateItCouldNotEvaluate(t *testing.T) {
	s := unmeasurablePerfGate(t, []string{"perf"})
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Verdict         string   `json:"verdict"`
		Budgets         []Gate   `json:"budgets"`
		UnmeasuredGates []string `json:"unmeasuredGates"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Budgets) != 0 {
		t.Fatalf("budgets = %+v, want empty — the row's absence is the premise of this test", got.Budgets)
	}
	if len(got.UnmeasuredGates) != 1 || got.UnmeasuredGates[0] != "perf" {
		t.Fatalf("unmeasuredGates = %v, want [perf] — an agent reading `budgets: []` alone cannot tell \"not gated\" from \"gated, and never evaluated\"", got.UnmeasuredGates)
	}
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %q, want failed", got.Verdict)
	}

	// The empty case ships as [] rather than null, on the same surface.
	clean := unmeasurablePerfGate(t, nil)
	clean.UnmeasuredGates = nil
	clean.ensureArrays()
	rawClean, err := json.Marshal(clean)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(rawClean), `"unmeasuredGates":[]`) {
		t.Fatalf("empty unmeasuredGates did not marshal as []: %s", rawClean)
	}
}
