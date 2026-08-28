# retrace-multi-listener

## ADDED Requirements

### Requirement: Multiple named listeners in one standalone config
A standalone `retrace.yaml` (no `entry:`) SHALL support a `listeners:` list,
each entry naming a proxy listener with a unique `name`, an `upstream` base
URL, and an optional fixed `host`/`port`; `retrace run` SHALL bind one proxy
listener per entry in a single process, each recording against its own
upstream into the same run directory.

#### Scenario: Two listeners, two upstreams, one run
- **WHEN** `retrace.yaml` declares `listeners: [{name: edge, upstream:
  http://localhost:4000}, {name: auth, upstream: http://localhost:4050}]`
  and `retrace run` records a flow that calls both
- **THEN** the run directory's `wire.jsonl` contains hops from both
  upstreams, each tagged with its listener's name

#### Scenario: Duplicate or empty listener names are refused at load
- **WHEN** `retrace.yaml`'s `listeners:` list has two entries with the same
  `name`, or an entry with an empty `name`
- **THEN** `retrace run`/`retrace replay` refuse to start with an error
  naming the conflicting entry, before any capture or replay begins

### Requirement: Bare `upstream:` is sugar for a single listener
A config using today's bare `upstream:`/`proxy_host:`/`proxy_port:` fields (no `listeners:`) SHALL behave identically to an explicit one-entry `listeners:` list whose entry is named `client-edge` — the same name
`retrace run` has always used for its one listener — so every existing
config, run, and committed reference bundle keeps working with zero changes
to its recorded hop tags.

#### Scenario: Existing single-upstream config is unaffected
- **WHEN** a config sets only `upstream: http://localhost:4000` (no
  `listeners:`)
- **THEN** capture and replay behave exactly as before this change, and
  every recorded hop's tag is `client-edge`, unchanged

#### Scenario: Mixing the sugar and the spelled-out form is refused
- **WHEN** a config sets both `upstream:` (or `proxy_host:`/`proxy_port:`)
  AND a non-empty `listeners:` list
- **THEN** the config is refused at load time with an error naming both
  forms, rather than silently preferring one

### Requirement: `listeners:` and `entry:` are mutually exclusive
A config combining ensemble-attached mode (`entry:`) with a non-empty `listeners:` list SHALL be refused at load time, since `entry:` mode
already captures every service ensemble's proxy mesh sees from one attach
point and has no use for a second, standalone-only listener list.

#### Scenario: entry: with listeners: is a load error
- **WHEN** a config sets both `entry: some-service` and a non-empty
  `listeners:` list
- **THEN** `retrace run` refuses to start with an error naming both keys

### Requirement: Per-listener environment variables
`retrace run` and `retrace replay` SHALL export one `RETRACE_PROXY_URL_
<NAME>` environment variable per configured listener (name upper-cased,
non-alphanumeric runs collapsed to `_`), in addition to keeping
`RETRACE_PROXY_URL` pointing at the first-declared listener's address, so
an existing single-listener test file needs no changes and a
multi-listener test file can address each backend by name.

#### Scenario: Multi-listener env exposes both forms
- **WHEN** `retrace run` starts a config with listeners named `edge` and
  `auth`, in that order
- **THEN** the test command's environment has `RETRACE_PROXY_URL` (equal to
  `RETRACE_PROXY_URL_EDGE`), `RETRACE_PROXY_URL_EDGE`, and
  `RETRACE_PROXY_URL_AUTH`

### Requirement: Replay never serves one listener's traffic through another's port
`retrace replay` SHALL bind one listening port per configured listener and
answer each port only with the recorded exchanges captured through that
listener; a request matching another listener's recorded traffic but
arriving on the wrong port SHALL be reported as a miss, never served.

#### Scenario: A client asking the wrong listener's port gets a miss, not a cross-served answer
- **WHEN** a replayed flow's client sends a request matching a call
  recorded on listener `auth`'s port, to listener `edge`'s port instead
- **THEN** `retrace replay` reports it as an unmatched request (a miss),
  and does not serve `auth`'s recorded response
