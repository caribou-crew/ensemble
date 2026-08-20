# ensemble-proxy

## ADDED Requirements

### Requirement: One process, N listeners
Ensemble SHALL front each configured service with a dedicated proxy listener
port inside a single Go process (no per-service proxy processes).

#### Scenario: Twenty services, one process
- **WHEN** ensemble.yaml declares 20 services
- **THEN** `ensemble up` opens 20 intercept ports from one OS process

### Requirement: Hop capture and streaming
Every proxied request SHALL produce a hop record (per core-trace-model)
appended to an on-disk NDJSON ring and streamed over SSE with cursor-based
live tail, pause/resume/clear.

#### Scenario: Live tail
- **WHEN** a client calls through any intercept port while a dashboard is connected
- **THEN** the hop appears on the SSE stream with timings and status

### Requirement: Latency injection
The proxy SHALL apply per-target and per-path delay rules (longest-prefix
match; fixed delay or p50/p95/p99 distribution), settable and resettable live
via REST/CLI/dashboard, including arm-all and reset-all.

#### Scenario: Simulated slow dependency
- **WHEN** a 400ms fixed rule is set for target `bff` path `/orders`
- **THEN** matching requests are delayed ~400ms and the injected delay is recorded distinctly from upstream time on the hop

### Requirement: Latency profile import
Ensemble SHALL support importing latency percentiles from external APM
providers (Datadog first) into a cache, applied to rules only on explicit user
action (pull never auto-arms).

#### Scenario: Pull without arm
- **WHEN** a user pulls Datadog percentiles for a target
- **THEN** the cache updates and no delay is injected until the user applies the rule

### Requirement: Stub engine
Ensemble SHALL serve config-defined stub endpoints (match on method/path,
respond with status/headers/body file, optional templating) for dependencies
that cannot run locally.

#### Scenario: AWS KMS stub
- **WHEN** a service calls the configured stub port with POST /encrypt
- **THEN** the stub returns the configured canned response and the call appears as a hop

### Requirement: Session partitioning
Ensemble SHALL register recording sessions, allocate an ephemeral client-edge
port per session that stamps `retrace-run` baggage, partition hops by that
baggage into per-session streams, and route unstamped traffic to the ambient
stream.

#### Scenario: Two suites in parallel
- **WHEN** two retrace runs execute concurrently against one ensemble
- **THEN** each recording contains only its own hops and the ambient stream contains neither

#### Scenario: Propagation gap detected
- **WHEN** hops arrive mid-chain without session baggage while a session is active
- **THEN** the session's capture-trust verdict is set to degraded, naming the non-propagating service
