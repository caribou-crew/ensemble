package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// gitOriginAndClone builds a bare "origin" repo with one commit on main and
// a working clone tracking it — the shape every freshness test needs, since
// behind-counts are meaningless without a real remote to compare against.
func gitOriginAndClone(t *testing.T) (origin, clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	origin = t.TempDir()
	run(t, origin, "init", "-q", "--bare")

	seed := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "retrace test"},
		{"config", "commit.gpgsign", "false"},
	} {
		run(t, seed, args...)
	}
	write(t, filepath.Join(seed, "main.go"), "package main\n")
	run(t, seed, "add", "main.go")
	run(t, seed, "commit", "-qm", "first")
	run(t, seed, "branch", "-M", "main")
	run(t, seed, "remote", "add", "origin", origin)
	run(t, seed, "push", "-q", "origin", "main")
	// The bare repo's HEAD defaults to whatever init.defaultBranch the
	// local git install uses (often "master"), which may not be the branch
	// just pushed — pointed explicitly so `git clone` checks out "main"
	// with a tracking branch instead of failing to find a HEAD to check out.
	run(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")

	clone = filepath.Join(t.TempDir(), "clone")
	cloneCmd := exec.Command("git", "clone", "-q", origin, clone)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "retrace test"},
		{"config", "commit.gpgsign", "false"},
	} {
		run(t, clone, args...)
	}
	return origin, clone
}

// pushCommit adds one commit with the given file contents directly to a
// checkout of repo (any local clone of origin, not necessarily the one
// under test) and pushes it — simulating "someone else pushed" without
// touching the working tree the test is asserting about.
func pushCommit(t *testing.T, origin, branch, contents string) {
	t.Helper()
	pusher := filepath.Join(t.TempDir(), "pusher")
	cloneCmd := exec.Command("git", "clone", "-q", "-b", branch, origin, pusher)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone (pusher): %v: %s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "retrace test"},
		{"config", "commit.gpgsign", "false"},
	} {
		run(t, pusher, args...)
	}
	write(t, filepath.Join(pusher, "extra.go"), contents)
	run(t, pusher, "add", "extra.go")
	run(t, pusher, "commit", "-qm", "extra")
	run(t, pusher, "push", "-q", "origin", branch)
}

// --- eligibility ---

func TestEligibleForFreshnessIndependentRepo(t *testing.T) {
	configDir := t.TempDir() // not a repo at all — the common case, ensemble.yaml isn't versioned
	svcDir := gitRepo(t)
	if !eligibleForFreshness(context.Background(), svcDir, configDir) {
		t.Error("an independent git repo should be eligible")
	}
}

func TestEligibleForFreshnessSameRepoAsConfig(t *testing.T) {
	configDir := gitRepo(t)
	svcDir := filepath.Join(configDir, "stub")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if eligibleForFreshness(context.Background(), svcDir, configDir) {
		t.Error("a service living inside the config's own repo should be ineligible")
	}
}

func TestEligibleForFreshnessNotARepo(t *testing.T) {
	if eligibleForFreshness(context.Background(), t.TempDir(), t.TempDir()) {
		t.Error("a plain directory should be ineligible")
	}
}

// --- checkServiceFreshness ---

func TestCheckServiceFreshnessUpToDate(t *testing.T) {
	_, clone := gitOriginAndClone(t)
	got := checkServiceFreshness(context.Background(), clone, "main")
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want main", got.Branch)
	}
	if got.BehindBranch != 0 || got.BehindDefault != 0 {
		t.Errorf("BehindBranch=%d BehindDefault=%d, want 0/0 for a fresh clone", got.BehindBranch, got.BehindDefault)
	}
	if got.CheckedAt == "" {
		t.Error("CheckedAt is empty after a successful check")
	}
}

func TestCheckServiceFreshnessBehindOwnBranch(t *testing.T) {
	origin, clone := gitOriginAndClone(t)
	pushCommit(t, origin, "main", "package main // one\n")
	pushCommit(t, origin, "main", "package main // two\n")

	got := checkServiceFreshness(context.Background(), clone, "main")
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if got.BehindBranch != 2 {
		t.Errorf("BehindBranch = %d, want 2", got.BehindBranch)
	}
	if got.BehindDefault != 2 {
		t.Errorf("BehindDefault = %d, want 2 (own branch IS the default branch here)", got.BehindDefault)
	}
}

func TestCheckServiceFreshnessBehindDefaultOnly(t *testing.T) {
	origin, clone := gitOriginAndClone(t)
	run(t, origin, "branch", "feature", "main")
	run(t, clone, "fetch", "-q", "origin")
	run(t, clone, "checkout", "-q", "-b", "feature", "origin/feature")

	pushCommit(t, origin, "main", "package main // main moved on\n")

	got := checkServiceFreshness(context.Background(), clone, "main")
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if got.Branch != "feature" {
		t.Errorf("Branch = %q, want feature", got.Branch)
	}
	if got.BehindBranch != 0 {
		t.Errorf("BehindBranch = %d, want 0 (nothing pushed to origin/feature)", got.BehindBranch)
	}
	if got.BehindDefault != 1 {
		t.Errorf("BehindDefault = %d, want 1", got.BehindDefault)
	}
}

func TestCheckServiceFreshnessFetchFailure(t *testing.T) {
	_, clone := gitOriginAndClone(t)
	run(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "does-not-exist"))

	got := checkServiceFreshness(context.Background(), clone, "main")
	if got.Error == "" {
		t.Fatal("expected an error for an unreachable origin, got none")
	}
	if got.CheckedAt != "" {
		t.Errorf("CheckedAt = %q, want empty on a failed check", got.CheckedAt)
	}
}

// --- mergeFreshness ---

func TestMergeFreshnessSuccessReplaces(t *testing.T) {
	prev := &FreshnessState{Branch: "old", BehindBranch: 5, CheckedAt: "old-time"}
	next := FreshnessState{Branch: "main", BehindBranch: 0, CheckedAt: "new-time"}
	got := mergeFreshness(prev, next)
	if *got != next {
		t.Errorf("mergeFreshness with a successful next = %+v, want %+v", *got, next)
	}
}

func TestMergeFreshnessFailurePreservesLastKnownGood(t *testing.T) {
	prev := &FreshnessState{Branch: "main", BehindBranch: 2, BehindDefault: 2, DefaultBranch: "main", CheckedAt: "old-time"}
	next := FreshnessState{DefaultBranch: "main", Error: "git fetch origin: network unreachable"}
	got := mergeFreshness(prev, next)
	if got.Branch != prev.Branch || got.BehindBranch != prev.BehindBranch || got.BehindDefault != prev.BehindDefault || got.CheckedAt != prev.CheckedAt {
		t.Errorf("mergeFreshness dropped last-known-good state: %+v", got)
	}
	if got.Error != next.Error {
		t.Errorf("Error = %q, want %q", got.Error, next.Error)
	}
}

func TestMergeFreshnessNeverCheckedStaysEmpty(t *testing.T) {
	next := FreshnessState{DefaultBranch: "main", Error: "git fetch origin: network unreachable"}
	got := mergeFreshness(nil, next)
	if got.CheckedAt != "" {
		t.Errorf("CheckedAt = %q, want empty for a service that has never succeeded", got.CheckedAt)
	}
	if got.Error != next.Error {
		t.Errorf("Error = %q, want %q", got.Error, next.Error)
	}
}

// --- orchestrator lifecycle ---

func TestFreshnessPollPopulatesEligibleServiceState(t *testing.T) {
	_, clone := gitOriginAndClone(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Dir: clone, Run: "sleep 30"},
		},
		Freshness: &config.FreshnessConfig{DefaultBranch: "main", PollIntervalS: 1},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	st := waitForFreshness(t, o, "svc")
	if st.Branch != "main" {
		t.Errorf("Branch = %q, want main", st.Branch)
	}
	if st.BehindBranch != 0 || st.BehindDefault != 0 {
		t.Errorf("expected up to date, got behindBranch=%d behindDefault=%d", st.BehindBranch, st.BehindDefault)
	}
	if st.Error != "" {
		t.Errorf("Error = %q, want empty", st.Error)
	}
}

func TestFreshnessDisabledWithoutConfig(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Dir: t.TempDir(), Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	time.Sleep(100 * time.Millisecond)
	s, _ := o.Service("svc")
	if s.Freshness != nil {
		t.Errorf("Freshness = %+v, want nil when freshness: isn't configured", s.Freshness)
	}
	o.mu.Lock()
	cancel := o.freshnessCancel
	o.mu.Unlock()
	if cancel != nil {
		t.Error("freshness loop started despite no freshness: config")
	}
}

func TestFreshnessStopsOnDown(t *testing.T) {
	_, clone := gitOriginAndClone(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Dir: clone, Run: "sleep 30"},
		},
		Freshness: &config.FreshnessConfig{DefaultBranch: "main", PollIntervalS: 1},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	waitForFreshness(t, o, "svc") // let the loop actually start before stopping it

	if err := o.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}

	o.mu.Lock()
	cancel := o.freshnessCancel
	done := o.freshnessDone
	o.mu.Unlock()
	if cancel != nil || done != nil {
		t.Error("freshness loop still tracked as running after Down")
	}
}

// waitForFreshness polls o.Service(name) until Freshness is populated
// (checked successfully at least once) or the test times out.
func waitForFreshness(t *testing.T, o *Orchestrator, name string) FreshnessState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := o.Service(name); ok && s.Freshness != nil && (s.Freshness.CheckedAt != "" || s.Freshness.Error != "") {
			return *s.Freshness
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("freshness for %q was never populated", name)
	return FreshnessState{}
}
