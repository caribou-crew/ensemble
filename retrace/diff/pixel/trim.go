package pixel

import (
	"image"
	"image/draw"
)

// TrimUniformBorder finds the tight bounding box of non-background content
// in img — where "background" is the top-left pixel's colour — and crops
// to it. Compare is its only caller, and only when Options.Trim is set.
//
// It refuses to trim (ok == false) rather than hand back a result nobody
// asked for, whenever the tight box would be under 2px in either
// dimension — the crop carries no comparison signal at that size. A fully
// uniform image (nothing rendered) is the extreme case of this: the scans
// below meet with zero width or height, which is always caught by the same
// <2px check, so there is exactly one size guard, not two.
//
// A refusal is not an error: the caller keeps comparing the image whole.
// When there is no border to remove — content already touches every
// edge — TrimUniformBorder succeeds but hands back the image untouched,
// with kept covering the full bounds.
func TrimUniformBorder(img *image.RGBA) (cropped *image.RGBA, kept Rect, ok bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, Rect{}, false
	}
	ref := pixelAt(img, b.Min.X, b.Min.Y)

	top := b.Min.Y
	for top < b.Max.Y && rowIsUniform(img, top, b.Min.X, b.Max.X, ref) {
		top++
	}
	bottom := b.Max.Y
	for bottom > top && rowIsUniform(img, bottom-1, b.Min.X, b.Max.X, ref) {
		bottom--
	}
	left := b.Min.X
	for left < b.Max.X && colIsUniform(img, left, b.Min.Y, b.Max.Y, ref) {
		left++
	}
	right := b.Max.X
	for right > left && colIsUniform(img, right-1, b.Min.Y, b.Max.Y, ref) {
		right--
	}

	// bottom's loop never crosses top, and right's never crosses left, so
	// kw and kh are always >= 0 — a fully uniform image collapses both to
	// exactly 0, which this single check also refuses.
	kw, kh := right-left, bottom-top
	if kw < 2 || kh < 2 {
		return nil, Rect{}, false // too small to carry any comparison signal
	}

	kept = Rect{X: float64(left - b.Min.X), Y: float64(top - b.Min.Y), Width: float64(kw), Height: float64(kh)}
	if kept.X == 0 && kept.Y == 0 && kw == w && kh == h {
		return img, kept, true // already tight: no border found, left untouched
	}

	out := image.NewRGBA(image.Rect(0, 0, kw, kh))
	draw.Draw(out, out.Bounds(), img, image.Point{X: left, Y: top}, draw.Src)
	return out, kept, true
}

func pixelAt(img *image.RGBA, x, y int) [4]uint8 {
	i := img.PixOffset(x, y)
	return [4]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
}

func rowIsUniform(img *image.RGBA, y, x0, x1 int, ref [4]uint8) bool {
	for x := x0; x < x1; x++ {
		if pixelAt(img, x, y) != ref {
			return false
		}
	}
	return true
}

func colIsUniform(img *image.RGBA, x, y0, y1 int, ref [4]uint8) bool {
	for y := y0; y < y1; y++ {
		if pixelAt(img, x, y) != ref {
			return false
		}
	}
	return true
}
