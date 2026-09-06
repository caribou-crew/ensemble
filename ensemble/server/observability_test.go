package server_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

func observationAPI(t *testing.T, rec *proxy.Recorder) string {
	t.Helper()
	ts := httptest.NewServer(server.New(server.Deps{Rec: rec}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func observationGet(t *testing.T, base, path string) (int, map[string]any, string) {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if resp.StatusCode == 200 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("expected evidence JSON: %v: %.200s", err, raw)
		}
	}
	return resp.StatusCode, out, string(raw)
}

func TestObservationRequestsFilterAndBoundEvidence(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{})
	for _, h := range []trace.Hop{
		{TraceID: "t", SpanID: "a", To: "be", Path: "/orders", Status: 500, T: trace.Timings{DoneMs: 100}},
		{TraceID: "t", SpanID: "b", To: "other", Path: "/orders", Status: 500, T: trace.Timings{DoneMs: 100}},
		{TraceID: "t", SpanID: "c", To: "be", Path: "/health", Status: 500, T: trace.Timings{DoneMs: 100}},
		{TraceID: "t", SpanID: "d", To: "be", Path: "/orders", Status: 200, T: trace.Timings{DoneMs: 100}},
		{TraceID: "t", SpanID: "e", To: "be", Path: "/orders", Status: 500, T: trace.Timings{DoneMs: 1}},
		{TraceID: "t", SpanID: "f", To: "be", Path: "/orders/42", Status: 503, T: trace.Timings{DoneMs: 200}, Req: trace.Payload{Body: "body-canary", Headers: map[string]string{"secret": "header-canary"}}, Err: "error-canary"},
	} {
		rec.Record(h)
	}
	code, out, raw := observationGet(t, observationAPI(t, rec), "/api/observability/requests?service=be&path=/orders&errorsOnly=true&minDurationMs=50&limit=1")
	if code != 200 {
		t.Fatalf("code=%d body=%s", code, raw)
	}
	hops := out["hops"].([]any)
	if len(hops) != 1 || hops[0].(map[string]any)["seq"] != float64(6) {
		t.Fatalf("wrong filtered tail: %s", raw)
	}
	scope := out["scope"].(map[string]any)
	if scope["matched"] != float64(2) || scope["truncated"] != true || scope["source"] != "live-ring" || scope["completeness"] != "unknown" {
		t.Fatalf("scope overclaims evidence: %s", raw)
	}
	for _, secret := range []string{"body-canary", "header-canary", "error-canary", "\"req\"", "\"resp\""} {
		if strings.Contains(raw, secret) {
			t.Errorf("compact response leaked %s", secret)
		}
	}
}

func TestObservationTraceExplanationUsesObservedParentsAndQuality(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{})
	start := time.Now()
	for _, h := range []trace.Hop{
		{TraceID: "chosen", SpanID: "a", To: "bff", Status: 200, T: trace.Timings{Start: start, DoneMs: 100}},
		{TraceID: "chosen", SpanID: "b", ParentSpanID: "a", From: "bff", To: "be", Status: 503, T: trace.Timings{Start: start.Add(time.Millisecond), DoneMs: 80}},
		{TraceID: "chosen", SpanID: "c", ParentSpanID: "absent", From: "be", To: "provider", Status: 200, Streaming: true, T: trace.Timings{Start: start.Add(2 * time.Millisecond)}, Req: trace.Payload{Truncated: true}, Err: "redaction failed: synthetic; payload bodies dropped"},
		{TraceID: "unrelated", SpanID: "absent", To: "unrelated", Status: 500, T: trace.Timings{DoneMs: 999}},
	} {
		rec.Record(h)
	}
	code, out, raw := observationGet(t, observationAPI(t, rec), "/api/observability/traces/chosen?limit=2")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, raw)
	}
	if len(out["hops"].([]any)) != 2 || out["observedHops"] != float64(3) || out["slowestHopSeq"] != float64(1) {
		t.Fatalf("bad trace explanation: %s", raw)
	}
	if strings.Contains(raw, "unrelated") {
		t.Fatalf("unrelated evidence: %s", raw)
	}
	for _, evidence := range []string{"parent-unobserved", "stream-incomplete", "redaction-failed", "http-error"} {
		if !strings.Contains(raw, evidence) {
			t.Errorf("missing %s: %s", evidence, raw)
		}
	}
	if out["scope"].(map[string]any)["completeness"] != "degraded" {
		t.Fatalf("unsafe evidence not degraded: %s", raw)
	}
}

func TestObservationEmptyAndUnavailableNeverClaimHealthy(t *testing.T) {
	for _, rec := range []*proxy.Recorder{nil, proxy.NewRecorder(proxy.RecorderOpts{})} {
		code, out, raw := observationGet(t, observationAPI(t, rec), "/api/observability/traces/absent")
		if rec == nil {
			if code != 503 {
				t.Fatalf("missing recorder status=%d", code)
			}
			continue
		}
		if code != 200 || out["observedHops"] != float64(0) || out["scope"].(map[string]any)["completeness"] != "unknown" || !strings.Contains(raw, "trace-unobserved") {
			t.Fatalf("empty evidence=%s", raw)
		}
	}
}

func TestObservationRejectsInvalidQueries(t *testing.T) {
	base := observationAPI(t, proxy.NewRecorder(proxy.RecorderOpts{}))
	for _, query := range []string{"limit=0", "limit=101", "limit=-1", "limit=no", "errorsOnly=maybe", "minDurationMs=NaN", "minDurationMs=Inf", "minDurationMs=-1", "limit=1&limit=2", "unknown=x"} {
		code, _, raw := observationGet(t, base, "/api/observability/requests?"+query)
		if code != 400 {
			t.Errorf("query %q status=%d body=%.200s", query, code, raw)
		}
	}
	for _, query := range []string{"limit=201", "limit=0", "errorsOnly=true"} {
		code, _, _ := observationGet(t, base, "/api/observability/traces/t?"+query)
		if code != 400 {
			t.Errorf("trace query %s status=%d", query, code)
		}
	}
}

type observationBrokenWriter struct{}

func (observationBrokenWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic write failure")
}

func TestObservationDisclosesRetentionLossAndTextBounds(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 1, Writer: trace.NewWriter(observationBrokenWriter{})})
	rec.Record(trace.Hop{TraceID: "evicted", SpanID: "first", To: "be"})
	rec.Record(trace.Hop{TraceID: "t", SpanID: "s", To: "be", Path: "/" + strings.Repeat("x", 5000), Status: 200})
	rec.Close()
	code, out, raw := observationGet(t, observationAPI(t, rec), "/api/observability/requests")
	if code != 200 {
		t.Fatalf("code=%d: %s", code, raw)
	}
	scope := out["scope"].(map[string]any)
	if scope["retainedHops"] != float64(1) || scope["oldestSeq"] != float64(2) || scope["writeErrors"] != float64(2) || scope["completeness"] != "degraded" {
		t.Fatalf("loss scope: %s", raw)
	}
	h := out["hops"].([]any)[0].(map[string]any)
	if len(h["path"].(string)) > 515 || h["textTruncated"] != true || len(raw) > 5000 {
		t.Fatalf("response is not bounded: len=%d", len(raw))
	}
}
