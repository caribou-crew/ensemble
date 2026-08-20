# Stretch ideas — for interactive discussion (captured 2026-08-20, Steven heading to bed)

Two seeds from Steven + my expansions. Nothing here is committed work; it's
an agenda. Existing stretch S.1 (Datadog latency import) stays as-is — these
build past it.

## 1. Datadog → LLM incident replication loop (Steven's idea)

Goal: an LLM maps a real DD issue onto the local ensemble stack, replicates
the conditions, diagnoses, and attempts a fix — locally, with retrace as the
verification harness.

Building blocks (roughly in dependency order):

- **a. Service mapping config** (exists in S.1 shape): `services.<name>.apm`
  → DD service/env. Token via user .env, never in config.
- **b. Fault injection beyond latency** (prereq, useful standalone): today
  the proxy injects latency only. Add per-target/path fault rules: status
  override (500/503), connection reset, response truncation, malformed
  JSON, bandwidth cap. Same rule store + REST + CLI shape as latency —
  cheap to add, huge for replication ("this endpoint was 503ing at 3%").
- **c. Condition import**: pull from DD APM for a time window — per-endpoint
  latency quantiles (S.1), error rates, timeout rates — and translate into
  a *replication profile*: a named set of latency+fault rules. One click /
  one CLI call: `ensemble apm replicate --incident <window>`.
- **d. Trace import**: fetch a specific DD trace, map spans to local
  services, reconstruct the request sequence, replay it as an ensemble
  session. Gaps (services not in local topology) get stubbed with recorded
  DD span timings.
- **e. LLM surface**: the REST API is already LLM-first; the missing piece
  is packaging. Options: (1) an MCP server (`ensemble mcp`) exposing
  status/traffic/latency/faults/inspector/seed as tools; (2) a docs recipe
  showing Claude Code driving the plain REST/CLI. MCP is thin over
  existing endpoints — low cost, high demo value.
- **f. The loop**: LLM reads DD issue → replicates via (c)/(d) → observes
  local hops + inspector DB state → hypothesizes → edits code → ensemble
  restart → retrace diff pre/post fix proves behavior change. retrace's
  wire/hop diffs are the objective "did the fix change what we thought"
  check — that's the differentiator vs. generic agent debugging.

Open questions for tomorrow:
- Scope: is (b)+(e) the right v1 slice, with (c)/(d) later? (My lean: yes.)
- DD API surface: APM metrics + traces need different endpoints/permissions;
  which token scopes are acceptable to require?
- Should replication profiles be shareable artifacts (checked in like
  retrace recordings) so a teammate can replay an incident?

## 2. Passthrough mode — proxy in front of real prod/QA (Steven's idea)

Goal: an app (or test) points at the ensemble proxy, which forwards to a
real remote env (QA/staging/prod) instead of a local process — full network
capture, latency injection, sessions, retrace recording, no local stack.

- Config shape: `services.<name>.upstream: https://qa.example.com` (mutually
  exclusive with run/docker). Proxy already reverse-proxies; needs https
  upstream + SNI/Host handling + streaming for large bodies.
- Trace headers: remote envs won't propagate `retrace-run` baggage back in
  sub-calls we can't see — capture is client-edge only, verdict machinery
  already handles "reduced scope" honestly (standalone-capture path).
- **Safety rails (important for prod):** default read-only guard —
  non-GET/HEAD to a `passthrough: prod`-flagged upstream requires explicit
  `allow_writes: true`. Redaction becomes load-bearing (real PII in
  hops) — maybe force encrypt-all or destroy-mode defaults for passthrough
  targets. Also: never inject faults into prod by default (arm gate).
- Killer workflow: point at QA → click through a flow → retrace records it →
  that recording becomes the CI mock. "Record from QA, replay stackless
  forever, revalidate weekly." Pairs with a scheduled `retrace revalidate`
  drift-bot that flags when QA's API shape drifts from the recordings.
- Auth: passthrough needs real auth headers to QA; those must be
  redact-encrypted in recordings but replayed... destroy-mode placeholders
  work for replay-matching; revalidate needs live creds from env. Design
  moment here.

## 3. Further ideas (mine, cheaper/adjacent)

- **OTLP export**: emit ensemble hops as OpenTelemetry spans so a local run
  shows up in DD/Grafana/Jaeger — inverse of import; makes ensemble play
  nice with existing observability habits. Probably small (OTLP/HTTP JSON).
- **Scenario scripts**: declarative YAML "chaos scenarios" (sequence of
  seed/fault/latency/flip steps with assertions) runnable via CLI —
  reusable incident drills; also the natural artifact for the LLM loop in
  (1f) to generate and persist.
- **Session time-travel**: pair a session's hops with inspector snapshots
  (before/after fingerprints already exist in the poller) so "what did the
  DB look like at hop 14" is answerable. Cheap tier: snapshot at session
  start/end only.
- **retrace drift-bot**: scheduled `retrace revalidate` in CI against QA;
  opens a PR/issue with the wire-diff when the backend drifts. Turns
  recordings from fixtures into a living contract.

Suggested discussion order tomorrow: passthrough safety rails (2) →
fault-injection scope (1b) → MCP vs recipe (1e) → everything else.
