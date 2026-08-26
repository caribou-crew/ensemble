package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/caribou-crew/ensemble/core/proxy"
)

// latencyRulesPath returns where ensemble persists live latency rules so
// they survive `ensemble up` restarts — a peer to hopsPath under the same
// owner-only .ensemble/ directory.
func latencyRulesPath(cfgDir string) string {
	return filepath.Join(cfgDir, ".ensemble", "latency.json")
}

// loadLatencyRules reads a previously-persisted rule set. A missing file
// returns (nil, nil) — the sentinel a caller uses to tell "never persisted
// (fresh install)" apart from "persisted as empty" (json.Unmarshal leaves a
// non-nil, zero-length slice for a "[]" file, which round-trips as
// "explicitly empty").
func loadLatencyRules(path string) ([]proxy.LatencyRule, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var rules []proxy.LatencyRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if rules == nil {
		rules = []proxy.LatencyRule{}
	}
	return rules, nil
}

// persistLatencyRules overwrites path with rules — the full ruleset, not a
// diff, matching the "state file is the state" model loadLatencyRules
// relies on. Owner-only permissions: a rule's Source can echo a Datadog
// query string, and hops.jsonl next to it gets the same treatment for the
// same reason (see cmd_up.go).
func persistLatencyRules(path string, rules []proxy.LatencyRule) error {
	if rules == nil {
		rules = []proxy.LatencyRule{}
	}
	data, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return fmt.Errorf("encode latency rules: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
