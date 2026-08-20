package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/stub"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/inspector"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// upOptions configures one `ensemble up` run.
type upOptions struct {
	ConfigPath string
	Profiles   []string
	Addr       string // control-plane API listen address, e.g. ":4700"
}

// defaultAPIAddr is the loopback-only control-plane listen address `ensemble
// up` binds unless overridden with --api. It must match defaultAPIURL()'s
// client-side assumption (main.go) that the API lives on 127.0.0.1:4700.
//
// Deliberately NOT ":4700" (all interfaces): every route except
// POST /api/shutdown is unauthenticated, so binding wide would expose full
// captured request/response bodies, arbitrary seed execution, and
// service/latency control to anyone on the local network.
const defaultAPIAddr = "127.0.0.1:4700"

// Hop log rotation budget: at most hopLogKeep+1 files of hopLogMaxBytes
// each under .ensemble/, i.e. half a gigabyte at the defaults below. Sized
// so a long soak still leaves days of history on disk while the ceiling
// stays something a laptop won't notice.
const (
	hopLogMaxBytes = 128 << 20 // 128 MiB per file
	hopLogKeep     = 3         // hops.jsonl.1 .. hops.jsonl.3
)

// parseUpOptions parses `up`'s flags into an upOptions. Split out of cmdUp
// so tests can assert on flag defaults (notably --api's loopback default)
// without driving a full process.
func parseUpOptions(args []string, stderr io.Writer) (upOptions, error) {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("c", "ensemble.yaml", "path to ensemble.yaml")
	profile := fs.String("profile", "", "comma-separated active profiles")
	addr := fs.String("api", defaultAPIAddr, "control-plane API listen address")
	if err := fs.Parse(args); err != nil {
		return upOptions{}, err
	}
	return upOptions{ConfigPath: *cfgPath, Profiles: splitCSV(*profile), Addr: *addr}, nil
}

func cmdUp(args []string, stdout, stderr io.Writer) int {
	opts, err := parseUpOptions(args, stderr)
	if err != nil {
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runUp(ctx, opts, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "ensemble: up: %v\n", err)
		return 1
	}
	return 0
}

// runUp loads opts.ConfigPath and wires the full local stack — Recorder
// (with an NDJSON writer under .ensemble/hops.jsonl and a Redactor seeded
// from cfg.Redact), Proxy, LatencyStore (seeded from cfg.Latency.Defaults),
// SessionManager (entry targets from cfg.Services), Orchestrator (SQLRunner
// from ensemble/inspector), and cfg.Stubs — then serves the control-plane
// API on opts.Addr until ctx is canceled (SIGINT/SIGTERM, or the API's own
// POST /api/shutdown), at which point it tears everything down.
//
// Split from cmdUp so tests can drive it in-process with a
// context.CancelFunc standing in for SIGINT, per the brief's "SIGINT path
// via context cancel" test requirement.
func runUp(ctx context.Context, opts upOptions, stdout, stderr io.Writer) error {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	hopsPath := filepath.Join(cfg.Dir, ".ensemble", "hops.jsonl")
	// Rotating, and owner-only (0700/0600, not 0755/0644): hops.jsonl is a
	// verbatim capture of every request and response flowing through the
	// stack — bearer tokens the redactor didn't know to scrub, session
	// cookies, customer data — and an unbounded append would fill the disk
	// of anyone who leaves a stack (or a retry loop) running overnight.
	hopsFile, err := trace.OpenRotatingFile(hopsPath, hopLogMaxBytes, hopLogKeep)
	if err != nil {
		return fmt.Errorf("open hops log: %w", err)
	}
	defer hopsFile.Close()

	redactor := trace.NewRedactor(cfg.Redact, 0)
	rec := proxy.NewRecorder(proxy.RecorderOpts{
		Redactor: redactor,
		Writer:   trace.NewWriter(hopsFile),
	})

	px := proxy.New(rec)
	lat := proxy.NewLatencyStore(nil)
	px.Latency = lat
	for _, d := range cfg.Latency.Defaults {
		lat.Set(proxy.LatencyRule{
			Target: d.Target, Path: d.Path,
			FixedMs: d.FixedMs, P50: d.P50, P95: d.P95, P99: d.P99,
			Enabled: d.Enabled,
		})
	}

	var entries []string
	for name, svc := range cfg.Services {
		if svc.Entry {
			entries = append(entries, name)
		}
	}
	sessions := proxy.NewSessionManager(px, rec, entries)
	defer sessions.Close()

	orch := orchestrator.New(cfg, px, orchestrator.Opts{
		Profiles: opts.Profiles,
		Logf:     func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) },
	})
	orch.SQLRunner = inspector.NewSQLRunner(cfg.Databases)

	stubs, err := startStubs(cfg, rec)
	if err != nil {
		return err
	}
	defer func() {
		for _, s := range stubs {
			s.Close()
		}
	}()

	// Nobody else owns the Proxy's lifecycle (server.Deps documents that the
	// server doesn't); without this, every intercept listener wireProxy
	// binds outlives runUp's return — still bound, and still recording into
	// a Recorder whose hopsFile is about to close. Declared after the stubs
	// defer so it runs first on unwind (LIFO): orchestrator is stopped
	// (explicit orch.Down() below) before we return, then proxy listeners,
	// then stubs, then sessions, then the hops file.
	defer px.Close()

	if err := orch.Up(ctx); err != nil {
		_ = orch.Down()
		return fmt.Errorf("orchestrator up: %w", err)
	}

	// shutdownCtx derives from ctx so either SIGINT/SIGTERM (which cancels
	// ctx) or the API's POST /api/shutdown (which calls cancelShutdown
	// directly) stops Serve the same way.
	shutdownCtx, cancelShutdown := context.WithCancel(ctx)
	defer cancelShutdown()

	allowedHosts, exposureWarning := apiHostPolicy(opts.Addr)
	handler := server.New(server.Deps{
		Cfg: cfg, Orch: orch, Rec: rec, Lat: lat, Sessions: sessions, Version: version,
		Shutdown: cancelShutdown, AllowedHosts: allowedHosts,
	})

	// "starting", not "serving": server.Serve binds inside the goroutine
	// below, so at this point the listener isn't confirmed up yet — the
	// select below is what actually observes bind success (shutdownCtx
	// stays live) vs. failure (serveErrCh fires immediately).
	fmt.Fprintf(stdout, "ensemble: starting API on %s\n", opts.Addr)
	if exposureWarning != "" {
		fmt.Fprintln(stderr, exposureWarning)
	}
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- server.Serve(shutdownCtx, opts.Addr, handler) }()

	// Two ways this stops waiting: a normal shutdown (SIGINT/SIGTERM
	// canceled ctx, or POST /api/shutdown called cancelShutdown), or Serve
	// returning on its own — which only happens on a bind failure, since a
	// clean shutdown's Serve return is instead observed via shutdownCtx.Done
	// racing it. Without this second case, a bind failure (e.g. the address
	// is already in use) left the error sitting unread in serveErrCh
	// forever: services and stubs kept running, and runUp never returned.
	var serveErr error
	select {
	case <-shutdownCtx.Done():
		fmt.Fprintln(stdout, "ensemble: shutting down")
		serveErr = <-serveErrCh
	case serveErr = <-serveErrCh:
		if serveErr != nil {
			fmt.Fprintf(stderr, "ensemble: API server failed to start: %v\n", serveErr)
		}
	}

	downErr := orch.Down()
	if serveErr != nil {
		return serveErr
	}
	return downErr
}

// apiHostPolicy translates a --api bind address into the browser guard's
// extra allowed hosts (see server.Deps.AllowedHosts) and, when the bind
// reaches past this machine, the warning runUp prints about it.
//
// Loopback binds need no extra hosts — the guard always allows loopback
// and "localhost". A wildcard bind (":4700", "0.0.0.0:4700", "[::]:4700")
// can be reached under any hostname that resolves here, none of which we
// can enumerate, so host/origin matching is turned off with "*" and the
// user is told what they just opened. A specific non-loopback address is
// allowed under exactly that name, and warned about too.
func apiHostPolicy(addr string) (hosts []string, warning string) {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")

	if host == "" || host == "0.0.0.0" || host == "::" {
		return []string{"*"}, exposedWarning(addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil, ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil, ""
	}
	return []string{host}, exposedWarning(addr)
}

func exposedWarning(addr string) string {
	return fmt.Sprintf(
		"ensemble: WARNING: the control-plane API is bound to %s, which is reachable from outside this machine.\n"+
			"ensemble: every route except POST /api/shutdown is unauthenticated — anyone who can reach that address\n"+
			"ensemble: can read captured request/response bodies, run seeds, restart services, and inject latency.\n"+
			"ensemble: bind to 127.0.0.1 (the default) unless you mean it.", addr)
}

// startStubs starts every config-defined stub HTTP server, mapping
// config.Stub/config.StubRoute onto core/stub's types (kept as separate
// types deliberately — config is the on-disk YAML shape, stub is the
// runtime shape).
func startStubs(cfg *config.Config, rec *proxy.Recorder) ([]*stub.Stub, error) {
	var stubs []*stub.Stub
	for name, st := range cfg.Stubs {
		routes := make([]stub.Route, len(st.Routes))
		for i, r := range st.Routes {
			// BodyFile is declared relative to Config.Dir, same as
			// SeedSQL.File (orchestrator/seed.go) — not the process CWD,
			// which is what core/stub's os.ReadFile would otherwise use.
			bodyFile := r.Respond.BodyFile
			if bodyFile != "" && !filepath.IsAbs(bodyFile) {
				bodyFile = filepath.Join(cfg.Dir, bodyFile)
			}
			routes[i] = stub.Route{
				Match: stub.Match{Method: r.Match.Method, Path: r.Match.Path},
				Respond: stub.Respond{
					Status: r.Respond.Status, Headers: r.Respond.Headers,
					Body: r.Respond.Body, BodyFile: bodyFile, Template: r.Respond.Template,
				},
			}
		}
		s := stub.New(name, routes, rec)
		listen := "127.0.0.1:0"
		if st.Port != 0 {
			listen = fmt.Sprintf("127.0.0.1:%d", st.Port)
		}
		if _, err := s.Serve(listen); err != nil {
			for _, started := range stubs {
				started.Close()
			}
			return nil, fmt.Errorf("stub %s: %w", name, err)
		}
		stubs = append(stubs, s)
	}
	return stubs, nil
}

// splitCSV parses a comma-separated flag value into a trimmed, non-empty
// slice. "" -> nil, so an unset --profile leaves Orchestrator.Opts.Profiles
// nil (every service without a Profile requirement stays active).
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
