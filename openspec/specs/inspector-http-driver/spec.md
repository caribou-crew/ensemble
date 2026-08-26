# inspector-http-driver

## Purpose
TBD

## Requirements

### Requirement: HTTP-backed inspector driver
Ensemble SHALL accept a `databases:` entry with `type: http` declaring a
required `url` and optional `headers`, and SHALL register an
`inspector.Driver` for it that serves `Tables`/`Rows`/`Fingerprint` via
`GET {url}/tables`, `GET {url}/rows?table=&limit=&offset=`, and
`GET {url}/fingerprint?table=` respectively, sending `headers` on every
request. This entry SHALL then behave identically to a postgres/mysql/
dynamodb entry for every existing consumer: `GET /api/databases`, the
schema/rows endpoints, and the SSE change stream.

#### Scenario: A stateful service exposes itself for inspection
- **WHEN** a service with no real database implements the three-route
  contract and a `databases:` entry of `type: http` points `url` at it
- **THEN** that entry's tables and rows appear in the dashboard's inspector
  exactly as a real database's would, and the SSE stream fires a change
  event when the service's underlying state changes

#### Scenario: Missing url is rejected
- **WHEN** a `databases:` entry sets `type: http` with no `url`
- **THEN** `Load` fails naming the entry

#### Scenario: Unknown table
- **WHEN** `rows` or `fingerprint` is requested for a table name the service
  doesn't recognize
- **THEN** the service SHALL return 404, and the driver surfaces that as
  the same "not found" error `Inspector.Rows`/`Schema` already returns for
  an unregistered database
