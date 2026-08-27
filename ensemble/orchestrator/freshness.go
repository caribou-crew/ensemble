package orchestrator

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Freshness answers a question Version (see version.go) does not: not "did
// the backend change between two runs", but "is this checkout current
// relative to its remote". It reuses the same git-shelling primitive
// (gitOutput) and the same best-effort conventions — a failure degrades to
// an absent/stale answer, never a fabricated one — but runs on its own
// schedule (a background poll, not once at start) and is surfaced as its
// own field so the two signals never get conflated in the dashboard.
//
// Read-only: every git call here is `fetch`, `rev-list`, or `symbolic-ref`.
// Nothing here ever pulls, merges, or rebases — resolving staleness stays a
// deliberate developer action.

// freshnessConcurrency bounds how many `git fetch` calls run at once during
// one poll pass. A stack with many services fetching in full parallel
// against one git host risks an SSH connection storm or a rate limit; small
// enough not to matter for a handful of services, large enough that even a
// larger stack's poll pass finishes well inside a single interval.
const freshnessConcurrency = 4

// FreshnessState is one service's git-freshness snapshot — see
// ServiceState.Freshness. CheckedAt and Error are RFC3339/plain-text rather
// than typed so a stale-but-good snapshot can be JSON-round-tripped
// unchanged when a later check fails (see mergeFreshness).
type FreshnessState struct {
	Branch        string `json:"branch"`
	BehindBranch  int    `json:"behindBranch"`
	BehindDefault int    `json:"behindDefault"`
	DefaultBranch string `json:"defaultBranch"`
	// CheckedAt is the RFC3339 timestamp of the last SUCCESSFUL fetch —
	// empty means this service has never been successfully checked, which
	// the dashboard renders as "unknown" rather than "up to date".
	CheckedAt string `json:"checkedAt,omitempty"`
	// Error is set when the most recent check attempt failed (fetch
	// failure, or the branch/rev-list comparison couldn't be resolved).
	// Non-empty alongside a populated Branch/BehindBranch/BehindDefault
	// means "this is what we last knew, and the most recent recheck
	// failed" — see mergeFreshness.
	Error string `json:"error,omitempty"`
}

// isGitRepo reports whether dir is inside a git working tree.
func isGitRepo(ctx context.Context, dir string) bool {
	return gitOutput(ctx, dir, "rev-parse", "--is-inside-work-tree") == "true"
}

// repoToplevel returns dir's repository root, or "" if dir isn't inside a
// git working tree.
func repoToplevel(ctx context.Context, dir string) string {
	return gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
}

// eligibleForFreshness reports whether serviceDir should be freshness-
// checked: it must be its own git repository, distinct from the repository
// containing ensemble.yaml (configDir) — a stub or script living in the
// config's own repo is always at "whatever commit local-stack is", and
// checking it would just be a badge that never says anything useful.
func eligibleForFreshness(ctx context.Context, serviceDir, configDir string) bool {
	if !isGitRepo(ctx, serviceDir) {
		return false
	}
	svcTop := repoToplevel(ctx, serviceDir)
	if svcTop == "" {
		return false
	}
	return svcTop != repoToplevel(ctx, configDir)
}

// currentBranch returns dir's current branch name, or "" if HEAD isn't on a
// branch (detached) or the lookup fails.
func currentBranch(ctx context.Context, dir string) string {
	return gitOutput(ctx, dir, "symbolic-ref", "--short", "HEAD")
}

// behindCount reports how many commits ref has that HEAD does not, via
// `git rev-list --count HEAD..ref`. ok is false when the count couldn't be
// resolved (e.g. ref doesn't exist — a branch never pushed, or a
// misconfigured default_branch) — distinct from a genuine 0, which callers
// must not silently treat as "up to date".
func behindCount(ctx context.Context, dir, ref string) (n int, ok bool) {
	out := gitOutput(ctx, dir, "rev-list", "--count", "HEAD.."+ref)
	if out == "" {
		return 0, false
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, false
	}
	return n, true
}

// gitRun runs a git command for its exit status alone, mirroring gitOutput's
// exec setup but without an output buffer — `git fetch`'s stdout is empty on
// both success and failure, so gitOutput's "" return can't distinguish them;
// this can.
func gitRun(ctx context.Context, dir string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	return cmd.Run()
}

// checkServiceFreshness fetches dir's origin remote and reports how far it
// is behind its own remote branch and behind defaultBranch. Every failure
// path returns a FreshnessState carrying only DefaultBranch and Error —
// callers merge that over any previously-known-good state (mergeFreshness)
// rather than treating it as a fresh, empty answer.
func checkServiceFreshness(ctx context.Context, dir, defaultBranch string) FreshnessState {
	if err := gitRun(ctx, dir, "fetch", "origin"); err != nil {
		return FreshnessState{DefaultBranch: defaultBranch, Error: fmt.Sprintf("git fetch origin: %v", err)}
	}

	branch := currentBranch(ctx, dir)
	if branch == "" {
		return FreshnessState{DefaultBranch: defaultBranch, Error: "could not determine current branch"}
	}

	behindBranch, ok := behindCount(ctx, dir, "origin/"+branch)
	if !ok {
		return FreshnessState{DefaultBranch: defaultBranch, Error: fmt.Sprintf("could not compare HEAD against origin/%s", branch)}
	}
	behindDefault, ok := behindCount(ctx, dir, "origin/"+defaultBranch)
	if !ok {
		return FreshnessState{DefaultBranch: defaultBranch, Error: fmt.Sprintf("could not compare HEAD against origin/%s", defaultBranch)}
	}

	return FreshnessState{
		Branch:        branch,
		BehindBranch:  behindBranch,
		BehindDefault: behindDefault,
		DefaultBranch: defaultBranch,
		CheckedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

// mergeFreshness combines a freshly-attempted check (next) with whatever was
// previously known (prev, possibly nil): a successful next entirely
// replaces prev, but a failed next (Error set) keeps prev's
// Branch/BehindBranch/BehindDefault/CheckedAt and only updates Error — see
// the "fetch failure preserves last-known state" requirement. A service
// that has never succeeded (prev nil, next failed) reports an empty
// CheckedAt, which the dashboard renders as unknown rather than stale-good.
func mergeFreshness(prev *FreshnessState, next FreshnessState) *FreshnessState {
	if next.Error == "" {
		return &next
	}
	merged := FreshnessState{DefaultBranch: next.DefaultBranch, Error: next.Error}
	if prev != nil {
		merged.Branch = prev.Branch
		merged.BehindBranch = prev.BehindBranch
		merged.BehindDefault = prev.BehindDefault
		merged.CheckedAt = prev.CheckedAt
	}
	return &merged
}

// eligibleFreshnessServices resolves every active service's current Dir
// (through its variant, same as startService) and returns the name->dir map
// of those eligible for freshness checking.
func (o *Orchestrator) eligibleFreshnessServices(ctx context.Context) map[string]string {
	out := map[string]string{}
	for name := range o.activeServices() {
		svc, err := o.resolve(name)
		if err != nil {
			continue
		}
		dir := resolveDir(o.cfg.Dir, svc.Dir)
		if eligibleForFreshness(ctx, dir, o.cfg.Dir) {
			out[name] = dir
		}
	}
	return out
}

// runFreshnessPoll runs one freshness pass over every eligible service,
// bounded-concurrency (freshnessConcurrency), and merges each result into
// orchestrator state. A no-op when freshness isn't configured.
func (o *Orchestrator) runFreshnessPoll(ctx context.Context) {
	freshness := o.cfg.Freshness
	if freshness == nil {
		return
	}
	defaultBranch := freshness.EffectiveDefaultBranch()

	sem := make(chan struct{}, freshnessConcurrency)
	var wg sync.WaitGroup
	for name, dir := range o.eligibleFreshnessServices(ctx) {
		wg.Add(1)
		go func(name, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := checkServiceFreshness(ctx, dir, defaultBranch)
			o.setState(name, func(s *ServiceState) {
				s.Freshness = mergeFreshness(s.Freshness, result)
			})
		}(name, dir)
	}
	wg.Wait()
}

// TriggerFreshnessCheck runs one freshness pass immediately, outside the
// normal poll schedule — see POST /api/freshness/check. Returns once every
// eligible service has been checked (or the context is done). A no-op when
// freshness isn't configured.
func (o *Orchestrator) TriggerFreshnessCheck(ctx context.Context) {
	o.runFreshnessPoll(ctx)
}

// beginFreshness starts the background freshness poll loop — see design.md's
// "background poll, not on-demand-only" decision. A no-op when freshness
// isn't configured, or when a loop is already running (a second Up on the
// same Orchestrator must not leak a second one). Never blocks Up: the first
// pass runs inside the spawned goroutine, not before it starts.
func (o *Orchestrator) beginFreshness() {
	if o.cfg.Freshness == nil {
		return
	}

	o.mu.Lock()
	if o.freshnessCancel != nil {
		o.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.freshnessCancel = cancel
	done := make(chan struct{})
	o.freshnessDone = done
	o.mu.Unlock()

	interval := time.Duration(o.cfg.Freshness.EffectivePollIntervalS()) * time.Second
	go o.runFreshnessLoop(ctx, done, interval)
}

// runFreshnessLoop runs an immediate poll pass, then one every interval,
// until ctx is cancelled (by Down — see stopFreshness).
func (o *Orchestrator) runFreshnessLoop(ctx context.Context, done chan struct{}, interval time.Duration) {
	defer close(done)

	o.runFreshnessPoll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.runFreshnessPoll(ctx)
		}
	}
}

// stopFreshness cancels the background freshness loop, if running, and
// waits for its goroutine to actually return — so Down can be certain no
// more fetches happen and no more state mutations land after it returns.
func (o *Orchestrator) stopFreshness() {
	o.mu.Lock()
	cancel := o.freshnessCancel
	done := o.freshnessDone
	o.freshnessCancel = nil
	o.freshnessDone = nil
	o.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-done
}
