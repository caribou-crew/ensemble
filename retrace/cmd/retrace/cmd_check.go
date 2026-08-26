package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// identifyTimeout bounds a single /identify probe. A door that is bound but
// wedged must not hold the whole command: the question "is this port still
// held" is answered by a fast yes/no, and a slow answer is a no for
// supervision purposes.
const identifyTimeout = 2 * time.Second

// Identity is one door's answer. It mirrors the marker door's JSON rather
// than reusing runs.Running: what a door reports and what a file records
// are different facts, and a shared type would let a missing field read as
// an empty one.
type Identity struct {
	Tool          string    `json:"tool"`
	OwnerRecorded bool      `json:"ownerRecorded"`
	PID           int       `json:"pid,omitempty"`
	App           string    `json:"app,omitempty"`
	Flow          string    `json:"flow,omitempty"`
	RunID         string    `json:"runId,omitempty"`
	ProxyURL      string    `json:"proxyUrl,omitempty"`
	MarkerURL     string    `json:"markerUrl,omitempty"`
	StartedAt     time.Time `json:"startedAt,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// probeResult is one door probe: what was asked, and what came back.
// Answered is never inferred from Identity being zero — "nothing is
// listening" and "something answered with an empty body" are different
// findings, and only the first means the port is free.
type probeResult struct {
	URL       string    `json:"url"`
	Answered  bool      `json:"answered"`
	IsRetrace bool      `json:"isRetrace"`
	Identity  *Identity `json:"identity,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// checkResult is the --json document.
type checkResult struct {
	Probes []probeResult `json:"probes"`
	// Unsupervised are the runs that hold no finalized sentinel. Present
	// only for the no-flag form, which is a sweep rather than a point probe.
	Unsupervised []runs.RunStatus `json:"unsupervised,omitempty"`
	Abandoned    int              `json:"abandoned"`
	// Held is the --url form's answer: does a retrace run hold this address?
	// It is a separate field rather than a reuse of Abandoned because they
	// are different questions, and a script branching on the wrong one would
	// read "the port is free" as "nothing was abandoned".
	Held bool `json:"held"`
}

// cmdCheck answers "who holds this port" and "which runs never finished".
//
// Two forms, because the question arrives two ways:
//
//	retrace check --url http://127.0.0.1:53221
//	    A point probe. You have a port and a "already in use" error, and
//	    lsof gave you a pid with no way to tell which RUN it belongs to.
//
//	retrace check
//	    A sweep. Every run without a finalized sentinel is listed, and each
//	    one that recorded a marker door is probed to see whether it is still
//	    held. This is the form that turns "something is leaking ports" into
//	    a named run.
//
// The sweep probes the door rather than trusting pid liveness alone,
// because pids are reused: `runs.Status` can only say a process with that
// number exists, while a door that answers with THIS run's id proves it is
// still the same process.
//
// Exit codes, in both forms 0 means "nothing of ours needs your attention":
//
//	--url    0 no retrace run holds this address (it is free, or something
//	           else has it); 1 a retrace run holds it, pid and run id
//	           reported. So `retrace check --url X && <bind X>` proceeds
//	           only when nothing of ours is there.
//	sweep    0 every run is finalized; 1 abandoned runs were found.
//
// 1 rather than 2 in both cases — leftover state is a finding to review,
// not a gate a build should die on. 3 stays "could not evaluate".
func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		url       = fs.String("url", "", "probe this marker-door URL's /identify instead of sweeping recorded runs (host:port is accepted; http:// is assumed)")
		app       = fs.String("app", "", "sweep only this app (default: every recorded app)")
		flow      = fs.String("flow", "", "sweep only this flow (default: every recorded flow)")
		asJSON    = fs.Bool("json", false, "emit the result as JSON on stdout")
		abandonAf = fs.Duration("abandoned-after", runs.DefaultAbandonAfter,
			"how long an un-finalized run with NO recorded owner may go before it is called abandoned")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if strings.TrimSpace(*url) != "" {
		return checkOneURL(normalizeURL(*url), *asJSON, stdout, stderr)
	}
	return checkSweep(*app, *flow, *abandonAf, *asJSON, stdout, stderr)
}

// normalizeURL accepts "127.0.0.1:53221" as well as a full URL. The value
// a user has in hand comes from an "address already in use" message or from
// lsof, and neither prints a scheme.
func normalizeURL(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		return strings.TrimRight(s, "/")
	}
	return "http://" + strings.TrimRight(s, "/")
}

// identify probes one door. Every failure mode is reported as data rather
// than as an error return: "nothing answered" is the single most useful
// answer this command gives (the port is free), and a command that exited 3
// on a refused connection could never say it.
func identify(ctx context.Context, base string) probeResult {
	res := probeResult{URL: base}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/identify", nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.Answered = true
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return res
	}
	var id Identity
	if err := json.NewDecoder(resp.Body).Decode(&id); err != nil {
		res.Error = "answer was not JSON: " + err.Error()
		return res
	}
	// `tool` is the discriminator, not the mere fact that something
	// answered: any HTTP server on that port answers 404 with a body, and
	// treating "answered" as "is retrace" is how a user gets told to go
	// kill an unrelated process.
	res.IsRetrace = id.Tool == "retrace"
	res.Identity = &id
	return res
}

func checkOneURL(base string, asJSON bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), identifyTimeout)
	defer cancel()
	res := identify(ctx, base)

	if asJSON {
		if err := writeJSON(stdout, checkResult{Probes: []probeResult{res}, Held: res.IsRetrace}); err != nil {
			return fail(stderr, "check: %v", err)
		}
	} else {
		printProbe(stdout, res)
	}
	// 1 means "a retrace run holds this address", so
	// `retrace check --url X && <bind X>` reads correctly: proceed only when
	// nothing of ours is there.
	//
	// A free port is exit 0, NOT a finding. An earlier cut had it backwards
	// and printed a ✓ beside a non-zero exit — the glyph said "all clear"
	// while the code said "look at this", and a contract nobody can predict
	// is one nobody scripts against.
	if res.IsRetrace {
		return exitDiff
	}
	return exitOK
}

func printProbe(w io.Writer, res probeResult) {
	switch {
	case !res.Answered:
		fmt.Fprintf(w, "✓ nothing is listening at %s (%s)\n", res.URL, res.Error)
		fmt.Fprintf(w, "  the port is free — whatever reported it as busy was looking at a different address\n")
	case !res.IsRetrace:
		fmt.Fprintf(w, "✓ %s answered, but not as retrace\n", res.URL)
		if res.Error != "" {
			fmt.Fprintf(w, "  %s\n", res.Error)
		}
		fmt.Fprintf(w, "  something else holds this port; retrace will not tell you what, and must not be the one to kill it\n")
	case res.Identity != nil && !res.Identity.OwnerRecorded:
		fmt.Fprintf(w, "⚠ %s is a retrace door with no owner record\n", res.URL)
		fmt.Fprintf(w, "  a replay server, or a capture from a build older than run supervision\n")
		if res.Identity.Error != "" {
			fmt.Fprintf(w, "  the run directory reported: %s\n", res.Identity.Error)
		}
	default:
		id := res.Identity
		fmt.Fprintf(w, "⚠ %s is retrace, pid %d\n", res.URL, id.PID)
		fmt.Fprintf(w, "  run:   %s/%s/%s\n", id.App, id.Flow, id.RunID)
		if id.ProxyURL != "" {
			fmt.Fprintf(w, "  proxy: %s\n", id.ProxyURL)
		}
		if !id.StartedAt.IsZero() {
			fmt.Fprintf(w, "  since: %s (%s)\n", id.StartedAt.Format(time.RFC3339), humanAge(int(time.Since(id.StartedAt)/time.Second)))
		}
	}
}

func checkSweep(app, flow string, abandonAf time.Duration, asJSON bool, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "check: cannot determine the working directory: %v", err)
	}
	root := runs.RunsRoot(cwd)
	all, err := runs.StatusAll(root, time.Now(), abandonAf)
	if err != nil {
		return fail(stderr, "check: cannot read %s: %v", root, err)
	}

	var unsupervised []runs.RunStatus
	for _, st := range all {
		if st.State == runs.StateComplete {
			continue
		}
		if app != "" && st.App != app {
			continue
		}
		if flow != "" && st.Flow != flow {
			continue
		}
		unsupervised = append(unsupervised, st)
	}

	// Probe every door an un-finalized run recorded. This is what separates
	// "a pid with that number exists" from "our run is still there": pids
	// are reused, and a door that answers with a run id is proof the
	// original process is the one still holding it.
	var probes []probeResult
	ctx, cancel := context.WithTimeout(context.Background(), identifyTimeout*time.Duration(max(1, len(unsupervised))))
	defer cancel()
	for _, st := range unsupervised {
		if st.Owner == nil || st.Owner.MarkerURL == "" {
			continue
		}
		pctx, pcancel := context.WithTimeout(ctx, identifyTimeout)
		probes = append(probes, identify(pctx, st.Owner.MarkerURL))
		pcancel()
	}

	abandoned := 0
	for _, st := range unsupervised {
		if st.State == runs.StateAbandoned {
			abandoned++
		}
	}

	if asJSON {
		if err := writeJSON(stdout, checkResult{Probes: probes, Unsupervised: unsupervised, Abandoned: abandoned}); err != nil {
			return fail(stderr, "check: %v", err)
		}
	} else {
		printSweep(stdout, root, unsupervised, probes, abandoned)
	}
	if abandoned > 0 {
		return exitDiff
	}
	return exitOK
}

func printSweep(w io.Writer, root string, unsupervised []runs.RunStatus, probes []probeResult, abandoned int) {
	if len(unsupervised) == 0 {
		fmt.Fprintf(w, "✓ every run under %s is finalized\n", root)
		return
	}
	for _, st := range unsupervised {
		fmt.Fprintf(w, "%s %s/%s/%s — %s\n", stateMark(st.State), st.App, st.Flow, st.RunID, st.State)
		fmt.Fprintf(w, "  %s\n", st.Reason)
		if st.Owner != nil && st.Owner.ProxyURL != "" {
			fmt.Fprintf(w, "  proxy: %s\n", st.Owner.ProxyURL)
		}
	}
	held := 0
	for _, p := range probes {
		if p.IsRetrace {
			held++
		}
	}
	fmt.Fprintf(w, "\n%d un-finalized run(s), %d abandoned, %d still holding a door\n", len(unsupervised), abandoned, held)
	if abandoned > 0 {
		fmt.Fprintf(w, "\nAn abandoned run's directory holds a partial recording. Delete it, or keep\nit for forensics — but never diff against it: nothing finished writing it.\n")
	}
}
