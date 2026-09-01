package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

func TestRetryOnBindConflictRetriesOnlyAddressInUse(t *testing.T) {
	calls := 0
	err := retryOnBindConflict(func() error {
		calls++
		if calls < 3 {
			return errors.New("listen tcp 127.0.0.1:9: bind: address already in use")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

func TestRetryOnBindConflictGivesUpOnOtherErrors(t *testing.T) {
	calls := 0
	want := errors.New("some other bind failure")
	err := retryOnBindConflict(func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected the original error to propagate, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry) for a non-conflict error, got %d", calls)
	}
}

// TestUpWithPureUpstreamServiceSkipsProcessSpawn: a service declaring only
// `upstream` (no run/docker) must start with no process at all and its
// proxy listener wired straight to the real remote target.
func TestUpWithPureUpstreamServiceSkipsProcessSpawn(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From", "qa")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxyPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"edge": {Upstream: upstream.URL, Proxy: proxyPort},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	st, ok := o.Service("edge")
	if !ok || st.Placement != "passthrough" || st.Status != StatusHealthy || st.PID != 0 {
		t.Fatalf("expected passthrough/healthy/no-pid, got %+v (ok=%v)", st, ok)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/anything", proxyPort))
	if err != nil {
		t.Fatalf("proxy GET: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.Header.Get("X-From") != "qa" {
		t.Fatalf("proxy did not reach the passthrough upstream: %+v", resp.Header)
	}
}

// TestFlipToPassthroughRewiresProxyThenBackToNative: a flippable service
// (both a local placement and a passthrough placement declared) round-trips
// between them, and the proxy listener actually re-targets each time — not
// just the reported Placement string.
func TestFlipToPassthroughRewiresProxyThenBackToNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From", "qa")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxyPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"edge": {
				Run: "sleep 30", Proxy: proxyPort,
				Upstream: upstream.URL, Passthrough: "qa",
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	before, ok := o.Service("edge")
	if !ok || before.Placement != "native" || before.PID == 0 {
		t.Fatalf("expected native placement w/ PID after Up, got %+v (ok=%v)", before, ok)
	}

	if err := o.FlipTo(context.Background(), "edge", "passthrough"); err != nil {
		t.Fatalf("FlipTo passthrough: %v", err)
	}
	mid, ok := o.Service("edge")
	if !ok || mid.Placement != "passthrough" || mid.PID != 0 {
		t.Fatalf("expected passthrough/no-pid after flip, got %+v (ok=%v)", mid, ok)
	}
	// The kill signal is synchronous but reaping isn't — poll rather than
	// asserting immediately (same pattern as TestStopThenRestart).
	if err := pollUntil(context.Background(), time.Second, func() (bool, error) {
		return !processAlive(before.PID), nil
	}); err != nil {
		t.Fatalf("native process %d still alive after flip to passthrough: %v", before.PID, err)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/anything", proxyPort))
	if err != nil {
		t.Fatalf("proxy GET after flip: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.Header.Get("X-From") != "qa" {
		t.Fatalf("proxy did not re-target to the passthrough upstream: %+v", resp.Header)
	}

	if err := o.FlipTo(context.Background(), "edge", "native"); err != nil {
		t.Fatalf("FlipTo native: %v", err)
	}
	after, ok := o.Service("edge")
	if !ok || after.Status != StatusHealthy || after.Placement != "native" || after.PID == 0 {
		t.Fatalf("state after flip back = %+v (ok=%v), want healthy/native w/ PID", after, ok)
	}
}

// TestReconcileUpstreamEditRewiresProxy: a plain config-file edit to
// `upstream:` on an already-passthrough service — no Flip/FlipTo call —
// must still re-target the live proxy listener. Reconcile's changed-service
// path skips stopCurrent for a passthrough service (nothing is running) and
// falls straight through to startService + wireProxy, the same generalized
// wireProxy FlipTo uses; this confirms that shared path actually rewires
// rather than no-opping on the "already wired" fast path.
func TestReconcileUpstreamEditRewiresProxy(t *testing.T) {
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From", "a")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From", "b")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstreamB.Close()

	proxyPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"edge": {Upstream: upstreamA.URL, Proxy: proxyPort},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	get := func() string {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/anything", proxyPort))
		if err != nil {
			t.Fatalf("proxy GET: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.Header.Get("X-From")
	}
	if got := get(); got != "a" {
		t.Fatalf("before reconcile: proxy X-From = %q, want a", got)
	}

	newCfg := *cfg
	newCfg.Services = map[string]config.Service{
		"edge": {Upstream: upstreamB.URL, Proxy: proxyPort},
	}
	result, err := o.Reconcile(context.Background(), newCfg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := actionFor(t, result, "service", "edge"); got != "restarted" {
		t.Errorf("service edge action = %q, want restarted", got)
	}

	if got := get(); got != "b" {
		t.Fatalf("after reconcile: proxy X-From = %q, want b (proxy did not re-target)", got)
	}
}

// TestFlipToPassthroughWithoutUpstreamErrors: a service with no declared
// passthrough placement has nothing to flip to, same as today's
// run/docker-only rule.
func TestFlipToPassthroughWithoutUpstreamErrors(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", Proxy: freePort(t)},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	err := o.FlipTo(context.Background(), "svc", "passthrough")
	if err == nil {
		t.Fatal("expected an error")
	}
}
