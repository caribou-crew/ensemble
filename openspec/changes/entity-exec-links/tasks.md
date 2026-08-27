## 1. Config: schema, command table, validation

- [x] 1.1 Add `Kind` and `Exec` fields to `config.EntityLink` (`config/config.go`); zero value preserves today's behavior exactly.
- [x] 1.2 Add `config/execcommands.go`: the closed two-entry command table (`ios-simctl-openurl` → `["xcrun","simctl","openurl","booted","{{url}}"]`, `adb-view` → `["adb","shell","am","start","-a","android.intent.action.VIEW","-d","{{url}}"]`) and a lookup function.
- [x] 1.3 Unit test: every table entry has exactly one `"{{url}}"` argv element (catches a malformed table entry as a test failure, not a dashboard bug).
- [x] 1.4 Extend `config/validate.go`: `kind` must be `""`/`"url"`/`"exec"`; `kind: url` (or absent) must not set `exec:`; `kind: exec` requires `exec:` naming a known table entry (error lists all valid names); `kind: exec` template must have a literal scheme before its first `{{` (regex `^[a-zA-Z][a-zA-Z0-9+.\-]*:`); `kind: exec` template literal text must contain no ASCII control character.
- [x] 1.5 While in this validation loop: add the pre-existing gap noted in review — entity link `label` values should be unique within an entity (`EntityView.tsx` currently uses `key={link.label}`, so duplicates are a React key collision today).
- [x] 1.6 Table-driven tests for every rejection in 1.4/1.5, asserting the exact error message and that it names the entity and link.

## 2. Server: expose exec links over the existing entities endpoint

- [x] 2.1 Add `Kind` and `Argv` fields to the server's `entityLink` response type (`server/entities.go`); `Argv` populated from the config-load-validated table lookup, `Kind` set only for `exec` links.
- [x] 2.2 Confirm the `exec:` config key itself never appears in the JSON response (it's a server-side lookup key only).
- [x] 2.3 Update `server/openapi.go` to document the two new response fields.
- [x] 2.4 Test: a `kind: url` (or default) link serializes exactly as it does today — no `kind`, no `argv` — guarding the existing dashboard client against an unexpected shape change.
- [x] 2.5 Test: a `kind: exec` link serializes with both `kind: "exec"` and the expected `argv`.

## 3. Dashboard: command resolution, quoting, and clipboard copy

- [x] 3.1 Add `kind` and `argv` to the `EntityLink` type in `dashboard/ensemble-ui/src/api/types.ts`.
- [x] 3.2 Add `shellQuote(s: string): string` to `format.ts` — POSIX single-quote escaping (`'` + `s.replace(/'/g, "'\\''")` + `'`).
- [x] 3.3 Add `buildExecCommand(link, row): { command: string } | { disabledReason: string }` to `format.ts`: resolve the template with the existing `resolveLinkTemplate`, check for an unresolved/missing column (disabled reason naming the column), quote the resolved value into the `argv`'s `"{{url}}"` slot, join into a command string, then check the full joined string for ASCII control characters (disabled reason if present). Pure function, no DOM.
- [x] 3.4 Vitest table for `buildExecCommand`/`shellQuote` covering: a normal case, `&`/`?` in the query string, an embedded single quote, a control character in a row value, and a missing template column.
- [x] 3.5 In `EntityView.tsx`, branch the per-link button's `onClick`/render on `link.kind`: `url` (or absent) keeps today's `openResolvedLink(resolveLinkTemplate(...))` behavior unchanged; `exec` calls `buildExecCommand` and either copies the result to the clipboard (reusing the `CopyButton` interaction/feedback pattern) or renders disabled with the reason as its `title`.
- [x] 3.6 Set the button's `title` to the full resolved command string whenever it is enabled (not just on failure) — required so a developer always sees what will be copied before clicking.
- [x] 3.7 Consider extracting `CopyButton`'s "copied"/"copy failed" feedback bubble into a shared piece so the two call sites don't grow independent copies of the same state machine (nice-to-have, not blocking).

## 4. Docs and example config

- [x] 4.1 Add a `kind: exec` section to the entity links docs page: config shape, the two available commands, and why the command table is not config-extensible (a committed `ensemble.yaml` + a free-form `command:`/`args:` variant would mean a PR could put an arbitrary command on a teammate's clipboard).
- [x] 4.2 Add example `kind: exec` links to relevant docs/usage examples using generic placeholder names (e.g. `myapp`, `example-entity`, `widget-card`) — do not reference any real internal app or product name.
- [x] 4.3 Do not add a `kind: exec` link to `sample/ensemble.yaml` — the sample stack has no device attached, so the button would always fail, and the sample config is exercised as a live round-trip (per `f08c9ae`).
