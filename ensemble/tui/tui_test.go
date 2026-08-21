package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunUnreachableAPIReturnsError(t *testing.T) {
	// No server listening at this URL.
	err := Run(context.Background(), "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected an error when the control plane isn't reachable")
	}
	if !strings.Contains(err.Error(), "is not reachable") {
		t.Fatalf("expected a reachability error, got: %v", err)
	}
}

func TestRunUnreachableAPIDoesNotStartProgram(t *testing.T) {
	// A server that answers /api/status with an error status: Run should
	// fail the same way, before ever touching the terminal.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := Run(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a 500 status response")
	}
}
