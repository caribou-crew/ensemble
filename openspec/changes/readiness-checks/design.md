## Context

Today `ensemble up` gates on `health:`/TCP/docker-running checks per service
(`orchestrator.gateHealth`, `orchestrator/orchestrator.go:1236`), then runs
`on_ready` synchronously (`runOnReady`, `orchestrator/orchestrator.go:626`) before
`Up` returns. Both are proof of *process* state (listening, seed script exited 0),
not proof that the seeded data is queryable through the service graph as a client
would see it. `Config.RoutablePort(name)` (`ensemble/config/config.go:368`) is the
existing `service:` → `http://127.0.0.1:<port>` resolver, already used by the
gateway (`wireOneGateway`); this is the seam a readiness check should resolve
addresses through, rather than inventing a second lookup.

Two adjacent-but-distinct efforts exist and are explicitly out of scope here:
`init-ensemble-retrace` task F.1 ("real readiness checks for `redis`/`localstack`
databases... a readiness seam that does not require a full inspector") is about
*driver-level* database readiness feeding `gateDatabaseHealth`'s `DBReady` hook —
internal to the health-gate phase, before `on_ready` even runs. And
`closed-loop-round-one` item 4 (`preflight:`/`setup:`/`teardown:`) is a retrace-side
gate that runs *before the proxy binds*, for a single capture flow. This proposal's
checks run once, after `on_ready`, at the whole-stack level, asserting on
application responses rather than process/driver state. All three can coexist.

## Goals / Non-Goals

**Goals:**
- A stack-level READY/NOT-READY signal, computed after `on_ready`, backed by
  user-defined HTTP assertions against real service responses.
- Keep `ensemble up`'s existing return behavior unchanged — readiness must not
  make `ensemble up` block for up to `timeout_s` by default.
- Reuse existing seams (`RoutablePort`, the shell-script trust model already used
  by `on_ready.run`) rather than introducing new resolution or execution paths.

**Non-Goals:**
- Not a test framework: no fixtures, parallelism knobs, or setup/teardown per check.
- Not contract/OpenAPI validation — that remains retrace's job.
- Not continuous monitoring — checks run once (with bounded retry) after boot, not
  on a recurring poll.
- Not a replacement for `init-ensemble-retrace` F.1's DB-driver readiness seam.

## Decisions

**Readiness runs as an async phase after `on_ready`, not inside `Up`'s synchronous
path.** `runOnReady` stays exactly as-is (synchronous, still gates `Up`'s return
on seed/migration success). Once it succeeds, the orchestrator starts a
readiness goroutine and `Up` returns immediately — matching the CLI surface in
the proposal (`ensemble up && ensemble ready`, where `ready` is the thing that
blocks). This avoids turning a config typo in `tools/readiness.yaml` into a
60-second hang on every `ensemble up`.

**Per-check retry, not per-round retry.** Each check tracks its own
pass/fail/last-error state. On each `retry_interval_s` tick, only checks that
haven't yet passed are re-executed; a check that already returned 200 + a
passing `body_jq` is not re-hit on later ticks. This matters for checks that
exercise non-idempotent auth (e.g. a `headers_from` script minting a one-time
token) and avoids hammering a slow-to-seed service with checks that already
succeeded. Global state is READY once every check has passed at least once;
NOT-READY if `timeout_s` elapses first, reporting which named checks never
passed and their last error.

**`body_jq` uses an embedded pure-Go jq-compatible evaluator (`gojq`), not a
shelled-out `jq` binary.** Ensemble ships as a single Go binary; requiring `jq`
on PATH (including in CI images) would be a silent new host dependency. `gojq`
covers the assertion patterns this feature needs (`.data | length > 0`, `.status
== "UP"`, field equality/existence) without CGO or subprocess overhead. Trade-off:
not 100% jq syntax parity, acceptable given the narrow assertion use case.

**`headers_from` scripts run through the same execution path as `on_ready.run`**
(`runShellStep`), not a new sandboxed runner. Output contract: each non-blank
stdout line is `Header-Name: value`; parse failures on a line are a config error,
not a check failure. No new trust boundary is introduced — a project that already
trusts its `on_ready` scripts trusts its readiness auth scripts equally.

**Config validation mirrors `on_ready`'s existing pattern.** At load time, each
check's `service:` is resolved via `Config.RoutablePort` and rejected (like
`validate.go`'s unknown-seed-name check) if it doesn't resolve to a configured
service or stub — fail fast at config-parse time, not at first check execution.

**Readiness state surfaces via the existing `/api/status` response**, as an
additional `Readiness` field alongside the existing per-service `Services`
array (state machine: `pending → checking → ready | not_ready`, plus per-check
results) — additive, so existing `ensemble status`/`ensemble-tui` consumers are
unaffected. `ensemble ready` is a thin CLI poller over this same endpoint with
its own `--timeout` flag (defaulting to the config's `timeout_s`), not a second
source of truth.

## Risks / Trade-offs

- **Async phase races with process shutdown** (`ensemble down` mid-readiness-loop)
  → guard the readiness goroutine with the same context-cancellation pattern the
  orchestrator already uses for other background loops; a cancelled context stops
  the retry loop and leaves state `not_ready` rather than panicking.
- **`gojq` dependency is new to `go.mod`** → small, pure-Go, actively maintained;
  no CGO/runtime binary requirement, so it doesn't affect the single-binary
  distribution story.
- **Response-body assertions couple readiness config to API response shape** →
  same fragility class as seed scripts already have against schema changes;
  acceptable since both are maintained by the same team as the services they check.
- **A `headers_from` script minting real credentials on every retry tick** → the
  per-check (not per-round) retry design means a passed check is never re-hit,
  bounding the blast radius to checks that are still failing.

## Open Questions

- Should `ensemble ready`'s own `--timeout` be allowed to exceed the config's
  `timeout_s` (e.g. a CI caller willing to wait longer than the orchestrator's
  own retry budget), or is the config's `timeout_s` a hard ceiling and the CLI
  flag can only shorten it? Leaning toward: CLI flag can only shorten, since
  after `timeout_s` the orchestrator has already settled on `not_ready` and
  waiting longer wouldn't change the answer.
- Whether/how to eventually fold `init-ensemble-retrace` F.1's DB-readiness seam
  into this mechanism (e.g. a `kind: db` check type) is deferred — no design
  commitment made here.
