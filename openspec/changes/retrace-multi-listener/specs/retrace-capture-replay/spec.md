# retrace-capture-replay

## MODIFIED Requirements

### Requirement: Runner-agnostic capture
`retrace run --flow <name> -- <command>` SHALL execute the user's test command
with env pointing at a capture proxy, recording screenshots (via adapters),
wire traffic, flow-part groups, and — when an ensemble session is active —
the full multi-hop chain, into a per-runId directory with a versioned
manifest. In standalone mode (no ensemble session), the config MAY declare
more than one named listener (`listeners:`), each proxying its own upstream
into the same run directory; a config with no `listeners:` list keeps
recording through exactly one listener, named `client-edge`, unchanged from
prior behavior.

#### Scenario: Full-chain recording with ensemble
- **WHEN** retrace runs a Playwright flow while ensemble is up
- **THEN** the run dir contains manifest.json, shots/, wire.jsonl, hops.jsonl, groups.jsonl with a shared runId and trace-linked hops

#### Scenario: Standalone recording
- **WHEN** retrace runs without ensemble
- **THEN** the run dir contains client-edge wire.jsonl and the manifest notes the reduced capture scope

#### Scenario: Standalone recording through multiple listeners
- **WHEN** retrace runs without ensemble, against a config declaring two
  `listeners:` entries
- **THEN** the run dir's wire.jsonl contains hops from both listeners, each
  tagged with its own listener name rather than a single `client-edge` tag

### Requirement: Strict replay
`retrace replay --ref <flow>` SHALL serve a blessed recording as mocks from
the single binary, matching requests via wire-rules; unmatched requests
SHALL fail the run by default with a report of misses. Any `encrypt`-mode
field in the served response SHALL be decrypted before being written to
the client when the team key resolves from `RETRACE_RECORDING_KEY` or
`.retrace/recording.key`; when it does not resolve, replay SHALL fail the
match for that response rather than serve the ciphertext marker as though
it were the real value. When the reference bundle's recording used more
than one listener, replay SHALL bind one port per listener and answer each
port only with the exchanges recorded through that listener.

#### Scenario: Client deviation caught in CI
- **WHEN** a code change adds an extra API call not present in the
  reference
- **THEN** replay exits non-zero and reports the unmatched request

#### Scenario: CI replay decrypts a field the client asserts against
- **WHEN** a reference response has an `encrypt`-mode field and
  `RETRACE_RECORDING_KEY` is set to the same team key the recording was
  captured under
- **THEN** the client under test receives the original plaintext value in
  that field

#### Scenario: Replay without the key never leaks the marker as data
- **WHEN** a reference response has an `encrypt`-mode field and no team key
  resolves
- **THEN** replay reports a key-missing error for that response rather
  than serving the `$enc:v1:...` marker string as the field's value

#### Scenario: Multi-listener replay routes by port, not by path
- **WHEN** a reference bundle was recorded through listeners `edge` and
  `auth`
- **THEN** `retrace replay` binds one port per listener and each port only
  answers requests matching that listener's own recorded exchanges
