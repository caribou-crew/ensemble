package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// waitFor polls until cond passes or the deadline hits — session hops are
// appended by the manager's subscription goroutine, not the request path.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// buildChain stands up svc-front -> svc-leaf, both intercepted, and returns
// the front's normal intercept address plus the leaf proxy address.
// propagate controls which headers svc-front forwards downstream.
func buildChain(t *testing.T, p *Proxy, propagate []string) (frontAddr string) {
	t.Helper()
	leaf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"leaf":true}`)
	}))
	t.Cleanup(leaf.Close)
	leafProxy, err := p.Serve(Target{Name: "svc-leaf", Listen: "127.0.0.1:0", Upstream: leaf.URL})
	if err != nil {
		t.Fatal(err)
	}

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequest("GET", "http://"+leafProxy+"/leaf", nil)
		for _, k := range propagate {
			if v := r.Header.Get(k); v != "" {
				req.Header.Set(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		fmt.Fprint(w, `{"front":true}`)
	}))
	t.Cleanup(front.Close)
	frontProxy, err := p.Serve(Target{Name: "svc-front", Listen: "127.0.0.1:0", Upstream: front.URL})
	if err != nil {
		t.Fatal(err)
	}
	return frontProxy
}

func mustGet(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
}

func TestTwoConcurrentSessionsPlusAmbientDoNotCrossContaminate(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 256})
	p := New(rec)
	defer p.Close()
	frontProxy := buildChain(t, p, []string{"traceparent", "baggage"})

	mgr := NewSessionManager(p, rec, []string{"svc-front"})
	defer mgr.Close()

	sesA, err := mgr.Start("run-A", "svc-front", "http://"+frontProxy, "")
	if err != nil {
		t.Fatal(err)
	}
	sesB, err := mgr.Start("run-B", "svc-front", "http://"+frontProxy, "")
	if err != nil {
		t.Fatal(err)
	}

	// Note the session edge fronts the front-proxy itself, so a session call
	// records edge hop + front hop + leaf hop = 3 session hops.
	mustGet(t, "http://"+sesA.EdgeAddr+"/flow-a")
	mustGet(t, "http://"+sesB.EdgeAddr+"/flow-b")
	mustGet(t, "http://"+frontProxy+"/ambient") // interactive user, no session

	waitFor(t, "session A hops", func() bool { return len(sesA.Hops()) == 3 })
	waitFor(t, "session B hops", func() bool { return len(sesB.Hops()) == 3 })

	for _, h := range sesA.Hops() {
		if h.Session != "run-A" {
			t.Fatalf("foreign hop in session A: %+v", h)
		}
	}
	for _, h := range sesB.Hops() {
		if h.Session != "run-B" {
			t.Fatalf("foreign hop in session B: %+v", h)
		}
	}

	// Ambient traffic reached the recorder but neither session.
	waitFor(t, "ambient hops recorded", func() bool {
		n := 0
		for _, h := range rec.Snapshot() {
			if h.Session == "" {
				n++
			}
		}
		return n == 2 // front + leaf for the ambient call
	})

	if v, _ := sesA.Verdict(); v != trace.VerdictOK {
		t.Fatalf("session A verdict should be ok: %v", v)
	}
}

func TestPropagationGapDegradesSessionAndNamesService(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 256})
	p := New(rec)
	defer p.Close()
	// svc-front forwards traceparent but DROPS baggage — the provable gap.
	frontProxy := buildChain(t, p, []string{"traceparent"})

	mgr := NewSessionManager(p, rec, []string{"svc-front"})
	defer mgr.Close()
	ses, err := mgr.Start("run-X", "svc-front", "http://"+frontProxy, "")
	if err != nil {
		t.Fatal(err)
	}

	mustGet(t, "http://"+ses.EdgeAddr+"/flow")

	waitFor(t, "degraded verdict", func() bool {
		v, _ := ses.Verdict()
		return v == trace.VerdictDegraded
	})
	_, reasons := ses.Verdict()
	found := false
	for _, r := range reasons {
		if contains(r, "svc-front") {
			found = true
		}
	}
	if !found {
		t.Fatalf("reason must name the non-propagating service: %v", reasons)
	}
}

func TestUnattributedMidChainTrafficMarksSuspect(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 256})
	p := New(rec)
	defer p.Close()
	leaf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer leaf.Close()
	leafProxy, err := p.Serve(Target{Name: "svc-leaf", Listen: "127.0.0.1:0", Upstream: leaf.URL})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer front.Close()
	frontProxy, err := p.Serve(Target{Name: "svc-front", Listen: "127.0.0.1:0", Upstream: front.URL})
	if err != nil {
		t.Fatal(err)
	}

	mgr := NewSessionManager(p, rec, []string{"svc-front"}) // leaf is NOT an entry
	defer mgr.Close()
	ses, err := mgr.Start("run-Y", "svc-front", "http://"+frontProxy, "")
	if err != nil {
		t.Fatal(err)
	}

	// A context-less call lands mid-chain while the session is active.
	mustGet(t, "http://"+leafProxy+"/direct")

	waitFor(t, "suspect verdict", func() bool {
		v, _ := ses.Verdict()
		return v == trace.VerdictSuspect
	})
}

// TestControlPlaneAnnotationDoesNotDegradeSession guards against a
// specific integration bug found in ensemble/server's task-2.4 review:
// mutating API endpoints record a control-plane annotation hop straight
// into the same Recorder SessionManager subscribes to (To:
// "ensemble-control", no TraceID, no Session, no From — see
// ensemble/server/routes.go's withAnnotation). Without a guard, the
// context-less-mid-chain-arrival heuristic below treats that exactly like
// unattributed real traffic and permanently degrades every active session
// to "suspect" from ordinary control-plane usage (e.g. adjusting a latency
// rule while recording).
func TestControlPlaneAnnotationDoesNotDegradeSession(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 64})
	p := New(rec)
	defer p.Close()
	frontProxy := buildChain(t, p, []string{"traceparent", "baggage"})

	mgr := NewSessionManager(p, rec, []string{"svc-front"})
	defer mgr.Close()
	ses, err := mgr.Start("run-ctl", "svc-front", "http://"+frontProxy, "")
	if err != nil {
		t.Fatal(err)
	}

	// Mirrors exactly what withAnnotation records for every mutating API
	// call: no trace context at all, To a sentinel name that's never a
	// real entry service.
	rec.Record(trace.Hop{
		To:     "ensemble-control",
		Method: "PUT",
		Path:   "/api/latency",
		Status: 200,
	})

	// A real session call afterward: SessionManager.loop is a single
	// goroutine ranging over the Recorder's subscription channel in
	// delivery order, so these hops landing in ses.Hops() proves the
	// annotation above was already routed — no sleep-based race needed.
	mustGet(t, "http://"+ses.EdgeAddr+"/flow")
	waitFor(t, "session hops after annotation", func() bool { return len(ses.Hops()) == 3 })

	if v, reasons := ses.Verdict(); v != trace.VerdictOK {
		t.Fatalf("control-plane annotation degraded the session: verdict=%v reasons=%v", v, reasons)
	}
}

func TestEndStopsEdgeAndFinalizesSession(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 64})
	p := New(rec)
	defer p.Close()
	frontProxy := buildChain(t, p, []string{"traceparent", "baggage"})

	mgr := NewSessionManager(p, rec, []string{"svc-front"})
	defer mgr.Close()
	ses, err := mgr.Start("run-Z", "svc-front", "http://"+frontProxy, "")
	if err != nil {
		t.Fatal(err)
	}
	mustGet(t, "http://"+ses.EdgeAddr+"/flow")
	waitFor(t, "hops", func() bool { return len(ses.Hops()) == 3 })

	done := mgr.End("run-Z")
	if done == nil || len(done.Hops()) != 3 {
		t.Fatalf("End must return the finalized session")
	}
	// The edge listener is gone.
	if _, err := http.Get("http://" + ses.EdgeAddr + "/x"); err == nil {
		t.Fatal("edge listener still accepting after End")
	}
	if mgr.End("run-Z") != nil {
		t.Fatal("double End must return nil")
	}
}

// TestSlowSubscriberDropDegradesSessionVerdict guards final-review finding
// I8: the Recorder's fan-out to SessionManager's subscription is
// non-blocking, so a burst that outpaces the manager's own single
// consumption goroutine silently dropped hops for it — a session could
// lose hops yet still report verdict "ok". A capture-trust verdict that
// reads "ok" on an incomplete capture is worse than no verdict, so a
// detected drop must degrade every session active while it happened.
//
// Forces a real overflow (not a synthetic signal): a single producer
// goroutine floods rec.Record — cheap (one mutex, a 1-entry subs map, a
// non-blocking send) — far faster than the manager's consumer goroutine,
// which does real work per hop (channel receive, lock, map lookup,
// append), so the subscription's 256-slot buffer is guaranteed to
// overflow well before the consumer can drain it.
func TestSlowSubscriberDropDegradesSessionVerdict(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	mgr := NewSessionManager(p, rec, nil)
	defer mgr.Close()
	ses, err := mgr.Start("run-drop", "svc", "http://127.0.0.1:1", "") // never dialed
	if err != nil {
		t.Fatal(err)
	}

	const flood = 5000
	for i := 0; i < flood; i++ {
		rec.Record(trace.Hop{Session: ses.ID, To: "svc", TraceID: fmt.Sprintf("t-%d", i)})
	}

	waitFor(t, "degraded verdict from a dropped hop", func() bool {
		v, _ := ses.Verdict()
		return v == trace.VerdictDegraded
	})
	_, reasons := ses.Verdict()
	found := false
	for _, r := range reasons {
		if contains(r, "dropped") {
			found = true
		}
	}
	if !found {
		t.Fatalf("reason must mention the drop: %v", reasons)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
