// Package pixel is a Go port of the pixelmatch algorithm (YIQ perceptual
// colour delta with antialiasing rejection), plus the screenshot-diff
// policy flowlens layered on top: mask rectangles, a coarse gate threshold
// and a fine reporting threshold, union-canvas padding when two shots have
// different geometry, and a magenta density overlay.
//
// It uses image/png from the standard library — the whole reason the diff
// engines are Go is that this needs no dependency.
package pixel

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"

	"github.com/caribou-crew/ensemble/retrace/config"
)

// maxYIQDelta is pixelmatch's 35215: the maximum possible YIQ difference
// between two colours, so `threshold` reads as a fraction of "as different
// as two colours can be".
const maxYIQDelta = 35215.0

// Overlay tuning, carried from the prototype: magenta at 45–70% alpha,
// dilated 3px so a one-pixel change is visible at a glance, with alpha
// scaled by how dense the changes are within a 10px radius.
var overlayColor = [3]float64{255, 0, 170}

const (
	overlayAlphaMin  = 0.45
	overlayAlphaMax  = 0.70
	overlayDilatePx  = 3
	overlayDensityPx = 10
)

// Rect is the pixel package's own rectangle type. It is deliberately
// distinct from config.Rect (which carries a Why explaining the mask) —
// the diff engine has no use for that explanation, only for where to hide
// pixels. RectsFrom is the one conversion between the two.
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Options configures one Compare call.
type Options struct {
	Masks         []Rect
	GateThreshold float64
	FineThreshold float64
	WantDiff      bool
	WantOverlay   bool
	// Trim crops a uniform border off BOTH shots before comparing, when the
	// checkpoint asked for it (runs.Checkpoint.Trim, set at capture from a
	// `<name>.trim` marker). Trimming is a COMPARE-time decision on
	// purpose: it must never alter what was captured, and putting it here
	// is what keeps retrace/capture from importing retrace/diff/pixel.
	Trim bool
}

// Overlap is embedded in diff.CheckpointVerdict and therefore reaches
// summary.json, `retrace diff --json`, the REST item response and the
// static export. It is nil on the equal-size path, which is what almost
// every unit fixture uses — so an untagged version of this type would have
// shipped `{"Width":390,…}` to a UI reading `overlap.width`, and the
// plan's own golden could not have caught it (the golden is written from
// these same Go types, so a wrong-cased key gets baked in rather than
// flagged).
type Overlap struct {
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	DiffPct     float64 `json:"diffPct"`
	DiffPctFine float64 `json:"diffPctFine"`
	NumDiff     int     `json:"numDiff"`
	PaddingPct  float64 `json:"paddingPct"`
}

// Result is Compare's report of one screenshot comparison. It is consumed
// (not embedded verbatim) by Task 10's diff.CheckpointVerdict, which is why
// only Overlap — the one nested type Task 10 does embed — carries json tags
// here; the top-level fields get their wire tags where CheckpointVerdict
// declares them.
type Result struct {
	Width, Height int
	DiffPct       float64
	DiffPctFine   float64
	NumDiff       int
	// Mismatch reports whether the two SHOTS (WidthA/HeightA vs
	// WidthB/HeightB, the real pre-trim geometry) differed in size. It is
	// deliberately NOT "did Compare need to pad/crop to run the
	// comparison" — Trim crops each side independently, so two same-size
	// shots can trim to different-size crops without the SHOTS having
	// mismatched; that is PaddedForDiff's meaning, not this one.
	Mismatch bool
	// PaddedForDiff reports whether Compare had to reconcile a size
	// difference (union-pad/crop) between what it actually measured
	// (post-trim, if Trim was set) in order to run the comparison. This can
	// be true when Mismatch is false — differing trims on same-size shots —
	// and is the field that means "Overlap/NumDiff were computed on a
	// reconciled canvas," not "the shots differed."
	PaddedForDiff                    bool
	WidthA, HeightA, WidthB, HeightB int
	Overlap                          *Overlap
	// TrimA/TrimB are the rects Compare actually kept when Options.Trim was
	// set, in the ORIGINAL images' coordinates; nil when trimming was not
	// requested or was refused. WidthA/HeightA/WidthB/HeightB remain the
	// shots' real, pre-trim geometry — the report says what was captured
	// AND what was compared, never one standing in for the other.
	TrimA, TrimB *Rect
}

// Images holds the visualisation outputs of a Compare call. Both are nil
// when not requested, or when requested but NumDiff == 0 — there is
// nothing informative to draw when nothing differs.
type Images struct{ Diff, Overlay *image.RGBA }

// ErrDecodeA and ErrDecodeB identify which side of a Compare call failed to
// decode as a PNG, so a caller can tell "a is corrupt" from "b is corrupt"
// with errors.Is instead of parsing a message.
var (
	ErrDecodeA = errors.New("pixel: image a is not a decodable PNG")
	ErrDecodeB = errors.New("pixel: image b is not a decodable PNG")
)

func Decode(b []byte) (*image.RGBA, error) {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("not a decodable PNG: %w", err)
	}
	if rgba, ok := img.(*image.RGBA); ok && rgba.Rect.Min == (image.Point{}) {
		return rgba, nil
	}
	out := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out, nil
}

func Encode(img *image.RGBA) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// colorDelta is pixelmatch's perceptual difference. yOnly returns the
// brightness delta alone, which is what antialias detection compares.
// Semi-transparent pixels are blended against white first, so an alpha
// change is not silently free — but pixelmatch's blend formula assumes
// STRAIGHT (non-premultiplied) channels, as produced by a canvas
// getImageData. image.RGBA (what Decode always returns) is premultiplied,
// so this blend runs against an already-premultiplied value: correct for
// fully opaque pixels (the overwhelming case for browser screenshots,
// where alpha is always 255), not faithful for a semi-transparent one.
func colorDelta(a, b []uint8, k, m int, yOnly bool) float64 {
	r1, g1, b1, a1 := float64(a[k]), float64(a[k+1]), float64(a[k+2]), float64(a[k+3])
	r2, g2, b2, a2 := float64(b[m]), float64(b[m+1]), float64(b[m+2]), float64(b[m+3])
	if a1 == a2 && r1 == r2 && g1 == g2 && b1 == b2 {
		return 0
	}
	if a1 < 255 {
		f := a1 / 255
		r1, g1, b1 = blend(r1, f), blend(g1, f), blend(b1, f)
	}
	if a2 < 255 {
		f := a2 / 255
		r2, g2, b2 = blend(r2, f), blend(g2, f), blend(b2, f)
	}
	y1, y2 := rgb2y(r1, g1, b1), rgb2y(r2, g2, b2)
	y := y1 - y2
	if yOnly {
		return y
	}
	i := rgb2i(r1, g1, b1) - rgb2i(r2, g2, b2)
	q := rgb2q(r1, g1, b1) - rgb2q(r2, g2, b2)
	delta := 0.5053*y*y + 0.299*i*i + 0.1957*q*q
	if y1 > y2 {
		return -delta
	}
	return delta
}

func blend(c, a float64) float64    { return 255 + (c-255)*a }
func rgb2y(r, g, b float64) float64 { return r*0.29889531 + g*0.58662247 + b*0.11448223 }
func rgb2i(r, g, b float64) float64 { return r*0.59597799 - g*0.27417610 - b*0.32180189 }
func rgb2q(r, g, b float64) float64 { return r*0.21147017 - g*0.52261711 + b*0.31114694 }

// Match counts differing pixels between two same-sized images. Antialiased
// pixels are detected and NOT counted — text rendering differs by a
// subpixel between machines, and counting that would make every CI run a
// diff. When out is non-nil it receives either the classic red-on-grey diff
// or, with diffMask, an alpha-only mask used to build the overlay.
func Match(a, b, out *image.RGBA, threshold float64, diffMask bool) int {
	w, h := a.Rect.Dx(), a.Rect.Dy()
	maxDelta := maxYIQDelta * threshold * threshold
	diff := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pos := y*a.Stride + x*4
			delta := colorDelta(a.Pix, b.Pix, pos, pos, false)
			if math.Abs(delta) > maxDelta {
				if antialiased(a, x, y, b) || antialiased(b, x, y, a) {
					if out != nil && !diffMask {
						setPix(out, pos, 255, 255, 0, 255) // yellow: antialiasing, not a change
					}
					continue
				}
				if out != nil {
					setPix(out, pos, 255, 0, 0, 255)
				}
				diff++
			} else if out != nil && !diffMask {
				// Unchanged pixels fade to grey so the red stands out.
				v := uint8(255 - (255-rgb2y(float64(a.Pix[pos]), float64(a.Pix[pos+1]), float64(a.Pix[pos+2])))*0.1)
				setPix(out, pos, v, v, v, 255)
			}
		}
	}
	return diff
}

// antialiased reports whether the pixel at (x1,y1) in img looks like an
// antialiasing artefact: its brightness is an extreme among its neighbours,
// and the extreme neighbour has many identical siblings in BOTH images —
// i.e. it sits on an edge between two solid regions.
func antialiased(img *image.RGBA, x1, y1 int, other *image.RGBA) bool {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	x0, y0 := max(x1-1, 0), max(y1-1, 0)
	x2, y2 := min(x1+1, w-1), min(y1+1, h-1)
	pos := y1*img.Stride + x1*4
	zeroes := 0
	if x1 == x0 || x1 == x2 || y1 == y0 || y1 == y2 {
		zeroes = 1
	}
	minDelta, maxD := 0.0, 0.0
	minX, minY, maxX, maxY := -1, -1, -1, -1
	for x := x0; x <= x2; x++ {
		for y := y0; y <= y2; y++ {
			if x == x1 && y == y1 {
				continue
			}
			d := colorDelta(img.Pix, img.Pix, pos, y*img.Stride+x*4, true)
			switch {
			case d == 0:
				zeroes++
				if zeroes > 2 {
					return false // a flat neighbourhood is not an edge
				}
			case d < minDelta:
				minDelta, minX, minY = d, x, y
			case d > maxD:
				maxD, maxX, maxY = d, x, y
			}
		}
	}
	if minDelta == 0 || maxD == 0 {
		return false
	}
	return (hasManySiblings(img, minX, minY) && hasManySiblings(other, minX, minY)) ||
		(hasManySiblings(img, maxX, maxY) && hasManySiblings(other, maxX, maxY))
}

// hasManySiblings reports whether a pixel has 3+ identical neighbours,
// counting the image border as one — the signature of a solid region.
func hasManySiblings(img *image.RGBA, x1, y1 int) bool {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	x0, y0 := max(x1-1, 0), max(y1-1, 0)
	x2, y2 := min(x1+1, w-1), min(y1+1, h-1)
	pos := y1*img.Stride + x1*4
	zeroes := 0
	if x1 == x0 || x1 == x2 || y1 == y0 || y1 == y2 {
		zeroes = 1
	}
	for x := x0; x <= x2; x++ {
		for y := y0; y <= y2; y++ {
			if x == x1 && y == y1 {
				continue
			}
			p := y*img.Stride + x*4
			if img.Pix[pos] == img.Pix[p] && img.Pix[pos+1] == img.Pix[p+1] &&
				img.Pix[pos+2] == img.Pix[p+2] && img.Pix[pos+3] == img.Pix[p+3] {
				zeroes++
				if zeroes > 2 {
					return true
				}
			}
		}
	}
	return false
}

func setPix(img *image.RGBA, pos int, r, g, b, a uint8) {
	img.Pix[pos], img.Pix[pos+1], img.Pix[pos+2], img.Pix[pos+3] = r, g, b, a
}

// ApplyMasks paints rectangles opaque black in BOTH images before
// comparison, so a clock widget or an avatar cannot fail a checkpoint. The
// rects are clamped to the image, so a mask authored for a taller device
// degrades to a partial mask instead of panicking. A zero-value Rect (or an
// empty rects slice) paints nothing — that is the correct reading of "no
// mask here", not a trap: unlike a threshold, there is no meaning of
// "unset rect" that Compare needs to distinguish from "empty rect".
func ApplyMasks(img *image.RGBA, rects []Rect) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	for _, r := range rects {
		for y := max(r.Y, 0); y < min(r.Y+r.Height, h); y++ {
			for x := max(r.X, 0); x < min(r.X+r.Width, w); x++ {
				setPix(img, y*img.Stride+x*4, 0, 0, 0, 255)
			}
		}
	}
}

// RectsFrom converts the config package's YAML rectangles into pixel
// rectangles. It lives HERE, not in config, because config is the leaf
// package everything reads and must not import an engine. Tasks 10 and 11
// both call it — it is the ONLY conversion between the two Rect types, so
// there is no second one to drift.
func RectsFrom(rs []config.Rect) []Rect {
	out := make([]Rect, len(rs))
	for i, r := range rs {
		out[i] = Rect{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
	}
	return out
}

// Compare decodes both PNGs, optionally trims a uniform border off each,
// reconciles mismatched geometry onto a shared canvas, applies masks, and
// runs Match at both the gate and fine thresholds. It never panics or
// throws on mismatched input — a dimension mismatch is reported in the
// Result, not an error.
func Compare(aPNG, bPNG []byte, o Options) (Result, Images, error) {
	aImg, err := Decode(aPNG)
	if err != nil {
		return Result{}, Images{}, fmt.Errorf("%w: %w", ErrDecodeA, err)
	}
	bImg, err := Decode(bPNG)
	if err != nil {
		return Result{}, Images{}, fmt.Errorf("%w: %w", ErrDecodeB, err)
	}

	// A zero threshold means "unset", exactly like config.Thresholds: there
	// is no way in Go to tell "the caller wrote 0" from "the caller wrote
	// nothing", so both must mean "use the standard default", never a
	// hair-trigger gate at literally zero.
	gate := o.GateThreshold
	if gate == 0 {
		gate = config.DefaultGate
	}
	fine := o.FineThreshold
	if fine == 0 {
		fine = config.DefaultFine
	}

	res := Result{
		WidthA:  aImg.Bounds().Dx(),
		HeightA: aImg.Bounds().Dy(),
		WidthB:  bImg.Bounds().Dx(),
		HeightB: bImg.Bounds().Dy(),
	}
	// Mismatch reports whether the two SHOTS differed in size — never
	// whether Trim happened to crop them to different sizes. It is derived
	// from WidthA/HeightA/WidthB/HeightB (the real, pre-trim geometry
	// recorded above, never overwritten by anything below), independent of
	// whatever the trim/padding reconciliation below decides is needed to
	// actually run the comparison. Two same-size shots with different
	// amounts of uniform border around identical content must never report
	// Mismatch: true — that would make Trim manufacture the exact false
	// signal it exists to remove.
	res.Mismatch = res.WidthA != res.WidthB || res.HeightA != res.HeightB

	// Masks are applied HERE, before any trim or size reconciliation, in
	// the ORIGINAL screenshot's coordinate space — the only frame in which
	// a mask rect means what its author meant. Applying masks after Trim
	// (as the brief's step order literally reads) is wrong: A and B are
	// trimmed independently, so a mask authored for the untrimmed shot
	// would land on different content on each side, or on none at all.
	// This ordering also means Overlap (computed below, on masked
	// workA/workB) correctly excludes masked regions instead of counting
	// them as real content change.
	ApplyMasks(aImg, o.Masks)
	ApplyMasks(bImg, o.Masks)

	// workA/workB are what Compare actually measures; aImg/bImg (and their
	// original dimensions recorded above) are never overwritten again.
	workA, workB := aImg, bImg
	if o.Trim {
		if cropped, kept, ok := TrimUniformBorder(aImg); ok {
			workA = cropped
			r := kept
			res.TrimA = &r
		}
		if cropped, kept, ok := TrimUniformBorder(bImg); ok {
			workB = cropped
			r := kept
			res.TrimB = &r
		}
	}

	wA, hA := workA.Bounds().Dx(), workA.Bounds().Dy()
	wB, hB := workB.Bounds().Dx(), workB.Bounds().Dy()

	var cmpA, cmpB *image.RGBA
	if wA != wB || hA != hB {
		// Overlap is measured on the cropped intersection BEFORE padding —
		// that is the only number that means "the content changed"; the
		// padded union below inflates NumDiff with forced diff wherever one
		// shot has no corresponding pixels in the other.
		ow, oh := min(wA, wB), min(hA, hB)
		overlapA := cropTopLeft(workA, ow, oh)
		overlapB := cropTopLeft(workB, ow, oh)
		overlapArea := ow * oh
		overlapGateDiff := Match(overlapA, overlapB, nil, gate, false)
		overlapFineDiff := Match(overlapA, overlapB, nil, fine, false)

		unionW, unionH := max(wA, wB), max(hA, hB)
		unionArea := unionW * unionH
		paddingPct := 0.0
		if unionArea > 0 {
			paddingPct = 100 * float64(unionArea-overlapArea) / float64(unionArea)
		}
		res.Overlap = &Overlap{
			Width:       ow,
			Height:      oh,
			NumDiff:     overlapGateDiff,
			DiffPct:     pct(overlapGateDiff, overlapArea),
			DiffPctFine: pct(overlapFineDiff, overlapArea),
			PaddingPct:  paddingPct,
		}

		cmpA = padToUnion(workA, unionW, unionH)
		cmpB = padToUnion(workB, unionW, unionH)
		res.PaddedForDiff = true
	} else {
		cmpA, cmpB = workA, workB
	}

	res.Width, res.Height = cmpA.Bounds().Dx(), cmpA.Bounds().Dy()
	area := res.Width * res.Height

	gateDiff := Match(cmpA, cmpB, nil, gate, false)
	fineDiff := Match(cmpA, cmpB, nil, fine, false)
	res.NumDiff = gateDiff
	res.DiffPct = pct(gateDiff, area)
	res.DiffPctFine = pct(fineDiff, area)

	var images Images
	if gateDiff > 0 {
		if o.WantDiff {
			diffImg := image.NewRGBA(cmpA.Bounds())
			Match(cmpA, cmpB, diffImg, gate, false)
			images.Diff = diffImg
		}
		if o.WantOverlay {
			images.Overlay = buildOverlayImage(cmpA, cmpB, gate, res.Width, res.Height)
		}
	}

	return res, images, nil
}

func pct(n, area int) float64 {
	if area == 0 {
		return 0
	}
	return 100 * float64(n) / float64(area)
}

func cropTopLeft(img *image.RGBA, w, h int) *image.RGBA {
	if img.Bounds().Dx() == w && img.Bounds().Dy() == h {
		return img
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

func padToUnion(img *image.RGBA, w, h int) *image.RGBA {
	if img.Bounds().Dx() == w && img.Bounds().Dy() == h {
		return img
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()), img, img.Bounds().Min, draw.Src)
	return out
}

// buildOverlayImage builds a magenta density overlay: a diff mask, dilated
// 3px so a one-pixel change is visible, composited over a copy of B with
// alpha scaled by local change density within a 10px radius.
func buildOverlayImage(a, b *image.RGBA, threshold float64, w, h int) *image.RGBA {
	maskImg := image.NewRGBA(a.Bounds())
	Match(a, b, maskImg, threshold, true)

	raw := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if maskImg.Pix[maskImg.PixOffset(x, y)+3] > 0 {
				raw[y*w+x] = 1
			}
		}
	}
	dilated := dilate(raw, w, h, overlayDilatePx)

	densityIn := make([]float32, w*h)
	for i, v := range dilated {
		densityIn[i] = float32(v)
	}
	density := boxFilter2D(densityIn, w, h, overlayDensityPx)

	out := image.NewRGBA(b.Bounds())
	draw.Draw(out, out.Bounds(), b, b.Bounds().Min, draw.Src)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			if dilated[idx] == 0 {
				continue
			}
			d := float64(density[idx])
			if d > 1 {
				d = 1
			}
			alpha := overlayAlphaMin + (overlayAlphaMax-overlayAlphaMin)*d
			blendPixel(out, out.PixOffset(x, y), overlayColor, alpha)
		}
	}
	return out
}

// dilate expands a 0/1 mask by radius r using a max filter. r is a small
// fixed constant (3px), so the naive O(w·h·r²) approach is fine here —
// unlike the density box filter below, which runs at a 10px radius and
// must stay O(w·h).
func dilate(mask []uint8, w, h, r int) []uint8 {
	out := make([]uint8, len(mask))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var m uint8
			for dy := -r; dy <= r; dy++ {
				ny := y + dy
				if ny < 0 || ny >= h {
					continue
				}
				for dx := -r; dx <= r; dx++ {
					nx := x + dx
					if nx < 0 || nx >= w {
						continue
					}
					if v := mask[ny*w+nx]; v > m {
						m = v
					}
				}
			}
			out[y*w+x] = m
		}
	}
	return out
}

// boxFilter2D box-blurs mask (w×h, row-major) at radius r using two 1D
// passes with a running sum — O(w·h) regardless of r, not O(w·h·r²).
// Edges are handled by replicating the boundary value, so every output
// pixel averages over the same (2r+1)×(2r+1) window.
func boxFilter2D(mask []float32, w, h, r int) []float32 {
	rowPass := make([]float32, w*h)
	for y := 0; y < h; y++ {
		boxSum1D(mask[y*w:(y+1)*w], rowPass[y*w:(y+1)*w], r)
	}
	out := make([]float32, w*h)
	col := make([]float32, h)
	colOut := make([]float32, h)
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			col[y] = rowPass[y*w+x]
		}
		boxSum1D(col, colOut, r)
		for y := 0; y < h; y++ {
			out[y*w+x] = colOut[y]
		}
	}
	window := float32((2*r + 1) * (2*r + 1))
	for i := range out {
		out[i] /= window
	}
	return out
}

// boxSum1D computes, for each index i, the sum of src over the window
// [i-r, i+r], replicating the boundary value past either edge — an O(n)
// running sum, independent of r.
func boxSum1D(src, dst []float32, r int) {
	n := len(src)
	if n == 0 {
		return
	}
	get := func(i int) float32 {
		switch {
		case i < 0:
			return src[0]
		case i >= n:
			return src[n-1]
		default:
			return src[i]
		}
	}
	var sum float32
	for j := -r; j <= r; j++ {
		sum += get(j)
	}
	dst[0] = sum
	for i := 1; i < n; i++ {
		sum += get(i+r) - get(i-r-1)
		dst[i] = sum
	}
}

func blendPixel(img *image.RGBA, pos int, color [3]float64, alpha float64) {
	for c := 0; c < 3; c++ {
		orig := float64(img.Pix[pos+c])
		img.Pix[pos+c] = uint8(orig*(1-alpha) + color[c]*alpha)
	}
	img.Pix[pos+3] = 255
}
