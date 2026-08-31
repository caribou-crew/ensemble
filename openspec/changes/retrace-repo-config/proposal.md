## Why

`retrace serve` already aggregates every app under one `.retrace/runs/`
tree into one dashboard, and `retrace sync --from github` already pulls CI
runs onto local disk — so a single repo can already get a standalone,
no-ensemble retrace view for the apps whose run data happens to live under
one shared `.retrace/runs/` root. It breaks down the moment a repo's apps
are recorded from different subdirectories (web at repo root, mobile under
`apps/sample/react-native`, say): `retrace serve` only ever reads the one
`.retrace/runs/` tree under its own cwd, so a developer sees six apps or
one, never all seven, without physically colocating run directories that
don't belong together. The only existing way to see every app from one
process is ensemble's global `retrace:` block in `ensemble.yaml` — but that
config is machine-wide, couples viewing to `ensemble up`, and hand-wires
every client repo's app keys and paths into one file, so two repos that
both have a `uxt-web` app collide. Each repo should own the mapping of its
own apps to its own paths, and should be viewable standalone.

## What Changes

- Add a repo-scoped config file, `retrace.repo.yaml`, committed at a repo's
  root: `apps: { <app-key>: { root: <dir> } }`, mapping each app this repo
  owns to the directory holding that app's own `retrace.yaml` and
  `.retrace/` tree (its run/ref data keeps living exactly where `retrace
  run`/`retrace ref` already put it — no run data moves). An optional
  top-level `repo:` (GitHub `org/repo`) and `sync:` block (workflows,
  branch, since, …) give `retrace sync`/`retrace serve --watch` their
  defaults so a developer doesn't have to repeat `--repo`/`--branch` on
  every invocation.
- `retrace serve`, run from anywhere inside a repo that has a
  `retrace.repo.yaml` (found by searching upward from cwd, the way `.git`
  is found), aggregates the review queue across every mapped root into one
  dashboard/API — regardless of which subdirectory each app's runs live
  under — instead of only the one `.retrace/runs/` tree under its own cwd.
  A repo with no `retrace.repo.yaml` anywhere above cwd keeps today's
  single-root behavior unchanged.
- Add `retrace serve --watch [--interval DURATION]`: once serving starts,
  periodically re-runs the same sync `retrace sync` already does — scoped,
  per root, to only the apps `retrace.repo.yaml` maps to that root, using
  the `sync:` block's (or CLI flags') repo/workflow/branch/since filters —
  so a standalone dashboard keeps pulling new GitHub Actions runs without a
  developer re-typing `retrace sync` by hand. Auth stays whatever `gh`
  resolves, unchanged.
- `retrace sync` gains an internal app allowlist so a sync scoped to one
  root of a multi-root repo merges only the run directories belonging to
  the apps mapped to that root, rather than merging every app an artifact
  happens to contain into every root's tree. This is additive: a sync with
  no allowlist (every existing single-app or single-root invocation) merges
  exactly as it does today.

## Capabilities

### New Capabilities
- `retrace-repo-config`: the `retrace.repo.yaml` schema, upward-search
  discovery from any cwd inside the repo, and the app-key → root mapping
  other capabilities read.
- `retrace-serve-aggregation`: `retrace serve`'s review queue and per-flow
  detail/shot routes aggregating across every root a repo config maps,
  instead of one cwd's `.retrace/runs/` tree.
- `retrace-live-sync`: `retrace serve --watch`'s background, per-root,
  app-scoped polling sync loop.
- `retrace-sync`: not yet present under `openspec/specs/` — the change that
  introduces it (`retrace-ci-sync`) hasn't archived yet — so the app
  allowlist added here (an additive requirement: `retrace sync`'s merge
  step, when scoped to a root by a repo config, merges only the run
  directories belonging to the apps mapped to that root) is filed as a new
  `ADDED` requirement under this same capability path rather than a
  `MODIFIED` delta against a spec that doesn't exist yet. It is additive
  either way: a sync with no allowlist (every caller today) merges exactly
  as `retrace-ci-sync` already specifies.

### Modified Capabilities
(none — see `retrace-sync` above for why its change is filed as ADDED)

## Impact

- `retrace/config` (or a new sibling package, e.g. `retrace/repoconfig`):
  new `retrace.repo.yaml` type, loader, and upward-search discovery.
- `retrace/serve`: `Deps`/`BuildQueue` gain a multi-root aggregation path;
  route handlers (`routes.go`) resolve an `{app}` path value to the root
  Deps that owns it instead of assuming one `Deps` for the whole server.
  Every existing single-root caller (`retrace serve` today, `ensemble
  serve`'s single-stack dashboard) is unaffected — aggregation only
  activates when a repo config maps more than one root.
- `retrace/sync`: `Options` gains an app allowlist consulted by the
  GitHub merge step (`github.go`); `Run`/`List` unchanged for callers that
  don't set it.
- `retrace/cmd/retrace`: `cmd_serve.go` gains `--watch`/`--interval` and
  repo-config discovery; a new background sync loop reuses `retrace/sync`
  directly, no new package.
- No impact to `retrace run`, `retrace ref`, `retrace diff`, `retrace
  export`, `retrace replay`, or `ensemble`'s own global `retrace:` block —
  that aggregator remains valid and unchanged for whoever still wants one
  cross-repo, all-stacks-at-once view; this change adds the per-repo,
  standalone alternative alongside it.
- Non-breaking: a repo with no `retrace.repo.yaml` sees identical `retrace
  serve`/`retrace sync` behavior to today.
