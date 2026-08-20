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
	if err := WriteManifest(p, m); err != nil {
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
	if _, ok := any["capture"]; !ok {
		t.Fatal(`capture must always be present — "unknown trust" and "not written yet" must not look alike`)
	}
}

func TestReadHopsSkipsBlankLinesAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/hops.jsonl"
	body := "{\"schema\":\"ensemble/1\",\"seq\":1,\"to\":\"bff\"}\n\n{\"schema\":\"ensemble/1\",\"seq\":2,\"to\":\"cart\"}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hops, err := ReadHops(path)
	if err != nil {
		t.Fatalf("ReadHops: %v", err)
	}
	if len(hops) != 2 || hops[1].To != "cart" {
		t.Fatalf("hops = %+v", hops)
	}
	missing, err := ReadHops(dir + "/nope.jsonl")
	if err != nil || missing != nil {
		t.Fatalf("missing file must be (nil, nil), got (%v, %v)", missing, err)
	}
}
