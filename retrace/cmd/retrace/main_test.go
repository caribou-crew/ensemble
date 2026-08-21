package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/buildinfo"
)

// TestRunWithNoArgsPrintsUsageAndExitsUsage — review finding 8 (Major).
// main.go's run(args, stdout, stderr) seam exists specifically so this
// package's exit-code contract can be tested in-process rather than by
// exec'ing a subprocess (which `go run` cannot do reliably — see the
// review's flagged item on `go run`'s own exit code masking the child's).
func TestRunWithNoArgsPrintsUsageAndExitsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run(nil, &stdout, &stderr)
	if got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "retrace —") {
		t.Fatalf("stderr = %q, want usage text", stderr.String())
	}
}

// The resolved version (buildinfo.Resolve enriches the unstamped "dev" this
// test binary carries with the commit it was built from — see
// core/buildinfo's own tests) is what --version must print, not the raw
// "dev" package var.
func TestRunVersionPrintsVersionAndExitsOK(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run([]string{"--version"}, &stdout, &stderr)
	if got != exitOK {
		t.Fatalf("exit = %d, want %d", got, exitOK)
	}
	want := buildinfo.Resolve(version)
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHelpPrintsUsageToStdoutAndExitsOK(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run([]string{"-h"}, &stdout, &stderr)
	if got != exitOK {
		t.Fatalf("exit = %d, want %d", got, exitOK)
	}
	if !strings.Contains(stdout.String(), "retrace —") {
		t.Fatalf("stdout = %q, want usage text", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// TestRunUnknownCommandFailsWithUsage guards against the exact regression
// the review's failure scenario describes: a later subcommand reordering
// the switch so --version falls through to default, silently breaking
// every release script that gates on `retrace --version`.
func TestRunUnknownCommandFailsWithUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run([]string{"bogus"}, &stdout, &stderr)
	if got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `retrace: unknown command "bogus"`) {
		t.Fatalf("stderr = %q, want the unknown-command message", stderr.String())
	}
}

// TestUsageDescribesTheShippedDiffContract pins the help text against the
// flags and exit codes `retrace diff` actually implements. The review found
// it still describing the pre-Task-10 contract — "3 usage/IO error", with
// quarantine (now 3's primary meaning in this command) unmentioned and all
// four of --images/--out/--allow-degraded/--no-fail missing from the usage
// line. Documentation that drifts from a CI contract is worse than none:
// a pipeline author reads it and branches on the wrong number.
func TestUsageDescribesTheShippedDiffContract(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--help exit = %d, want 0", code)
	}
	out := stdout.String()
	for _, flag := range []string{"--images", "--out", "--allow-degraded", "--no-fail"} {
		if !strings.Contains(out, flag) {
			t.Errorf("usage never mentions %s, a flag `retrace diff` ships:\n%s", flag, out)
		}
	}
	if !strings.Contains(out, "quarantined") {
		t.Errorf("usage does not name quarantine, the primary meaning of exit 3 in `retrace diff`:\n%s", out)
	}
	if strings.Contains(out, "3 usage/IO error") {
		t.Errorf("usage still describes the pre-Task-10 exit-code contract:\n%s", out)
	}
	if !strings.Contains(out, "does NOT zero a") && !strings.Contains(out, "not zero a quarantine") {
		t.Errorf("usage does not state that --no-fail leaves a quarantine at 3:\n%s", out)
	}
}
