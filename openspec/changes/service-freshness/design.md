## Context

Each `ServiceState` in the orchestrator already carries a `Version` string —
a git-commit-plus-dirty-digest fingerprint computed by `serviceVersion` in
`orchestrator/version.go`, reusing a small helper (`gitOutput`) that shells
out to `git -C <dir> ...` with a 5s timeout. That fingerprint answers "did
the backend change between two runs" for retrace, but it says nothing about
whether the checkout is *current* relative to its remote — a developer has
to `cd` into each service and run `git fetch && git status` by hand to find
that out.

This change adds a second, complementary git-derived signal — freshness —
computed the same way (shell out to `git`, bounded timeout, best-effort),
but on its own schedule (periodic background fetch, not once at startup)
and surfaced as its own field rather than folded into `Version`.

## Goals / Non-Goals

**Goals:**
- Ambient, glanceable staleness indicator per service on the Services tab:
  behind your own remote branch, and/or behind the configured default
  branch.
- Read-only: `git fetch` only, never `git pull`/`merge`/`rebase`. Resolving
  staleness stays a deliberate developer action.
- Safe under network failure: a fetch failure degrades to "unknown," never
  to a false "up to date" or a false "behind."
- Reuse the existing `gitOutput` helper and its timeout/error conventions
  rather than inventing a parallel git-shelling mechanism.

**Non-Goals:**
- No automatic pull/rebase/merge (an `ensemble pull` command is a natural
  follow-on, explicitly out of scope for this change).
- No dirty-working-tree indicator — `Version`'s `+<digest>` suffix already
  covers "uncommitted changes exist"; freshness is strictly about
  remote-vs-local commit distance. Duplicating that signal here is noise.
- No retrace/manifest integration in this change — freshness lives on
  `ServiceState` and is available for a later change to consume, but this
  change does not modify the recording/retrace manifest format.
- No multi-remote support: only `origin` is fetched/compared.

## Decisions

### Reuse `gitOutput`, add a sibling `freshness.go`

`orchestrator/version.go` already has the exact primitive needed
(`gitOutput(ctx, dir, args...) string`, 5s-bounded, empty string on any
failure). `freshness.go` calls it directly rather than duplicating the
exec/timeout plumbing. This keeps the two git-derived signals visually and
structurally parallel in the codebase, which matters because they'll sit
next to each other in `ServiceState` and in the dashboard.

### Background poll, not on-demand-only

The value is ambient awareness — same category as a CI badge. On-demand
requires remembering to check, which is the exact failure mode ("just run
`git fetch`") this change exists to remove. A background goroutine per
orchestrator lifecycle (`Up`/`Down`), one fetch pass every
`poll_interval_s` (default 300s), covers this without user action. A
`POST /api/freshness/check` endpoint still exists for the "I want the
answer right now" case (e.g. right before recording a retrace baseline).

### `git fetch`, never `git pull`

Fetch is read-only against the working tree and index: no merge conflicts,
no dirty-tree failures, no risk to a running service that has the repo
checked out as its live `Dir`. The badge reports *that* you're behind;
resolving it is a separate, deliberate action (out of scope here).

### Per-service fetch, one goroutine per service, bounded concurrency

Considered a single `git fetch --all` across services that happen to share
a remote, but most local-stack topologies are distinct repos (an 8-service
stack, say, with 8 independently-owned repos), so a shared-remote
optimization would rarely trigger and adds branching for no common case.
Instead: one `git fetch origin` per service, run concurrently, capped by a
semaphore (default 4) to avoid an SSH connection storm against a git host
when a stack has many services. Serial fetch of 8 services at the existing
5s `versionTimeout` could take up to 40s — longer than a 5-minute default
poll interval is fine in the worst case, but bounded parallelism keeps a
single poll pass well under a minute even for larger stacks.

### Skip services whose `Dir` isn't an independently-versioned repo

A service is checked only if:
1. `git -C <dir> rev-parse --is-inside-work-tree` succeeds, and
2. that repo's toplevel (`git -C <dir> rev-parse --show-toplevel`) differs
   from the toplevel of the directory containing `ensemble.yaml`.

Condition 2 excludes stubs/scripts that live in the same repo as the config
itself — those are always at "whatever commit local-stack is," and checking
them would just be a freshness badge that never says anything useful.

### Default branch: explicit config, no auto-detection (v1)

`git symbolic-ref refs/remotes/origin/HEAD` can infer the default branch,
but it silently returns nothing on a remote that hasn't set (or has a
stale) HEAD ref — common enough on self-hosted/enterprise git — which would
produce a confusing "default branch unknown" state despite `main` obviously
being right there. Config-declared `default_branch` (defaulting to `main`
if the field is omitted entirely) is unambiguous and one line to write.
Auto-detection can be layered on later as a fallback when the field is
absent, without changing the config shape.

### Failure encoding: `Error` field, no synthetic "behind" count

A fetch failure (VPN down, SSH key not forwarded, host unreachable) sets
`FreshnessState.Error` and leaves `BehindBranch`/`BehindDefault` at their
last-known values with the *previous* `CheckedAt`, rather than zeroing them
or marking them behind. The dashboard renders unknown/grey only when
`CheckedAt` is empty (never successfully checked) or when reading `Error`
non-empty on a state that also has zero history — see spec for the exact
rendering rule. This avoids the failure mode where a flaky network turns a
correct "up to date" badge into a false "behind" or a false "current" on
every transient failure.

## Risks / Trade-offs

- **[Risk]** SSH-based git remotes may prompt for a passphrase or hang
  waiting on an agent when run from a background goroutine with no TTY. →
  **Mitigation**: `versionTimeout`-style bound (5s, matching the existing
  convention) on every `git fetch` invocation; a hang degrades to a timeout
  error, not a wedged goroutine.
- **[Risk]** Frequent background fetches against a rate-limited git host
  (e.g. GitHub App token limits) across many stacks/developers. →
  **Mitigation**: default 5-minute interval is deliberately conservative
  for a local dev tool; `poll_interval_s` is configurable per stack if a
  team needs it lower or higher.
- **[Trade-off]** No exponential backoff on repeated fetch failure — a
  network outage means every service retries every 5 minutes until it
  recovers. Accepted: this is a local tool, not a service under load;
  backoff complexity isn't justified at this interval.
- **[Trade-off]** No dirty-tree signal in the freshness badge, even though
  it's arguably related. Accepted per Non-Goals — `Version`'s digest suffix
  already answers that question elsewhere in the UI, and merging the two
  concerns into one badge would make the badge harder to read at a glance.

## Migration Plan

Purely additive: new optional config block, new optional response field,
new route. No existing config, API consumer, or CLI output is changed in a
way that breaks compatibility — a stack with no `freshness:` block simply
never starts the background checker, and `ServiceState.Freshness` is
omitted (`omitempty`) from JSON for any service that was skipped or never
checked. No rollback procedure needed beyond reverting the change; no data
migration.

## Open Questions

- **`ensemble pull` follow-on**: once the dashboard shows "you're behind,"
  a per-service `ensemble pull <service>` (fast-forward-only) is the
  obvious next step. Explicitly deferred — flagged here so it isn't lost.
- **retrace integration**: recording freshness into the retrace manifest at
  capture time ("this recording was made 12 commits behind main") is low
  cost once `ServiceState.Freshness` exists, but is left for a follow-up
  change so this one stays scoped to orchestrator/API/dashboard/CLI.
