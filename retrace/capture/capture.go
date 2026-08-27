// Package capture owns the recording half of retrace: it opens (or borrows)
// a listener, points the test command at it through env, and writes the run
// directory. It deliberately does NOT contain a proxy — core/proxy is the
// one interceptor in this repo and this package reuses it, so a hop retrace
// records is byte-identical to a hop ensemble streams.
package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	// Host is the hostname the client-edge listener binds on AND is
	// advertised as (RETRACE_PROXY_URL). Empty means the default,
	// "127.0.0.1" — see defaultHost. Exists for a URL-bound auth validator
	// that compares hostnames rather than full URLs (design.md §6.1.2); a
	// non-default value must resolve loopback-only, or core/proxy refuses.
	Host string
	// Port is the fixed TCP port the client-edge listener binds on AND is
	// advertised as. Zero means the default — an ephemeral port chosen by
	// the OS, see listenPort. Exists for a URL-bound auth validator that
	// does strict origin+path matching against a fixed allowlist of
	// origins (design.md §6.1.2's proxy.port addendum): proxy.host alone
	// only fixes the hostname half of that match. A configured port
	// already held by another process fails the run immediately — the OS
	// bind error already names it, and retrace never silently picks a
	// different port than the one asked for.
	Port    int
	Redact  []string
	MaxBody int
	Now     func() time.Time
}

// listenPort normalizes Options.Port: zero means "the caller did not
// choose", which resolves to "0" — net.Listen's own ephemeral-port
// request, the long-standing default. A non-zero value is used literally.
func listenPort(p int) string {
	if p == 0 {
		return "0"
	}
	return strconv.Itoa(p)
}

// defaultHost normalizes Options.Host: empty means "the caller did not
// choose", which resolves to the long-standing default rather than to
// net.Listen's own interpretation of an empty host (all interfaces) — a
// capture proxy defaulting to every interface would be a silent widening
// of what "just works" for a tool whose whole threat model assumes
// loopback-only (core/httpguard; design.md §6.1.2).
func defaultHost(h string) string {
	if h == "" {
		return "127.0.0.1"
	}
	return h
}

type Session struct {
	Paths runs.Paths
	RunID string
	// App and Flow are carried on the Session (not only in the Paths they
	// were joined into) because the owner record names them as data, and
	// parsing them back out of a filesystem path would be a second, weaker
	// source of truth for values PathsFor already validated.
	App         string
	Flow        string
	ProxyURL    string
	MarkerURL   string
	UpstreamURL string
	Mode        string
	StartedAt   time.Time

	rec       *proxy.Recorder
	prox      *proxy.Proxy
	stopProxy func()
	markerSrv *http.Server
	wireFile  *os.File
	requests  atomic.Int64
	closed    bool

	// Redaction inputs, kept on the Session because the attached path
	// writes its hops at Close time rather than streaming them through a
	// Recorder. Set in BOTH constructors: a Session that forgot them would
	// write the user's own keys to disk in plaintext.
	redact  []string
	maxBody int

	// ens is nil in standalone mode and non-nil in ensemble-attached mode.
	// It is the single discriminator every method below branches on — see
	// Drain, Close, ProxyFailure and WatchProxy in ensemble.go.
	ens        EnsembleClient
	hops       []trace.Hop // drained from ensemble; nil in standalone
	endReport  EndReport
	ended      bool // EndSession actually returned a report — see EndVerdict
	trustNotes []string

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
		Listen:        defaultHost(o.Host) + ":" + listenPort(o.Port),
		Upstream:      strings.TrimRight(o.Upstream, "/"),
		InjectBaggage: map[string]string{trace.BaggageSession: runID},
	})
	if err != nil {
		wire.Close()
		return nil, err
	}

	s := &Session{
		Paths: p, RunID: runID, App: o.App, Flow: o.Flow, Mode: runs.ModeStandalone,
		StartedAt: now(), rec: rec, prox: prox, stopProxy: stop, wireFile: wire,
		ProxyURL:    "http://" + addr,
		UpstreamURL: strings.TrimRight(o.Upstream, "/"),
		redact:      o.Redact, maxBody: maxBody,
	}
	if err := s.startMarkerDoor(now); err != nil {
		stop()
		wire.Close()
		return nil, err
	}
	return s, nil
}

// startMarkerDoor opens the marker/supervision door and, once its address
// is known, claims the run directory by writing the owner record.
//
// The two are one step on purpose. This is the last thing both constructors
// do, and it is the moment the run becomes addressable: MarkerURL does not
// exist until the listener is bound, and the owner record's whole job is to
// let someone map a bound port back to this run. Splitting them into two
// calls in two constructors is how one of them eventually forgets, leaving
// a directory that holds listeners nothing can trace back.
//
// The record is written BEFORE the test command runs (both constructors
// return into that), so a command that crashes the process on its first
// line still leaves a directory that names its owner.
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

	// A run that cannot record its owner is a run nothing can supervise, so
	// this is a hard failure rather than a warning: both constructors treat
	// a startMarkerDoor error as fatal and clean up the directory, which is
	// the right outcome — better no run than an untraceable one.
	return runs.MarkRunning(s.Paths, runs.Running{
		App:       s.App,
		Flow:      s.Flow,
		RunID:     s.RunID,
		ProxyURL:  s.ProxyURL,
		MarkerURL: s.MarkerURL,
		StartedAt: s.StartedAt,
	})
}

// Env is the test-command handshake. RETRACE_UPSTREAM_URL is conditional,
// not all-or-nothing like the other three: it exists so an app using a
// URL-bound auth scheme (DPoP/RFC 9449 and similar) can sign requests
// against the real service address while routing the bytes through
// RETRACE_PROXY_URL — a proof minted against the proxy's own address
// fails validation upstream, and retrace holds no private key to
// re-sign one that was (see design.md §6.1.2). It is empty, and
// therefore omitted, whenever the caller never configured an upstream —
// attached-mode captures don't require one, and a flow with no
// URL-bound auth has no use for it either.
func (s *Session) Env() []string {
	env := []string{
		"RETRACE_RUN_DIR=" + s.Paths.RunDir,
		"RETRACE_PROXY_URL=" + s.ProxyURL,
		"RETRACE_MARKER_URL=" + s.MarkerURL,
	}
	if s.UpstreamURL != "" {
		env = append(env, "RETRACE_UPSTREAM_URL="+s.UpstreamURL)
	}
	return env
}

// Hops returns the full provider chain. Standalone reads it from the local
// Recorder; ensemble-attached mode has no local Recorder at all and returns
// what Drain pulled over REST (nil before Drain runs).
func (s *Session) Hops() []trace.Hop {
	if s.rec != nil {
		return s.rec.Snapshot()
	}
	return s.hops
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
// It is always nil in ensemble-attached mode: retrace does not own the
// client-edge listener there, so it cannot truthfully witness it dying, and
// reporting ensemble's edge as "retrace's proxy died" would put a
// broken/proxy-died verdict on a run whose capture machinery was fine.
func (s *Session) ProxyFailure() *ProxyFailure {
	if s.ens != nil {
		return nil
	}
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
	if s.ens != nil {
		return // ensemble owns the listener; see ProxyFailure
	}
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

// DeviceFile is the name an adapter writes its screen geometry to, in the
// run directory beside manifest.json. A file rather than a marker-door route
// for the same reason screenshots are: an adapter that can write shots can
// write this, and one that cannot has no geometry to report anyway.
const DeviceFile = "device.json"

// Device resolves the screen this run's shots were taken on: the adapter's
// device.json if it wrote one, otherwise the geometry of the first shot.
//
// The fallback is not a nicety. Without it, every run captured by an adapter
// that does not write device.json — including every run captured before this
// existed — would have a nil Device, and the comparison guard treats nil as
// unknown and steps aside. A guard that turns itself off for the majority of
// runs is not a guard, so the weakest evidence available (one screenshot's
// own dimensions) is recorded, honestly labelled Kind "shot".
//
// It is genuinely weaker: a shot of a scrolled page or a single element is
// not the screen. That is exactly why Kind is on the record and why a
// mismatch message says where each side's number came from — a reader
// comparing two "shot" runs needs to know they are comparing screenshots,
// not viewports.
//
// checkpoints is passed in rather than re-scanned so the fallback names the
// same first shot the manifest reports (Checkpoints sorts by name, so
// "first" is deterministic rather than whatever the directory yielded).
// Returns nil, nil when there is neither a device.json nor a single shot —
// no evidence, stated as no evidence.
func (s *Session) Device(checkpoints []runs.Checkpoint) (*runs.Device, error) {
	b, err := os.ReadFile(filepath.Join(s.Paths.RunDir, DeviceFile))
	switch {
	case err == nil:
		var d runs.Device
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("%s is not readable JSON: %w", DeviceFile, err)
		}
		// An adapter that wrote the file and left kind blank still told us
		// something real; default it rather than refusing the run. "browser"
		// is not assumed — that would put a provenance on the record nobody
		// asserted.
		if d.Kind == "" {
			d.Kind = "device"
		}
		if d.Width <= 0 || d.Height <= 0 {
			return nil, fmt.Errorf("%s reports a %dx%d screen — a device.json that is written at all must report a real one", DeviceFile, d.Width, d.Height)
		}
		return &d, nil
	case errors.Is(err, os.ErrNotExist):
		if len(checkpoints) == 0 {
			return nil, nil
		}
		first := checkpoints[0]
		return &runs.Device{Kind: "shot", ID: first.Name, Width: first.Width, Height: first.Height}, nil
	default:
		return nil, err
	}
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
