# Review an application suite by commit

Retrace's **Suites** view collects independent runner jobs into one source
revision. A feature matrix shows web, iOS and Android side by side; selecting a
cell shows its flows, functional/wire/visual evidence, and retry history. Existing
run and cross-app comparison pages remain the detailed evidence viewers.

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

## Review a whole build in one page

Selecting a build opens its **Gallery** (the flow-by-flow queue is one click away
under **Flow review**; the choice is kept in the `suiteView` URL parameter). One
row per flow, one tile per platform lane:

- A result linked to a saved pair shows **reference, candidate and diff** for the
  pair's final checkpoint. A native result shows its attached screens.
- Every tile carries an explicit **wire callout**. When a readable saved pair
  exists it shows Retrace's own counts (paired, changed, missing, extra, moved,
  violations). Otherwise it says **"Wire diff not represented"** with the runner's
  `wireNote` (or a default reason) and the runner's asserted wire state, labelled
  as an assertion. An unreadable linked pair is reported as such, never as
  "unchanged".
- A lane with no imported result is a quiet "Not run" tile: no image, no wire
  evidence, never a pass.
- Filters: needs attention, wire not represented, wire changed, plus flow search.
- Counts are Retrace's, so approved-difference policies can still show entries as
  changed; compare with the runner's asserted wire state shown beside them.

Lanes appear together in one build only when the runners report the same suite
version, git sha, dirty/workspace identity, baseline and policy. A native run
tested on a newer commit than the web comparison is a separate build.

`GET /api/suites/{suite}/builds/{build}/gallery` returns the same board as JSON
(references and identifiers, never pixels); Ensemble exposes it under
`/api/retrace/suites/...`.

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
