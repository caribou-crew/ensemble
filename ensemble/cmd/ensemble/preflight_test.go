package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

func TestCheckPortsFreeAllFree(t *testing.T) {
	cfg := &config.Config{Services: map[string]config.Service{
		"svc": {Run: "x", Port: freePort(t)},
	}}
	if err := checkPortsFree(cfg, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckPortsFreeReportsConflict(t *testing.T) {
	port := freePort(t)
	occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()

	cfg := &config.Config{Services: map[string]config.Service{
		"svc": {Run: "x", Port: port},
	}}
	err = checkPortsFree(cfg, nil)
	if err == nil {
		t.Fatal("expected a conflict error")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("port %d", port)) || !strings.Contains(err.Error(), "service svc") {
		t.Errorf("error %q does not name the conflicting port/service", err)
	}
}

func TestCheckPortsFreeSkipsInactiveProfileGatedService(t *testing.T) {
	port := freePort(t)
	occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()

	cfg := &config.Config{Services: map[string]config.Service{
		"order": {Run: "x", Port: port, Profile: "full"},
	}}
	if err := checkPortsFree(cfg, nil); err != nil {
		t.Errorf("inactive profile-gated service's occupied port must not be a conflict, got: %v", err)
	}
	if err := checkPortsFree(cfg, []string{"full"}); err == nil {
		t.Error("expected a conflict once the profile gating that port is active")
	}
}

// TestRunUp_PortConflictNeverSpawnsService is the preflight check driven
// through runUp end to end: a service's configured port is already taken,
// so runUp must fail fast before spawning anything — unlike a bind
// failure discovered mid-startup (TestRunUp_BindFailureTearsDownAndReturnsError),
// the service process here must never even start.
func TestRunUp_PortConflictNeverSpawnsService(t *testing.T) {
	port := freePort(t)
	occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ensemble.yaml")
	marker := fmt.Sprintf("ensemble-preflight-test-marker-%d", os.Getpid())
	yaml := fmt.Sprintf(`
services:
  svc:
    run: "sleep 30 # %s"
    port: %d
    entry: true
`, marker, port)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Cleanup(func() {
		if pid, ok := findPID(t, marker); ok {
			if proc, err := os.FindProcess(pid); err == nil {
				proc.Kill()
			}
		}
	})

	opts := upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", freePort(t))}
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runUp(context.Background(), opts, &stdout, &stderr) }()

	select {
	case runErr := <-done:
		if runErr == nil {
			t.Fatal("expected runUp to return a port-conflict error")
		}
		if !strings.Contains(runErr.Error(), fmt.Sprintf("port %d", port)) {
			t.Errorf("error %q does not name the conflicting port", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runUp did not return in time")
	}

	if _, alive := findPID(t, marker); alive {
		t.Fatal("service process was spawned despite a preflight port conflict")
	}
}
