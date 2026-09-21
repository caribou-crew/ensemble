# Suite build gallery

Approved intent: from a suite build, one click shows every flow across web, iOS and Android
(final screens for lanes without a reference diff, reference/candidate/diff for saved pairs),
with wire evidence called out explicitly, even when the lanes come from separate manual or CI runs.

## Decisions
- Built on `retrace-commit-suites` (builds, lanes, planes, evidence links); no change to grouping or
  rollup rules. Lanes merge into one build only when their source/baseline/policy identity agrees.
- Native images enter through the suite report (`screens: [{label,file}]`), copied by `retrace suite
  import` into content-addressed storage (`.retrace/suite-assets/`, deliberately outside
  `.retrace/suites/`, whose reader rejects unknown files). Stored reports hold hash/media/size only.
- Serving is allow-listed by the stored report and re-verifies the hash; raster types only.
- `wireNote` is a runner statement, never a verdict. The gallery's `wire.represented` is true only
  when a readable saved pair supplied counts; it is authoritative over any other field.
- Default scope is the newest finished result per lane across revisions, each tile labelled with its own
  revision (a per-build view is one select away). This is display only: build grouping and rollup are unchanged.
- Network evidence is a compact chip per row plus a separate tab ranked by missing/extra/violations; there are
  no per-tile warning panels, and an absent comparison is a dash, not a claim.
- API-first: gallery board and screens are REST routes (standalone `/api/suites/...`, embedded
  `/api/retrace/suites/...`); the UI composes image URLs from identifiers with existing pair routes.

## Fail-closed zero values
Absent screens, note, pair or result never read as evidence: a missing lane is "Not run", an
unreadable pair is reported, an unlinked result is "Wire diff not represented".

## Out of scope
Native reference diffs, per-checkpoint galleries, editing/approval, fetching CI artifacts, merging
builds across differing source identities.
