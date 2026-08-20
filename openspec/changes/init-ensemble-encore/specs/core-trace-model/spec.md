# core-trace-model

## ADDED Requirements

### Requirement: Unified hop schema
The core module SHALL define a single versioned schema for a network hop
(trace id, correlation id, parent linkage, service names, method, path,
status, timings [proxy-in, upstream-first-byte, upstream-done], headers,
size-capped bodies, redaction markers) used identically by ensemble live
telemetry and encore recordings.

#### Scenario: One schema, two consumers
- **WHEN** ensemble captures a live hop and encore writes the same hop into a recording
- **THEN** both serialize to the identical NDJSON record shape and schema version

### Requirement: W3C trace context propagation
Proxies SHALL stamp `traceparent` when absent, propagate it when present, and
carry `correlationId` and `encore-run` as W3C baggage entries.

#### Scenario: Join key survives the chain
- **WHEN** a request traverses client → edge → bff → service proxies
- **THEN** every recorded hop shares the same trace id and correlation id

### Requirement: Relay collapse
The trace model SHALL support folding transparent relay hops (e.g. edge
proxies that forward unchanged) into a collapsed view while retaining the raw
hops.

#### Scenario: Edge hop folded in display
- **WHEN** a trace includes a hop whose request/response transparently mirrors its parent
- **THEN** the collapsed view merges it and the raw NDJSON still contains both hops

### Requirement: Redaction at capture
The system SHALL redact sensitive headers (authorization, cookie, set-cookie,
dpop, plus a user-configured list) at capture time, before any persistence or
streaming.

#### Scenario: Secret never touches disk
- **WHEN** a proxied request carries an Authorization header
- **THEN** the persisted and streamed hop shows a redaction marker, not the value

### Requirement: Per-key redaction modes
The system SHALL support per-key redaction modes — display (plaintext at
rest, UI-masked), encrypt (field-level AES-256-GCM at capture, marker
`$enc:v1:...`), destroy (deterministic placeholder `red-<hash>` keyed by a
discarded per-recording key) — defaulting auth-bearing headers to destroy
and user-listed fields to encrypt.

#### Scenario: Destroyed values stay correlatable
- **WHEN** the same secret value appears in two hops of one recording under destroy mode
- **THEN** both hops carry the identical placeholder and the value is unrecoverable

#### Scenario: Encrypted field round-trips with the key
- **WHEN** a field is captured under encrypt mode and later read with the team key present
- **THEN** the original value decrypts; without the key only the marker is visible

### Requirement: Export formats
The core SHALL export any trace or hop set as HAR 1.2, curl command(s), and
raw request/response text.

#### Scenario: HAR round-trip
- **WHEN** a user exports a trace as HAR
- **THEN** the file is valid HAR 1.2 and importable by standard tooling
