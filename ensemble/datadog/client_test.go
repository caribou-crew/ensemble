package datadog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueryPercentileSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","series":[{"pointlist":[[1000,40],[2000,60],[3000,null]]}]}`))
	}))
	defer srv.Close()

	c := &HTTPClient{Site: "datadoghq.com", APIKey: "key", AppKey: "app", BaseURL: srv.URL}
	got, err := c.QueryPercentile(context.Background(), "p50:trace.http.server.request.duration{service:billing}", 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 50 {
		t.Errorf("QueryPercentile() = %v, want 50 (average of 40 and 60, null skipped)", got)
	}
}

func TestQueryPercentileAuthHeaders(t *testing.T) {
	var gotAPIKey, gotAppKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("DD-API-KEY")
		gotAppKey = r.Header.Get("DD-APPLICATION-KEY")
		w.Write([]byte(`{"status":"ok","series":[{"pointlist":[[1000,10]]}]}`))
	}))
	defer srv.Close()

	c := &HTTPClient{APIKey: "my-api-key", AppKey: "my-app-key", BaseURL: srv.URL}
	if _, err := c.QueryPercentile(context.Background(), "p50:foo{bar}", 60); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAPIKey != "my-api-key" {
		t.Errorf("DD-API-KEY = %q, want my-api-key", gotAPIKey)
	}
	if gotAppKey != "my-app-key" {
		t.Errorf("DD-APPLICATION-KEY = %q, want my-app-key", gotAppKey)
	}
}

func TestQueryPercentileEmptyPointlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","series":[{"pointlist":[[1000,null],[2000,null]]}]}`))
	}))
	defer srv.Close()

	c := &HTTPClient{BaseURL: srv.URL}
	_, err := c.QueryPercentile(context.Background(), "p50:foo{bar}", 60)
	if err == nil {
		t.Fatal("expected error for all-null pointlist, got nil")
	}
	if !strings.Contains(err.Error(), "no data points") {
		t.Errorf("error = %v, want mention of no data points", err)
	}
}

func TestQueryPercentileEmptySeries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","series":[]}`))
	}))
	defer srv.Close()

	c := &HTTPClient{BaseURL: srv.URL}
	if _, err := c.QueryPercentile(context.Background(), "p50:foo{bar}", 60); err == nil {
		t.Fatal("expected error for empty series, got nil")
	}
}

func TestQueryPercentileHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errors":["Forbidden"]}`))
	}))
	defer srv.Close()

	c := &HTTPClient{BaseURL: srv.URL}
	_, err := c.QueryPercentile(context.Background(), "p50:foo{bar}", 60)
	if err == nil {
		t.Fatal("expected error for 401 status, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want mention of status 401", err)
	}
}

func TestQueryPercentileDatadogErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","error":"bad query syntax"}`))
	}))
	defer srv.Close()

	c := &HTTPClient{BaseURL: srv.URL}
	_, err := c.QueryPercentile(context.Background(), "p50:foo{bar}", 60)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad query syntax") {
		t.Errorf("error = %v, want Datadog's error message", err)
	}
}

func TestQueryPercentileMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := &HTTPClient{BaseURL: srv.URL}
	if _, err := c.QueryPercentile(context.Background(), "p50:foo{bar}", 60); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

type fakeClient struct {
	queries []string
	results map[string]float64
	err     map[string]error
}

func (f *fakeClient) QueryPercentile(ctx context.Context, query string, windowMinutes int) (float64, error) {
	f.queries = append(f.queries, query)
	if err, ok := f.err[query]; ok {
		return 0, err
	}
	return f.results[query], nil
}

func TestQueryPercentileTripleSubstitutesAllThree(t *testing.T) {
	f := &fakeClient{results: map[string]float64{
		"p50:foo{bar}": 0.010,
		"p95:foo{bar}": 0.020,
		"p99:foo{bar}": 0.030,
	}}
	p50, p95, p99, err := QueryPercentileTriple(context.Background(), f, "p{P}:foo{bar}", 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p50 != 10 || p95 != 20 || p99 != 30 {
		t.Errorf("got p50=%v p95=%v p99=%v, want 10/20/30 (Datadog seconds converted to ms)", p50, p95, p99)
	}
	want := []string{"p50:foo{bar}", "p95:foo{bar}", "p99:foo{bar}"}
	if len(f.queries) != len(want) {
		t.Fatalf("queries = %v, want %v", f.queries, want)
	}
	for i, q := range want {
		if f.queries[i] != q {
			t.Errorf("queries[%d] = %q, want %q", i, f.queries[i], q)
		}
	}
}
