package pixel

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/config"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestIdenticalImagesDiffToZero(t *testing.T) {
	a := loadFixture(t, "identical.a.png")
	b := loadFixture(t, "identical.b.png")
	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.NumDiff != 0 {
		t.Fatalf("NumDiff = %d, want 0", res.NumDiff)
	}
	if res.DiffPct != 0 {
		t.Fatalf("DiffPct = %v, want 0", res.DiffPct)
	}
}

func TestAChangedRectProducesANonzeroDiffPct(t *testing.T) {
	a := loadFixture(t, "diff.a.png")
	b := loadFixture(t, "diff.b.png")
	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.DiffPct <= 0 {
		t.Fatalf("DiffPct = %v, want > 0", res.DiffPct)
	}
}

func TestMaskingTheChangedRectSuppressesTheDiff(t *testing.T) {
	a := loadFixture(t, "mask.a.png")
	b := loadFixture(t, "mask.b.png")
	res, _, err := Compare(a, b, Options{Masks: []Rect{{X: 20, Y: 20, Width: 10, Height: 10}}})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.DiffPct != 0 {
		t.Fatalf("DiffPct = %v, want 0 with the changed rect masked", res.DiffPct)
	}
}

func TestAnUnmaskedRectStillReportsADiff(t *testing.T) {
	a := loadFixture(t, "mask.a.png")
	b := loadFixture(t, "mask.b.png")
	// Mask somewhere that is NOT the (20,20)-(30,30) rect mask.b actually
	// changed.
	res, _, err := Compare(a, b, Options{Masks: []Rect{{X: 0, Y: 0, Width: 5, Height: 5}}})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.DiffPct <= 0 {
		t.Fatalf("DiffPct = %v, want > 0 when the real change is unmasked", res.DiffPct)
	}
}

// solidPNG encodes a w×h opaque image of one colour.
func solidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	b, err := Encode(img)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

// taller40x60 returns a 40×60 PNG whose top 40 rows are pixel-identical to
// top40x40's decoded content, and whose remaining 20 rows are a second,
// distinct solid colour — the "real" extra content a taller screenshot
// would have.
func taller40x60(t *testing.T, top color.RGBA, extra color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 60))
	for y := 0; y < 60; y++ {
		c := top
		if y >= 40 {
			c = extra
		}
		for x := 0; x < 40; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	b, err := Encode(img)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

func TestDimensionMismatchIsReportedNotThrown(t *testing.T) {
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	a := solidPNG(t, 40, 40, base)
	b := taller40x60(t, base, color.RGBA{R: 200, G: 200, B: 200, A: 255})

	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare returned an error instead of reporting the mismatch: %v", err)
	}
	if !res.Mismatch {
		t.Fatal("Mismatch = false, want true for a 40x40 vs 40x60 pair")
	}
	if !res.PaddedForDiff {
		t.Fatal("PaddedForDiff = false, want true for a 40x40 vs 40x60 pair")
	}
}

// TestWidthAHeightAWidthBHeightBAreNotInterchangeable pins NEW-2: every
// existing size-mismatch fixture in this package varies only ONE dimension
// (e.g. 40x40 vs 40x60 — WidthA == WidthB, only the heights differ), so a
// mutation that swaps which image's dimensions feed WidthA/HeightA versus
// WidthB/HeightB is undetectable there: Mismatch's `!=` check is symmetric
// under that swap, and no existing assertion reads these four fields'
// literal values. Here a and b differ in BOTH dimensions, and all four
// values (30, 50, 70, 90) are pairwise distinct, so a swap is visible no
// matter which pair of fields it confuses.
func TestWidthAHeightAWidthBHeightBAreNotInterchangeable(t *testing.T) {
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	a := solidPNG(t, 30, 50, base)
	b := solidPNG(t, 70, 90, base)

	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.WidthA != 30 || res.HeightA != 50 || res.WidthB != 70 || res.HeightB != 90 {
		t.Fatalf("WidthA/HeightA/WidthB/HeightB = %d/%d/%d/%d, want 30/50/70/90 — "+
			"an A<->B field swap must be visible here", res.WidthA, res.HeightA, res.WidthB, res.HeightB)
	}
}

func TestDifferentSizesStillProduceAnOverlayAndADiff(t *testing.T) {
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	a := solidPNG(t, 40, 40, base)
	b := taller40x60(t, base, color.RGBA{R: 200, G: 200, B: 200, A: 255})

	_, images, err := Compare(a, b, Options{WantDiff: true, WantOverlay: true})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if images.Diff == nil {
		t.Fatal("Images.Diff == nil, want a diff image for a padded mismatch")
	}
	if images.Overlay == nil {
		t.Fatal("Images.Overlay == nil, want an overlay image for a padded mismatch")
	}
}

func TestIdenticallySizedScreenshotsAreNotFlaggedAsPadded(t *testing.T) {
	a := loadFixture(t, "identical.a.png")
	b := loadFixture(t, "identical.b.png")
	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.PaddedForDiff {
		t.Fatal("PaddedForDiff = true, want false for two identically sized shots")
	}
}

func TestASizeMismatchReportsOverlapMeasuredWithoutThePadding(t *testing.T) {
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	// The shared top 40x40 region is identical between a and b — only the
	// padding (b's extra 20 rows, which a has nothing to compare against)
	// differs. Overlap must report on the shared region alone.
	a := solidPNG(t, 40, 40, base)
	b := taller40x60(t, base, color.RGBA{R: 200, G: 200, B: 200, A: 255})

	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.Overlap == nil {
		t.Fatal("Overlap == nil, want a populated overlap block for a size mismatch")
	}
	if res.Overlap.PaddingPct <= 0 {
		t.Fatalf("Overlap.PaddingPct = %v, want > 0", res.Overlap.PaddingPct)
	}
	if res.Overlap.DiffPct == res.DiffPct {
		t.Fatalf("Overlap.DiffPct (%v) == Result.DiffPct (%v), want the overlap number to exclude the forced padding diff",
			res.Overlap.DiffPct, res.DiffPct)
	}
	if res.Overlap.DiffPct != 0 {
		t.Fatalf("Overlap.DiffPct = %v, want 0: the shared region is pixel-identical", res.Overlap.DiffPct)
	}
	if res.DiffPct == 0 {
		t.Fatal("Result.DiffPct = 0, want > 0: the padded region has no counterpart in a")
	}
}

// wider60x40 returns a 60×40 PNG whose left 40 columns are pixel-identical
// to a 40x40 shot, with a second, distinct solid colour filling the
// remaining 20 columns. This exercises the WIDTH axis of the union-canvas
// padding, which cropTopLeft/padToUnion handle independently from height —
// a mutation that skips the intersection crop on a height-mismatch fixture
// can slip through by stride coincidence (both images share width, so
// Match's row-major indexing happens to line up); a width mismatch does
// not have that coincidence and catches it for real.
func wider60x40(t *testing.T, left, extra color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 60, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 60; x++ {
			c := left
			if x >= 40 {
				c = extra
			}
			img.SetRGBA(x, y, c)
		}
	}
	b, err := Encode(img)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

func TestWidthMismatchOverlapExcludesPadding(t *testing.T) {
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	a := solidPNG(t, 40, 40, base)
	b := wider60x40(t, base, color.RGBA{R: 200, G: 200, B: 200, A: 255})

	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.Overlap == nil {
		t.Fatal("Overlap == nil, want a populated overlap block for a width mismatch")
	}
	if res.Overlap.DiffPct != 0 {
		t.Fatalf("Overlap.DiffPct = %v, want 0: the shared left region is pixel-identical", res.Overlap.DiffPct)
	}
	if res.DiffPct == 0 {
		t.Fatal("Result.DiffPct = 0, want > 0: the padded region has no counterpart in a")
	}
}

func TestMatchingSizesReportNoOverlapBlockAtAll(t *testing.T) {
	a := loadFixture(t, "diff.a.png")
	b := loadFixture(t, "diff.b.png")
	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.Overlap != nil {
		t.Fatalf("Overlap = %+v, want nil when sizes match", res.Overlap)
	}
}

func TestCompareRejectsNonPngInputWithANamedError(t *testing.T) {
	goodB := loadFixture(t, "identical.b.png")
	garbage := []byte("this is not a png")

	res, images, err := Compare(garbage, goodB, Options{})
	if err == nil {
		t.Fatal("Compare returned no error for non-PNG input a")
	}
	if !errors.Is(err, ErrDecodeA) {
		t.Fatalf("error = %v, want it to wrap ErrDecodeA", err)
	}
	if res != (Result{}) {
		t.Fatalf("Result = %+v, want the zero value on decode failure", res)
	}
	if images != (Images{}) {
		t.Fatalf("Images = %+v, want the zero value on decode failure", images)
	}

	goodA := loadFixture(t, "identical.a.png")
	_, _, err = Compare(goodA, garbage, Options{})
	if !errors.Is(err, ErrDecodeB) {
		t.Fatalf("error = %v, want it to wrap ErrDecodeB for a bad b", err)
	}
}

func TestDiffImageIsOnlyProducedWhenPixelsActuallyDiffer(t *testing.T) {
	a := loadFixture(t, "identical.a.png")
	b := loadFixture(t, "identical.b.png")
	res, images, err := Compare(a, b, Options{WantDiff: true, WantOverlay: true})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.NumDiff != 0 {
		t.Fatalf("NumDiff = %d, want 0 for identical images", res.NumDiff)
	}
	if images.Diff != nil {
		t.Fatal("Images.Diff != nil, want nil when NumDiff == 0")
	}
	if images.Overlay != nil {
		t.Fatal("Images.Overlay != nil, want nil when NumDiff == 0")
	}
}

// TestUnsetThresholdsDefaultToConfigDefaults pins the zero-value clause of
// Options.GateThreshold/FineThreshold: since Go cannot distinguish "the
// caller wrote 0" from "the caller left this unset", both MUST mean "use
// config.DefaultGate/DefaultFine", never a hair-trigger gate at literally
// zero threshold (which would flag any nonzero pixel delta at all).
//
// The fixture below has a small, uniform per-pixel colour delta — large
// enough that an unsubstituted zero threshold (maxDelta == 0) flags every
// pixel, but well under the default gate's cutoff (0.1 => maxDelta ~352 in
// YIQ-delta units), so a correctly defaulted Compare reports no diff.
func TestUnsetThresholdsDefaultToConfigDefaults(t *testing.T) {
	a := solidPNG(t, 10, 10, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	b := solidPNG(t, 10, 10, color.RGBA{R: 25, G: 20, B: 30, A: 255}) // subtle, uniform delta

	res, _, err := Compare(a, b, Options{}) // GateThreshold/FineThreshold left at zero value
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.NumDiff != 0 {
		t.Fatalf("NumDiff = %d, want 0: an unset threshold must default to config.DefaultGate (0.1), "+
			"not compare at a literal zero threshold", res.NumDiff)
	}
}

// TestExplicitThresholdsRouteToTheCorrectField pins F2: that a caller's
// EXPLICIT, non-default GateThreshold/FineThreshold are actually used, and
// used in the right slot (gate -> NumDiff/DiffPct, the verdict number; fine
// -> DiffPctFine, the reporting number).
//
// dr=43 (R: 10 -> 53, G/B unchanged) puts the uniform per-pixel colour delta
// at ~296 YIQ-delta units:
//   - below the DEFAULT gate's cutoff (0.1 => ~352): a caller who discards
//     the explicit 0.05 gate in favour of config.DefaultGate would report
//     NumDiff = 0 instead of the full area.
//   - above a stricter EXPLICIT gate's cutoff (0.05 => ~88): must be caught.
//   - above the DEFAULT fine's cutoff (0.05 => ~88): a caller who discards
//     the explicit 0.2 fine in favour of config.DefaultFine would report
//     DiffPctFine > 0 instead of 0.
//   - below a more lenient EXPLICIT fine's cutoff (0.2 => ~1409): must NOT
//     be caught.
//
// So gate and fine disagree in both directions at once: any mutation that
// discards the caller's values (M1), swaps which threshold feeds which
// field (M2), or reports the fine number using the gate's count (M3b) gets
// a wrong, exactly-pinned number here.
func TestExplicitThresholdsRouteToTheCorrectField(t *testing.T) {
	a := solidPNG(t, 10, 10, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	b := solidPNG(t, 10, 10, color.RGBA{R: 53, G: 20, B: 30, A: 255})

	strict, _, err := Compare(a, b, Options{GateThreshold: 0.05, FineThreshold: 0.2})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if strict.NumDiff != 100 {
		t.Fatalf("NumDiff = %d, want 100: GateThreshold: 0.05 is stricter than config.DefaultGate (0.1) "+
			"and must be the value actually used, not silently replaced by the default", strict.NumDiff)
	}
	if strict.DiffPct != 100 {
		t.Fatalf("DiffPct = %v, want 100", strict.DiffPct)
	}
	if strict.DiffPctFine != 0 {
		t.Fatalf("DiffPctFine = %v, want 0: FineThreshold: 0.2 is too lenient to catch this delta, "+
			"and must not be reported using the gate's (stricter) count", strict.DiffPctFine)
	}
	if strict.DiffPct == strict.DiffPctFine {
		t.Fatalf("DiffPct (%v) == DiffPctFine (%v), want the gate and fine numbers to differ", strict.DiffPct, strict.DiffPctFine)
	}

	// Same fixture, only the gate relaxed: proof it's the CALLER's gate
	// value controlling NumDiff, not a fixed default or the fine value.
	lenient, _, err := Compare(a, b, Options{GateThreshold: 0.3, FineThreshold: 0.2})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if lenient.NumDiff != 0 {
		t.Fatalf("NumDiff = %d with GateThreshold: 0.3, want 0: a more lenient caller gate must suppress the diff", lenient.NumDiff)
	}
	if strict.NumDiff <= lenient.NumDiff {
		t.Fatalf("NumDiff did not rise when the caller's gate got stricter: strict=%d lenient=%d", strict.NumDiff, lenient.NumDiff)
	}
}

// TestResolveRectsConvertsPctRectsToAbsolutePixels pins the core promise of
// pct masks: the SAME rect resolves to a DIFFERENT absolute rectangle
// against two different image sizes, proportionally.
//
// Height is checked with an epsilon, not exact equality: Go's untyped
// constant arithmetic (e.g. `2556 * 0.06` written directly in a `want`
// struct literal) folds at compile time using exact rational arithmetic,
// then rounds ONCE to float64 — a different rounding path than the
// runtime float64*float64 multiplication ResolveRects performs, and the
// two can differ by 1 ULP (verified: 2556*0.06 constant-folded = the
// float64 above 153.36000000000001..., while 0.06*2556.0 computed at
// runtime = 153.35999999999998...). X/Y/Width need no epsilon: they are 0
// or a multiplication by 1, both exact in float64.
func TestResolveRectsConvertsPctRectsToAbsolutePixels(t *testing.T) {
	rects := []Rect{{X: 0, Y: 0, Width: 1, Height: 0.06, Pct: true}}
	const epsilon = 0.0001
	closeEnough := func(got, want float64) bool {
		d := got - want
		return d > -epsilon && d < epsilon
	}

	iosResolved := ResolveRects(rects, 1178, 2556)
	r := iosResolved[0]
	if r.X != 0 || r.Y != 0 || r.Width != 1178 || !closeEnough(r.Height, 153.36) {
		t.Fatalf("ResolveRects for a 1178x2556 image = %+v, want X:0 Y:0 Width:1178 Height:~153.36", r)
	}

	androidResolved := ResolveRects(rects, 320, 640)
	r = androidResolved[0]
	if r.X != 0 || r.Y != 0 || r.Width != 320 || !closeEnough(r.Height, 38.4) {
		t.Fatalf("ResolveRects for a 320x640 image = %+v, want X:0 Y:0 Width:320 Height:~38.4", r)
	}
}

// TestResolveRectsLeavesAbsoluteRectsUntouched pins that a non-pct rect
// (the overwhelmingly common case) is passed through byte-for-byte,
// regardless of the image size it is resolved against.
func TestResolveRectsLeavesAbsoluteRectsUntouched(t *testing.T) {
	rects := []Rect{{X: 10, Y: 20, Width: 100, Height: 40}}
	got := ResolveRects(rects, 9999, 9999)
	if got[0] != rects[0] {
		t.Fatalf("ResolveRects(...) = %+v, want the absolute rect unchanged: %+v", got[0], rects[0])
	}
}

// TestApplyMasksResolvesPctPerImageSize is the ApplyMasks-level version of
// the promise above: the SAME masks list, applied to two DIFFERENT sized
// images (standing in for an iOS shot and an Android shot sharing one
// flow-keyed masks: entry), paints a proportionally correct band on each.
func TestApplyMasksResolvesPctPerImageSize(t *testing.T) {
	base := color.RGBA{R: 1, G: 2, B: 3, A: 255}
	tall := newRGBA(100, 200, base) // "iOS": mask should cover rows [0,20)
	short := newRGBA(100, 50, base) // "Android": mask should cover rows [0,5)
	masks := []Rect{{X: 0, Y: 0, Width: 1, Height: 0.1, Pct: true}}

	ApplyMasks(tall, masks)
	ApplyMasks(short, masks)

	black := color.RGBA{A: 255}
	if got := tall.RGBAAt(0, 19); got != black {
		t.Fatalf("tall(0,19) = %+v, want masked black (10%% of 200 = row 20 is the boundary)", got)
	}
	if got := tall.RGBAAt(0, 20); got != base {
		t.Fatalf("tall(0,20) = %+v, want untouched — the pct mask must not overshoot its own boundary", got)
	}
	if got := short.RGBAAt(0, 4); got != black {
		t.Fatalf("short(0,4) = %+v, want masked black (10%% of 50 = row 5 is the boundary)", got)
	}
	if got := short.RGBAAt(0, 5); got != base {
		t.Fatalf("short(0,5) = %+v, want untouched — the pct mask must not overshoot its own boundary", got)
	}
}

// TestRectsFromConvertsEveryField pins F7: RectsFrom is the ONLY conversion
// between config.Rect and pixel.Rect, so a mutation that drops or
// misroutes one field (e.g. zeroing Y) silently relocates every mask.
func TestRectsFromConvertsEveryField(t *testing.T) {
	in := []config.Rect{
		{X: 1, Y: 2, Width: 3, Height: 4, Why: "ignored by RectsFrom"},
		{X: 5, Y: 6, Width: 7, Height: 8},
		{X: 0, Y: 0, Width: 1, Height: 0.06, Pct: true},
	}
	want := []Rect{
		{X: 1, Y: 2, Width: 3, Height: 4},
		{X: 5, Y: 6, Width: 7, Height: 8},
		{X: 0, Y: 0, Width: 1, Height: 0.06, Pct: true},
	}
	got := RectsFrom(in)
	if len(got) != len(want) {
		t.Fatalf("len(RectsFrom(...)) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RectsFrom(...)[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestMasksApplyInTheOriginalCoordinateSpaceEvenWhenTrimmed pins F1: mask
// rects are authored in the ORIGINAL screenshot's coordinates, and must
// still cover the right content once Trim crops each side independently.
//
// a and b share a 10px uniform border and a 20x20 base interior; the only
// difference between them is a 6x6 "widget" at (12,12)-(18,18) — squarely
// inside the interior, in ORIGINAL coordinates. The mask targets exactly
// that rect. If masks were (incorrectly) applied AFTER Trim, (12,12) in the
// TRIMMED image's coordinates corresponds to ORIGINAL (22,22) — background,
// not the widget — so the real difference would survive unmasked.
func TestMasksApplyInTheOriginalCoordinateSpaceEvenWhenTrimmed(t *testing.T) {
	border := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	widgetA := color.RGBA{R: 250, G: 0, B: 0, A: 255}
	widgetB := color.RGBA{R: 0, G: 250, B: 0, A: 255}

	build := func(widget color.RGBA) []byte {
		img := newRGBA(40, 40, border)
		for y := 10; y < 30; y++ {
			for x := 10; x < 30; x++ {
				img.SetRGBA(x, y, base)
			}
		}
		for y := 12; y < 18; y++ {
			for x := 12; x < 18; x++ {
				img.SetRGBA(x, y, widget)
			}
		}
		b, err := Encode(img)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return b
	}
	a := build(widgetA)
	b := build(widgetB)

	mask := []Rect{{X: 12, Y: 12, Width: 6, Height: 6}}

	res, _, err := Compare(a, b, Options{Trim: true, Masks: mask})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.TrimA == nil || res.TrimB == nil {
		t.Fatalf("TrimA=%v TrimB=%v, want both non-nil", res.TrimA, res.TrimB)
	}
	if res.NumDiff != 0 {
		t.Fatalf("NumDiff = %d, want 0: the masked widget is the only difference between a and b, "+
			"and the mask rect is authored in the ORIGINAL (untrimmed) coordinate space", res.NumDiff)
	}
}

// TestMaskAppliesInsideTheOverlapRegionOnASizeMismatch pins F4: Overlap is
// "the only number that means 'the content changed'" (see Overlap's doc
// comment), so a masked region inside the shared/overlap area of a
// size-mismatched pair must not count toward Overlap.NumDiff/DiffPct.
func TestMaskAppliesInsideTheOverlapRegionOnASizeMismatch(t *testing.T) {
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	pad := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	changed := color.RGBA{R: 250, G: 0, B: 0, A: 255}

	a := solidPNG(t, 40, 40, base)

	img := image.NewRGBA(image.Rect(0, 0, 40, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 40; x++ {
			c := base
			if y >= 40 {
				c = pad
			}
			img.SetRGBA(x, y, c)
		}
	}
	for y := 5; y < 15; y++ {
		for x := 5; x < 15; x++ {
			img.SetRGBA(x, y, changed)
		}
	}
	b, err := Encode(img)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	res, _, err := Compare(a, b, Options{Masks: []Rect{{X: 5, Y: 5, Width: 10, Height: 10}}})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.Overlap == nil {
		t.Fatal("Overlap == nil, want a populated overlap block for a size mismatch")
	}
	if res.Overlap.NumDiff != 0 {
		t.Fatalf("Overlap.NumDiff = %d, want 0: the changed rect inside the shared region is masked", res.Overlap.NumDiff)
	}
	if res.Overlap.DiffPct != 0 {
		t.Fatalf("Overlap.DiffPct = %v, want 0", res.Overlap.DiffPct)
	}
}

// TestOverlapWidthAndHeightAreNotInterchangeable pins F9: both existing
// size-mismatch fixtures happen to produce a SQUARE overlap rectangle
// (40x40), so a mutation that swaps Overlap.Width/Height goes undetected.
// a is 30x50, b is 60x40, so overlap = min(30,60) x min(50,40) = 30x40 —
// every field differs from every other, so a swap is visible.
func TestOverlapWidthAndHeightAreNotInterchangeable(t *testing.T) {
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	extra := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	a := solidPNG(t, 30, 50, base)

	img := image.NewRGBA(image.Rect(0, 0, 60, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 60; x++ {
			c := extra
			if x < 30 {
				c = base
			}
			img.SetRGBA(x, y, c)
		}
	}
	b, err := Encode(img)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	res, _, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.Overlap == nil {
		t.Fatal("Overlap == nil, want a populated overlap block for a size mismatch")
	}
	if res.Overlap.Width != 30 || res.Overlap.Height != 40 {
		t.Fatalf("Overlap = %+v, want Width=30 Height=40 — a width/height swap must be detectable", res.Overlap)
	}
}

// TestApplyMasksClampsToImageBounds pins F10: ApplyMasks's doc comment
// promises rects are clamped to the image ("a mask authored for a taller
// device degrades to a partial mask instead of panicking"). This exercises
// both halves: an in-bounds rect must not paint one pixel past its own
// edge, and a negative-origin, oversize rect must clamp rather than panic.
func TestApplyMasksClampsToImageBounds(t *testing.T) {
	base := color.RGBA{R: 1, G: 2, B: 3, A: 255}
	img := newRGBA(10, 10, base)
	ApplyMasks(img, []Rect{
		{X: 2, Y: 2, Width: 3, Height: 3},   // in-bounds: must cover exactly [2,5)x[2,5)
		{X: -5, Y: 6, Width: 8, Height: 20}, // negative origin, overflows both edges
	})

	black := color.RGBA{A: 255}
	for y := 2; y < 5; y++ {
		for x := 2; x < 5; x++ {
			if got := img.RGBAAt(x, y); got != black {
				t.Fatalf("(%d,%d) = %+v, want masked black", x, y, got)
			}
		}
	}
	if got := img.RGBAAt(5, 2); got != base {
		t.Fatalf("(5,2) = %+v, want untouched: the mask clamp must not extend one pixel past r.X+r.Width", got)
	}
	if got := img.RGBAAt(2, 5); got != base {
		t.Fatalf("(2,5) = %+v, want untouched: the mask clamp must not extend one pixel past r.Y+r.Height", got)
	}
	if got := img.RGBAAt(0, 9); got != black {
		t.Fatalf("(0,9) = %+v, want masked black from the negative-origin, oversize rect", got)
	}
	if got := img.RGBAAt(9, 9); got != base {
		t.Fatalf("(9,9) = %+v, want untouched: x=9 is outside the negative-origin rect's width", got)
	}
}

// TestOverlayTintsANeighbourhoodAroundAChangedPixel pins F5: the overlay
// pipeline (buildOverlayImage, dilate, boxFilter2D, blendPixel) was
// asserted only as "Images.Overlay != nil" — an overlay that draws nothing
// at all would pass. a and b differ at exactly one pixel, (10,10); the rest
// of both 40x40 images is identical.
func TestOverlayTintsANeighbourhoodAroundAChangedPixel(t *testing.T) {
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	changed := color.RGBA{R: 250, G: 0, B: 0, A: 255}
	a := newRGBA(40, 40, base)
	b := newRGBA(40, 40, base)
	b.SetRGBA(10, 10, changed)

	overlay := buildOverlayImage(a, b, 0.1, 40, 40)

	// blendPixel must actually run: the changed pixel itself must NOT equal
	// b's raw (unblended) colour there (kills M4: blendPixel call removed).
	if got := overlay.RGBAAt(10, 10); got == changed {
		t.Fatalf("overlay(10,10) = %+v, want it tinted toward magenta, not a bare copy of b", got)
	}

	// dilate must spread the tint beyond the single changed pixel: (10,12)
	// is 2px away, b's colour there is untouched `base`, and it must still
	// be tinted by the 3px dilation radius (kills M5: overlayDilatePx 3->0,
	// which would leave only the exact source pixel tinted).
	if got := overlay.RGBAAt(10, 12); got == base {
		t.Fatalf("overlay(10,12) = %+v, want it tinted by the dilated neighbourhood, not equal to b's untouched pixel", got)
	}

	// An unchanged corner, far outside both the dilation and density radii,
	// must be left exactly as b drew it.
	if got := overlay.RGBAAt(39, 39); got != base {
		t.Fatalf("overlay(39,39) = %+v, want b's untouched pixel %+v", got, base)
	}

	// The box filter's window normalisation controls how saturated the
	// tint is via the density-scaled alpha (overlayAlphaMin..Max). At
	// (10,10), the whole 7x7 dilated blob (49 "on" pixels, from a 3px
	// dilation around one point) sits inside the density filter's 21x21
	// window, giving a normalised density of 49/441 =~ 0.111 and alpha =~
	// 0.478 — blending changed=(250,0,0) toward magenta=(255,0,170) yields
	// B =~ 170*0.478 =~ 81. Without normalisation the raw (un-divided) sum
	// is 49, clamped to density=1, forcing the MAX alpha (0.70) and
	// B =~ 170*0.70 =~ 119 (kills M6: box-filter window normalisation
	// deleted).
	if b := overlay.RGBAAt(10, 10).B; b >= 100 {
		t.Fatalf("overlay(10,10).B = %d, want < 100: the density-scaled alpha must not be saturated to the max "+
			"by an un-normalised box filter", b)
	}
}

// antialiasedFixture builds a hard black|grey-edge|white image, 9x7, where
// the single-column edge between the black and white blocks is an
// intermediate grey — the antialiasing signature pixelmatch's `antialiased`
// looks for: an extreme value among its neighbours, whose extreme neighbour
// (the solid black or white block) has many identical siblings.
func antialiasedFixture(edge color.RGBA) *image.RGBA {
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	img := image.NewRGBA(image.Rect(0, 0, 9, 7))
	for y := 0; y < 7; y++ {
		for x := 0; x < 9; x++ {
			c := black
			switch {
			case x == 4:
				c = edge
			case x > 4:
				c = white
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// TestAntialiasedEdgeIsNotCountedAsADiff pins F6: every other fixture in
// this package is antialiasing-free (hard edges only), so the rejection
// branch in Match/antialiased/hasManySiblings never ran. a and b share the
// SAME hard black/white edge but a slightly different grey value on the one
// antialiased edge column — simulating subpixel AA jitter between two
// renders of the same edge — and must report zero diff at a threshold that
// would otherwise flag that column's colour delta.
func TestAntialiasedEdgeIsNotCountedAsADiff(t *testing.T) {
	a := antialiasedFixture(color.RGBA{R: 128, G: 128, B: 128, A: 255})
	b := antialiasedFixture(color.RGBA{R: 140, G: 140, B: 140, A: 255})

	// threshold 0.03 => maxDelta =~ 31.7 YIQ-delta units; the edge column's
	// grey delta (128 vs 140, a neutral shift) is =~ 72.8 — well over that
	// cutoff, so this would be flagged as a diff if it weren't recognised
	// as antialiasing.
	if diff := Match(a, b, nil, 0.03, false); diff != 0 {
		t.Fatalf("Match = %d, want 0: the differing edge column is an antialiasing artefact, not a real change", diff)
	}
}
