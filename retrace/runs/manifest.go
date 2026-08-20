package runs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// Capture modes. A reader must be able to tell a reduced client-edge-only
// capture from a full-chain one WITHOUT inferring it from an empty
// hops.jsonl — an empty chain and an unrecorded chain are different facts.
const (
	ModeEnsemble   = "ensemble"
	ModeStandalone = "standalone"
)

// Manifest is the versioned index of one run directory.
type Manifest struct {
	Schema      string       `json:"schema"`
	App         string       `json:"app"`
	Flow        string       `json:"flow"`
	RunID       string       `json:"runId"`
	Mode        string       `json:"mode"`
	Git         Git          `json:"git"`
	StartedAt   time.Time    `json:"startedAt"`
	FinishedAt  time.Time    `json:"finishedAt"`
	Checkpoints []Checkpoint `json:"checkpoints"`
	// Groups is the flow-part timeline, folded from groups.jsonl by
	// Task 4's run body at manifest time. Task 10 loads it from BOTH
	// manifests and feeds diff.Options.GroupsA/GroupsB, which is what
	// gives the wire diff its named sections.
	//
	// Never omitempty, and always defaulted to []Group{} by WriteManifest
	// when nil: DeriveGroups cannot produce an empty-but-meaningfully-
	// different result from "no markers were ever placed" (zero records in
	// means zero groups out, always), so unlike Capture there is nothing to
	// gain from a third "key absent" state. Matching Checkpoints' shape
	// gives every sibling "list of things" field on Manifest one encoding
	// of empty instead of three.
	Groups []Group `json:"groups"`
	// Capture is never omitted: "no verdict recorded" and "verdict ok" must
	// not serialize the same way, or a broken capture reads as a clean one.
	Capture CaptureTrust `json:"capture"`
	Wire    Counts       `json:"wire"`
	// Hops is nil in standalone mode — see ModeStandalone. Present-but-zero
	// means the chain was recorded and was empty.
	Hops *Counts `json:"hops,omitempty"`
	Test Test    `json:"test"`
	Env  Env     `json:"env"`
}

type Git struct {
	SHA    string `json:"sha"`
	Branch string `json:"branch"`
	Dirty  bool   `json:"dirty"`
}

type Checkpoint struct {
	Name string `json:"name"`
	File string `json:"file"` // run-dir-relative, e.g. "shots/cart.png"
	// Width and Height are the shot's REAL geometry, always pre-trim. A
	// checkpoint that asked for border trimming still reports what was
	// captured; trimming is a compare-time decision (Tasks 7 and 10) and
	// the rect actually used is reported there, per checkpoint pair.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Trim records that a `<name>.trim` marker sat beside the shot — the
	// adapter asked for uniform-border trimming at compare time. Reading
	// the marker here, rather than in the pixel engine, is what keeps
	// `capture` from importing `pixel`: capture records a fact, compare
	// acts on it.
	Trim bool `json:"trim,omitempty"`
}

type Counts struct {
	Calls int `json:"calls"`
}

type Test struct {
	Command    string  `json:"command"`
	ExitCode   int     `json:"exitCode"`
	DurationMs float64 `json:"durationMs"`
}

type Env struct {
	Go       string `json:"go"`
	Platform string `json:"platform"`
	Retrace  string `json:"retrace"`
}

// CaptureTrust is the capture-trust verdict every report surface banners.
// The types live here (not in retrace/capture) because the manifest carries
// them and the assessor reads Group — the other direction would be a cycle.
type CaptureTrust struct {
	Status  trace.Verdict `json:"status"`
	Reasons []TrustReason `json:"reasons,omitempty"`
	Gaps    []Gap         `json:"gaps,omitempty"`
	Summary string        `json:"summary"`
	Hint    string        `json:"hint,omitempty"`
}

type TrustReason struct {
	Code   string        `json:"code"`
	Status trace.Verdict `json:"status"`
	Detail string        `json:"detail"`
	Hint   string        `json:"hint,omitempty"`
}

type Gap struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Seconds int       `json:"seconds"`
}

// WriteManifest stamps and normalizes m before writing manifest.json:
//
//   - Schema is always overwritten with the current constant.
//   - Capture.Status must not be the zero value: an empty trace.Verdict
//     ranks equal to trace.VerdictOK in Verdict.Worse (verdictRank has no
//     entry for "", so the map lookup yields 0 — same as VerdictOK), which
//     would make an unassessed capture gate as clean. A caller that hasn't
//     run trust assessment yet must pass an explicit verdict (e.g.
//     trace.VerdictFailed), not a zero CaptureTrust.
//   - Checkpoints and Groups are defaulted from nil to an empty slice, so
//     "no items" always serializes as [] and never as null.
//
// m is a pointer so the caller's own copy also carries the stamped Schema
// and defaulted slices after a successful write, not just the file on
// disk.
func WriteManifest(p Paths, m *Manifest) error {
	m.Schema = Schema
	if m.Capture.Status == "" {
		return fmt.Errorf("runs: manifest capture status must not be empty — pass an explicit trace.Verdict")
	}
	if m.Checkpoints == nil {
		m.Checkpoints = []Checkpoint{}
	}
	if m.Groups == nil {
		m.Groups = []Group{}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.ManifestPath, append(b, '\n'), 0o644)
}

// ReadManifest reads and validates a manifest.json. A schema mismatch is
// rejected rather than silently unmarshalled: encoding/json zeroes fields
// it doesn't recognize, and a zeroed CaptureTrust would gate as clean (see
// WriteManifest) — a version check that is written but never read implies
// a safety that doesn't exist.
//
// Capture.Status is checked here too, with the same message shape as
// WriteManifest's check (re-review residual on finding 1): WriteManifest
// only guards manifests THIS build wrote. A manifest written by an older
// build, or hand-edited (reference bundles are committed to git and
// therefore hand-editable), can still carry `"capture":{"status":""}` —
// and per the same zero-value trap, that reads as VerdictOK-equivalent to
// any caller that doesn't re-check. Rejecting it on read makes "a manifest
// this package will hand back always has an assessed capture verdict" an
// invariant of ReadManifest, not just of WriteManifest.
func ReadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, err
	}
	if m.Schema != Schema {
		return Manifest{}, fmt.Errorf("runs: manifest schema %q, want %q", m.Schema, Schema)
	}
	if m.Capture.Status == "" {
		return Manifest{}, fmt.Errorf("runs: manifest capture status must not be empty — pass an explicit trace.Verdict")
	}
	return m, nil
}

// ReadHops loads an NDJSON hop file, unmarshalling each line into
// core/trace's Hop type — retrace never defines a parallel record type for
// captured traffic (see global-constraints.md: "one hop schema"). A
// missing file is (nil, 0, nil): a standalone run legitimately has no
// hops.jsonl.
//
// Fail-open policy, shared with ReadGroupRecords (groups.go) — write it
// once, here, so the next reader sees one rule instead of two behaviors: a
// corrupt line (a half-written record from a killed test process) is
// skipped and counted, never discarding hops already parsed on either side
// of it. A real I/O error reading the file is still surfaced, alongside
// whatever was already parsed.
//
// A line that is valid JSON but not a hop (e.g. "{}", or any object
// missing the schema stamp trace.Writer always sets) unmarshals without
// error into a zero-value trace.Hop — json.Unmarshal never complains about
// absent fields. Left unchecked that inflates the hop count with phantom
// records instead of surfacing the corruption (re-review residual on
// finding 4). Checking Schema == trace.SchemaVersion after a successful
// unmarshal, and counting a mismatch into skipped like any other corrupt
// line, is the cheapest correct signal: every hop this package's own
// writers produce carries it (trace.Writer stamps it unconditionally), so
// its absence is exactly "not actually a hop record".
func ReadHops(path string) (hops []trace.Hop, skipped int, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for s.Scan() {
		line := bytes.TrimSpace(s.Bytes())
		if len(line) == 0 {
			continue
		}
		var h trace.Hop
		if uerr := json.Unmarshal(line, &h); uerr != nil {
			skipped++
			continue
		}
		if h.Schema != trace.SchemaVersion {
			skipped++
			continue
		}
		hops = append(hops, h)
	}
	return hops, skipped, s.Err()
}
