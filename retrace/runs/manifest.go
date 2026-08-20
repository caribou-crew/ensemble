package runs

import (
	"encoding/json"
	"errors"
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
	Groups []Group `json:"groups,omitempty"`
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

func WriteManifest(p Paths, m Manifest) error {
	m.Schema = Schema
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.ManifestPath, append(b, '\n'), 0o644)
}

func ReadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ReadHops loads an NDJSON hop file through core/trace's reader, so retrace
// never re-implements the schema's parsing. A missing file is (nil, nil):
// a standalone run legitimately has no hops.jsonl.
func ReadHops(path string) ([]trace.Hop, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := trace.NewReader(f)
	var out []trace.Hop
	for {
		h, err := r.Next()
		if errors.Is(err, trace.ErrEOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
}
