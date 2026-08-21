package pixel

import (
	"image"
	"image/color"
	"testing"
)

func newRGBA(w, h int, fill color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	return img
}

func TestTrimsAUniformBorder(t *testing.T) {
	border := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	interior := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	img := newRGBA(20, 20, border)
	for y := 5; y < 15; y++ {
		for x := 5; x < 15; x++ {
			img.SetRGBA(x, y, interior)
		}
	}

	cropped, kept, ok := TrimUniformBorder(img)
	if !ok {
		t.Fatal("ok = false, want true for a uniform 5px border")
	}
	want := Rect{X: 5, Y: 5, Width: 10, Height: 10}
	if kept != want {
		t.Fatalf("kept = %+v, want %+v", kept, want)
	}
	if cropped.Bounds().Dx() != 10 || cropped.Bounds().Dy() != 10 {
		t.Fatalf("cropped size = %dx%d, want 10x10", cropped.Bounds().Dx(), cropped.Bounds().Dy())
	}
	if got := cropped.RGBAAt(0, 0); got != interior {
		t.Fatalf("cropped(0,0) = %+v, want the interior colour %+v", got, interior)
	}
}

func TestRefusesToTrimAFullyUniformImage(t *testing.T) {
	img := newRGBA(10, 10, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	cropped, kept, ok := TrimUniformBorder(img)
	if ok {
		t.Fatalf("ok = true, want false for a fully uniform image (cropped=%v kept=%+v)", cropped, kept)
	}
	if cropped != nil {
		t.Fatalf("cropped = %v, want nil on refusal", cropped)
	}
	if kept != (Rect{}) {
		t.Fatalf("kept = %+v, want the zero value on refusal", kept)
	}
}

func TestRefusesToTrimBelowTwoPixels(t *testing.T) {
	ref := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	other := color.RGBA{R: 250, G: 0, B: 0, A: 255}
	img := newRGBA(10, 10, ref)
	// A single differing column at x=4, present in every row, so no row is
	// uniform (no vertical trim happens) but the horizontal scan closes down
	// to a 1px-wide kept rect around it.
	for y := 0; y < 10; y++ {
		img.SetRGBA(4, y, other)
	}

	cropped, kept, ok := TrimUniformBorder(img)
	if ok {
		t.Fatalf("ok = true, want false when the trimmed result is under 2px wide (cropped=%v kept=%+v)", cropped, kept)
	}
	if cropped != nil {
		t.Fatalf("cropped = %v, want nil on refusal", cropped)
	}
}

func TestLeavesAnAlreadyTightImageUntouched(t *testing.T) {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			c := white
			if (x+y)%2 != 0 {
				c = black
			}
			img.SetRGBA(x, y, c)
		}
	}

	cropped, kept, ok := TrimUniformBorder(img)
	if !ok {
		t.Fatal("ok = false, want true for an already-tight image")
	}
	if cropped != img {
		t.Fatal("cropped is a different image, want the same image handed back untouched")
	}
	want := Rect{X: 0, Y: 0, Width: 8, Height: 8}
	if kept != want {
		t.Fatalf("kept = %+v, want %+v (full bounds)", kept, want)
	}
}

// TestCompareTrimsBothSidesWhenTrimIsRequested is the wiring test: it would
// have caught an implemented-but-never-called TrimUniformBorder. a and b
// share an identical interior but have DIFFERENT uniform 10px borders, so
// comparing the full 40x40 shots (Trim: false) reports a real diff, while
// comparing with Trim: true crops both borders off independently and finds
// the shared interior identical.
func TestCompareTrimsBothSidesWhenTrimIsRequested(t *testing.T) {
	interior := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	borderA := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	borderB := color.RGBA{R: 50, G: 50, B: 50, A: 255}

	build := func(border color.RGBA) []byte {
		img := newRGBA(40, 40, border)
		for y := 10; y < 30; y++ {
			for x := 10; x < 30; x++ {
				img.SetRGBA(x, y, interior)
			}
		}
		b, err := Encode(img)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return b
	}
	a := build(borderA)
	b := build(borderB)

	untrimmed, _, err := Compare(a, b, Options{Trim: false})
	if err != nil {
		t.Fatalf("Compare(Trim: false): %v", err)
	}
	if untrimmed.NumDiff == 0 {
		t.Fatal("NumDiff = 0 with Trim: false, want > 0: the differently-coloured borders should show as a diff")
	}
	if untrimmed.TrimA != nil || untrimmed.TrimB != nil {
		t.Fatalf("TrimA=%v TrimB=%v with Trim: false, want both nil", untrimmed.TrimA, untrimmed.TrimB)
	}

	trimmed, _, err := Compare(a, b, Options{Trim: true})
	if err != nil {
		t.Fatalf("Compare(Trim: true): %v", err)
	}
	if trimmed.NumDiff != 0 {
		t.Fatalf("NumDiff = %d with Trim: true, want 0: the shared interior should compare equal once the differing borders are cropped off", trimmed.NumDiff)
	}
	if trimmed.TrimA == nil || trimmed.TrimB == nil {
		t.Fatalf("TrimA=%v TrimB=%v with Trim: true, want both non-nil", trimmed.TrimA, trimmed.TrimB)
	}
	wantKept := Rect{X: 10, Y: 10, Width: 20, Height: 20}
	if *trimmed.TrimA != wantKept {
		t.Fatalf("TrimA = %+v, want %+v", *trimmed.TrimA, wantKept)
	}
	if *trimmed.TrimB != wantKept {
		t.Fatalf("TrimB = %+v, want %+v", *trimmed.TrimB, wantKept)
	}
	if trimmed.WidthA != 40 || trimmed.HeightA != 40 || trimmed.WidthB != 40 || trimmed.HeightB != 40 {
		t.Fatalf("WidthA/HeightA/WidthB/HeightB = %d/%d/%d/%d, want 40/40/40/40 (pre-trim geometry)",
			trimmed.WidthA, trimmed.HeightA, trimmed.WidthB, trimmed.HeightB)
	}
}

// TestCompareTrimReadsEachSideFromItsOwnImage is a stricter wiring check
// than TestCompareTrimsBothSidesWhenTrimIsRequested (F3): that test's
// fixtures share an IDENTICAL interior, so it cannot distinguish "B
// trimmed from B" from "B trimmed from A" — cropping either side produces
// the same content either way. Here A and B have DIFFERENT interiors, so
// reading the wrong side's TrimUniformBorder result produces a detectably
// different comparison.
func TestCompareTrimReadsEachSideFromItsOwnImage(t *testing.T) {
	borderA := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	borderB := color.RGBA{R: 50, G: 50, B: 50, A: 255}
	interiorA := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	interiorB := color.RGBA{R: 90, G: 40, B: 200, A: 255}

	build := func(border, interior color.RGBA) []byte {
		img := newRGBA(40, 40, border)
		for y := 10; y < 30; y++ {
			for x := 10; x < 30; x++ {
				img.SetRGBA(x, y, interior)
			}
		}
		b, err := Encode(img)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return b
	}
	a := build(borderA, interiorA)
	b := build(borderB, interiorB)

	trimmed, _, err := Compare(a, b, Options{Trim: true})
	if err != nil {
		t.Fatalf("Compare(Trim: true): %v", err)
	}
	if trimmed.TrimA == nil || trimmed.TrimB == nil {
		t.Fatalf("TrimA=%v TrimB=%v, want both non-nil", trimmed.TrimA, trimmed.TrimB)
	}
	if trimmed.NumDiff == 0 {
		t.Fatal("NumDiff = 0 with Trim: true, want > 0: the two sides' interiors are different colours, " +
			"so trimming must compare B's own cropped content, not a copy of A's")
	}
	if trimmed.DiffPct != 100 {
		t.Fatalf("DiffPct = %v, want 100: every pixel in the trimmed interior differs", trimmed.DiffPct)
	}
}

// TestTrimmedSizeMismatchDoesNotSetMismatch pins F8: Mismatch reports
// whether the two SHOTS differed in size, never whether independent
// trimming happened to crop them to different sizes. a and b are both
// 40x40 with pixel-identical interior content, differing only in how much
// uniform border surrounds it (10px vs 15px) — so they trim to
// DIFFERENT-size crops (20x20 vs 10x10) even though the shots themselves
// are identically sized. Comparing still requires reconciling that
// post-trim size difference (PaddedForDiff must stay true), but Mismatch
// must not follow it: these are the same-size shots Trim exists to compare
// cleanly, and Task 10 turns Mismatch straight into verdict "changed"
// regardless of DiffPct.
func TestTrimmedSizeMismatchDoesNotSetMismatch(t *testing.T) {
	border := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	interior := color.RGBA{R: 10, G: 20, B: 30, A: 255}

	build := func(borderPx int) []byte {
		img := newRGBA(40, 40, border)
		for y := borderPx; y < 40-borderPx; y++ {
			for x := borderPx; x < 40-borderPx; x++ {
				img.SetRGBA(x, y, interior)
			}
		}
		b, err := Encode(img)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return b
	}
	a := build(10) // trims to a 20x20 interior
	b := build(15) // trims to a 10x10 interior — same content, narrower border

	res, _, err := Compare(a, b, Options{Trim: true})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.WidthA != 40 || res.HeightA != 40 || res.WidthB != 40 || res.HeightB != 40 {
		t.Fatalf("WidthA/HeightA/WidthB/HeightB = %d/%d/%d/%d, want 40/40/40/40: the shots themselves are the same size",
			res.WidthA, res.HeightA, res.WidthB, res.HeightB)
	}
	if res.Mismatch {
		t.Fatal("Mismatch = true, want false: the SHOTS are the same size — only the independently-trimmed crops differ")
	}
	if !res.PaddedForDiff {
		t.Fatal("PaddedForDiff = false, want true: the trims differ in size, so Compare still had to reconcile a canvas to compare them")
	}
	if res.Overlap == nil {
		t.Fatal("Overlap == nil, want a populated overlap block: the trims differ in size")
	}
	if res.Overlap.NumDiff != 0 {
		t.Fatalf("Overlap.NumDiff = %d, want 0: the shared interior content is pixel-identical", res.Overlap.NumDiff)
	}
}

// TestRefusesToTrimAZeroSizeImage pins F11: the w==0||h==0 guard is the
// only thing standing between an exported function and an
// index-out-of-range panic in pixelAt (img.Pix[0] on an empty Pix).
func TestRefusesToTrimAZeroSizeImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 0, 0))
	cropped, kept, ok := TrimUniformBorder(img)
	if ok {
		t.Fatalf("ok = true, want false for a 0x0 image (cropped=%v kept=%+v)", cropped, kept)
	}
	if cropped != nil {
		t.Fatalf("cropped = %v, want nil on refusal", cropped)
	}
	if kept != (Rect{}) {
		t.Fatalf("kept = %+v, want the zero value on refusal", kept)
	}
}
