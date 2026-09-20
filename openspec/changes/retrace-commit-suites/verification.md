# Commit suites verification

## Acceptance exercised

- A versioned expected inventory drives every denominator; missing lanes and
  missing flows remain visible. Optional platform exclusions are explicit.
- Three separate web/iOS/Android job reports with the same source, baseline,
  and policy join one build. Synthetic browser fixture: 12/15 cells passed,
  one iOS visual failure, one Android incomplete wire result, one Android flow
  not run. The fixture is labeled synthetic and kept separate from real data.
- Selecting a feature/platform opens only those flows. Required plane states,
  reasons, and earlier failed attempts remain inspectable. URL selection
  survives refresh; returning to the queue restores keyboard navigation.
- Clean commits, dirty snapshots, baselines, policies, and inventory versions
  do not silently merge. Latest finished results win per flow/platform.
- Runner assertions stay distinct from comparison artifacts. Existing original,
  candidate, diff, overlay, and wire viewers remain available. Run links pin
  the candidate ID; their current-reference recomputation is disclosed.
- Invalid input, missing exact evidence, and unreadable reports fail visibly.
  Imports cannot replace an existing attempt ID. Embedded evidence endpoints
  are read-only and preserve Host/Origin protection and mapped source roots.

## Automated checks

Run from the repository root:

```sh
go test -race ./core/... ./ensemble/... ./retrace/...
go vet ./core/... ./ensemble/... ./retrace/...
pnpm -r --if-present test
pnpm --filter retrace-ui build
pnpm --filter ensemble-ui build
```

Semantic mutation checks exercised missing-as-pass, dirty source identity,
retry selection, JSON case aliases, hidden errors, duplicate publication,
wrong source roots, embedded POST access, mapped screenshots, displayed plane
states, exact-run fallback, screenshot link identity, queue context recovery,
and JS report validation. Assertions rejected each mutation; code restored.

## Browser recording method

The local acceptance flow uses the Playwright checkpoint adapter with strict
handshake enforcement. It records desktop and narrow-screen flows separately,
then compares two finalized captures by exact run IDs with a zero-pixel-change
budget. Actual dashboard API requests pass through the Retrace proxy; static
assets load directly from the dashboard server. This avoids conflating asset
fetch order and ephemeral proxy origins with application API behavior. No
additional ignore rules or widened thresholds are used.

The initial full-page network recording had zero screenshot differences but
reported changing local proxy ports and parallel asset order. Those recordings
were retained as diagnostics, not accepted as clean network evidence.

## Application evidence limits

The private white-label application review inventory contains 60 expected
flows on three platforms. Historical native and web captures have different
verified dirty source identities and stay in separate builds. Native scoped
functional/wire results do not imply complete visual parity. Failed historical
web captures remain incomplete, even where individual case partitions looked
clean. No historical capture is relabeled as the current clean commit.

This release provides aggregation, report import, and review. Existing runners
still execute the tests. Automated white-label runner export, coordinated
same-source native/web campaigns, CI artifact transport, and artifact retention
policy are follow-up integration work.

## Final recorded verdicts (2026-09-20)

| Flow | Pixel checkpoints | API requests | Structured verdict |
| --- | ---: | ---: | --- |
| Standalone desktop | 3 unchanged | 2 unchanged | pass |
| Embedded desktop | 3 unchanged | 2 unchanged | pass |
| Standalone narrow screen | 1 unchanged | 2 unchanged bodies/headers, reordered | changed |

All six captures finalized with capture trust `ok`, no missing/extra requests,
no gate failures, and no unmeasured gates. Only the existing HTTP-date built-in
suppression applied. The narrow-screen finding is the two concurrent read-only
API requests arriving in the opposite order; it remains visible and was not
suppressed or accepted away.

Real historical Taxi pair images were also checked in both hosts: reference,
candidate, diff, and overlay loaded from their mapped roots. Failed-capture
warnings remain visible. This verifies viewer access, not Taxi acceptance.
