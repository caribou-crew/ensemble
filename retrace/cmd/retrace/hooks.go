package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/config"
)

// Hooks are the generic slot for everything a flow needs true before it is
// worth recording: a seed assertion, a device or simulator discovery step, an
// env stamp, a migration check. retrace never learns what any of them are —
// the consumer repo encodes its own preconditions as commands, and retrace
// only enforces the contract that a failing one stops the capture.
//
// Why that contract matters more than the commands: a capture taken against a
// stack whose seed silently failed is not a failed capture, it is a plausible
// one. It records, it diffs, and it reports differences that are really just
// missing data — which is exactly the noise an agent driving the loop will
// iterate against forever. Refusing to capture is the cheaper answer.
//
// Ordering, and where the recording window sits:
//
//	preflight (global)      ─┐ before the session exists, so nothing they do
//	preflight (flow)         │ is recorded and a failure costs no run dir
//	setup (flow)            ─┘
//	  ── session opens ── the flow's command runs, traffic is captured ──
//	  ── session closes ──
//	teardown (flow)           always, even when the flow failed
//
// setup deliberately runs OUTSIDE the recording window. A seed step that ran
// inside it would be captured as flow traffic and diffed as though the app had
// made those calls — the wire plane would describe the harness, not the app.
// The corollary is that setup does not get the proxy env: it talks to the
// stack directly, which is what seeding should do anyway.
const hookShell = "/bin/sh"

// hookError names the exact command that stopped the run. "preflight failed"
// with no command is a message that sends someone to read their whole config;
// the command text and its exit status are the entire diagnostic value here.
type hookError struct {
	Label   string
	Index   int
	Total   int
	Command string
	Err     error
}

func (e *hookError) Error() string {
	return fmt.Sprintf("%s command %d of %d failed (%v): %s", e.Label, e.Index+1, e.Total, e.Err, e.Command)
}

func (e *hookError) Unwrap() error { return e.Err }

// runHooks runs cmds in declared order through a shell, stopping at the first
// failure. Declared order is load-bearing — a discovery step that feeds the
// step after it is the normal shape — so this never parallelizes.
//
// Everything the commands print goes to stderr, never stdout: under --json,
// stdout carries the manifest alone as the documented CI contract, and a hook
// that echoes a line would otherwise corrupt the document a pipeline is
// parsing.
func runHooks(ctx context.Context, label string, cmds []string, cwd string, env []string, stderr io.Writer) error {
	for i, c := range cmds {
		if strings.TrimSpace(c) == "" {
			// `sh -c ""` exits 0, so a blank entry would be a hook that
			// silently never runs while the config claims a precondition is
			// being enforced. That is the failure mode this whole file exists
			// to prevent, so it is an error rather than a skip.
			return &hookError{Label: label, Index: i, Total: len(cmds), Command: "(blank)",
				Err: fmt.Errorf("blank command: a hook that runs nothing cannot enforce anything")}
		}
		fmt.Fprintf(stderr, "retrace: %s [%d/%d] %s\n", label, i+1, len(cmds), c)
		cmd := exec.CommandContext(ctx, hookShell, "-c", c)
		cmd.Dir = cwd
		cmd.Env = env
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if err := cmd.Run(); err != nil {
			return &hookError{Label: label, Index: i, Total: len(cmds), Command: c, Err: err}
		}
	}
	return nil
}

// hookEnv is the process environment plus the two facts a generic hook cannot
// otherwise know. A seed script shared across flows needs to know which flow
// it is preparing for, and requiring every consumer to thread that through
// their own wrapper would defeat the point of a declarative hook.
func hookEnv(app, flow string) []string {
	return append(os.Environ(),
		"RETRACE_APP="+app,
		"RETRACE_FLOW="+flow,
	)
}

// runGlobalPreflight runs Config.Preflight — ONCE per invocation, before any
// flow, which is what the config has always promised. It is the coarse check
// ("is the stack even up"), so it gates every flow's own hooks: running a
// flow's seed assertion before knowing the stack answered at all produces a
// failure that blames the flow when nothing is running.
//
// Once matters more since a single `retrace run` can record many flows. A
// global preflight re-run per flow would charge a multi-flow run N times for
// a check whose entire scope is the stack, and a flaky one would fail a later
// flow for a condition that was true when the run started.
//
// The flow name is not in the env here: the global preflight is not being run
// on behalf of any one flow, and stamping it with the first flow's name would
// be a lie a shared seed script could easily act on.
func runGlobalPreflight(ctx context.Context, cfg *config.Config, app, cwd string, stderr io.Writer) error {
	return runHooks(ctx, "preflight", cfg.Preflight, cwd, hookEnv(app, ""), stderr)
}

// runFlowPreconditions runs one flow's own preflight, then its setup — the
// per-flow half of the before-the-session sequence. runGlobalPreflight has
// already passed by the time this is called.
func runFlowPreconditions(ctx context.Context, fl config.Flow, app, flow, cwd string, stderr io.Writer) error {
	env := hookEnv(app, flow)
	if err := runHooks(ctx, "flow preflight", fl.Preflight, cwd, env, stderr); err != nil {
		return err
	}
	return runHooks(ctx, "setup", fl.Setup, cwd, env, stderr)
}
