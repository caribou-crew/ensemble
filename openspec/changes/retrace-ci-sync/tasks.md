## 1. Run provenance sidecar (`retrace/runs`)

- [x] 1.1 Add `retrace/runs/source.go`: `Source` struct (`Kind`, `Workflow`,
      `RunURL`, `SHA`, `SyncedAt` — JSON tags matching the wire-type
      conventions the rest of the package uses), `WriteSource(p Paths, s
      Source) error`, and `ReadSource(p Paths) (*Source, error)` returning
      `(nil, nil)` when `source.json` doesn't exist (never an error for the
      ordinary "this run is local" case).
- [x] 1.2 Unit tests: write then read round-trips; `ReadSource` on a run
      directory with no `source.json` returns `(nil, nil)`; `ReadSource` on
      a malformed `source.json` returns an error, not a zero `Source`.
- [x] 1.3 Confirm (with a test) that `runs.ReadManifest`/`WriteManifest` are
      untouched and unaware of `source.json` — the diff engine must never
      import `retrace/runs.Source`.

## 2. `retrace/serve`: surface provenance on the queue

- [x] 2.1 Add `Source *runs.Source` (`json:"source,omitempty"`) to
      `serve.Item` in `retrace/serve/queue.go`.
- [x] 2.2 In `itemOf`, populate `Source` via `runs.ReadSource` on the `B`
      side's run directory (the run under review); leave nil on `brokenItem`
      rows (nothing was assessed, so nothing is known about where it came
      from).
- [x] 2.3 Tests: `BuildQueue` over a runs tree with one local and one
      `source.json`-carrying run reports `Source: nil` and `Source:
      &Source{Kind:"ci",...}` on the respective items; existing
      `queue_test.go` cases (no sidecar anywhere) still pass unchanged.

## 3. `retrace/sync` package: GitHub backend

- [x] 3.1 Create `retrace/sync/sync.go`: `type Options struct { Cwd, Repo,
      Workflow string; Since time.Duration }` and `type Result struct {
      Synced []string; Skipped []SkipReason }` (wire-shaped, since this
      backs both the CLI's `--json` output and the ensemble REST route).
- [x] 3.2 Add `github.go`: shell out to `gh run list --repo <repo> --json
      databaseId,workflowName,headSha,url,createdAt [--workflow <name>]`
      (parse via `encoding/json`, filter by `Since` in Go rather than
      relying on `gh`'s own date flags), then `gh run download <id> --repo
      <repo> --dir <tmp>` per qualifying run into a temp directory.
- [x] 3.3 Add a clear, fast-failing check: if `exec.LookPath("gh")` fails,
      return an error naming `gh auth login` before any `gh` invocation is
      attempted (spec: "`gh` is missing" scenario).
- [x] 3.4 Add the merge step: walk each downloaded artifact's temp
      directory for `**/manifest.json`, derive `<app>/<flow>/<run-id>` from
      its parent path, and copy that whole run directory into
      `runs.RunsRoot(cwd)/<app>/<flow>/<run-id>/` — but only if that
      destination directory does not already exist (idempotency: an
      existing directory means already-synced, per design D6). Write
      `source.json` (Task 1.1) into the copied directory afterward with
      `Kind: "ci"`, the workflow name, run URL, and SHA from the `gh run
      list` record, and `SyncedAt: now`.
- [x] 3.5 An artifact directory with no `manifest.json` anywhere under it
      is recorded in `Result.Skipped` with a reason and nothing from it is
      written (spec: "Malformed artifact is skipped" scenario).
- [x] 3.6 Tests using a fake `gh` on `PATH` (a test script/binary that
      prints canned `gh run list` JSON and pre-populates the `--dir` a `gh
      run download` call would have written to) covering: first sync merges
      everything in range; second sync with no new runs merges nothing;
      malformed artifact is skipped; `Since` filtering excludes older runs.

## 4. `retrace sync` CLI command

- [x] 4.1 Add `retrace/cmd/retrace/cmd_sync.go`: flags `--from
      github|s3` (only `github` implemented; `s3` errors "not yet
      supported"), `--repo`, `--workflow`, `--since` (duration, default
      `7d`), `--json`. Delegates entirely to `sync.Run` (Task 3.1) — no
      logic duplicated from the package.
- [x] 4.2 Wire `case "sync": return cmdSync(args[1:], stdout, stderr)` into
      `retrace/cmd/retrace/main.go`, matching the existing `ref`/`replay`
      dispatch pattern.
- [x] 4.3 `--json` prints `sync.Result` as the CI/agent-readable
      contract; without it, print one line per synced run and one line per
      skipped artifact with its reason.
- [x] 4.4 CLI tests mirroring `cmd_ref_test.go`'s process-level style:
      missing `--repo` is a usage error; a successful sync's `--json`
      output round-trips through `sync.Result`'s JSON tags.

## 5. CI workflow template

- [x] 5.1 Add `docs/retrace-ci-example.yml`: one job per app running
      `retrace run --app <app>`, uploading `.retrace/runs` (full tree, so
      the `<app>/<flow>/<run-id>` structure survives) as
      `retrace-runs-<app>-${{ github.sha }}` (short retention, e.g. 7
      days), and a separate `retrace export --out ./report --json` step
      whose exit code gates the job (unchanged existing behavior — this
      step needs no changes, just needs to be shown alongside the new
      upload step).
- [x] 5.2 Add a short doc section (`docs/retrace-ci-sync.md`) explaining:
      what `retrace sync --from github --repo org/repo` expects to find
      (the `retrace-runs-<app>-*` artifacts), and the CLI-first agent
      recipe (`retrace diff --flow <flow> --app <app> --json`) as the
      documented LLM-integration path — no MCP server in this change.

## 6. `ensemble/config`: optional `retrace:` block

- [x] 6.1 Add `RetraceConfig` (`Dir string`, optional — defaults to the
      directory containing `ensemble.yaml` when the block is present but
      `dir` is omitted, per `service-freshness`'s precedent for optional
      blocks with defaults) to `ensemble/config/config.go` as `Retrace
      *RetraceConfig`.
- [x] 6.2 Validation: if `Retrace` is set and `Dir` is a relative path,
      resolve it relative to the config file's own directory (mirroring how
      service `Dir`s already resolve).
- [x] 6.3 Config tests: block absent; block present with an explicit `dir`;
      block present with `dir` omitted (defaults to the config directory).

## 7. `ensemble/server`: retrace routes

- [x] 7.1 Add `require github.com/caribou-crew/ensemble/retrace` to
      `ensemble/go.mod`; confirm `go build ./ensemble/...` succeeds and `go
      vet ./core/... ./ensemble/... ./retrace/...` reports no import-cycle
      or other issue (there is none: nothing under `retrace/` imports
      `ensemble`).
- [x] 7.2 Add `ensemble/server/retrace.go`: resolve the effective retrace
      directory from `config.Retrace`, build a `retrace/serve.Deps` via
      `retrace/config.Discover(dir)`, and register `GET
      /api/retrace/queue`, `GET /api/retrace/queue/{app}/{flow}`, `GET
      /api/retrace/shots/{app}/{flow}/{side}/{name}` delegating straight to
      the exported `serve.WriteQueue` / `serve.ResolveFlow` /
      `serve.WriteItem` / `serve.WriteShot` (import and call them directly
      — do not copy their bodies). Routes are always registered; a nil
      `config.Retrace` answers 501 in JSON, mirroring the existing `Insp`
      nil-disables-with-501 pattern — see the reconciled requirement in
      `specs/ensemble-retrace-view/spec.md` (the dashboard's SPA fallback
      answers any unmatched path with 200, so "not registered" cannot be
      told apart from a typo without a route that always answers in JSON).
- [x] 7.3 Add `POST /api/retrace/sync`, decoding the stack's configured
      sync source (`RetraceConfig.Repo`/`Workflow`/`Since`, already added
      in Task 6.1) and calling `sync.Run` (Task 3.1) directly; on error,
      respond with the same error text the CLI would print (spec: "Sync
      failure surfaces inline").
- [x] 7.4 There is no config-reload trigger in `ensemble/server` (unlike
      `retrace serve`'s own `reloadConfig`) to mirror — resolved instead by
      calling `retrace/config.Discover(dir)` fresh on every request rather
      than caching, so a `retrace ref rule`/`accept` run from the CLI is
      reflected without restarting `ensemble up`, with no reload machinery
      needed.
- [x] 7.5 Route tests (mirroring `retrace/serve/routes_test.go`'s style):
      queue/item/shot routes return identical JSON to calling
      `retrace/serve` directly against the same `.retrace/` fixture; no
      `retrace:` config means the routes answer 501 in JSON rather than
      404 or 500; sync route success and `gh`-missing failure paths.
- [x] 7.6 Add the new routes to `ensemble/server`'s OpenAPI doc
      (`openapi.go`) — per `AGENTS.md`'s API-first-parity rule, these routes
      exist so this is not optional.

## 8. `dashboard/design-system`: shared diff-viewer components

- [x] 8.1 Move `ShotCompare.tsx`/`.css`/`.test.tsx`, `WireDiffTable.tsx`/
      `.css`/`.test.tsx`, `HopDeltaList.tsx`/`.css`, and
      `CaptureBanner.tsx`/`.css`/`.test.tsx` from
      `dashboard/retrace-ui/src/components/` into `dashboard/design-system/
      components/`, along with the wire types they render (and the
      supporting types those depend on: `TrustReason`, `Gap`, `Rect`,
      `HeaderDiff`, `ServiceCount`, `RouteFailure`, `StatusFinding`)
      currently in `retrace-ui/src/api/types.ts`, into a new
      `design-system/diffTypes.ts`. `retrace-ui/src/api/types.ts`
      re-exports them from there so existing call sites (App.tsx,
      api/client.ts, etc.) are unchanged.
- [x] 8.2 Change `ShotCompare`'s image-URL construction: replace its
      `import { api } from '../api/client'` with a `resolveShotUrl: (app:
      string, flow: string, side: string, name: string) => string` prop
      (exported as `ResolveShotUrl`).
- [x] 8.3 Update `dashboard/design-system/package.json` exports to include
      the four components and `diffTypes` (one subpath per file, matching
      the existing `./useAsync` pattern).
- [x] 8.4 Update `dashboard/retrace-ui` to import these from
      `@ensemble/design-system` instead of its local `components/`/
      `api/types.ts`, passing a `resolveShotUrl` that builds `/api/shots/
      ...` URLs (its existing behavior, now explicit). `QueueList.tsx`'s
      own `CaptureBanner` import was also repointed (not previously
      listed, but broke on the move).
- [x] 8.5 Run `pnpm -r --if-present test` and fix any import path fallout;
      confirm `retrace-ui`'s existing component tests still pass unchanged
      in their new location (moved, not rewritten).

## 9. `dashboard/ensemble-ui`: Retrace tab

- [ ] 9.1 Add `api/types.ts` additions for the queue response shape (`Item`
      per `retrace/serve.Item`, including the optional `source` field) and
      `api/client.ts` functions for `GET /api/retrace/queue`, `GET
      /api/retrace/queue/{app}/{flow}`, and `POST /api/retrace/sync`.
- [ ] 9.2 Add `views/RetraceView.tsx`: a table (app, flow, verdict, what
      changed, when, source badge) from `GET /api/retrace/queue`, a "Sync
      now" button calling `POST /api/retrace/sync` then re-fetching the
      queue (no auto-poll — matches `LatencyView`'s manual-refresh pattern,
      not `ServicesView`'s timer), and a "last synced" indicator.
- [ ] 9.3 Row selection renders inline detail using `ShotCompare`,
      `WireDiffTable`, `HopDeltaList`, and `CaptureBanner` from
      `@ensemble/design-system` (Task 8), fed by `GET
      /api/retrace/queue/{app}/{flow}` and `resolveShotUrl` built against
      `/api/retrace/shots/...`.
- [ ] 9.4 Register the tab in `App.tsx` alongside Services/Topology/
      Traffic/Entities/Inspector, conditionally hidden when `GET
      /api/retrace/queue` responds 501 (no `retrace:` block configured) —
      mirroring how `service-freshness` badges are omitted rather than
      shown empty.
- [ ] 9.5 Component tests mirroring the existing views' style
      (`RetraceView.tsx` alongside `ServicesView.tsx`'s
      `.poll-race.test.ts`/`.stale-error.test.ts` conventions): queue
      loads and renders; row click loads and renders detail; sync failure
      shows inline, not a crash; tab is absent when the queue route 501s.

## 10. End-to-end verification

- [ ] 10.1 In `sample/` (which already has `.retrace/runs/brew/checkout/`
      fixtures), add a synced-looking run directory with a `source.json` by
      hand, run `ensemble up`, and confirm the Retrace tab shows both the
      local and the "CI" row with correct badges — use the `retrace-iterate`
      skill to capture and diff the dashboard change itself, per
      `AGENTS.md`'s "verifying a change you made to a client" rule.
- [ ] 10.2 `go test -race ./core/... ./ensemble/... ./retrace/...` and
      `pnpm -r --if-present test` both green.
