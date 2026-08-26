## Context

`core/proxy.LatencyStore` already supports everything `DelayFor` needs to
inject a realistic distribution — `LatencyRule.P50/P95/P99` and the
piecewise-linear sampler in `sampleQuantiles` exist today and are unchanged
by this proposal. What's missing is entirely upstream of that: a way to
*fill in* those three numbers from Datadog instead of by hand, and a way to
apply a whole set of them (a "profile") in one command.

Every existing `ensemble latency <verb>` subcommand (`list`/`set`/`reset`/
`arm-all`) is a thin CLI client over the control-plane REST API
(`ensemble/cmd/ensemble/client.go` → `POST/GET/DELETE /api/latency*`); the
`LatencyStore` itself lives only in the server process, alongside the
orchestrator. `ensemble.yaml` (and any file a top-level key points at, e.g.
`readiness.file`) is likewise only ever parsed server-side, at `ensemble up`
— the CLI never reads it. Both new commands (`from-datadog`, `apply`) have
to follow that same shape: CLI parses flags and calls a new control-plane
endpoint; the server process does the actual Datadog querying and
`LatencyStore.Set` calls.

`Config.Load` already loads an optional `.env` next to `ensemble.yaml` and
prefers the real process environment over it (`expand.go`'s `envLookup`) —
that's the existing mechanism for exactly the "env, or `.env`" credential
sourcing the proposal asks for; this doesn't need a second implementation.

## Goals / Non-Goals

**Goals:**
- `ensemble latency from-datadog` and `ensemble latency apply <profile>`
  work as specified in the proposal, through the control plane like every
  other `latency` subcommand.
- A pulled rule is visibly distinguishable from a hand-set one
  (`latency list`/`--json` shows its source).
- Zero-config path: `from-datadog` works with just `DD_API_KEY`/
  `DD_APP_KEY`/`DD_SITE` in the environment or `.env`, no `datadog:` block
  required — mirrors how the old `lcs` command worked with no project-level
  config.

**Non-Goals:**
- No dashboard UI for any of this in this change (Impact already says so) —
  `latency list --json`'s new `source` field is enough for a follow-up.
- No `ensemble up --latency <profile>` startup flag. The proposal mentions
  it as an alternate activation path but the CLI Surface section doesn't
  list it, and "profiles shouldn't inject on every boot" (proposal's own
  design choice) argues against wiring it into `up` at all. Left as an
  open question below, not built now.
- No caching/TTL of Datadog results across `apply` runs — every invocation
  re-queries. This is an occasional, human-invoked operation, not a
  hot path.
- No support for Datadog query languages other than the `p{P}:metric{tags}`
  APM percentile form the proposal specifies.

## Decisions

**Datadog querying is server-side, behind two new endpoints.** `POST
/api/latency/from-datadog` (one ad hoc rule) and `POST /api/latency/apply`
(a named profile, resolving every rule in it). Both end by calling
`LatencyStore.Set` exactly like `handleLatencyUpsert` does today, so
`DelayFor`, `latency list`, and the dashboard's existing LatencyView all see
pulled rules the same way as manual ones. Alternative considered: have the
CLI query Datadog itself and `PUT /api/latency` with the resolved numbers.
Rejected — it would need its own copy of the config-loading and
credential-resolution logic the server already has, duplicating rather than
reusing `Config`.

**`LatencyRule` gains one additive field: `Source string`.** Empty means
manual (today's only case, so no migration needed for existing rules/rule
files). A pulled rule's `Source` is the resolved query with the window,
e.g. `"datadog:p{P}:trace.http.server.request.duration{service:billing,
env:prod} (last 60m)"` — one string per rule, not one per percentile, since
a `LatencyRule` already bundles all three. `latency list`'s human output
prints it (`manual` vs `datadog:...`); `--json` includes it verbatim.

**Percentile substitution issues three Datadog queries per rule, not one.**
Datadog's metric query language computes `p50:`/`p95:`/`p99:` as distinct
aggregations — there's no single call that returns all three from an APM
percentile query. `{P}` is substituted with `50`/`95`/`99` and each result
queried independently, then assembled into one `LatencyRule`. This is the
literal meaning of "one query template produces p50/p95/p99 by
substitution" from the proposal, made concrete.

**A Datadog query result window is collapsed to one number by averaging its
non-null points.** `/api/v1/query` returns a pointlist over the requested
window; ensemble needs a single p50/p95/p99 value per rule, not a series.
Averaging is the simplest deterministic reduction and matches "eyeball a
percentile graph and type in roughly what it's been" — the exact behavior
this feature replaces. Not configurable in this change; a
`--window-reduction max|avg|last` flag is a natural follow-up if average
turns out wrong for some metric, not built preemptively.

**Credentials resolve through `Config`'s existing `.env`/env precedence,
not a second env reader.** `Config` gains an exported `LookupEnv(name
string) (string, bool)` backed by the same `dotenv` map `Load` already
parses (currently discarded after expansion — this change keeps it on
`Config`). `datadog.api_key_env`/`app_key_env` (default `DD_API_KEY`/
`DD_APP_KEY`) name *which* var to read through it; `datadog.site` (default
`datadoghq.com`) is plain non-secret config, not read from `DD_SITE` when a
`datadog:` block exists — but when the block is entirely absent (the
zero-config path), `DD_SITE`/`DD_API_KEY`/`DD_APP_KEY` are read directly by
those default names, so `from-datadog` needs no `ensemble.yaml` changes at
all to try out.

**Profile files follow `readiness:`'s exact shape.** `latency.profiles.
<name>.file` is a path relative to `Config.Dir` unless absolute, parsed by
a new `LoadLatencyProfile` mirroring `LoadReadinessChecks` byte-for-byte in
structure. Same reasoning as readiness: keeps `ensemble.yaml` itself small,
and the profile file is independently versionable/shareable.

**Naming note, not a rename:** `latency.profiles` sits right next to the
existing top-level `profiles:` (service activation groups, `Config.
Profiles`) — a different concept at a different YAML path. They don't
collide in the schema, but they're one skim away from looking related.
Docs (README, `--help`) should say "latency profile" in full every time,
never bare "profile," in any text that also discusses service profiles.

**Datadog client is an interface, real implementation is a thin HTTP
client.** `DatadogClient.QueryPercentile(ctx, query string, windowMinutes
int) (value float64, err error)`, backed by `https://api.<site>/api/v1/
query`. Constructed with an overridable base URL so tests run against an
`httptest.Server` instead of the real API — same shape as
`runOneReadinessCheck`'s plain `http.Client`, just against an external host
instead of a resolved service port. A 10s per-call timeout (readiness's
`readinessRequestTimeout` is 5s against localhost; Datadog is a real
network hop, so a looser bound).

**`apply` is best-effort per rule, not all-or-nothing.** Each rule in a
profile is queried/applied independently; one rule's Datadog error (no
data in window, bad query, auth failure) is reported against that rule and
does not block the others from applying. Matches readiness checks' own
independence (`SessionManager`/readiness both treat per-item failure as
local, never a reason to abort everything else) and avoids one broken
metric blocking an otherwise-good profile pull. The CLI's summary output
lists every rule's outcome (applied vs error+reason), never silently drops
a failed one.

## Risks / Trade-offs

- **[Risk] Datadog API key checked into `.env` and accidentally committed]**
  → Mitigation: `datadog:` config only ever names *env var names*, never
  values (already the proposal's own design choice); README/docs should
  say `.env` belongs in `.gitignore` the same as any other secret file (the
  sample stack's own layout doesn't currently ship a `.env` example for
  this reason).
- **[Risk] Averaging a noisy window produces a misleading "typical"
  latency]** → Mitigation: `latency list` always shows the query and window
  alongside the number, so a suspicious value is traceable back to its
  source and re-checked in Datadog directly, not trusted blindly.
- **[Risk] A profile file references a target that gets renamed/removed in
  `ensemble.yaml`]** → Mitigation: `Config.Validate` checks every profile
  rule's `target` the same way `ReadinessCheck.Service` is checked today —
  fails `ensemble up` at load time with the bad rule named, not a silent
  no-op at `apply` time.
- **[Trade-off] Three HTTP round-trips per rule (p50/p95/p99) means
  `apply`-ing a profile with many rules is noticeably slower than
  `latency set`.** Acceptable — this is a deliberate, occasional action
  ("switch into production-latency mode for this test session"), not
  something on any hot path or run on every `ensemble up`.

## Open Questions

- Should `ensemble up --variant`-style startup flag (`--latency <profile>`)
  ship in a later change once `apply` has proven itself, or never (profiles
  staying strictly opt-in, post-startup)? Left for the tasks that follow
  this one to decide with real usage data, not guessed now.
- Is average-over-window the right reduction for every metric shape, or
  should p50/p95/p99 each use a different reduction (e.g. p99 as `max` to
  stay conservative)? Flagged in Decisions as a likely future flag, not
  resolved here.
