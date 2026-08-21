package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestCLI_TUIFailsWhenUnreachable mirrors
// TestCLI_DashboardFailsWhenUnreachable: with nothing listening, `ensemble
// tui` must fail with a clear message and a non-zero exit, and never get
// as far as taking over the terminal.
func TestCLI_TUIFailsWhenUnreachable(t *testing.T) {
	unreachable := fmt.Sprintf("http://127.0.0.1:%d", freePort(t))

	var stdout, stderr bytes.Buffer
	code := run([]string{"tui", "--api-url", unreachable}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit, stdout = %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "ensemble up") {
		t.Fatalf("stderr = %q, want a hint to run `ensemble up`", stderr.String())
	}
}

func TestParseUpOptionsTUIFlag(t *testing.T) {
	var stderr bytes.Buffer

	opts, err := parseUpOptions(nil, &stderr)
	if err != nil {
		t.Fatalf("parseUpOptions: %v", err)
	}
	if opts.TUI {
		t.Fatal("expected --tui to default to false")
	}

	opts, err = parseUpOptions([]string{"--tui"}, &stderr)
	if err != nil {
		t.Fatalf("parseUpOptions --tui: %v", err)
	}
	if !opts.TUI {
		t.Fatal("expected --tui to set opts.TUI")
	}
}

func TestTuiAPIURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:4700": "http://127.0.0.1:4700",
		":4700":          "http://127.0.0.1:4700",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
	}
	for addr, want := range cases {
		if got := tuiAPIURL(addr); got != want {
			t.Errorf("tuiAPIURL(%q) = %q, want %q", addr, got, want)
		}
	}
}
