package capture

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestStandaloneCaptureRecordsClientEdgeHopsAndWritesWireJsonl(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"token":"secret-value"}`))
	}))
	defer upstream.Close()

	cwd := t.TempDir()
	s, err := StartStandalone(Options{
		Cwd: cwd, App: "web", Flow: "checkout", Upstream: upstream.URL,
		Redact: []string{"token"},
		Now:    func() time.Time { return time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}

	resp, err := http.Get(s.ProxyURL + "/cart")
	if err != nil {
		t.Fatalf("through proxy: %v", err)
	}
	resp.Body.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	hops, skipped, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil || skipped != 0 || len(hops) != 1 {
		t.Fatalf("wire.jsonl hops = %v (%v)", hops, err)
	}
	if hops[0].Path != "/cart" || hops[0].Status != 200 {
		t.Fatalf("hop = %+v", hops[0])
	}
	if strings.Contains(hops[0].Resp.Body, "secret-value") {
		t.Fatal("redaction must happen at capture: the plaintext must never reach disk")
	}
	if _, err := os.Stat(s.Paths.HopsPath); !os.IsNotExist(err) {
		t.Fatal("standalone mode must NOT write hops.jsonl — an absent chain and an empty chain are different facts")
	}
}

func TestSessionEnvCarriesTheFullHandshake(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	s, err := StartStandalone(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	env := map[string]string{}
	for _, kv := range s.Env() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, k := range []string{"RETRACE_RUN_DIR", "RETRACE_PROXY_URL", "RETRACE_MARKER_URL"} {
		if env[k] == "" {
			t.Errorf("%s is missing or empty; the handshake is all-or-nothing", k)
		}
	}
	if env["RETRACE_RUN_DIR"] != s.Paths.RunDir {
		t.Errorf("RETRACE_RUN_DIR = %q, want %q", env["RETRACE_RUN_DIR"], s.Paths.RunDir)
	}
}

// Geometry comes from the PNG header, and it is the shot's REAL geometry —
// pre-trim. Trimming is a compare-time decision (Task 7/Task 10); the
// manifest records what was actually captured.
func TestCheckpointsReadsShotGeometryFromPngHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	s, err := StartStandalone(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 40, 40))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Paths.ShotsDir, "cart.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sibling marker file must not be mistaken for a checkpoint.
	if err := os.WriteFile(filepath.Join(s.Paths.ShotsDir, "cart.trim"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cps, err := s.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}
	want := []runs.Checkpoint{{Name: "cart", File: "shots/cart.png", Width: 40, Height: 40, Trim: true}}
	if !reflect.DeepEqual(cps, want) {
		t.Fatalf("checkpoints = %+v, want %+v", cps, want)
	}
}

// A user trying retrace in /tmp must still get a recording: no repository
// is a zero value, never an error.
func TestGitInfoIsAZeroValueOutsideARepository(t *testing.T) {
	if got := GitInfo(t.TempDir()); got != (runs.Git{}) {
		t.Fatalf("GitInfo outside a repo = %+v, want the zero value", got)
	}
}

// --- zero-value pins (global-constraints.md, both clauses) ---

// An empty Upstream is the zero value of Options.Upstream and it means
// "the caller never said where the app's traffic goes" — which must be a
// refusal, not a proxy pointed at "" that turns every captured call into a
// 502 and writes a run directory claiming a capture happened. Mutating
// StartStandalone to drop the check (proceed with Upstream: "") makes this
// test fail.
func TestStandaloneRefusesWithoutAnUpstream(t *testing.T) {
	s, err := StartStandalone(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout"})
	if err == nil {
		s.Close()
		t.Fatal("StartStandalone with no upstream succeeded; an unset upstream must refuse, not capture into the void")
	}
	if !strings.Contains(err.Error(), "--upstream") {
		t.Errorf("error = %q, want it to name --upstream", err)
	}
	if entries, _ := os.ReadDir(runs.RunsRoot(t.TempDir())); len(entries) != 0 {
		t.Errorf("a refused start left %d run directories behind", len(entries))
	}
}

// A nil Recorder is the zero value of Session.rec and it means "this
// session has no LOCAL recorder" (Task 5's ensemble-attached mode drains
// hops over REST instead). It must not mean "call Snapshot anyway":
// (*proxy.Recorder).Snapshot takes r.mu.Lock() on its receiver and panics
// on nil, and Task 6 calls RequestsSeen on EVERY run. Deleting the nil
// guard in RequestsSeen (or in Hops) makes this test panic instead of
// pass.
func TestRequestsSeenAndHopsTolerateASessionWithNoLocalRecorder(t *testing.T) {
	s := &Session{Mode: runs.ModeEnsemble}
	s.requests.Add(3)
	if got := s.RequestsSeen(); got != 3 {
		t.Fatalf("RequestsSeen = %d, want 3", got)
	}
	if got := s.Hops(); got != nil {
		t.Fatalf("Hops = %+v, want nil", got)
	}
}

// Every request that reaches the marker door counts as traffic that
// reached retrace: Task 6 keys "the app never routed through us" on
// RequestsSeen being zero, and a flow that only posted markers still
// proves the handshake worked.
func TestRequestsSeenCountsProxiedCallsAndMarkers(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	s, err := StartStandalone(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	if got := s.RequestsSeen(); got != 0 {
		t.Fatalf("RequestsSeen before any traffic = %d, want 0", got)
	}
	resp, err := http.Get(s.ProxyURL + "/cart")
	if err != nil {
		t.Fatalf("through proxy: %v", err)
	}
	resp.Body.Close()
	resp, err = http.Post(s.MarkerURL+"/group", "application/json", strings.NewReader(`{"name":"checkout"}`))
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	resp.Body.Close()

	if got := s.RequestsSeen(); got != 2 {
		t.Fatalf("RequestsSeen = %d, want 2 (one proxied call + one marker)", got)
	}
}

// ProxyFailure's zero value is nil — "the interceptor never misbehaved" —
// and ProxyDied is its only producer. WatchProxy must actually record one
// when the client-edge listener stops answering, or Task 6 can never tell
// "the flow made no calls" from "the flow's calls went nowhere".
func TestWatchProxyRecordsAFailureWhenTheListenerStops(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	s, err := StartStandalone(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	if s.ProxyFailure() != nil {
		t.Fatalf("ProxyFailure on a healthy session = %+v, want nil", s.ProxyFailure())
	}
	s.ProxyDied(os.ErrClosed)
	f := s.ProxyFailure()
	if f == nil || f.Phase != "running" {
		t.Fatalf("ProxyFailure = %+v, want a running-phase failure", f)
	}
	// First failure wins: a listener that stops answering keeps failing the
	// dial, and the last message is the least informative one.
	s.ProxyDied(os.ErrDeadlineExceeded)
	if got := s.ProxyFailure().Message; got != os.ErrClosed.Error() {
		t.Fatalf("ProxyFailure.Message = %q, want the first failure %q", got, os.ErrClosed.Error())
	}
}
