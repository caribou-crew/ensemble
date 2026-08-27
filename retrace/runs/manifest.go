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
	// Wire is never omitted, the same as Capture and unlike Hops: it always
	// has a key, and Counts.Recorded (not a nil pointer) is what says
	// whether the count inside it is real. See Counts below.
	Wire Counts `json:"wire"`
	// Hops is nil in standalone mode — see ModeStandalone — and that nil
	// pointer is the ONLY encoding of "not recorded" for this field.
	// Present-but-zero (&Counts{Recorded:true}) means the chain was recorded
	// and was empty. A non-nil Hops must always have Recorded true:
	// WriteManifest and ReadManifest both reject Hops != nil && !Hops.Recorded,
	// because Counts.Recorded exists for Wire, which has no pointer to be
	// nil — giving Hops a second "absent" spelling would let the impossible
	// state hops:{calls:40,recorded:false} reach disk, and omitempty on a
	// pointer means a non-nil-but-unrecorded Counts would still emit the
	// key, so the two encodings would not even be mutually exclusive.
	Hops *Counts `json:"hops,omitempty"`
	Test Test    `json:"test"`
	Env  Env     `json:"env"`
	// Device is the screen the shots in this run were taken on. A pointer,
	// and nil when the run captured no shots and the adapter wrote no
	// device.json: a zero Device is 0×0, which is not "unknown geometry" but
	// a CLAIM of one — and two runs that both failed to record a device would
	// compare 0×0 against 0×0 and agree.
	//
	// Distinct from the per-checkpoint Width/Height above, and both are
	// needed. A checkpoint's geometry is one shot's; Device is the screen the
	// whole run was captured on, which is what makes two RUNS comparable at
	// all. A pair captured at different device sizes produces a per-checkpoint
	// diff percentage for every checkpoint, and every one of those numbers is
	// meaningless — the guard belongs at run scale, where it can refuse once
	// rather than mislead many times.
	Device *Device `json:"device,omitempty"`
	// Stack is what the backend was when this run was recorded. Nil in
	// standalone mode and against any control plane too old to report it —
	// the same absence-is-not-evidence encoding Device uses, and for the same
	// reason: a zero Stack is an empty service map, which compares equal to
	// every other run that failed to record one.
	Stack *Stack `json:"stack,omitempty"`
}

// Device describes the screen a run's shots were taken on, read from a
// `device.json` the adapter writes into the run directory.
type Device struct {
	// Kind says where the geometry came from, and is what stops two
	// incomparable facts from being compared as one. "browser" is a viewport
	// the test framework set; "device" is a real or emulated handset the
	// adapter was told about; "shot" is the fallback, inferred from the first
	// screenshot when no adapter reported anything.
	Kind string `json:"kind"` // "browser" | "device" | "shot"
	// ID names the device or browser profile when the adapter knows it
	// ("iPhone 15 Pro", "Pixel_7_API_34"). Advisory: geometry decides
	// comparability, and an id is what makes a mismatch message legible.
	ID     string `json:"id,omitempty"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// Scale is the device-pixel ratio when the adapter reports one. Optional
	// and NOT part of the comparability test: at equal width and height, two
	// runs at different scales produce shots of different pixel dimensions,
	// which the per-checkpoint size check already catches with a far more
	// precise message. Recorded because it explains that message.
	Scale float64 `json:"scale,omitempty"`
}

// SameScreen reports whether two runs were captured on the same geometry.
//
// nil is never "the same as" anything, including another nil: an unknown
// geometry compared against an unknown geometry is two unknowns, not a match.
// Returning true there would make the guard vanish exactly when neither side
// recorded a device — the case with the least evidence in it.
//
// Kind is deliberately NOT compared. The same 390×844 viewport reported by
// Playwright and inferred from a shot is the same screen, and refusing that
// pair would fail every run captured before the adapter learned to write a
// device.json against every run captured after.
func SameScreen(a, b *Device) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Width == b.Width && a.Height == b.Height
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

// Counts reports how many calls were recorded on one plane (currently
// Manifest.Wire; Manifest.Hops uses *Counts instead — see its doc comment).
// Recorded and Calls draw the same "absent vs empty" distinction Hops draws
// with a nil pointer: Recorded true with Calls 0 means "recorded, and there
// were none" — a real, clean fact. Recorded false means "not recorded",
// with Reason saying why, and a diff must refuse to compare against it
// rather than silently treating it as zero calls.
//
// The zero value, Counts{}, is deliberately Recorded:false — the
// protective reading, not the permissive one. Any code path that forgets
// to set Wire (or a Hops it allocates) therefore asserts "unknown, refuse"
// for free, rather than asserting a clean wire plane it never actually
// recorded. (An earlier `Missing bool` encoding had this backwards: its
// zero value, Missing:false, asserted "recorded and clean" — the
// permissive reading the global zero-value constraint forbids.) Recorded
// is therefore never omitempty — a bool that disappears when false is
// exactly how "absent" and "fine" end up as the same bytes on disk, which
// is the trap Capture's doc comment also warns about.
type Counts struct {
	Calls    int    `json:"calls"`
	Recorded bool   `json:"recorded"`
	Reason   string `json:"reason,omitempty"`
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

// validateCounts enforces, for one Counts-typed manifest field, the same
// "absence must not silently read as fine" invariant WriteManifest already
// enforces on Capture.Status: a plane that was not recorded must say so
// explicitly (Recorded false) AND explain why (Reason set), and must not
// simultaneously claim a nonzero Calls — "not recorded" with 40 calls is a
// self-contradiction, not a state this schema allows onto disk.
func validateCounts(field string, c Counts) error {
	if c.Recorded {
		return nil
	}
	if c.Calls != 0 {
		return fmt.Errorf("runs: manifest %s.calls must be 0 when recorded is false (got %d) — a plane cannot claim to be unrecorded and also report calls", field, c.Calls)
	}
	if c.Reason == "" {
		return fmt.Errorf("runs: manifest %s.recorded is false but reason is empty — explain why %s was not recorded", field, field)
	}
	return nil
}

// validateHops enforces the Hops-specific half of the same invariant: for
// Hops, "not recorded" has exactly one encoding — the nil pointer (see
// Manifest.Hops's doc comment) — so a non-nil Hops claiming Recorded false
// is a second, contradictory spelling of "absent" that must never reach
// disk.
func validateHops(hops *Counts) error {
	if hops == nil {
		return nil
	}
	if !hops.Recorded {
		return fmt.Errorf("runs: manifest hops is present but recorded is false — for hops, absence is the nil pointer (omit hops entirely), not recorded:false")
	}
	return validateCounts("hops", *hops)
}

// validateDevice refuses a device whose geometry is not a real screen.
//
// A 0 or negative dimension is the whole reason Device is a pointer: nil
// means "no device was recorded" and every consumer treats that as unknown,
// but a present device.json reading 0×0 is a CLAIM — and two runs that both
// wrote one would compare 0×0 against 0×0 and agree they were captured on
// the same screen. The guard has to refuse the value at the door, because
// downstream it is indistinguishable from a fact.
//
// Kind is required for the same reason it is recorded: "" is not one of the
// three, and a device whose provenance is blank cannot be explained in the
// mismatch message that is this feature's entire output.
func validateDevice(d *Device) error {
	if d == nil {
		return nil
	}
	if d.Kind == "" {
		return fmt.Errorf("runs: manifest device has no kind — want one of browser, device, shot")
	}
	if d.Width <= 0 || d.Height <= 0 {
		return fmt.Errorf("runs: manifest device geometry is %dx%d — a device that is present must report a real screen, or it will compare equal to another broken one", d.Width, d.Height)
	}
	return nil
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
//   - Wire and, if non-nil, Hops must not claim absence (Recorded false)
//     while also reporting a nonzero Calls, and must carry a Reason when
//     absence IS claimed — see validateCounts. A non-nil Hops must never
//     claim absence at all — see validateHops and Manifest.Hops's doc
//     comment.
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
	if err := validateCounts("wire", m.Wire); err != nil {
		return err
	}
	if err := validateHops(m.Hops); err != nil {
		return err
	}
	if err := validateDevice(m.Device); err != nil {
		return err
	}
	if err := validateStack(m.Stack); err != nil {
		return err
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
//
// path is a bare string, not a Paths, by ruling (round-3 re-review): the
// guard from finding 2 lives at the construction seam — a function that
// JOINS a caller-supplied component into a path must validate that
// component; a function handed a fully-formed path the caller already
// resolved does not re-litigate it. ReadManifest joins nothing. The caller
// owns path and is responsible for having constructed it through PathsFor
// (e.g. p.ManifestPath) — forcing a Paths parameter here would mean some
// callers (Task 10's repro-bundle reader, Task 11's reference-bundle
// reader under .retrace-ref/) fabricating a Paths that never came from
// PathsFor, which is strictly worse than an honest string.
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
	// Wire/Hops are re-checked here for the same reason Capture.Status is,
	// immediately above: WriteManifest only guards manifests THIS build
	// wrote. An older build, or a hand-edited reference bundle, can still
	// carry the self-contradictory hops:{calls:40,recorded:false}. Checked
	// in the same order as WriteManifest so both functions reject the same
	// manifest for the same reason first.
	if err := validateCounts("wire", m.Wire); err != nil {
		return Manifest{}, err
	}
	if err := validateHops(m.Hops); err != nil {
		return Manifest{}, err
	}
	// Re-checked on read for the same reason Wire and Hops are: a manifest
	// hand-edited, or written by a version that did not have this check, must
	// not reach a comparison carrying a geometry that compares equal to
	// another broken one.
	if err := validateDevice(m.Device); err != nil {
		return Manifest{}, err
	}
	if err := validateStack(m.Stack); err != nil {
		return Manifest{}, err
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
//
// path is a bare string, not a Paths, for the same reason as ReadManifest
// above (round-3 re-review ruling): ReadHops joins nothing, so there is no
// construction seam here for finding 2's guard to sit at. The caller owns
// path and is responsible for having constructed it through PathsFor (e.g.
// p.HopsPath/p.WirePath).
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
