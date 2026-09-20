// Package suites validates and aggregates external runner assertions against a
// versioned expected inventory. It never infers a comparison from artifacts.
package suites

const InventorySchema = "retrace/suites/1"
const AttemptSchema = "retrace/suite-attempt/1"
const InventoryFile = "retrace.suites.json"

type Inventory struct {
	Schema string  `json:"schema"`
	Suites []Suite `json:"suites"`
}
type Suite struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Version   string    `json:"version"`
	Platforms []string  `json:"platforms"`
	Features  []Feature `json:"features"`
}
type Feature struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Flows []Flow `json:"flows"`
}
type Flow struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Platforms      []string `json:"platforms,omitempty"`
	RequiredPlanes []string `json:"requiredPlanes"`
}
type Git struct {
	SHA    string `json:"sha"`
	Branch string `json:"branch"`
	Dirty  bool   `json:"dirty"`
}
type Planes struct {
	Functional string `json:"functional"`
	Wire       string `json:"wire"`
	Visual     string `json:"visual"`
}

func (p Planes) State(plane string) string {
	switch plane {
	case "functional":
		return p.Functional
	case "wire":
		return p.Wire
	case "visual":
		return p.Visual
	}
	return ""
}

type Evidence struct {
	App    string `json:"app"`
	Flow   string `json:"flow"`
	RunID  string `json:"runId"`
	PairID string `json:"pairId,omitempty"`
}
type Result struct {
	FlowID   string    `json:"flowId"`
	Planes   Planes    `json:"planes"`
	Reason   string    `json:"reason,omitempty"`
	Evidence *Evidence `json:"evidence,omitempty"`
}
type Attempt struct {
	Schema       string   `json:"schema"`
	SuiteID      string   `json:"suiteId"`
	SuiteVersion string   `json:"suiteVersion"`
	AttemptID    string   `json:"attemptId"`
	Platform     string   `json:"platform"`
	Git          Git      `json:"git"`
	WorkspaceID  string   `json:"workspaceId,omitempty"`
	BaselineID   string   `json:"baselineId"`
	PolicyID     string   `json:"policyId"`
	StartedAt    string   `json:"startedAt"`
	FinishedAt   string   `json:"finishedAt"`
	Results      []Result `json:"results"`
}
type Response struct {
	Suites []SuiteOverview `json:"suites"`
}
type SuiteOverview struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Version   string       `json:"version"`
	Platforms []string     `json:"platforms"`
	Builds    []SuiteBuild `json:"builds"`
}
type SuiteBuild struct {
	ID          string            `json:"id"`
	Git         Git               `json:"git"`
	WorkspaceID string            `json:"workspaceId,omitempty"`
	BaselineID  string            `json:"baselineId"`
	PolicyID    string            `json:"policyId"`
	UpdatedAt   string            `json:"updatedAt"`
	Counts      Counts            `json:"counts"`
	Platforms   []PlatformSummary `json:"platforms"`
	Features    []FeatureSummary  `json:"features"`
}
type Counts struct {
	Total      int `json:"total"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Incomplete int `json:"incomplete"`
	NotRun     int `json:"notRun"`
}
type PlatformSummary struct {
	Platform string `json:"platform"`
	Counts   Counts `json:"counts"`
}
type FeatureSummary struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Counts    Counts            `json:"counts"`
	Platforms []PlatformSummary `json:"platforms"`
	Flows     []FlowSummary     `json:"flows"`
}
type FlowSummary struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Platforms []FlowCell `json:"platforms"`
}
type FlowCell struct {
	Platform       string          `json:"platform"`
	Status         string          `json:"status"`
	RequiredPlanes []string        `json:"requiredPlanes"`
	Latest         *AttemptResult  `json:"latest,omitempty"`
	History        []AttemptResult `json:"history"`
}
type AttemptResult struct {
	AttemptID  string    `json:"attemptId"`
	StartedAt  string    `json:"startedAt"`
	FinishedAt string    `json:"finishedAt"`
	Planes     Planes    `json:"planes"`
	Reason     string    `json:"reason,omitempty"`
	Evidence   *Evidence `json:"evidence,omitempty"`
}
