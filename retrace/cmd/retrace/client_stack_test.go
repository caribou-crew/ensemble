package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// statusServer answers GET /api/status with body and nothing else, so a
// request to any other path is a test failure rather than a silent 200.
func statusServer(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

func TestStackDecodesTheServiceVersionsEnsembleReports(t *testing.T) {
	c := statusServer(t, `{"services":[
		{"name":"api","version":"abc123"},
		{"name":"web","version":"def456"}
	],"seed":{"name":"baseline","appliedAt":"2026-08-01T12:00:00Z"}}`)

	got, err := c.Stack(context.Background())
	if err != nil {
		t.Fatalf("Stack: %v", err)
	}
	if got == nil {
		t.Fatal("Stack returned nothing for a control plane that reported two services")
	}
	if got.Services["api"] != "abc123" || got.Services["web"] != "def456" {
		t.Errorf("services = %v", got.Services)
	}
	if got.Seed == nil || got.Seed.Name != "baseline" {
		t.Fatalf("seed = %+v, want baseline", got.Seed)
	}
	want := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if !got.Seed.AppliedAt.Equal(want) {
		t.Errorf("appliedAt = %s, want %s", got.Seed.AppliedAt, want)
	}
}

func TestAnOlderEnsembleYieldsNoStackRatherThanAnEmptyOne(t *testing.T) {
	// An ensemble predating `version:` answers /api/status perfectly well and
	// reports no fingerprints. An empty Stack would be a CLAIM — "this
	// backend consists of nothing" — and it would compare equal to every
	// other run that recorded nothing, turning two unfingerprinted runs into
	// positive evidence that the stack did not move.
	c := statusServer(t, `{"services":[{"name":"api"},{"name":"web"}],"readiness":{"state":"ready"}}`)

	got, err := c.Stack(context.Background())
	if err != nil {
		t.Fatalf("Stack: %v", err)
	}
	if got != nil {
		t.Errorf("Stack = %+v, want nil", got)
	}
}

func TestASeedWithNoVersionedServicesIsStillWorthRecording(t *testing.T) {
	// The two halves are independent. A stack with no fingerprints but a
	// recorded seed can still answer "were these two runs primed from the
	// same data", which nothing else in the report can.
	c := statusServer(t, `{"services":[{"name":"api"}],"seed":{"name":"promo","appliedAt":"2026-08-01T12:00:00Z"}}`)

	got, err := c.Stack(context.Background())
	if err != nil {
		t.Fatalf("Stack: %v", err)
	}
	if got == nil || got.Seed == nil || got.Seed.Name != "promo" {
		t.Fatalf("Stack = %+v, want the seed kept", got)
	}
	if len(got.Services) != 0 {
		t.Errorf("services = %v, want none — the unversioned service must not be recorded blank", got.Services)
	}
}

func TestAControlPlaneThatRefusesIsAnErrorNotAnEmptyStack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"orchestrator down"}`))
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL).Stack(context.Background())
	if err == nil {
		t.Fatal("a 500 was reported as a successful fingerprint")
	}
	if got != nil {
		t.Errorf("Stack = %+v alongside an error, want nil", got)
	}
}
