## Why

Entity links today only support `kind: url` — a `{{column}}`-templated string opened via `window.open`/`location.assign` in the browser. That covers web targets and OS-registered custom schemes, but there is no way to open a deep link on a connected Android device or iOS Simulator from the dashboard: doing so today means copying a row value out of the table, switching to a terminal, hand-assembling an `adb`/`xcrun` command, and running it — repeated on every iteration of local development.

An earlier draft of this proposal added a server-side executor (`POST /api/entities/{name}/exec` running a whitelisted CLI command). Review surfaced that this would be the first request-content-to-argv path in ensemble's otherwise-unauthenticated local API, with a real CSRF/loopback-bind blast radius. This proposal instead adds a **copy-to-clipboard** button: the dashboard assembles the exact `adb`/`xcrun` command client-side (same trust boundary as the existing `kind: url` links, which already resolve templates against row data in the browser) and copies it for the developer to paste and run themselves. This removes the new-server-surface risk entirely while still eliminating the actual friction (hand-assembling the command).

## What Changes

- Add a new entity link type, `kind: exec`, alongside the existing (default) `kind: url`.
- `kind: exec` links reference one of a closed, Go-authored set of command templates (`ios-simctl-openurl`, `adb-view`) via an `exec:` config key, plus the existing `template:` field for `{{column}}` interpolation.
- `GET /api/entities` gains `kind` and `argv` fields per link, expanding the exec command's argv (Go-authoritative — the `exec:` key is validated against the known table at config load and never sent to the client).
- The dashboard resolves the template against the row (client-side, same as `kind: url` today), single-quote-escapes the resolved value into the command's argv slots, and copies the joined command string to the clipboard on click, using the existing `CopyButton` interaction pattern.
- Config validation (fatal, at `ensemble up`): `kind` must be a known value; `kind: exec` requires a known `exec:` name; `kind: url` must not set `exec:`; the template's scheme must be literal (not sourced from a row column); the template's literal text must contain no control characters.
- Render-time validation (per row, client-side): a resolved command containing an ASCII control character, or a template with an unresolved `{{column}}`, disables the button with a reason shown as its title — never silently produces an unsafe or wrong command.
- No new HTTP route, no server-side process execution, no CSRF/auth changes, no change to how the API binds.
- A future, separate proposal may add server-side auto-execution behind a loopback-only gate and a CSRF token — deliberately out of scope here; see `design.md` appendix for the deferred design.

## Capabilities

### New Capabilities
- `entity-exec-links`: config schema, validation, and dashboard behavior for `kind: exec` entity links that copy an assembled local CLI command (targeting a connected Android device or iOS Simulator) to the clipboard.

### Modified Capabilities
(none — `kind: url` link behavior is unchanged; `kind: exec` is additive)

## Impact

- `config/config.go`, `config/validate.go`, new `config/execcommands.go` (the closed command table).
- `server/entities.go`, `server/openapi.go` (expand `entityLink` response shape).
- `dashboard/ensemble-ui/src/api/types.ts`, `dashboard/ensemble-ui/src/format.ts` (new `shellQuote`/`buildExecCommand`), `dashboard/ensemble-ui/src/views/EntityView.tsx` (branch on `kind`, reuse `CopyButton`).
- Docs: entity links page gains the `kind: exec` section and the command table.
- No changes to HTTP routing, server auth/CSRF posture, or API bind policy.
