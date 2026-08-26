package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeStatusServer answers GET /api/status with body — enough to drive
// cmdStatus without a real orchestrator.
func fakeStatusServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestCmdStatusTextNoReadinessConfigured(t *testing.T) {
	ts := fakeStatusServer(t, `{"services":[{"name":"svc","status":"healthy"}],"readiness":{"state":"ready","checks":[]}}`)

	var stdout, stderr bytes.Buffer
	code := cmdStatus([]string{"--api-url", ts.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "READINESS") {
		t.Errorf("stdout should not print a readiness summary with no checks configured:\n%s", stdout.String())
	}
}

func TestCmdStatusTextWithReadinessConfigured(t *testing.T) {
	ts := fakeStatusServer(t, `{"services":[{"name":"svc","status":"healthy"}],"readiness":{"state":"checking","checks":[{"name":"catalog-up","passed":true},{"name":"payments-up","passed":false,"lastError":"status 503, want 200"}]}}`)

	var stdout, stderr bytes.Buffer
	code := cmdStatus([]string{"--api-url", ts.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "READINESS: 1/2 passed (checking)") {
		t.Errorf("stdout missing readiness summary:\n%s", out)
	}
}

func TestCmdStatusJSONIncludesReadiness(t *testing.T) {
	ts := fakeStatusServer(t, `{"services":[],"readiness":{"state":"ready","checks":[{"name":"catalog-up","passed":true}]}}`)

	var stdout, stderr bytes.Buffer
	code := cmdStatus([]string{"--api-url", ts.URL, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}

	var got StatusResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nstdout: %s", err, stdout.String())
	}
	if got.Readiness.State != "ready" || len(got.Readiness.Checks) != 1 || !got.Readiness.Checks[0].Passed {
		t.Errorf("readiness = %+v", got.Readiness)
	}
}
