## Why

retrace already solves the local loop: record a flow, diff it, review the
result. Most teams run their visual/wire regression suite in CI on every
merge, across multiple apps (iOS, Android, React Native, web). Today those
results live as GitHub Actions artifacts or CI logs nobody looks at until
something is already broken. A developer working locally has no ambient
view of what's passing across platforms, whether their branch broke
something on a platform they aren't running locally, or what exactly
changed. When CI does fail, the loop is: find the workflow run, download
the artifact, unzip, open the HTML report — a detour multiplied across
apps and flows. The missing piece is a single view, in the dashboard the
developer already runs (`ensemble up`), that shows cross-app retrace status
at a glance with detail one click away, fed by both local and CI runs.

## What Changes

- Add `retrace sync --from github --repo org/repo [--workflow NAME]
  [--since 7d]`: downloads recent workflow-run artifacts (via the `gh` CLI)
  and merges the run directories they contain into the local
  `.retrace/runs/<app>/<flow>/<run-id>/` tree — the same tree `retrace run`
  writes to. No new backend or credential store; `gh`'s own auth
  (`gh auth token`, falling back to a `GITHUB_TOKEN` env var) is reused.
- Add a run-provenance sidecar (`retrace/runs`): every synced run gets a
  small `source.json` beside its `manifest.json` recording that it came
  from CI (workflow, run URL, commit SHA, synced-at time). A run with no
  sidecar is local — the zero-value case needs no new writer to touch.
  `retrace/serve`'s `Item` carries this through to any REST client.
- Add a CI workflow template (`docs/`) showing `retrace run` +
  `actions/upload-artifact` per app — no changes to `retrace run` or
  `retrace export` are required for this to work; sync consumes the run
  directory a normal `retrace run` already produces.
- Add a **Retrace tab** to the ensemble dashboard: a cross-app,
  worst-first table of every recorded flow (verdict, what changed, when,
  local vs CI), with a per-flow detail view (pixel diff, wire diff, hop
  diff) one click away — all rendered by `ensemble up`'s own server, so
  there is nothing extra to run. `ensemble/server` imports `retrace/serve`
  and `retrace/config` directly and calls the exact same `BuildQueue` /
  `SummaryFor` functions `retrace serve`'s HTTP handlers call, so the
  dashboard and `retrace serve` can never disagree about a flow's verdict.
  A "Sync now" button in the tab triggers `retrace sync` and refreshes;
  sync is manual, not polled, so the tab never makes a GitHub API call the
  developer didn't ask for.
- Document the existing CLI-first path for LLM/agent use (`retrace diff
  --json` is already structured for this) rather than building a new MCP
  server — no code change, a docs note only.

## Capabilities

### New Capabilities
- `retrace-sync`: pulling CI-recorded run artifacts from GitHub Actions
  into the local `.retrace/runs/` tree, with provenance recorded per run
  so downstream tooling can tell a CI run from a local one.
- `ensemble-retrace-view`: an ensemble dashboard tab and REST API surfacing
  the retrace review queue and per-flow diff detail without requiring
  `retrace serve` to be running as a separate process.

### Modified Capabilities
(none — no existing capability's requirements change)

## Impact

- `retrace/runs`: new `source.go` (Source type, `WriteSource`/`ReadSource`
  sidecar helpers). No change to `Manifest` or its schema.
- `retrace/serve`: `Item` gains an optional `Source` field, populated from
  the sidecar when present.
- `retrace/sync` (new package): GitHub artifact listing/download (shells
  out to `gh`) and the merge-into-`.retrace/runs` writer.
- `retrace/cmd/retrace`: new `sync` subcommand.
- `ensemble/config`: `Config` gains an optional `retrace:` block (path to
  the `.retrace` tree when it isn't next to `ensemble.yaml`).
- `ensemble/server`: new routes (`GET /api/retrace/queue`, `GET
  /api/retrace/queue/{app}/{flow}`, `GET
  /api/retrace/shots/{app}/{flow}/{side}/{name}`) backed by imported
  `retrace/serve` + `retrace/config` — a new intra-repo module dependency,
  `ensemble` on `retrace` (verified acyclic: nothing under `retrace/`
  imports `ensemble`).
- `dashboard/design-system`: gains the shared diff-viewing components
  (`ShotCompare`, `WireDiffTable`, `HopDeltaList`, `CaptureBanner`) lifted
  out of `dashboard/retrace-ui` so `dashboard/ensemble-ui` can render the
  same detail view without a second implementation.
- `dashboard/ensemble-ui`: new `RetraceView.tsx` tab, `api/types.ts`
  additions, a "Sync now" action that calls a new `POST
  /api/retrace/sync` route (which shells out to `retrace sync` using the
  stack's configured `retrace:` settings).
- `docs/`: a CI workflow template (`retrace-ci.yml` example) and a short
  recipe for the CLI-first agent loop (`retrace diff --json`).
- No impact to `core/proxy`, orchestration, tracing, entities, inspector,
  or `retrace run`/`retrace export`/`retrace ref`'s own behavior.
- Non-breaking: a stack with no `.retrace/` directory and no synced runs
  shows an empty Retrace tab with the existing "no runs yet" wording
  `retrace serve` already has; a stack that never runs `retrace sync` sees
  only its local runs, exactly as today via `retrace serve`.
