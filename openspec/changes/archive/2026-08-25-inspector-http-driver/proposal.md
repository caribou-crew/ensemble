# Proposal: inspector-http-driver

Status: proposed (2026-08-23).

## Why

`inspector.Driver` (Tables/Rows/Fingerprint) already covers postgres, mysql,
and dynamodb — anything with a real database socket to connect to. Some
services deliberately have no database: `cardco-go` (local-stack's Go stand-in
for real cardco) keeps its cardholders/cards/accounts in an in-memory `Store`
on purpose, matching real cardco's own dev-DB-gets-wiped-on-restart posture.
There is currently no way to see that state in the dashboard's inspector —
you can watch `cardco-mysql`'s tables when real cardco is the active variant,
but flip `CARDCO_BACKEND=cardco-go` and the inspector goes dark for that
service, with no path to get it back short of adding a bespoke driver to
ensemble itself for every such service.

This is a recurring shape, not a one-off: any service that owns its state
outside a database ensemble already knows how to inspect (an in-memory
store, a SQLite file, a third-party API it wraps) hits the same wall.

## What Changes

- A new `Database.Type: "http"` — same `databases:` block, so a service's
  debug surface shows up in `GET /api/databases`, the schema/rows endpoints,
  and the SSE change stream exactly like postgres/mysql/dynamodb do today.
  No new API surface for the dashboard to learn.
- Two new `Database` fields, meaningful only for `type: http`:
  - `url` — base URL of the service's own inspection endpoint.
  - `headers` — static headers sent on every request (auth for a protected
    debug surface — `Authorization: Basic ...`, a bearer token, whatever the
    service already requires).
- A new `inspector.NewHTTPDriver(baseURL, headers)` implementing `Driver` by
  calling three GET routes the *service* implements — see design.md for the
  exact contract. This is the "adapter": any service earns an inspector page
  by adding three small handlers, no new ensemble binary or plugin-loading
  machinery required.
- `buildInspector`'s type switch gains one `case "http"`.

## Capabilities

### New Capabilities
- `inspector-http-driver`: a `Driver` backed by a small HTTP contract a
  service implements itself, for state that has no real database to point
  postgres/mysql/dynamodb drivers at.

### Modified Capabilities
<!-- No existing requirement's behavior changes — postgres/mysql/dynamodb
     drivers, the dashboard, and the SSE stream are all untouched; `http` is
     purely additive to the Database.Type switch. -->

## Impact

- `ensemble/config`: `Database.URL`, `Database.Headers`; `http` added to
  `validDatabaseTypes`; validation (`url` required when `type: http`).
- `ensemble/inspector`: `NewHTTPDriver`, `httpdriver.go` (+ tests) alongside
  `postgres.go`/`mysql.go`/`dynamo.go`.
- `ensemble/cmd/ensemble`: one `case "http"` in `buildInspector`.
- Reference implementation (not part of this proposal's own diff — lives in
  local-stack): `cardco-go` gains the three-route contract, proving the design
  against a real consumer before any second service adopts it.
- README: config reference, one worked example (cardco-go).
