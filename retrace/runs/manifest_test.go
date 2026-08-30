package runs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestManifestRoundTripsAndStampsSchema(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{
		App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble,
		StartedAt:   time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		Checkpoints: []Checkpoint{{Name: "cart", File: "shots/cart.png", Width: 390, Height: 844}},
		Capture:     CaptureTrust{Status: trace.VerdictDegraded, Summary: "propagation gap at bff"},
		Wire:        Counts{Recorded: true},
	}
	if err := WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Schema != Schema {
		t.Fatalf("Schema = %q, want %q", got.Schema, Schema)
	}
	if got.Capture.Status != trace.VerdictDegraded || len(got.Checkpoints) != 1 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	raw, _ := os.ReadFile(p.ManifestPath)
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("manifest must end with a newline (it is committed in reference bundles)")
	}
	var any map[string]json.RawMessage
	if err := json.Unmarshal(raw, &any); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	// Assert the on-disk VALUE, not merely the key's presence — a
	// zero-value CaptureTrust also has a "capture" key (review finding 1).
	var capture CaptureTrust
	if err := json.Unmarshal(any["capture"], &capture); err != nil {
		t.Fatalf("capture is not valid JSON: %v", err)
	}
	if capture.Status == "" {
		t.Fatal(`capture.status must never serialize as "" — an unset verdict must not gate as ok`)
	}
	if capture.Status != trace.VerdictDegraded {
		t.Fatalf("capture.status = %q, want %q", capture.Status, trace.VerdictDegraded)
	}
}

// TestManifestRoundTripsFixtureSourceAndOmitsWhenNil covers
// retrace-run-fixtures-design.md's D5: Fixtures carries provenance for a
// `retrace run --fixtures` capture and must round-trip through
// WriteManifest/ReadManifest, while an ordinary run's nil Fixtures must
// not serialize a "fixtures" key at all — the same "absent vs present"
// distinction Capture.Status already protects.
func TestManifestRoundTripsFixtureSourceAndOmitsWhenNil(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{
		App: "web", Flow: "checkout", RunID: "r1", Mode: ModeStandalone,
		Capture:  CaptureTrust{Status: trace.VerdictOK},
		Wire:     Counts{Recorded: true},
		Fixtures: &FixtureSource{Ref: "checkout", RefKind: "run", RunID: "r0", Served: 3, UnusedCount: 1, MissCount: 0},
	}
	if err := WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Fixtures == nil || got.Fixtures.Served != 3 || got.Fixtures.RunID != "r0" || got.Fixtures.Ref != "checkout" {
		t.Fatalf("round trip lost Fixtures: %+v", got.Fixtures)
	}

	p2, err := Create(RunsRoot(t.TempDir()), "web", "login", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m2 := Manifest{App: "web", Flow: "login", RunID: "r1", Mode: ModeStandalone, Capture: CaptureTrust{Status: trace.VerdictOK}, Wire: Counts{Recorded: true}}
	if err := WriteManifest(p2, &m2); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	raw, err := os.ReadFile(p2.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var any map[string]json.RawMessage
	if err := json.Unmarshal(raw, &any); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if _, present := any["fixtures"]; present {
		t.Fatalf(`"fixtures" key present for an ordinary (non-fixtures) run: %s`, raw)
	}
}

// TestWriteManifestRejectsZeroValueCaptureStatus — review finding 1
// (Critical). trace.Verdict("").Worse or .Worse("") ranks equal to
// trace.VerdictOK (verdictRank has no entry for "", so the map lookup
// yields 0 — same rank as VerdictOK), which would make an unassessed
// capture gate as clean. WriteManifest fails loud instead of writing a
// zero verdict; it does not invent an "unknown" trace.Verdict value
// (that belongs to core/trace, outside this task).
func TestWriteManifestRejectsZeroValueCaptureStatus(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble}
	// m.Capture is the zero value here: Status == "".
	if err := WriteManifest(p, &m); err == nil {
		t.Fatal("WriteManifest must reject a zero-value Capture.Status")
	}
}

// TestWriteManifestStampsCallersSchema — review finding 9 (Minor).
// WriteManifest takes *Manifest so the caller's own copy also carries the
// stamped Schema after a successful write, not just the copy on disk.
func TestWriteManifestStampsCallersSchema(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{
		App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble,
		Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"},
		Wire:    Counts{Recorded: true},
	}
	if err := WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if m.Schema != Schema {
		t.Fatalf("caller's m.Schema = %q, want %q", m.Schema, Schema)
	}
}

// TestWriteManifestDefaultsNilSlicesToEmptyArrays — review finding 6
// (Major). Checkpoints and Groups must serialize as [] when unset, never
// as null: a TS consumer doing manifest.checkpoints.map(...) must not
// throw on a flow with no checkpoints.
func TestWriteManifestDefaultsNilSlicesToEmptyArrays(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{
		App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble,
		Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"},
		Wire:    Counts{Recorded: true},
	}
	// m.Checkpoints and m.Groups are both nil here.
	if err := WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	raw, err := os.ReadFile(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), `"checkpoints": []`) {
		t.Fatalf("checkpoints must serialize as [], not null: %s", raw)
	}
	if !strings.Contains(string(raw), `"groups": []`) {
		t.Fatalf("groups must serialize as [], not null: %s", raw)
	}
}

// TestReadManifestRejectsSchemaMismatch — review finding 10 (Minor). The
// Schema constant exists to version the file; a reader that never checks
// it lets encoding/json silently zero fields it doesn't recognize — which,
// via finding 1, would make a future-schema manifest gate as clean.
func TestReadManifestRejectsSchemaMismatch(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	body := `{"schema":"retrace/99","capture":{"status":"ok","summary":"ok"}}` + "\n"
	if err := os.WriteFile(p.ManifestPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadManifest(p.ManifestPath); err == nil {
		t.Fatal("ReadManifest must reject a schema it doesn't recognize")
	}
}

// TestReadManifestRejectsZeroValueCaptureStatus — re-review residual on
// finding 1. WriteManifest already rejects a zero Capture.Status at write
// time; this pins the same rule at read time, for a manifest this build
// did not write (an older build, or a hand-edited reference bundle —
// reference bundles are committed to git and therefore hand-editable).
// Without this, `{"schema":"retrace/1","capture":{"status":""}}` reads
// back cleanly and, via trace.Verdict.Worse's zero-value trap, gates as
// clean.
func TestReadManifestRejectsZeroValueCaptureStatus(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	body := `{"schema":"retrace/1","capture":{"status":""}}` + "\n"
	if err := os.WriteFile(p.ManifestPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadManifest(p.ManifestPath); err == nil {
		t.Fatal("ReadManifest must reject a zero-value Capture.Status, the same as WriteManifest does")
	}
}

// TestWriteManifestRejectsACountsThatForgotRecorded pins F3's write-seam
// guard for the exact defect class it exists to catch: a construction site
// that reports a real Calls count but forgets to set Recorded (leaving it
// at its false zero value) — e.g. `runs.Counts{Calls: len(wireHops)}`. This
// is the whole argument for F4's inversion: a forgotten field must fail
// loudly instead of silently asserting a clean, unrecorded-but-populated
// plane. Without validateCounts, this exact manifest round-trips fine.
func TestWriteManifestRejectsACountsThatForgotRecorded(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{
		App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble,
		Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"},
		Wire:    Counts{Calls: 12}, // Recorded forgotten — left at its false zero value
	}
	if err := WriteManifest(p, &m); err == nil {
		t.Fatal("WriteManifest must reject Wire.Calls != 0 with Recorded left false — a forgotten Recorded must fail loudly, not assert a clean wire plane")
	}
}

// TestWriteManifestRequiresAReasonWhenRecordedIsFalse pins the fourth bullet
// of F3: absence (Recorded false) must not be half-written — a caller that
// claims a plane was not recorded must say why.
func TestWriteManifestRequiresAReasonWhenRecordedIsFalse(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{
		App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble,
		Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"},
		// Wire is the zero value here: Recorded false, Calls 0, Reason "".
	}
	if err := WriteManifest(p, &m); err == nil {
		t.Fatal("WriteManifest must reject Recorded=false with no Reason explaining why")
	}
}

// TestWriteManifestRejectsANonNilHopsClaimingAbsence pins F3's first bullet:
// for Hops, absence has exactly one encoding — the nil pointer (see
// Manifest.Hops's doc comment). A non-nil Hops with Recorded false is a
// second, contradictory spelling of "absent" and must never reach disk.
func TestWriteManifestRejectsANonNilHopsClaimingAbsence(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{
		App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble,
		Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"},
		Wire:    Counts{Recorded: true},
		Hops:    &Counts{Calls: 40, Reason: "chain truncated"}, // Recorded left false
	}
	if err := WriteManifest(p, &m); err == nil {
		t.Fatal("WriteManifest must reject a non-nil Hops with Recorded false — for Hops, absence is the nil pointer, not recorded:false")
	}
}

// TestReadManifestRejectsACountsThatForgotRecorded mirrors
// TestWriteManifestRejectsACountsThatForgotRecorded at the read seam — a
// hand-edited or older-build manifest can carry the same self-contradictory
// state on disk even though this build's own WriteManifest would refuse to
// write it.
func TestReadManifestRejectsACountsThatForgotRecorded(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	body := `{"schema":"retrace/1","capture":{"status":"ok","summary":"ok"},"wire":{"calls":12,"recorded":false}}` + "\n"
	if err := os.WriteFile(p.ManifestPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadManifest(p.ManifestPath); err == nil {
		t.Fatal("ReadManifest must reject wire.calls != 0 with wire.recorded false, the same as WriteManifest does")
	}
}

// TestCountsRecordedAndReasonRoundTripThroughRealJson is the item-5 text
// test, updated for F4's inversion (Missing bool -> Recorded bool, zero
// value now the protective "not recorded" reading): parses real JSON text
// (not a struct literal) and asserts Recorded and Reason arrive with the
// exact key spelling, and that Recorded true with Calls 0 ("recorded, none
// happened") is distinguishable on the wire from Recorded false with a
// Reason ("not recorded, and why").
func TestCountsRecordedAndReasonRoundTripThroughRealJson(t *testing.T) {
	var recorded Counts
	if err := json.Unmarshal([]byte(`{"calls":0,"recorded":true}`), &recorded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !recorded.Recorded || recorded.Calls != 0 {
		t.Fatalf("recorded-and-empty must be Recorded=true Calls=0, got %+v", recorded)
	}

	var absent Counts
	if err := json.Unmarshal([]byte(`{"calls":0,"recorded":false,"reason":"wire capture disabled"}`), &absent); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if absent.Recorded || absent.Reason != "wire capture disabled" {
		t.Fatalf("absent must be Recorded=false with Reason set, got %+v", absent)
	}

	// Marshal side: Recorded must never be omitted (never omitempty), Reason
	// must be omitted when blank.
	b, err := json.Marshal(Counts{Calls: 3, Recorded: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"recorded":true`) {
		t.Fatalf(`Counts{Calls:3, Recorded:true} must serialize "recorded":true explicitly, got %s`, b)
	}
	if strings.Contains(string(b), "reason") {
		t.Fatalf("a blank Reason must be omitted (omitempty), got %s", b)
	}

	// The zero value must not omit recorded either — that is precisely how
	// "absent" and "fine" would end up as the same bytes on disk.
	zeroB, err := json.Marshal(Counts{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(zeroB), `"recorded":false`) {
		t.Fatalf(`Counts{} (the zero value) must serialize "recorded":false explicitly, got %s`, zeroB)
	}

	b2, err := json.Marshal(Counts{Reason: "standalone mode"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b2), `"recorded":false`) || !strings.Contains(string(b2), `"reason":"standalone mode"`) {
		t.Fatalf("Counts with Reason set (Recorded left at its false zero value) must serialize both, got %s", b2)
	}
}

func TestReadHopsSkipsBlankLinesAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/hops.jsonl"
	body := "{\"schema\":\"ensemble/1\",\"seq\":1,\"to\":\"bff\"}\n\n{\"schema\":\"ensemble/1\",\"seq\":2,\"to\":\"cart\"}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hops, skipped, err := ReadHops(path)
	if err != nil {
		t.Fatalf("ReadHops: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(hops) != 2 || hops[1].To != "cart" {
		t.Fatalf("hops = %+v", hops)
	}
	missing, skippedMissing, err := ReadHops(dir + "/nope.jsonl")
	if err != nil || missing != nil || skippedMissing != 0 {
		t.Fatalf("missing file must be (nil, 0, nil), got (%v, %d, %v)", missing, skippedMissing, err)
	}
}

// TestReadHopsToleratesCorruptLinesAndKeepsGoodOnes — review finding 4
// (Major). ReadHops must be fail-open like ReadGroupRecords: a half-written
// record from a killed test process is skipped and counted, and hops
// parsed successfully on either side of it must not be discarded.
func TestReadHopsToleratesCorruptLinesAndKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/hops.jsonl"
	body := "{\"schema\":\"ensemble/1\",\"seq\":1,\"to\":\"bff\"}\n" +
		"{not json\n" +
		"{\"schema\":\"ensemble/1\",\"seq\":2,\"to\":\"cart\"}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hops, skipped, err := ReadHops(path)
	if err != nil {
		t.Fatalf("a corrupt line must not fail the whole read: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if len(hops) != 2 || hops[0].To != "bff" || hops[1].To != "cart" {
		t.Fatalf("intact hops on both sides of the corrupt line must be kept: %+v", hops)
	}
}

// TestReadHopsCountsValidJSONNonHopAsSkipped — re-review residual on
// finding 4. "{}" is valid JSON and unmarshals cleanly into a zero-value
// trace.Hop (json.Unmarshal doesn't complain about missing fields), so
// unlike "{not json" it was NOT caught by the existing corrupt-line check
// and was silently counted as a real hop, inflating the count. Every hop
// this package's writers produce carries trace.SchemaVersion (trace.Writer
// stamps it unconditionally), so its absence is what distinguishes "not
// actually a hop record" from a genuine hop.
func TestReadHopsCountsValidJSONNonHopAsSkipped(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/hops.jsonl"
	body := "{\"schema\":\"ensemble/1\",\"seq\":1,\"to\":\"bff\"}\n" +
		"{}\n" +
		"{\"schema\":\"ensemble/1\",\"seq\":2,\"to\":\"cart\"}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hops, skipped, err := ReadHops(path)
	if err != nil {
		t.Fatalf("ReadHops: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 — a valid-JSON-but-not-a-hop line must count as skipped", skipped)
	}
	if len(hops) != 2 || hops[0].To != "bff" || hops[1].To != "cart" {
		t.Fatalf("the empty object must not be returned as a phantom zero-value hop: %+v", hops)
	}
}
