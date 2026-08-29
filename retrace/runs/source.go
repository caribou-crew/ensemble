package runs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// SourceFile is the sidecar written beside manifest.json for a run that did
// not originate on this machine. Its ABSENCE is the encoding for "recorded
// locally" — see Source's own doc comment — so no writer of manifest.json
// (no adapter, no `retrace run`) needs to change for this file to exist at
// all; only `retrace sync` ever writes one.
const SourceFile = "source.json"

// SourceSchema versions this sidecar independently of the manifest, the
// same reason SuperviseSchema versions running.json/finalized apart from
// it: a reader of source.json has no business depending on the manifest
// schema having been reached, and a future build must be able to tell an
// old source.json from a new one without also parsing a manifest.
const SourceSchema = "retrace/source/1"

// SourceKind is the only two-valued distinction Source makes. It is a
// string, not a bool, so a THIRD origin (the S3 backend the design defers)
// costs no encoding change later.
const (
	SourceKindCI = "ci"
)

// Source records where a run directory came from, when it did not come
// from a `retrace run` on this machine.
//
// It is read by the review queue and the dashboard ONLY. diff.Build never
// imports this package's Source type and never will: where a run was
// recorded has no bearing on whether it matches its reference, and folding
// it into Manifest would drag provenance into the zero-value contract
// Hops/Device/Stack already carry — none of which this file needs to
// participate in. A run with no source.json is unambiguously local; this
// type has no "local" value because "absent" already means that, and a
// synced run and a hand-written test both get exactly one way to say so.
type Source struct {
	Schema string `json:"schema"`
	// Kind is currently always SourceKindCI: source.json is only ever
	// written by `retrace sync`, and sync has one backend today.
	Kind string `json:"kind"`
	// Workflow is the GitHub Actions workflow name the run was recorded
	// under, e.g. "retrace-ios".
	Workflow string `json:"workflow"`
	// RunURL is the GitHub Actions run's web URL, so a reviewer can jump
	// straight from a dashboard row to the CI log that produced it.
	RunURL string `json:"runUrl"`
	// SHA is the commit the run was recorded against. Independent of
	// Manifest.Git.SHA: that field is whatever `retrace run` captured on
	// the CI runner, and it should agree with this one, but this is the
	// value `gh run list` reported for the workflow run itself.
	SHA string `json:"sha"`
	// HeadBranch is the branch the run's workflow was triggered against
	// (gh run list's own "headBranch"), e.g. "main".
	HeadBranch string `json:"headBranch,omitempty"`
	// Event is what triggered the run, e.g. "push" or "schedule".
	Event string `json:"event,omitempty"`
	// Actor is the GitHub login who triggered the run. gh run list's own
	// JSON output has no actor field (retrace/sync fetches it separately
	// via `gh api`), so this may be empty for a run synced before this
	// field existed.
	Actor string `json:"actor,omitempty"`
	// SyncedAt is when `retrace sync` merged this run onto local disk —
	// not when CI recorded it, which is CaptureTrust/Manifest territory.
	SyncedAt time.Time `json:"syncedAt"`
}

func (p Paths) sourcePath() string { return filepath.Join(p.RunDir, SourceFile) }

// SourcePath exposes the sidecar location for callers outside this package
// (retrace/sync's merge step), matching RunningPath/FinalizedPath's own
// single-construction-seam rule.
func (p Paths) SourcePath() string { return p.sourcePath() }

// WriteSource stamps Schema and writes the sidecar, atomically (via the
// same temp-file-and-rename writeJSONFile every other sentinel in this
// package uses) so a reader never sees a half-written source.json.
func WriteSource(p Paths, s Source) error {
	if s.Kind == "" {
		return fmt.Errorf("runs: source record needs a kind — an empty kind is indistinguishable from a bug that forgot to set one")
	}
	s.Schema = SourceSchema
	return writeJSONFile(p.sourcePath(), s)
}

// ReadSource loads the sidecar. A missing file is (nil, nil): "no source
// recorded" is the ordinary case for every locally recorded run, not an
// error — mirroring ReadRunning's own contract for running.json.
func ReadSource(p Paths) (*Source, error) {
	b, err := os.ReadFile(p.sourcePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Source
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("runs: %s: %w", SourceFile, err)
	}
	if s.Schema != SourceSchema {
		return nil, fmt.Errorf("runs: %s schema %q, want %q", SourceFile, s.Schema, SourceSchema)
	}
	return &s, nil
}
