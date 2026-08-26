package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/capture"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func decodeCheck(t *testing.T, out string) checkResult {
	t.Helper()
	var got checkResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode check JSON: %v\n%s", err, out)
	}
	return got
}

// realMarkerDoor serves the ACTUAL marker door over a run directory whose
// owner record this test wrote. Probing a hand-rolled fake would test the
// client against a shape nothing produces; the point of /identify is that
// the door and the prober agree.
func realMarkerDoor(t *testing.T, app, flow, runID, proxyURL string) *httptest.Server {
	t.Helper()
	p := runs.Paths{RunDir: t.TempDir()}
	if err := runs.MarkRunning(p, runs.Running{
		App: app, Flow: flow, RunID: runID, ProxyURL: proxyURL,
		StartedAt: time.Now().Add(-90 * time.Second),
	}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	srv := httptest.NewServer(capture.NewMarkerDoor(p, nil))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckURLNamesTheRunHoldingThePort(t *testing.T) {
	door := realMarkerDoor(t, "web", "checkout", "20260826T090000Z-abc1234", "http://127.0.0.1:53221")
	bin := buildRetrace(t)
	cwd := t.TempDir()

	res := runRetrace(t, bin, cwd, "", "check", "--url", door.URL, "--json")
	if res.code != exitDiff {
		t.Fatalf("exit = %d, want %d — a live retrace run holds this address\n%s%s", res.code, exitDiff, res.stdout, res.stderr)
	}
	got := decodeCheck(t, res.stdout)
	if !got.Held {
		t.Error("held = false though a retrace run answered")
	}
	if len(got.Probes) != 1 {
		t.Fatalf("got %d probes, want 1", len(got.Probes))
	}
	pr := got.Probes[0]
	if !pr.Answered || !pr.IsRetrace {
		t.Fatalf("answered=%v isRetrace=%v, want both true (err: %s)", pr.Answered, pr.IsRetrace, pr.Error)
	}
	if pr.Identity == nil {
		t.Fatal("no identity in the probe result")
	}
	if pr.Identity.PID != os.Getpid() {
		t.Errorf("pid = %d, want the test process %d", pr.Identity.PID, os.Getpid())
	}
	if pr.Identity.RunID != "20260826T090000Z-abc1234" {
		t.Errorf("runId = %q, want the owning run", pr.Identity.RunID)
	}
	if pr.Identity.ProxyURL != "http://127.0.0.1:53221" {
		t.Errorf("proxyUrl = %q — the answer to 'which port does this run hold' is the whole feature", pr.Identity.ProxyURL)
	}
}

func TestCheckURLTextOutputNamesPidAndRun(t *testing.T) {
	door := realMarkerDoor(t, "web", "checkout", "20260826T090000Z-abc1234", "http://127.0.0.1:53221")
	bin := buildRetrace(t)
	res := runRetrace(t, bin, t.TempDir(), "", "check", "--url", door.URL)
	for _, want := range []string{"retrace", "web/checkout/20260826T090000Z-abc1234", "http://127.0.0.1:53221"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("output does not mention %q:\n%s", want, res.stdout)
		}
	}
}

// TestCheckURLOnAFreePortSaysThePortIsFree: the most useful answer this
// command gives. A probe that exited 3 on a refused connection could never
// say it.
func TestCheckURLOnAFreePortSaysThePortIsFree(t *testing.T) {
	// Bind then immediately release, so the address is real and almost
	// certainly unbound — far more reliable than guessing a port number.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	bin := buildRetrace(t)
	res := runRetrace(t, bin, t.TempDir(), "", "check", "--url", addr, "--json")
	// A free port is exit 0: there is nothing of ours to act on, and the ✓
	// this prints must not sit beside a non-zero code.
	if res.code != exitOK {
		t.Fatalf("exit = %d, want 0 for a port nothing holds\n%s%s", res.code, res.stdout, res.stderr)
	}
	got := decodeCheck(t, res.stdout)
	if got.Held {
		t.Error("held = true for a port nothing is listening on")
	}
	pr := got.Probes[0]
	if pr.Answered {
		t.Error("a closed port was reported as having answered")
	}
	if pr.Error == "" {
		t.Error("no error recorded for a refused connection")
	}
	// The bare host:port form must be accepted — that is what an "address
	// already in use" message and lsof both hand the user.
	if !strings.HasPrefix(pr.URL, "http://") {
		t.Errorf("url = %q, want a scheme to have been assumed", pr.URL)
	}
}

// TestCheckURLDoesNotClaimAPortItDoesNotOwn: "something answered" is not
// "retrace answered". Getting this wrong tells a user to go kill an
// unrelated process.
func TestCheckURLDoesNotClaimAPortItDoesNotOwn(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hello":"i am not retrace"}`))
	}))
	defer other.Close()

	bin := buildRetrace(t)
	res := runRetrace(t, bin, t.TempDir(), "", "check", "--url", other.URL, "--json")
	// Not ours, so nothing for retrace to act on: exit 0.
	if res.code != exitOK {
		t.Fatalf("exit = %d, want 0\n%s", res.code, res.stdout)
	}
	got := decodeCheck(t, res.stdout)
	if got.Held {
		t.Error("held = true for a port held by a foreign server")
	}
	pr := got.Probes[0]
	if !pr.Answered {
		t.Error("a live server was reported as not answering")
	}
	if pr.IsRetrace {
		t.Fatal("a foreign server was claimed as retrace — this is how a user gets told to kill the wrong process")
	}
}

func TestCheckSweepIsCleanWhenEveryRunIsFinalized(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	fabricateRun(t, cwd, "web", "checkout", 2*time.Hour, true)
	fabricateRun(t, cwd, "web", "cart", time.Hour, true)

	res := runRetrace(t, bin, cwd, "", "check", "--json")
	if res.code != exitOK {
		t.Fatalf("exit = %d, want 0\n%s%s", res.code, res.stdout, res.stderr)
	}
	got := decodeCheck(t, res.stdout)
	if got.Abandoned != 0 || len(got.Unsupervised) != 0 {
		t.Errorf("abandoned=%d unsupervised=%d, want 0 and 0", got.Abandoned, len(got.Unsupervised))
	}
}

func TestCheckSweepReportsAbandonedRunsAndExitsOne(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	fabricateRun(t, cwd, "web", "checkout", 2*time.Hour, true)
	lost := fabricateRun(t, cwd, "web", "cart", 2*time.Hour, false)

	res := runRetrace(t, bin, cwd, "", "check", "--json")
	if res.code != exitDiff {
		t.Fatalf("exit = %d, want %d when abandoned runs exist\n%s%s", res.code, exitDiff, res.stdout, res.stderr)
	}
	got := decodeCheck(t, res.stdout)
	if got.Abandoned != 1 {
		t.Errorf("abandoned = %d, want 1", got.Abandoned)
	}
	if len(got.Unsupervised) != 1 || got.Unsupervised[0].RunID != lost {
		t.Fatalf("unsupervised = %+v, want just %s", got.Unsupervised, lost)
	}
	// A finalized run must never appear in the sweep — it is the state that
	// needs no attention, and listing it would bury the one that does.
	if got.Unsupervised[0].State != runs.StateAbandoned {
		t.Errorf("state = %q, want abandoned", got.Unsupervised[0].State)
	}
}

// TestCheckSweepProbesTheDoorARunRecorded is the pid-reuse defence: the
// sweep must actually dial the recorded marker URL, not just trust that a
// process with that number exists.
func TestCheckSweepProbesTheDoorARunRecorded(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()

	// A live door, and a run directory whose owner record points at it.
	id := runs.NewRunID(time.Now().Add(-time.Minute), "abc1234def")
	p, err := runs.Create(runs.RunsRoot(cwd), "web", "checkout", id)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(capture.NewMarkerDoor(p, nil))
	defer srv.Close()
	if err := runs.MarkRunning(p, runs.Running{
		App: "web", Flow: "checkout", RunID: id,
		MarkerURL: srv.URL, ProxyURL: "http://127.0.0.1:53221",
		StartedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	res := runRetrace(t, bin, cwd, "", "check", "--json")
	if res.code != exitOK {
		t.Fatalf("exit = %d, want 0 — the owner is alive and the door answers\n%s%s", res.code, res.stdout, res.stderr)
	}
	got := decodeCheck(t, res.stdout)
	if len(got.Probes) != 1 {
		t.Fatalf("got %d probes, want 1 — the sweep did not dial the recorded door", len(got.Probes))
	}
	if !got.Probes[0].IsRetrace {
		t.Errorf("probe did not recognise the door: %+v", got.Probes[0])
	}
	if got.Probes[0].Identity == nil || got.Probes[0].Identity.RunID != id {
		t.Errorf("probe identity did not name run %s: %+v", id, got.Probes[0].Identity)
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:4800":         "http://127.0.0.1:4800",
		"http://127.0.0.1:4800":  "http://127.0.0.1:4800",
		"http://127.0.0.1:4800/": "http://127.0.0.1:4800",
		"https://host:1/":        "https://host:1",
		"  127.0.0.1:1  ":        "http://127.0.0.1:1",
	} {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
