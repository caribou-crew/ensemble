package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestHelperRecordsEdgeAndAuth is the recording-leg test command for the
// multi-listener replay tests below: it calls each listener's own path
// through its own env var, so wire.jsonl carries one hop per target.
func TestHelperRecordsEdgeAndAuth(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "record-edge-and-auth" {
		return
	}
	fetch := func(env, path string) {
		url := os.Getenv(env)
		if url == "" {
			fmt.Fprintln(os.Stderr, "helper: missing", env)
			os.Exit(9)
		}
		resp, err := http.Get(url + path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch via", env, ":", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	fetch("RETRACE_PROXY_URL_EDGE", "/edge-only")
	fetch("RETRACE_PROXY_URL_AUTH", "/auth-only")
	os.Exit(0)
}

// TestHelperReplaysEdgeAndAuthCorrectly is the replay-leg test command that
// calls each listener's own recorded path through its own env var — the
// routing a well-behaved app performs, and the case that must stay a clean
// pass.
func TestHelperReplaysEdgeAndAuthCorrectly(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-edge-and-auth-correctly" {
		return
	}
	for _, c := range []struct{ env, path string }{
		{"RETRACE_PROXY_URL_EDGE", "/edge-only"},
		{"RETRACE_PROXY_URL_AUTH", "/auth-only"},
	} {
		resp, err := http.Get(os.Getenv(c.env) + c.path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch via", c.env, ":", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	os.Exit(0)
}

// TestHelperReplaysAuthsPathOnTheEdgePort deliberately sends a request that
// was only ever recorded through the auth listener to the edge listener's
// port — the cross-listener leak task 4/5 exist to close. It must come
// back as a miss (a 501, not the auth listener's recorded body), so the
// helper does not even inspect the response — the replay's own exit code
// carries the assertion.
func TestHelperReplaysAuthsPathOnTheEdgePort(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-auth-path-on-edge-port" {
		return
	}
	resp, err := http.Get(os.Getenv("RETRACE_PROXY_URL_EDGE") + "/auth-only")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

func recordAndAcceptTwoListeners(t *testing.T, bin, cwd string, edge, auth *httptest.Server) {
	t.Helper()
	writeConfig(t, cwd, fmt.Sprintf("app: web\nlisteners:\n  - name: edge\n    upstream: %s\n  - name: auth\n    upstream: %s\n", edge.URL, auth.URL))
	args := append([]string{"run", "--flow", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperRecordsEdgeAndAuth")...)
	res := runRetrace(t, bin, cwd, "record-edge-and-auth", args...)
	if res.code != 0 {
		t.Fatalf("retrace run: exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	acc := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web")
	if acc.code != 0 {
		t.Fatalf("ref accept: exit = %d\nstdout: %s\nstderr: %s", acc.code, acc.stdout, acc.stderr)
	}
}

func twoListenerUpstreams() (edge, auth *httptest.Server) {
	edge = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"from":"edge"}`))
	}))
	auth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"from":"auth"}`))
	}))
	return edge, auth
}

func TestReplayServesEachListenersOwnRecordedCallsOnItsOwnPort(t *testing.T) {
	edge, auth := twoListenerUpstreams()
	defer edge.Close()
	defer auth.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	recordAndAcceptTwoListeners(t, bin, cwd, edge, auth)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplaysEdgeAndAuthCorrectly")...)
	res := runRetrace(t, bin, cwd, "replay-edge-and-auth-correctly", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — each listener replayed its own recorded call\nstdout: %s\nstderr: %s",
			res.code, res.stdout, res.stderr)
	}
}

func TestReplayMissesACallSentToTheWrongListenersPort(t *testing.T) {
	edge, auth := twoListenerUpstreams()
	defer edge.Close()
	defer auth.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	recordAndAcceptTwoListeners(t, bin, cwd, edge, auth)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplaysAuthsPathOnTheEdgePort")...)
	res := runRetrace(t, bin, cwd, "replay-auth-path-on-edge-port", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d — the auth listener's recorded call must never answer on the edge listener's port\nstdout: %s\nstderr: %s",
			res.code, exitGate, res.stdout, res.stderr)
	}
	out := res.stdout + res.stderr
	if !strings.Contains(out, "/auth-only") {
		t.Fatalf("report never names the unmatched call:\n%s", out)
	}
}
