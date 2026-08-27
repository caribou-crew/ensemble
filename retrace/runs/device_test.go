package runs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func dev(w, h int) *Device { return &Device{Kind: "browser", Width: w, Height: h} }

func TestTwoUnknownScreensAreNotAMatch(t *testing.T) {
	// The zero-value trap this whole guard turns on. If nil matched nil, the
	// check would switch itself off for exactly the pair with the least
	// evidence in it — two runs neither of which recorded a screen.
	if SameScreen(nil, nil) {
		t.Error("SameScreen(nil, nil) = true — two unknowns are not a match")
	}
	if SameScreen(dev(390, 844), nil) || SameScreen(nil, dev(390, 844)) {
		t.Error("a known screen matched an unknown one")
	}
}

func TestTheSameGeometryMatchesRegardlessOfProvenance(t *testing.T) {
	// A 390x844 viewport Playwright reported and a 390x844 inferred from a
	// shot are the same screen. Comparing Kind would fail every run captured
	// before adapters wrote device.json against every run captured after —
	// a flag day for a guard that is supposed to catch real mistakes.
	browser := &Device{Kind: "browser", Width: 390, Height: 844}
	shot := &Device{Kind: "shot", Width: 390, Height: 844, ID: "cart"}
	if !SameScreen(browser, shot) {
		t.Error("the same geometry from two sources did not match")
	}
}

func TestADifferentGeometryDoesNotMatch(t *testing.T) {
	// The oracle from the brief: two real iPhone sizes that differ by a
	// couple of dozen pixels in each direction.
	if SameScreen(dev(1206, 2622), dev(1178, 2556)) {
		t.Error("1206x2622 matched 1178x2556")
	}
	// One dimension equal is still a mismatch, and this is the case a
	// half-written comparison passes.
	if SameScreen(dev(390, 844), dev(390, 800)) {
		t.Error("same width, different height matched")
	}
	if SameScreen(dev(390, 844), dev(400, 844)) {
		t.Error("same height, different width matched")
	}
}

func TestScaleDoesNotDecideComparability(t *testing.T) {
	// Two runs at equal width and height but different device-pixel ratios
	// produce shots of different PIXEL dimensions, which the per-checkpoint
	// size check already catches with a far more precise message. Refusing
	// here too would report the vaguer of the two facts first.
	a := &Device{Kind: "device", Width: 390, Height: 844, Scale: 2}
	b := &Device{Kind: "device", Width: 390, Height: 844, Scale: 3}
	if !SameScreen(a, b) {
		t.Error("a different scale at the same geometry was treated as a different screen")
	}
}

func TestADeviceThatIsPresentMustDescribeARealScreen(t *testing.T) {
	// A present device.json reading 0x0 is a CLAIM, and two runs that both
	// wrote one would compare 0x0 to 0x0 and agree. Refused at the door,
	// because downstream it is indistinguishable from a fact.
	for _, d := range []*Device{
		{Kind: "browser", Width: 0, Height: 844},
		{Kind: "browser", Width: 390, Height: 0},
		{Kind: "browser", Width: -1, Height: 844},
		{Kind: "browser"},
	} {
		if err := validateDevice(d); err == nil {
			t.Errorf("validateDevice(%+v) accepted a screen that is not one", d)
		}
	}
	if err := validateDevice(&Device{Width: 390, Height: 844}); err == nil {
		t.Error("a device with no kind was accepted — its provenance is what a mismatch message explains")
	}
	if err := validateDevice(nil); err != nil {
		t.Errorf("nil (no device recorded) must be legal: %v", err)
	}
	if err := validateDevice(dev(390, 844)); err != nil {
		t.Errorf("a real screen was rejected: %v", err)
	}
}

func TestABrokenDeviceIsRefusedOnBothWriteAndRead(t *testing.T) {
	// Read matters independently: a manifest hand-edited, or written by a
	// version predating this check, must not reach a comparison carrying a
	// geometry that compares equal to another broken one.
	p := Paths{RunDir: t.TempDir()}
	p.ManifestPath = p.RunDir + "/manifest.json"
	m := &Manifest{
		Schema:  Schema,
		Capture: CaptureTrust{Status: "ok"},
		Wire:    Counts{Recorded: true},
		Device:  &Device{Kind: "browser", Width: 0, Height: 0},
	}
	if err := WriteManifest(p, m); err == nil {
		t.Fatal("WriteManifest accepted a 0x0 device")
	}

	// Now put one on disk behind WriteManifest's back and prove the read
	// side refuses it too.
	m.Device = dev(390, 844)
	if err := WriteManifest(p, m); err != nil {
		t.Fatal(err)
	}
	rawBytes, err := os.ReadFile(p.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(rawBytes)
	broken := strings.Replace(raw, `"width": 390`, `"width": 0`, 1)
	if broken == raw {
		t.Fatalf("test setup: the width was not where this test expected it:\n%s", raw)
	}
	if err := os.WriteFile(p.ManifestPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(p.ManifestPath); err == nil {
		t.Error("ReadManifest accepted a 0-width device off disk")
	}
}

func TestADeviceIsOmittedFromJSONWhenThereIsNone(t *testing.T) {
	// nil must not serialize as `"device": {}` — a key that is present says
	// something was recorded, and 0x0 is what a consumer would then read.
	b, err := json.Marshal(Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"device"`) {
		t.Errorf("a manifest with no device emitted the key:\n%s", b)
	}
}
