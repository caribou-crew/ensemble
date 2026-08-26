package runs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Run-directory sentinels. Together they answer the one question a run
// directory could not previously answer: is anything still writing to it?
//
// Before these existed, "a run directory" and "a COMPLETE run directory"
// were the same bytes on disk. A capture killed mid-flow — Ctrl-C, a CI
// timeout, an OOM — left a directory holding a shots dir and a partial
// wire.jsonl and no manifest, indistinguishable from a run that was still
// three seconds from finishing. Every reader then had to guess, and the
// permissive guess (treat it as finished) is the one that reports a
// truncated wire plane as a clean one.
const (
	// RunningFile is written FIRST, as soon as the run directory exists and
	// the listeners are up, and names the process that owns the directory.
	RunningFile = "running.json"
	// FinalizedFile is written LAST, after manifest.json is safely on disk.
	// Its presence is the only proof a run finished; its absence is never
	// read as "fine". The name is bare (no extension) because it is a
	// sentinel first and a document second.
	FinalizedFile = "finalized"
)

// SuperviseSchema versions the sentinel documents independently of the
// manifest: a sentinel is written by a process that may crash before it
// writes anything else, so it must stay readable by a newer build without
// depending on the manifest schema having been reached at all.
const SuperviseSchema = "retrace/supervise/1"

// DefaultAbandonAfter is how old an un-finalized run with NO owner
// recorded must be before it is called abandoned. It is a FALLBACK bound,
// not the primary test: a run that recorded an owner is judged by whether
// that owner is still alive (see Status), which is both faster and correct
// for a suite that legitimately runs for an hour. This bound only governs
// directories written by a build that predates RunningFile, or whose
// running.json is unreadable.
const DefaultAbandonAfter = 15 * time.Minute

// Running is the owner record. It exists so a stray listener can be traced
// back to the run that opened it: the incident this whole file is for is
// "port 53221 is already in use" with no way to tell whether the holder is
// retrace, which run it belongs to, or whether it is safe to kill.
type Running struct {
	Schema    string    `json:"schema"`
	PID       int       `json:"pid"`
	App       string    `json:"app"`
	Flow      string    `json:"flow"`
	RunID     string    `json:"runId"`
	ProxyURL  string    `json:"proxyUrl,omitempty"`
	MarkerURL string    `json:"markerUrl,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// Finalized is the completion record.
type Finalized struct {
	Schema     string    `json:"schema"`
	RunID      string    `json:"runId"`
	FinishedAt time.Time `json:"finishedAt"`
	// ExitCode is the test command's exit status, duplicated from the
	// manifest on purpose: this file is the one a supervisor reads when it
	// does not want to (or cannot) parse a full manifest.
	ExitCode int `json:"exitCode"`
}

// State is a run directory's supervision state. The zero value is
// deliberately not one of these — a State that was never computed must not
// compare equal to StateComplete, for the same reason Counts{} means
// "unrecorded" rather than "clean".
type State string

const (
	// StateRunning: an owner is alive, or the directory is too young to
	// judge. Never a claim that the run will finish.
	StateRunning State = "running"
	// StateComplete: FinalizedFile is present.
	StateComplete State = "complete"
	// StateAbandoned: no FinalizedFile, and either the recorded owner is
	// gone or the directory has aged past the fallback bound.
	StateAbandoned State = "abandoned"
)

// RunStatus is what Status reports. Owner is nil when no running.json was
// readable — which is itself the fact that forces the age fallback, so it
// must stay distinguishable from a zero-valued Running.
type RunStatus struct {
	App   string `json:"app"`
	Flow  string `json:"flow"`
	RunID string `json:"runId"`
	State State  `json:"state"`
	// Reason says which rule produced State, in one clause. A supervision
	// verdict a human cannot audit is a verdict they will override blindly.
	Reason string `json:"reason"`
	// Age is measured from StartedAt (or the run id's timestamp when no
	// owner was recorded) to the `now` passed to Status.
	AgeSeconds int        `json:"ageSeconds"`
	StartedAt  time.Time  `json:"startedAt"`
	Owner      *Running   `json:"owner,omitempty"`
	Done       *Finalized `json:"done,omitempty"`
}

// processAlive reports whether a process with this pid currently exists. It
// is a package var so tests can pin liveness without spawning real
// processes — a test that forked a child to be killed would be testing the
// operating system's scheduler, not this file's rules.
//
// Signal 0 performs error checking only: it never delivers a signal, and
// EPERM means the process EXISTS and belongs to another user, which is
// still alive for our purposes.
var processAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (p Paths) runningPath() string   { return filepath.Join(p.RunDir, RunningFile) }
func (p Paths) finalizedPath() string { return filepath.Join(p.RunDir, FinalizedFile) }

// RunningPath and FinalizedPath expose the sentinel locations for callers
// outside this package (the CLI's `runs`/`check`) without letting them
// rebuild the join themselves — the same single-construction-seam rule
// PathsFor follows.
func (p Paths) RunningPath() string   { return p.runningPath() }
func (p Paths) FinalizedPath() string { return p.finalizedPath() }

// MarkRunning writes the owner record. It is called once the run directory
// and its listeners exist, and BEFORE the test command is started: a
// command that crashes the whole process on its first line must still
// leave behind a directory that says who owned it.
//
// PID is stamped here rather than taken from the caller so there is exactly
// one answer to "which process is this", and Schema is stamped for the same
// reason WriteManifest stamps it.
func MarkRunning(p Paths, r Running) error {
	r.Schema = SuperviseSchema
	r.PID = os.Getpid()
	if r.StartedAt.IsZero() {
		return fmt.Errorf("runs: running record needs a startedAt — a run with no start time cannot be aged")
	}
	return writeJSONFile(p.runningPath(), r)
}

// Finalize writes the completion sentinel. It must be the LAST write into a
// run directory; callers that write anything afterwards have made the
// sentinel a lie.
//
// It also removes running.json: leaving both behind would make "is anything
// writing here" answerable two ways, and the stale owner record would name
// a pid that is about to exit and may be reused. Removal failure is
// reported — a finalized run that still advertises a live owner is exactly
// the confusion this file exists to end.
func Finalize(p Paths, f Finalized) error {
	f.Schema = SuperviseSchema
	if f.FinishedAt.IsZero() {
		return fmt.Errorf("runs: finalized record needs a finishedAt")
	}
	if err := writeJSONFile(p.finalizedPath(), f); err != nil {
		return err
	}
	if err := os.Remove(p.runningPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("runs: finalized %s but could not clear %s: %w", f.RunID, RunningFile, err)
	}
	return nil
}

// writeJSONFile writes via a temp file and rename, so a reader never sees a
// half-written sentinel. os.WriteFile truncates first and can leave a
// zero-length file if the process dies mid-write — for a sentinel whose
// whole job is to be trustworthy after a crash, that failure mode is the
// one that matters most.
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// ReadRunning loads the owner record. A missing file is (nil, nil): "no
// owner recorded" is a legitimate state (an older build, or a finalized
// run), not an error.
func ReadRunning(p Paths) (*Running, error) {
	b, err := os.ReadFile(p.runningPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Running
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("runs: %s: %w", RunningFile, err)
	}
	if r.Schema != SuperviseSchema {
		return nil, fmt.Errorf("runs: %s schema %q, want %q", RunningFile, r.Schema, SuperviseSchema)
	}
	return &r, nil
}

// ReadFinalized loads the completion record. A missing file is (nil, nil) —
// an unfinished run is the normal case this whole package is about.
func ReadFinalized(p Paths) (*Finalized, error) {
	b, err := os.ReadFile(p.finalizedPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f Finalized
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("runs: %s: %w", FinalizedFile, err)
	}
	if f.Schema != SuperviseSchema {
		return nil, fmt.Errorf("runs: %s schema %q, want %q", FinalizedFile, f.Schema, SuperviseSchema)
	}
	return &f, nil
}

// runIDTime recovers the start time from a timestamp-first run id (see
// NewRunID). This is the age source when no owner was recorded, and it is
// preferred over the directory's mtime: mtime moves every time anything is
// appended, so a long-dead run whose last write was recent would read as
// young — the permissive answer again.
func runIDTime(runID string) (time.Time, bool) {
	stamp := runID
	if i := strings.IndexByte(stamp, '-'); i >= 0 {
		stamp = stamp[:i]
	}
	t, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Status classifies one run directory. It touches the filesystem and the
// process table, never the network — `retrace check` layers the marker-door
// probe on top, because a live pid proves a process exists, not that it is
// still OUR process (pids are reused).
//
// The rules, in order:
//
//   - finalized present            -> complete
//   - owner recorded, pid alive    -> running
//   - owner recorded, pid gone     -> abandoned
//   - no owner, age > abandonAfter -> abandoned
//   - no owner, age <= abandonAfter -> running (too young to judge)
//
// Owner liveness comes first because it is the only rule that stays correct
// for a suite that legitimately runs longer than any fixed bound. An
// age-only rule would label a 40-minute e2e capture abandoned while it was
// still writing, and a supervision signal with false positives gets
// switched off.
func Status(p Paths, app, flow, runID string, now time.Time, abandonAfter time.Duration) (RunStatus, error) {
	if abandonAfter <= 0 {
		abandonAfter = DefaultAbandonAfter
	}
	st := RunStatus{App: app, Flow: flow, RunID: runID}

	done, err := ReadFinalized(p)
	if err != nil {
		return st, err
	}
	owner, ownerErr := ReadRunning(p)
	// A corrupt or wrong-schema running.json is NOT fatal: it is one more
	// way of having no owner, and the age fallback exists precisely for
	// directories whose owner cannot be read. Reporting it in Reason keeps
	// it from being silent.
	corruptOwner := ownerErr != nil

	// StartedAt: the owner's own stamp when we have it, else the run id.
	switch {
	case owner != nil && !owner.StartedAt.IsZero():
		st.StartedAt = owner.StartedAt
	default:
		if t, ok := runIDTime(runID); ok {
			st.StartedAt = t
		}
	}
	if !st.StartedAt.IsZero() {
		st.AgeSeconds = int(now.Sub(st.StartedAt) / time.Second)
	}
	st.Owner = owner
	st.Done = done

	switch {
	case done != nil:
		st.State = StateComplete
		st.Reason = FinalizedFile + " present"
	case owner != nil && processAlive(owner.PID):
		st.State = StateRunning
		st.Reason = fmt.Sprintf("owner pid %d is alive", owner.PID)
	case owner != nil:
		st.State = StateAbandoned
		st.Reason = fmt.Sprintf("owner pid %d is gone and no %s was written", owner.PID, FinalizedFile)
	case st.StartedAt.IsZero():
		// No owner and an unparseable run id: nothing to age against. Say
		// so rather than picking a state — an unauditable guess here is
		// worse than an honest "cannot tell", and the caller can still see
		// the missing sentinel.
		st.State = StateAbandoned
		st.Reason = "no " + FinalizedFile + ", no owner recorded, and the run id carries no timestamp to age against"
	case now.Sub(st.StartedAt) > abandonAfter:
		st.State = StateAbandoned
		st.Reason = fmt.Sprintf("no owner recorded and no %s after %s", FinalizedFile, now.Sub(st.StartedAt).Round(time.Second))
	default:
		st.State = StateRunning
		st.Reason = fmt.Sprintf("no owner recorded, but only %s old", now.Sub(st.StartedAt).Round(time.Second))
	}
	if corruptOwner {
		st.Reason += fmt.Sprintf(" (%s unreadable: %v)", RunningFile, ownerErr)
	}
	return st, nil
}

// StatusAll classifies every run under root, newest last (the lexical order
// ListRuns already guarantees is chronological). A directory that cannot be
// classified is reported with the error rather than dropped: a supervision
// listing that silently omits the one broken directory is worse than no
// listing.
func StatusAll(root string, now time.Time, abandonAfter time.Duration) ([]RunStatus, error) {
	apps, err := ListAppsErr(root)
	if err != nil {
		return nil, err
	}
	var out []RunStatus
	for _, app := range apps {
		flows, err := ListFlowsErr(root, app)
		if err != nil {
			return nil, err
		}
		for _, flow := range flows {
			ids, err := ListRunsErr(root, app, flow)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				p, perr := PathsFor(root, app, flow, id)
				if perr != nil {
					out = append(out, RunStatus{
						App: app, Flow: flow, RunID: id,
						State:  StateAbandoned,
						Reason: "unreadable run directory: " + perr.Error(),
					})
					continue
				}
				st, serr := Status(p, app, flow, id, now, abandonAfter)
				if serr != nil {
					st.App, st.Flow, st.RunID = app, flow, id
					st.State = StateAbandoned
					st.Reason = "unreadable run directory: " + serr.Error()
				}
				out = append(out, st)
			}
		}
	}
	return out, nil
}
