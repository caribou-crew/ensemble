package main

// cmd_export_test.go drives `retrace export` through a BUILT binary, never
// `go run` (which collapses every non-zero child to 1 — see
// global-constraints.md). The exit code is the whole point of this command:
// the brief's stated design is that `retrace export` can be the ONLY step in
// a CI job, so the number the process returns IS the build result.

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- fixtures -----------------------------------------------------------

var (
	expWhite = color.RGBA{255, 255, 255, 255}
	expBlue  = color.RGBA{0, 0, 255, 255}
)

func expShot(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding a fixture shot: %v", err)
	}
	return buf.Bytes()
}

const (
	expRunA = "20260821T100000Z-aaaaaaa"
	expRunB = "20260821T101000Z-bbbbbbb"
)

// expRecord writes one run directory the way `retrace run` leaves it, through
// runs.Create/runs.WriteManifest — the production writers, so the manifest is
// one the real recorder could have produced rather than one this test typed.
func expRecord(t *testing.T, cwd, app, flow, runID string, shot []byte, status int, body string, trust runs.CaptureTrust) {
	t.Helper()
	p, err := runs.Create(runs.RunsRoot(cwd), app, flow, runID)
	if err != nil {
		t.Fatalf("runs.Create(%s/%s/%s): %v", app, flow, runID, err)
	}
	if err := os.WriteFile(filepath.Join(p.ShotsDir, "home.png"), shot, 0o644); err != nil {
		t.Fatalf("writing a fixture shot: %v", err)
	}
	h := trace.Hop{
		Schema: trace.SchemaVersion, Seq: 1, From: "web", To: "api",
		Method: "GET", Path: "/" + flow, Status: status,
		T:    trace.Timings{Start: time.Date(2026, 8, 21, 10, 0, 1, 0, time.UTC), DoneMs: 10},
		Resp: trace.Payload{Headers: map[string]string{"content-type": "application/json"}, Body: body},
	}
	line, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshalling a fixture hop: %v", err)
	}
	if err := os.WriteFile(p.WirePath, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("writing wire.jsonl: %v", err)
	}
	m := runs.Manifest{
		App: app, Flow: flow, RunID: runID, Mode: runs.ModeStandalone,
		Git:         runs.Git{SHA: "deadbee", Branch: "main"},
		StartedAt:   time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		FinishedAt:  time.Date(2026, 8, 21, 10, 0, 5, 0, time.UTC),
		Capture:     trust,
		Wire:        runs.Counts{Calls: 1, Recorded: true},
		Checkpoints: []runs.Checkpoint{{Name: "home", File: "shots/home.png", Width: 24, Height: 24}},
	}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest(%s): %v", runID, err)
	}
}

var expOK = runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"}

// expFlow records one flow in one of the four states diff.ExitCode has a
// value for. Every arm goes through refs.Accept for its reference, so side A
// is a real committed bundle rather than a hand-copied directory.
func expFlow(t *testing.T, cwd, app, flow, kind string) {
	t.Helper()
	expRecord(t, cwd, app, flow, expRunA, expShot(t, expWhite), 200, `{"ok":true}`, expOK)
	if _, err := refs.Accept(refs.AcceptOptions{
		Cwd: cwd, RunsRoot: runs.RunsRoot(cwd), App: app, Flow: flow, RunID: expRunA,
	}); err != nil {
		t.Fatalf("refs.Accept(%s/%s): %v", app, flow, err)
	}
	switch kind {
	case "pass":
		expRecord(t, cwd, app, flow, expRunB, expShot(t, expWhite), 200, `{"ok":true}`, expOK)
	case "changed":
		// Every pixel differs, so this is unambiguously "changed" rather
		// than sitting near a threshold — and no status, gate or capture
		// problem rides along with it, which is what keeps this arm from
		// masking the arm below.
		expRecord(t, cwd, app, flow, expRunB, expShot(t, expBlue), 200, `{"ok":true}`, expOK)
	case "failed":
		// A 500 no expected-status rule excuses: a hard gate, and the shot
		// is IDENTICAL so nothing else can produce this verdict.
		expRecord(t, cwd, app, flow, expRunB, expShot(t, expWhite), 500, `{"error":"boom"}`, expOK)
	case "quarantined":
		// The capture's own trust verdict is not "ok", so serve refuses to
		// compare at all (it never passes --allow-degraded). Identical shot
		// and identical status: the ONLY thing that can make this arm
		// non-pass is the quarantine.
		expRecord(t, cwd, app, flow, expRunB, expShot(t, expWhite), 200, `{"ok":true}`, runs.CaptureTrust{
			Status:  trace.VerdictBroken,
			Summary: "the proxy recorded nothing for 40 seconds",
			Reasons: []runs.TrustReason{{Code: "quiet-stretch", Status: trace.VerdictBroken, Detail: "40s with no traffic"}},
		})
	default:
		t.Fatalf("unknown fixture kind %q", kind)
	}
}

// --- tests --------------------------------------------------------------

// The CI contract, measured as a real process exit status.
//
// diff.ExitCode has FOUR values and the quarantined one is the HIGHEST, so
// the pairing below is not "quarantined alone" — a lone quarantined flow
// would pass against any mapping that happens to order the other three
// correctly. It is quarantined exported ALONGSIDE a failing flow, which
// pins 3 as the maximum: a run NOBODY COULD EVALUATE must not exit like a
// run that was evaluated and found broken, let alone like one that passed.
func TestExportExitsWithTheWorstVerdictItExported(t *testing.T) {
	bin := buildRetrace(t)
	for _, tc := range []struct {
		name  string
		kinds []string
		want  int
	}{
		{"two passes", []string{"pass", "pass"}, 0},
		{"a pass and a changed", []string{"pass", "changed"}, 1},
		{"a pass and a failure", []string{"pass", "failed"}, 2},
		{"a failure and a quarantine", []string{"failed", "quarantined"}, 3},
		// The other order, because "max" must not be "the last one wins":
		// exporting the quarantine FIRST has to reach the same 3.
		{"a quarantine and a failure", []string{"quarantined", "failed"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			for i, kind := range tc.kinds {
				expFlow(t, cwd, "web", string(rune('a'+i))+"flow", kind)
			}
			out := filepath.Join(t.TempDir(), "report")
			res := runRetrace(t, bin, cwd, "", "export", "--out", out, "--json")
			if res.code != tc.want {
				t.Fatalf("exit = %d, want %d for %v\nstdout: %s\nstderr: %s", res.code, tc.want, tc.kinds, res.stdout, res.stderr)
			}
			// And the same number is a VALUE on the document an agent
			// reads, not only the status of a process it did not watch.
			var got struct {
				Dir      string   `json:"dir"`
				Files    []string `json:"files"`
				Items    int      `json:"items"`
				ExitCode int      `json:"exitCode"`
			}
			if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
				t.Fatalf("export --json: %v\n%s", err, res.stdout)
			}
			if got.ExitCode != tc.want {
				t.Fatalf("ExportResult.exitCode = %d, want %d", got.ExitCode, tc.want)
			}
			if got.Items != len(tc.kinds) {
				t.Fatalf("items = %d, want %d", got.Items, len(tc.kinds))
			}
			if _, err := os.Stat(filepath.Join(out, "index.html")); err != nil {
				t.Fatalf("no overview was written: %v", err)
			}
		})
	}
}

// --out has no default, and the refusal says why rather than writing a
// report into whatever directory the job was standing in.
func TestExportWithoutAnOutDirectoryRefusesRatherThanGuessing(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	expFlow(t, cwd, "web", "login", "pass")
	res := runRetrace(t, bin, cwd, "", "export")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.code, exitUsage, res.stderr)
	}
	if !strings.Contains(res.stderr, "--out") {
		t.Fatalf("the refusal does not name the flag: %s", res.stderr)
	}
}

// A typo'd --flow is "could not evaluate" (3), never a quiet 0 over a report
// containing nothing.
func TestExportOfAFlowThatDoesNotExistExitsCouldNotEvaluate(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	expFlow(t, cwd, "web", "login", "pass")
	out := filepath.Join(t.TempDir(), "report")
	res := runRetrace(t, bin, cwd, "", "export", "--out", out, "--flow", "lgoin")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "lgoin") {
		t.Fatalf("the refusal does not name the flow it could not find: %s", res.stderr)
	}
}

// A project that has recorded nothing exports a valid, empty report — and
// says so where a CI log will show it. The exit code below it is 0 because
// no flow contributed anything worse; the sentence is what keeps that 0 from
// reading as "everything passed".
func TestExportOfAProjectWithNothingRecordedSaysSoOnStderr(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	out := filepath.Join(t.TempDir(), "report")
	res := runRetrace(t, bin, cwd, "", "export", "--out", out)
	if res.code != exitOK {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.code, exitOK, res.stderr)
	}
	if !strings.Contains(res.stderr, "contains no flows") {
		t.Fatalf("an empty export said nothing about being empty: %q", res.stderr)
	}
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatalf("no overview: %v", err)
	}
	if strings.Contains(strings.ToLower(string(index)), "all clear") {
		t.Fatalf("a project with no runs exported an all-clear report")
	}
}
