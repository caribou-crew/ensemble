package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
)

// v6LoopbackAvailable probes for a working ::1, so a companion-bind
// assertion can tell "this platform genuinely has no IPv6 loopback" apart
// from "the companion bind is broken".
func v6LoopbackAvailable(t *testing.T) bool {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// TestHelperReplaysEdgeViaIPv6Companion dials the edge listener's IPv6
// companion address directly (not the "127.0.0.1" RETRACE_PROXY_URL_EDGE
// literal) — an app that resolves "localhost" to ::1 (an iOS Simulator,
// say) must still reach a working listener even though retrace advertised
// the IPv4 literal.
func TestHelperReplaysEdgeViaIPv6Companion(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-edge-via-ipv6-companion" {
		return
	}
	edgeURL := os.Getenv("RETRACE_PROXY_URL_EDGE")
	if edgeURL == "" {
		fmt.Fprintln(os.Stderr, "helper: missing RETRACE_PROXY_URL_EDGE")
		os.Exit(9)
	}
	_, port, err := net.SplitHostPort(edgeURL[len("http://"):])
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: parsing", edgeURL, ":", err)
		os.Exit(9)
	}
	resp, err := http.Get("http://" + net.JoinHostPort("::1", port) + "/edge-only")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch via [::1]:", port, ":", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

func TestReplayListenerAlsoAnswersOnIPv6Companion(t *testing.T) {
	if !v6LoopbackAvailable(t) {
		t.Skip("no IPv6 loopback on this platform")
	}
	edge, auth := twoListenerUpstreams()
	defer edge.Close()
	defer auth.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	recordAndAcceptTwoListeners(t, bin, cwd, edge, auth)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplaysEdgeViaIPv6Companion")...)
	res := runRetrace(t, bin, cwd, "replay-edge-via-ipv6-companion", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — the edge listener's IPv6 companion should have answered too\nstdout: %s\nstderr: %s",
			res.code, res.stdout, res.stderr)
	}
}
