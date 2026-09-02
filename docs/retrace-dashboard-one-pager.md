# Retrace review dashboard — cleanup & findings

**Branch:** `fix/retrace-queue-skip-maestro-artifact-manifests` (11 commits — see `git diff origin/main..HEAD`)
**Context:** driven from a real multi-surface E2E grid (ux-toolkit: web + 6 mobile cells), reviewed in both the standalone `retrace serve` UI and ensemble's embedded Retrace tab.

Both dashboards render the queue and detail through the **same shared
`dashboard/design-system` components**, so every UI change below lands in
`retrace serve` (standalone) *and* `ensemble-ui`'s Retrace tab at once.

---

## What changed (proposed for upstream)

### 1. Queue no longer buries the reviewer in a real project
- **`fix: skip Maestro artifact trees` (`0e0e857`)** — `appIsReal` accepted any
  `manifest.json` as proof a directory was a retrace app. A Maestro debug
  bundle carries its own `manifest.json` (schema `maestro-schemas/
  artifact-manifest`), so `retrace sync`'d `tests/<timestamp>/<flow>` dumps
  listed as dozens of quarantined "no reference" rows and buried the real
  ones. Now a dir is a real app only if a `manifest.json` **parses as a
  retrace manifest** (`runs.ReadManifest`, schema `retrace/1`). Regression
  test reproduces the exact polluting row.
- **`feat: one row per app/flow` (`90d5ec0`)** — the queue rendered a
  full-width `CaptureBanner` + gate list **under every row**, and on a
  multi-root dashboard showed the same app/flow more than once. Reduced to
  one row per app/flow (`dedupeByKey`, latest run wins) showing app, flow,
  latest verdict, and a short reason **code** in a `details` column (full
  text in a `title=` tooltip). Clicking a row opens the detail page; no
  inline accordion.
- **`fix: compact capture-error presentation` (`5add1a4`)** — a quarantined
  flow printed the same "capture not assessed" sentence ~4x. `CaptureBanner`
  now collapses two identical sides to one line ("reference & candidate: ...")
  with the one-time hint shown once; the detail screen drops the banner and
  gate list for quarantined flows since the quarantine block already states
  the reason.

### 2. Detail screen tells the reviewer what they're looking at
- **`fix: label reference vs candidate` (`b9ab75f`)** — the header spelled out
  neither side (reference in a `title=` tooltip only). Now shows
  `reference: <baseline> -> candidate: <run under review>` explicitly, and
  quarantine reasons read `reference`/`candidate` instead of `side a`/`side b`.
- **`fix: badge unexpected HTTP status on its wire row` (`1780e92`)** — a call
  that returned e.g. 502 on **both** sides diffs as `identical` (no
  statusChange), so the 502 was invisible on the row and lived only in a
  detached gate line. `summary.unexpectedStatuses` is now threaded into
  `WireDiffTable` and badged on the matching row (`status 502`); its counts
  read `identical shape · status 502`.

### 3. Sync uses the config it already has
- **`feat: prefill sync repo from retrace.repo.yaml` (`4f53c6e`)** —
  standalone `retrace serve`'s Browse-&-sync panel made the reviewer type the
  GitHub repo, even though `retrace.repo.yaml` already declares `repo:` and a
  `sync:` block (`repoconfig` parses both; `cmd_serve` only used them for
  `--watch`). Added `serve.SyncConfig` + `GET /api/sync/config`, a repo
  fallback in the sync handlers, `retraceClient.syncConfig()`, and panel
  prefill. No configured repo -> the manual form, unchanged. ensemble-ui
  unaffected (its repos come from `ensemble.yaml`).

### 4. Geometry mismatch can yield a wire verdict instead of quarantining
- **`feat: geometry_mismatch: wire-only` (`fb399aa`)** — a geometry mismatch
  quarantined the whole run (no half-pass), correct when the pair is a
  harness mistake but wrong when device sizes legitimately differ between
  environments (CI emulator vs local) and the flow's real assertion is the
  **client-side wire**. New opt-in config `geometry_mismatch: wire-only`
  (default `strict` unchanged) skips the pixel plane, records a
  `GeometryNote`, and lets wire/hop produce a real verdict. A broken
  **capture** still quarantines (ordering preserved). Verified: a
  flutter-android flow that was "not compared" (1080x2424 ref vs 320x640
  candidate) now `pass` with 10/10 wire paired.

### 5. Mobile CI now syncs as real retrace runs (the big one)
The original symptom — mobile runs landing as junk `tests/<timestamp>` apps —
had a real two-sided cause, fixed in both repos:
- **`fix: reject non-retrace manifests at ingest` (`3fcca86`, ensemble)** —
  `retrace sync` derived `app/flow/run-id` from the PATH of ANY `manifest.json`
  it found. A mobile artifact bundling Maestro's `~/.maestro/tests/` tree
  carries a look-alike `manifest.json` (schema `maestro-schemas/artifact-
  manifest`), so sync built a `tests/<timestamp>/<cell>` app. Now
  `runs.ReadManifest` must parse it as a retrace manifest (schema `retrace/1`)
  first — the same guard `appIsReal` applies at the queue, moved up to ingest
  so junk never lands on disk regardless of what a workflow uploads. Regression
  test included.
- **`fix: mobile replay produces a syncable bundle` (`332ca13d`, ux-toolkit)** —
  the ROOT cause: Maestro's `takeScreenshot` writes to `~/.maestro/tests/`, not
  `RETRACE_RUN_DIR`, so the replay run dir had no `shots/` and could not sync
  as a retrace run. `scripts/retrace-maestro-shots.sh` now wraps Maestro
  (`--debug-output --flatten-debug-output`) and copies the named screenshots
  into `RETRACE_RUN_DIR/shots/`, giving the mobile replay the same
  `<app>/<flow>/<run-id>/shots/` shape a web Playwright replay has — which
  `findWebReplayBundles` turns into a real run (checkpoint names already match
  the reference). Both cell actions stop uploading `~/.maestro/tests/` in the
  retrace artifact (it moves to a separate `maestro-debug-*` artifact for
  triage), so sync never sees it.

### 6. One-click "pull latest" across lanes
- **`feat: pull latest` (`68c40e0`)** — the sync panel only pulled one CI run
  at a time then made the reviewer pick a flow. Added a `pull latest` button
  that syncs the freshest artifact-bearing run per workflow in one call and
  lands on the refreshed queue (one row per app/flow at its latest verdict).

---

## Findings for the ensemble/retrace team (context / smaller items)

1. **Replay emits no group markers, so a replayed candidate can't pair against
   a grouped reference.** The retrace fixture helper only posts group markers
   when `RETRACE_MARKER_URL` is set — which `retrace run` (record) sets but
   `retrace replay` does not (it binds the proxy `--listen` but no marker
   server). A committed reference recorded *with* groups therefore shows every
   call as `wireMissing` against a replayed candidate with `groups.b=[]`
   ("before any marker"). CI runs `replay`, so this is the common case.
   **Suggested fix:** bind a marker server during `replay` too, so a replayed
   candidate's group structure matches the reference it is diffed against.

2. **The embedded ensemble server is missing the per-flow detail routes.**
   Standalone `retrace serve` registers `/queue/{app}/{flow}/runs`, `/shots`,
   `/videos`, `/evidence`; `ensemble/server`'s `/api/retrace/...` mount does
   not, so drilling into a flow in ensemble-ui returns the SPA shell ("no
   runs") and no images/video. Detail review works only on standalone today.

3. **Cautionary data quality (project-side, noted for the runbook):** a
   committed reference had a **502 frozen into it** (flutter-ios call #4),
   producing a `failed` that diffs as "identical" — i.e. a bad baseline was
   accepted. And two mobile runs quarantined for **broken captures** (retrace
   proxy died mid-run: `dial tcp 127.0.0.1:4800`; and a test that failed
   during capture). These are exactly the states the dashboard should — and
   now cleanly does — surface; the fix is upstream capture reliability +
   "don't `ref accept` an unverified run."

---

## Verification
- `go test ./retrace/...` — all green, incl. new
  `TestBuildQueueSkipsAMaestroArtifactManifest`,
  `TestGeometryMismatchWireOnlyDowngradesToAWireVerdict`,
  `TestMaestroArtifactDumpIsSkippedNotSyncedAsTestsApp` (sync-ingest guard).
- `dashboard/design-system` — `tsc --noEmit` clean, 81 vitest pass.
- `make ui` — both UI bundles build; `ensemble-ui` and `retrace-ui` typecheck.
- End-to-end: `/api/sync/config` returns the configured repo; flutter-android
  geometry-mismatch flow verified `pass` with wire paired under `wire-only`.

---

## All-flows real-state snapshot (ux-toolkit grid, wire-only active)

| Flow | Verdict | Real cause |
|------|---------|-----------|
| flutter-android / card-views | pass | wire-only geometry policy; 10/10 wire paired |
| native-android / card-views | pass | committed reference matches |
| rn-ios / deep-link-switch | pass | — |
| web / card-views | changed | replay emits no markers -> 6 wire unpaired (finding #2) |
| rn-ios / card-views | changed | 2 genuinely missing calls |
| flutter-ios / card-views | failed | real 502 on call #4 frozen into reference (finding #4) |
| native-ios / card-views | quarantined | broken capture — proxy died mid-run (finding #4) |
| rn-android / card-views | quarantined | test failed during capture (finding #4) |

No fake green: every non-pass is a genuine, correctly-labeled state.
