package diff

import (
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// sideAt builds a RunRef whose manifest reports one checkpoint and a device.
// A nil device models a run that recorded no geometry at all.
func sideAt(d *runs.Device, shots ...string) RunRef {
	var cps []runs.Checkpoint
	for _, n := range shots {
		cps = append(cps, runs.Checkpoint{Name: n})
	}
	return RunRef{Kind: "run", Manifest: runs.Manifest{Checkpoints: cps, Device: d}}
}

func screen(w, h int) *runs.Device { return &runs.Device{Kind: "device", Width: w, Height: h} }

func TestMismatchedGeometryIsRefusedNotMeasured(t *testing.T) {
	// The oracle from the brief. Two iPhone sizes a couple of dozen pixels
	// apart produce a large diff percentage for EVERY checkpoint, and not one
	// of those numbers means anything — a precise wrong answer is far more
	// likely to be acted on than an obvious error.
	q := geometryCheck(sideAt(screen(1206, 2622), "cart"), sideAt(screen(1178, 2556), "cart"))
	if len(q) != 2 {
		t.Fatalf("geometryCheck returned %d rows, want 2 (one per side)", len(q))
	}
	for _, row := range q {
		if !strings.Contains(row.Reason, GeometryMismatch) {
			t.Errorf("side %s does not carry the %q token a CI filter greps for: %s", row.Side, GeometryMismatch, row.Reason)
		}
		for _, want := range []string{"1206x2622", "1178x2556"} {
			if !strings.Contains(row.Reason, want) {
				t.Errorf("the reason does not report %s: %s", want, row.Reason)
			}
		}
	}
	if q[0].Side != "a" || q[1].Side != "b" {
		t.Errorf("sides = %q, %q — both must be named; neither run is the wrong one, and naming one invites someone to \"fix\" it", q[0].Side, q[1].Side)
	}
}

func TestMatchingGeometryIsNotRefused(t *testing.T) {
	if q := geometryCheck(sideAt(screen(390, 844), "cart"), sideAt(screen(390, 844), "cart")); len(q) != 0 {
		t.Errorf("two runs on the same screen were refused: %+v", q)
	}
}

func TestARunWithNoShotsHasNoGeometryToProtect(t *testing.T) {
	// A wire-only run has no pixel plane. Refusing there would break every
	// API-only project in the world over a screen it never rendered to.
	//
	// Both sides carry a DIFFERENT device on purpose. A playwright adapter
	// writes device.json from the viewport whether or not the test ever takes
	// a screenshot, so "no shots" and "no device" are separate conditions and
	// each needs its own guard. Passing nil devices here would let the
	// missing-device guard satisfy the test and leave this one untested.
	a := sideAt(&runs.Device{Kind: "browser", ID: "chromium", Width: 1280, Height: 720})
	b := sideAt(&runs.Device{Kind: "browser", ID: "webkit", Width: 390, Height: 844})
	if q := geometryCheck(a, b); len(q) != 0 {
		t.Errorf("two shot-less runs were refused over viewports neither one rendered to: %+v", q)
	}
}

func TestAnUnrecordedScreenIsNotTreatedAsAMismatch(t *testing.T) {
	// The upgrade path, and the reason this check demands proof rather than
	// vouching. Every recording made before device.json existed has no device;
	// re-recording one side gives it one via capture's shot fallback. If a
	// missing device counted as a mismatch, upgrading retrace would refuse
	// every comparison against a stored reference — a guard that fires on the
	// entire installed base is not a guard, it is an outage.
	for _, tc := range []struct {
		name string
		a, b RunRef
	}{
		{"neither side recorded one", sideAt(nil, "cart"), sideAt(nil, "cart")},
		{"only side a recorded one", sideAt(screen(390, 844), "cart"), sideAt(nil, "cart")},
		{"only side b recorded one", sideAt(nil, "cart"), sideAt(screen(390, 844), "cart")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if q := geometryCheck(tc.a, tc.b); len(q) != 0 {
				t.Errorf("refused a pair with no proven mismatch: %+v", q)
			}
		})
	}
}

func TestAnUnrecordedScreenStillDescribesItself(t *testing.T) {
	// geometryCheck no longer hands describeDevice a nil, so this covers the
	// defensive branch directly rather than leaving it as untested code that
	// would panic the day a second caller appears.
	if got := describeDevice(nil); !strings.Contains(got, "unrecorded") {
		t.Errorf("describeDevice(nil) = %q, want it to say the screen was never recorded", got)
	}
}

func TestOneSideWithShotsIsEnoughToRefuse(t *testing.T) {
	// The asymmetric case: a stored reference full of shots against a run that
	// died before taking any, on a different screen. Only one side has a pixel
	// plane, and that is enough — "no shots on EITHER side" is what buys the
	// wire-only exemption, not "no shots on one".
	//
	// The alternative reading reports this as missing checkpoints and says
	// nothing about the screen, which sends someone re-recording on the wrong
	// device to chase it.
	q := geometryCheck(sideAt(screen(1206, 2622), "cart"), sideAt(screen(1178, 2556)))
	if len(q) != 2 {
		t.Fatalf("a shot-bearing side was compared against a different screen: %+v", q)
	}
}

func TestTheReasonNamesWhereEachNumberCameFrom(t *testing.T) {
	// The fix depends entirely on provenance: two "browser" runs at different
	// sizes is a viewport someone changed; a "browser" against a "device" is
	// two adapters that were never comparable.
	a := sideAt(&runs.Device{Kind: "browser", ID: "chromium", Width: 390, Height: 844}, "cart")
	b := sideAt(&runs.Device{Kind: "shot", ID: "cart", Width: 1170, Height: 2532}, "cart")
	q := geometryCheck(a, b)
	if len(q) == 0 {
		t.Fatal("a browser viewport was compared against raw shot dimensions")
	}
	for _, want := range []string{"browser", "chromium", "shot"} {
		if !strings.Contains(q[0].Reason, want) {
			t.Errorf("the reason omits %q: %s", want, q[0].Reason)
		}
	}
}

func TestAGeometryRefusalQuarantinesTheWholeComparison(t *testing.T) {
	// Through Build, not geometryCheck: the check existing and the check
	// being CONSULTED are different facts, and only the second one protects
	// anybody. A Build that computed the wire plane anyway would invite the
	// reader to trust the halves that were computed.
	cfg := baseConfig(t)
	aRef := sideAt(screen(1206, 2622), "cart")
	bRef := sideAt(screen(1178, 2556), "cart")
	aRef.Dir, bRef.Dir = t.TempDir(), t.TempDir()

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Verdict != "quarantined" {
		t.Fatalf("verdict = %q, want quarantined", s.Verdict)
	}
	if len(s.Quarantined) != 2 {
		t.Errorf("Quarantined = %+v, want both sides", s.Quarantined)
	}
	if len(s.Checkpoints) != 0 || len(s.Wire.Paired) != 0 {
		t.Errorf("a refused comparison computed a plane: checkpoints=%d paired=%d", len(s.Checkpoints), len(s.Wire.Paired))
	}
}

func TestAllowDegradedDoesNotOverrideAGeometryRefusal(t *testing.T) {
	// --allow-degraded lets a human accept a LOWER-CONFIDENCE comparison.
	// This is not lower confidence; it is a different screen. There is no
	// comparison here to accept, so the flag has nothing to override — the
	// same rule incompleteCheck follows.
	cfg := baseConfig(t)
	aRef := sideAt(screen(1206, 2622), "cart")
	bRef := sideAt(screen(1178, 2556), "cart")
	aRef.Dir, bRef.Dir = t.TempDir(), t.TempDir()

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, AllowDegraded: true})
	if s.Verdict != "quarantined" {
		t.Fatalf("verdict = %q, want quarantined even under --allow-degraded", s.Verdict)
	}
}
