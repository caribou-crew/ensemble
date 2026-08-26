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
	"time"

	"github.com/caribou-crew/ensemble/core/buildinfo"
	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/inspector"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
	"github.com/caribou-crew/ensemble/ensemble/tui"
)

// upOptions configures one `ensemble up` run.
type upOptions struct {
	ConfigPath string
	Profiles   []string
	Variants   map[string]string // per-service variant overrides (--variant svc=name)
	Addr       string            // control-plane API listen address, e.g. ":4700"
	TUI        bool              // enter the terminal UI once the stack is up, instead of blocking silently
	// Positional profile names (`ensemble up lane2`): added to a running
	// stack if one answers at the client URL, else folded into Profiles
	// for a cold start — see cmdUp.
	Positional []string
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
	variant := fs.String("variant", "", "comma-separated service=variant overrides of each service's default variant")
	addr := fs.String("api", defaultAPIAddr, "control-plane API listen address")
	tuiFlag := fs.Bool("tui", false, "enter the terminal UI once the stack is up, instead of blocking silently")
	if err := fs.Parse(args); err != nil {
		return upOptions{}, err
	}
	variants, err := parseVariantFlag(*variant)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: up: %v\n", err)
		return upOptions{}, err
	}
	return upOptions{ConfigPath: *cfgPath, Profiles: splitCSV(*profile), Variants: variants, Addr: *addr, TUI: *tuiFlag, Positional: fs.Args()}, nil
}

// parseVariantFlag parses --variant's "svc=name,svc2=name2" form.
func parseVariantFlag(v string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range splitCSV(v) {
		name, variant, ok := strings.Cut(pair, "=")
		if !ok || name == "" || variant == "" {
			return nil, fmt.Errorf("--variant: %q is not service=variant", pair)
		}
		out[name] = variant
	}
	return out, nil
}

func cmdUp(args []string, stdout, stderr io.Writer) int {
	opts, err := parseUpOptions(args, stderr)
	if err != nil {
		return 2
	}
	c := NewClient(defaultAPIURL())
	running := c.Health(context.Background()) == nil
	switch {
	case len(opts.Positional) > 0 && running:
		// `ensemble up lane2`: attach to a running stack when there is one.
		return switchProfiles(c, opts.Positional, true, false, stdout, stderr)
	case len(opts.Positional) > 0:
		fmt.Fprintf(stderr, "ensemble: no running stack at %s; starting with profiles %s\n", c.BaseURL, strings.Join(opts.Positional, ", "))
		opts.Profiles = append(opts.Profiles, opts.Positional...)
	case running:
		// Plain `ensemble up` with no positional profiles, against an
		// already-running stack: reconcile the freshly loaded config
		// against whatever was last applied, touching only what changed
		// (a changed gateway port rebinds just that gateway, a changed
		// service restarts just that service, and so on — see
		// orchestrator.Reconcile) — instead of requiring a full
		// `ensemble down` + `ensemble up` to pick up an edit.
		return reconcileRunning(c, opts.ConfigPath, stdout, stderr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runUp(ctx, opts, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "ensemble: up: %v\n", err)
		return 1
	}
	return 0
}

// reconcileRunning loads configPath and posts it to a running stack's
// POST /api/reconcile, printing what it did (or didn't — "unchanged" is a
// reported outcome too) per unit. See cmdUp's `case running:` for when this
// runs instead of a cold start.
func reconcileRunning(c *Client, configPath string, stdout, stderr io.Writer) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: up: %v\n", err)
		return 1
	}
	result, err := c.Reconcile(context.Background(), *cfg)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: reconcile: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ensemble: already running at %s; reconciled config\n", c.BaseURL)
	for _, a := range result.Actions {
		fmt.Fprintf(stdout, "ensemble:   %s %q: %s\n", a.Kind, a.Name, a.Action)
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

	if err := runPreflightChecks(cfg); err != nil {
		return err
	}

	if err := checkPortsFree(cfg, opts.Profiles); err != nil {
		return err
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

	logf := func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) }

	px := proxy.New(rec)
	px.TraceHeader = cfg.TraceHeader
	px.SourceHeaders = cfg.SourceHeaders
	lat := proxy.NewLatencyStore(nil)
	px.Latency = lat

	// A prior run's persisted rules (arms set, Datadog pulls applied, etc.)
	// supersede ensemble.yaml's latency.defaults entirely — the persisted
	// file, once it exists, is the full state, not a delta layered on top
	// of defaults (defaults only ever seed a never-before-persisted store;
	// see latency_persist.go).
	latencyPath := latencyRulesPath(cfg.Dir)
	persisted, err := loadLatencyRules(latencyPath)
	if err != nil {
		return fmt.Errorf("load persisted latency rules: %w", err)
	}
	if persisted != nil {
		for _, r := range persisted {
			lat.Set(r)
		}
	} else {
		for _, d := range cfg.Latency.Defaults {
			lat.Set(proxy.LatencyRule{
				Target: d.Target, Path: d.Path,
				FixedMs: d.FixedMs, P50: d.P50, P95: d.P95, P99: d.P99,
				Enabled: d.Enabled,
			})
		}
	}
	lat.OnChange(func(rules []proxy.LatencyRule) {
		if err := persistLatencyRules(latencyPath, rules); err != nil {
			logf("ensemble: persist latency rules: %v", err)
		}
	})

	var entries []string
	for name, svc := range cfg.Services {
		if svc.Entry {
			entries = append(entries, name)
		}
	}
	// A gateway is always an entry: clients call it directly, so unstamped
	// traffic arriving there is ambient, not a propagation gap.
	for name := range cfg.Gateways {
		entries = append(entries, name)
	}
	sessions := proxy.NewSessionManager(px, rec, entries)
	defer sessions.Close()

	// Check --variant against the config up front so a typo fails here,
	// not as a confusing start failure deep in Up.
	for name, variant := range opts.Variants {
		if _, err := cfg.ResolveService(name, variant); err != nil {
			return fmt.Errorf("--variant %s=%s: %w", name, variant, err)
		}
	}
	orch := orchestrator.New(cfg, px, orchestrator.Opts{
		Profiles: opts.Profiles,
		Variants: opts.Variants,
		Logf:     logf,
	})
	orch.SQLRunner = inspector.NewSQLRunner(cfg.Databases)
	orch.Rec = rec
	insp := buildInspector(cfg, logf)
	orch.DBReady = dbReadyProbe(insp)

	// Nobody else owns the Proxy's lifecycle (server.Deps documents that the
	// server doesn't); without this, every intercept listener wireProxy
	// binds outlives runUp's return — still bound, and still recording into
	// a Recorder whose hopsFile is about to close. Declared so it runs
	// first on unwind (LIFO): orchestrator is stopped (explicit orch.Down()
	// below, which also closes every stub it started) before we return,
	// then proxy listeners, then sessions, then the hops file.
	defer px.Close()

	// shutdownCtx derives from ctx so either SIGINT/SIGTERM (which cancels
	// ctx) or the API's POST /api/shutdown (which calls cancelShutdown
	// directly) stops Serve the same way.
	shutdownCtx, cancelShutdown := context.WithCancel(ctx)
	defer cancelShutdown()

	allowedHosts, exposureWarning := apiHostPolicy(opts.Addr)
	handler := server.New(server.Deps{
		Cfg: cfg, Orch: orch, Rec: rec, Lat: lat, Sessions: sessions, Version: buildinfo.Resolve(version),
		Shutdown: cancelShutdown, AllowedHosts: allowedHosts, Insp: insp,
	})

	// The API server binds before orch.Up runs its (possibly slow: docker
	// pulls, DB health gates, builds) dependency walk, not after — so
	// `ensemble status` and `ensemble dashboard` can connect and watch
	// services come up live instead of getting connection refused for the
	// whole startup window. "starting", not "serving": server.Serve binds
	// inside the goroutine below, so at this point the listener isn't
	// confirmed up yet — the select below is what actually observes bind
	// success (shutdownCtx stays live) vs. failure (serveErrCh fires
	// immediately).
	fmt.Fprintf(stdout, "ensemble: starting API on %s\n", opts.Addr)
	if exposureWarning != "" {
		fmt.Fprintln(stderr, exposureWarning)
	}
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- server.Serve(shutdownCtx, opts.Addr, handler) }()

	// orch.Up runs concurrently with the API server rather than blocking
	// ahead of it (see above). A non-nil return no longer means Up
	// aborted early — orch.Up itself now keeps going through a per-node
	// failure (see its doc comment) — so it's handled below as a warning,
	// not a teardown: whatever came up stays up, and the operator can use
	// the now-already-reachable status/dashboard/restart to fix the
	// failed node in parallel instead of losing the whole stack to one
	// bad service.
	upErrCh := make(chan error, 1)
	go func() { upErrCh <- orch.Up(ctx) }()

	var serveErr error
	select {
	case upErr := <-upErrCh:
		if upErr != nil {
			logStartupWarning(stderr, upErr)
		}
		// Fall through to the steady-state wait below, same as a clean Up.
	case <-shutdownCtx.Done():
		// SIGINT/SIGTERM (or POST /api/shutdown, though nothing can reach
		// it yet since Up hasn't returned) arrived while services/
		// databases were still coming up. orch.Up shares ctx, so it should
		// already be unwinding — wait for it before Down touches shared
		// orchestrator state (Up and Down must never run concurrently).
		fmt.Fprintln(stdout, "ensemble: shutting down")
		<-upErrCh
		serveErr = <-serveErrCh
		downErr := orch.Down()
		if serveErr != nil {
			return serveErr
		}
		return downErr
	case serveErr = <-serveErrCh:
		// The control-plane API failed to bind (e.g. a second `ensemble
		// up`). Let Up finish on its own — its fate is independent of the
		// bind — before writing to stderr again: Up's own progress logging
		// (logf) runs in its own goroutine until it returns, and stderr
		// isn't safe for concurrent writers.
		<-upErrCh
		fmt.Fprintf(stderr, "ensemble: API server failed to start: %v\n", serveErr)
		_ = orch.Down()
		return serveErr
	}

	// --tui: hand off to the terminal UI in place of blocking silently.
	// It runs against shutdownCtx, so an external shutdown (SIGINT/SIGTERM,
	// POST /api/shutdown) makes its tea.Program exit on its own (see
	// tui.Run's tea.WithContext); either that or the user quitting the TUI
	// itself (q/ctrl+c) reaches this same cancelShutdown() call, which is
	// exactly the trigger the select below already knows how to wait on —
	// so the rest of the shutdown path (stopping orch, closing stubs) runs
	// unchanged whether the stack was watched via the TUI or not.
	if opts.TUI {
		go func() {
			if tuiErr := tui.Run(shutdownCtx, tuiAPIURL(opts.Addr)); tuiErr != nil {
				fmt.Fprintf(stderr, "ensemble: tui: %v\n", tuiErr)
			}
			cancelShutdown()
		}()
	}

	// Two ways this stops waiting: a normal shutdown (SIGINT/SIGTERM
	// canceled ctx, or POST /api/shutdown called cancelShutdown, or the TUI
	// above exiting), or Serve returning on its own — which only happens on
	// a bind failure, since a clean shutdown's Serve return is instead
	// observed via shutdownCtx.Done racing it. Without this second case, a
	// bind failure (e.g. the address is already in use) left the error
	// sitting unread in serveErrCh forever: services and stubs kept
	// running, and runUp never returned.
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

// logStartupWarning reports a partial orch.Up failure (one or more
// service/database nodes failed or were skipped) without treating it as
// fatal — runUp keeps the stack up around it. upErr is an errors.Join of
// one error per affected node (see Orchestrator.Up), printed one per line.
func logStartupWarning(stderr io.Writer, upErr error) {
	fmt.Fprintln(stderr, "ensemble: WARNING: one or more services/databases failed to start — the stack is staying up with whatever did:")
	for line := range strings.SplitSeq(upErr.Error(), "\n") {
		fmt.Fprintf(stderr, "ensemble:   %s\n", line)
	}
	fmt.Fprintln(stderr, "ensemble: use `ensemble status` or the dashboard to see what failed, and the dashboard's restart action to retry once fixed.")
}

// buildInspector constructs an inspector.Inspector and registers a Driver
// for every cfg.Databases entry whose type ensemble knows how to inspect
// (postgres, mysql, dynamodb — reusing the same DSN-building conventions
// as inspector.NewSQLRunner's seed-SQL path so a database's connection
// details are computed exactly one way — plus http, for a service that
// exposes its own state over the three-route contract NewHTTPDriver
// speaks). redis/localstack are provisioned but have no inspector.Driver
// yet, so they're silently left unregistered — GET /api/databases only
// ever lists cfg.Databases ∩ registered drivers, so an unregistered
// database just doesn't show up there rather than erroring.
//
// All four constructors connect lazily (database/sql pools and dials on
// first query; NewDynamoDriver and NewHTTPDriver are plain http.Client
// wrappers) — this never blocks on or requires a live database, so it's
// safe to call unconditionally during startup even before the database's
// container is healthy.
func buildInspector(cfg *config.Config, logf func(string, ...any)) *inspector.Inspector {
	insp := inspector.New()
	for name, db := range cfg.Databases {
		switch db.Type {
		case "postgres":
			drv, err := inspector.NewPostgresDriver(inspector.PostgresDSN(db))
			if err != nil {
				logf("ensemble: inspector: %s: %v", name, err)
				continue
			}
			insp.Register(name, drv)
		case "mysql":
			drv, err := inspector.NewMySQLDriver(inspector.MySQLDSN(db))
			if err != nil {
				logf("ensemble: inspector: %s: %v", name, err)
				continue
			}
			insp.Register(name, drv)
		case "dynamodb":
			insp.Register(name, inspector.NewDynamoDriver(fmt.Sprintf("http://127.0.0.1:%d", db.Port)))
		case "http":
			insp.Register(name, inspector.NewHTTPDriver(db.URL, db.Headers))
		}
	}
	return insp
}

// dbReadyProbe builds an orchestrator.DBReadyFunc backed by insp: for a
// database with a registered inspector.Driver (postgres, mysql, dynamodb,
// http — see buildInspector), readiness is a real protocol-level check — the same
// Schema/Tables call the dashboard uses to inspect the database, so
// startDatabase's health gate and the dashboard's reads exercise identical
// connection logic (task 3.6, defect D3: a bare TCP dial can't tell a live
// server from docker's published-port proxy accepting a connection into
// nothing).
//
// A database whose type has no registered driver (redis, localstack —
// buildInspector never registers those) falls back to a single TCP dial
// attempt; startDatabase's caller (gateDatabaseHealth) retries this every
// poll interval until it succeeds or the health timeout elapses, so this
// preserves the pre-3.6 TCP-gate behavior for exactly the types that have
// no better check available yet.
func dbReadyProbe(insp *inspector.Inspector) orchestrator.DBReadyFunc {
	return func(ctx context.Context, name string, db config.Database) error {
		if insp.Has(name) {
			_, err := insp.Schema(ctx, name)
			return err
		}
		if db.Port <= 0 {
			return nil
		}
		conn, err := (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", db.Port))
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

// tuiAPIURL builds the URL `up --tui` connects its terminal UI to for a
// control plane bound at addr (opts.Addr — typically loopback per
// defaultAPIAddr, but --api can override it, including to a wildcard bind
// like ":4700"). The TUI always talks to it over loopback: it runs in the
// same process/host as the stack it's watching, so there's never a reason
// to route through whatever non-loopback host a wildcard or explicit bind
// also happens to answer on.
func tuiAPIURL(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "4700"
	}
	return "http://127.0.0.1:" + port
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
