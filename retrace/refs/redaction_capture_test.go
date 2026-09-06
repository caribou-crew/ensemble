package refs

import (
	"os"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/capture"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// A dropped JSON capture carries no secret for the scan to find, but it
// must still fail the capture-trust gate instead of becoming a clean ref.
func TestAcceptRefusesAssessedRedactionFailure(t *testing.T) {
	cwd, root := t.TempDir(), t.TempDir()
	const app, flow, runID = "web", "login", "20260905T000000Z-abcdef1"
	p := writeRun(t, root, app, flow, runID)
	f, err := os.Create(p.WirePath)
	if err != nil {
		t.Fatal(err)
	}
	red, err := trace.NewRedactor(nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Redactor: red, Writer: trace.NewWriter(f)})
	h := rec.Record(trace.Hop{To: "api", Method: "POST", Path: "/login", Status: 200,
		Resp: trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"password":"synthetic-accept-canary","tail":"`, Truncated: true},
	})
	rec.Close()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	m.Capture = capture.Assess(capture.AssessInput{Hops: []trace.Hop{h}, RequestsSeen: 1})
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatal(err)
	}
	opts := AcceptOptions{Cwd: cwd, RunsRoot: root, App: app, Flow: flow, RunID: runID}
	if _, err := Accept(opts); err == nil || !strings.Contains(err.Error(), "capture verdict") {
		t.Fatalf("redaction failure promotion error=%v, want capture refusal", err)
	}
	dir, err := BundleDir(cwd, app, flow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("refusal created a reference: %v", err)
	}
	// The existing explicit override remains available for a knowingly
	// incomplete capture, and the discarded credential cannot return.
	opts.Force = true
	accepted, err := Accept(opts)
	if err != nil {
		t.Fatalf("explicit override: %v", err)
	}
	if accepted.CaptureStatus != trace.VerdictDegraded {
		t.Errorf("forced reference hides degraded status: %s", accepted.CaptureStatus)
	}
	saved, err := runs.ReadManifest(dir + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Capture.Status != trace.VerdictDegraded {
		t.Errorf("reference manifest hides degraded status: %s", saved.Capture.Status)
	}
	wire, err := os.ReadFile(dir + "/wire.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "synthetic-accept-canary") {
		t.Error("forced incomplete reference leaked the dropped credential")
	}
}
