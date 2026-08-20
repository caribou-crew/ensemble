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
