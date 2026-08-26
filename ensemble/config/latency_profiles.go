package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// LatencyProfileFile is the parsed contents of a latency.profiles.<name>.file
// — a named, file-backed set of latency rules applied together by
// `ensemble latency apply <name>`. Mirrors ReadinessChecksFile's shape.
type LatencyProfileFile struct {
	Rules []LatencyProfileRule `yaml:"rules"`
}

// LatencyProfileRule is one rule in a latency profile: either FromDatadog
// (resolved by querying Datadog at apply time) or FixedMs (applied
// literally) — exactly one of the two, enforced by Config.Validate.
type LatencyProfileRule struct {
	Target      string              `yaml:"target"`
	Path        string              `yaml:"path"`
	FromDatadog *LatencyFromDatadog `yaml:"from_datadog"`
	FixedMs     float64             `yaml:"fixed_ms"`
}

// LatencyFromDatadog is a rule's Datadog percentile query: Query SHALL
// contain the literal substring "{P}", substituted with "50"/"95"/"99" to
// produce three separate Datadog queries (see design.md's "three queries
// per rule" decision — Datadog's percentile aggregations aren't available
// from one call). WindowMinutes, when 0, falls back to
// Config.DatadogDefaultWindowMinutes.
type LatencyFromDatadog struct {
	Query         string `yaml:"query"`
	WindowMinutes int    `yaml:"window_minutes"`
}

// HasSource reports whether r declares exactly one of FromDatadog/FixedMs
// — the shape Config.Validate requires of every profile rule.
func (r LatencyProfileRule) HasExactlyOneSource() bool {
	hasDatadog := r.FromDatadog != nil
	hasFixed := r.FixedMs != 0
	return hasDatadog != hasFixed // exactly one, not both, not neither
}

// LoadLatencyProfile reads and parses the file a LatencyProfile.File points
// at, resolved relative to dir (Config.Dir) unless File is absolute.
// Mirrors LoadReadinessChecks byte-for-byte in structure.
func LoadLatencyProfile(dir string, p LatencyProfile) (*LatencyProfileFile, error) {
	path := p.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("latency profile: read %s: %w", path, err)
	}
	var f LatencyProfileFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("latency profile: parse %s: %w", path, err)
	}
	return &f, nil
}

// LatencyProfile returns the parsed rules file for the named latency
// profile — cached from Validate — or nil if name isn't a configured
// profile (or validation failed).
func (c *Config) LatencyProfile(name string) *LatencyProfileFile {
	return c.latencyProfiles[name]
}

// LatencyProfileNames returns every configured latency profile name,
// sorted.
func (c *Config) LatencyProfileNames() []string {
	names := make([]string, 0, len(c.Latency.Profiles))
	for name := range c.Latency.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
