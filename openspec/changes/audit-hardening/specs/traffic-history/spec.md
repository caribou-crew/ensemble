## ADDED Requirements

### Requirement: Persisted traffic is queryable
`GET /api/traffic/history` SHALL serve hops from the persisted
`.ensemble/hops.jsonl`, newest-first, paginated by `before=<seq>` +
`limit`, honoring the same filters as `GET /api/traffic`
(`errorsOnly`, `session`, plus method/path/status filtering to match the
UI's filter grammar server-side where practical). Corrupt lines SHALL be
skipped and counted, never fail the request.

#### Scenario: Reach traffic older than the ring
- **WHEN** the in-memory ring has rolled past hop `seq=900`
- **THEN** `?before=1000&limit=100` returns hops 900–999 from disk

#### Scenario: No history file
- **WHEN** no `hops.jsonl` exists yet
- **THEN** the endpoint returns an empty page, not an error

### Requirement: Load earlier in the Traffic view
The dashboard Traffic view SHALL offer a "load earlier" affordance that
pages backwards through history, visually distinguishing loaded-from-disk
hops from live-streamed ones only insofar as ordering stays by `seq`.

#### Scenario: Scroll into yesterday
- **WHEN** the user clicks "load earlier" repeatedly
- **THEN** progressively older hops append below, and live streaming
  continues unaffected

### Requirement: Whole-session HAR export
`GET /api/sessions/{id}/export?format=har` SHALL export every hop carrying
that session id (from ring plus history) as one HAR, reusing the existing
per-trace HAR rendering. The CLI SHALL expose it as
`ensemble traffic --session <id> --export har`.

#### Scenario: Export a recording session
- **WHEN** a retrace run's session id is exported
- **THEN** a single HAR contains all of that run's hops in order
