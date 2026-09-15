package runs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func writeMinimal(t *testing.T, m *Manifest) Paths {
	t.Helper()
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "run-7")
	if err != nil {
		t.Fatal(err)
	}
	m.Capture = CaptureTrust{Status: "ok"}
	m.Wire = Counts{Recorded: true}
	if err := WriteManifest(p, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	return p
}

func TestEnsembleLinkRoundTrips(t *testing.T) {
	p := writeMinimal(t, &Manifest{Ensemble: &EnsembleLink{API: "http://127.0.0.1:4700", Session: "run-7"}})
	got, err := ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Ensemble == nil || got.Ensemble.API != "http://127.0.0.1:4700" || got.Ensemble.Session != "run-7" {
		t.Fatalf("Ensemble = %+v, want the written link", got.Ensemble)
	}
}

// Asserted on the JSON, not on the decoded struct: "absent" and
// "present and empty" decode identically into a nil-able field, and only
// the bytes on disk can tell a reader that this run claims no attachment.
func TestStandaloneManifestEmitsNoEnsembleKey(t *testing.T) {
	p := writeMinimal(t, &Manifest{})
	raw, err := os.ReadFile(p.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"ensemble"`) {
		t.Fatalf("a run with no control plane emitted an ensemble key:\n%s", raw)
	}
}

// A manifest written before this field existed must decode as unattached,
// not as attached to an empty control plane.
func TestOlderManifestDecodesAsUnattached(t *testing.T) {
	p := writeMinimal(t, &Manifest{})
	raw, err := os.ReadFile(p.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	delete(generic, "ensemble")
	out, _ := json.Marshal(generic)
	if err := os.WriteFile(p.ManifestPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Ensemble != nil {
		t.Fatalf("Ensemble = %+v, want nil for a manifest with no ensemble key", got.Ensemble)
	}
}

// ReadManifest re-checks what WriteManifest checked: a hand-edited bundle,
// or one written by a build without the check, must fail the same way.
func TestReadManifestRejectsAHalfFilledEnsembleLink(t *testing.T) {
	p := writeMinimal(t, &Manifest{})
	raw, err := os.ReadFile(p.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	generic["ensemble"] = map[string]any{"api": "http://127.0.0.1:4700"}
	out, _ := json.Marshal(generic)
	if err := os.WriteFile(p.ManifestPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadManifest(p.ManifestPath); err == nil {
		t.Fatal("ReadManifest accepted an ensemble link with no session id")
	}
}
