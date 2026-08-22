package runs

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// GroupRecord is one flow-part marker as an adapter writes it. The writer is
// deliberately stateless — an `end` record carries no name, because a CLI or
// HTTP marker door is a fresh caller that cannot know what is open. Every
// sequencing rule lives in DeriveGroups instead.
type GroupRecord struct {
	Phase string    `json:"phase"` // "start" | "end"
	Name  string    `json:"name,omitempty"`
	TS    time.Time `json:"ts"`
	Quiet bool      `json:"quiet,omitempty"` // declared silence: suppresses gap suspicion
}

// ErrMarkerWithoutTimestamp is what AppendGroupRecord returns for a record
// whose TS is the zero time.
//
// A marker with no timestamp is NOT a marker at the beginning of time. It
// is the Zero-Value Constraint's first clause at its most expensive: TS is
// a bare time.Time, `runs.GroupRecord{Phase: "start", Name: "warmup", Quiet:
// true}` is one omitted field in ordinary Go, and a record that omits `ts`
// unmarshals cleanly to the same value. DeriveGroups sorts by TS, so such a
// record sorts first and opens a group at 0001-01-01 that closeAt runs
// forward to the run's finish — a DECLARED-SILENT interval covering all of
// history, which capture.FindGaps then subtracts from every gap in the run.
// A proxy that died for ten minutes mid-run goes from "suspect" to "ok",
// and "ok" is the one verdict diff's quarantine and capture.Fatal both let
// through.
//
// AppendGroupRecord is exported and groups.jsonl is a documented file-drop
// protocol (adapters/js/README.md), so the shipped adapters all writing a
// real timestamp is not a defence — it is the absence of one.
var ErrMarkerWithoutTimestamp = errors.New("runs: group marker has no ts — a marker with no timestamp is not a marker")

// timestamped is the ONE predicate all three seams below share. Written
// once because the alternative is three copies of `!r.TS.IsZero()` that can
// drift: the whole finding this guards against was born from a producer and
// a consumer disagreeing about what an absent value meant.
func (r GroupRecord) timestamped() bool { return !r.TS.IsZero() }

// Group is a derived half-open interval [StartedAt, EndedAt).
type Group struct {
	Name      string    `json:"name"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Quiet     bool      `json:"quiet,omitempty"`
}

// AppendGroupRecord and ReadGroupRecords take a Paths, not a bare runDir
// string, so the traversal guard is structural rather than documented: a
// Paths is only obtainable from PathsFor/Create, both of which validate
// app/flow/runID (review finding 2, re-review section 2 — the write side).
// The review named the old string signature as exactly the shape a Task 4
// implementer wiring RETRACE_RUN_DIR or a request field into a marker
// write would get no guard from, because nothing in it said "this must
// have come from PathsFor". A Paths{RunDir: ...} literal is technically
// still forgeable in Go; that is an accepted, documented residual (see
// Paths' doc comment) — the goal here is removing the accidental door, not
// making Paths unforgeable.
func AppendGroupRecord(p Paths, r GroupRecord) error {
	// The WRITE seam, refusing loudly, in the same spirit as
	// WriteManifest's rejection of an empty Capture.Status: a caller that
	// forgot the timestamp gets an error it must handle, not a marker that
	// quietly disables gap detection for the whole run. This is checked
	// before the directory is created so a rejected marker leaves nothing
	// behind.
	if !r.timestamped() {
		return ErrMarkerWithoutTimestamp
	}
	if err := os.MkdirAll(p.RunDir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(p.RunDir, "groups.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ReadGroupRecords tolerates corrupt lines: a half-written marker from a
// killed test process must not make the whole run unreadable.
//
// Fail-open policy, shared with ReadHops (manifest.go) — one rule for
// every NDJSON reader in this package, not two behaviors: skip and
// continue past a corrupt record rather than erroring the whole file,
// and never discard records already parsed on either side of it (see the
// `return out, skipped, s.Err()` below — a real scanner error still
// surfaces alongside whatever was already collected).
//
// A line with no `ts` is counted into skipped exactly like a corrupt one,
// and for exactly ReadHops' stated reason: it is valid JSON that is not a
// record. json.Unmarshal never complains about an absent field, so such a
// line arrives as a well-formed, declared-quiet marker at 0001-01-01 —
// see ErrMarkerWithoutTimestamp for what that does to the whole run. Being
// fail-open makes this WORSE than corruption rather than better: a corrupt
// line is dropped, a half-correct one is honoured.
//
// skipped is returned rather than logged here (this package writes nothing
// to stderr) and the caller is expected to say so — a dropped marker that
// silently restores gap detection is still a fact the operator's flow
// definition is wrong, and `retrace run` prints it. This is review finding
// 12, previously parked: the parking was acceptable while a dropped record
// could only cost a section name.
func ReadGroupRecords(p Paths) (records []GroupRecord, skipped int, err error) {
	f, err := os.Open(filepath.Join(p.RunDir, "groups.jsonl"))
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var out []GroupRecord
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var r GroupRecord
		if err := json.Unmarshal(line, &r); err != nil {
			skipped++
			continue
		}
		if !r.timestamped() {
			skipped++
			continue
		}
		out = append(out, r)
	}
	return out, skipped, s.Err()
}

// DeriveGroups folds markers into intervals in start order. A name may
// repeat. An unclosed group closes when the next one opens, or at
// finishedAt — a marker placed after the traffic it meant to bracket then
// shows as an empty part, which is exactly the symptom worth seeing.
func DeriveGroups(records []GroupRecord, finishedAt time.Time) []Group {
	// The third seam, and the last one that can see a zero TS: records
	// assembled in memory never pass through either of the other two. It
	// drops silently because it has no error channel and because the drop
	// is the SAFE direction — gap detection is restored, not disabled —
	// while the two seams that do have a channel (AppendGroupRecord's
	// error, ReadGroupRecords' skipped count) are the ones that report.
	//
	// Filtering before the sort, not inside the loop: an unfiltered zero TS
	// sorts FIRST, and the damage is entirely in where it sorts.
	sorted := make([]GroupRecord, 0, len(records))
	for _, r := range records {
		if !r.timestamped() {
			continue
		}
		sorted = append(sorted, r)
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })

	var out []Group
	var open *Group
	closeAt := func(ts time.Time) {
		if open != nil {
			open.EndedAt = ts
			out = append(out, *open)
			open = nil
		}
	}
	for _, r := range sorted {
		switch r.Phase {
		case "start":
			closeAt(r.TS)
			open = &Group{Name: r.Name, StartedAt: r.TS, Quiet: r.Quiet}
		case "end":
			closeAt(r.TS)
		}
	}
	closeAt(finishedAt)
	return out
}

// GroupAt returns the part a timestamp falls in, "" for none. Half-open, so
// a call made at the instant a part opens belongs to that part.
func GroupAt(groups []Group, ts time.Time) string {
	for _, g := range groups {
		if !ts.Before(g.StartedAt) && ts.Before(g.EndedAt) {
			return g.Name
		}
	}
	return ""
}

// GroupNames lists distinct part names in first-seen order.
func GroupNames(groups []Group) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		if !seen[g.Name] {
			seen[g.Name] = true
			out = append(out, g.Name)
		}
	}
	return out
}
