package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestHelperFetchesBothListeners is the test command for the
// multi-listener run below: it calls through BOTH named listeners' proxy
// URLs, so the run's wire.jsonl should carry one hop per listener.
func TestHelperFetchesBothListeners(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "fetch-both-listeners" {
		return
	}
	for _, env := range []string{"RETRACE_PROXY_URL_EDGE", "RETRACE_PROXY_URL_AUTH"} {
		url := os.Getenv(env)
		if url == "" {
			fmt.Fprintln(os.Stderr, "helper: missing", env)
			os.Exit(9)
		}
		resp, err := http.Get(url + "/x")
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch via", env, ":", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	os.Exit(0)
}

func TestRunMultiListenerRecordsBothUpstreamsWithDistinctHopTags(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"from":"edge"}`))
	}))
	defer edge.Close()
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"from":"auth"}`))
	}))
	defer auth.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, fmt.Sprintf("app: web\nlisteners:\n  - name: edge\n    upstream: %s\n  - name: auth\n    upstream: %s\n", edge.URL, auth.URL))

	args := append([]string{"run", "--flow", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperFetchesBothListeners")...)
	res := runRetrace(t, bin, cwd, "fetch-both-listeners", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Wire.Calls != 2 {
		t.Errorf("wire.calls = %d, want 2", m.Wire.Calls)
	}
}

// TestRunMultiListenerConfigWithUpstreamFlagIsIgnoredWithANote confirms the
// single-listener CLI-override flags don't silently pick one of several
// configured listeners apart when retrace.yaml already names more than
// one — they're ignored, loudly, rather than guessed.
func TestRunMultiListenerConfigWithUpstreamFlagIsIgnoredWithANote(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"from":"edge"}`))
	}))
	defer edge.Close()
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"from":"auth"}`))
	}))
	defer auth.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, fmt.Sprintf("app: web\nlisteners:\n  - name: edge\n    upstream: %s\n  - name: auth\n    upstream: %s\n", edge.URL, auth.URL))

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", "http://127.0.0.1:1"},
		selfCmd(t, "TestHelperFetchesBothListeners")...)
	res := runRetrace(t, bin, cwd, "fetch-both-listeners", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !containsIgnoredListenersNote(res.stderr) {
		t.Errorf("stderr = %q, want a note that --upstream was ignored", res.stderr)
	}
}

func containsIgnoredListenersNote(s string) bool {
	return len(s) > 0 && (indexOfIgnoredNote(s) >= 0)
}

func indexOfIgnoredNote(s string) int {
	needle := "are ignored"
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
