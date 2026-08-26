package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
)

// TestLatencyRulesPersistAcrossRestart runs `ensemble up` twice against the
// same config directory — a `latency set` rule from the first run must
// still be there (armed, same values) after the stack is stopped and
// started again, with no API call in between. This is the actual feature:
// LatencyStore is in-memory and gets rebuilt from scratch by runUp on every
// `ensemble up`, so surviving a restart depends entirely on
// cmd_up.go persisting to (and reloading from) .ensemble/latency.json.
func TestLatencyRulesPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	upPort := freePort(t)
	proxyPort := freePort(t)
	cfgPath := writeConfig(t, dir, upPort, proxyPort)

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from svc"))
	})

	// --- run 1: bring the stack up, arm a rule, stop it ---
	apiPort1 := freePort(t)
	ctx1, cancel1 := context.WithCancel(context.Background())
	result1 := make(chan error, 1)
	go func() {
		result1 <- runUp(ctx1, upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort1)}, &bytes.Buffer{}, &bytes.Buffer{})
	}()

	time.Sleep(standinBackendDelay)
	ln1, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", upPort))
	if err != nil {
		t.Fatalf("listen on stand-in backend port %d: %v", upPort, err)
	}
	srv1 := httptest.NewUnstartedServer(backendHandler)
	srv1.Listener.Close()
	srv1.Listener = ln1
	srv1.Start()

	apiURL1 := "http://127.0.0.1:" + strconv.Itoa(apiPort1)
	waitHealthy(t, apiURL1)
	waitServiceHealthy(t, NewClient(apiURL1), "svc")

	c1 := NewClient(apiURL1)
	if _, err := c1.LatencySet(context.Background(), proxy.LatencyRule{Target: "svc", Path: "/", FixedMs: 77, Enabled: true}); err != nil {
		t.Fatalf("latency set: %v", err)
	}

	cancel1()
	select {
	case err := <-result1:
		if err != nil {
			t.Fatalf("first runUp returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first runUp did not stop in time")
	}
	srv1.Close()

	persistedPath := latencyRulesPath(dir)
	if _, err := os.Stat(persistedPath); err != nil {
		t.Fatalf("expected a persisted latency file at %s after run 1: %v", persistedPath, err)
	}

	// --- run 2: same config dir, freshly-constructed LatencyStore — the
	// rule from run 1 must come back without any API call re-setting it.
	apiPort2 := freePort(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	result2 := make(chan error, 1)
	go func() {
		result2 <- runUp(ctx2, upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort2)}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	t.Cleanup(func() {
		cancel2()
		select {
		case <-result2:
		case <-time.After(5 * time.Second):
		}
	})

	startStandinBackend(t, upPort, backendHandler)

	apiURL2 := "http://127.0.0.1:" + strconv.Itoa(apiPort2)
	waitHealthy(t, apiURL2)
	waitServiceHealthy(t, NewClient(apiURL2), "svc")

	c2 := NewClient(apiURL2)
	ll, err := c2.LatencyList(context.Background())
	if err != nil {
		t.Fatalf("latency list: %v", err)
	}
	if len(ll.Rules) != 1 {
		t.Fatalf("latency rules after restart = %+v, want exactly the one persisted rule", ll.Rules)
	}
	got := ll.Rules[0]
	if got.Target != "svc" || got.Path != "/" || got.FixedMs != 77 || !got.Enabled {
		t.Fatalf("restored rule = %+v, want {svc / 77ms enabled}", got)
	}
}

// TestLatencyResetPersistsAcrossRestart confirms an explicit reset is
// itself persisted — a restart after `latency reset` must NOT resurrect
// ensemble.yaml's latency.defaults, matching "the persisted file, once it
// exists, is the full state" (see cmd_up.go's comment on this).
func TestLatencyResetPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	upPort := freePort(t)
	proxyPort := freePort(t)
	path := filepath.Join(dir, "ensemble.yaml")
	yaml := fmt.Sprintf(`
services:
  svc:
    run: "sleep 30"
    port: %d
    proxy: %d
    entry: true

latency:
  defaults:
    - target: svc
      path: /
      fixed_ms: 42
      enabled: true
`, upPort, proxyPort)
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from svc"))
	})

	// --- run 1: confirm the default seeded, then reset it ---
	apiPort1 := freePort(t)
	ctx1, cancel1 := context.WithCancel(context.Background())
	result1 := make(chan error, 1)
	go func() {
		result1 <- runUp(ctx1, upOptions{ConfigPath: path, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort1)}, &bytes.Buffer{}, &bytes.Buffer{})
	}()

	time.Sleep(standinBackendDelay)
	ln1, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", upPort))
	if err != nil {
		t.Fatalf("listen on stand-in backend port %d: %v", upPort, err)
	}
	srv1 := httptest.NewUnstartedServer(backendHandler)
	srv1.Listener.Close()
	srv1.Listener = ln1
	srv1.Start()

	apiURL1 := "http://127.0.0.1:" + strconv.Itoa(apiPort1)
	waitHealthy(t, apiURL1)
	waitServiceHealthy(t, NewClient(apiURL1), "svc")

	c1 := NewClient(apiURL1)
	ll1, err := c1.LatencyList(context.Background())
	if err != nil {
		t.Fatalf("latency list: %v", err)
	}
	if len(ll1.Rules) != 1 || ll1.Rules[0].FixedMs != 42 {
		t.Fatalf("expected the config default seeded on a fresh store, got %+v", ll1.Rules)
	}

	if _, err := c1.LatencyReset(context.Background()); err != nil {
		t.Fatalf("latency reset: %v", err)
	}

	cancel1()
	select {
	case err := <-result1:
		if err != nil {
			t.Fatalf("first runUp returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first runUp did not stop in time")
	}
	srv1.Close()

	// --- run 2: the reset must stick — no rules, config default not resurrected ---
	apiPort2 := freePort(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	result2 := make(chan error, 1)
	go func() {
		result2 <- runUp(ctx2, upOptions{ConfigPath: path, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort2)}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	t.Cleanup(func() {
		cancel2()
		select {
		case <-result2:
		case <-time.After(5 * time.Second):
		}
	})

	startStandinBackend(t, upPort, backendHandler)

	apiURL2 := "http://127.0.0.1:" + strconv.Itoa(apiPort2)
	waitHealthy(t, apiURL2)
	waitServiceHealthy(t, NewClient(apiURL2), "svc")

	c2 := NewClient(apiURL2)
	ll2, err := c2.LatencyList(context.Background())
	if err != nil {
		t.Fatalf("latency list: %v", err)
	}
	if len(ll2.Rules) != 0 {
		t.Fatalf("latency rules after restart = %+v, want empty (reset persisted, default not resurrected)", ll2.Rules)
	}
}
