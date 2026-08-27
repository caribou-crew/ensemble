## Context

retrace already has everything needed to *review* a flow — `retrace/serve`
diffs every recorded run against its accepted reference and ranks them
worst-first (`BuildQueue`), and `retrace/runs` owns the on-disk layout
(`.retrace/runs/<app>/<flow>/<run-id>/`, `NewRunID(now, sha)` so IDs sort by
time). What's missing is (a) a way to get a *CI-recorded* run onto a
developer's disk next to their local ones, and (b) a place to see all of it
across apps without running a second server.

During scoping, the original "linked to `retrace serve` on :4800" plan for
the dashboard detail view was replaced: `retrace serve` and `ensemble`
are separate binaries with separate lifecycles, and `retrace serve` isn't
part of what `ensemble up` starts. Requiring a developer to also remember
`retrace serve` just to see results defeats the point of putting this in
the dashboard. `ensemble/server` importing `retrace/serve` as a Go library
is possible today: they're separate modules in the same Go workspace
(`ensemble/go.mod` already requires `core`; nothing under `retrace/`
imports `ensemble`, so `ensemble` → `retrace` introduces no cycle).

## Goals / Non-Goals

**Goals:**
- A developer sees cross-app retrace status inside `ensemble up`'s own
  dashboard, with no second process to run or remember.
- CI-recorded runs land in the same `.retrace/runs/` tree local runs do, so
  the existing diff engine (and `retrace serve`, unmodified) handles them
  for free — no second implementation of verdict/scoring logic.
- `retrace sync` works against a GH repo with zero additional
  infrastructure (no server, no S3 bucket, no new credential store).

**Non-Goals:**
- An S3 backend, auto-sync polling, an embedded `retrace-ui` mount, or an
  MCP server. All four were considered and deferred (see proposal); none
  are precluded by this design.
- Historical trend / sparkline views. `BuildQueue` reports the latest run
  per app/flow; this change does not add a second, time-series query.
- Syncing `.retrace-ref/` (accepted reference bundles). What counts as
  "known good" stays a local, human decision (`retrace ref accept`); sync
  only ever adds *candidate* runs to review, never changes what they're
  compared against.

## Decisions

### D1: Shell out to `gh`, don't write a GitHub API client
`retrace/sync`'s GitHub backend invokes `gh run list --repo … --json` and
`gh run download <id> --repo … --dir <tmp>` rather than calling the GitHub
REST API directly. `gh run download` already unzips the artifact for us.

Alternative considered: a hand-rolled `net/http` client against
`api.github.com` (pagination, artifact zip download, unzip). Rejected —
`AGENTS.md`'s "justify every third-party Go dependency" cuts the other way
here too: shelling out to a tool the target audience (developers running
`gh` locally already, per the earlier "use `gh auth token`" preference)
already has installed costs zero new Go dependencies and reuses `gh`'s own
auth resolution (`gh auth login`, `GH_TOKEN`/`GITHUB_TOKEN` env) instead of
reimplementing it. Cost: `retrace sync --from github` requires `gh` on
`PATH`; the command fails fast with a clear "install gh" message when it
isn't, rather than a wrapped network error. `gh` is a local-machine-only
dependency — the CI side only needs `actions/upload-artifact`, no `gh`.

### D2: Sync the raw run directory, not the exported report
The CI workflow's `retrace run --app X` step already produces
`.retrace/runs/<app>/**` (manifest + shots). That whole subtree is what
gets uploaded and synced — *not* `retrace export`'s output. `retrace
export` remains, unchanged, as CI's own pass/fail gate and human-browsable
static report; it's a rendering of a comparison already made and has no
manifest to re-diff against whatever reference is accepted locally today.
Syncing the raw run lets the *developer's own* `.retrace-ref/` decide the
verdict, which is what makes CI and local rows comparable in one table.

### D3: Provenance as a sidecar file, not a `Manifest` field
A synced run gets `.retrace/runs/<app>/<flow>/<run-id>/source.json`
(`{kind: "ci", workflow, runUrl, sha, syncedAt}`), written by `retrace
sync` after the run directory is copied in. `Manifest` is untouched.

Alternative considered: add a `Source *Source` field to `runs.Manifest`.
Rejected — `Manifest`'s existing fields (`Hops`, `Device`, `Stack`) each
carry a carefully-documented nil-means-"not recorded" contract that every
adapter, `WriteManifest`/`ReadManifest`, and dozens of hand-constructed
test manifests already have to agree on. Provenance has nothing to do with
diffing (`diff.Build` never needs to know where a run came from) and
doesn't need to be dragged into that contract. A sidecar mirrors the
existing pattern adapters already use for out-of-band per-run facts
(`device.json`), but stays outside the diff engine's contract entirely: a
run with no `source.json` is unambiguously local, and every existing writer
of `manifest.json` (every adapter, `retrace run` itself) needs no change.
`retrace/serve.Item` gains an optional `Source *runs.Source` field, read
via a new `runs.ReadSource`, so both `retrace serve` and the ensemble tab
show the same badge from the same code.

### D4: `ensemble/server` imports `retrace/serve` + `retrace/sync` directly
No new process, no subprocess exec, no HTTP proxy to a `retrace serve`
port. `ensemble/server` takes `retrace` as a Go module dependency and:
- calls `serve.BuildQueue(serve.Deps{...})` /`serve.SummaryFor` for `GET
  /api/retrace/queue` and `GET /api/retrace/queue/{app}/{flow}` — the exact
  functions `retrace serve`'s own handlers call, so the two surfaces cannot
  disagree about a verdict;
- re-serves shot images at `GET /api/retrace/shots/{app}/{flow}/{side}/
  {name}` using the same `SummaryFor` + checkpoint-lookup path
  `retrace/serve/routes.go`'s `handleShot` uses;
- calls `sync.Run(sync.Options{...})` (the same function the CLI's `retrace
  sync` command calls) for `POST /api/retrace/sync`, so the dashboard's
  "Sync now" button and the CLI command are one implementation.

Alternative considered: proxy to a separately-run `retrace serve` process.
Rejected per the scoping conversation — `ensemble up` is meant to be the
one thing a developer starts; a second required process for a "read the
results" feature reintroduces the exact three-minute-detour problem this
change exists to remove. Alternative considered: reimplement queue-building
in `ensemble/server`. Rejected — `retrace/serve`'s own doc comments are
explicit that `ScoreOf`/`itemOf`/`EmptyReasonFor` are each meant to have
exactly one definition; a second Go implementation is the kind of drift
this codebase has repeatedly paid down.

`retrace.Cfg` for these routes comes from `retrace/config.Discover(dir)`
where `dir` defaults to the same directory as `ensemble.yaml` (matches
`sample/`'s layout, where `.ensemble/` and `.retrace/` are siblings) and is
overridable via an optional `retrace: { dir: <path> }` block in
`ensemble.yaml` for stacks where they aren't. Discovered once at `ensemble
up` startup and re-discovered on the same config-reload path `ensemble`
already has (mirrors `retrace serve`'s own `reloadConfig`), so a `retrace
ref rule` run from the CLI is reflected in the dashboard without a
restart.

No new selection logic is needed for "local vs CI, which one shows": each
app/flow is already one `Item` keyed by `(app, flow)`, and `SummaryFor`
already diffs against the newest eligible run (`FindRun(..., "latest")`).
Because `NewRunID` embeds a timestamp, a CI run newer than the developer's
last local run for that flow becomes "latest" and its verdict is what the
table shows — exactly the ambient-visibility behavior the proposal wants,
with no change to `runs.FindRun` or `BuildQueue`'s ordering.

### D5: Shared diff-viewer components move to `dashboard/design-system`
`ShotCompare`, `WireDiffTable`, `HopDeltaList`, and `CaptureBanner` (and
the wire types they render: `CaptureTrust`, `Verdict`, `HopDiff`, `Route`,
`Entry`, `FieldDiff`, `Section`, `CheckpointVerdict`) move from
`dashboard/retrace-ui/src/components` + `api/types.ts` into
`dashboard/design-system`, re-exported from both `retrace-ui` and the new
`ensemble-ui` `RetraceView`. `ShotCompare` currently imports `retrace-ui`'s
`api/client` to build a shot's image URL; that becomes a `resolveShotUrl:
(app, flow, side, name) => string` prop instead, since `ensemble-ui` serves
shots from `/api/retrace/shots/...` and `retrace-ui` from `/api/shots/...`.

Alternative considered: fork a slimmer copy of each component into
`ensemble-ui`. Rejected for the same reason as D4's alternative — two
implementations of "how a wire diff renders" is exactly the kind of
divergence this codebase's own comments repeatedly warn against, and the
components are already presentational (their only non-prop imports are
`@ensemble/design-system` and their own types) — cheap to lift, expensive
to let drift.

### D6: Sync is manual and idempotent by directory presence
`POST /api/retrace/sync` (and the CLI) runs on request only — no
background polling, matching the "Sync now" + "last synced Xh ago" lean
from scoping. `sync.Run` treats a run directory that already exists on
disk (`.retrace/runs/<app>/<flow>/<run-id>/`) as already-synced and skips
it — no separate sync-state file. This is safe because `NewRunID`'s
timestamp+sha already makes re-downloading the same CI run produce the
same run-id; the directory-existence check is the whole idempotency
mechanism.

## Risks / Trade-offs

- **`gh` not installed/authenticated** → `retrace sync --from github`
  fails immediately with a message naming `gh auth login`, rather than a
  generic network error; the ensemble tab's "Sync now" surfaces that same
  message inline instead of a bare 500.
- **`gh run download` output shape drift** (GitHub could change artifact
  zip layout) → `sync` validates that a downloaded artifact contains at
  least one `manifest.json` under an `<app>/<flow>/<run-id>/` shape before
  merging anything in; an artifact that doesn't match is reported and
  skipped, never partially merged.
- **`ensemble` taking a dependency on `retrace`** couples two products that
  were previously independent (`AGENTS.md` describes them as two products
  sharing only `core/`). Mitigation: the dependency is one-directional and
  entirely inside the Go workspace (no new external dependency), gated
  behind the optional `retrace:` config block — a stack with no `.retrace/`
  directory and no config block runs `ensemble up` exactly as before, with
  an empty Retrace tab.
- **Artifact retention (7-day GH default)** means `retrace sync` run less
  often than weekly loses CI history for that gap. Accepted for v1 per the
  proposal's lean; the S3 backend (deferred) is the fix if a team hits
  this.

## Migration Plan

Additive throughout — no existing route, config field, CLI flag, or wire
type changes shape. Rollout is: land `retrace/runs` sidecar + `retrace
sync` + CI template first (usable standalone via `retrace serve` even
before the ensemble tab exists), then the `ensemble/server` routes, then
the dashboard tab. Each stage ships working, testable software on its own.
No rollback concerns beyond reverting the change — nothing migrates data
in place.

## Open Questions

- Multi-repo sync (a stack in repo A wanting CI results from repo B) is
  out of scope for this design; `--repo` takes one value. Extending it to
  a list is additive if a real need shows up.
