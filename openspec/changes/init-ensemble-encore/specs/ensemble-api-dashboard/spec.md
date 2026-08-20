# ensemble-api-dashboard

## ADDED Requirements

### Requirement: API-first parity
Every capability available in the dashboard SHALL be available via REST/SSE
(JSON), and the CLI SHALL be a thin client of that API, so humans, TUI, and
LLM agents observe identical data and controls.

#### Scenario: Agent sets latency
- **WHEN** an agent POSTs a latency rule via REST
- **THEN** the dashboard reflects the rule immediately and the CLI lists it identically

### Requirement: Topology view
The dashboard SHALL render the configured stack as a clustered graph with
per-service status/health, heat by traffic, and a trace-scoped causal layout
when a trace is selected.

#### Scenario: Trace-scoped layout
- **WHEN** a user selects a trace in the traffic view
- **THEN** the topology re-lays out to show only the causal chain of that trace with per-hop timings

### Requirement: Traffic view
The dashboard SHALL show live and historical hops with filtering, per-hop
detail (headers/bodies/timings), chain/flow grouping by trace, errors-only
mode, follow mode, and export (HAR/curl/raw).

#### Scenario: Hop detail
- **WHEN** a user opens a hop
- **THEN** they see redacted headers, body, status, and the timing breakdown including any injected delay

### Requirement: Generic DB inspector
The dashboard SHALL provide schema browsing, table row viewing, and a two-tier
change stream (mutation snapshot diffs + background poller; DynamoDB Streams
for dynamo) for configured postgres, mysql, and dynamodb databases.

#### Scenario: Change stream catches external write
- **WHEN** any client writes a row to a watched table
- **THEN** the inspector emits a change event with before/after values within the poll interval

### Requirement: Entity plugin slots
Config-declared `entities` (base URL + id field) SHALL render generic
list/detail/CRUD pages in the dashboard without custom code.

#### Scenario: Users entity page
- **WHEN** config declares a `users` entity against a BFF endpoint
- **THEN** the dashboard shows a Users page with list, detail, create, update, delete wired to that endpoint

### Requirement: Terminal cockpit
The `ensemble` CLI with no args SHALL open a TUI cockpit (services
status/restart/placement, traffic tail with filter and yank-as-curl, seeds),
each view backed by the same REST endpoints.

#### Scenario: Yank as curl
- **WHEN** a user selects a hop in the TUI and yanks it
- **THEN** a reproducing curl command is copied
