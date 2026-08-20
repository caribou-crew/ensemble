# sample-stack

## ADDED Requirements

### Requirement: Deep polyglot demo
The repo SHALL ship a sample stack ("brew", a coffee-ordering storefront):
Expo RN client, React web client, Go edge gateway, two Node BFFs, Go
catalog-svc (Postgres), Java/Spring order-svc (MySQL, behind profile `full`),
Node user-svc (Postgres), Go notify-worker (Redis queue), DynamoDB Local cart
storage, and payment/analytics/kms stubs — all real CRUD, all forwarding
trace headers, all with health endpoints, wired solely through a checked-in
`ensemble.yaml`.

#### Scenario: First run without JDK
- **WHEN** a user runs `ensemble up` in sample/ without the full profile
- **THEN** the stack starts without requiring Java and the order flow degrades gracefully or is marked unavailable

#### Scenario: Six-hop trace
- **WHEN** an order is placed from the web client with the full profile up
- **THEN** the dashboard shows one trace spanning edge → bff → order-svc → (catalog, user, payment-stub) → queue → notify-worker

### Requirement: Named seeds
The sample SHALL define seed targets `baseline`, `empty`, `bulk`, and
`outage` via the generic seed mechanism.

#### Scenario: Seed baseline
- **WHEN** `ensemble seed baseline` runs
- **THEN** products, users, and a few orders exist and the clients render them

### Requirement: Dog-food e2e
CI SHALL run retrace against the sample stack (record → replay → diff) so both
products are exercised end-to-end on every commit.

#### Scenario: CI loop
- **WHEN** CI runs the e2e job
- **THEN** a flow is recorded live, replayed from the recording without the stack, and diffed with zero unexplained differences
