## Why

`retrace replay` only asserts one direction — did the client reach the
recorded responses. It never checks that the client's *requests* still match
what was recorded, because `replay.Bundle.Match` is a deliberately loose
subset match (extra request fields are accepted, and a repeated identical
call is served from the same recorded exchange without complaint). That lets
call-count drift, new/changed request headers or attributes, and restructured
request bodies replay perfectly green in CI, even though these are exactly
the client-side regressions a zero-backend gate exists to catch.

## What Changes

- Add `retrace replay --assert-requests`: replay additionally records every
  observed request (method, path, query, decoded + raw request body, request
  headers) as it serves it, and — for the exchanges that matched — diffs
  those observed requests against the reference bundle's recorded requests
  using the existing wire-plane comparison engine (`retrace/diff`), honoring
  the project's configured `wire_rules` and `query_ignore` tolerances.
- A request that hit the same recorded exchange more than once now surfaces
  as a surplus call (bucketed by method + normalized path + normalized
  query, same bucketing `retrace diff` uses) instead of silently succeeding
  N times.
- A request that matched under `Match`'s subset rule but carries a field,
  header, or attribute the recording never declared is now reported as a
  request-side deviation, gated the same way `retrace diff`'s wire plane is
  (`gates.wire.budget_pct`, `fail_on`).
- `--json` gains an `extra` array (observed requests with no recorded
  counterpart — new endpoints or surplus calls) as a sibling of `unused` and
  `misses`, plus a `requestDiff` section reporting any field/header
  deviation found on a matched call.
- Exit behavior: a request-side deviation beyond the configured budget is a
  hard gate, exiting the same code an unmatched-call miss already does today
  (2).
- **Backward compatible**: without `--assert-requests`, replay's behavior,
  report shape, and exit codes are unchanged.

## Capabilities

### New Capabilities

(none — this extends an existing capability's behavior)

### Modified Capabilities

- `retrace-capture-replay`: the "Strict replay" requirement gains an
  opt-in request-side assertion mode that catches call-count, header, and
  request-body-shape drift the response-matching mode cannot see.

## Impact

- `retrace/cmd/retrace/cmd_replay.go` — new `--assert-requests` flag, report
  shape (`extra`, `requestDiff`), exit-code wiring.
- `retrace/replay/server.go`, `retrace/replay/match.go`, `retrace/replay/bundle.go`
  — `Server` optionally records each observed request as a comparable
  record; `Bundle`/`Match` expose what a hit actually matched against.
  No change to existing matching/serving behavior.
- `retrace/diff` (`wire.go`, `summary.go`) — reused as the comparison and
  budget-gate engine; only exported surface may grow (a way to run the
  existing pairing/diff logic over two in-memory hop slices without the
  rest of `diff.Build`'s pixel/hop/quarantine machinery, which does not
  apply to a replay run).
- `retrace.yaml` — no new config keys; `gates.wire.budget_pct`, `wire_rules`,
  and `query_ignore` are reused as-is.
- No change to `retrace diff`, reference bundles, or existing replay users
  who don't pass the new flag.
