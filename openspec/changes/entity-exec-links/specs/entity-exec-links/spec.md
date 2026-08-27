## ADDED Requirements

### Requirement: `kind: exec` entity link configuration
An entity `links` entry MAY set `kind: exec`. When present, it SHALL also set `exec:` naming a command in the closed, Go-authored command table, and `template:` (the existing `{{column}}`-interpolated string). An entity link with `kind` absent or `kind: url` SHALL NOT set `exec:`.

#### Scenario: Valid exec link
- **WHEN** an entity config declares a link with `kind: exec`, `exec: adb-view`, and a `template:`
- **THEN** `ensemble up` starts successfully and the link is served to the dashboard

#### Scenario: url link with exec key set
- **WHEN** an entity config declares a link with `kind: url` (or no `kind`) and also sets `exec:`
- **THEN** `ensemble up` fails with a fatal error naming the entity and link index

### Requirement: Exec command name validation
`ensemble up` SHALL reject, with a fatal error, any `kind: exec` link whose `exec:` value does not match a name in the command table. The error SHALL list every valid command name.

#### Scenario: Unknown exec name
- **WHEN** an entity config declares a link with `kind: exec` and `exec: adb-veiw` (typo)
- **THEN** `ensemble up` fails at startup with an error listing the valid command names, rather than starting with a link that silently does nothing

### Requirement: Literal scheme requirement for exec templates
`ensemble up` SHALL reject, with a fatal error, any `kind: exec` link whose `template:` does not have a literal URI scheme (matching `^[a-zA-Z][a-zA-Z0-9+.\-]*:`) before its first `{{` placeholder. The scheme portion of the template SHALL NOT be sourced from a row column.

#### Scenario: Template scheme comes from a column
- **WHEN** an entity config declares a `kind: exec` link with `template: "{{scheme}}://widget/{{id}}"`
- **THEN** `ensemble up` fails at startup with a fatal error identifying the entity and link

#### Scenario: Template with literal scheme
- **WHEN** an entity config declares a `kind: exec` link with `template: "myapp://widget/{{id}}"`
- **THEN** validation passes

### Requirement: No control characters in exec template literal text
`ensemble up` SHALL reject, with a fatal error, any `kind: exec` link whose `template:` contains an ASCII control character (byte < 0x20 or 0x7F) in its literal (non-placeholder) text.

#### Scenario: Control character in template literal text
- **WHEN** an entity config declares a `kind: exec` link whose template's literal text contains a control character
- **THEN** `ensemble up` fails at startup with a fatal error identifying the entity and link

### Requirement: Exec command argv exposed to the dashboard
The `GET /api/entities` response SHALL include, for each `kind: exec` link, the link's `kind` and its `argv` — the command table's argv template for the named command, with exactly one element equal to the literal sentinel `"{{url}}"` marking the slot for the resolved URL. The `exec:` config key itself SHALL NOT be included in the response. Links with `kind: url` (or no `kind`) SHALL be served exactly as they are today, with no `kind` or `argv` field present.

#### Scenario: Exec link served with argv
- **WHEN** the dashboard requests `GET /api/entities` for an entity with a `kind: exec` link
- **THEN** the response includes that link's `kind: "exec"` and an `argv` array containing exactly one `"{{url}}"` element

#### Scenario: url link response is unchanged
- **WHEN** the dashboard requests `GET /api/entities` for an entity with only `kind: url` (or default) links
- **THEN** each link in the response has no `kind` or `argv` field

### Requirement: Client-side command resolution and clipboard copy
For a `kind: exec` link, the dashboard SHALL resolve the link's `template:` against the current row's fields entirely client-side (the same resolution semantics as `kind: url` links), substitute the resolved value into the command's `{{url}}` argv slot with shell-safe quoting, join the resulting argv into a single command string, and copy that string to the system clipboard when the link's button is clicked. No network request SHALL be made to resolve or execute the command.

#### Scenario: Click copies the assembled command
- **WHEN** a developer clicks a `kind: exec` link's button for a row
- **THEN** the fully assembled command string (e.g. `adb shell am start -a android.intent.action.VIEW -d 'myapp://widget/abc'`) is copied to the system clipboard and no HTTP request is sent

### Requirement: Shell-safe quoting of the resolved URL
The dashboard SHALL wrap the resolved URL value in POSIX single-quote escaping (`'`, with any embedded `'` replaced by `'\''`) before substituting it into the command's `{{url}}` argv slot. Other (literal, Go-authored) argv elements SHALL NOT be quoted.

#### Scenario: URL containing shell metacharacters
- **WHEN** the resolved URL is `myapp://widget?a=1&b=2`
- **THEN** the copied command contains the URL single-quoted, so pasting it into a POSIX shell runs it as one argument rather than being word-split at `&`

#### Scenario: URL containing a single quote
- **WHEN** the resolved URL contains a literal `'` character
- **THEN** the copied command escapes it using the POSIX `'\''` idiom rather than rejecting the value

### Requirement: Resolved command must not contain control characters
The dashboard SHALL refuse to produce or copy a command string that contains any ASCII control character (byte < 0x20 or 0x7F) anywhere in the resolved command. When this occurs, the link's button SHALL render disabled with a reason indicating a control character was present in the row data.

#### Scenario: Row value contains a newline
- **WHEN** a row's field used in a `kind: exec` link's template contains a newline character
- **THEN** the button for that link renders disabled with a reason, and no command is copied to the clipboard on click

### Requirement: Missing template column disables the button
For a `kind: exec` link, if any `{{column}}` placeholder in the template resolves to a missing or empty row value, the dashboard SHALL render the link's button disabled with a reason naming the missing column, rather than copying a command built with an empty substitution.

#### Scenario: Referenced column absent from row
- **WHEN** a `kind: exec` link's template references `{{widget_token}}` and the row has no `widget_token` field
- **THEN** the button renders disabled with a reason naming `widget_token`, and clicking it does nothing

### Requirement: Command preview shown before copy
The dashboard SHALL display the fully resolved command string as the link button's tooltip (`title` attribute) whenever the button is enabled, so a developer can see exactly what will be copied before clicking.

#### Scenario: Hovering an enabled exec link button
- **WHEN** a developer hovers a `kind: exec` link's button that is enabled
- **THEN** the tooltip shows the exact command string that will be copied to the clipboard
