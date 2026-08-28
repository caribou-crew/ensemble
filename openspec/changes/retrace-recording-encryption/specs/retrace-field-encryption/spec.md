## ADDED Requirements

### Requirement: Per-key redaction modes
`redact:` entries SHALL support three modes — `destroy` (the existing
irreversible behavior: matched value becomes the literal `[redacted]`),
`encrypt` (field-level AES-256-GCM at capture), and `display` (plaintext at
rest, a capture-time no-op) — via a mapping form (`{field, mode, why}`); a
bare scalar entry SHALL mean `destroy`, matching every existing config's
current behavior with no edit required.

#### Scenario: Existing bare-scalar config is unaffected
- **WHEN** a config has `redact: [password, card_number]` with no team key
  configured anywhere
- **THEN** capture succeeds and both fields become `[redacted]`, identical
  to today's behavior

#### Scenario: Encrypt mode is opt-in
- **WHEN** a config has `redact: [{ field: account_number, mode: encrypt
  }]` and a team key resolves
- **THEN** the captured field becomes an `$enc:v1:...` marker, not
  `[redacted]` and not the plaintext value

### Requirement: A team key is required to capture an encrypt-mode field
`retrace run` and `retrace record`'s underlying capture SHALL refuse to
start — before any traffic is captured — when a `redact:` entry names
`mode: encrypt` and no team key resolves from `RETRACE_RECORDING_KEY` or
`.retrace/recording.key`.

#### Scenario: Missing key fails the capture, not the field
- **WHEN** a config names an `encrypt`-mode field and no team key resolves
- **THEN** capture exits with an error naming the missing key, and no run
  directory is created

### Requirement: Envelope encryption with a per-recording data key
Each run SHALL get one random data key at capture time, used to encrypt
every `encrypt`-mode field in that run; the data key SHALL itself be
wrapped by the resolved team key and written to an `encryption.json`
sidecar beside `manifest.json`, never in plaintext anywhere on disk.

#### Scenario: Sidecar absent when nothing is encrypted
- **WHEN** a run has no `encrypt`-mode fields configured
- **THEN** no `encryption.json` is written for that run

### Requirement: `retrace rekey` rotates the team key without re-recording
`retrace rekey --old <key> --new <key>` SHALL walk every run under
`.retrace/runs/` and every reference under `.retrace-ref/`, unwrap each
`encryption.json`'s data key with the old team key, rewrap it with the new
key, and overwrite the sidecar — without reading or rewriting any
encrypted field value.

#### Scenario: Rotation preserves decryptability
- **WHEN** `retrace rekey --old K1 --new K2` completes against a tree with
  encrypted recordings
- **THEN** every previously-encrypted field decrypts correctly when
  `RETRACE_RECORDING_KEY=K2` is set, and fails to decrypt under `K1`

### Requirement: Reveal and redact-rule actions in the review UI
The dashboard's wire diff view SHALL render `encrypt`- and `display`-mode
fields masked by default with a reveal-on-click action that re-fetches the
field from the server (decrypting only when the server process's own
environment resolves the team key), and SHALL offer an "add redaction
rule" action on any wire entry that writes a new `redact:` entry into
`retrace.yaml`.

#### Scenario: Reveal without a server-side key shows an explanation
- **WHEN** a reviewer clicks reveal on an encrypted field and the server
  process has no team key configured
- **THEN** the field stays masked and the UI states the key is not
  available, rather than showing an empty or broken value
