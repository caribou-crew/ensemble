// Package capture owns the recording half of retrace: it opens (or borrows)
// a listener, points the test command at it through env, and writes the run
// directory. It deliberately does NOT contain a proxy — core/proxy is the
// one interceptor in this repo and this package reuses it, so a hop retrace
// records is byte-identical to a hop ensemble streams.
package capture

import (
	"context"
	"fmt"
	"image"
	_ "image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

type Options struct {
	Cwd      string
	App      string
	Flow     string
	Upstream string
	Redact   []string
	MaxBody  int
	Now      func() time.Time
}

type Session struct {
	Paths     runs.Paths
	RunID     string
	ProxyURL  string
	MarkerURL string
	Mode      string
	StartedAt time.Time

	rec       *proxy.Recorder
	prox      *proxy.Proxy
	stopProxy func()
	markerSrv *http.Server
	wireFile  *os.File
	requests  atomic.Int64
	closed    bool

	mu           sync.Mutex
	proxyFailure *ProxyFailure
}

// ProxyFailure is the one structured "the interceptor itself misbehaved"
// fact a run can record. Phase is always "running": see Session.ProxyDied.
// Task 6's Assess consumes this type; it is declared HERE, in the task that
// produces it, so package capture has exactly one declaration of it.
type ProxyFailure struct {
	Phase   string `json:"phase"` // always "running" — see ProxyDied
	Message string `json:"message"`
}

func StartStandalone(o Options) (*Session, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	if o.Upstream == "" {
		return nil, fmt.Errorf("standalone capture needs --upstream (the base URL clients would call)")
	}
	runID := runs.NewRunID(now(), gitSHA(o.Cwd))
	p, err := runs.Create(runs.RunsRoot(o.Cwd), o.App, o.Flow, runID)
	if err != nil {
		return nil, err
	}
	wire, err := os.Create(p.WirePath)
	if err != nil {
		return nil, err
	}
	maxBody := o.MaxBody
	if maxBody <= 0 {
		maxBody = proxy.CaptureLimit
	}
	// Redaction at capture: the Recorder scrubs before the ring, before the
	// writer, before anything is streamed. Phase 4b swaps per-key modes in
	// at exactly this seam.
	rec := proxy.NewRecorder(proxy.RecorderOpts{
		Ring:     8192,
		Redactor: trace.NewRedactor(o.Redact, maxBody),
		Writer:   trace.NewWriter(wire),
	})
	prox := proxy.New(rec)
	addr, stop, err := prox.ServeStoppable(proxy.Target{
		Name:          "client-edge",
		Listen:        "127.0.0.1:0",
		Upstream:      strings.TrimRight(o.Upstream, "/"),
		InjectBaggage: map[string]string{trace.BaggageSession: runID},
	})
	if err != nil {
		wire.Close()
		return nil, err
	}

	s := &Session{
		Paths: p, RunID: runID, Mode: runs.ModeStandalone,
		StartedAt: now(), rec: rec, prox: prox, stopProxy: stop, wireFile: wire,
		ProxyURL: "http://" + addr,
	}
	if err := s.startMarkerDoor(now); err != nil {
		stop()
		wire.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) startMarkerDoor(now func() time.Time) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	// Counted INSIDE the guard (NewMarkerDoorCounted's onAdmitted), not
	// outside it: a request the guard rejects (cross-site, DNS-rebinding)
	// must never inflate RequestsSeen — see its doc comment.
	door := NewMarkerDoorCounted(s.Paths, now, func() { s.requests.Add(1) })
	s.markerSrv = &http.Server{Handler: door}
	go s.markerSrv.Serve(ln)
	s.MarkerURL = "http://" + ln.Addr().String()
	return nil
}

func (s *Session) Env() []string {
	return []string{
		"RETRACE_RUN_DIR=" + s.Paths.RunDir,
		"RETRACE_PROXY_URL=" + s.ProxyURL,
		"RETRACE_MARKER_URL=" + s.MarkerURL,
	}
}

func (s *Session) Hops() []trace.Hop {
	if s.rec == nil {
		return nil
	}
	return s.rec.Snapshot()
}

// RequestsSeen counts everything that reached retrace at all — proxied calls
// plus markers. Zero of these is proof the app never routed through us,
// which is a different (and much worse) fact than "the flow made no calls".
//
// The nil check is load-bearing, not defensive style. In ensemble-attached
// mode (Task 5) there is no local Recorder at all — ensemble owns the
// listener and retrace drains hops over REST — so s.rec is nil, and
// (*proxy.Recorder).Snapshot takes r.mu.Lock() on its receiver, which
// panics on nil. Task 6 feeds RequestsSeen into Assess on EVERY run, so
// without this guard every ensemble-attached `retrace run` crashes at
// manifest time. Task 5 counts attached traffic into s.requests instead.
func (s *Session) RequestsSeen() int {
	n := int(s.requests.Load())
	if s.rec != nil {
		n += len(s.rec.Snapshot())
	}
	return n
}

func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.stopProxy != nil {
		s.stopProxy()
	}
	if s.markerSrv != nil {
		s.markerSrv.Close()
	}
	if s.wireFile == nil {
		return nil
	}
	return s.wireFile.Close()
}

// ProxyDied records that the client-edge listener stopped answering while
// the test command was still running. This is the ONLY producer of a
// capture.ProxyFailure (Task 6 ranks it `broken/proxy-died`), and it is
// what makes that reason code reachable outside its unit test: a bind
// failure aborts StartStandalone before a manifest exists, so there is no
// such thing as a recorded `proxy-never-started`.
func (s *Session) ProxyDied(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxyFailure == nil {
		s.proxyFailure = &ProxyFailure{Phase: "running", Message: err.Error()}
	}
}

// ProxyFailure returns the recorded running-phase failure, or nil. Task 4's
// run body calls this after the test command exits and passes the result
// into Task 6's Assess.
func (s *Session) ProxyFailure() *ProxyFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxyFailure
}

// WatchProxy polls the client-edge listener while the test command runs. A
// listener that stops accepting is the difference between "the flow made no
// calls" and "the flow's calls went nowhere" — Task 6 must be able to tell
// those apart, and only this loop can tell it.
//
// It samples once BEFORE entering the tick loop and once more on
// ctx.Done(), rather than relying solely on the 500ms tick: runFlow starts
// this goroutine and cancels it the instant the test command returns, so a
// flow shorter than one tick period would otherwise never be sampled at
// all, and a proxy that died inside a fast test would go unrecorded.
func (s *Session) WatchProxy(ctx context.Context) {
	if !s.probeProxy() {
		return
	}
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.probeProxy()
			return
		case <-t.C:
			if !s.probeProxy() {
				return
			}
		}
	}
}

// probeProxy dials the client-edge listener once. It records a
// ProxyFailure (via ProxyDied) and reports false on a failed dial; true
// means the listener answered.
func (s *Session) probeProxy() bool {
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(s.ProxyURL, "http://"), 200*time.Millisecond)
	if err != nil {
		s.ProxyDied(err)
		return false
	}
	c.Close()
	return true
}

// Checkpoints reads shots/*.png and decodes each header for geometry. PNG
// dimensions are decoded via image.DecodeConfig, so a 4MB screenshot costs
// a 33-byte read rather than a full decode.
func (s *Session) Checkpoints() ([]runs.Checkpoint, error) {
	entries, err := os.ReadDir(s.Paths.ShotsDir)
	if err != nil {
		return nil, err
	}
	var out []runs.Checkpoint
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			continue
		}
		f, err := os.Open(filepath.Join(s.Paths.ShotsDir, e.Name()))
		if err != nil {
			return nil, err
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("checkpoint %s is not a readable PNG: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		// A `<name>.trim` marker beside the shot means the adapter asked
		// for uniform-border trimming. Record the request; do NOT trim
		// here. Trimming needs retrace/pixel, and capture importing pixel
		// would put a capture → pixel edge in a dependency graph that is
		// deliberately capture → runs, proxy, trace. Width/Height stay
		// pre-trim for the same reason: the manifest reports what was
		// captured, and the compare step reports what it used.
		_, trimErr := os.Stat(filepath.Join(s.Paths.ShotsDir, name+".trim"))
		out = append(out, runs.Checkpoint{
			Name:   name,
			File:   filepath.ToSlash(filepath.Join("shots", e.Name())),
			Width:  cfg.Width,
			Height: cfg.Height,
			Trim:   trimErr == nil,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// gitSHA shells out to git for the run id's provenance suffix. A repo-less
// directory is normal (a user trying retrace in /tmp), so failure is "" and
// NewRunID falls back to "nogit" — never an error that blocks a recording.
func gitSHA(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GitInfo is the manifest's provenance block. Same rule as gitSHA: a
// missing repo is a zero value, never an error — the manifest needs it and
// Task 11's reference-eligibility rules read Git.Dirty.
//
// EXPORTED, unlike gitSHA. gitSHA is called only from inside this package
// (both constructors), but GitInfo is called from runFlow in
// `package main` — same reasoning as WatchProxy. An unexported identifier
// used across a package boundary is `undefined:` at build time, on the only
// path `retrace run` has.
func GitInfo(dir string) runs.Git {
	g := runs.Git{SHA: gitSHA(dir)}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		g.Branch = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err == nil {
		g.Dirty = strings.TrimSpace(string(out)) != ""
	}
	return g
}
