# ensemble-orchestrator

## ADDED Requirements

### Requirement: Topology-agnostic config
Ensemble SHALL read a user-supplied `ensemble.yaml` declaring services
(`dir`, optional `build` with freshness `watch` globs, `run`, `port`,
assigned `proxy` port, `env`, `health`, `depends_on`, optional `docker`
placement), databases, stubs, entities, latency defaults, named seeds, and
profiles — remaining agnostic to service implementation (jar, node, go,
python, container).

#### Scenario: Jar from a sibling repo
- **WHEN** a service declares `dir: ~/work/ledger` and `run: java -jar build/libs/ledger.jar`
- **THEN** `ensemble up` executes that command in that directory with the injected env

### Requirement: Lifecycle supervision
`ensemble up` SHALL start containers (Docker API) and native processes in
dependency order, gate on health checks, supervise restarts, and expose
status; `ensemble down` SHALL stop everything it started.

#### Scenario: Dependency ordering
- **WHEN** bff declares `depends_on: [svc-a, postgres]`
- **THEN** bff starts only after svc-a is healthy and postgres accepts connections

### Requirement: Live placement flip
A service declaring both `run` and `docker` SHALL be flippable between native
and container placement at runtime without restarting the stack.

#### Scenario: Flip to container
- **WHEN** a user flips a native service to container placement from the dashboard
- **THEN** the native process stops, the container starts, and intercept ports are unchanged

### Requirement: Build-on-restart
A restart of a service with a configured `build` SHALL re-run the build first,
skipping it when `watch` globs show no changes since the last build.

#### Scenario: Fresh build skipped
- **WHEN** a service is restarted and no watched file changed
- **THEN** the build step is skipped and the service starts from existing artifacts

### Requirement: Generic seeding
`ensemble seed <target>` SHALL execute the named seed target from config (SQL
files and/or HTTP calls) against the running stack.

#### Scenario: Baseline seed
- **WHEN** a user runs `ensemble seed baseline`
- **THEN** the configured SQL and HTTP seed steps execute in order and report per-step results

### Requirement: Emulated managed databases
Database entries SHALL support typed containers including `postgres`,
`mysql`, `redis`, `dynamodb` (DynamoDB Local), and `localstack` (with a
`services` list), so AWS-only dependencies can run locally for real where
stubbing is insufficient.

#### Scenario: LocalStack SQS + DynamoDB
- **WHEN** config declares `type: localstack, services: [dynamodb, sqs]`
- **THEN** `ensemble up` starts LocalStack exposing those services and injects its endpoint env into dependents
