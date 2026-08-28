## Context

`core/trace/redact.go`'s `Redactor` already has the right seam — capture
scrubs "before the ring, before the writer, before anything is streamed"
(`capture.go`'s own comment names this "Phase 4b's" hook point) — but only
one behavior lives there: replace the matched value with the literal
`[redacted]`. That is correct for a bearer token retrace never needs again.
It is wrong for a field a real assertion depends on: an account number, an
order id tied to a real record. A project with such a field today has two
bad options — redact it (the diff can never see it change) or don't (the
value sits in git, in every committed `.retrace-ref/` bundle, forever).

This was scoped once, as task 4.8 of `init-ensemble-retrace`
(`design.md` §6.1.1, `specs/{core-trace-model,retrace-capture-replay}`),
and never built. This change carries it to implementation.

## Goals / Non-Goals

**Goals:**
- A field can be captured, diffed, replayed, and reviewed at its real
  value — by a developer with the team key, and by CI with the same key
  from a secret — while the byte on disk (and in every git commit) is
  never the plaintext.
- Rotating the team key never requires re-recording anything.
- Every existing config, recording, and `.retrace-ref/` bundle in the wild
  keeps behaving exactly as it does today with zero edits.

**Non-Goals:**
- A public-key / multi-recipient model (age/SOPS-style). The proposal
  keeps this open (envelope encryption underneath makes it addable later)
  but a single shared team key is all this change builds.
- Re-deriving the destroy-mode marker as a correlatable hash
  (`red-<hash8>`). See the proposal's "Corrections" section — deferred as
  a documented follow-up, not built here.
- Encrypting whole recordings (`recordings: encrypt-all`). Field-level only;
  the whole-recording option is mentioned in the old draft but has no
  concrete requester and roughly doubles the surface (masks/pixel diff
  would need their own opaque-blob handling). Left for a future change if
  someone actually needs it.

## Decisions

### D1: Envelope encryption, per recording, sidecar-stored

Each run gets one random 32-byte data key (DEK) at capture time. Every
`encrypt`-mode field in that run is sealed with that DEK (fresh nonce per
field). The DEK itself is sealed with the team key (KEK) and written,
base64, to a new sidecar: `encryption.json`, next to `manifest.json`:

```json
{
  "schema": "retrace/encryption/1",
  "keyId": "a1b2c3d4",
  "wrappedDataKey": "<base64 nonce+ciphertext>"
}
```

This is the same shape decision `retrace-ci-sync` already made for
`source.json` — a small, optional, additively-versioned sidecar rather
than a `Manifest` schema change. Two reasons that pattern fits here even
better than it did for source metadata:

- **`retrace rekey` becomes a rewrap, not a re-record.** Rotating the team
  key means: for every run/reference, unwrap the DEK with the old key,
  rewrap with the new key, overwrite `encryption.json`. Zero bytes of
  wire.jsonl/hops.jsonl are touched. If the DEK lived inline in
  `manifest.json`, this would still be true in principle, but a sidecar
  makes "did this file's encryption change" a one-file diff instead of a
  noisy line inside the manifest.
- **`Manifest` stays schema-stable.** `ReadManifest`/`WriteManifest`,
  every JSON consumer of a manifest (dashboard, `retrace export`, CI
  artifact readers), and every existing fixture keep working unmodified.
  A run with no `encryption.json` simply has no `encrypt`-mode fields —
  the same "absence is the local case" reading `source.json` established.

`keyId` is `hex(sha256(teamKey))[:8]` — enough to notice "this run was
wrapped by a different team key than the one I have" and fail with a
clear message, not enough to leak anything about the key itself.

### D2: `encrypt` requires the key at capture time — hard failure, not fallback

If `retrace.yaml` names a field `mode: encrypt` and neither
`RETRACE_RECORDING_KEY` nor `.retrace/recording.key` resolves, capture
refuses to start. This matches the existing rule one scroll up in
`config.go`: `Loaded == false` (no config found) already means "refuse
rather than write unredacted hops to disk as a degraded mode." An
`encrypt` rule with no key to encrypt with is the same shape of danger —
the alternative (silently writing plaintext, or silently downgrading to
`destroy`) is a surprise a developer discovers by grepping a committed
bundle, which is exactly the failure mode this feature exists to prevent.

### D3: Marker format and mode defaults — corrected from the original draft

See the proposal's "Corrections" section for the reasoning. Settled here:

- `destroy` — unchanged from today: matched value becomes the literal
  `core/trace.Redacted` (`"[redacted]"`). Default for the built-in
  auth-header list and for any bare-scalar `redact:` entry.
- `encrypt` — matched value becomes `$enc:v1:<base64(nonce||ciphertext)>`.
  Opt-in only, via the mapping form: `{ field: account_number, mode:
  encrypt }`.
- `display` — capture-time no-op (value passes through as captured).
  Exists so a config can say "this field is fine in the clear" for a field
  that would otherwise be swept up by a broader glob, without also
  requesting encryption. Dashboards still mask it behind reveal-on-click —
  screen protection only, per the original design's framing of `display`.

### D4: `RedactEntry` — bare-scalar or mapping, same as `WireIgnoreEntry`

```yaml
redact:
  - password                              # bare scalar: Field=password, Mode=destroy
  - field: account_number
    mode: encrypt
    why: "checkout total needs the real account id to assert against"
  - field: display_name
    mode: display
```

`UnmarshalYAML` mirrors `WireIgnoreEntry` (`config.go` around line 328)
exactly: scalar node → `{Field: s, Mode: ModeDestroy}`; mapping node →
decode with hand-rolled known-field enforcement (`field`, `mode`, `why`).
`Why` is accepted but NOT added to `ValidateWhy`'s ratchet in this change —
seeing whether teams actually want it enforced before wiring it into
`require_why` is a smaller follow-up than reworking the ratchet's message
format for a new tolerance kind up front.

### D5: Team key loading — env first, then a gitignored keyfile

`retrace/reckey.LoadTeamKey(dir string) (key []byte, source string, err
error)`:
1. `RETRACE_RECORDING_KEY` env var — hex or base64, sniffed by length
   (32 raw bytes decodes to 64 hex chars or 44 base64 chars).
2. `<dir>/.retrace/recording.key` — raw bytes, no encoding (created via
   `retrace rekey --init`, gitignored the same way `.retrace/runs/` is —
   `**/.retrace/*` already covers it, so no `.gitignore` change is
   needed).
3. Neither present → a typed "no team key" error. Callers that only need
   the key for an *optional* decrypt (diff, serve, review) treat this as
   "show markers, not values." Callers where the key is *required*
   (capture with an `encrypt`-mode field configured, `retrace rekey`)
   surface it as a hard failure.

CI wiring is then just: `env: { RETRACE_RECORDING_KEY: ${{
secrets.RETRACE_RECORDING_KEY }} }` on the job step, no retrace-side
change needed beyond this env var being the one the tool already looks
for — this is also why the earlier proposal step confirmed keeping this
exact name rather than inventing a new one.

### D6: Decrypt-on-read, everywhere a field is displayed or replayed

Four read paths need the same `TryDecrypt(marker string, dataKey []byte)
(value string, ok bool)`:
- `retrace/diff` (wire field comparison) — decrypts both sides before
  comparing when the key resolves, so a real value change is a real wire
  diff again, not two identical markers.
- `retrace/replay` — decrypts before serving a mocked response body/header
  to the client under test. This is the one CI actually needs: a client
  asserting against the real account number in a strict-mock replay must
  see the real number.
- `retrace/serve` (review queue + item detail) — decrypts for the API
  response only when the server process's own env/keyfile resolves the
  key; otherwise the JSON carries the marker, unresolved.
- Dashboards (`retrace-ui`, `ensemble-ui`) never see a key. They render
  whatever `retrace/serve` sent — a resolved value or a marker — and a
  masked field's reveal-on-click just re-requests the same field from the
  server, which is already either plaintext (key present server-side) or
  still a marker (it isn't). There is no client-side crypto.

Each of these four sites gets the SAME function from `core/trace`
(`DecryptField`) fed the SAME per-run DEK (unwrapped once via
`retrace/reckey.UnwrapDataKey` from that run's `encryption.json` +
whichever team key resolved) — no second implementation of the marker
format or the AEAD parameters.

### D7: `retrace ref accept` copies `encryption.json` byte-for-byte

`retrace/refs`' bundle assembly already copies `manifest.json`,
`wire.jsonl`, `hops.jsonl`, `shots/` into `.retrace-ref/<app>/<flow>/`
without transforming them. `encryption.json` joins that copy list,
untouched — the reference bundle's encrypted fields decrypt with whatever
team key(s) `retrace rekey` has kept its wrapped DEK current against,
exactly like a run's.

## Risks / Trade-offs

- **Losing the team key loses the data, forever, by design.** This is the
  entire point (envelope encryption with no recovery backdoor), but it
  means `RETRACE_RECORDING_KEY` needs the same operational care as any
  other production secret — worth calling out in the CLI's own `--help`
  text for `rekey` and in the sample config's comment, not just in this
  doc.
- **`retrace rekey` touching every run under `.retrace/runs/` in one pass**
  is a bulk filesystem operation; a crash partway through must leave every
  touched file independently valid (rewrap is old-key-decrypt then
  new-key-encrypt of one small blob, written via the existing
  write-tmp-then-rename pattern `retrace/runs` already uses elsewhere) —
  no run should be left with a DEK wrapped by neither key.
