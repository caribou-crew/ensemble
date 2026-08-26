package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCmdLatencyFromDatadogMissingTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdLatencyFromDatadog([]string{"--query", "p{P}:foo{bar}"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--target") {
		t.Errorf("stderr = %q, want mention of --target", stderr.String())
	}
}

func TestCmdLatencyFromDatadogMissingQuery(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdLatencyFromDatadog([]string{"--target", "billing"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--query") {
		t.Errorf("stderr = %q, want mention of --query", stderr.String())
	}
}

func TestCmdLatencyFromDatadogHumanOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/latency/from-datadog" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"rules":[{"target":"billing","path":"/","p50":45,"p95":120,"p99":340,"enabled":false,"source":"datadog:p{P}:trace{svc} (last 60m)"}]}`))
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := cmdLatencyFromDatadog([]string{"--api-url", ts.URL, "--target", "billing", "--query", "p{P}:trace{svc}"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	want := "billing /: p50=45ms p95=120ms p99=340ms (source: datadog, last 60m)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestCmdLatencyFromDatadogJSONOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"rules":[{"target":"billing","path":"/","p50":45,"p95":120,"p99":340,"enabled":false,"source":"datadog:p{P}:trace{svc} (last 60m)"}]}`))
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := cmdLatencyFromDatadog([]string{"--api-url", ts.URL, "--target", "billing", "--query", "p{P}:trace{svc}", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	var got LatencyListResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nstdout: %s", err, stdout.String())
	}
	if len(got.Rules) != 1 || got.Rules[0].Target != "billing" {
		t.Errorf("rules = %+v", got.Rules)
	}
}

func TestCmdLatencyFromDatadogServerErrorSurfaced(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"datadog API key not set: export DD_API_KEY (or add it to .env)"}`))
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := cmdLatencyFromDatadog([]string{"--api-url", ts.URL, "--target", "billing", "--query", "p{P}:trace{svc}"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "DD_API_KEY") {
		t.Errorf("stderr = %q, want the server's error message surfaced", stderr.String())
	}
}

func TestCmdLatencyApplyMissingProfileArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdLatencyApply(nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("stderr = %q, want a usage message", stderr.String())
	}
}

func TestCmdLatencyApplyAllSucceedExitsZero(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"target":"billing","path":"/","ok":true,"p50":10,"p95":20,"p99":30,"source":"datadog:..."},{"target":"billing","path":"/health","ok":true,"fixedMs":5}]}`))
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := cmdLatencyApply([]string{"--api-url", ts.URL, "production"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "2 applied, 0 failed") {
		t.Errorf("stdout = %q, want a final count summary", stdout.String())
	}
}

func TestCmdLatencyApplyPartialFailureExitsNonZero(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"target":"billing","path":"/good","ok":true,"p50":10,"p95":20,"p99":30},{"target":"billing","path":"/bad","ok":false,"error":"no data points"}]}`))
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := cmdLatencyApply([]string{"--api-url", ts.URL, "production"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 on partial failure", code)
	}
	if !strings.Contains(stdout.String(), "1 applied, 1 failed") {
		t.Errorf("stdout = %q, want a final count summary", stdout.String())
	}
	if !strings.Contains(stdout.String(), "no data points") {
		t.Errorf("stdout = %q, want the failing rule's error", stdout.String())
	}
}

func TestCmdLatencyApplyUnknownProfileSurfacesError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"unknown latency profile \"ghost\""}`))
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := cmdLatencyApply([]string{"--api-url", ts.URL, "ghost"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "ghost") {
		t.Errorf("stderr = %q, want the unknown profile named", stderr.String())
	}
}
