# retrace-capture-replay

## ADDED Requirements

### Requirement: Runner-agnostic capture
`retrace run --flow <name> -- <command>` SHALL execute the user's test command
with env pointing at a capture proxy, recording screenshots (via adapters),
wire traffic, flow-part groups, and — when an ensemble session is active —
the full multi-hop chain, into a per-runId directory with a versioned
manifest.

#### Scenario: Full-chain recording with ensemble
- **WHEN** retrace runs a Playwright flow while ensemble is up
- **THEN** the run dir contains manifest.json, shots/, wire.jsonl, hops.jsonl, groups.jsonl with a shared runId and trace-linked hops

#### Scenario: Standalone recording
- **WHEN** retrace runs without ensemble
- **THEN** the run dir contains client-edge wire.jsonl and the manifest notes the reduced capture scope

### Requirement: Capture-trust verdict
Every recording SHALL carry a capture-trust verdict
(ok/suspect/degraded/broken/failed) computed from environment checks, proxy
attach verification, and propagation-gap detection; all report surfaces SHALL
banner non-ok verdicts.

#### Scenario: Degraded run flagged
- **WHEN** a mid-chain service dropped trace headers during a parallel run
- **THEN** the manifest verdict is degraded with the offending service named, and the review UI banners it

### Requirement: Strict replay
`retrace replay --ref <flow>` SHALL serve a blessed recording as mocks from the
single binary, matching requests via wire-rules; unmatched requests SHALL fail
the run by default with a report of misses.

#### Scenario: Client deviation caught in CI
- **WHEN** a code change adds an extra API call not present in the reference
- **THEN** replay exits non-zero and reports the unmatched request

### Requirement: Reference bundles
Accepted recordings SHALL be stored as compact committed reference bundles
(size-bounded, redacted) addressable per app/flow.

#### Scenario: Reference committed
- **WHEN** a reviewer accepts a run as the new reference
- **THEN** a compact bundle is written under the references directory suitable for git commit

### Requirement: Encrypted recordings replay with fidelity
Replay SHALL decrypt encrypt-mode fields at serve time (team key from
RETRACE_RECORDING_KEY env or a gitignored keyfile, envelope-wrapped per
recording), SHALL treat destroy-mode placeholders as pairing-stable values,
and SHALL support an opt-in encrypt-all mode for whole recordings. `retrace
rekey` SHALL rotate the team key by re-wrapping data keys without
re-recording.

#### Scenario: CI replay with the key
- **WHEN** CI runs replay with RETRACE_RECORDING_KEY set against a recording with encrypted response fields
- **THEN** the client under test receives the original recorded values

#### Scenario: Recording without the key
- **WHEN** a recording with encrypted fields is opened without the key
- **THEN** views show masked markers, replay of those fields fails with a clear key-missing error, and nothing plaintext is recoverable from the artifact

### Requirement: Revalidation
`retrace revalidate` SHALL re-run reference recordings against a live stack and
report server-side drift between the recording and current responses.

#### Scenario: Stale recording detected
- **WHEN** a service's response shape changed since the reference was recorded
- **THEN** revalidate reports the drift per field without failing on rule-matched volatile fields
