## Context

`retrace serve` (`retrace/cmd/retrace/cmd_serve.go`) builds one
`serve.Deps{Cwd, Cfg, ...}` from the process's own working directory and
hands it to `serve.New`. Every route (`retrace/serve/routes.go`) and
`serve.BuildQueue` reads `runs.RunsRoot(d.Cwd)` — one `.retrace/runs/` tree
— and lists every app directory under it (`runs.ListAppsErr`). Multiple
apps already aggregate into one queue *today*, but only when their run
directories are physically siblings under that one tree; the sample repo's
six mobile apps work this way because `retrace run` for all of them writes
into `apps/sample/react-native/.retrace/runs/`. A seventh app recorded
elsewhere (repo-root `.retrace/runs/` for web) is invisible to a `retrace
serve` started from either directory.

`Deps` already has one per-app seam: `CfgFor func(app string)
(*config.Config, error)`, used today only by `ensemble/server` to resolve
each app's own `retrace.yaml` for diff *rules* (masks, wire_ignore,
thresholds) — see `ensemble/config.RetraceConfig.Apps` and
`ensemble/server/retrace.go`'s `retraceDeps`. It does not, and cannot,
relocate where an app's *run data* is read from: `BuildQueue` still walks
one `runs.RunsRoot(d.Cwd)`. Ensemble's global aggregator sidesteps this by
requiring every app's runs to be synced/recorded into the one stack-wide
`.retrace/runs/` tree regardless of where each app's `retrace.yaml` lives —
`Apps` only ever redirects config resolution. This proposal needs runs
themselves to be read from different roots, which `Deps`/`BuildQueue`
cannot do today.

`retrace/sync.Run` (`retrace/sync/sync.go`, `github.go`) downloads GitHub
Actions artifacts and merges every `<app>/<flow>/<run-id>/manifest.json` an
artifact contains into `runs.RunsRoot(Options.Cwd)`, with no concept of
"which apps belong here" — it merges whatever an artifact happens to
contain.

## Goals / Non-Goals

**Goals:**
- Read a per-repo config that maps app keys to the root directories that
  already hold each app's `retrace.yaml` and `.retrace/` tree (no run data
  moves).
- Let `retrace serve`, invoked from any directory inside such a repo,
  aggregate the review queue and every per-flow route across all mapped
  roots.
- Let `retrace serve --watch` keep pulling GitHub Actions runs into the
  right root for each app, without a developer re-running `retrace sync`.
- Leave every existing single-root/single-app behavior (today's `retrace
  serve`, `retrace sync`, and ensemble's global `retrace:` aggregator)
  byte-for-byte unchanged when no `retrace.repo.yaml` is present.

**Non-Goals:**
- Replacing or deprecating ensemble's `retrace:`/`Apps` cross-repo
  aggregator — it stays for the "every stack at once" view.
- A GUI/editor for `retrace.repo.yaml`, or auto-generating it from
  discovered apps — it is hand-written and committed, like `retrace.yaml`.
- Auto-refreshing dashboard UI (client-side polling). `GET /api/queue`
  already recomputes from disk on every request with no cache, so a page
  reload after a background sync already shows the new state; `--watch`'s
  job is keeping the *data* fresh, not pushing updates into an open tab.
  Client-side polling can follow later without changing this design.
- Supporting more than one GitHub repo per `retrace.repo.yaml`. One repo
  key at the top level is enough for the stated use case (one client repo,
  one CI repo); `ensemble`'s `Repos`/`Workflows` plural form already covers
  the "several source repos" case for whoever needs that, at the ensemble
  layer.

## Decisions

### D1: `retrace.repo.yaml` maps app keys straight to root directories, mirroring `ensemble.RetraceConfig.Apps`

```yaml
repo: acme/sample-app             # optional; org/repo for sync
apps:
  uxt-web:             { root: . }
  uxt-rn-ios:          { root: apps/sample/react-native }
  uxt-native-ios:      { root: apps/sample/react-native }
  uxt-flutter-ios:     { root: apps/sample/react-native }
  uxt-rn-android:      { root: apps/sample/react-native }
  uxt-native-android:  { root: apps/sample/react-native }
  uxt-flutter-android: { root: apps/sample/react-native }
sync:                              # optional; defaults for `sync`/`--watch`
  workflows: ["Retrace *"]
  branch: main
  since: 24h
```

Two or more app keys naming the same `root` is the expected, common case
(the six mobile apps above) — it is exactly today's "apps already
colocated" case, unchanged. A root is a directory relative to
`retrace.repo.yaml`'s own location (or absolute); it is where that app's
`retrace.yaml` and `.retrace/runs/<app>/` already live, so this file adds
no new place for run data to be written.

**Alternative considered:** a flat `apps: [uxt-web, uxt-rn-ios, ...]` list
plus a separate `roots: [...]` list, inferring the mapping by walking each
root and asking what apps it contains. Rejected: it requires every root to
be readable (and every app name unique across it) just to resolve the
mapping, turns a config-time error (a typo'd app key) into a run-time
"which root is this app in" lookup miss, and the explicit map is one line
longer per app while being unambiguous by construction — the same tradeoff
`ensemble.RetraceConfig.Apps` already made.

### D2: Discovery walks upward from cwd, like `.git`

`retrace serve`/`retrace sync`/`retrace sync list` first look for
`retrace.repo.yaml` in cwd, then each parent directory in turn, stopping at
the filesystem root (or a `.git` directory, whichever is found — matching
where a developer would expect the search to stop). Found: multi-root
aggregation activates, rooted at the directory containing
`retrace.repo.yaml`. Not found: every command behaves exactly as it does
today (single cwd, `config.Discover(cwd)`), so a repo that never adopts
this file sees no change at all — this is the load-bearing compatibility
guarantee named in the proposal's Impact section.

**Alternative considered:** requiring `retrace serve` to be run from the
directory containing `retrace.repo.yaml` only. Rejected: the feature
request is explicit that "run anywhere in the repo" is the point — a
mobile app's own subdirectory is exactly where a developer already runs
`retrace run`/`retrace diff` today, and forcing a `cd` back to repo root
just to view the dashboard reintroduces the friction this whole change
exists to remove.

### D3: One `serve.Deps` per distinct root, merged by a thin aggregator — `BuildQueue`/`SummaryFor` themselves are untouched

A new type (`serve.Sources`, or similar — naming is an implementation
detail for tasks.md) holds one `Deps` per distinct root directory named in
the repo config, each built the same way `cmd_serve.go` builds one today
(`config.Discover(root)`). Aggregation:

- **Queue** (`GET /api/queue`): call `BuildQueue` once per root's `Deps`,
  concatenate the `[]Item` slices, re-sort with the existing `ScoreOf`
  comparator. `BuildQueue` and `ScoreOf` are not modified — a single-root
  repo (or no repo config at all) produces byte-identical output to today,
  because it is the one-root case of the same aggregator.
- **Per-flow routes** (`GET /api/queue/{app}/{flow}`, shots, evidence,
  video, report): resolve `{app}` to its root via the config's app→root
  map (not by searching every root), then dispatch to that root's `Deps`
  exactly as `routes.go` does today. An `{app}` absent from the map — and
  present when there is no repo config at all — resolves to today's single
  `Deps`, unchanged.
- **Config-reload / rule-append routes** (`POST .../rule`, the redact
  overlay): also resolve by app → root, then write through that root's own
  `retrace.yaml`/overlay, exactly as a single-root server already writes
  through its one `Cwd` — there is still exactly one writable config per
  app, just resolved through the map instead of being the server's only
  option.

**Alternative considered:** teach `runs.RunsRoot`/`BuildQueue` themselves
to accept multiple roots and synthesize one merged app→app-directory
lookup internally. Rejected: `BuildQueue`'s single-root shape is exactly
what `ensemble/server` already depends on (`retraceDeps` builds one `Deps`
per stack), and widening its signature would touch every existing caller
for a capability only the new multi-root path needs. Composing multiple
existing single-root `Deps`/`BuildQueue` calls at a layer above keeps
`retrace/serve`'s core (`queue.go`) exactly as it is today — the aggregator
is new code, not a rewrite of tested code.

### D4: `retrace sync`'s merge step takes an app allowlist, so a per-root sync doesn't pollute the wrong root

`sync.Options` gains an optional `Apps []string` (empty means "no filter,"
i.e. today's behavior for every existing caller). When set, the GitHub
merge step (`github.go`) only writes a downloaded artifact's
`<app>/<flow>/<run-id>/` directories whose `<app>` is in the allowlist,
and reports every other app's run dirs it saw as **skipped**, with a
reason ("not in this root's app allowlist") distinct from the existing
malformed-artifact skip reason — same `Result.Skipped` shape, no wire
change.

`retrace serve --watch` and a future `retrace sync` invoked against a
`retrace.repo.yaml` root call `sync.Run` once per distinct root, passing
that root's own list of mapped app keys as `Apps`. This is what stops
`uxt-web`'s run directory from also landing under
`apps/sample/react-native/.retrace/runs/uxt-web/` when a sync scoped to
the mobile root happens to see a `uxt-web` artifact too (both roots poll
the same GitHub repo, since `retrace.repo.yaml` names one `repo:` for the
whole file).

**Alternative considered:** one sync per *app* instead of per root
(seven `sync.Run` calls instead of two). Rejected: `sync.Run` downloads
and inspects a whole artifact once per call, so per-app calls would
re-download and re-inspect the same artifact up to six times for the
mobile root's apps, for no additional correctness — the allowlist already
gets per-app precision out of one per-root download pass.

### D5: `--watch` is a ticker inside `cmd_serve.go`, not a new package

`retrace serve --watch [--interval 5m]` starts a `time.Ticker` (default
interval TBD in tasks — 5m matches `ensemble`'s existing
`FreshnessConfig.PollIntervalS` default order of magnitude) alongside the
HTTP server, calling `sync.Run` once per root on every tick (and once
immediately at startup, so a fresh `retrace serve --watch` doesn't wait a
full interval before showing CI data). Sync errors (rate limiting, `gh`
auth expiring mid-session) are logged to stderr and do not stop the
server or the ticker — a transient GitHub/`gh` failure must not take down
the dashboard a developer is actively looking at. `sync:` block values
(or CLI flags, which win when both are set) supply repo/workflow/branch
filters; `Repo` (or repo-config's top-level `repo:`) is required for
`--watch` to do anything, exactly as `retrace sync` already requires one.

**Alternative considered:** a separate `retrace watch` subcommand that
only syncs, run alongside `retrace serve` in a second terminal/process.
Rejected: the proposal's whole point is one command, one process,
hands-off — a second process to keep alive defeats it, and `retrace
serve`'s existing graceful-shutdown machinery (`ctx`/`signal.NotifyContext`)
already gives the ticker a clean stop signal for free.

## Risks / Trade-offs

- **[Risk] A misconfigured `retrace.repo.yaml` (a root that doesn't exist,
  or two repo configs found by nested repos/worktrees) silently serves an
  incomplete dashboard.** → Mitigation: `retrace serve` validates every
  mapped root at startup (root exists, is a directory) and fails fast,
  naming the bad entry — matching `config.Load`'s existing "fail at load,
  not at first use" posture; discovery stops at the *nearest*
  `retrace.repo.yaml` found walking up, so a nested worktree's own config
  (if any) always wins over an ancestor's.
- **[Risk] `--watch`'s background ticker calling `gh` on an interval could
  hit GitHub API rate limits on a busy repo with many developers each
  running `retrace serve --watch`.** → Mitigation: default interval is
  conservative (minutes, not seconds — matching `ensemble`'s freshness
  checker precedent); `gh`'s own auth/rate-limit errors surface to stderr
  exactly as a manual `retrace sync` failure would, so a developer sees
  why syncing paused rather than a silently stale dashboard.
- **[Trade-off] Two roots polling the same GitHub repo on the same
  interval (mobile root and web root, in the running example) means `gh
  run list` is called twice per tick instead of once.** → Accepted:
  `gh run list` is cheap relative to `gh run download`, and D4's allowlist
  is what keeps the *download/merge* half correct; a future optimization
  could share one `gh run list` result across roots targeting the same
  repo, but it is not required for correctness and is deferred.

## Migration Plan

No migration: this is additive, opt-in per repo (adopting a
`retrace.repo.yaml` file), and every command's behavior is unchanged for a
repo that never adds one. Rollback is deleting the file.

## Open Questions

- Exact default `--interval` value and its flag name/shape (`--interval
  5m` vs. a `sync.interval` key in the `sync:` block) — either is
  consistent with this design; pick one in tasks.md without revisiting the
  approach.
