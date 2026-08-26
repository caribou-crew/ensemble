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

func TestRunPreflightChecksAllPass(t *testing.T) {
	cfg := &config.Config{Preflight: []config.PreflightCheck{
		{Name: "one", Run: "true"},
		{Name: "two", Run: "exit 0"},
	}}
	if err := runPreflightChecks(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunPreflightChecksStopsAtFirstFailure(t *testing.T) {
	marker := fmt.Sprintf("ensemble-preflight-checks-test-marker-%d", os.Getpid())
	touchPath := filepath.Join(t.TempDir(), marker)
	cfg := &config.Config{Preflight: []config.PreflightCheck{
		{Name: "fails", Run: "exit 1"},
		{Name: "would-run", Run: fmt.Sprintf("touch %q", touchPath)},
	}}
	if err := runPreflightChecks(cfg); err == nil {
		t.Fatal("expected an error from the failing check")
	} else if !strings.Contains(err.Error(), `"fails"`) {
		t.Errorf("error %q does not name the failing check", err)
	}
	if _, err := os.Stat(touchPath); err == nil {
		t.Error("a later check ran despite an earlier one failing")
	}
}

func TestRunPreflightChecksCustomMessage(t *testing.T) {
	cfg := &config.Config{Preflight: []config.PreflightCheck{
		{Name: "docker", Run: "exit 1", Message: "Docker isn't running — start Docker Desktop"},
	}}
	err := runPreflightChecks(cfg)
	if err == nil || !strings.Contains(err.Error(), "Docker isn't running") {
		t.Fatalf("expected custom message in error, got: %v", err)
	}
}

func TestRunPreflightChecksFallsBackToCommandOutput(t *testing.T) {
	cfg := &config.Config{Preflight: []config.PreflightCheck{
		{Name: "docker", Run: "echo 'cannot connect to the docker daemon' >&2; exit 1"},
	}}
	err := runPreflightChecks(cfg)
	if err == nil || !strings.Contains(err.Error(), "cannot connect to the docker daemon") {
		t.Fatalf("expected command output in error, got: %v", err)
	}
}

func TestRunPreflightChecksTimeout(t *testing.T) {
	cfg := &config.Config{Preflight: []config.PreflightCheck{
		{Name: "slow", Run: "sleep 5", TimeoutS: 1},
	}}
	start := time.Now()
	err := runPreflightChecks(cfg)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("runPreflightChecks did not respect timeout_s, took %s", elapsed)
	}
}

func TestRunUp_FailingPreflightNeverSpawnsService(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ensemble.yaml")
	marker := fmt.Sprintf("ensemble-preflight-up-test-marker-%d", os.Getpid())
	yaml := fmt.Sprintf(`
preflight:
  - name: "container runtime"
    run: "exit 1"
    message: "container runtime is not available"
services:
  svc:
    run: "sleep 30 # %s"
    port: %d
    entry: true
`, marker, freePort(t))
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
			t.Fatal("expected runUp to return a preflight error")
		}
		if !strings.Contains(runErr.Error(), "container runtime is not available") {
			t.Errorf("error %q does not carry the preflight message", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runUp did not return in time")
	}

	if _, alive := findPID(t, marker); alive {
		t.Fatal("service process was spawned despite a failing preflight check")
	}
}

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
