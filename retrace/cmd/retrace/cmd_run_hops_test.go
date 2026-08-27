package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// oracleHops is the chain both halves of the byte-equality test record. One
// hop carries a secret in a header so the comparison also proves the two
// paths redact — not merely that they agree.
func oracleHops() []trace.Hop {
	edge := trace.Hop{
		Schema: trace.SchemaVersion, Seq: 1, TraceID: "t-1",
		To: "edge", Method: "GET", Path: "/cart", Status: 200,
	}
	edge.Req.Headers = map[string]string{"x-team-token": "super-secret-value"}
	inner := trace.Hop{
		Schema: trace.SchemaVersion, Seq: 2, TraceID: "t-1",
		From: "edge", To: "bff", Method: "GET", Path: "/cart/items", Status: 200,
	}
	return []trace.Hop{edge, inner}
}

// ndjson renders hops the way any exporter would have to: one core/trace hop
// per line, written with trace.NewWriter so the fixture bytes are the schema's
// own and not hand-rolled.
func ndjson(t *testing.T, hops []trace.Hop) string {
	t.Helper()
	var buf bytes.Buffer
	w := trace.NewWriter(&buf)
	for _, h := range hops {
		if err := w.Write(h); err != nil {
			t.Fatal(err)
		}
	}
	return buf.String()
}

func hopScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return "./" + name
}

func hopsFileOf(t *testing.T, cwd, app, flow, runID string) []byte {
	t.Helper()
	p, err := runs.PathsFor(runs.RunsRoot(cwd), app, flow, runID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p.HopsPath)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestAnExportedChainLandsByteEqualWithTheEnsemblePath is this section's
// oracle. A hop plane that reached disk from a tracing backend must be
// INDISTINGUISHABLE from one ensemble produced for the same traffic —
// otherwise `retrace diff` is comparing two files that only look like the
// same kind of thing, and every hop-plane verdict depends on which path
// recorded which side.
//
// Byte-equality, not "equivalent after decoding": the recording is the
// artifact, it is committed, and two encoders that agree today drift apart
// the first time one of them gains a field.
func TestAnExportedChainLandsByteEqualWithTheEnsemblePath(t *testing.T) {
	hops := oracleHops()
	bin := buildRetrace(t)
	const cfgRedact = "redact:\n  - x-team-token\n"

	// Side A: attached to ensemble, which drains the chain from its own proxies.
	api := newEnsembleAPI(t, hops)
	attached := t.TempDir()
	writeConfig(t, attached, "app: web\nentry: bff\n"+cfgRedact)
	res := runRetrace(t, bin, attached, "fetch",
		append([]string{"run", "--flow", "checkout", "--ensemble", api.URL},
			selfCmd(t, "TestHelperFetchesThroughProxy")...)...)
	if res.code != 0 {
		t.Fatalf("attached run: exit = %d\nstderr: %s", res.code, res.stderr)
	}
	mA := onlyManifest(t, attached, "web", "checkout")
	if mA.Mode != runs.ModeEnsemble {
		t.Fatalf("side A recorded as %q — the oracle needs a real attached run\nstderr: %s", mA.Mode, res.stderr)
	}

	// Side B: no ensemble anywhere; an export script hands over the same chain.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	exported := t.TempDir()
	if err := os.WriteFile(filepath.Join(exported, "chain.jsonl"), []byte(ndjson(t, hops)), 0o644); err != nil {
		t.Fatal(err)
	}
	dump := hopScript(t, exported, "dump.sh", "cat chain.jsonl\n")
	writeConfig(t, exported, "app: web\n"+cfgRedact+"hops:\n  source:\n    export: "+dump+"\n")
	res = runRetrace(t, bin, exported, "fetch",
		append([]string{"run", "--flow", "checkout", "--upstream", upstream.URL},
			selfCmd(t, "TestHelperFetchesThroughProxy")...)...)
	if res.code != 0 {
		t.Fatalf("exported run: exit = %d\nstderr: %s", res.code, res.stderr)
	}
	mB := onlyManifest(t, exported, "web", "checkout")
	if mB.Mode != runs.ModeStandalone {
		t.Fatalf("side B recorded as %q — the oracle needs the non-ensemble path", mB.Mode)
	}

	a := hopsFileOf(t, attached, "web", "checkout", mA.RunID)
	b := hopsFileOf(t, exported, "web", "checkout", mB.RunID)
	if !bytes.Equal(a, b) {
		t.Errorf("hops.jsonl differs between the two paths\nensemble:\n%s\nexported:\n%s", a, b)
	}
	// And the shared Redactor did its job on both, which is the reason the
	// two files go through one writer rather than two.
	if bytes.Contains(a, []byte("super-secret-value")) {
		t.Error("the ensemble path wrote a secret to disk unredacted")
	}
	if bytes.Contains(b, []byte("super-secret-value")) {
		t.Error("the exported path wrote a secret to disk unredacted")
	}
	// The counts have to agree too — a file both sides wrote identically but
	// counted differently would still make the two runs incomparable.
	if mA.Hops == nil || mB.Hops == nil || mA.Hops.Calls != mB.Hops.Calls {
		t.Errorf("manifest.hops = %+v vs %+v, want the same recorded count", mA.Hops, mB.Hops)
	}
}

// TestAnExternalHopSourceIsNamedInTheCaptureRecord: a reader comparing two
// runs needs to know when the chain was collected by different machinery. It
// is provenance, not a complaint, so it must NOT demote the verdict — `diff`
// quarantines any side whose status is not ok, and an external source is a
// supported way to record.
func TestAnExternalHopSourceIsNamedInTheCaptureRecord(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "chain.jsonl"), []byte(ndjson(t, oracleHops())), 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, cwd, "app: web\nhops:\n  source:\n    file: ./chain.jsonl\n")

	res := runRetrace(t, bin, cwd, "fetch",
		append([]string{"run", "--flow", "checkout", "--upstream", upstream.URL},
			selfCmd(t, "TestHelperFetchesThroughProxy")...)...)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", res.code, res.stderr)
	}
	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Hops == nil || m.Hops.Calls != 2 {
		t.Fatalf("manifest.hops = %+v, want the 2-hop chain the fixture holds", m.Hops)
	}
	if m.Capture.Status != trace.VerdictOK {
		t.Errorf("capture status = %q — an external hop source must not demote a good run\nreasons: %+v",
			m.Capture.Status, m.Capture.Reasons)
	}
	// The banner still reports the capture, not the provenance line.
	if m.Capture.Summary != "capture looks complete" {
		t.Errorf("summary = %q, want the clean-capture summary", m.Capture.Summary)
	}
	var found string
	for _, r := range m.Capture.Reasons {
		if r.Code == "hop-source" {
			found = r.Detail
		}
	}
	if !strings.Contains(found, "file") {
		t.Errorf("the capture record does not name the hop source: %+v", m.Capture.Reasons)
	}
}

// TestAFailedArmRefusesTheRunBeforeTheTestCommandRuns. The config asked for a
// chain; a window that never opened cannot produce one, and a recording that
// silently has no hop plane is indistinguishable from a stack that made no
// downstream calls — the exact confusion this plane exists to end. Failing
// first also costs the user seconds instead of a whole suite.
func TestAFailedArmRefusesTheRunBeforeTheTestCommandRuns(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	arm := hopScript(t, cwd, "on.sh", "echo 'the tracing backend refused' >&2\nexit 1\n")
	dump := hopScript(t, cwd, "dump.sh", "echo 'export ran' > exported.txt\n")
	// The test command would leave a file behind. It must not exist.
	writeConfig(t, cwd, "app: web\nhops:\n  source:\n    arm: "+arm+"\n    export: "+dump+"\n")

	res := runRetrace(t, bin, cwd, "fetch",
		append([]string{"run", "--flow", "checkout", "--upstream", upstream.URL},
			selfCmd(t, "TestHelperFetchesThroughProxy")...)...)
	if res.code == 0 {
		t.Fatalf("a failed arm recorded a run anyway\nstdout: %s", res.stdout)
	}
	if !strings.Contains(res.stderr, "arming the hop source") {
		t.Errorf("the refusal does not say what failed: %s", res.stderr)
	}
	if _, err := os.Stat(filepath.Join(cwd, "exported.txt")); err == nil {
		t.Error("export ran even though arming failed")
	}
	if ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout"); len(ids) != 0 {
		// A directory with no manifest is what `runs` reports as abandoned;
		// a refusal must not leave one behind.
		for _, id := range ids {
			p, err := runs.PathsFor(runs.RunsRoot(cwd), "web", "checkout", id)
			if err != nil {
				continue
			}
			if _, merr := os.Stat(p.ManifestPath); merr == nil {
				t.Errorf("a refused run wrote a manifest: %s", id)
			}
		}
	}
}

// TestAFailedExportKeepsTheRecordingAndSaysSo is the other half of the
// ruling. By the time the export runs, the wire plane and the shots are
// already on disk; throwing a good recording away over a telemetry backend
// that timed out would be the worse trade. The shortfall goes into the
// DURABLE trust record, not only into stderr, which no artifact keeps.
func TestAFailedExportKeepsTheRecordingAndSaysSo(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	dump := hopScript(t, cwd, "dump.sh", "echo 'the backend is down' >&2\nexit 4\n")
	writeConfig(t, cwd, "app: web\nhops:\n  source:\n    export: "+dump+"\n")

	res := runRetrace(t, bin, cwd, "fetch",
		append([]string{"run", "--flow", "checkout", "--upstream", upstream.URL},
			selfCmd(t, "TestHelperFetchesThroughProxy")...)...)
	if res.code != 0 {
		t.Fatalf("a failed export discarded the whole recording: exit = %d\nstderr: %s", res.code, res.stderr)
	}
	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Wire.Calls == 0 {
		t.Error("the wire plane was lost with the hop plane")
	}
	// No chain was recorded, so the manifest must say absent — not zero,
	// which would claim someone looked and found nothing.
	if m.Hops != nil {
		t.Errorf("manifest.hops = %+v, want absent when the export failed", m.Hops)
	}
	var noted bool
	for _, r := range m.Capture.Reasons {
		if strings.Contains(r.Detail, "collecting hops") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("the failure is not in the durable trust record: %+v", m.Capture.Reasons)
	}
}

// TestAnExternalHopSourceDeclinesToAttach: ensemble's session records BOTH
// planes from its own proxies, so an attached run with an external hop source
// would have two producers for one plane and no rule for which wins. The
// configured source wins by being the only one asked — and the run says so
// rather than leaving the reader to infer it from the mode.
func TestAnExternalHopSourceDeclinesToAttach(t *testing.T) {
	api := newEnsembleAPI(t, chainHops())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "chain.jsonl"), []byte(ndjson(t, oracleHops())), 0o644); err != nil {
		t.Fatal(err)
	}
	// entry: bff and a live control plane — everything an attach needs.
	writeConfig(t, cwd, "app: web\nentry: bff\nhops:\n  source:\n    file: ./chain.jsonl\n")

	res := runRetrace(t, bin, cwd, "fetch",
		append([]string{"run", "--flow", "checkout", "--ensemble", api.URL, "--upstream", upstream.URL},
			selfCmd(t, "TestHelperFetchesThroughProxy")...)...)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", res.code, res.stderr)
	}
	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Mode != runs.ModeStandalone {
		t.Fatalf("mode = %q, want standalone: a configured hop source is the only producer asked", m.Mode)
	}
	if !strings.Contains(res.stderr, "hops.source") {
		t.Errorf("the run does not say why it did not attach: %s", res.stderr)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.started) != 0 {
		t.Errorf("a session was registered with ensemble anyway: %v", api.started)
	}
}
