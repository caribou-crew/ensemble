package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLongestPrefixWinsAndTargetBeatsWildcard(t *testing.T) {
	s := NewLatencyStore(nil)
	s.Set(LatencyRule{Target: "*", Path: "/", FixedMs: 1, Enabled: true})
	s.Set(LatencyRule{Target: "bff", Path: "/", FixedMs: 10, Enabled: true})
	s.Set(LatencyRule{Target: "bff", Path: "/orders", FixedMs: 100, Enabled: true})

	cases := []struct {
		target, path string
		wantMs       float64
	}{
		{"bff", "/orders/123", 100}, // longest prefix
		{"bff", "/users", 10},       // target root
		{"svc", "/orders", 1},       // wildcard fallback
	}
	for _, c := range cases {
		if got := s.DelayFor(c.target, c.path); got != time.Duration(c.wantMs)*time.Millisecond {
			t.Fatalf("%s %s: want %vms, got %v", c.target, c.path, c.wantMs, got)
		}
	}
}

func TestDisarmedRuleInjectsNothing(t *testing.T) {
	s := NewLatencyStore(nil)
	s.Set(LatencyRule{Target: "bff", Path: "/", FixedMs: 50, Enabled: false})
	if got := s.DelayFor("bff", "/x"); got != 0 {
		t.Fatalf("disarmed rule fired: %v", got)
	}
	s.ArmAll(true)
	if got := s.DelayFor("bff", "/x"); got != 50*time.Millisecond {
		t.Fatalf("ArmAll did not arm: %v", got)
	}
	s.Reset()
	if got := s.DelayFor("bff", "/x"); got != 0 {
		t.Fatalf("Reset left rules behind: %v", got)
	}
}

func TestDistributionSamplesQuantileAnchors(t *testing.T) {
	// The CDF is anchored at (0,0) (0.5,p50) (0.95,p95) (0.99,p99) (1,p99)
	// with linear interpolation. A pinned uniform source makes it exact.
	u := 0.0
	s := NewLatencyStore(func() float64 { return u })
	s.Set(LatencyRule{Target: "svc", Path: "/", P50: 100, P95: 400, P99: 900, Enabled: true})

	cases := []struct {
		u      float64
		wantMs float64
	}{
		{0, 0},
		{0.5, 100},
		{0.95, 400},
		{0.99, 900},
		{0.25, 50},   // halfway to the p50 anchor
		{0.725, 250}, // halfway between p50 and p95 anchors
	}
	for _, c := range cases {
		u = c.u
		if got := s.DelayFor("svc", "/x"); got != time.Duration(c.wantMs*float64(time.Millisecond)) {
			t.Fatalf("u=%v: want %vms, got %v", c.u, c.wantMs, got)
		}
	}
}

func TestSourceRoundTripsAndIsIgnoredByDelayFor(t *testing.T) {
	s := NewLatencyStore(nil)
	src := "datadog:p{P}:trace.http.server.request.duration{service:billing,env:prod} (last 60m)"
	s.Set(LatencyRule{Target: "billing", Path: "/", P50: 45, P95: 120, P99: 340, Enabled: true, Source: src})

	rules := s.Rules()
	if len(rules) != 1 || rules[0].Source != src {
		t.Fatalf("source did not round-trip: %+v", rules)
	}
	if got := s.DelayFor("billing", "/x"); got == 0 {
		t.Fatalf("rule with Source set did not delay: %v", got)
	}
}

func TestSetUpsertsByTargetAndPath(t *testing.T) {
	s := NewLatencyStore(nil)
	s.Set(LatencyRule{Target: "bff", Path: "/x", FixedMs: 10, Enabled: true})
	s.Set(LatencyRule{Target: "bff", Path: "/x", FixedMs: 20, Enabled: true})
	if rules := s.Rules(); len(rules) != 1 || rules[0].FixedMs != 20 {
		t.Fatalf("upsert failed: %+v", rules)
	}
	s.Remove("bff", "/x")
	if rules := s.Rules(); len(rules) != 0 {
		t.Fatalf("remove failed: %+v", rules)
	}
}

func TestProxyInjectsDelayAndRecordsItDistinctly(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	lat := NewLatencyStore(nil)
	p := New(rec)
	p.Latency = lat
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "svc", Listen: "127.0.0.1:0", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}

	lat.Set(LatencyRule{Target: "svc", Path: "/", FixedMs: 120, Enabled: true})
	begin := time.Now()
	resp, err := http.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	elapsed := time.Since(begin)

	if elapsed < 120*time.Millisecond {
		t.Fatalf("delay not applied on the wire: %v", elapsed)
	}
	h := rec.Snapshot()[0]
	if h.InjectedDelayMs != 120 {
		t.Fatalf("injected delay not recorded: %+v", h)
	}
	// Upstream timings must EXCLUDE the injected delay — timings stay honest.
	if h.T.DoneMs >= 120 {
		t.Fatalf("upstream time contaminated by injected delay: %+v", h.T)
	}
}
