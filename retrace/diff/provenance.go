package diff

import "github.com/caribou-crew/ensemble/retrace/runs"

// ComparisonProvenance records what retrace resolved for this comparison.
// It is optional because Summary is also built by consumers that already own
// their inputs (serve, export, and reference rejection) and have no selector
// invocation to report.
type ComparisonProvenance struct {
	A                SideProvenance             `json:"a"`
	B                SideProvenance             `json:"b"`
	ComparisonConfig ComparisonConfigProvenance `json:"comparisonConfig"`
}

// SideProvenance names the selected recording and the repository root in
// which selector resolution actually found it. RecordedGit is copied from the
// selected manifest; it is evidence captured with that run, not the current
// checkout's git state.
type SideProvenance struct {
	Root        string   `json:"root"`
	Selector    string   `json:"selector"`
	Kind        string   `json:"kind"`
	App         string   `json:"app"`
	Flow        string   `json:"flow"`
	RunID       string   `json:"runId"`
	RecordedGit runs.Git `json:"recordedGit"`
}

// ComparisonConfigProvenance describes the current policy used to evaluate
// the two recordings. Scope is always "comparison-time" so this block cannot
// be mistaken for historical capture configuration.
type ComparisonConfigProvenance struct {
	Scope       string `json:"scope"`
	Dir         string `json:"dir"`
	Fingerprint string `json:"fingerprint"`
}
