package main

import (
	"encoding/json"
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

// TestHelperReplayCallsEdgeThreeTimesAndAuthOnce drifts only the edge
// listener's call count (3x against a 1x reference) while calling auth
// exactly as recorded — the scoping case: a surplus on one listener must
// not be misattributed to, or hidden by, the other listener's clean traffic.
func TestHelperReplayCallsEdgeThreeTimesAndAuthOnce(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-edge-drift-auth-clean" {
		return
	}
	edgeURL := os.Getenv("RETRACE_PROXY_URL_EDGE")
	for i := 0; i < 3; i++ {
		resp, err := http.Get(edgeURL + "/edge-only")
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch via edge:", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	resp, err := http.Get(os.Getenv("RETRACE_PROXY_URL_AUTH") + "/auth-only")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch via auth:", err)
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

func TestReplayAssertRequestsPassesWhenBothListenersMatchExactly(t *testing.T) {
	edge, auth := twoListenerUpstreams()
	defer edge.Close()
	defer auth.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	recordAndAcceptTwoListeners(t, bin, cwd, edge, auth)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--assert-requests", "--json"},
		selfCmd(t, "TestHelperReplaysEdgeAndAuthCorrectly")...)
	res := runRetrace(t, bin, cwd, "replay-edge-and-auth-correctly", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — both listeners' traffic matches their own recording exactly\nstdout: %s\nstderr: %s",
			res.code, res.stdout, res.stderr)
	}
	var doc assertRequestsReport
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, res.stdout)
	}
	if doc.RequestDiff == nil || doc.RequestDiff.Paired != 2 || doc.RequestDiff.Changed != 0 {
		t.Fatalf("requestDiff = %+v, want both listeners' one call each paired clean (2 paired, 0 changed)", doc.RequestDiff)
	}
}

// TestReplayAssertRequestsScopesCallCountDriftToItsOwnListener proves a
// surplus on ONE listener is reported against that listener's own traffic
// and does not spill onto — or get masked by — the other listener's clean
// recording. See design.md Decision 2 / tasks.md 2.2.
func TestReplayAssertRequestsScopesCallCountDriftToItsOwnListener(t *testing.T) {
	edge, auth := twoListenerUpstreams()
	defer edge.Close()
	defer auth.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	recordAndAcceptTwoListeners(t, bin, cwd, edge, auth)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--assert-requests", "--json"},
		selfCmd(t, "TestHelperReplayCallsEdgeThreeTimesAndAuthOnce")...)
	res := runRetrace(t, bin, cwd, "replay-edge-drift-auth-clean", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d — the edge listener's 3x-against-1x drift must fail the run\nstdout: %s\nstderr: %s",
			res.code, exitGate, res.stdout, res.stderr)
	}
	var doc assertRequestsReport
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, res.stdout)
	}
	if len(doc.Extra) != 2 {
		t.Fatalf("extra = %+v, want exactly 2 surplus calls (3 made on edge, 1 recorded)", doc.Extra)
	}
	for _, c := range doc.Extra {
		if c.Path != "/edge-only" {
			t.Fatalf("extra call = %+v, want only the edge listener's /edge-only — the auth listener's clean call must never appear here", c)
		}
	}
	// The auth listener's one, correctly-repeated call is still counted as
	// paired and clean — its own traffic never became "changed" just
	// because edge's did.
	if doc.RequestDiff == nil || doc.RequestDiff.Paired != 2 || doc.RequestDiff.Changed != 0 {
		t.Fatalf("requestDiff = %+v, want both listeners' matched exchange paired clean (2 paired: 1 edge + 1 auth, 0 changed)", doc.RequestDiff)
	}
}
