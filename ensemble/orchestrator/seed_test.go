package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// fakeSQLRunner records every RunFile call so tests can assert declared
// order and argument resolution without a real database.
type fakeSQLRunner struct {
	calls []string
	err   error
}

func (f *fakeSQLRunner) RunFile(ctx context.Context, dbName, path string) error {
	f.calls = append(f.calls, dbName+":"+path)
	return f.err
}

// Test: HTTP seed steps run in declared order and stop at the first
// failure — later steps must not execute.
func TestSeedHTTPStepsSuccessThenFailureStopsOrder(t *testing.T) {
	var thirdCalled bool

	ok1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok1.Close()

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()

	never := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		thirdCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer never.Close()

	cfg := &config.Config{
		Dir: t.TempDir(),
		Seeds: map[string]config.Seed{
			"baseline": {
				HTTP: []config.SeedHTTP{
					{Method: "GET", URL: ok1.URL},
					{Method: "GET", URL: fail.URL},
					{Method: "GET", URL: never.URL},
				},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	results, err := o.Seed(context.Background(), "baseline")
	if err == nil {
		t.Fatal("expected an error from the failing step")
	}
	if len(results) != 2 {
		t.Fatalf("results = %d steps, want 2 (stop at first failure): %+v", len(results), results)
	}
	if !results[0].OK || results[0].Kind != "http" || results[0].Ref != ok1.URL {
		t.Fatalf("step 1 = %+v, want an OK http step for %s", results[0], ok1.URL)
	}
	if results[1].OK || results[1].Err == "" {
		t.Fatalf("step 2 = %+v, want a recorded failure", results[1])
	}
	if thirdCalled {
		t.Fatal("third step ran despite the second step failing")
	}
}

// Test: a SQL seed step with no SQLRunner configured errors cleanly and
// reports a failed step rather than panicking.
func TestSeedSQLWithoutRunnerErrors(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Seeds: map[string]config.Seed{
			"baseline": {
				SQL: []config.SeedSQL{
					{Database: "primary", File: "seed.sql"},
				},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	results, err := o.Seed(context.Background(), "baseline")
	if err == nil {
		t.Fatal("expected an error: no SQL runner configured")
	}
	if !strings.Contains(err.Error(), "no SQL runner configured") {
		t.Fatalf("error = %v, want it to mention the missing SQL runner", err)
	}
	if len(results) != 1 || results[0].OK {
		t.Fatalf("results = %+v, want one failed sql step", results)
	}
	if !strings.Contains(results[0].Err, "no SQL runner configured") {
		t.Fatalf("step.Err = %q, want it to mention the missing SQL runner", results[0].Err)
	}
}

// Test: SQL steps run before HTTP steps (declared order), and the SQL
// runner sees File resolved against Config.Dir.
func TestSeedSQLThenHTTPOrder(t *testing.T) {
	var httpCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	sqlPath := filepath.Join(dir, "seed.sql")
	if err := os.WriteFile(sqlPath, []byte("-- seed"), 0o644); err != nil {
		t.Fatalf("write seed.sql: %v", err)
	}

	cfg := &config.Config{
		Dir: dir,
		Seeds: map[string]config.Seed{
			"baseline": {
				SQL:  []config.SeedSQL{{Database: "primary", File: "seed.sql"}},
				HTTP: []config.SeedHTTP{{Method: "GET", URL: srv.URL}},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	runner := &fakeSQLRunner{}
	o.SQLRunner = runner

	results, err := o.Seed(context.Background(), "baseline")
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2: %+v", len(results), results)
	}
	if results[0].Kind != "sql" || !results[0].OK {
		t.Fatalf("step 1 = %+v, want an OK sql step", results[0])
	}
	if results[1].Kind != "http" || !results[1].OK {
		t.Fatalf("step 2 = %+v, want an OK http step", results[1])
	}
	if !httpCalled {
		t.Fatal("http step did not run")
	}
	want := "primary:" + sqlPath
	if len(runner.calls) != 1 || runner.calls[0] != want {
		t.Fatalf("SQLRunner calls = %v, want [%q]", runner.calls, want)
	}
}
