package suites

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// buildIdentity excludes branch: two jobs on different branch aliases still
// compare the same immutable source, baseline and policy. Dirty snapshots and
// every inventory revision remain separate. JSON avoids delimiter collisions.
type buildIdentity struct {
	SuiteID      string
	SuiteVersion string
	SHA          string
	Dirty        bool
	WorkspaceID  string
	BaselineID   string
	PolicyID     string
}

func identity(a Attempt) string {
	b, _ := json.Marshal(buildIdentity{a.SuiteID, a.SuiteVersion, strings.ToLower(a.Git.SHA), a.Git.Dirty, a.WorkspaceID, a.BaselineID, a.PolicyID})
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}
func parsedTime(value string) time.Time { t, _ := time.Parse(time.RFC3339, value); return t }
func newer(a, b Attempt) bool {
	at, bt := parsedTime(a.FinishedAt), parsedTime(b.FinishedAt)
	if at.Equal(bt) {
		return a.AttemptID > b.AttemptID
	}
	return at.After(bt)
}
func cellStatus(required []string, planes Planes) string {
	if len(required) == 0 {
		return "incomplete"
	}
	status := "pass"
	for _, p := range required {
		if planes.State(p) == "failed" {
			return "failed"
		}
		if planes.State(p) != "pass" {
			status = "incomplete"
		}
	}
	return status
}
func (c *Counts) add(status string) {
	c.Total++
	switch status {
	case "pass":
		c.Passed++
	case "failed":
		c.Failed++
	case "incomplete":
		c.Incomplete++
	default:
		c.NotRun++
	}
}
func platformSummaries(platforms []string) []PlatformSummary {
	out := make([]PlatformSummary, 0, len(platforms))
	for _, p := range platforms {
		out = append(out, PlatformSummary{Platform: p})
	}
	return out
}
func addPlatform(summaries []PlatformSummary, platform, status string) {
	for i := range summaries {
		if summaries[i].Platform == platform {
			summaries[i].Counts.add(status)
			return
		}
	}
}

// Aggregate is pure: its only inputs are inventory and runner attempts. It
// validates every attempt before exposing any aggregate, even unselected retries.
// A failed read or invalid historical report can therefore never disappear into
// a clean dashboard. All response arrays are non-nil for a stable JSON contract.
func Aggregate(inv Inventory, attempts []Attempt) ([]SuiteOverview, error) {
	if err := ValidateInventory(inv); err != nil {
		return nil, err
	}
	grouped := map[string][]Attempt{}
	seen := map[string]bool{}
	for _, a := range attempts {
		if err := validateAttempt(inv, a); err != nil {
			return nil, err
		}
		key := a.SuiteID + "/" + a.AttemptID
		if seen[key] {
			return nil, fmt.Errorf("suites: duplicate attempt %q", key)
		}
		seen[key] = true
		id := identity(a)
		grouped[id] = append(grouped[id], a)
	}
	out := make([]SuiteOverview, 0, len(inv.Suites))
	for _, suite := range inv.Suites {
		overview := SuiteOverview{ID: suite.ID, Title: suite.Title, Version: suite.Version, Platforms: append([]string{}, suite.Platforms...), Builds: []SuiteBuild{}}
		for id, group := range grouped {
			if group[0].SuiteID != suite.ID {
				continue
			}
			sort.Slice(group, func(i, j int) bool { return newer(group[i], group[j]) })
			latest := group[0]
			git := latest.Git
			git.SHA = strings.ToLower(git.SHA)
			build := SuiteBuild{ID: id, Git: git, WorkspaceID: latest.WorkspaceID, BaselineID: latest.BaselineID, PolicyID: latest.PolicyID, UpdatedAt: latest.FinishedAt, Platforms: platformSummaries(suite.Platforms), Features: []FeatureSummary{}}
			// Preserve complete per-cell history, ordered newest first. Reports that
			// omit this flow do not erase an earlier result for it.
			histories := map[string][]AttemptResult{}
			for _, a := range group {
				for _, r := range a.Results {
					key := a.Platform + "/" + r.FlowID
					var evidence *Evidence
					if r.Evidence != nil {
						copied := *r.Evidence
						evidence = &copied
					}
					histories[key] = append(histories[key], AttemptResult{a.AttemptID, a.StartedAt, a.FinishedAt, r.Planes, r.Reason, evidence})
				}
			}
			for _, feature := range suite.Features {
				fs := FeatureSummary{ID: feature.ID, Title: feature.Title, Platforms: platformSummaries(suite.Platforms), Flows: []FlowSummary{}}
				for _, flow := range feature.Flows {
					fl := FlowSummary{ID: flow.ID, Title: flow.Title, Platforms: []FlowCell{}}
					for _, platform := range flowPlatforms(suite, flow) {
						cell := FlowCell{Platform: platform, Status: "not-run", RequiredPlanes: append([]string{}, flow.RequiredPlanes...), History: []AttemptResult{}}
						if history := histories[platform+"/"+flow.ID]; len(history) > 0 {
							cell.History = history
							result := history[0]
							cell.Latest = &result
							cell.Status = cellStatus(flow.RequiredPlanes, result.Planes)
						}
						fl.Platforms = append(fl.Platforms, cell)
						fs.Counts.add(cell.Status)
						build.Counts.add(cell.Status)
						addPlatform(fs.Platforms, platform, cell.Status)
						addPlatform(build.Platforms, platform, cell.Status)
					}
					fs.Flows = append(fs.Flows, fl)
				}
				build.Features = append(build.Features, fs)
			}
			overview.Builds = append(overview.Builds, build)
		}
		sort.Slice(overview.Builds, func(i, j int) bool {
			a, b := overview.Builds[i], overview.Builds[j]
			at, bt := parsedTime(a.UpdatedAt), parsedTime(b.UpdatedAt)
			if at.Equal(bt) {
				return a.ID < b.ID
			}
			return at.After(bt)
		})
		out = append(out, overview)
	}
	return out, nil
}
