# Review an application suite by commit

Retrace's **Suites** view collects independent runner jobs into one source
revision. A feature matrix shows web, iOS and Android side by side; selecting a
cell shows its flows, functional/wire/visual evidence, and retry history. Existing
run and cross-app comparison pages remain the detailed evidence viewers.

## Quick start

You need three things in one project directory (the one you run `retrace serve` from):
an inventory, results from your runners, and the server.

1. **Describe what you expect** in `retrace.suites.json` (next section): features, flows
   and the platforms you care about (`web`, `ios`, `android`).
2. **After each runner job, import one report per platform:**
   - *Web (reference vs candidate):* record both sides with `retrace run`, then
     `retrace diff --flow NAME` (see [comparing checkouts](comparing-migrations.md)). The
     comparison is saved under the candidate run as `.retrace/runs/<app>/<flow>/<run>/diffs/<pairId>`.
     Put `{app, flow, runId, pairId}` in the flow's `evidence` in the report.
   - *iOS / Android (no reference):* write the screenshot to a file and list it in the report as
     `screens: [{label, file}]`. No Retrace run is needed.
   - Build the JSON with `createSuiteAttempt` from `adapters/js` (or write it by hand) and run
     `retrace suite import --file report.json`. Reports are immutable; use a new `attemptId` per job.
3. **Look:** `retrace serve --addr 127.0.0.1:4800`, open it, choose **Comparison suites**. The
   Gallery is the default view of a suite.

### Reading the page

- **Table:** one row per flow. Web shows reference, candidate and diff; iOS and Android show their
  final screen. Click any thumbnail for the full image. Scroll and compare.
- **Revision line above the table:** each platform column shows its newest result and names the
  source revision it was produced on. Columns can come from different revisions.
- **Network chip / tab:** red = missing or extra requests or rule violations (look at these first),
  amber = only changed or moved exchanges (often approved header differences), green = no
  difference, dash = no saved comparison. The tab ranks flows and shows what the runner asserted.
- **"Coverage counts" (collapsed, above the table):** counts of *cells* (flow × platform) for one
  revision. A cell passes only when every required plane passes (functional, wire and visual), so
  `0 / 159 passed` is normal until a runner reports wire and visual as passed. It is a coverage
  ledger, not the visual verdict; the gallery is where you judge differences.
- Nothing is accepted by looking at it. Statuses are what the runners asserted.

### What others need

- A `retrace` binary built from this repository (Go, Node and pnpm; `make build` writes
  `bin/retrace` with the UI embedded). Suites and the gallery are not in a published package yet.
- The project directory with `retrace.suites.json`. If the web recordings live in other
  repositories, a `retrace.repo.yaml` naming each app's root (see `retrace suite` docs below).
- Web pairs: two Retrace recordings of the same flow with the same named screenshot checkpoints,
  compared with `retrace diff`. Without a saved pair, a web result shows no reference/diff images
  and no network chip.
- Native lanes: only screenshots (PNG, JPEG or WebP, up to 8 MiB) and a report; no adapter.
- A place to keep `.retrace/` (it holds screenshots and recordings; keep it private and ignored).
  There is no hosted mode: each person runs `retrace serve` against their own copy of that directory,
  so to share results, share or sync the `.retrace/suites` and `.retrace/suite-assets` directories
  together with the recordings the pairs point to.
- In CI: build the report from the job's results and run `retrace suite import`, then keep the
  `.retrace` directory as an artifact.

## Define the expected inventory

Place `retrace.suites.json` at the project root where you run `retrace serve`:

```json
{
  "schema": "retrace/suites/1",
  "suites": [{
    "id": "legacy-to-taxi",
    "title": "Legacy → Taxi",
    "version": "v1",
    "platforms": ["web", "ios", "android"],
    "features": [{
      "id": "authentication",
      "title": "Authentication",
      "flows": [{
        "id": "login",
        "title": "Sign in and reach Wallet",
        "requiredPlanes": ["functional", "wire", "visual"]
      }]
    }]
  }]
}
```

Every flow contributes one expected cell for each supported platform. A flow
can declare `platforms` to narrow that set. Explicitly unsupported platforms do
not enter its denominator; an expected platform with no result is **not run**.
At least one plane must be required. Configure the requirement because of what
the test needs to prove, never because a recent result failed.

## Import each runner job

Run your existing Playwright/Maestro/test command, then produce a JSON report
from its actual results. The JS adapter exports `createSuiteAttempt` to help
build this JSON. Non-JS runners can write the same contract directly:

```json
{
  "schema": "retrace/suite-attempt/1",
  "suiteId": "legacy-to-taxi",
  "suiteVersion": "v1",
  "attemptId": "web-job-173",
  "platform": "web",
  "git": {
    "sha": "0123456789abcdef0123456789abcdef01234567",
    "branch": "main",
    "dirty": false
  },
  "baselineId": "legacy-reference-2026-09-20",
  "policyId": "reviewed-parity-v1",
  "startedAt": "2026-09-20T06:00:00Z",
  "finishedAt": "2026-09-20T06:01:00Z",
  "results": [{
    "flowId": "login",
    "planes": {"functional": "pass", "wire": "pass", "visual": "incomplete"},
    "reason": "Visual reference not yet reviewed",
    "evidence": {"app": "taxi", "flow": "login", "runId": "candidate-run"}
  }]
}
```

The IDs above are illustrative. Use the exact source/build and baseline your
runner tested. `evidence` is optional; omit it if there is no corresponding
Retrace run. For a cross-app comparison add its `pairId` and use the candidate
app, flow and run IDs. The link opens the existing reference/candidate/diff and
wire viewer; an imported status does not create those artifacts.

Run evidence pins the candidate (B) to the exact run ID: it never resolves a
missing ID through `latest` or a Git SHA prefix. Suite evidence rejects the
reserved candidate aliases `latest` and `reference`. The comparison is recomputed
using the current reference and policy, so it does not reproduce the imported
suite verdict or establish the historical baseline associated with `baselineId`.
For historical baseline evidence, link a saved pair (`pairId`) made from
retained, concrete run IDs. Existing pair semantics remain unchanged: pairs
can use mutable reference-bundle aliases and can be regenerated. Imported
reports are immutable, but retaining their evidence artifacts and controlling
pair regeneration remain the runner’s responsibility. Both dashboards disclose
this distinction above suite run evidence.

```sh
retrace suite import --file suite-report.json
retrace serve --addr 127.0.0.1:4800
```

The importer validates the inventory, source identity, timestamps, platform,
flow IDs and all three explicit plane states, then stores an immutable report
under `.retrace/suites/<suite>/<attempt>.json`. Import iOS and Android reports
independently: no shared job/process or start time is needed. Keep `.retrace/`
private/ignored, as with normal captures.

The first version accepts one inventory version per suite. Changing a suite's
version while retaining reports for its old version produces a visible
validation error; archive the old review root or use a separate suite ID for a
new inventory. It does not reinterpret old results against a new denominator.

## Attach screenshots and wire notes

A result can carry the images a reviewer should see and a statement about wire
evidence you do not have. This is how a native lane with no Retrace run (for
example Maestro output for iOS or Android) shows its final screen next to the web
pair:

```json
{
  "flowId": "login",
  "planes": {"functional": "pass", "wire": "incomplete", "visual": "incomplete"},
  "screens": [{"label": "Final screen", "file": "shots/login.png"}],
  "wireNote": "No reference wire exists for this lane; only the recorded replay was checked."
}
```

- `file` is relative to the report file and must stay inside its directory. It
  must be a regular file (no symlink), a PNG, JPEG or WebP judged by its bytes
  (not its name), at most 8 MiB. At most 12 screens per result, with unique labels.
- `retrace suite import` copies each image into content-addressed storage under
  `.retrace/suite-assets/<suite>/<attempt>/<sha256>.<ext>` and publishes the report
  with the hash, media type and size. **A stored report never contains a path.** A
  rejected import writes no report and no assets.
- Images are served only at
  `GET /api/suites/{suite}/attempts/{attempt}/screens/{sha256}`, and only for a hash
  that the stored report references; the bytes are re-verified against the hash on
  every read, with `nosniff`. SVG and HTML are never accepted.
- `wireNote` (1-400 characters) is shown verbatim in the gallery. It never changes
  a plane: `wire` stays exactly what the runner asserted.
- The JS adapter accepts both fields in `record()` with the same limits.

## Review the suite in one page

Selecting a suite opens its **Gallery**, a scrolling table with one row per flow
(the flow-by-flow queue is one click away under **Flow review**; the choice is kept
in the `suiteView` URL parameter):

| Flow | Web reference | Web candidate | Web diff | iOS | Android | Network |
| --- | --- | --- | --- | --- | --- | --- |

- **Newest result per platform.** Each platform column shows its latest finished
  result across all source revisions, so a web comparison and a native run made on
  different commits sit on one page. The revision behind every column is named
  above the table (with any older revisions that contribute), and nothing is merged
  into a single build. Switch to **This source revision only** for one build's
  lanes. A lane with no result anywhere is an empty cell, never a pass.
- A result linked to a saved pair shows **reference, candidate and diff** for the
  pair's final checkpoint; a native result shows its attached screens. Click a
  thumbnail for the full image.
- The **Network** column is a small chip from Retrace's own comparison counts. Red
  means missing or extra requests or rule violations; amber means only changed or
  moved exchanges; green means no difference. No saved comparison means a dash.
- The **Network** tab lists every saved comparison with differences, most serious
  first (missing/extra/violations, then changed/moved), next to what the runner
  asserted for the wire plane. It also names lanes that have no network comparison
  and lists results without one (with their `wireNote`) in a collapsed section, and
  reports an unreadable linked pair instead of treating it as unchanged.
- Filters: needs attention, visual differences, network differences, plus flow search.
- Counts are Retrace's, so approved-difference policies (for example header changes)
  can still show entries as changed; read them beside the runner's assertion.

`GET /api/suites/{suite}/gallery` returns the newest-per-lane board and
`GET /api/suites/{suite}/builds/{build}/gallery` one build's, as JSON (identifiers
and references, never pixels; each tile carries its `source` revision). Ensemble
exposes both under `/api/retrace/suites/...`. A suite with no imported reports is a
404, not an empty board.

## Understand the rollup

- Build identity includes suite/version, full Git SHA, dirty snapshot, baseline,
  and comparison policy. Distinct baselines/policies never silently merge.
- Dirty sources require `workspaceId`. Use a stable digest or immutable source
  snapshot ID shared by the platform jobs; do not label different working trees
  with one convenient ID. Dirty builds never merge into a clean commit.
- Latest **finished** attempt wins per flow/platform; attempt ID breaks equal
  timestamps deterministically. Earlier failures stay available in history.
- A flow passes only when every required plane passes. Any required failed plane
  makes it failed; missing/incomplete evidence cannot make it pass. Optional
  planes still show their actual states.
- Plane states are `pass`, `failed`, `incomplete`, `not-run`, or
  `not-applicable`. A required plane cannot be not-applicable.
- Counts describe expected flow/platform cells, not screenshots or artifacts.
  The dashboard labels runner assertions and links to evidence rather than
  claiming it independently recomputed every imported comparison.
- A malformed configured report is an error, not a row silently removed from a
  green denominator. Empty suites never show 100% passed.

`GET /api/suites` exposes the same aggregate used by the standalone UI.
Ensemble exposes it under `/api/retrace/suites` for its configured project root.
The suite importer does not execute or schedule suites, fetch CI artifacts,
change tolerances, promote references, or infer pass from exit code alone.
