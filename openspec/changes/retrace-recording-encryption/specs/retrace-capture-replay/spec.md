## MODIFIED Requirements

### Requirement: Strict replay
`retrace replay --ref <flow>` SHALL serve a blessed recording as mocks from
the single binary, matching requests via wire-rules; unmatched requests
SHALL fail the run by default with a report of misses. Any `encrypt`-mode
field in the served response SHALL be decrypted before being written to
the client when the team key resolves from `RETRACE_RECORDING_KEY` or
`.retrace/recording.key`; when it does not resolve, replay SHALL fail the
match for that response rather than serve the ciphertext marker as though
it were the real value.

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
