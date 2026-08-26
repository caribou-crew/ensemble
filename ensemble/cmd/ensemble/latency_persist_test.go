package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
)

func TestLoadLatencyRulesMissingFileReturnsNilNil(t *testing.T) {
	rules, err := loadLatencyRules(filepath.Join(t.TempDir(), "latency.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules != nil {
		t.Fatalf("rules = %+v, want nil (never-persisted sentinel)", rules)
	}
}

func TestLoadLatencyRulesEmptyArrayIsNonNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latency.json")
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	rules, err := loadLatencyRules(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules == nil {
		t.Fatal("rules = nil, want a non-nil empty slice for an explicitly empty persisted file")
	}
	if len(rules) != 0 {
		t.Fatalf("rules = %+v, want empty", rules)
	}
}

func TestLoadLatencyRulesMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latency.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := loadLatencyRules(path); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestPersistThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "latency.json")
	want := []proxy.LatencyRule{
		{Target: "billing", Path: "/", P50: 45, P95: 120, P99: 340, Enabled: true, Source: "datadog:..."},
		{Target: "*", Path: "/health", FixedMs: 5, Enabled: false},
	}
	if err := persistLatencyRules(path, want); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got, err := loadLatencyRules(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPersistLatencyRulesCreatesOwnerOnlyDirAndFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".ensemble", "latency.json")
	if err := persistLatencyRules(path, []proxy.LatencyRule{{Target: "svc", Path: "/", FixedMs: 1}}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf(".ensemble dir perm = %o, want 0700", perm)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("latency.json perm = %o, want 0600", perm)
	}
}

func TestPersistLatencyRulesNilBecomesEmptyArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latency.json")
	if err := persistLatencyRules(path, nil); err != nil {
		t.Fatalf("persist: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("persisted nil as %q, want \"[]\"", data)
	}
}

func TestLatencyRulesPathIsUnderDotEnsemble(t *testing.T) {
	got := latencyRulesPath("/some/dir")
	want := filepath.Join("/some/dir", ".ensemble", "latency.json")
	if got != want {
		t.Errorf("latencyRulesPath() = %q, want %q", got, want)
	}
}
