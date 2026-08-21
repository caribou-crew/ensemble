package pixel

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
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
