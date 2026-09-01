## ADDED Requirements

### Requirement: Detect env wiring that bypasses capture
On `ensemble up` (and config reload/reconcile), for every service and stub
`env:` value containing a loopback-or-docker-host reference to a port
(`127.0.0.1:<p>`, `localhost:<p>`, `host.docker.internal:<p>`): if `<p>`
equals another declared node's real `port:` and that node also declares a
`proxy:` port, a wiring warning SHALL be produced naming the referencing
service, the env var, the target node, and the proxy port to use instead.
Ports matching a node's `proxy:` port, a stub's port, a database port, or
no declared node SHALL NOT warn. Warnings never fail `up`.

#### Scenario: Real port where proxy port intended
- **WHEN** service `edge` has `env: { CATALOG_URL: "http://127.0.0.1:8081" }`,
  and `catalog` declares `port: 8081` + `proxy: 9081`
- **THEN** `up` prints a wiring warning telling `edge` to use `9081`, and
  the stack still starts

#### Scenario: Correct proxy wiring
- **WHEN** the same env var references `9081`
- **THEN** no warning is produced

#### Scenario: Node without a proxy
- **WHEN** the referenced node declares no `proxy:` port
- **THEN** no warning is produced (there is no capture to bypass)

### Requirement: Warnings are visible everywhere status is
Wiring warnings SHALL appear in `ensemble up` terminal output, in a
`warnings` field of `GET /api/status` (and `ensemble status`/`--json`),
and as a badge on the dashboard Services tab for the referencing service.

#### Scenario: Status carries the warning
- **WHEN** a wiring warning exists
- **THEN** `ensemble status --json` includes it under `warnings` with the
  service, env var, and suggested port

### Requirement: Variant-aware evaluation
Validation SHALL evaluate the env of the *active* variant (or flip
placement) of each service, re-evaluating on variant switch.

#### Scenario: Only one variant mis-wired
- **WHEN** variant `stub` wires correctly but variant `real` references a
  real port
- **THEN** the warning appears only while `real` is the active variant
