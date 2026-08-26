## ADDED Requirements

### Requirement: Top-level readiness configuration
`ensemble.yaml` SHALL support an optional top-level `readiness:` key with fields
`file` (path to a readiness checks file, relative to `ensemble.yaml`), `timeout_s`
(total time budget for checks to pass, default 60), and `retry_interval_s` (delay
between retries of not-yet-passing checks, default 5).

#### Scenario: No readiness key configured
- **WHEN** `ensemble.yaml` has no `readiness:` key
- **THEN** the stack is unconditionally considered ready — with no checks run and
  no existing behavior of `ensemble up`/`ensemble status` changed — regardless of
  whether `on_ready` succeeds, fails, or never runs because a node failed to
  start; a project that never opted into this feature never sees `ensemble
  ready` fail because of it

#### Scenario: readiness.file does not exist
- **WHEN** `readiness.file` points at a path that does not exist on disk
- **THEN** config loading fails with an error naming the missing file, the same
  way an invalid `on_ready` reference fails today

### Requirement: Readiness checks file format
The file referenced by `readiness.file` SHALL define a `checks:` list, where each
check has a unique `name`, a `service` (an existing service or stub name), a
`path` (HTTP request path on that service), an optional `headers_from` (path to
an executable script), and an `assert` block with an expected `status` and/or a
`body_jq` expression.

#### Scenario: Check references an unknown service
- **WHEN** a check's `service` does not resolve via the same lookup the gateway
  uses to route requests (i.e. it is not a configured service or stub)
- **THEN** config loading fails at parse time, naming the check and the unknown
  service, rather than failing later at check-execution time

#### Scenario: Duplicate check names
- **WHEN** two checks in the same readiness file share a `name`
- **THEN** config loading fails with an error identifying the duplicate name

### Requirement: Readiness checks resolve service addresses like the gateway does
A check's `service` SHALL be resolved to an address using the same resolution
the gateway uses for `GatewayRoute.Service` (service's native port, not a proxy
port), so readiness checks keep working if a service's port changes.

#### Scenario: Service port changes between runs
- **WHEN** a service's assigned port differs from a previous run
- **THEN** its readiness checks still resolve and execute against the current
  port without any change to the readiness checks file

### Requirement: Readiness runs after on_ready, asynchronously from Up
Readiness checks SHALL begin only after `on_ready` completes successfully, and
SHALL run as a background phase that does not delay `ensemble up` returning.

#### Scenario: on_ready fails
- **WHEN** `on_ready` (seeds or its `run` step) fails
- **THEN** no readiness checks are executed, and readiness state remains
  `pending`/`not_ready`

#### Scenario: ensemble up returns before readiness resolves
- **WHEN** all services and databases reach `healthy` and `on_ready` succeeds
- **THEN** `ensemble up` returns without waiting for readiness checks to finish,
  and readiness checks continue running in the background

### Requirement: Per-check retry until timeout
Each readiness check SHALL be retried independently, at `retry_interval_s`
intervals, until it passes or the overall `timeout_s` budget elapses. A check
that has already passed SHALL NOT be re-executed on later ticks.

#### Scenario: A check passes on the first attempt
- **WHEN** a check's first execution satisfies its `assert` block
- **THEN** that check is marked passed and is not re-executed on subsequent
  retry ticks, even while other checks are still retrying

#### Scenario: A check never passes before timeout
- **WHEN** a check has not satisfied its `assert` block by the time `timeout_s`
  elapses since readiness checks began
- **THEN** overall readiness state becomes `not_ready`, and the check's name and
  last observed error/response are recorded

#### Scenario: All checks pass before timeout
- **WHEN** every configured check has independently passed within `timeout_s`
- **THEN** overall readiness state becomes `ready`

### Requirement: headers_from script output is parsed as HTTP headers
When a check specifies `headers_from`, the script SHALL be executed and its
stdout parsed as one `Header-Name: value` pair per non-blank line; the resulting
headers SHALL be attached to that check's request.

#### Scenario: headers_from script succeeds
- **WHEN** a check's `headers_from` script exits 0 and prints `Authorization:
  Basic <token>` to stdout
- **THEN** the check's HTTP request includes an `Authorization` header with
  that value

#### Scenario: headers_from script fails
- **WHEN** a check's `headers_from` script exits non-zero
- **THEN** that check attempt is recorded as failed with the script's error
  output, and is retried on the next tick like any other failed check

#### Scenario: No headers_from configured
- **WHEN** a check omits `headers_from`
- **THEN** its request is sent with no additional headers beyond standard ones

### Requirement: body_jq assertion evaluation
When a check's `assert` block includes `body_jq`, the response body SHALL be
parsed as JSON and evaluated against the `body_jq` expression using an embedded
jq-compatible evaluator; the check passes only if the expression evaluates to a
truthy result (and, if `status` is also specified, the response status matches).

#### Scenario: body_jq expression is truthy
- **WHEN** a check's response body, evaluated against its `body_jq` expression,
  yields a truthy result, and the response status matches any configured
  `status`
- **THEN** the check passes

#### Scenario: body_jq expression is falsy
- **WHEN** a check's response body, evaluated against its `body_jq` expression,
  yields a falsy result
- **THEN** the check fails for that attempt, recording the evaluated result

#### Scenario: Response body is not valid JSON
- **WHEN** a check specifies `body_jq` but the response body cannot be parsed
  as JSON
- **THEN** the check fails for that attempt, recording a JSON-parse error

### Requirement: Readiness state is exposed via existing status surfaces
`ensemble status` SHALL report overall readiness state (`pending`, `checking`,
`ready`, or `not_ready`) and, for each configured check, whether it has passed,
alongside the existing per-service status rows, without altering those rows'
existing fields.

#### Scenario: ensemble status text output includes readiness
- **WHEN** a user runs `ensemble status` while readiness checks are configured
- **THEN** the output includes a readiness summary (e.g. count of checks
  passed out of total, and overall state) in addition to the existing
  per-service table

#### Scenario: ensemble status --json includes readiness
- **WHEN** a user runs `ensemble status --json` while readiness checks are
  configured
- **THEN** the JSON response includes a `readiness` field with overall state
  and per-check pass/fail detail, alongside the existing `services` array

### Requirement: ensemble ready CLI command
The CLI SHALL provide an `ensemble ready` command that polls the orchestrator's
readiness state and blocks until it resolves to `ready` or `not_ready`, or until
an optional `--timeout` flag elapses, exiting 0 if ready and non-zero otherwise.
It SHALL support a `--json` flag that prints a structured result instead of
plain text.

#### Scenario: Stack becomes ready before timeout
- **WHEN** `ensemble ready` is run and the orchestrator's readiness state
  resolves to `ready` before the command's timeout elapses
- **THEN** the command prints a success message and exits 0

#### Scenario: Stack does not become ready before timeout
- **WHEN** `ensemble ready` is run and readiness state is still `not_ready` (or
  `pending`/`checking`) when the command's timeout elapses
- **THEN** the command exits non-zero, naming which checks have not passed

#### Scenario: ensemble ready --json
- **WHEN** `ensemble ready --json` is run
- **THEN** it prints a JSON object with at least `ready` (boolean) and `checks`
  (per-check pass/fail detail) instead of plain text, regardless of exit code

#### Scenario: No readiness configured
- **WHEN** `ensemble ready` is run against a stack with no `readiness:` key
  configured
- **THEN** it exits 0 immediately, treating the stack as ready
