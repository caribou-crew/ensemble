package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCmdMCPRejectsInvalidAPIURLWithoutProtocolNoise(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdMCP([]string{"--api-url", "file:///tmp/ensemble"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout contains non-protocol output: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "invalid Ensemble API URL") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestCmdMCPRejectsUnexpectedArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdMCP([]string{"extra"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout contains non-protocol output: %q", stdout.String())
	}
}
