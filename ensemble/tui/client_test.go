package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

func TestClientStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(StatusResponse{Services: []orchestrator.ServiceState{{Name: "catalog", Status: orchestrator.StatusHealthy}}})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	resp, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(resp.Services) != 1 || resp.Services[0].Name != "catalog" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestClientRestartAndFlip(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		json.NewEncoder(w).Encode(orchestrator.ServiceState{Name: "catalog", Status: orchestrator.StatusStarting})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	if _, err := c.Restart(context.Background(), "catalog"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if gotPath != "/api/services/catalog/restart" || gotMethod != http.MethodPost {
		t.Fatalf("Restart hit %s %s", gotMethod, gotPath)
	}

	if _, err := c.Flip(context.Background(), "catalog"); err != nil {
		t.Fatalf("Flip: %v", err)
	}
	if gotPath != "/api/services/catalog/flip" || gotMethod != http.MethodPost {
		t.Fatalf("Flip hit %s %s", gotMethod, gotPath)
	}
}

func TestClientErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": `service "ghost" not found`})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	_, err := c.Restart(context.Background(), "ghost")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected a non-empty error message")
	}
}

func TestClientLatencyArmAllAndReset(t *testing.T) {
	var lastBody map[string]bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/latency/arm-all":
			json.NewDecoder(r.Body).Decode(&lastBody)
			json.NewEncoder(w).Encode(LatencyListResponse{Rules: []proxy.LatencyRule{{Target: "*", Path: "/", Enabled: lastBody["enabled"]}}})
		case "/api/latency/reset":
			json.NewEncoder(w).Encode(LatencyListResponse{Rules: nil})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	resp, err := c.LatencyArmAll(context.Background(), true)
	if err != nil {
		t.Fatalf("LatencyArmAll: %v", err)
	}
	if !resp.Rules[0].Enabled {
		t.Fatalf("expected rule armed, got %+v", resp.Rules)
	}

	if _, err := c.LatencyReset(context.Background()); err != nil {
		t.Fatalf("LatencyReset: %v", err)
	}
}

func TestClientProfiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/profiles" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(orchestrator.ProfilesState{Active: []string{"lane1"}, Profiles: []orchestrator.ProfileInfo{{Name: "lane1", Active: true}}})
		case r.URL.Path == "/api/profiles/lane1/up":
			json.NewEncoder(w).Encode(orchestrator.ProfilesState{Active: []string{"lane1"}})
		case r.URL.Path == "/api/profiles/lane1/down":
			json.NewEncoder(w).Encode(orchestrator.ProfilesState{})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	resp, err := c.Profiles(context.Background())
	if err != nil || len(resp.Profiles) != 1 {
		t.Fatalf("Profiles: %+v, %v", resp, err)
	}
	if _, err := c.ProfileUp(context.Background(), "lane1"); err != nil {
		t.Fatalf("ProfileUp: %v", err)
	}
	if _, err := c.ProfileDown(context.Background(), "lane1"); err != nil {
		t.Fatalf("ProfileDown: %v", err)
	}
}

func TestClientSeedPartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(SeedResponse{OK: false, Error: "boom", Results: []orchestrator.SeedStepResult{{Kind: "sql", OK: false, Err: "boom"}}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	resp, err := c.Seed(context.Background(), "catalog")
	if err != nil {
		t.Fatalf("Seed: unexpected transport error: %v", err)
	}
	if resp.OK || len(resp.Results) != 1 {
		t.Fatalf("expected partial result preserved, got %+v", resp)
	}
}
