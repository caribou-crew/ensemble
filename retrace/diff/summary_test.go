package diff

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff/pixel"
	"github.com/caribou-crew/ensemble/retrace/rules"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- fixtures -----------------------------------------------------------
//
// Build reads its input off disk (manifests, wire.jsonl, hops.jsonl,
// shots/*.png) exactly the way `retrace diff` does — these helpers build a
// real run directory rather than a hand-assembled Summary, per the brief's
// procedural instruction: the composition is what this task tests.

func okCapture() runs.CaptureTrust {
	return runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"}
}

func writeWireFile(t *testing.T, dir string, hops []trace.Hop) {
	t.Helper()
	writeHopFile(t, filepath.Join(dir, "wire.jsonl"), hops)
}

func writeChainFile(t *testing.T, dir string, hops []trace.Hop) {
	t.Helper()
	writeHopFile(t, filepath.Join(dir, "hops.jsonl"), hops)
}

func writeHopFile(t *testing.T, path string, hops []trace.Hop) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := trace.NewWriter(f)
	for _, h := range hops {
		if err := w.Write(h); err != nil {
			t.Fatal(err)
		}
	}
}

// solidPNG encodes a flat-color image, the smallest fixture pixel.Compare
// can meaningfully diff.
func solidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	b, err := pixel.Encode(img)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rectPNG is solidPNG with one rectangle painted a different color, the
// smallest fixture that produces a real (or a maskable) pixel difference.
func rectPNG(t *testing.T, w, h int, base color.RGBA, rx, ry, rw, rh int, rc color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, base)
		}
	}
	for y := ry; y < ry+rh; y++ {
		for x := rx; x < rx+rw; x++ {
			img.SetRGBA(x, y, rc)
		}
	}
	b, err := pixel.Encode(img)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeShot writes one checkpoint's PNG under runDir/shots/<name>.png and
// returns the runs.Checkpoint manifest entry pointing at it.
func writeShot(t *testing.T, runDir, name string, png []byte) runs.Checkpoint {
	t.Helper()
	rel := filepath.Join("shots", name+".png")
	full := filepath.Join(runDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, png, 0o644); err != nil {
		t.Fatal(err)
	}
	return runs.Checkpoint{Name: name, File: filepath.ToSlash(rel)}
}

func manifest(runID string, cps []runs.Checkpoint, groups []runs.Group, cap runs.CaptureTrust) runs.Manifest {
	return runs.Manifest{
		Schema: runs.Schema, App: "app", Flow: "flow", RunID: runID,
		Checkpoints: cps, Groups: groups, Capture: cap, Wire: runs.Counts{Recorded: true},
	}
}

func baseConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Dir:        t.TempDir(),
		Thresholds: config.Thresholds{Gate: config.DefaultGate, Fine: config.DefaultFine},
		Gates:      map[string]config.Gate{},
	}
}

func gatePct(v float64) config.Gate { return config.Gate{BudgetPct: &v} }

func mustBuild(t *testing.T, in BuildInput) Summary {
	t.Helper()
	s, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s
}

// --- verdict rules --------------------------------------------------------

func TestUnexpected500FailsTheRunEvenWhenPixelsAndWireAreClean(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	// Identical wire on both sides — pixels and wire plane are both clean —
	// but B's chain carries an unexpected 500 the spec says must fail the
	// run regardless of the other planes.
	h := hop(1, "GET", "/cart", 200, "", `{"ok":true}`)
	bad := hop(2, "GET", "/orders", 500, "", `{"error":"boom"}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h, bad})

	a := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	b := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: a, B: b, Cfg: cfg})
	if s.Verdict != "failed" {
		t.Fatalf("verdict = %q, want failed (unexpected 500 must fail regardless of pixel/wire)", s.Verdict)
	}
	if ExitCode(s) != 2 {
		t.Fatalf("ExitCode = %d, want 2", ExitCode(s))
	}
	if len(s.UnexpectedStatuses) != 1 {
		t.Fatalf("UnexpectedStatuses = %+v, want one finding", s.UnexpectedStatuses)
	}
}

func TestARuleViolationExitsNonZero(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	// "updatedAt" carries a non-ISO8601 value on both sides: the iso8601
	// matcher is ready but neither side satisfies it, so Classify reports
	// Violation, not just Changed.
	a := hop(1, "GET", "/cart", 200, "", `{"updatedAt":"not-a-date"}`)
	b := hop(1, "GET", "/cart", 200, "", `{"updatedAt":"also-not-a-date"}`)
	writeWireFile(t, dirA, []trace.Hop{a})
	writeWireFile(t, dirB, []trace.Hop{b})

	opts := Options{Rules: []rules.Rule{{
		Body: []rules.BodyRule{{Glob: "updatedAt", Matcher: rules.Matcher{Kind: rules.KindNamed, Name: "iso8601"}}},
	}}}
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})
	if s.Verdict != "failed" {
		t.Fatalf("verdict = %q, want failed (a rule Violation must fail the build)", s.Verdict)
	}
	if ExitCode(s) != 2 {
		t.Fatalf("ExitCode = %d, want 2", ExitCode(s))
	}
	if s.Counts.Violations != 1 {
		t.Fatalf("Counts.Violations = %d, want 1", s.Counts.Violations)
	}
}

// TestAHeaderViolationExitsNonZero pins Task 8's own review finding: a
// header rule Violation must gate at exit 2 exactly like a body one. Before
// that fix, HeaderDiff always reported Type "changed", which would only
// ever reach "changed" (exit 1) here — never "failed".
func TestAHeaderViolationExitsNonZero(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	a := trace.Hop{Seq: 1, Method: "GET", Path: "/cart", Status: 200,
		Resp: trace.Payload{Headers: map[string]string{"x-request-id": "not-a-uuid-a"}}}
	b := trace.Hop{Seq: 1, Method: "GET", Path: "/cart", Status: 200,
		Resp: trace.Payload{Headers: map[string]string{"x-request-id": "not-a-uuid-b"}}}
	writeWireFile(t, dirA, []trace.Hop{a})
	writeWireFile(t, dirB, []trace.Hop{b})

	opts := Options{Rules: []rules.Rule{{
		Headers: map[string]rules.Matcher{"x-request-id": {Kind: rules.KindNamed, Name: "uuid"}},
	}}}
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})
	if s.Verdict != "failed" {
		t.Fatalf("verdict = %q, want failed (a header rule Violation must fail the build)", s.Verdict)
	}
	if s.Counts.Violations != 1 {
		t.Fatalf("Counts.Violations = %d, want 1 (must count header violations, not just body ones)", s.Counts.Violations)
	}
}

func TestAVolatileFieldUnderAnIso8601RuleProducesNoDiffEntry(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	a := hop(1, "GET", "/cart", 200, "", `{"updatedAt":"2024-01-01T00:00:00Z"}`)
	b := hop(1, "GET", "/cart", 200, "", `{"updatedAt":"2024-06-15T12:30:00Z"}`)
	writeWireFile(t, dirA, []trace.Hop{a})
	writeWireFile(t, dirB, []trace.Hop{b})

	opts := Options{Rules: []rules.Rule{{
		Body: []rules.BodyRule{{Glob: "updatedAt", Matcher: rules.Matcher{Kind: rules.KindNamed, Name: "iso8601"}}},
	}}}
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})
	if s.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass (a tolerated field must not move the verdict)", s.Verdict)
	}
	if len(s.Wire.Paired) != 1 || len(s.Wire.Paired[0].BodyDiff) != 0 {
		t.Fatalf("expected zero BodyDiff entries, got %+v", s.Wire.Paired)
	}
}

func TestAMaskedRegionDoesNotAffectTheCheckpointVerdict(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	red := color.RGBA{R: 250, G: 0, B: 0, A: 255}

	cpA := writeShot(t, dirA, "cart", solidPNG(t, 40, 40, base))
	// The only difference between A and B sits entirely inside the mask
	// rect below.
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 40, 40, base, 5, 5, 10, 10, red))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Masks = map[string][]config.Rect{"cart": {{X: 0, Y: 0, Width: 20, Height: 20}}}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass (masked region must not move the verdict); checkpoints=%+v", s.Verdict, s.Checkpoints)
	}
	if len(s.Checkpoints) != 1 || s.Checkpoints[0].Verdict != "ok" {
		t.Fatalf("checkpoint verdict = %+v, want ok", s.Checkpoints)
	}
}

// TestAPctMaskedRegionDoesNotAffectTheCheckpointVerdict is
// TestAMaskedRegionDoesNotAffectTheCheckpointVerdict's fractional-mask
// counterpart: the only difference between A and B sits inside a `pct:
// true` mask instead of an absolute one, resolved against this 40x40
// checkpoint's real size (top-left quadrant: width 0.5, height 0.5 = the
// same [0,20)x[0,20) region the absolute test above covers with Width:20,
// Height:20 — proving the two forms agree at one concrete resolution).
func TestAPctMaskedRegionDoesNotAffectTheCheckpointVerdict(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	red := color.RGBA{R: 250, G: 0, B: 0, A: 255}

	cpA := writeShot(t, dirA, "cart", solidPNG(t, 40, 40, base))
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 40, 40, base, 5, 5, 10, 10, red))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Masks = map[string][]config.Rect{"cart": {{X: 0, Y: 0, Width: 0.5, Height: 0.5, Pct: true}}}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass (pct-masked region must not move the verdict); checkpoints=%+v", s.Verdict, s.Checkpoints)
	}
	if len(s.Checkpoints) != 1 || s.Checkpoints[0].Verdict != "ok" {
		t.Fatalf("checkpoint verdict = %+v, want ok", s.Checkpoints)
	}
}

func TestAnAddedDownstreamCallMarksTheFlowChanged(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	edge := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{edge})
	writeWireFile(t, dirB, []trace.Hop{edge})

	chainA := []trace.Hop{{Seq: 1, TraceID: "t1", From: "client", To: "bff", Method: "GET", Path: "/cart", Status: 200}}
	chainB := []trace.Hop{
		{Seq: 1, TraceID: "t1", From: "client", To: "bff", Method: "GET", Path: "/cart", Status: 200},
		{Seq: 2, TraceID: "t1", From: "bff", To: "pricing", Method: "GET", Path: "/price", Status: 200},
	}
	writeChainFile(t, dirA, chainA)
	writeChainFile(t, dirB, chainB)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Verdict != "changed" {
		t.Fatalf("verdict = %q, want changed (a new downstream route)", s.Verdict)
	}
	if len(s.Hops.NewRoutes) != 1 {
		t.Fatalf("NewRoutes = %+v, want one entry", s.Hops.NewRoutes)
	}
}

func TestOneJsonDocumentCarriesCheckpointsWirePairsAndHopDeltas(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 10, 10, color.RGBA{A: 255}))
	cpB := writeShot(t, dirB, "cart", solidPNG(t, 10, 10, color.RGBA{A: 255}))
	h := hop(1, "GET", "/cart", 200, "", `{"a":1}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	chain := []trace.Hop{{Seq: 1, TraceID: "t1", From: "client", To: "bff", Method: "GET", Path: "/cart", Status: 200}}
	writeChainFile(t, dirA, chain)
	writeChainFile(t, dirB, chain)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t)
	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"checkpoints", "wire", "hops", "verdict", "counts"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("json document missing %q: %s", key, b)
		}
	}
	wireDoc, ok := doc["wire"].(map[string]any)
	if !ok {
		t.Fatalf("wire is not an object: %s", b)
	}
	if _, ok := wireDoc["paired"]; !ok {
		t.Fatalf("wire.paired missing: %s", b)
	}
	hopsDoc, ok := doc["hops"].(map[string]any)
	if !ok {
		t.Fatalf("hops is not an object: %s", b)
	}
	if _, ok := hopsDoc["newRoutes"]; !ok {
		t.Fatalf("hops.newRoutes missing: %s", b)
	}
}

// TestAWireOnlyDeltaMarksTheVerdictChanged closes a gap the mutation-testing
// pass found: TestOneJsonDocumentCarriesCheckpointsWirePairsAndHopDeltas
// checks that Wire.Paired/Counts.WireChanged are populated correctly from a
// wire delta, but never asserts Verdict itself moves off "pass" for a
// wire-only change with every other plane clean — a dropped wire-delta
// check in changed() slipped past every summary_test.go test and was only
// caught by the CLI e2e suite (TestDiffExitsOneWhenAFieldChanged).
func TestAWireOnlyDeltaMarksTheVerdictChanged(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	a := hop(1, "GET", "/cart", 200, "", `{"total":1}`)
	b := hop(1, "GET", "/cart", 200, "", `{"total":2}`)
	writeWireFile(t, dirA, []trace.Hop{a})
	writeWireFile(t, dirB, []trace.Hop{b})

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Counts.WireChanged != 1 {
		t.Fatalf("Counts.WireChanged = %d, want 1", s.Counts.WireChanged)
	}
	if s.Verdict != "changed" {
		t.Fatalf("verdict = %q, want changed (a wire-only delta must move the verdict off pass)", s.Verdict)
	}
	if ExitCode(s) != 1 {
		t.Fatalf("ExitCode = %d, want 1", ExitCode(s))
	}
}

func TestRelayFoldingIsOnByDefaultInBuild(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	edge := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{edge})
	writeWireFile(t, dirB, []trace.Hop{edge})

	// A: client -> bff directly. B: client -> edge -> bff, a transparent
	// relay. With folding on (the default this test pins), both sides
	// collapse to the same client->bff route and the run stays "pass". A
	// caller that ever flips folding off by omission (the exact trap
	// HopOptions.NoCollapse's negative-boolean shape exists to prevent)
	// would see this test fail with an unexplained NewRoute/GoneRoute.
	chainA := []trace.Hop{
		{Seq: 1, TraceID: "t1", From: "client", To: "bff", Method: "GET", Path: "/cart", Status: 200},
	}
	chainB := []trace.Hop{
		{Seq: 1, TraceID: "t1", From: "client", To: "edge", Method: "GET", Path: "/cart", Status: 200},
		{Seq: 2, TraceID: "t1", From: "edge", To: "bff", Method: "GET", Path: "/cart", Status: 200},
	}
	writeChainFile(t, dirA, chainA)
	writeChainFile(t, dirB, chainB)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass (relay folding must be on by default); hops=%+v", s.Verdict, s.Hops)
	}
	if len(s.Hops.NewRoutes) != 0 {
		t.Fatalf("NewRoutes = %+v, want empty — the relay must fold away, not appear as a new route", s.Hops.NewRoutes)
	}
}

func TestACaptureTrustBannerRidesAlongInJsonAndText(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})

	suspectCapture := runs.CaptureTrust{Status: trace.VerdictSuspect, Summary: "a quiet stretch was seen", Hint: "check the run"}
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, suspectCapture)}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	// AllowDegraded so the "suspect" side is compared, not quarantined —
	// this test is about the banner riding along, not about quarantine.
	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, AllowDegraded: true})
	if s.Capture.A.Status != trace.VerdictSuspect || s.Capture.A.Summary != "a quiet stretch was seen" {
		t.Fatalf("Capture.A = %+v, want the suspect verdict carried through", s.Capture.A)
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	capDoc, ok := doc["capture"].(map[string]any)
	if !ok {
		t.Fatalf("capture missing from json: %s", b)
	}
	aDoc, ok := capDoc["a"].(map[string]any)
	if !ok || aDoc["status"] != "suspect" {
		t.Fatalf("capture.a.status = %v, want suspect: %s", capDoc["a"], b)
	}

	var text strings.Builder
	RenderText(&text, s)
	if !strings.Contains(text.String(), "suspect") {
		t.Fatalf("RenderText output does not mention the suspect capture banner:\n%s", text.String())
	}
}

func TestANonOkSideIsQuarantinedByDefault(t *testing.T) {
	for _, side := range []string{"a", "b"} {
		t.Run("broken on side "+side, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			h := hop(1, "GET", "/cart", 200, "", `{}`)
			writeWireFile(t, dirA, []trace.Hop{h})
			writeWireFile(t, dirB, []trace.Hop{h})

			broken := runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the capture listener stopped during the run"}
			capA, capB := okCapture(), okCapture()
			if side == "a" {
				capA = broken
			} else {
				capB = broken
			}
			aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, capA)}
			bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, capB)}
			cfg := baseConfig(t)

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
			if s.Verdict != "quarantined" {
				t.Fatalf("verdict = %q, want quarantined", s.Verdict)
			}
			if ExitCode(s) != 3 {
				t.Fatalf("ExitCode = %d, want 3", ExitCode(s))
			}
			if len(s.Quarantined) != 1 || s.Quarantined[0].Side != side {
				t.Fatalf("Quarantined = %+v, want side %s named", s.Quarantined, side)
			}
			if s.Quarantined[0].Reason != broken.Summary {
				t.Fatalf("Quarantined reason = %q, want the side's own capture summary %q", s.Quarantined[0].Reason, broken.Summary)
			}
			if len(s.Checkpoints) != 0 || len(s.Wire.Paired) != 0 || s.Hops.HopRequireConfigured || len(s.Budgets) != 0 {
				t.Fatalf("a quarantined Build must not populate any comparison field, got %+v", s)
			}
		})
	}
}

// TestIncompleteCheckOnlyFiresOnANegativeExitCodeNotAnyNonzeroOne pins the
// other half of incompleteCheck's contract: a POSITIVE Test.ExitCode is a
// completed test that failed (already surfaced through capture.Assess's
// "test-failed" -> VerdictFailed -> the ordinary, AllowDegraded-gated
// quarantineCheck path), not a truncated recording. Collapsing "!= 0" would
// make incompleteCheck fire unconditionally for a merely-failing test too,
// silently taking away --allow-degraded's ability to let a human compare
// one anyway.
//
// Table-driven over BOTH sides. The review found this pinned on side b
// only: narrowing side a's "< 0" to "!= 0" was green, and --a defaults to
// the REFERENCE side, so a stale never-finished reference bundle is the
// likelier real case of the two.
func TestIncompleteCheckOnlyFiresOnANegativeExitCodeNotAnyNonzeroOne(t *testing.T) {
	for _, side := range []string{"a", "b"} {
		t.Run("a positive exit code on side "+side, func(t *testing.T) {
			a := RunRef{Manifest: manifest("a", nil, nil, okCapture())}
			b := RunRef{Manifest: manifest("b", nil, nil, okCapture())}
			if side == "a" {
				a.Manifest.Test.ExitCode = 7
			} else {
				b.Manifest.Test.ExitCode = 7
			}
			if q := incompleteCheck(a, b); len(q) != 0 {
				t.Fatalf("incompleteCheck = %+v, want empty — a positive exit code is a completed (failing) test, not a truncated recording", q)
			}
		})
	}
}

// TestIncompleteCheckFiresOnANegativeExitCodeOnEitherSide is the mirror of
// the test above, and the reason it exists is that the two together are
// what pin "< 0": a test asserting only that 7 does NOT fire passes just as
// happily against an incompleteCheck that never fires at all, and the
// existing -1 fixtures only ever put the killed run on side b — so deleting
// side a's block entirely was green.
func TestIncompleteCheckFiresOnANegativeExitCodeOnEitherSide(t *testing.T) {
	for _, side := range []string{"a", "b"} {
		t.Run("a signal-killed run on side "+side, func(t *testing.T) {
			a := RunRef{Manifest: manifest("a", nil, nil, okCapture())}
			b := RunRef{Manifest: manifest("b", nil, nil, okCapture())}
			if side == "a" {
				a.Manifest.Test.ExitCode = -1
			} else {
				b.Manifest.Test.ExitCode = -1
			}
			q := incompleteCheck(a, b)
			if len(q) != 1 || q[0].Side != side {
				t.Fatalf("incompleteCheck = %+v, want exactly one entry naming side %q", q, side)
			}
			if !strings.Contains(q[0].Reason, "truncated") {
				t.Fatalf("reason = %q, want one saying the recording is truncated", q[0].Reason)
			}
		})
	}
}

// TestASignalKilledTestCommandIsQuarantinedNotDiffed pins the team lead's
// ruling on the brief's original "signal-killed child" passage: `retrace
// diff` never execs a child (only `retrace run`'s `-- <test command>` tail
// does — cmd_run.go, out of scope for this task), so the signal-kill
// reaches Build as DATA, not as a live process. cmd_run.go writes
// exec.ExitError.ExitCode()'s raw -1 straight into the manifest's
// Test.ExitCode when the test command is killed by a signal (CI's timeout,
// Ctrl-C); a fixture manifest carrying that value is all this needs — no
// child process anywhere.
//
// Side b's Capture is deliberately "ok" here, unlike
// TestANonOkSideIsQuarantinedByDefault: this proves incompleteCheck itself
// gates the build, not a side effect of capture.Assess also marking a
// nonzero TestExitCode VerdictFailed. Today those two paths would agree in
// production, but cmd_run.go's own pass-through behavior is a separate,
// still-open ruling — this test must not depend on it.
func TestASignalKilledTestCommandIsQuarantinedNotDiffed(t *testing.T) {
	for _, side := range []string{"a", "b"} {
		t.Run("killed on side "+side, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			h := hop(1, "GET", "/cart", 200, "", `{}`)
			writeWireFile(t, dirA, []trace.Hop{h})
			writeWireFile(t, dirB, []trace.Hop{h})

			aManifest := manifest("a", nil, nil, okCapture())
			bManifest := manifest("b", nil, nil, okCapture())
			if side == "a" {
				aManifest.Test.ExitCode = -1
			} else {
				bManifest.Test.ExitCode = -1
			}
			aRef := RunRef{Kind: "run", Dir: dirA, Manifest: aManifest}
			bRef := RunRef{Kind: "run", Dir: dirB, Manifest: bManifest}
			cfg := baseConfig(t)

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
			if s.Verdict != "quarantined" {
				t.Fatalf("verdict = %q, want quarantined (a signal-killed test command produced a truncated recording, not a comparable run)", s.Verdict)
			}
			if ExitCode(s) != 3 {
				t.Fatalf("ExitCode = %d, want 3", ExitCode(s))
			}
			if len(s.Quarantined) != 1 || s.Quarantined[0].Side != side {
				t.Fatalf("Quarantined = %+v, want side %s named", s.Quarantined, side)
			}
			if len(s.Checkpoints) != 0 || len(s.Wire.Paired) != 0 || len(s.Budgets) != 0 {
				t.Fatalf("a quarantined Build must not populate any comparison field, got %+v", s)
			}
		})
	}
}

// TestAllowDegradedDoesNotOverrideASignalKilledTestCommand: --allow-degraded
// (see TestAllowDegradedOverridesQuarantine) exists so a human can accept a
// LOWER-CONFIDENCE but still-complete capture. A run whose test command was
// killed by a signal has no complete data to accept — there is nothing for
// the flag to override, so incompleteCheck is NOT gated behind
// AllowDegraded the way quarantineCheck is.
func TestAllowDegradedDoesNotOverrideASignalKilledTestCommand(t *testing.T) {
	for _, side := range []string{"a", "b"} {
		t.Run("killed on side "+side, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			h := hop(1, "GET", "/cart", 200, "", `{}`)
			writeWireFile(t, dirA, []trace.Hop{h})
			writeWireFile(t, dirB, []trace.Hop{h})

			aManifest := manifest("a", nil, nil, okCapture())
			bManifest := manifest("b", nil, nil, okCapture())
			if side == "a" {
				aManifest.Test.ExitCode = -1
			} else {
				bManifest.Test.ExitCode = -1
			}
			aRef := RunRef{Kind: "run", Dir: dirA, Manifest: aManifest}
			bRef := RunRef{Kind: "run", Dir: dirB, Manifest: bManifest}
			cfg := baseConfig(t)

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, AllowDegraded: true})
			if s.Verdict != "quarantined" {
				t.Fatalf("verdict = %q, want quarantined even with AllowDegraded (a truncated recording has no complete data to accept)", s.Verdict)
			}
			if ExitCode(s) != 3 {
				t.Fatalf("ExitCode = %d, want 3", ExitCode(s))
			}
			if len(s.Quarantined) != 1 || s.Quarantined[0].Side != side {
				t.Fatalf("Quarantined = %+v, want side %s named", s.Quarantined, side)
			}
		})
	}
}

// TestASuspectSideIsQuarantinedEvenThoughItIsNotFatal pins quarantineCheck's
// documented breadth: it reads the raw Status, excluding only VerdictOK —
// deliberately WIDER than capture.Fatal, which also excludes VerdictSuspect
// (see trust.go's Fatal doc comment). A side capture.Fatal would call
// trustworthy enough to fail the build on must still be refused up front by
// the default (non---allow-degraded) quarantine check: comparing against a
// side nobody confirmed clean is not evidence of anything, "somewhat
// suspicious" included. TestACaptureTrustBannerRidesAlongInJsonAndText
// covers the opposite corner (a suspect side WITH --allow-degraded, so the
// banner rides through comparison) but never pins the default-refusal path
// for VerdictSuspect specifically — this test closes that gap.
//
// Table-driven over BOTH sides: the review found the A arm pinned (narrowing
// it to capture.Fatal was caught) and the B arm not (narrowing B to Fatal
// was green), because the only suspect-side fixture in the suite put the
// suspect run on A.
func TestASuspectSideIsQuarantinedEvenThoughItIsNotFatal(t *testing.T) {
	for _, side := range []string{"a", "b"} {
		t.Run("suspect on side "+side, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			h := hop(1, "GET", "/cart", 200, "", `{}`)
			writeWireFile(t, dirA, []trace.Hop{h})
			writeWireFile(t, dirB, []trace.Hop{h})

			suspect := runs.CaptureTrust{Status: trace.VerdictSuspect, Summary: "a quiet stretch was seen"}
			capA, capB := okCapture(), okCapture()
			if side == "a" {
				capA = suspect
			} else {
				capB = suspect
			}
			aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, capA)}
			bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, capB)}
			cfg := baseConfig(t)

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
			if s.Verdict != "quarantined" {
				t.Fatalf("verdict = %q, want quarantined (a suspect side must be refused by default, not silently compared)", s.Verdict)
			}
			if ExitCode(s) != 3 {
				t.Fatalf("ExitCode = %d, want 3", ExitCode(s))
			}
			if len(s.Quarantined) != 1 || s.Quarantined[0].Side != side {
				t.Fatalf("Quarantined = %+v, want side %s named", s.Quarantined, side)
			}
		})
	}
}

func TestAllowDegradedOverridesQuarantine(t *testing.T) {
	for _, side := range []string{"a", "b"} {
		t.Run("broken on side "+side, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			h := hop(1, "GET", "/cart", 200, "", `{}`)
			writeWireFile(t, dirA, []trace.Hop{h})
			writeWireFile(t, dirB, []trace.Hop{h})

			broken := runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the capture listener stopped during the run"}
			capA, capB := okCapture(), okCapture()
			if side == "a" {
				capA = broken
			} else {
				capB = broken
			}
			aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, capA)}
			bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, capB)}
			cfg := baseConfig(t)

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, AllowDegraded: true})
			if s.Verdict != "failed" {
				t.Fatalf("verdict = %q, want failed (a fatal side still fails the build once compared)", s.Verdict)
			}
			if len(s.Quarantined) != 0 {
				t.Fatalf("Quarantined = %+v, want empty — AllowDegraded must skip quarantine, not merely relabel it", s.Quarantined)
			}
			if len(s.Wire.Paired) != 1 {
				t.Fatalf("expected the comparison to actually run, got Wire=%+v", s.Wire)
			}
		})
	}
}

func TestAnUnconfiguredPlaneGetsNoGateEntry(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	a := hop(1, "GET", "/cart", 200, "", `{"a":1}`)
	b := hop(1, "GET", "/cart", 200, "", `{"a":2,"b":3,"c":4}`)
	writeWireFile(t, dirA, []trace.Hop{a})
	writeWireFile(t, dirB, []trace.Hop{b})

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t) // Gates has no "wire" key

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Counts.WireChanged == 0 {
		t.Fatalf("expected the wire diff to actually change, got Counts=%+v", s.Counts)
	}
	for _, g := range s.Budgets {
		if g.Plane == "wire" {
			t.Fatalf("Budgets contains a wire entry (%+v) though cfg.Gates never configured \"wire\" — a stray zero-Threshold Gate would read as \"passed\"", g)
		}
	}
}

// TestAWireBudgetComparesAPercentageNotARawCount pins the team lead's
// review finding: every Gate.Threshold comes from `gates: {<plane>:
// {budget_pct}}` — always a percentage — so Observed must be one too, for
// every plane including "wire". An earlier draft used Counts.WireChanged
// (a raw count) for wire's Observed, matching the brief's literal wording;
// under that reading 3 changed entries out of 1000 would fail a
// `budget_pct: 2` gate (3 > 2), which is absurd — 0.3% of a flow's calls
// changing is nowhere near a 2% budget. A fixture where the raw-count and
// percentage readings agree (e.g. 3 of 4) would pin neither; this one only
// passes under the percentage reading.
func TestAWireBudgetComparesAPercentageNotARawCount(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	const total, changed = 1000, 3
	hopsA := make([]trace.Hop, total)
	hopsB := make([]trace.Hop, total)
	for i := range total {
		path := fmt.Sprintf("/item/%d", i)
		respB := `{"v":1}`
		if i < changed {
			respB = `{"v":2}`
		}
		hopsA[i] = hop(uint64(i+1), "GET", path, 200, "", `{"v":1}`)
		hopsB[i] = hop(uint64(i+1), "GET", path, 200, "", respB)
	}
	writeWireFile(t, dirA, hopsA)
	writeWireFile(t, dirB, hopsB)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["wire"] = gatePct(2) // 2% budget; 3/1000 = 0.3% must PASS

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Counts.WireChanged != changed {
		t.Fatalf("Counts.WireChanged = %d, want %d", s.Counts.WireChanged, changed)
	}
	var g *Gate
	for i := range s.Budgets {
		if s.Budgets[i].Plane == "wire" {
			g = &s.Budgets[i]
		}
	}
	if g == nil {
		t.Fatalf("no wire Gate in Budgets: %+v", s.Budgets)
	}
	if g.Failed {
		t.Fatalf("wire Gate = %+v, want Failed=false (0.3%% observed, 2%% threshold) — a raw-count reading would compare 3 > 2 and fail", *g)
	}
	if g.Observed < 0.29 || g.Observed > 0.31 {
		t.Fatalf("wire Gate.Observed = %v, want ~0.3 (a percentage: 3/1000*100), not the raw count 3", g.Observed)
	}
}

// TestAPerfBudgetOf0MsEmitsNoGateAtAll pins the second half of the units
// ruling: BudgetMs == 0 (DerivePerfBudget never configured one) must not
// silently read as "0% over budget" — clean — nor divide by zero. It must
// behave exactly like a plane cfg.Gates never mentions: no entry at all.
func TestAPerfBudgetOf0MsEmitsNoGateAtAll(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["perf"] = gatePct(10) // configured, but no PerfBudgetMs anywhere -> BudgetMs stays 0

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Perf.BudgetMs != 0 {
		t.Fatalf("test setup: Perf.BudgetMs = %v, want 0", s.Perf.BudgetMs)
	}
	for _, g := range s.Budgets {
		if g.Plane == "perf" {
			t.Fatalf("Budgets contains a perf entry (%+v) though BudgetMs is 0 — an unset budget must emit no Gate, not one that divides by zero or reads as clean", g)
		}
	}
}

// TestAPerfBudgetOverARunThatRecordedNoCallsEmitsNoGateAtAll is the OTHER
// empty-denominator case on this plane, and the one budgetsOf's own doc
// comment already claimed was covered: MeasuredMs is TotalCallDurationMs
// over side B's hops, so a run that recorded NO calls measures 0ms, lands
// at -100% of any budget, and reported a clean gate on the run with the
// least evidence in it. With fail_on: [perf] that is a green CI job over a
// run that made no calls.
//
// observedFor was the one plane of four that never tested its own
// denominator: wire refuses on WirePaired == 0, hop on no ServiceCounts,
// pixel on no checkpoints, and perf asked only whether a BUDGET existed.
//
// Both arms, because "refuse always" satisfies the first one alone.
func TestAPerfBudgetOverARunThatRecordedNoCallsEmitsNoGateAtAll(t *testing.T) {
	call := hop(1, "GET", "/cart", 200, "", `{}`)

	for _, tc := range []struct {
		name         string
		wireA, wireB []trace.Hop
		wantGate     bool
	}{
		{"neither side recorded a call", nil, nil, false},
		// A budget is about the run under review, so side B is what
		// decides: a reference that made calls cannot lend evidence to a
		// candidate that made none.
		{"only the reference recorded a call", []trace.Hop{call}, nil, false},
		{"this run recorded a call", []trace.Hop{call}, []trace.Hop{call}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aRef, bRef := twoRuns(t, tc.wireA, tc.wireB, nil, nil)
			cfg := baseConfig(t)
			cfg.Gates["perf"] = gatePct(10)
			cfg.Flows = map[string]config.Flow{"flow": {PerfBudgetMs: 5000}}

			s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
			if s.Perf.BudgetMs != 5000 {
				t.Fatalf("test setup: Perf.BudgetMs = %v, want 5000", s.Perf.BudgetMs)
			}
			var gate *Gate
			for i, g := range s.Budgets {
				if g.Plane == "perf" {
					gate = &s.Budgets[i]
				}
			}
			if tc.wantGate {
				if gate == nil {
					t.Fatalf("this run recorded a call and its perf budget emitted no Gate — refusing always is not the fix; Budgets = %+v", s.Budgets)
				}
				return
			}
			if gate != nil {
				t.Fatalf("this run recorded no call and its perf budget reported %+v — a gate that says \"within budget\" over a run with no backend work in it, and with fail_on: [perf] a green CI job over it", *gate)
			}
		})
	}
}

// TestAPerfBudgetIsPercentOverNotPercentOf pins the perf half of the units
// ruling: Observed is percent OVER budget, (Measured-Budget)/Budget*100 —
// 0 means "exactly at budget" — not percent OF budget consumed
// (Measured/Budget*100, this implementer's original call, overturned in
// review because 100 would then mean "at budget" and every threshold on
// this one plane alone would have to be written around 100 instead of 0).
// 150ms measured against a 100ms budget is 50% over.
func TestAPerfBudgetIsPercentOverNotPercentOf(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	slow := trace.Hop{Seq: 1, Method: "GET", Path: "/cart", Status: 200, T: trace.Timings{DoneMs: 150}}
	writeWireFile(t, dirB, []trace.Hop{slow})

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Flows = map[string]config.Flow{"flow": {PerfBudgetMs: 100}}
	cfg.Gates["perf"] = gatePct(60) // 50% over must PASS a 60% allowance

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Perf.MeasuredMs != 150 || s.Perf.BudgetMs != 100 {
		t.Fatalf("test setup: Perf = %+v, want Measured 150 / Budget 100", s.Perf)
	}
	var g *Gate
	for i := range s.Budgets {
		if s.Budgets[i].Plane == "perf" {
			g = &s.Budgets[i]
		}
	}
	if g == nil {
		t.Fatalf("no perf Gate in Budgets: %+v", s.Budgets)
	}
	if g.Observed < 49.9 || g.Observed > 50.1 {
		t.Fatalf("perf Gate.Observed = %v, want ~50 (percent OVER budget: (150-100)/100*100), not ~150 (percent OF budget)", g.Observed)
	}
	if g.Failed {
		t.Fatalf("perf Gate = %+v, want Failed=false (50%% over is under the 60%% allowance)", *g)
	}
}

func TestAZeroBudgetGatesOnAnyDifference(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	red := color.RGBA{R: 250, G: 0, B: 0, A: 255}
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 40, 40, base))
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 40, 40, base, 5, 5, 6, 6, red))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["pixel"] = gatePct(0) // explicitly zero: must be pixel-identical

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	var px *Gate
	for i := range s.Budgets {
		if s.Budgets[i].Plane == "pixel" {
			px = &s.Budgets[i]
		}
	}
	if px == nil {
		t.Fatalf("no pixel Budget entry, want one (configured explicitly at 0)")
	}
	if !px.Failed {
		t.Fatalf("pixel Gate = %+v, want Failed true — a configured zero budget must gate on any nonzero DiffPct", px)
	}
}

func TestFailOnDeterminesWhichBudgetCanFailTheBuild(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	red := color.RGBA{R: 250, G: 0, B: 0, A: 255}
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 40, 40, base))
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 40, 40, base, 5, 5, 10, 10, red))
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}

	cfg1 := baseConfig(t)
	cfg1.Gates["pixel"] = gatePct(0)
	cfg1.FailOn = []string{"wire"}
	s1 := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg1})
	if s1.Verdict != "changed" {
		t.Fatalf("with fail_on:[wire], a failing pixel Gate must not fail the build; verdict = %q, want changed", s1.Verdict)
	}

	cfg2 := baseConfig(t)
	cfg2.Gates["pixel"] = gatePct(0)
	cfg2.FailOn = []string{"pixel"}
	s2 := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg2})
	if s2.Verdict != "failed" {
		t.Fatalf("with fail_on:[pixel], the same failing pixel Gate must fail the build; verdict = %q, want failed", s2.Verdict)
	}
}

func TestConformanceUncheckedOnlyStaysPass(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	// Identical, well-formed, NON-truncated bodies on both sides, so the
	// wire plane itself reports zero changes — the only thing that can move
	// the verdict here is the conformance plane. The spec documents /cart's
	// 200 response as a $ref to a schema that does not exist in
	// components.schemas, so the required-field check cannot run at all
	// (unresolvable $ref) and reports "unchecked", never a silent pass.
	h := hop(1, "GET", "/cart", 200, "", `{"sku":"x"}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})

	specPath := writeSpecFile(t, `{
		"paths": {"/cart": {"get": {"responses": {"200": {"content": {"application/json": {
			"schema": {"$ref": "#/components/schemas/Missing"}
		}}}}}}},
		"components": {"schemas": {}}
	}`)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	// Cfg.Dir joins with Cfg.OpenAPI at the call site (filepath.Join(in.Cfg.Dir,
	// in.Cfg.OpenAPI)); an absolute OpenAPI path with Dir == "" resolves to
	// itself, which keeps this fixture independent of baseConfig's t.TempDir().
	cfg.Dir = ""
	cfg.OpenAPI = specPath

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Conformance) != 1 || s.Conformance[0].Kind != "unchecked" {
		t.Fatalf("Conformance = %+v, want exactly one \"unchecked\" finding", s.Conformance)
	}
	if s.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass — an unchecked-only conformance list must be verdict-neutral", s.Verdict)
	}
}

func TestConformanceOneRealFindingChangesTheVerdict(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	// The SAME call, identically, on both sides (status 200 so it can never
	// also trip FindUnexpectedStatuses/exit-2 — this test isolates
	// conformance's own contribution to "changed"). The spec documents no
	// paths at all, so CheckOpenAPI reports "unknown-path" — a real,
	// non-"unchecked" finding.
	h := hop(1, "GET", "/unmapped", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})

	specPath := writeSpecFile(t, `{"paths": {}}`)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Dir = ""
	cfg.OpenAPI = specPath

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Conformance) != 1 || s.Conformance[0].Kind == "unchecked" {
		t.Fatalf("Conformance = %+v, want one non-unchecked finding", s.Conformance)
	}
	if s.Counts.WireChanged != 0 || s.Counts.WireMissing != 0 || s.Counts.WireExtra != 0 {
		t.Fatalf("wire plane must be clean in this fixture, got Counts=%+v", s.Counts)
	}
	if s.Verdict != "changed" {
		t.Fatalf("verdict = %q, want changed — a real conformance finding must move the verdict", s.Verdict)
	}
}

func writeSpecFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSectionsComeFromTheManifestsGroups(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	loginCall := trace.Hop{Seq: 1, Method: "GET", Path: "/login", Status: 200, T: trace.Timings{Start: t0.Add(1 * time.Second)}}
	checkoutCall := trace.Hop{Seq: 2, Method: "GET", Path: "/checkout", Status: 200, T: trace.Timings{Start: t0.Add(11 * time.Second)}}
	writeWireFile(t, dirA, []trace.Hop{loginCall, checkoutCall})
	writeWireFile(t, dirB, []trace.Hop{loginCall, checkoutCall})

	groups := []runs.Group{
		{Name: "login", StartedAt: t0, EndedAt: t0.Add(10 * time.Second)},
		{Name: "checkout", StartedAt: t0.Add(10 * time.Second), EndedAt: t0.Add(20 * time.Second)},
	}
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, groups, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, groups, okCapture())}
	cfg := baseConfig(t)
	opts, err := OptionsFor(cfg, aRef.Manifest, bRef.Manifest)
	if err != nil {
		t.Fatalf("OptionsFor: %v", err)
	}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, Options: opts})
	if len(s.Sections) != 2 {
		t.Fatalf("Sections = %+v, want 2 (login, checkout)", s.Sections)
	}
	if s.Sections[0].Name != "login" || len(s.Sections[0].Entries) != 1 || s.Sections[0].Entries[0].NormalizedPath != "/login" {
		t.Fatalf("Sections[0] = %+v, want login carrying the /login entry", s.Sections[0])
	}
	if s.Sections[1].Name != "checkout" || len(s.Sections[1].Entries) != 1 || s.Sections[1].Entries[0].NormalizedPath != "/checkout" {
		t.Fatalf("Sections[1] = %+v, want checkout carrying the /checkout entry", s.Sections[1])
	}
}

func TestSummaryJsonShapeIsStable(t *testing.T) {
	fixed := Summary{
		Schema: SummarySchema, App: "shop", Flow: "checkout",
		A:       RunRef{RunID: "reference", Kind: "bundle", Dir: "/runs/a"},
		B:       RunRef{RunID: "20240101T000000Z-abc1234", Kind: "run", Dir: "/runs/b"},
		Verdict: "changed",
		Checkpoints: []CheckpointVerdict{
			{Name: "cart", Verdict: "changed", DiffPct: 1.5, DiffPctFine: 2.1, NumDiff: 12,
				Images: CheckpointImages{A: "shots/cart.png", B: "shots/cart.png", Diff: "diff/shots/cart.png"}},
		},
		Wire:     Wire{Paired: []Entry{}, Missing: []Call{}, Extra: []Call{}},
		Sections: []Section{{Name: "", Entries: []Entry{}, Counts: map[string]int{}}},
		Hops:     HopDiff{NewRoutes: []Route{}, GoneRoutes: []Route{}},
		Perf:     PerfResult{Status: "unset"},
		Capture:  CaptureBanner{A: okCapture(), B: okCapture()},
		Counts:   Counts{Checkpoints: 1, PixelChanged: 1},
		Gates:    []string{},
		Budgets:  []Gate{{Plane: "pixel", Threshold: 0.1, Observed: 1.5, Failed: true}},
	}
	// Through the same exit ritual every Summary gets at Build's exits — the
	// array normalisation AND the triage classification. A golden built from
	// a hand-assembled struct that skipped it would document a shape
	// production never emits — nulls for array fields that always ship as [],
	// and an empty `triage.label`, which is the one value Build guarantees
	// never reaches a consumer.
	//
	// nil config: this Summary has no project triage rules, which is what
	// almost every project has, and triageOf treats nil as exactly that.
	fixed.finish(nil)

	got, err := json.MarshalIndent(fixed, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	golden := filepath.Join("testdata", "summary.golden.json")
	if os.Getenv("REGEN") != "" {
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading golden: %v (set REGEN=1 to write it)", err)
	}
	if string(got)+"\n" != string(want) {
		t.Fatalf("Summary JSON shape drifted from testdata/summary.golden.json — field names are an API; a rename is a breaking change for every consumer.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestBuildItselfRefusesASideThatResolvedToNone pins the rule directly at
// Build, not merely through the callers that already guard.
//
// "none" means "I could not compare", never "nothing differed". Every
// consumer of refs.Resolve today guards before calling Build and each of
// those guards is pinned — but a rule re-implemented at each consumer is a
// rule that will be forgotten at the next one, and Tasks 12 and 13 add two
// consumers that resolve references through the same call. Build's refusal
// is the invariant they cannot fail to inherit; the callers' guards stay,
// because they own the message that names `retrace ref accept`.
//
// BOTH sides are asserted: a guard written for side A only is the
// mutation-set symmetry this phase keeps paying for.
func TestBuildItselfRefusesASideThatResolvedToNone(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	cfg := baseConfig(t)
	good := func(dir, id string) RunRef {
		return RunRef{Kind: "run", Dir: dir, RunID: id, Manifest: manifest(id, nil, nil, okCapture())}
	}
	none := RunRef{Kind: "none"}

	for _, tc := range []struct {
		name string
		a, b RunRef
	}{
		{"side A", none, good(dirB, "b")},
		{"side B", good(dirA, "a"), none},
		{"both sides", none, none},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Build(BuildInput{App: "app", Flow: "flow", A: tc.a, B: tc.b, Cfg: cfg})
			if err == nil {
				t.Fatalf("Build returned a Summary (verdict %q) for a side that resolved to nothing comparable — an empty document whose verdict reads clean is exactly the plausible value \"none\" exists to prevent", s.Verdict)
			}
			if s.Verdict != "" {
				t.Fatalf("Build returned verdict %q alongside its error; a refused build must produce no document at all", s.Verdict)
			}
		})
	}

	// The mirror: the kinds that DO resolve still build. A refusal that
	// caught "run" or "bundle" would break every diff in the product.
	for _, kind := range []string{"run", "bundle"} {
		a, b := good(dirA, "a"), good(dirB, "b")
		a.Kind, b.Kind = kind, kind
		if _, err := Build(BuildInput{App: "app", Flow: "flow", A: a, B: b, Cfg: cfg}); err != nil {
			t.Fatalf("Build refused two %q sides: %v", kind, err)
		}
	}
}
