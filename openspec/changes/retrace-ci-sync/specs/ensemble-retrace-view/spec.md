## ADDED Requirements

### Requirement: Ensemble serves the retrace review queue without a separate process
`ensemble/server` SHALL expose `GET /api/retrace/queue`, returning every
recorded flow across every app worst-first, whenever a `retrace:` block is
configured in `ensemble.yaml` — computed by the same
`retrace/serve.BuildQueue` function `retrace serve` uses, never a
re-implementation. The route is always registered (mirroring how
`GET /api/databases` behaves when no inspector is configured): with no
`retrace:` block it responds 501 with a JSON error rather than 404, because
the dashboard's SPA fallback already answers any unmatched path with a 200
app shell — a client checking for "no retrace here" needs a route that
always exists and always answers in JSON to tell the two apart. The
dashboard SHALL show no Retrace tab in that case.

#### Scenario: Queue reflects local and CI runs identically to `retrace serve`
- **WHEN** `.retrace/runs/` contains both a locally recorded run and a
  `retrace sync`-merged CI run for different flows
- **THEN** `GET /api/retrace/queue` on the ensemble server and `GET
  /api/queue` on a `retrace serve` instance pointed at the same directory
  return the same verdict, score, and counts for every flow

#### Scenario: No `retrace:` block means no tab
- **WHEN** a stack's `ensemble.yaml` has no `retrace:` block
- **THEN** `ensemble up` starts normally, `GET /api/retrace/queue` responds
  501 with a JSON error, and the dashboard shows no Retrace tab

#### Scenario: Latest run wins regardless of source
- **WHEN** a flow has a local run recorded yesterday and a CI run synced
  today
- **THEN** `GET /api/retrace/queue/{app}/{flow}` reports the verdict computed
  against today's CI run, with its `source` field showing `kind: "ci"`

### Requirement: Ensemble serves per-flow diff detail and shot images
`ensemble/server` SHALL expose `GET /api/retrace/queue/{app}/{flow}`
(the full `diff.Summary`, including pixel/wire/hop counts and gates) and
`GET /api/retrace/shots/{app}/{flow}/{side}/{name}` (one comparison-pane
PNG: `a`, `b`, `diff`, or `overlay`), computed via the same `SummaryFor`
and checkpoint-resolution path `retrace/serve`'s own handlers use.

#### Scenario: Detail view needs no `retrace serve` process running
- **WHEN** `ensemble up` is running and no `retrace serve` process exists
  anywhere on the machine
- **THEN** `GET /api/retrace/queue/{app}/{flow}` and every
  `GET /api/retrace/shots/...` request for that flow succeed

#### Scenario: Unknown flow is a 404, unevaluable flow is a 409
- **WHEN** a client requests `GET /api/retrace/queue/{app}/{flow}` for a
  flow with no recorded runs and no reference bundle
- **THEN** the response is 404; requesting a flow that exists but has no
  accepted reference yet returns 409 with a message naming `retrace ref
  accept`

### Requirement: Sync is triggerable from the dashboard
`ensemble/server` SHALL expose `POST /api/retrace/sync`, which invokes the
same `retrace/sync` logic the `retrace sync` CLI command uses, using the
stack's configured `retrace:` sync source. The dashboard's Retrace tab
SHALL trigger this route from a "Sync now" action and SHALL NOT poll it
automatically.

#### Scenario: Sync now button triggers a real sync
- **WHEN** a developer clicks "Sync now" in the Retrace tab
- **THEN** `POST /api/retrace/sync` runs, new CI runs (if any) are merged
  into `.retrace/runs/`, and the next `GET /api/retrace/queue` reflects them

#### Scenario: No background polling
- **WHEN** the Retrace tab is open and idle for several minutes
- **THEN** no `POST /api/retrace/sync` request is made without the
  developer clicking "Sync now"

#### Scenario: Sync failure surfaces inline
- **WHEN** `POST /api/retrace/sync` fails (for example, `gh` is not
  installed on the machine running `ensemble up`)
- **THEN** the response carries the same error message the CLI would
  print, and the tab shows it inline rather than a generic failure

### Requirement: Retrace tab shows cross-app status with drill-down
The dashboard SHALL show a Retrace tab, at the same level as Services,
Topology, Traffic, Entities, and Inspector, listing every app/flow pair
from `GET /api/retrace/queue` with verdict, what changed, when the run was
recorded, and its source (local or CI). Selecting a row SHALL show that
flow's full diff detail (pixel, wire, and hop diffs) using the same
diff-viewer components `retrace-ui` uses, rendered inline — not a link to
an external URL.

#### Scenario: Row click opens inline detail
- **WHEN** a developer clicks a flow's row in the Retrace tab
- **THEN** the pixel diff, wire diff, and hop diff for that flow render
  inside the ensemble dashboard, with no navigation to another origin or
  port

#### Scenario: CI vs local is visible at a glance
- **WHEN** the queue contains both a CI-synced run and a locally recorded
  run for different flows
- **THEN** each row's source is visibly distinguishable (e.g. a badge)
  without opening the row
