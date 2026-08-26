package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultReadinessTimeoutS and DefaultReadinessRetryIntervalS apply when
// Readiness.TimeoutS/RetryIntervalS are left unset (zero) in ensemble.yaml.
const (
	DefaultReadinessTimeoutS       = 60
	DefaultReadinessRetryIntervalS = 5
)

// Readiness points at a readiness checks file (see ReadinessChecksFile) and
// bounds how long the orchestrator retries its checks after on_ready
// completes — a stack-level "is this actually usable" gate, distinct from
// on_ready's own seed/postinstall step. See ensemble/orchestrator's
// readiness phase for how these fields are used.
type Readiness struct {
	// File is the path to the readiness checks file, relative to the
	// directory containing ensemble.yaml (Config.Dir) unless absolute.
	File string `yaml:"file"`
	// TimeoutS bounds the total time checks are retried before the stack
	// is declared not_ready. 0 (unset) uses DefaultReadinessTimeoutS.
	TimeoutS int `yaml:"timeout_s"`
	// RetryIntervalS is the delay between retries of a check that hasn't
	// yet passed. 0 (unset) uses DefaultReadinessRetryIntervalS.
	RetryIntervalS int `yaml:"retry_interval_s"`
}

// EffectiveTimeoutS returns TimeoutS, or DefaultReadinessTimeoutS when unset.
func (r Readiness) EffectiveTimeoutS() int {
	if r.TimeoutS > 0 {
		return r.TimeoutS
	}
	return DefaultReadinessTimeoutS
}

// EffectiveRetryIntervalS returns RetryIntervalS, or
// DefaultReadinessRetryIntervalS when unset.
func (r Readiness) EffectiveRetryIntervalS() int {
	if r.RetryIntervalS > 0 {
		return r.RetryIntervalS
	}
	return DefaultReadinessRetryIntervalS
}

// ReadinessChecksFile is the parsed contents of the file Readiness.File
// points at: a list of named HTTP assertions run once the stack is seeded.
type ReadinessChecksFile struct {
	Checks []ReadinessCheck `yaml:"checks"`
}

// ReadinessCheck is one named readiness assertion: a request against
// Service's native address (resolved via Config.RoutablePort, the same
// seam the gateway uses) at Path, optionally authenticated via
// HeadersFrom, asserted against Assert.
type ReadinessCheck struct {
	Name string `yaml:"name"`
	// Service names an existing service or stub — validated against
	// Config.RoutablePort at config-load time (see Validate).
	Service string `yaml:"service"`
	Path    string `yaml:"path"`
	// HeadersFrom, if set, is a script (relative to Config.Dir unless
	// absolute) executed once per attempt; each non-blank stdout line is
	// parsed as a "Header-Name: value" pair and attached to the request.
	// Keeps auth secrets out of the readiness checks file itself.
	HeadersFrom string          `yaml:"headers_from"`
	Assert      ReadinessAssert `yaml:"assert"`
}

// ReadinessAssert is a check's pass condition. Status, when set, must
// equal the response status. BodyJQ, when set, is evaluated against the
// JSON-parsed response body; the check passes only if the result is
// truthy (and Status, if also set, matches). At least one of the two
// should be set, or the check trivially passes on any response.
type ReadinessAssert struct {
	Status *int   `yaml:"status"`
	BodyJQ string `yaml:"body_jq"`
}

// LoadReadinessChecks reads and parses the file r.File points at, resolved
// relative to dir (Config.Dir) unless r.File is absolute.
func LoadReadinessChecks(dir string, r Readiness) (*ReadinessChecksFile, error) {
	path := r.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("readiness: read %s: %w", path, err)
	}
	var f ReadinessChecksFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("readiness: parse %s: %w", path, err)
	}
	return &f, nil
}

// ReadinessChecks returns the checks file parsed during Validate, or nil
// when no readiness: key is configured (or validation failed).
func (c *Config) ReadinessChecks() *ReadinessChecksFile {
	return c.readinessChecks
}
