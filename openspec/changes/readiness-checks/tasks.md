## 1. Config schema

- [x] 1.1 Add `Readiness struct { File string; TimeoutS int; RetryIntervalS int }` and `Config.Readiness *Readiness` to `ensemble/config/config.go`, mirroring the existing `OnReady` struct's shape; default `TimeoutS` 60, `RetryIntervalS` 5 when unset
- [x] 1.2 Define the readiness checks file schema (`Checks []ReadinessCheck{Name, Service, Path, HeadersFrom, Assert{Status *int, BodyJQ string}}`) and a loader that parses it from the path in `Readiness.File` (resolved relative to `ensemble.yaml`'s directory)
- [x] 1.3 In `ensemble/config/validate.go`, validate: `readiness.file` exists on disk; every check's `service` resolves via `Config.RoutablePort`; check names are unique within the file
- [x] 1.4 Unit tests for the loader and validator (valid file, missing file, unknown service, duplicate names) alongside existing tests in `config_test.go`/`validate.go`'s test file

## 2. jq-style assertion evaluation

- [x] 2.1 Add `gojq` (or equivalent pure-Go jq-compatible evaluator) as a dependency
- [x] 2.2 Implement `evaluateBodyJQ(body []byte, expr string) (truthy bool, result any, err error)`: parse `body` as JSON, run `expr`, treat a non-empty/non-false/non-null/non-zero result as truthy
- [x] 2.3 Unit tests: truthy result, falsy result, invalid JSON body, invalid jq expression

## 3. Orchestrator: readiness phase

- [x] 3.1 Add a `ReadinessState` type (`pending`, `checking`, `ready`, `not_ready`) and a `ReadinessCheckState` per check (name, passed, lastError, lastCheckedAt) to the orchestrator's state
- [x] 3.2 After `runOnReady` succeeds in `Up`, launch a background goroutine (guarded by the orchestrator's existing shutdown/cancellation context) that runs the readiness retry loop; `Up` returns without waiting on it
- [x] 3.3 Implement the per-check retry loop: resolve each unpassed check's service address via `Config.RoutablePort`, run its `headers_from` script (if any) via the same execution path as `on_ready.run`, parse stdout as `Header-Name: value` lines, issue the HTTP request, evaluate `assert.status`/`assert.body_jq`, update per-check state; repeat every `retry_interval_s` until all pass or `timeout_s` elapses
- [x] 3.4 On `on_ready` failure or no `readiness:` configured, set state accordingly (`not_ready` and `ready` respectively, per spec scenarios) without starting the loop
- [x] 3.5 Tests: check passes on first attempt (never re-run), check passes on a later retry, check never passes (timeout → `not_ready` with recorded errors), `headers_from` script failure recorded as check failure, cancellation stops the loop cleanly

## 4. Status/API surface

- [x] 4.1 Extend the `/api/status` response (`ensemble/server/routes.go` `handleStatus`) with a `readiness` field: overall state + per-check pass/fail/lastError
- [x] 4.2 Extend `ensemble status` (`ensemble/cmd/ensemble/cmd_status.go`) text output with a readiness summary line; extend `--json` output with the new `readiness` field
- [x] 4.3 Tests covering both text and `--json` output with/without `readiness:` configured

## 5. `ensemble ready` CLI command

- [x] 5.1 Add `ensemble/cmd/ensemble/cmd_ready.go`: polls `/api/status` for readiness state, blocking until `ready`/`not_ready` or an optional `--timeout` flag (defaulting to the config's `timeout_s`) elapses; exit 0 if ready, non-zero otherwise, printing which checks haven't passed on failure
- [x] 5.2 Add `--json` flag printing `{"ready": bool, "checks": [...]}` 
- [x] 5.3 Wire the command into the CLI's command registry alongside `status`/`up`/`down`
- [x] 5.4 Tests: ready-before-timeout, not-ready-at-timeout, `--json` output shape, no-readiness-configured short-circuits to exit 0

## 6. Sample project & docs

- [x] 6.1 Add `tools/readiness.yaml` and `tools/readiness-auth.sh` to `sample/` demonstrating an authenticated and an unauthenticated check
- [x] 6.2 Wire `readiness:` into `sample/ensemble.yaml` (task text said `sample/.ensemble`, but that directory only holds runtime artifacts — `sample/ensemble.yaml` is the actual config file)
- [x] 6.3 Document the feature (config shape, `ensemble ready`, CI usage pattern) — added a "Readiness checks" section to the top-level README.md (matching how Gateways/Databases/etc. are documented there, not in docs/) plus a short pointer in sample/README.md
