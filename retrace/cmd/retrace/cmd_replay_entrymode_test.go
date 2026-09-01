package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestReplayServesAnEntryRecordedBundleThroughAnEntryPlusUpstreamConfig is
// the regression test for the 0/N-served release blocker: a config setting
// BOTH `entry:` and `upstream:` (sample/retrace.yaml's fallback pattern)
// gets a single "client-edge" listener synthesized by applyDefaults, while
// its bundle — recorded attached to ensemble — carries the ENTRY name as
// every exchange's Target. bindReplayListeners used to set
// TargetFilter="client-edge" on that lone listener, so every request
// missed with "no comparable exchange". A single listener must serve
// unfiltered (nothing could conflict), so the recorded exchange answers.
func TestReplayServesAnEntryRecordedBundleThroughAnEntryPlusUpstreamConfig(t *testing.T) {
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nentry: edge\nupstream: http://127.0.0.1:9080\n")
	cfg, err := config.Load(filepath.Join(cwd, "retrace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The precondition that made this bite: the sugar synthesizes a
	// listener whose name is NOT the entry the bundle was recorded under.
	if len(cfg.Listeners) != 1 || cfg.Listeners[0].Name != "client-edge" {
		t.Fatalf("listeners = %+v, want the synthesized single client-edge entry", cfg.Listeners)
	}

	// A bundle recorded attached to ensemble: Hop.To is the entry name.
	p, err := runs.Create(cwd, "web", "checkout", "20260101T000000Z-abcdef1")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p.WirePath)
	if err != nil {
		t.Fatal(err)
	}
	w := trace.NewWriter(f)
	if err := w.Write(trace.Hop{
		Seq: 1, To: "edge", Method: "GET", Path: "/cart", Status: 200,
		Resp: trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"items":[]}`},
	}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m := runs.Manifest{
		App: "web", Flow: "checkout", RunID: "20260101T000000Z-abcdef1",
		Mode:      runs.ModeEnsemble,
		StartedAt: time.Unix(0, 0).UTC(), FinishedAt: time.Unix(1, 0).UTC(),
		Capture: runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"},
		Wire:    runs.Counts{Calls: 1, Recorded: true},
	}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatal(err)
	}

	opts, err := replayOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lns, err := bindReplayListeners(cfg.Listeners, "127.0.0.1:0", p.RunDir, cfg.Dir, opts, filepath.Join(cwd, "misses.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rl := range lns {
		go rl.httpSrv.Serve(rl.ln)
		defer rl.httpSrv.Close()
	}

	resp, err := http.Get("http://" + lns[0].ln.Addr().String() + "/cart")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != `{"items":[]}` {
		t.Fatalf("replay answered %d %q, want the recorded 200 {\"items\":[]} — the entry-recorded exchange must be served, not filtered out", resp.StatusCode, body)
	}
	if served := lns[0].srv.ServedCount(); served == 0 {
		t.Fatalf("served = %d, want > 0", served)
	}
	// And the name mirrors the (absent) filter, so --assert-requests'
	// reference-hop filtering can't disagree with what was served.
	if lns[0].name != "" {
		t.Fatalf("replayListener.name = %q, want empty for an unfiltered single listener", lns[0].name)
	}
}
