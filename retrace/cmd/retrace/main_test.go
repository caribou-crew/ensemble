package main

import (
	"bytes"
	"strings"
	"testing"
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

func TestRunVersionPrintsVersionAndExitsOK(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run([]string{"--version"}, &stdout, &stderr)
	if got != exitOK {
		t.Fatalf("exit = %d, want %d", got, exitOK)
	}
	if strings.TrimSpace(stdout.String()) != version {
		t.Fatalf("stdout = %q, want %q", stdout.String(), version)
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
