package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/retrace/capture"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/replay"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// replayShutdownTimeout bounds the graceful close of the two listeners
// after the test command exits. Short: the command is already gone, so
// anything still in flight is a leaked connection, not work.
const replayShutdownTimeout = 2 * time.Second

// replaysRoot is where `retrace replay` puts its run directories —
// deliberately NOT runs.RunsRoot. A replay is not a recording: it writes
// misses.jsonl, whatever screenshots the adapter takes, and no manifest at
// all. Dropped into the runs root, that manifest-less directory would be
// the newest entry there, so `retrace diff --b latest` would resolve to it
// and fail on an unreadable manifest, and refs.Resolve would burn a
// candidate slot on it every time. A separate root keeps "what was
// recorded" and "what was replayed" from ever being confused for each
// other, while still giving adapters a real run directory to write into.
func replaysRoot(cwd string) string { return filepath.Join(cwd, ".retrace", "replays") }

// replayReport is the --json document. Every field carries an explicit
// camelCase tag (global-constraints.md): this is a CI contract.
type replayReport struct {
	App       string    `json:"app"`
	Flow      string    `json:"flow"`
	Ref       replayRef `json:"ref"`
	RunDir    string    `json:"runDir"`
	Exchanges int       `json:"exchanges"`
	// Served is how many calls were answered FROM THE BUNDLE, and Unused
	// names the recorded exchanges nothing ever asked for. Without them a
	// consumer of this document cannot tell "every call matched" from "the
	// client never called anything": missCount is 0 in both worlds. Served
	// == 0 is could-not-evaluate, exit 3.
	Served    int           `json:"served"`
	Unused    []replay.Key  `json:"unused"`
	MissCount int           `json:"missCount"`
	Misses    []replay.Miss `json:"misses"`
	Test      replayTest    `json:"test"`
}

type replayRef struct {
	Kind  string `json:"kind"`
	Dir   string `json:"dir"`
	RunID string `json:"runId"`
}

type replayTest struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exitCode"`
}

// cmdReplay answers a test command's HTTP calls from a reference bundle
// instead of a live stack. A call the bundle does not contain is a 501 and
// a miss, and any miss at all fails the command with exit 2 — see
// replay/server.go's doc comment: absence is never agreement.
func cmdReplay(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		ref    = fs.String("ref", "", "flow whose reference bundle answers the calls (required)")
		app    = fs.String("app", "", "app name (default: config app, else the directory name)")
		listen = fs.String("listen", "127.0.0.1:0", "address the replay server binds (loopback only)")
		asJSON = fs.Bool("json", false, "emit the replay report as JSON on stdout")
	)
	flagArgs, testCmd := splitDoubleDash(args)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}
	flow := strings.TrimSpace(*ref)
	if flow == "" {
		return fail(stderr, "replay: --ref is required — name the flow whose bundle should answer")
	}
	if len(testCmd) == 0 {
		return fail(stderr, "replay: a test command is required after `--`")
	}
	// Before anything is bound. A flag that binds and THEN refuses every
	// request (httpguard 403s a non-loopback Host) hands the operator a
	// running server, a live port and a stream of DNS-rebinding errors that
	// have nothing to do with what they did — the failure arriving later,
	// further from the cause, wearing someone else's message (R-I).
	if err := requireLoopback(*listen); err != nil {
		return fail(stderr, "replay: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "replay: cannot determine the working directory: %v", err)
	}
	cfg, err := config.Discover(cwd)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	appName := *app
	if appName == "" {
		appName = cfg.App
	}
	if appName == "" {
		appName = filepath.Base(cwd)
	}

	// Kind "none" means "I could not compare", never "nothing deviated" —
	// the same rule Task 11 set and cmd_diff follows. Exit 3, with the
	// candidate history, and name the verb that fixes it.
	r := refs.Resolve(cwd, runs.RunsRoot(cwd), appName, flow)
	if r.Kind == "none" {
		fmt.Fprintf(stderr, "retrace: replay: no reference bundle for %s/%s: %s\n", appName, flow, r.Reason)
		printCandidates(stderr, r)
		fmt.Fprintf(stderr, "  run `retrace ref accept --flow %s` once this flow has a good run\n", flow)
		return exitUsage
	}

	bundle, err := replay.LoadBundle(r.Dir, cfg.Dir)
	if err != nil {
		return fail(stderr, "replay: %v", err)
	}
	opts, err := replayOptions(cfg)
	if err != nil {
		return fail(stderr, "replay: %v", err)
	}

	// The replay run directory: where misses.jsonl, the adapter's
	// screenshots and any flow markers land.
	p, err := createReplayRun(cwd, appName, flow)
	if err != nil {
		return fail(stderr, "replay: %v", err)
	}

	// runs.Paths.MissesPath, at the one call site that knows the run
	// directory. There is exactly one name for that file.
	srv := replay.NewServer(bundle, opts, p.MissesPath)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fail(stderr, "replay: cannot listen on %s: %v", *listen, err)
	}
	httpSrv := &http.Server{Handler: srv}
	go httpSrv.Serve(ln)

	markerLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		httpSrv.Close()
		return fail(stderr, "replay: cannot open the marker door: %v", err)
	}
	markerSrv := &http.Server{Handler: capture.NewMarkerDoor(p, time.Now)}
	go markerSrv.Serve(markerLn)

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), replayShutdownTimeout)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
		_ = markerSrv.Shutdown(ctx)
	}()

	// The same three variables `retrace run` exports, so an adapter cannot
	// tell a replay from a recording and needs no second code path.
	env := []string{
		"RETRACE_RUN_DIR=" + p.RunDir,
		"RETRACE_PROXY_URL=http://" + ln.Addr().String(),
		"RETRACE_MARKER_URL=http://" + markerLn.Addr().String(),
	}

	// Under --json stdout carries the report ALONE, so the test runner's
	// own chatter goes to stderr — the same contract `retrace run` keeps.
	testStdout := stdout
	if *asJSON {
		testStdout = stderr
	}
	cmd := exec.Command(testCmd[0], testCmd[1:]...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr, cmd.Dir = testStdout, stderr, cwd
	runErr := cmd.Run()
	testExit := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return fail(stderr, "replay: could not run the test command: %v", runErr)
		}
		testExit = ee.ExitCode()
	}

	misses := srv.Misses()
	if err := srv.MissLogErr(); err != nil {
		// The count and the report are already correct — the in-memory
		// record is authoritative — but the durable file a CI job reads
		// afterwards is not, and that must not pass silently.
		fmt.Fprintf(stderr, "retrace: replay: could not append to %s (%v) — the miss report below is complete, the file is not\n", p.MissesPath, err)
	}

	served := srv.ServedCount()
	unused := srv.UnusedExchanges()

	if *asJSON {
		if err := writeJSON(stdout, replayReport{
			App: appName, Flow: flow,
			Ref:       replayRef{Kind: r.Kind, Dir: r.Dir, RunID: r.RunID},
			RunDir:    p.RunDir,
			Exchanges: len(bundle.Exchanges),
			Served:    served,
			Unused:    unused,
			MissCount: len(misses),
			Misses:    misses,
			Test:      replayTest{Command: strings.Join(testCmd, " "), ExitCode: testExit},
		}); err != nil {
			return fail(stderr, "replay: %v", err)
		}
	} else {
		renderReplay(stdout, appName, flow, r.Kind, len(bundle.Exchanges), served, unused, p.RunDir, misses)
	}

	// A miss is a hard gate. It outranks the test command's own code on
	// purpose: the interesting case is precisely the one where the suite
	// went green while calling something the recording never saw.
	if len(misses) > 0 {
		return exitGate
	}
	// Nothing was compared. That is NOT the same verdict as "everything
	// matched", and it must never share an exit code with it: a miss (2)
	// means the recording and reality disagree, which is a finding, while
	// zero served means there was nothing to disagree about. `revalidate`
	// already separates those, and 3 — could not evaluate — is where this
	// belongs.
	if served == 0 {
		fmt.Fprintf(stderr, "retrace: replay: the test command made NO calls the bundle could answer — nothing was compared, so this is not a pass\n")
		fmt.Fprintf(stderr, "  point the app under test at RETRACE_PROXY_URL (a hard-coded base URL ignores it), and check the command actually ran its suite\n")
		return exitUsage
	}
	return testExit
}

// requireLoopback refuses a --listen address that is not loopback, BEFORE
// a listener exists. The replay server serves recorded traffic — request
// and response bodies lifted verbatim out of a bundle — so the bind the
// product offers is loopback, and the help text has always said so. A flag
// must not describe a guarantee that is not made (R-I). httpguard stays
// exactly as it is: defence in depth for the Host header on a loopback
// bind, not a licence to offer this one.
func requireLoopback(addr string) error {
	// The determination itself lives in loopbackAddr (cmd_serve.go), which
	// `serve` also consults — for a different DECISION (it may bind wide,
	// behind an explicit --allow-host) but off the same fact. Two copies of
	// "is this loopback" is two places for the 0.0.0.0 case, the name-that-
	// resolves-both-ways case, or the IPv6 case to be got right once and
	// wrong once. The refusal below, and its wording, stay here: they are
	// this command's policy, not a shared one.
	loopback, err := loopbackAddr(addr)
	if err != nil {
		return fmt.Errorf("--listen %v", err)
	}
	if !loopback {
		return fmt.Errorf("--listen %s is not a loopback address — a replay server answers with recorded traffic, which carries whatever the bundle recorded (tokens, cookies, personal data), so it binds 127.0.0.1 only; reach it from another host with an SSH tunnel (`ssh -L 9000:127.0.0.1:9000 host`) or a port-forward, not by widening the bind", addr)
	}
	return nil
}

// renderReplay prints one line per miss, each naming the nearest recorded
// exchange and the fields that differed. Nothing is truncated — a report
// an agent has to read is not a dashboard.
func renderReplay(w io.Writer, app, flow, kind string, exchanges, served int, unused []replay.Key, runDir string, misses []replay.Miss) {
	fmt.Fprintf(w, "retrace: replayed %s/%s from the %s (%d recorded exchanges, %d served)\n", app, flow, kind, exchanges, served)
	fmt.Fprintf(w, "  %s\n", runDir)
	for _, k := range unused {
		fmt.Fprintf(w, "  never called: %s %s%s\n", k.Method, k.Path, pathQuery(k.Query))
	}
	if len(misses) == 0 {
		if served == 0 {
			// Never the "every call matched" sentence for a replay in
			// which nothing was asked. Two different worlds must not print
			// the same verdict.
			fmt.Fprintf(w, "  NOTHING WAS COMPARED: the test command made no calls the bundle could answer\n")
			return
		}
		fmt.Fprintf(w, "  every call matched the recording\n")
		return
	}
	var unmatched, keyUnavailable []replay.Miss
	for _, m := range misses {
		if m.Kind == replay.MissKeyUnavailable {
			keyUnavailable = append(keyUnavailable, m)
		} else {
			unmatched = append(unmatched, m)
		}
	}
	if len(unmatched) > 0 {
		fmt.Fprintf(w, "\n  %d unmatched call(s) — the client made a request the reference bundle does not contain:\n", len(unmatched))
		for _, m := range unmatched {
			q := ""
			if m.Query != "" {
				q = "?" + m.Query
			}
			near := "no comparable exchange"
			if m.Nearest != nil {
				near = "nearest " + m.Nearest.Method + " " + m.Nearest.Path
				if m.Nearest.Query != "" {
					near += "?" + m.Nearest.Query
				}
			}
			fmt.Fprintf(w, "    %s %s%s — %s\n", m.Method, m.Path, q, near)
			for _, f := range m.Diff {
				fmt.Fprintf(w, "      %s: expected %s, got %s\n", f.Field, f.Expected, f.Actual)
			}
		}
		fmt.Fprintf(w, "\n  Either the client changed — this is the deviation replay exists to catch — or the\n")
		fmt.Fprintf(w, "  recording is stale: `retrace revalidate --ref %s --upstream URL` says which.\n", flow)
	}
	if len(keyUnavailable) > 0 {
		fmt.Fprintf(w, "\n  %d call(s) matched a recording this server could not decrypt:\n", len(keyUnavailable))
		for _, m := range keyUnavailable {
			q := ""
			if m.Query != "" {
				q = "?" + m.Query
			}
			fmt.Fprintf(w, "    %s %s%s — %s\n", m.Method, m.Path, q, m.Detail)
		}
	}
}

// pathQuery renders a query for display, "" when there is none.
func pathQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

// printCandidates renders the runs refs.Resolve tried and why each was
// rejected. A "no reference" that names nothing tells the operator only
// that they are stuck.
func printCandidates(w io.Writer, r refs.Reference) {
	for _, c := range r.History {
		detail := ""
		if c.Detail != "" {
			detail = " (" + c.Detail + ")"
		}
		fmt.Fprintf(w, "  %s: %s%s\n", c.RunID, c.Reason, detail)
	}
}

// replayOptions assembles what the matcher is allowed to loosen, from the
// config alone. Every field's zero value is the strict one, so a project
// with no retrace.yaml gets the strictest possible mock rather than the
// most permissive.
func replayOptions(cfg *config.Config) (replay.Options, error) {
	rs, err := cfg.Rules()
	if err != nil {
		return replay.Options{}, err
	}
	return replay.Options{Rules: rs, Normalize: cfg.NormalizePath, QueryIgnore: cfg.QueryIgnoreKeys()}, nil
}

// createReplayRun makes the replay's run directory. Run ids are
// timestamp-first (runs.NewRunID) so listings stay chronological, which
// leaves a 1-second collision window between two replays of the same flow;
// runs.Create refuses to let two runs share a directory, so a collision is
// resolved with a suffix rather than by silently writing both replays'
// misses into one file.
func createReplayRun(cwd, app, flow string) (runs.Paths, error) {
	root := replaysRoot(cwd)
	base := runs.NewRunID(time.Now(), capture.GitInfo(cwd).SHA)
	for attempt := 0; attempt < 50; attempt++ {
		id := base
		if attempt > 0 {
			id = fmt.Sprintf("%s.%d", base, attempt+1)
		}
		p, err := runs.Create(root, app, flow, id)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return runs.Paths{}, err
		}
	}
	return runs.Paths{}, fmt.Errorf("cannot create a replay run directory under %s: 50 ids in the same second were already taken", root)
}
