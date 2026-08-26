## 1. `core/proxy` — rule source field

- [x] 1.1 Add a `Source` string field (JSON tag `source`, `omitempty`) to
  `LatencyRule` (`core/proxy/latency.go`) — additive only, `DelayFor`/
  `Set`/`Remove`/`Rules`/`Reset`/`ArmAll` unchanged.
- [x] 1.2 Update `core/proxy/latency_test.go` (or add a case) confirming a
  rule with `Source` set round-trips through `Set`/`Rules` unchanged and
  that `DelayFor` ignores it.

## 2. `ensemble/config` — schema, validation, credential lookup

- [x] 2.1 Store the parsed `.env` map on `Config` (currently discarded
  after `expandEnvVars` in `Load`) and add `func (c *Config) LookupEnv(name
  string) (string, bool)` reusing `envLookup`'s precedence (real env, then
  `.env`).
- [x] 2.2 Add `Datadog *DatadogConfig` to `Config` (new file
  `ensemble/config/datadog.go`): `Site`, `APIKeyEnv`, `AppKeyEnv`,
  `DefaultWindowMinutes`, `ServiceMap map[string]string`, all optional with
  documented defaults (`datadoghq.com` / `DD_API_KEY` / `DD_APP_KEY` / 60).
- [x] 2.3 Add `Latency *LatencyConfig` to `Config`
  (`ensemble/config/latency_profiles.go`): `Profiles map[string]struct{File
  string}`, mirroring `Readiness`'s single-`File` shape.
- [x] 2.4 Add `LatencyProfileFile`/`LatencyRuleConfig` types and
  `LoadLatencyProfile(dir string, file string) (*LatencyProfileFile,
  error)`, mirroring `LoadReadinessChecks` in `ensemble/config/readiness.go`
  byte-for-byte in structure (relative-to-`Config.Dir` resolution, same
  error wrapping style).
- [x] 2.5 Parse every declared profile file during `Config.Validate` (same
  timing as `ReadinessChecks()`), store the results for the orchestrator to
  use, and surface load/parse errors naming the profile and file.
- [x] 2.6 Validate each rule: exactly one of `from_datadog`/`fixed_ms` set;
  `target` names a configured service, stub, or `*` (reuse whatever
  `ReadinessCheck.Service` validation already resolves against —
  `RoutablePort` or equivalent).
- [x] 2.7 Config tests: valid profile loads; missing file; malformed YAML;
  rule with both/neither source; rule with unknown target; `LookupEnv`
  precedence (env beats `.env`).

## 3. Datadog client

- [x] 3.1 New package (`ensemble/datadog/` or `ensemble/orchestrator/
  datadog.go` — pick based on whether it needs orchestrator internals;
  design leans toward a standalone package with no ensemble-internal
  dependencies) defining `type Client interface { QueryPercentile(ctx
  context.Context, query string, windowMinutes int) (float64, error) }`.
- [x] 3.2 Implement the real client: builds `p{P}:...` → `p50:...`/
  `p95:...`/`p99:...` isn't this method's job (percentile substitution is
  the caller's, per design — this method takes one already-substituted
  query) — hits `GET https://api.<site>/api/v1/query` with `DD-API-KEY`/
  `DD-APPLICATION-KEY` headers and `from`/`to`/`query` params, averages the
  non-null pointlist values in the response, 10s timeout, overridable base
  URL for tests.
- [x] 3.3 Add a helper that runs the three percentile substitutions for one
  rule (`50`/`95`/`99`) and returns `(p50, p95, p99 float64, err error)` —
  the piece `from-datadog`/`apply` both call.
- [x] 3.4 Client tests against `httptest.Server`: successful query,
  auth-header assertions, empty pointlist, HTTP error status, malformed
  JSON.

## 4. Server — control-plane endpoints

- [x] 4.1 `POST /api/latency/from-datadog` (`ensemble/server/routes.go`):
  body `{target, query, window_minutes?, path?, enabled?}`, resolves
  credentials via `Config.Datadog`/`Config.LookupEnv`, runs the
  percentile-triple query, `LatencyStore.Set`s the result with `Source`
  populated, returns the updated rule list (same response shape as
  `handleLatencyUpsert`).
- [x] 4.2 `POST /api/latency/apply` (body `{profile string}`): loads the
  named profile from the config parsed in 2.5, resolves every rule
  independently (Datadog query or literal `fixed_ms`), applies each
  successful one via `LatencyStore.Set`, and returns a per-rule outcome
  list (`{target, path, ok, error?, p50/p95/p99 or fixedMs, source}`) —
  never fails the whole request because one rule errored (design's
  best-effort decision).
- [x] 4.3 Server tests: `from-datadog` happy path against a fake
  `datadog.Client`; `apply` with all-succeed, partial-failure, and
  unknown-profile cases; missing-credentials error surfaces the right
  message.

## 5. CLI

- [x] 5.1 `client.go`: add `LatencyFromDatadog`/`LatencyApply` methods
  calling the two new endpoints.
- [x] 5.2 `cmd_latency.go`: add `from-datadog` and `apply` subcommands to
  the `ensemble latency` dispatch, flags exactly as the proposal's CLI
  Surface section (`--target`, `--query`, `--window`, `--path`,
  `--enabled` for `from-datadog`; positional `<profile>` for `apply`).
- [x] 5.3 Human-readable output: `from-datadog` prints the one-line summary
  from the proposal (`billing /: p50=45ms p95=120ms p99=340ms (source:
  datadog, last 60m)`); `apply` prints one line per rule (success or
  error) plus a final count summary; both support `--json`.
- [x] 5.4 Extend `printLatencyRules` (or add a variant) to show `source`
  in `latency list`'s human output (`manual` when empty).
- [x] 5.5 CLI tests: flag parsing/validation (missing `--target`, missing
  profile arg), `--json` output shape, exit codes on partial-failure
  `apply`.

## 6. Docs

- [x] 6.1 Top-level `README.md`: document `datadog:`, `latency.profiles`,
  `from-datadog`, and `apply` next to the existing latency section, calling
  out that latency profiles are opt-in (never applied by a plain `ensemble
  up`) and that `.env` is where Datadog credentials belong, not
  `ensemble.yaml`.
- [x] 6.2 Note the naming proximity to (but non-collision with) the
  existing top-level `profiles:` key, per design.md's naming note.

## 7. End-to-end verification

- [ ] 7.1 Manual smoke test against a real (or sandboxed) Datadog account:
  `from-datadog` against a real metric, confirm `latency list` shows the
  right source string and `DelayFor` actually delays requests by roughly
  the pulled values.
- [x] 7.2 `go build ./...` and `go test ./...` clean across `core/`,
  `ensemble/` before marking this change ready to archive.
