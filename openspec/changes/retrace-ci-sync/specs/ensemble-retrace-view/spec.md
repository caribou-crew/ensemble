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

### Requirement: Multiple independently-configured repos can share one dashboard
`ensemble.yaml`'s `retrace:` block SHALL accept an `instances:` map
(`ensemble/config.RetraceConfig.Instances`), each entry an independent
`.retrace/` directory with its own `dir`, `repo`(s), `workflow`(s), sync
filters (`branch`/`actor`/`event`/`status`/`since`), and `apps` map — the
same fields `retrace:`'s own flat form already supports, scoped per repo
instead of dashboard-wide. This stays same-process: every instance is still
served by importing `retrace/serve` directly against that instance's
directory, never a separate `retrace serve` process per repo.

When `instances:` is absent (today's single-repo form), `ensemble/server`
SHALL behave exactly as before this requirement existed —
`RetraceConfig.EffectiveInstances()` synthesizes a single unlabeled
`"default"` entry from the block's own flat fields, so no request needs an
`?instance=` param and the dashboard shows no picker. This is what makes
`instances:` strictly additive rather than a breaking change to every
existing single-repo `ensemble.yaml`.

`ensemble/server` SHALL expose `GET /api/retrace/instances`, returning
`{instances: [{key, label}]}` for every entry in `EffectiveInstances()`
(one row for the synthetic `"default"` entry when `instances:` is unset).
Every existing retrace route (queue, item, shots, evidence, video, report,
sync, sync candidates) SHALL accept an optional `?instance=` query
parameter naming one of those keys, resolving to that instance's own
directory/config for the request; omitting it SHALL work exactly as
`?instance=` naming the sole configured instance when there is only one,
and SHALL be rejected (400) as ambiguous when `instances:` declares more
than one and none was named. An unrecognized `?instance=` key SHALL be
rejected (404).

The dashboard's Retrace tab SHALL call `GET /api/retrace/instances` before
loading the queue: with one instance (or none declared), it SHALL behave
exactly as the single-repo tab always has, with no picker and no
`?instance=` on any request. With more than one, it SHALL show a picker
listing every instance's label; after a repo is chosen, every subsequent
request SHALL carry that instance's key as `?instance=`, and the resulting
view SHALL be identical — same shared components, same data — to what
running `retrace serve` directly against that repo's own `.retrace/`
directory would show.

#### Scenario: Single-repo config is unaffected
- **WHEN** `ensemble.yaml`'s `retrace:` block declares no `instances:` map
- **THEN** `GET /api/retrace/instances` returns exactly one entry, the
  Retrace tab shows no picker, and every route behaves exactly as it did
  before `instances:` existed

#### Scenario: Multiple repos are each independently configured
- **WHEN** `retrace:` declares `instances: {web: {repo: "org/web", dir:
  "../web/.retrace"}, backend: {repo: "org/backend", dir:
  "../backend/.retrace"}}`
- **THEN** `GET /api/retrace/queue?instance=web` and
  `GET /api/retrace/queue?instance=backend` return each repo's own queue,
  computed from each repo's own `.retrace/` directory, and `POST
  /api/retrace/sync?instance=web` pulls from `org/web` without touching
  `backend`'s sync state

#### Scenario: Picking a repo shows the same dashboard `retrace serve` would
- **WHEN** a developer opens the Retrace tab against a multi-instance
  config and picks the "backend" repo
- **THEN** the queue, drill-down, and sync panel render identically to
  running `retrace serve` directly inside the backend repo's own checkout

#### Scenario: An unnamed instance is ambiguous, not silently wrong
- **WHEN** `retrace:` declares more than one `instances:` entry and a
  request to any retrace route omits `?instance=`
- **THEN** the response is 400, naming that an instance must be specified,
  rather than silently defaulting to one repo's data
