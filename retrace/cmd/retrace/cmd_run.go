package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/capture"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// ensembleHealthTimeout bounds the attach probe. It is short on purpose:
// the common case for a missing control plane is a refused connection
// (instant), and the point of the probe is to decide a mode, not to wait
// for a slow one.
const ensembleHealthTimeout = 3 * time.Second

// envOr reads key, falling back to def when it is unset OR empty. An empty
// environment variable is "not configured", not "configure the URL to the
// empty string" — the latter would produce requests to a bare path.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitDoubleDash cuts args at the first bare "--": everything before it is
// flags, everything after is the test command. The separator itself belongs
// to neither. A missing "--" means no test command was given.
func splitDoubleDash(args []string) (flagArgs, testCmd []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// cmdRun records one flow: it points the test command at a recording edge
// — retrace's own proxy standalone, ensemble's session edge when attached —
// and writes a run directory.
func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		flow     = fs.String("flow", "", "flow name to record (required)")
		app      = fs.String("app", "", "app name (default: config app, else the directory name)")
		upstream = fs.String("upstream", "", "standalone: base URL clients would call")
		asJSON   = fs.Bool("json", false, "emit the manifest as JSON on stdout")
		noConfig = fs.Bool("no-config", false, "capture without a retrace.yaml — user redaction keys will be absent")
		// Declared here rather than in Task 4 because every line that reads
		// them is below: a flag result bound to a local and never read is
		// `declared and not used`, a compile error.
		ensembleURL = fs.String("ensemble", envOr("ENSEMBLE_API", "http://127.0.0.1:4700"), "ensemble control-plane URL")
		noEnsemble  = fs.Bool("no-ensemble", false, "force standalone capture even if ensemble is up")
	)
	flagArgs, testCmd := splitDoubleDash(args)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*flow) == "" {
		return fail(stderr, "run: --flow is required — name the flow to record")
	}
	if len(testCmd) == 0 {
		return fail(stderr, "run: a test command is required after `--`")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "run: cannot determine the working directory: %v", err)
	}
	cfg, err := config.Discover(cwd)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	// config.Discover deliberately does not walk up the directory tree —
	// its inability to reach a parent directory or the user's home is a
	// security property, not a limitation. The cost is that running from a
	// subdirectory of a monorepo finds no config at all, and a defaulted
	// Config carries an EMPTY user redaction list, so capture would write
	// hops to disk without the user's own keys scrubbed. Absent config and
	// permissive config are different meanings, and this is the one place
	// in retrace where confusing them leaks secrets to a file rather than
	// mis-gating a diff. So: refuse, name the path, name the override.
	//
	// Keyed on cfg.Loaded, NEVER on len(cfg.Redact): an empty Redact list
	// is correct and deliberate — baseline redaction lives in core/trace's
	// redactor and config.Redact supplies user ADDITIONS on top of it.
	if !cfg.Loaded && !*noConfig {
		fmt.Fprintf(stderr, "retrace: refusing to capture — no retrace.yaml at %s\n", filepath.Join(cwd, "retrace.yaml"))
		fmt.Fprintf(stderr, "  retrace does not search parent directories on purpose, so run from the directory\n")
		fmt.Fprintf(stderr, "  that holds retrace.yaml, or pass --no-config to proceed WITHOUT your own redaction\n")
		fmt.Fprintf(stderr, "  keys — every captured request and response body will be written to disk with only\n")
		fmt.Fprintf(stderr, "  the built-in header/query redaction applied; secrets in body fields (passwords,\n")
		fmt.Fprintf(stderr, "  tokens) are written unredacted.\n")
		return exitGate
	}

	appName := *app
	if appName == "" {
		appName = cfg.App
	}
	if appName == "" {
		appName = filepath.Base(cwd)
	}
	up := *upstream
	if up == "" {
		up = cfg.Upstream
	}

	opts := capture.Options{
		Cwd:      cwd,
		App:      appName,
		Flow:     *flow,
		Upstream: up,
		Redact:   cfg.Redact,
		Now:      time.Now,
	}

	// Attach when ensemble answers AND the config names an entry service.
	// Anything else falls back to standalone with an explicit stderr note —
	// silently recording less than the user asked for is how a "the app
	// made no calls" report gets believed.
	//
	// sess stays nil until an attach actually succeeds, and Mode comes from
	// whichever constructor ran. A local `mode` variable set optimistically
	// beside the StartAttached call would record `ensemble` on a run whose
	// attach failed — a manifest claiming a full-chain recording that was
	// never made, which is exactly the "unreachable and fine compare equal"
	// trap the zero-value constraint is about.
	var sess *capture.Session
	switch {
	case *noEnsemble:
		// Explicit escape hatch — the user asked for standalone, so no note.
	case cfg.Entry == "":
		// retrace.yaml needing `entry:` is exactly the thing a new user
		// forgets, and this is otherwise the one attach-vs-standalone
		// decision that produced no signal at all (the manifest itself is
		// still honest: mode: standalone). No health probe here — deciding
		// to skip the attempt costs nothing and needs no network call.
		fmt.Fprintf(stderr, "retrace: no `entry:` configured in retrace.yaml — recording the client edge only\n")
	default:
		c := NewClient(*ensembleURL)
		hctx, cancel := context.WithTimeout(context.Background(), ensembleHealthTimeout)
		healthErr := c.Health(hctx)
		cancel()
		switch {
		case healthErr != nil:
			fmt.Fprintf(stderr, "retrace: ensemble at %s is not answering (%v) — recording the client edge only\n", *ensembleURL, healthErr)
		default:
			attached, attachErr := capture.StartAttached(opts, c, cfg.Entry)
			if attachErr != nil {
				// Health passed but the session did not start (404 unknown
				// entry, 409 active id, 400 no proxy port). A live control
				// plane is not proof of an attached capture.
				fmt.Fprintf(stderr, "retrace: ensemble at %s refused the session (%v) — recording the client edge only\n", *ensembleURL, attachErr)
			} else {
				sess = attached
			}
		}
	}
	if sess == nil {
		standalone, serr := capture.StartStandalone(opts)
		if serr != nil {
			return fail(stderr, "run: %v", serr)
		}
		sess = standalone
	}
	// Idempotent: runFlow closes the session as soon as the test command
	// exits, before reading anything back off disk.
	defer sess.Close()

	// When --json is set, stdout must carry the manifest ALONE — that is
	// the documented CI contract (`retrace run --json | jq`). The test
	// command's own stdout goes to stderr instead, so a test runner's own
	// log lines never land ahead of (or inside) the JSON document.
	testStdout := stdout
	if *asJSON {
		testStdout = stderr
	}
	m, err := runFlow(sess, runOptions{
		Cwd:     cwd,
		App:     appName,
		Flow:    *flow,
		TestCmd: testCmd,
		Stdout:  testStdout,
		Stderr:  stderr,
		Now:     time.Now,
	})
	if err != nil {
		return fail(stderr, "run: %v", err)
	}

	// Always, regardless of --json: stdout carries only the manifest under
	// --json (the documented CI contract), but stderr is free, and a
	// non-clean capture is exactly the fact a CI log must not bury —
	// Tasks 10, 13 and 16 banner the same verdict on their own surfaces.
	trust := m.Capture
	if trust.Status != trace.VerdictOK {
		fmt.Fprintf(stderr, "\n  ⚠ capture-trust: %s — %s\n", trust.Status, trust.Summary)
		if trust.Hint != "" {
			fmt.Fprintf(stderr, "    %s\n", trust.Hint)
		}
	}

	if *asJSON {
		if err := writeJSON(stdout, m); err != nil {
			return fail(stderr, "run: %v", err)
		}
	} else {
		fmt.Fprintf(stdout, "retrace: recorded %s/%s as %s\n", m.App, m.Flow, m.RunID)
		fmt.Fprintf(stdout, "  %s\n", sess.Paths.RunDir)
		fmt.Fprintf(stdout, "  wire %d calls · %d checkpoints · %d flow parts · capture %s\n",
			m.Wire.Calls, len(m.Checkpoints), len(m.Groups), m.Capture.Status)
	}
	// The test command's own exit code wins: a failing test must fail the
	// pipeline. retrace's 0/1/2/3 contract only applies when the command
	// itself succeeded.
	//
	// A signal-killed child is the one case that does not fit that rule:
	// exec.ExitError.ExitCode() reports -1 for a process terminated by a
	// signal (never for "still running" here — cmd.Run already returned),
	// and passing -1 straight to os.Exit gets silently truncated to 255 by
	// the OS, outside the run/diff 0/1/2/3 contract entirely. A CI timeout
	// or a Ctrl-C mid-test is then indistinguishable from garbage rather
	// than a defined "could not evaluate" status. Task 10 owns the whole
	// exit contract, including the codes this command does not itself
	// produce — see diff/summary.go's ExitCode doc — so it maps this one
	// case explicitly: 3, alongside config/IO failures, since a run that
	// never completed found nothing to report, changed or otherwise.
	if m.Test.ExitCode < 0 {
		return exitUsage
	}
	return m.Test.ExitCode
}

// runOptions is what cmdRun has already resolved from flags and config by
// the time it gets here.
type runOptions struct {
	Cwd       string
	App, Flow string
	TestCmd   []string // everything after "--"
	Stdout    io.Writer
	Stderr    io.Writer
	Now       func() time.Time
}

// runFlow executes the test command against an already-started session and
// returns the assembled manifest. Every Manifest field is set here; if a
// field has no assignment in this function it has no writer at all.
func runFlow(s *capture.Session, o runOptions) (runs.Manifest, error) {
	ctx, cancel := context.WithCancel(context.Background())
	// watchDone closes when WatchProxy returns. runFlow joins it right after
	// cancel() and strictly before s.Close(): WatchProxy's ctx.Done() branch
	// re-probes the client-edge listener on the way out, and if that probe
	// races Close() tearing the listener down, the dial failure is recorded
	// as a fabricated ProxyFailure on a perfectly healthy run. Joining first
	// guarantees the watcher has finished observing before anything closes
	// what it's observing.
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		s.WatchProxy(ctx) // the only producer of a ProxyFailure
	}()

	started := o.Now()
	cmd := exec.CommandContext(ctx, o.TestCmd[0], o.TestCmd[1:]...)
	cmd.Env = append(os.Environ(), s.Env()...)
	cmd.Stdout, cmd.Stderr, cmd.Dir = o.Stdout, o.Stderr, o.Cwd
	runErr := cmd.Run()
	cancel()
	<-watchDone
	elapsed := o.Now().Sub(started)

	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			// The test command never started (bad path, not executable,
			// etc.) — no manifest will ever be written for this run, so the
			// directory StartStandalone already created is removed rather
			// than left behind as an orphan. runs.ListRuns lists any
			// directory regardless of whether it holds a manifest, and a
			// "latest" selector would otherwise resolve to a run that never
			// happened (Task 8's diff). Best-effort: the command failure is
			// the one that matters to the caller.
			_ = os.RemoveAll(s.Paths.RunDir)
			return runs.Manifest{}, fmt.Errorf("could not run the test command: %w", runErr)
		}
	}

	// Drain BEFORE Close, and Close before reading anything off disk.
	//
	// The order is the whole point of the attached path: hops are recorded
	// at completion and ensemble drops a hop whose session has already
	// ended, so a downstream call still in flight when the test command
	// exited would otherwise be lost. Waiting for the count to settle first
	// NARROWS that loss window; it does not close it — a hop routed after
	// EndSession itself is still dropped, silently and uncounted, however
	// long Drain waited (see capture.Session.Drain's doc). Drain is a no-op
	// standalone. A drain error is noted (both to stderr and to the durable
	// trust record below), never a lost recording — whatever did arrive is
	// still written by Close.
	if err := s.Drain(context.Background()); err != nil {
		fmt.Fprintf(o.Stderr, "retrace: draining ensemble hops failed (%v) — the recording may be truncated\n", err)
		s.NoteDrainFailure(err)
	}
	// Close flushes wire.jsonl and, in attached mode, writes hops.jsonl and
	// wire.jsonl and then ends the ensemble session.
	if err := s.Close(); err != nil {
		return runs.Manifest{}, err
	}

	checkpoints, err := s.Checkpoints()
	if err != nil && !os.IsNotExist(err) {
		return runs.Manifest{}, err
	}

	// Flow-part groups: markers were appended to groups.jsonl by the marker
	// door and by file-writing adapters. THIS is where they stop being a
	// log and become part of the run. Without these three lines the wire
	// diff has no sections and nothing anywhere reports that.
	records, err := runs.ReadGroupRecords(s.Paths)
	if err != nil {
		return runs.Manifest{}, err
	}
	groups := runs.DeriveGroups(records, o.Now())

	// Read back what actually reached disk rather than what the ring
	// happened to hold — Close() has already flushed.
	wireHops, _, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil {
		return runs.Manifest{}, err
	}

	hops := s.Hops()
	trust := assessTrust(s, hops, checkpoints, groups, exitCode)

	m := runs.Manifest{
		Schema:      runs.Schema,
		App:         o.App,
		Flow:        o.Flow,
		RunID:       s.RunID,
		Mode:        s.Mode,
		Git:         capture.GitInfo(o.Cwd),
		StartedAt:   started,
		FinishedAt:  o.Now(),
		Checkpoints: checkpoints,
		Groups:      groups,
		Capture:     trust,
		Wire:        runs.Counts{Calls: len(wireHops), Recorded: true},
		Test:        runs.Test{Command: strings.Join(o.TestCmd, " "), ExitCode: exitCode, DurationMs: float64(elapsed.Milliseconds())},
		// Retrace is the recording binary's own version, from main.version
		// (the `var version = "dev"` in main.go, stamped by -ldflags at
		// release). It is replay-compatibility provenance: "which retrace
		// wrote this bundle" is the first question asked when a reference
		// recorded months ago stops replaying, and it cannot be
		// reconstructed after the fact.
		Env: runs.Env{
			Go:       runtime.Version(),
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Retrace:  version,
		},
	}
	// Hops is a *Counts on purpose: nil means "standalone, no chain was
	// recorded", present-and-zero means "the chain was recorded and was
	// empty". Task 5 sets it; standalone leaves it nil.
	if s.Mode == runs.ModeEnsemble {
		m.Hops = &runs.Counts{Calls: len(hops), Recorded: true}
	}
	return m, runs.WriteManifest(s.Paths, &m)
}

// assessTrust is the seam Task 4 left and Task 6 fills: it turns everything
// runFlow already gathered into the single capture-trust verdict every
// report surface (this banner, and Tasks 10/11/13/16) reads.
func assessTrust(s *capture.Session, hops []trace.Hop, cps []runs.Checkpoint,
	groups []runs.Group, exitCode int) runs.CaptureTrust {
	return capture.Assess(capture.AssessInput{
		ProxyFailure:        s.ProxyFailure(),
		Hops:                hops,
		Checkpoints:         len(cps),
		ExpectedCheckpoints: expectedCheckpoints(s.Paths),
		RequestsSeen:        requestsSeenForTrust(s),
		TestExitCode:        exitCode,
		// Quiet intervals come from the SAME derived groups the manifest
		// stores, so "the report says this stretch was deliberately quiet"
		// and "the verdict forgave this stretch" can never disagree.
		Quiet: quietOnly(groups),
		// The same constant Assess falls back to, passed explicitly so the
		// number has a visible name at the call site. There is one
		// declaration of it, in capture/trust.go.
		GapThreshold:   capture.DefaultGapThreshold,
		SessionVerdict: s.EndVerdict(),
		SessionReasons: s.EndReasons(),
		Notes:          s.TrustNotes(),
	})
}

// requestsSeenForTrust is the mode branch Finding 1 requires: it is the
// ONLY production call site of capture.AssessInput.RequestsSeen, so the
// -1-vs-0 distinction has to be made HERE, not left to whatever
// Session.RequestsSeen() happens to return.
//
// Session.RequestsSeen() (capture.go) counts marker-door hits plus, when
// s.rec is non-nil, recorder snapshots. In ensemble-attached mode s.rec is
// always nil — ensemble owns the client-edge listener, retrace's own proxy
// is never in the request path — so RequestsSeen() there is marker-door
// hits ONLY. A healthy attached flow that posts no markers (most flows
// don't; markers are an opt-in checkpoint mechanism) returns 0 from that
// method, indistinguishable from "the app never routed through retrace at
// all". Reading that 0 as AssessInput.RequestsSeen would verdict a
// perfectly good attached run `broken`/proxy-never-reached and get it
// quarantined by Task 10 — the false accusation this function exists to
// prevent.
//
// -1 unconditionally for attached mode, marker hits or not: a marker hit
// proves the marker door was reached, not that the edge was, and Assess's
// zero-calls branch needs "can this mode verify reachability at all", not
// "did any signal arrive". Standalone owns the client-edge listener
// outright, so its raw count IS the reachability signal Assess wants.
//
// This lives in cmd_run.go rather than inside RequestsSeen() itself
// because RequestsSeen()'s existing contract — a raw count, mode-agnostic —
// is depended on directly by
// TestRequestsSeenAndHopsTolerateASessionWithNoLocalRecorder (asserts 3 for
// an attached session with 3 marker hits) and by RequestsSeen()'s own doc
// comment. Folding the -1 override into that method would conflate two
// different questions — "how many things reached retrace" and "can this
// mode use that count as a reachability signal" — inside one method, and
// break a test that is protecting a real, separate contract. The mode
// (runs.ModeEnsemble vs runs.ModeStandalone) is already available here at
// the only call site, so the branch belongs here.
func requestsSeenForTrust(s *capture.Session) int {
	if s.Mode == runs.ModeEnsemble {
		return -1
	}
	return s.RequestsSeen()
}

// expectedCheckpoints reads the previous run of the same app/flow — the run
// directory immediately before this one in ListRuns' lexical (and, thanks
// to NewRunID's timestamp-first encoding, chronological) order — and
// returns the number of checkpoints its manifest recorded. It returns -1
// when there is no history to compare against: a wire-only flow's first
// ever run has no prior checkpoint count to fall short of.
//
// Report accuracy note: Assess gates its no-screenshots reason on
// `ExpectedCheckpoints > 0`, so -1 and 0 are behaviorally IDENTICAL there
// today — the sentinel is documentation, not load-bearing. It exists to
// match the -1-means-unknown convention this file uses elsewhere (see
// AssessInput.RequestsSeen) and so a future comparison against "the last
// run genuinely took zero checkpoints" can be told apart from "no history
// at all" without adding another field, not because Assess currently
// distinguishes them.
func expectedCheckpoints(p runs.Paths) int {
	runDir := filepath.Clean(p.RunDir)
	flowDir := filepath.Dir(runDir)
	appDir := filepath.Dir(flowDir)
	root := filepath.Dir(appDir)
	app, flow, runID := filepath.Base(appDir), filepath.Base(flowDir), filepath.Base(runDir)

	ids := runs.ListRuns(root, app, flow)
	prev := ""
	for _, id := range ids {
		if id == runID {
			break
		}
		prev = id
	}
	if prev == "" {
		return -1
	}
	pp, err := runs.PathsFor(root, app, flow, prev)
	if err != nil {
		return -1
	}
	m, err := runs.ReadManifest(pp.ManifestPath)
	if err != nil {
		return -1
	}
	return len(m.Checkpoints)
}

// quietOnly filters DeriveGroups' output down to the flow-declared quiet
// intervals FindGaps subtracts. Groups is the full flow-part timeline
// (login, checkout, ...); only the ones a flow marked `quiet` explain an
// otherwise-suspicious silence.
func quietOnly(groups []runs.Group) []runs.Group {
	var out []runs.Group
	for _, g := range groups {
		if g.Quiet {
			out = append(out, g)
		}
	}
	return out
}
