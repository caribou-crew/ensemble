package pixel

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateTestdata (re)writes the golden fixtures under testdata/. It is
// skipped by default — `go test` never mutates committed bytes on its own —
// and only runs with `REGEN=1 go test ./retrace/diff/pixel/ -run
// TestGenerateTestdata`, so a reviewer can regenerate and diff the PNGs
// against what is committed rather than trust the bytes blind.
//
// This reproduces flowlens's test/fixtures/generate-fixtures.mjs: 40×40,
// base RGB(10,20,30) opaque; diff.b adds a RGB(250,0,0) rect at
// (5,5)-(15,15); mask.b adds a RGB(0,250,0) rect at (20,20)-(30,30);
// identical.a/identical.b are the same solid image.
func TestGenerateTestdata(t *testing.T) {
	if os.Getenv("REGEN") == "" {
		t.Skip("set REGEN=1 to rewrite goldens")
	}

	const size = 40
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	red := color.RGBA{R: 250, G: 0, B: 0, A: 255}
	green := color.RGBA{R: 0, G: 250, B: 0, A: 255}

	write := func(name string, img *image.RGBA) {
		b, err := Encode(img)
		if err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join("testdata", name), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	solid := func() *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		fillRect(img, 0, 0, size, size, base)
		return img
	}

	write("identical.a.png", solid())
	write("identical.b.png", solid())

	diffA := solid()
	write("diff.a.png", diffA)
	diffB := solid()
	fillRect(diffB, 5, 5, 10, 10, red)
	write("diff.b.png", diffB)

	maskA := solid()
	write("mask.a.png", maskA)
	maskB := solid()
	fillRect(maskB, 20, 20, 10, 10, green)
	write("mask.b.png", maskB)
}

func fillRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			img.SetRGBA(x+dx, y+dy, c)
		}
	}
}
