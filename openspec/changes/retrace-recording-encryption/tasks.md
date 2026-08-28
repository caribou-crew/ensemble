## 1. `core/trace`: modes and field-level AES-256-GCM

- [x] 1.1 Add `core/trace/encrypt.go`: `EncryptField(dataKey []byte,
      plaintext string) (marker string, err error)` — AES-256-GCM, random
      12-byte nonce, output `"$enc:v1:" + base64(nonce||ciphertext)`;
      `DecryptField(dataKey []byte, marker string) (plaintext string, err
      error)` — parses the prefix, base64-decodes, splits nonce/ciphertext,
      opens; `IsEncrypted(s string) bool` — prefix check only.
- [x] 1.2 Unit tests: encrypt/decrypt round-trips a value; decrypt with the
      wrong data key fails with a clear error (not a panic); `IsEncrypted`
      on a plain string, a `[redacted]` marker, and an `$enc:v1:...` marker.
- [x] 1.3 In `redact.go`: add `type Mode string` (`ModeDisplay`,
      `ModeEncrypt`, `ModeDestroy`), `type KeyRule struct { Key string;
      Mode Mode }`. Change `NewRedactor(rules []KeyRule, maxBody int,
      dataKey []byte) (*Redactor, error)` — returns an error if any rule is
      `ModeEncrypt` and `dataKey == nil` (see D2). Keep
      `defaultRedactHeaders`/`defaultRedactQuery` wired in as `ModeDestroy`
      rules, unchanged marker (`Redacted`).
- [x] 1.4 `redactValue`/`Payload`/`redactPath` dispatch on the matched
      rule's mode: `ModeDestroy` → today's `Redacted` literal (unchanged);
      `ModeEncrypt` → `EncryptField`; `ModeDisplay` → passthrough, no
      change to the value.
- [x] 1.5 Tests: a config mixing all three modes across headers, body
      fields, and one query param produces the right marker per field in
      one `Payload`/`Hop` call; existing `redact_test.go` cases (all
      `ModeDestroy` today) pass unchanged with the new constructor
      signature.

## 2. `retrace/reckey`: team-key primitives (new package)

- [x] 2.1 `LoadTeamKey(dir string) (key []byte, source string, err error)`
      — env `RETRACE_RECORDING_KEY` first (hex or base64, sniffed by
      decoded length == 32), then `<dir>/.retrace/recording.key` (raw
      bytes), else a typed `ErrNoTeamKey`.
- [x] 2.2 `GenerateDataKey() ([]byte, error)` — 32 random bytes via
      `crypto/rand`.
- [x] 2.3 `WrapDataKey(dataKey, teamKey []byte) (wrapped string, err
      error)` / `UnwrapDataKey(wrapped string, teamKey []byte) ([]byte,
      error)` — AES-256-GCM over the data key, base64 output, same
      nonce-prefixed shape as `core/trace.EncryptField` (reuse it directly
      rather than a second AEAD call site).
- [x] 2.4 `KeyID(teamKey []byte) string` — `hex(sha256(teamKey))[:8]`.
- [x] 2.5 Tests: `LoadTeamKey` resolves env over keyfile when both present;
      resolves keyfile when env unset; returns `ErrNoTeamKey` when neither
      exists; hex and base64 env values both decode to the same 32 bytes;
      wrap/unwrap round-trips; `KeyID` is stable and 8 hex chars.

## 3. `retrace/runs`: `encryption.json` sidecar

- [x] 3.1 Add `retrace/runs/encryption.go`, mirroring `source.go`:
      `EncryptionFile = "encryption.json"`, `EncryptionSchema =
      "retrace/encryption/1"`, `type Encryption struct { Schema, KeyID,
      WrappedDataKey string }`, `Paths.EncryptionPath() string`,
      `WriteEncryption(p Paths, e Encryption) error` (via the package's
      existing `writeJSONFile`), `ReadEncryption(p Paths) (*Encryption,
      error)` returning `(nil, nil)` when absent.
- [x] 3.2 Tests: write/read round-trip; missing file is `(nil, nil)`, not
      an error; malformed JSON and wrong schema both error; confirm (test)
      that `ReadManifest`/`WriteManifest` are untouched and unaware of this
      file, matching `source.go`'s own task 1.3 precedent.

## 4. `retrace/capture`: wire the data key through capture

- [x] 4.1 `capture.Options` gains `Redact []config.RedactEntry` (or the
      capture package's own mirrored type — decide based on whether
      `capture` already imports `retrace/config`; avoid a new import cycle)
      replacing the old `[]string`. Resolve modes to `trace.KeyRule` at the
      top of `StartStandalone`/`StartEnsemble`.
- [x] 4.2 Before constructing the `Redactor`: if any rule is `ModeEncrypt`,
      call `reckey.LoadTeamKey`; on `ErrNoTeamKey`, fail capture start with
      a clear message naming the field and the two places a key can come
      from (see D2 — no run directory should be created). Otherwise
      generate a data key (`reckey.GenerateDataKey`), wrap it
      (`reckey.WrapDataKey`), and pass the raw data key into
      `trace.NewRedactor`.
- [x] 4.3 At session close (`Session.Close` in `ensemble.go`, and the
      standalone path in `capture.go`), write `runs.Encryption{KeyID,
      WrappedDataKey}` via `runs.WriteEncryption` when a data key was
      generated for this run; write nothing when no `encrypt`-mode rule
      was configured (mirrors `source.json`'s absence-is-local pattern —
      here absence is "nothing to decrypt").
- [x] 4.4 `hopsource.go`'s `RecordExternalHops` reuses the SAME
      already-resolved data key from the `Session` (not a second
      load/generate) — one data key per run, whichever path wrote hops.
- [x] 4.5 Tests: capturing with an `encrypt`-mode field and
      `RETRACE_RECORDING_KEY` set produces a run whose `wire.jsonl` has an
      `$enc:v1:` marker and whose `encryption.json` unwraps back to a data
      key that decrypts it; capturing the same config with the env var
      unset fails fast with no run directory left behind; capturing with
      only `destroy`/`display` fields configured writes no
      `encryption.json` at all.

## 5. `retrace/config`: `RedactEntry` and mode parsing

- [x] 5.1 Replace `Redact []string` with `Redact []RedactEntry`.
      `RedactEntry{ Field, Mode, Why string }` with `UnmarshalYAML`
      mirroring `WireIgnoreEntry` exactly (config.go ~line 328): bare
      scalar → `{Field: s, Mode: "destroy"}`; mapping → decode with
      known-field enforcement (`field`, `mode`, `why`). `Mode` defaults to
      `"destroy"` when the mapping form omits it. Reject an unrecognized
      `mode` value at load time (typo'd `"encypt"` must fail loudly, not
      silently become `destroy`).
- [x] 5.2 `RedactKeyRules() []trace.KeyRule` helper on `Config` (or a free
      function) — the seam `retrace/capture` calls instead of reading
      `Redact` directly, matching `WireIgnorePaths()`'s existing role for
      `WireIgnore`.
- [x] 5.3 Tests: bare-scalar list parses identically to today
      (`redact: [password]` → one `ModeDestroy` rule); mapping form with
      `mode: encrypt`/`mode: display` parses correctly; unknown mode value
      is a load error; a config mixing both forms in one list parses all
      entries correctly. Update `sample/retrace.yaml`'s existing
      `redact:` list — confirm it still parses (bare-scalar form,
      unchanged behavior) as a regression check, not a required edit.

## 6. Decrypt-on-read: diff, replay, serve

- [x] 6.1 Add a small shared helper (`retrace/reckey` or a new tiny
      `retrace/reckey/decrypt.go`): `ResolveDataKey(p runs.Paths, dir
      string) ([]byte, error)` — `ReadEncryption`, if nil return `(nil,
      nil)` (nothing to decrypt for this run); else `LoadTeamKey` +
      `UnwrapDataKey`. Every one of 6.2–6.4 calls this ONE function rather
      than re-deriving the key-resolution chain three times.
- [x] 6.2 `retrace/diff`'s wire field comparison (`wire.go`): before
      comparing two paired field values, if either is `trace.IsEncrypted`,
      resolve that side's data key (A's run dir, B's run dir — they can
      differ) and decrypt for the comparison; a field that fails to
      decrypt (key unavailable) compares as still-masked (both sides equal
      only if both markers are byte-identical, same as today's `destroy`
      behavior) rather than erroring the whole diff.
- [x] 6.3 `retrace/replay`'s response server (`bundle.go` / server path):
      decrypt `encrypt`-mode fields in the served body/headers before
      writing to the client; if the reference's data key can't be resolved
      (no team key), fail that response's replay with a named error rather
      than serving `$enc:v1:...` as the literal value (spec: "never leak
      the marker as data").
- [x] 6.4 `retrace/serve`'s item/queue response: decrypt for the JSON
      payload when the SERVING process's own key resolves; otherwise leave
      the marker as-is in the response (the API's existing shape needs no
      new field — a client sees either the plaintext or the marker string,
      and `trace.IsEncrypted` tells it which).
- [x] 6.5 Tests per site: diff sees a real change between two different
      plaintext values behind two different encrypted markers (proving the
      marker alone would have hidden it); replay serves the decrypted
      value to a test client when the key is set, and fails clearly when
      it isn't; serve's JSON carries the plaintext when its own env has the
      key and the marker when it doesn't.

## 7. `retrace/refs`: carry `encryption.json` into reference bundles

- [x] 7.1 Add `encryption.json` to the file list `retrace ref accept`
      copies into `.retrace-ref/<app>/<flow>/reference/`, byte-for-byte,
      alongside `manifest.json`/`wire.jsonl`/`hops.jsonl`/`shots/`. Absent
      when the source run has none.
- [x] 7.2 Test: accepting a run with `encryption.json` produces a
      reference bundle whose own `encryption.json` unwraps with the same
      team key and decrypts the same fields; accepting a run with none
      produces a reference bundle with none (no regression for every
      existing project's reference bundles).

## 8. `retrace rekey` CLI command

- [x] 8.1 New `retrace/cmd/retrace/cmd_rekey.go`: `retrace rekey --old
      <key> --new <key>` (both accept the same hex/base64 forms
      `LoadTeamKey` does, passed directly rather than read from env, so a
      rotation can run with both keys in hand without exporting either as
      `RETRACE_RECORDING_KEY`). Walks `.retrace/runs/**/encryption.json`
      and `.retrace-ref/**/encryption.json` (via `retrace/runs`' existing
      directory-walking helpers, not a hand-rolled `filepath.Walk`), unwraps
      each with `--old`, rewraps with `--new`, writes back via the
      package's atomic `writeJSONFile` path (through `WriteEncryption`).
- [x] 8.2 A file that fails to unwrap with `--old` (wrong key, or already
      rewrapped by a previous partial run whose new key matches `--new`'s
      `KeyID`) is reported and skipped, not fatal to the whole walk — the
      command's summary lists rewrapped vs. skipped counts.
- [x] 8.3 `retrace rekey --init` (documented in `--help`, not the primary
      path): writes a freshly generated key to `.retrace/recording.key`
      when neither it nor the env var exists yet, for a project's first
      setup.
- [x] 8.4 Tests: rekey over a tree with two encrypted runs and one
      reference bundle rewraps all three and the fields still decrypt
      under the new key and fail under the old; re-running rekey a second
      time with the same `--old`/`--new` is a no-op that reports "already
      on --new" rather than erroring; a run wrapped by a third, unrelated
      key is reported skipped, not silently left half-migrated.

## 9. Dashboard: reveal-on-click + add-redaction-rule

- [x] 9.1 `dashboard/design-system`'s `WireDiffTable` (or the entry-detail
      view it renders into): a field value that is `trace.IsEncrypted` (or
      arrives from the API pre-decrypted — see 6.4, the API is the source
      of truth for whether it's masked) renders masked with a reveal
      affordance; clicking it re-fetches that one field's value from the
      owning app's existing item-detail endpoint rather than assuming the
      already-loaded payload has the plaintext.
- [x] 9.2 An "add redaction rule" action on any wire entry/field — opens a
      small form (field name pre-filled, mode picker: destroy/encrypt/
      display, why) and calls a new mutation endpoint that appends a
      `RedactEntry` to `retrace.yaml`, mirroring the existing `rule` verb's
      write-back-to-config code path (`retrace/cmd/retrace/cmd_ref.go` or
      wherever `POST /api/rule` is currently handled — reuse that
      config-file-editing helper, don't duplicate YAML-node surgery).
- [x] 9.3 Wire the same component change through both consumers
      (`dashboard/retrace-ui` and `dashboard/ensemble-ui`'s
      `RetraceView`) — no second implementation, per the existing
      shared-component seam `retrace-ci-sync` established
      (`resolveShotUrl`-style prop injection if the reveal fetch needs an
      app-specific URL).
- [x] 9.4 Tests: masked field renders masked by default; reveal click calls
      the expected endpoint and displays the returned value; reveal on a
      server with no team key shows the "key not available" state from the
      spec, not an error boundary; add-rule form submission produces the
      expected `retrace.yaml` diff (via a test double / temp config dir,
      not a real file mutation in the test tree).

## 10. Docs, sample config, end-to-end verification

- [x] 10.1 `sample/retrace.yaml`: add one `encrypt`-mode entry (a
      plausible "account number"-shaped field, even if the sample app
      doesn't actually send one today — comment explaining it's here to
      demonstrate the capability, same spirit as the existing
      password/card_number comment) — decide during implementation whether
      the sample app needs a small change to actually send such a field,
      or whether documenting the config shape without live traffic is
      sufficient; if a live field is added, generate a
      `RETRACE_RECORDING_KEY` for local verification only (never commit a
      real key) and confirm end-to-end: capture → `encryption.json` +
      `$enc:v1:` marker in wire.jsonl → `retrace diff --json` shows the
      decrypted comparison → `retrace ref accept` copies the sidecar →
      `retrace rekey` rotates it → dashboard reveal shows the real value
      with the key present and stays masked without it.
- [x] 10.2 Add a short section to whichever doc already covers `redact:`
      (`retrace-iterate` skill or `docs/`) describing the three modes, the
      key env var / keyfile, and `retrace rekey` — cross-reference rather
      than duplicate the design doc's D5/D6.
- [x] 10.3 A CI-facing note (near the existing CI workflow template from
      `retrace-ci-sync`, or a new one) showing `env: {
      RETRACE_RECORDING_KEY: ${{ secrets.RETRACE_RECORDING_KEY }} }` on the
      replay/diff step.
- [ ] 10.4 `go test -race ./core/... ./retrace/...` and `pnpm -r
      --if-present test` both green.
