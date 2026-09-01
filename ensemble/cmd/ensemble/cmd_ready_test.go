package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeReadyServer answers GET /api/status with bodies() in sequence
// (repeating the last one once exhausted) — used to simulate the
// orchestrator's readiness state settling over successive polls.
func fakeReadyServer(t *testing.T, bodies []string) *httptest.Server {
	t.Helper()
	var i int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		idx := atomic.AddInt64(&i, 1) - 1
		if int(idx) >= len(bodies) {
			idx = int64(len(bodies) - 1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(bodies[idx]))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestCmdReadyNoConfigExitsZeroImmediately(t *testing.T) {
	ts := fakeReadyServer(t, []string{`{"services":[],"readiness":{"state":"ready","checks":[]}}`})

	var stdout, stderr bytes.Buffer
	code := cmdReady([]string{"--api-url", ts.URL, "--timeout", "1s"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
}

func TestCmdReadyBecomesReadyBeforeTimeout(t *testing.T) {
	ts := fakeReadyServer(t, []string{
		`{"services":[],"readiness":{"state":"checking","checks":[{"name":"catalog-up","passed":false}]}}`,
		`{"services":[],"readiness":{"state":"checking","checks":[{"name":"catalog-up","passed":false}]}}`,
		`{"services":[],"readiness":{"state":"ready","checks":[{"name":"catalog-up","passed":true}]}}`,
	})

	var stdout, stderr bytes.Buffer
	code := cmdReady([]string{"--api-url", ts.URL, "--timeout", "5s"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
}

func TestCmdReadyNotReadyAtTimeout(t *testing.T) {
	ts := fakeReadyServer(t, []string{
		`{"services":[],"readiness":{"state":"not_ready","checks":[{"name":"catalog-up","passed":false,"lastError":"status 503, want 200"}]}}`,
	})

	var stdout, stderr bytes.Buffer
	code := cmdReady([]string{"--api-url", ts.URL, "--timeout", "1s"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit when readiness never resolves ready")
	}
	if !strings.Contains(stderr.String(), "catalog-up") {
		t.Errorf("stderr should name the failing check:\n%s", stderr.String())
	}
}

// TestCmdReadyFailsFastOnCrashedService: readiness never resolves here, so
// without the crash check `ready` would sit out the full --timeout — the
// generous 30s flag is the proof that the early exit path fired.
func TestCmdReadyFailsFastOnCrashedService(t *testing.T) {
	ts := fakeReadyServer(t, []string{
		`{"services":[{"name":"web","status":"crashed","placement":"native","exitCode":1}],"readiness":{"state":"checking","checks":[]}}`,
	})

	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := cmdReady([]string{"--api-url", ts.URL, "--timeout", "30s"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for a crashed service")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s; want a fail-fast exit well under the 30s timeout", elapsed)
	}
	if !strings.Contains(stderr.String(), "web") {
		t.Errorf("stderr should name the crashed service:\n%s", stderr.String())
	}
}

func TestCmdReadyJSONShape(t *testing.T) {
	ts := fakeReadyServer(t, []string{`{"services":[],"readiness":{"state":"not_ready","checks":[{"name":"catalog-up","passed":false,"lastError":"boom"}]}}`})

	var stdout, stderr bytes.Buffer
	code := cmdReady([]string{"--api-url", ts.URL, "--timeout", "1s", "--json"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for --json when not ready")
	}

	var got struct {
		Ready  bool `json:"ready"`
		Checks []struct {
			Name      string `json:"name"`
			Passed    bool   `json:"passed"`
			LastError string `json:"lastError"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nstdout: %s", err, stdout.String())
	}
	if got.Ready {
		t.Error("ready = true, want false")
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "catalog-up" {
		t.Errorf("checks = %+v", got.Checks)
	}
}
