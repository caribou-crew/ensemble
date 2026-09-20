# Suite review workspace

The suite dashboard now opens recorded comparison evidence alongside a persistent
flow queue. The interaction follows the useful parts of Playwright Trace Viewer:
action selection remains beside the evidence, and evidence views switch in place.
Reference: https://playwright.dev/docs/trace-viewer#trace-viewer-features

- Default to unresolved results in feature inventory order. Search and filter by
  feature, platform, or result without changing the aggregate coverage counts.
- Choose a flow directly, use previous/next, or press J/K while focused within
  the review workspace. Typing in inputs does not trigger navigation.
- Show reference and candidate originals immediately, including unchanged
  checkpoints. Diff and overlay modes use the saved pair's images.
- Switch to wire traffic in place. Full comparison still exposes hops, budgets,
  capture details and other diagnostics; attempt history links exact evidence.
- Preserve selected flow/platform, result filter and search in URL parameters
  (`suiteFlow`, `suiteReviewPlatform`, `suiteFilter`, `suiteSearch`). This survives
  reload and returning from a full comparison. Unfiltered older URLs with an
  explicit flow show all statuses, so passing evidence remains reachable.
- Keep coverage matrix, source selection and provenance available in disclosures.
  Missing evidence and quarantined captures remain explicit. Imported report
  statuses never become visual approval merely because the images were viewed.

The shared components serve both standalone Retrace and embedded Ensemble. All
requests use the existing suites/pair APIs. No report schema, comparison policy,
acceptance state, or backend endpoint changed.

## Verification, September 20, 2026

JavaScript workspace suites pass (two existing Maestro tests skipped); all Go
packages pass. Both dashboard production builds pass. New tests cover initial
flow selection, search, restoration of passed targets, keyboard focus/typing,
exact pinned pair image URLs, side-by-side/diff/overlay modes, missing evidence,
failed/quarantined captures, and late responses. Semantic keyboard and image-mode
mutations fail their regression tests.

Local Taxi dogfood verification is private under WLA's
`.retrace/review-workspace-e2e`. Real browser tests exercised old entry/comparison
and new entry/comparison, next/reload, diff/wire, failed/search filters and reload,
and a 600px viewport without horizontal overflow. Both final captures report
capture trust `ok`. Earlier harness setup failures are retained, not used as
baseline evidence.

Old capture: `20260920T142910Z-f37a527`, 18 recorded calls.
New capture: `20260920T142921Z-f37a527`, 65 recorded calls.
Structured diff: `changed`, no failing gates, both capture sides `ok`. The two
shared desktop checkpoints intentionally changed; candidate-only failure-filter
and narrow-screen checkpoints were added. Wire changes include new navigation
checks and different UI assets, so this is not an assertion of wire parity.
The multi-viewport capture's device metadata describes its first viewport; the
600px shot is a layout check, not a cross-device pixel-parity claim.

These captures inherit the WLA harness checkout SHA, not the Ensemble library
revision. Their source provenance is this workspace change based on Ensemble
`4401e62`. They inspect retained Taxi evidence, not a fresh Taxi app run, and do
not approve the historical Taxi screenshots.
