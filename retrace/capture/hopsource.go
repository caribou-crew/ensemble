// hopsource.go collects a run's hop chain from somewhere other than
// ensemble's control plane: a pair of commands that bound a tracing window
// and export it, or a file of hops NDJSON that already exists.
//
// It produces []trace.Hop and nothing else. Everything downstream — the
// Redactor, hops.jsonl, the counts, the capture-trust record — is the SAME
// code the ensemble path goes through, which is the whole point: a hop plane
// that reached disk by a different route must be indistinguishable from one
// that did not, or the two can disagree about what a recording means.
package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// hopCommandTimeout bounds one arm/disarm/export command. Generous compared
// with the control-plane timeouts next door: an export usually queries a
// remote tracing backend, which is a network round trip against someone
// else's service and not a loopback REST call.
const hopCommandTimeout = 60 * time.Second

// HopSource runs a configured external hop source. Dir is where its commands
// run and where a relative File resolves — the directory holding retrace.yaml,
// never the process's working directory, so a run started from elsewhere reads
// the same fixture.
type HopSource struct {
	Kind   string
	Arm    string
	Disarm string
	Export string
	File   string
	Dir    string
	// Env is the environment the commands inherit on top of the process's own
	// — the session variables a script needs to find the run it is exporting
	// for (RETRACE_RUN_DIR and friends).
	Env []string
	// Stderr is where a command's own diagnostics go. Empty means os.Stderr.
	Stderr io.Writer
	// Shell overrides the interpreter, for tests. Empty means "sh -c".
	Shell []string
}

// HopSourceFrom builds a runnable source from the parsed config. It is the
// one translation between the two, so a kind constant is never re-spelled as
// a literal on the way across.
func HopSourceFrom(src config.HopSource, dir string, env []string, stderr io.Writer) HopSource {
	return HopSource{
		Kind: src.Resolved(), Arm: src.Arm, Disarm: src.Disarm, Export: src.Export,
		File: src.File, Dir: dir, Env: env, Stderr: stderr,
	}
}

// HopWindow is the one JSON line `disarm` writes to stdout. A window id is
// how a tracing backend names the interval that just closed; export receives
// it as RETRACE_HOP_WINDOW.
//
// A disarm that prints nothing is legal — some backends have no such
// identifier — and leaves the variable unset rather than set to the empty
// string, which a script would read as "there is a window called ”".
type HopWindow struct {
	WindowID string `json:"windowId"`
}

// EnvHopWindow is the variable `export` reads to learn which window to dump.
const EnvHopWindow = "RETRACE_HOP_WINDOW"

// Open arms the tracing window, before the test command runs.
//
// A failure here FAILS the run rather than recording without a hop plane. The
// config asked for a chain; a recording that silently has none looks exactly
// like a stack that made no downstream calls, and that is the one confusion
// this whole plane exists to prevent. Failing before the test command also
// costs the user a few seconds instead of a full suite.
func (h HopSource) Open(ctx context.Context) error {
	if h.Arm == "" {
		return nil
	}
	if _, err := h.run(ctx, h.Arm, nil); err != nil {
		return fmt.Errorf("arming the hop source: %w", err)
	}
	return nil
}

// Collect closes the window and returns the hops it yields.
//
// Unlike Open, a failure here is REPORTED, not fatal: the wire plane, the
// screenshots and the manifest are already on disk by the time it runs, and
// discarding a good recording over a failed telemetry export would be the
// worse trade. The caller notes it in the durable trust record — the same
// treatment a failed ensemble drain gets.
func (h HopSource) Collect(ctx context.Context) ([]trace.Hop, error) {
	if h.Kind == config.HopSourceFile {
		path := h.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(h.Dir, path)
		}
		// Checked explicitly: runs.ReadHops reads a missing file as an empty
		// run, which is right for a run directory that was never written and
		// exactly wrong here. A config that names a fixture and gets zero hops
		// because the path is a typo would record a chain-less run that looks
		// like a stack making no downstream calls.
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("reading hops from %s: %w", path, err)
		}
		hops, skipped, err := runs.ReadHops(path)
		if err != nil {
			return nil, fmt.Errorf("reading hops from %s: %w", path, err)
		}
		// ReadHops tolerates unparseable lines because a run that died
		// mid-write leaves a truncated tail. A fixture is not a casualty of
		// anything: a line it cannot read means the file is not what it claims
		// to be, and dropping it would report a shorter chain than the file
		// holds.
		if skipped > 0 {
			return nil, fmt.Errorf("reading hops from %s: %d line(s) are not core/trace hops", path, skipped)
		}
		return hops, nil
	}

	var extra []string
	if h.Disarm != "" {
		out, err := h.run(ctx, h.Disarm, nil)
		if err != nil {
			return nil, fmt.Errorf("disarming the hop source: %w", err)
		}
		w, err := parseWindow(out)
		if err != nil {
			return nil, fmt.Errorf("disarming the hop source: %w", err)
		}
		if w.WindowID != "" {
			extra = append(extra, EnvHopWindow+"="+w.WindowID)
		}
	}
	out, err := h.run(ctx, h.Export, extra)
	if err != nil {
		return nil, fmt.Errorf("exporting hops: %w", err)
	}
	hops, err := decodeHops(out)
	if err != nil {
		return nil, fmt.Errorf("exporting hops: %w", err)
	}
	return hops, nil
}

// parseWindow reads disarm's stdout: exactly one JSON object, or nothing.
//
// Strict on purpose. A disarm that printed a log line and then the object
// would parse fine under a lenient reader and hand export a window id that
// came from whatever the last line happened to be; a disarm whose stdout is
// entirely diagnostics would silently export the WRONG window. Both are
// failures the run should hear about, so both are errors here — the contract
// is one line, and it says so.
func parseWindow(out []byte) (HopWindow, error) {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return HopWindow{}, nil
	}
	if strings.ContainsAny(text, "\n") {
		return HopWindow{}, fmt.Errorf("stdout must be a single JSON line %s, got %d lines — send diagnostics to stderr",
			`{"windowId": "..."}`, len(strings.Split(text, "\n")))
	}
	var w HopWindow
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return HopWindow{}, fmt.Errorf("stdout must be %s: %w", `{"windowId": "..."}`, err)
	}
	return w, nil
}

// decodeHops reads export's stdout as hops NDJSON.
//
// A malformed line is an ERROR, not a skip. runs.ReadHops tolerates a
// truncated tail because a run that died mid-write really does leave one, and
// the alternative there is discarding a recording that exists. Nothing of the
// kind is true of a command's stdout: an unparseable line means the exporter
// emitted something other than the schema it promised, and quietly dropping
// it would report a shorter chain than the backend actually holds — a hop
// plane missing calls, with nothing anywhere saying so.
func decodeHops(out []byte) ([]trace.Hop, error) {
	var hops []trace.Hop
	for i, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var h trace.Hop
		dec := json.NewDecoder(bytes.NewReader(line))
		if err := dec.Decode(&h); err != nil {
			return nil, fmt.Errorf("line %d is not a core/trace hop: %w", i+1, err)
		}
		// The schema is checked HERE and not left to the reader. runs.ReadHops
		// silently skips a hop whose schema it does not know, so a chain
		// exported in the wrong shape would be written to hops.jsonl in full
		// and read back empty — a run whose file and whose counts disagree,
		// with nothing between them saying why.
		if h.Schema != trace.SchemaVersion {
			return nil, fmt.Errorf("line %d has schema %q, want %q — export must emit core/trace hops",
				i+1, h.Schema, trace.SchemaVersion)
		}
		hops = append(hops, h)
	}
	return hops, nil
}

// run executes one command through a shell, returning its stdout. Stderr goes
// straight to the caller's stderr on purpose: it is the script's own
// diagnostics channel, and a user watching a run sees it live rather than
// only bundled into an error message if one happens to fail.
func (h HopSource) run(ctx context.Context, command string, extraEnv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, hopCommandTimeout)
	defer cancel()

	argv := h.Shell
	if len(argv) == 0 {
		argv = []string{"sh", "-c"}
	}
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], command)...)
	cmd.Dir = h.Dir
	cmd.Env = append(append(append([]string{}, os.Environ()...), h.Env...), extraEnv...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = h.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("`%s`: %w", command, err)
	}
	return stdout.Bytes(), nil
}

// RecordExternalHops writes hops collected from an external source to
// hops.jsonl, through the SAME Redactor the ensemble path uses.
//
// Re-redacting is not belt-and-braces. The producer applied whatever rules IT
// was configured with, and a recording is committed and shared: retrace
// applies its own key list to everything that reaches its disk, whoever
// produced it.
func (s *Session) RecordExternalHops(hops []trace.Hop) error {
	// The SAME already-resolved data key the ensemble/standalone path used
	// (s.dataKey, set at Start*) — one data key per run, whichever path
	// wrote hops, never a second load/generate here.
	red, err := trace.NewRedactor(config.RedactKeyRules(s.redact), s.maxBody, s.dataKey)
	if err != nil {
		return fmt.Errorf("capture: rebuilding the redactor for external hops: %w", err)
	}
	n := 0
	if err := writeHops(s.Paths.HopsPath, hops, red, func(trace.Hop) bool { return true }, &n); err != nil {
		return err
	}
	s.hops = hops
	s.externalHops = true
	return nil
}

// RecordedChain is the chain that reached hops.jsonl — drained from ensemble
// or collected from a configured source. It is deliberately NOT Hops():
// Hops() answers "what did retrace itself observe", which is what the
// capture-trust assessment reasons about (reachability, gaps between calls
// retrace watched), and in standalone mode that is the local recorder's view
// of the client edge. An external chain is evidence about the BACKEND,
// collected by someone else's clock, and reading it as evidence about
// retrace's own capture would let a stale fixture vouch for a proxy the app
// never routed through.
//
// The manifest's hop count comes from here, so the number and the file can
// never disagree.
func (s *Session) RecordedChain() []trace.Hop { return s.hops }

// HopsRecorded reports whether this run has a hop plane at all — a chain was
// recorded, whether or not it turned out to be empty. It is the difference
// between manifest.hops being absent and being present-and-zero, which is the
// difference between "nobody looked" and "we looked and there was nothing".
func (s *Session) HopsRecorded() bool {
	return s.Mode == runs.ModeEnsemble || s.externalHops
}

// NoteHopSourceFailure records that Collect failed, so the shortfall survives
// in the durable trust record rather than only in the caller's stderr.
func (s *Session) NoteHopSourceFailure(err error) {
	s.trustNotes = append(s.trustNotes, "collecting hops from the configured source failed: "+err.Error())
}
