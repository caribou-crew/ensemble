## Why

`ensemble latency set` injects arbitrary delay, but choosing a realistic value
today means manually opening Datadog, eyeballing a percentile graph, and typing
numbers in by hand. Without production-shaped latency, local dev and e2e tests
run at localhost speed, which hides timeout bugs, race conditions, and UX
problems that only surface under real network delay. The predecessor tool
(local-stack) had `lcs latency from-datadog` for exactly this; ensemble has
no equivalent.

## What Changes

- New `ensemble latency from-datadog` CLI subcommand: queries a Datadog metric
  for p50/p95/p99 (via one query template with a `{P}` placeholder) and applies
  the result as a latency rule through the existing `LatencyStore`/`latency set`
  machinery.
- New declarative latency profiles: a `latency.profiles` block in `ensemble.yaml`
  points at a profile file (same `file:`-relative-to-`Config.Dir` pattern as
  `readiness:`) listing rules per target/path, each either `from_datadog` (a
  query + lookback window) or `fixed_ms` (manual override, for targets with no
  Datadog metric).
- New `ensemble latency apply <profile>` command: resolves every rule in a
  named profile (querying Datadog for each `from_datadog` rule) and applies them
  all as armed `LatencyRule`s in one pass; profiles are opt-in, never applied
  automatically by `ensemble up`.
- New optional `datadog:` config block: Datadog site, the *names* of the env
  vars holding the API/app keys (never the keys themselves), a default lookback
  window, and a `service_map` for when an ensemble service name doesn't match
  its Datadog service tag.
- `latency list`/status output gains a rule source: `manual` vs
  `datadog:<query>`, so a stale pulled profile is visibly distinguishable from
  a hand-set rule.

## Capabilities

### New Capabilities
- `datadog-latency-import`: querying Datadog for real percentile latency and
  applying it as ensemble `LatencyRule`s, both ad hoc (`from-datadog`) and via
  named, file-backed profiles (`apply <profile>`) — CLI surface, config schema,
  and the Datadog query client.

### Modified Capabilities
(none — this only adds a new source for `LatencyRule.P50/P95/P99`; it doesn't
change how `DelayFor` interprets a rule or how existing `latency set/list/
reset/arm-all` behave)

## Impact

- `ensemble/cmd/ensemble/`: new `cmd_latency_from_datadog.go` (or extended
  `cmd_latency.go`) for `from-datadog`/`apply`; `client.go` gains the new API
  calls.
- `ensemble/config/`: `Config` gains `Datadog *DatadogConfig` and `Latency
  *LatencyConfig` (mirroring `Readiness`/`ReadinessChecksFile`); new
  `config/latency_profiles.go` for the profile-file schema and loader;
  `validate.go` gains checks (unknown profile name, a rule naming a target with
  no routable port, etc.).
- `core/proxy/latency.go`: `LatencyRule` gains a `Source` field (`""` = manual,
  else the Datadog query string) — additive, no behavior change to `DelayFor`.
- New package (`ensemble/datadog/` or similar): a minimal Datadog `/api/v1/query`
  client — auth via `DD-API-KEY`/`DD-APPLICATION-KEY` headers, one query per
  percentile substitution.
- `ensemble/server/routes.go`: new control-plane endpoint(s) so `ensemble
  latency apply` can run through the same client/API path as every other
  `latency` subcommand, not bypass it.
- No changes to the dashboard's LatencyView in this proposal (surfacing rule
  `Source` there is a natural follow-up, not required for the CLI/profile
  workflow to work).
- New external dependency: outbound HTTPS calls to Datadog's API from the
  machine running `ensemble` — opt-in (only when `from-datadog`/`apply` is
  actually invoked), never during a plain `ensemble up`.
