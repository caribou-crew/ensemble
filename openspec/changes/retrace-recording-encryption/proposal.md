## Why

`redact:` today has exactly one mode: destroy. A matched header or body
field becomes the literal string `[redacted]` before anything touches disk —
irreversible, by design (`core/trace/redact.go`). That is correct for a
bearer token, but wrong for a field a real test needs to validate: an
account number, an order total tied to a real customer record, anything
whose *actual value* the diff is supposed to catch a regression in. A
project that redacts such a field gets a recording that always diffs clean
(both sides show `[redacted]`) or, worse, a recording nobody dares commit at
all — which is exactly the point this is blocking: recording a real flow
and letting CI validate it, when the flow touches data that can't sit in
git as plaintext.

This capability was scoped once already, as task 4.8 of
`init-ensemble-retrace`, and never built — `design.md` §6.1.1 and two spec
files there describe `display`/`encrypt`/`destroy` per-key modes with a
team-key round trip. This change carries that design to implementation,
correcting a few specifics the original draft got wrong for this codebase
(the destroy-mode marker format, and the default mode for existing
configs) and adding the pieces the draft only gestured at (where the
per-recording key lives, how `retrace rekey` finds every recording).

## What Changes

- Add per-key redaction **modes** to `redact:` — `destroy` (today's
  behavior, unchanged, still the default for a bare string entry and for
  the built-in auth-header list), `encrypt` (field-level AES-256-GCM at
  capture; the marker `$enc:v1:<base64 nonce+ciphertext>` is what reaches
  disk), and `display` (plaintext at rest, a capture-time no-op — the mode
  exists so a config can say "this field is fine in the clear, don't
  destroy it" without also asking for encryption).
- Add a **team key**: `RETRACE_RECORDING_KEY` (env var, hex or base64) or a
  gitignored `.retrace/recording.key` file next to `retrace.yaml`. Every
  recording gets its own random data key, wrapped by the team key
  (envelope encryption) and written to a small `encryption.json` sidecar
  next to `manifest.json` — the same sidecar pattern `retrace-ci-sync`'s
  `source.json` already established. A config with an `encrypt`-mode field
  and no team key present fails the capture loudly rather than silently
  falling back to plaintext or to destroy.
- `retrace diff`, `retrace replay`, `retrace serve`, and the dashboard's
  Retrace tab all decrypt `encrypt`-mode fields when the team key is
  available (same env var / keyfile lookup), and show the marker,
  unrecoverable, when it isn't. Replay is where this matters for CI: a
  client under strict-mock replay gets the real recorded value back, not
  the ciphertext marker.
- Add `retrace rekey --old <key> --new <key>`: rewraps every recording's
  data key (runs and committed `.retrace-ref/` bundles) without touching
  the encrypted field values themselves — a team key rotates in one pass
  over small sidecar files, not a re-record.
- Dashboard: fields under `encrypt` or `display` mode render masked with a
  reveal-on-click action (decrypts server-side, only when the server's own
  environment has the team key — the key itself never reaches the
  browser); a "redact this field" action on any wire entry writes a new
  `redact:` rule into `retrace.yaml` from the review queue, mirroring the
  existing `rule` verb's write-back-to-config pattern.

## Corrections to the original (never-implemented) design

- **Destroy-mode marker stays `[redacted]`, not `red-<hash8>`.** The
  original draft wanted a deterministic per-value hash placeholder so two
  different secrets in one recording stay distinguishable. Every
  already-committed `.retrace-ref/` bundle in every project using retrace
  today has `[redacted]` baked into its wire.jsonl; changing the marker
  format would make every one of them diff dirty against a fresh capture
  the day this ships, for a benefit (telling apart two different auth
  tokens within one run) no project has asked for. Kept as a documented
  follow-up, not built here.
- **A bare `redact: [name, ...]` entry stays `destroy` mode, not
  `encrypt`.** The draft defaulted user-listed body fields to `encrypt`.
  That would make every existing `redact:` list in every project suddenly
  require a team key to capture at all — a capture-time hard failure
  introduced by upgrading retrace, not by editing a config. `encrypt` is
  opt-in: `- { field: account_number, mode: encrypt }`.

## Capabilities

### New Capabilities
- `retrace-field-encryption`: reversible, per-key AES-256-GCM redaction at
  capture, with team-key round-tripping through diff/replay/serve/CI and a
  `retrace rekey` rotation path.

### Modified Capabilities
- `retrace-capture-replay`: replay decrypts `encrypt`-mode fields when the
  team key is present (was: replay serves recorded bytes verbatim).
- `core-trace-model`: `Redactor` gains per-key modes (was: a single
  destroy-only key list).

## Impact

- `core/trace`: `redact.go` gains `Mode`/`KeyRule` and per-key dispatch;
  new `encrypt.go` (AES-256-GCM field seal/open, marker parse).
- `retrace/reckey` (new package): team-key loading (env/keyfile), data-key
  generate/wrap/unwrap, key fingerprinting.
- `retrace/runs`: new `encryption.go` (`Encryption` type,
  `WriteEncryption`/`ReadEncryption` sidecar, mirroring `source.go`).
- `retrace/config`: `Redact []string` becomes `Redact []RedactEntry`
  (bare-scalar back-compat via `UnmarshalYAML`, matching `WireIgnoreEntry`).
- `retrace/capture`: `NewRedactor` call sites (`capture.go`, `ensemble.go`,
  `hopsource.go`) generate/load the data key and pass modes through;
  `Session.Close`/`RecordExternalHops` write `encryption.json`.
- `retrace/diff`, `retrace/replay`, `retrace/serve`: decrypt on read when
  the team key resolves; masked marker otherwise.
- `retrace/refs`: `encryption.json` copies into `.retrace-ref/` bundles
  alongside `manifest.json`.
- `retrace/cmd/retrace`: new `rekey` subcommand.
- `dashboard/design-system` + `dashboard/retrace-ui` + `dashboard/ensemble-ui`:
  masked-field reveal action and add-redaction-rule action in the wire
  diff view.
- No impact to `core/proxy`, orchestration, entities, inspector, or the
  pixel/hop diff planes.
- Non-breaking: no team key configured and no `encrypt`-mode field used —
  every existing config, recording, and reference bundle behaves exactly
  as it does today.
