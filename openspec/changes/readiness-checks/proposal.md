## Why

`health:` proves a process is listening. `on_ready` runs seeds and migrations. Neither
proves the seeded data is actually queryable end-to-end through the service graph. A
stack can report every service `healthy` while being unusable for real traffic (wrong
DynamoDB schema, a silently failed seed, misconfigured auth) — there is currently no
signal that means "this stack is ready for real traffic," only "these processes came up."

## What Changes

- Add a top-level `readiness:` key to `ensemble.yaml` pointing at an external checks
  file (`file:`, plus `timeout_s` and `retry_interval_s`), keeping the main config
  clean while the check definitions live alongside seed scripts.
- Add a readiness checks file format: a list of named checks, each targeting a
  `service:` + `path:` (resolved the same way the gateway resolves service addresses
  today, via `Config.RoutablePort`), an optional `headers_from:` script for injecting
  auth headers without putting secrets in YAML, and a `body_jq:` assertion against the
  response body plus an expected `status:`.
- Readiness checks run once, after `on_ready` completes (never in place of it, and
  never if `on_ready` itself fails), retrying on failure up to `timeout_s`.
- Add a `READY` signal to the orchestrator surfaced by `ensemble status` (which checks
  passed/failed, not just per-service health) and a new `ensemble ready` CLI command
  that blocks until ready-or-timeout and exits 0/1 (with a `--json` form), so CI and
  tools like retrace/the JS prototype have a single deterministic gate to wait on.

## Capabilities

### New Capabilities
- `readiness-checks`: top-level `readiness:` config, the readiness-check file format
  (service+path resolution, `headers_from`, `body_jq` assertions), the post-on_ready
  retry/timeout run loop, the resulting READY/NOT READY state, its surfacing in
  `ensemble status`, and the new `ensemble ready` CLI command.

### Modified Capabilities
(none — `ensemble status`'s existing per-service rows and `ensemble-tui`'s existing
per-service display are unchanged; readiness is reported as an additional summary
line/state, not a change to any existing requirement)

## Impact

- `ensemble/config`: new `Readiness` struct on `Config` (mirrors the existing
  `OnReady` struct's shape) and a loader/validator for the external checks file.
- `ensemble/orchestrator`: new phase after `runOnReady` that runs checks with retry
  until `timeout_s`, reusing `Config.RoutablePort` for service address resolution;
  extends whatever state `Up`/`States()` expose with readiness results.
- `ensemble/server`: `/api/status` (or a new endpoint) exposes readiness results;
  `ensemble/cmd/ensemble`: new `cmd_ready.go` alongside the existing `cmd_status.go`.
- New runtime dependency: checks assert on response bodies with `jq`-style
  expressions — needs a decision (shell out to `jq`, or an embedded Go JSON-query
  library) in design.md.
- Sample project (`sample/`) gets an example `tools/readiness.yaml` +
  `tools/readiness-auth.sh` to document the feature end-to-end.
