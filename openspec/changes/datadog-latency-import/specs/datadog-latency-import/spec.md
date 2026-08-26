# datadog-latency-import

## ADDED Requirements

### Requirement: Ad hoc Datadog latency import
`ensemble latency from-datadog` SHALL query Datadog for p50/p95/p99 by
substituting `{P}` in the given `--query` with `50`, `95`, and `99` in
turn, apply the result as a `LatencyRule` for `--target`/`--path` via the
same mechanism as `latency set`, and print the applied values and their
source. Flags: `--target NAME` and `--query QUERY` are required;
`--window MIN`, `--path PATH`, and `--enabled` are optional.

#### Scenario: Successful pull
- **WHEN** `ensemble latency from-datadog --target billing --query
  "p{P}:trace.http.server.request.duration{service:billing,env:prod}"
  --window 60 --path / --enabled` runs against a reachable Datadog account
- **THEN** a `LatencyRule{Target: "billing", Path: "/", Enabled: true}` is
  set with `P50`/`P95`/`P99` from the three queries, and the CLI prints
  `billing /: p50=<n>ms p95=<n>ms p99=<n>ms (source: datadog, last 60m)`

#### Scenario: Missing target
- **WHEN** `from-datadog` is run without `--target`
- **THEN** the command fails with a usage error and no request is made

#### Scenario: Default path and window
- **WHEN** `from-datadog` is run without `--path` or `--window`
- **THEN** `--path` defaults to `/` and `--window` defaults to the
  `datadog.default_window_minutes` config value, or 60 if unconfigured

### Requirement: Declarative latency profiles
Ensemble SHALL accept an optional `latency.profiles.<name>.file` mapping in
`ensemble.yaml`, naming a file (resolved relative to `Config.Dir` unless
absolute) that lists rules under a top-level `rules:` key. Each rule SHALL
have `target` and `path`, and exactly one of `from_datadog` (`query` +
optional `window_minutes`) or `fixed_ms`.

#### Scenario: Profile file loaded
- **WHEN** `ensemble.yaml` declares `latency.profiles.production.file:
  tools/latency-production.yaml` and that file lists two rules
- **THEN** `ensemble up` parses the file at load time and fails validation
  (naming the file and the parse error) if it is missing or malformed

#### Scenario: Rule with neither or both sources is invalid
- **WHEN** a rule in a profile file sets both `from_datadog` and
  `fixed_ms`, or neither
- **THEN** `Config.Validate` rejects the config, naming the profile, the
  rule's target/path, and the conflict

#### Scenario: Rule targets an unknown service
- **WHEN** a profile rule's `target` names no configured service, stub, or
  `*`
- **THEN** `Config.Validate` rejects the config, naming the profile and the
  unknown target

### Requirement: Applying a profile
`ensemble latency apply <profile>` SHALL resolve every rule in the named
`latency.profiles` entry — querying Datadog for each `from_datadog` rule,
using the literal value for each `fixed_ms` rule — and apply all of them as
armed `LatencyRule`s via the same mechanism as `latency set`. Each rule
SHALL be resolved and applied independently: one rule's failure SHALL NOT
prevent the others from applying.

#### Scenario: Full profile applies
- **WHEN** `ensemble latency apply production` runs and every rule's
  Datadog query succeeds
- **THEN** every rule in the profile is armed with its resolved values, and
  the CLI reports each target/path with its applied p50/p95/p99 (or
  fixed_ms) and source

#### Scenario: Partial failure still applies the rest
- **WHEN** `ensemble latency apply production` runs and one rule's Datadog
  query errors (e.g. no data in the window)
- **THEN** every other rule in the profile is still applied, and the CLI
  reports the failed rule's target/path and error alongside the successful
  ones' results

#### Scenario: Unknown profile name
- **WHEN** `ensemble latency apply staging` is run but no `staging` entry
  exists under `latency.profiles`
- **THEN** the command fails naming `staging` and the profiles that do
  exist, and no rule is applied

### Requirement: Datadog credential and site resolution
Datadog credentials SHALL be read from the environment (including a `.env`
file next to `ensemble.yaml`, with the real process environment taking
precedence), never from `ensemble.yaml` values directly. An optional
top-level `datadog:` block MAY set `site` (default `datadoghq.com`),
`api_key_env`/`app_key_env` (default `DD_API_KEY`/`DD_APP_KEY`, naming
which environment variables to read), `default_window_minutes` (default
60), and `service_map` (ensemble service name → Datadog service tag, for
targets where they differ). With no `datadog:` block, credentials and site
SHALL be read from `DD_API_KEY`/`DD_APP_KEY`/`DD_SITE` directly.

#### Scenario: Zero-config pull
- **WHEN** no `datadog:` block is configured but `DD_API_KEY`/`DD_APP_KEY`
  are set in the environment
- **THEN** `ensemble latency from-datadog` succeeds using those variables
  and the default site `datadoghq.com`

#### Scenario: Custom key env var names
- **WHEN** `datadog.api_key_env: PROD_DD_KEY` is configured and `PROD_DD_KEY`
  (not `DD_API_KEY`) is set in the environment
- **THEN** the Datadog client authenticates using `PROD_DD_KEY`'s value

#### Scenario: Missing credentials
- **WHEN** neither the configured (or default) API/app key environment
  variables are set
- **THEN** `from-datadog`/`apply` fail with an error naming which variable
  is missing, and no Datadog request is made

### Requirement: Rule source is visible
`LatencyRule` SHALL carry an optional `Source` field: empty for a manually
`set` rule, otherwise a human-readable description of the Datadog query and
window that produced it. `ensemble latency list` (both human and `--json`
output) SHALL include it.

#### Scenario: Pulled rule shows its source
- **WHEN** a rule was applied via `from-datadog` or `apply` with query
  `p{P}:trace.http.server.request.duration{service:billing,env:prod}` and a
  60-minute window
- **THEN** `ensemble latency list` shows that rule's source as
  `datadog:p{P}:trace.http.server.request.duration{service:billing,env:prod}
  (last 60m)`, and `--json` includes the same string in a `source` field

#### Scenario: Manual rule shows no source
- **WHEN** a rule was applied via `ensemble latency set`
- **THEN** `ensemble latency list` shows that rule's source as `manual`
  (human output) or an empty `source` field (`--json`)
