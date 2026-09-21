package suites

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngBytes is a minimal valid 1x1 PNG.
func pngBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa7\x9a\xa0\xa0\x00\x00\x00\x00IEND\xaeB`\x82")
}
func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// project builds a project root with the standard inventory and a separate
// directory holding an importable report and its screenshot files.
type screenProject struct{ cwd, src string }

func newScreenProject(t *testing.T) screenProject {
	t.Helper()
	p := screenProject{cwd: t.TempDir(), src: t.TempDir()}
	b, _ := json.Marshal(testInventory())
	if err := os.WriteFile(filepath.Join(p.cwd, InventoryFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
func (p screenProject) write(t *testing.T, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(p.src, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func (p screenProject) report(t *testing.T, id, platform string, screens []Screen) string {
	t.Helper()
	a := fullAttempt(id, platform)
	a.Results[0].Screens = screens
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	p.write(t, "report.json", b)
	return filepath.Join(p.src, "report.json")
}

func TestImportStoresScreenAsContentAddressedAsset(t *testing.T) {
	p := newScreenProject(t)
	p.write(t, "shots/final.png", pngBytes())
	got, err := Import(p.cwd, p.report(t, "ios-1", "ios", []Screen{{Label: "Final screen", File: "shots/final.png"}}))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Results[0].Screens[0]
	want := sum(pngBytes())
	if s.SHA256 != want || s.Media != "image/png" || s.Bytes != len(pngBytes()) || s.File != "" {
		t.Fatalf("stored form: %+v", s)
	}
	asset := filepath.Join(p.cwd, ".retrace", "suite-assets", "checkout", "ios-1", want+".png")
	if data, err := os.ReadFile(asset); err != nil || !bytes.Equal(data, pngBytes()) {
		t.Fatalf("asset not stored: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(p.cwd, ".retrace", "suites", "checkout", "ios-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "shots/final.png") || strings.Contains(string(stored), p.src) {
		t.Fatalf("source path leaked into the immutable report: %s", stored)
	}
	overview, err := Load(p.cwd)
	if err != nil {
		t.Fatal(err)
	}
	cell := overview[0].Builds[0].Features[0].Flows[0].Platforms[1]
	if cell.Platform != "ios" || cell.Latest == nil || len(cell.Latest.Screens) != 1 || cell.Latest.Screens[0].SHA256 != want {
		t.Fatalf("screens missing from aggregate: %+v", cell.Latest)
	}
}

func TestImportRejectsUnsafeOrInvalidScreens(t *testing.T) {
	big := append(pngBytes(), bytes.Repeat([]byte{0}, maxScreenBytes)...)
	cases := []struct {
		name    string
		prepare func(t *testing.T, p screenProject)
		screens []Screen
	}{
		{"absolute path", func(t *testing.T, p screenProject) {}, []Screen{{Label: "x", File: "/etc/hosts"}}},
		{"parent traversal", func(t *testing.T, p screenProject) { p.write(t, "../outside.png", pngBytes()) }, []Screen{{Label: "x", File: "../outside.png"}}},
		{"missing file", func(t *testing.T, p screenProject) {}, []Screen{{Label: "x", File: "nope.png"}}},
		{"not an image", func(t *testing.T, p screenProject) { p.write(t, "a.png", []byte("<html>not an image</html>")) }, []Screen{{Label: "x", File: "a.png"}}},
		{"svg is not accepted", func(t *testing.T, p screenProject) {
			p.write(t, "a.svg", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"))
		}, []Screen{{Label: "x", File: "a.svg"}}},
		{"oversized", func(t *testing.T, p screenProject) { p.write(t, "big.png", big) }, []Screen{{Label: "x", File: "big.png"}}},
		{"empty label", func(t *testing.T, p screenProject) { p.write(t, "a.png", pngBytes()) }, []Screen{{Label: " ", File: "a.png"}}},
		{"source with a hash but no file", func(t *testing.T, p screenProject) {}, []Screen{{Label: "x", SHA256: sum(pngBytes()), Media: "image/png", Bytes: 10}}},
		{"file and hash together", func(t *testing.T, p screenProject) { p.write(t, "a.png", pngBytes()) }, []Screen{{Label: "x", File: "a.png", SHA256: sum(pngBytes())}}},
		{"too many screens", func(t *testing.T, p screenProject) { p.write(t, "a.png", pngBytes()) }, func() []Screen {
			out := []Screen{}
			for i := 0; i < maxScreensPerResult+1; i++ {
				out = append(out, Screen{Label: "s", File: "a.png"})
			}
			return out
		}()},
		{"duplicate labels", func(t *testing.T, p screenProject) { p.write(t, "a.png", pngBytes()) }, []Screen{{Label: "same", File: "a.png"}, {Label: "same", File: "a.png"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newScreenProject(t)
			tc.prepare(t, p)
			if _, err := Import(p.cwd, p.report(t, "bad", "ios", tc.screens)); err == nil {
				t.Fatal("import accepted an invalid screen")
			}
			if _, err := os.Stat(filepath.Join(p.cwd, ".retrace", "suites", "checkout", "bad.json")); err == nil {
				t.Fatal("a rejected import still published a report")
			}
			if _, err := os.Stat(filepath.Join(p.cwd, ".retrace", "suite-assets")); err == nil {
				t.Fatal("a rejected import still wrote assets")
			}
		})
	}
}

func TestImportRejectsSymlinkedScreen(t *testing.T) {
	p := newScreenProject(t)
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, pngBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.src, "link.png")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := Import(p.cwd, p.report(t, "link", "ios", []Screen{{Label: "x", File: "link.png"}})); err == nil {
		t.Fatal("a symlinked screenshot was imported")
	}
}

func TestReadRejectsUnpublishedOrMalformedStoredScreens(t *testing.T) {
	for name, screen := range map[string]Screen{
		"source form on disk": {Label: "x", File: "a.png"},
		"bad hash":            {Label: "x", SHA256: "XYZ", Media: "image/png", Bytes: 5},
		"bad media":           {Label: "x", SHA256: sum(pngBytes()), Media: "image/svg+xml", Bytes: 5},
		"zero bytes":          {Label: "x", SHA256: sum(pngBytes()), Media: "image/png"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newScreenProject(t)
			a := fullAttempt("stored", "ios")
			a.Results[0].Screens = []Screen{screen}
			b, _ := json.Marshal(a)
			dir := filepath.Join(p.cwd, ".retrace", "suites", "checkout")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "stored.json"), b, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p.cwd); err == nil {
				t.Fatal("a malformed stored screen was aggregated")
			}
		})
	}
}

func TestOpenScreenServesOnlyReferencedVerifiedAssets(t *testing.T) {
	p := newScreenProject(t)
	p.write(t, "final.png", pngBytes())
	if _, err := Import(p.cwd, p.report(t, "ios-1", "ios", []Screen{{Label: "Final", File: "final.png"}})); err != nil {
		t.Fatal(err)
	}
	h := sum(pngBytes())
	data, media, err := OpenScreen(p.cwd, "checkout", "ios-1", h)
	if err != nil || media != "image/png" || !bytes.Equal(data, pngBytes()) {
		t.Fatalf("open: %v %q", err, media)
	}
	// A file with a correct name and hash that no stored report references must
	// not be served either: the report, not the directory, is the allow-list.
	stray := append(pngBytes(), 7)
	strayDir := filepath.Join(p.cwd, ".retrace", "suite-assets", "checkout", "ios-1")
	if err := os.WriteFile(filepath.Join(strayDir, sum(stray)+".png"), stray, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][3]string{
		"present but unreferenced": {"checkout", "ios-1", sum(stray)},
		"unreferenced hash":        {"checkout", "ios-1", sum([]byte("other"))},
		"unknown attempt":          {"checkout", "nope", h},
		"traversal suite":          {"..", "ios-1", h},
		"traversal attempt":        {"checkout", "../ios-1", h},
		"non-hash":                 {"checkout", "ios-1", "../../etc/passwd"},
	} {
		if _, _, err := OpenScreen(p.cwd, args[0], args[1], args[2]); err == nil {
			t.Fatalf("%s: served", name)
		}
	}
	// A tampered file must fail closed rather than serve different bytes.
	asset := filepath.Join(p.cwd, ".retrace", "suite-assets", "checkout", "ios-1", h+".png")
	if err := os.WriteFile(asset, append(pngBytes(), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenScreen(p.cwd, "checkout", "ios-1", h); err == nil {
		t.Fatal("tampered asset was served")
	}
}

func TestReimportOfAnExistingAttemptChangesNothing(t *testing.T) {
	p := newScreenProject(t)
	p.write(t, "final.png", pngBytes())
	file := p.report(t, "ios-1", "ios", []Screen{{Label: "Final", File: "final.png"}})
	if _, err := Import(p.cwd, file); err != nil {
		t.Fatal(err)
	}
	p.write(t, "final.png", append(pngBytes(), 9))
	if _, err := Import(p.cwd, file); err == nil {
		t.Fatal("attempt id was reused")
	}
	entries, _ := os.ReadDir(filepath.Join(p.cwd, ".retrace", "suite-assets", "checkout", "ios-1"))
	if len(entries) != 1 {
		t.Fatalf("second import left extra assets: %d", len(entries))
	}
}
