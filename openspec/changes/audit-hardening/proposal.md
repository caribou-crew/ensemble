## Why

A full audit of the codebase (2026-08-31) confirmed every pillar of the
product works end-to-end, but surfaced a cluster of findings that bite the
exact target user — someone running their stack locally, recording e2e
traffic, replaying it as CI mocks, and diffing pixels/wire between commits:

1. **Secrets get committed.** Default redaction covers headers and query
   strings only (`core/trace/redact.go:34,106,185`); a JSON *response body*
   containing `access_token`/`password` is recorded verbatim and then
   git-committed by `retrace ref accept`. Worse, destroy-mode redaction
   writes `[redacted]` into request bodies/keys that `replay/match.go`
   then compares literally — redacting a request field guarantees a replay
   miss.
2. **Silent capture blind spots.** Env→proxy wiring is entirely manual and
   unvalidated — pointing a service at another's *real* port instead of its
   proxy port makes hops silently invisible. WebSocket upgrades die as dead
   101s (`core/proxy/proxy.go:363`), SSE stalls unflushed and unrecorded
   until stream end (`proxy.go:533`), gRPC just fails, and binary bodies
   corrupt through UTF-8 round-tripping yet replay as HITs with exit 0.
3. **Lifecycle blind spots.** A service that crashes after startup stays
   `healthy` forever (`ensemble/orchestrator/proc.go:93`); logs exist on
   disk but have no API or UI; captured traffic history on disk
   (`hops.jsonl`) has no read API, so yesterday's traffic is unreachable.
4. **Robustness debt.** Live-path panics in redact, recorder disk I/O under
   the global mutex, count-bounded (not byte-bounded) rings, an unbounded
   stub body read, one truncated body poisoning an entire reference bundle,
   silent post-`End` hop drops (roadmap F.3), lossy multi-`Set-Cookie`.
5. **The headline feature is undocumented.** No CI example demonstrates
   `retrace replay --ref`; the reference-bundle lifecycle across commits is
   walked through nowhere; roadmap boxes 4.1–4.8 are stale.

## What Changes

- **recording-redaction**: default key-based redaction of JSON request and
  response *bodies* (same secret-key list as query redaction), an
  accept-time secret scan that refuses `ref accept` on likely credentials
  (override with `--force`), and redaction-aware replay matching (a
  destroy-redacted field matches any value).
- **proxy-wiring-validation**: on `up`, detect service `env:` values that
  reference another service's *real* port when that service has a `proxy:`
  port; surface as a warning in `up` output, `status`, and the dashboard.
- **protocol-guardrails**: flush streaming responses through the proxy and
  record streaming hops at headers-time (finalized on close); detect and
  loudly reject WebSocket upgrades and gRPC (`501` + a flagged, visible
  hop) instead of silently breaking; capture non-UTF-8 bodies losslessly
  via a base64 payload field, refused-or-excluded at replay rather than
  served mangled.
- **service-supervision**: capture process exit (code/signal/time), move
  the service to a visible `exited`/`crashed` state with an SSE event, and
  add a log tail API + log panes in the dashboard and TUI. No auto-restart.
- **traffic-history**: a read API over the persisted `hops.jsonl` (filter +
  paginate), whole-session HAR export, and a "load earlier" affordance in
  the Traffic view.
- **capture-robustness**: degrade instead of panic in redact's record path;
  move recorder disk writes off the request-serializing mutex; byte-budget
  the hop ring; cap stub request-body reads; per-exchange (rule-excludable)
  instead of whole-bundle refusal for truncated bodies; count and surface
  post-`End` dropped hops (closes roadmap F.3); fix the session
  bind-before-duplicate-check race; preserve multiple `Set-Cookie` values;
  add `ReadHeaderTimeout` and loopback enforcement to the stub listener.
- **Docs + hygiene** (no spec delta): a `retrace replay` CI job in
  `docs/retrace-ci-example.yml`, a reference-lifecycle walkthrough, a
  bring-your-own-stack getting-started, stale-doc cleanup
  (`docs/phase-3-porting-inventory.md`, `sample/README.md` rn-app note),
  roadmap checkbox sync in `init-ensemble-retrace/tasks.md`, a lint job in
  CI, and Windows npm packaging (binaries already build).

## Capabilities

### New Capabilities
- `recording-redaction`: secret-safe recordings by default — body redaction,
  accept-time scanning, redaction-aware matching.
- `proxy-wiring-validation`: detect env wiring that bypasses capture.
- `protocol-guardrails`: streaming fidelity plus loud, visible failure for
  unsupported protocols and lossless binary body capture.
- `service-supervision`: crash visibility and log access.
- `traffic-history`: query captured traffic beyond the live ring.
- `capture-robustness`: bounded memory, no live-path panics, no silent
  loss, cookie fidelity.

### Modified Capabilities
(none — no existing spec'd capability's requirements change; existing specs
cover gateway/tui/variants/profiles/inspector/docker, all untouched)

## Out of scope (deferred follow-ups, deliberately)

Full WebSocket/gRPC/h2c/TLS proxying; latency replay; stateful replay
scenarios beyond the `used` counter; fault injection beyond latency;
a11y-tree diff (tracked as init task 4.9); pixel-reproducible renderer
container recipe; request resend/edit from the dashboard; Cypress/Detox
adapters. This change makes each gap *visible and safe*, not invisible.

## Impact

- `core/trace`: redact defaults + body walker, `Payload.BodyB64` +
  `Payload.SetCookies` fields (additive, `omitempty`), panic removal.
- `core/proxy`: flusher passthrough, streaming/upgrade/gRPC detection,
  recorder write pipeline, ring byte budget, session race fix + drop
  counting.
- `core/stub`: body read cap, loopback enforcement, `ReadHeaderTimeout`.
- `ensemble/config` + `ensemble/orchestrator` + `ensemble/server` +
  `dashboard/ensemble-ui` + `ensemble/tui`: wiring validation, exit
  supervision, log + history endpoints and views.
- `retrace/refs`, `retrace/replay`, `retrace/serve`: accept-time scan,
  redaction-aware matching, per-exchange refusal rules.
- `docs/`, `.github/workflows`, `npm/`, `openspec/`: documentation and
  hygiene.
- Schema: new hop/payload fields are additive with `omitempty`; existing
  recordings and reference bundles stay readable unchanged.
