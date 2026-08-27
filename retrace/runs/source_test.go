package runs

import (
	"os"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestReadSourceOnLocalRunIsNilNil(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	got, err := ReadSource(p)
	if err != nil {
		t.Fatalf("ReadSource on a run with no source.json: %v", err)
	}
	if got != nil {
		t.Fatalf("ReadSource = %+v, want nil for a locally recorded run", got)
	}
}

func TestWriteSourceThenReadRoundTrips(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	want := Source{
		Kind:     SourceKindCI,
		Workflow: "retrace-ios",
		RunURL:   "https://github.com/org/repo/actions/runs/123",
		SHA:      "abc123",
		SyncedAt: fixedNow,
	}
	if err := WriteSource(p, want); err != nil {
		t.Fatalf("WriteSource: %v", err)
	}
	got, err := ReadSource(p)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if got == nil {
		t.Fatal("ReadSource returned nil after WriteSource")
	}
	if got.Schema != SourceSchema {
		t.Errorf("schema = %q, want %q", got.Schema, SourceSchema)
	}
	if got.Kind != want.Kind || got.Workflow != want.Workflow || got.RunURL != want.RunURL || got.SHA != want.SHA {
		t.Errorf("round-trip mismatch: got %+v, want %+v", *got, want)
	}
	if !got.SyncedAt.Equal(want.SyncedAt) {
		t.Errorf("syncedAt = %v, want %v", got.SyncedAt, want.SyncedAt)
	}
}

func TestWriteSourceRejectsEmptyKind(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	if err := WriteSource(p, Source{Workflow: "x"}); err == nil {
		t.Fatal("WriteSource accepted an empty kind")
	}
	if _, statErr := os.Stat(p.SourcePath()); statErr == nil {
		t.Error("a rejected WriteSource still wrote source.json")
	}
}

func TestReadSourceRejectsMismatchedSchema(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	if err := writeJSONFile(p.sourcePath(), map[string]string{"schema": "not-a-real-schema", "kind": "ci"}); err != nil {
		t.Fatalf("writeJSONFile: %v", err)
	}
	if _, err := ReadSource(p); err == nil {
		t.Fatal("ReadSource accepted a source.json with the wrong schema")
	}
}

func TestReadManifestIsUnawareOfSource(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	m := &Manifest{
		App: "shop", Flow: "checkout", RunID: p.RunDir, StartedAt: fixedNow, FinishedAt: fixedNow,
		Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"},
		Wire:    Counts{Recorded: true},
	}
	if err := WriteManifest(p, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if err := WriteSource(p, Source{Kind: SourceKindCI, Workflow: "x", SyncedAt: fixedNow}); err != nil {
		t.Fatalf("WriteSource: %v", err)
	}
	got, err := ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.App != "shop" || got.Flow != "checkout" {
		t.Fatalf("ReadManifest returned unexpected content after a sibling source.json was written: %+v", got)
	}
}
