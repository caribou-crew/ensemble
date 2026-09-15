## Why

Two client apps — a legacy one and its replacement — are developed against
one shared local ensemble stack, and the question all day long is "where
does the new one's backend traffic diverge from the old one's". The data to
answer it already exists: `core/proxy` reads a client-identity header
(`client_identity_headers`, default `x-source-client` / `x-local-client`)
into `hop.client`, validates it against `proxy.ValidClient` precisely so it
can be grouped on, and `HopTable` already renders it as a badge. Every hop
also carries `traceId`, and every `retrace run` already registers its run id
as the ensemble session id, so `hop.session` already joins a run to its
hops.

None of that is reachable as a product surface. `GET /api/traffic`,
`/api/traffic/history` and `/api/traffic/stream` filter on
`errorsOnly`/`session`/`method`/`path`/`status` but not on client;
`ensemble traffic` has `--session`, `--since`, `--errors-only`, `--follow`
and no `--client`; the dashboard's query grammar has a `session:` field and
a session dropdown but no client equivalent; and the Traffic view shows one
merged stream with no way to put two clients beside each other. Answering
"where do the two apps diverge" today means `ensemble traffic --json |
jq 'select(.req.headers["x-source-client"]=="…")'` twice, in two terminals,
and reading the interleave by eye.

The concrete divergence that motivated this — one client calling a service
directly while the other routes through an extra edge service and 404s on a
profile path that only exists on the first route — is obvious in seconds
side by side, and took a hand-written `jq` filter to find.

## What Changes

- **Client becomes a filter dimension on every traffic read.**
  `GET /api/traffic`, `GET /api/traffic/history` and `GET
  /api/traffic/stream` accept `client=<identity>`, honored server-side, and
  composable with every filter they already take. `ensemble traffic` gains
  `--client`, which works with `--follow`, `--errors-only`, `--session`,
  `--since` and `--export`.
- **A hop is attributed to a client transitively, and never guessed.** A
  client identity is recorded wherever its header arrives, which in practice
  is the entry hop; an internal service-to-service hop carries it only if
  that service forwards the header. Filtering on the stored field alone
  would therefore show a client's entry hop and hide the chain behind it —
  exactly the part that reveals a routing divergence. A hop with no client
  of its own SHALL inherit the client of the entry hop sharing its
  `traceId`; a hop whose trace has no resolvable client is **unattributed**
  and belongs to no client, rather than to all of them. The resolution rule
  lives in `core/trace`, is derived at read time, and is never written to a
  hop.
- **The dashboard gets a client dropdown and a `client:` query field**,
  mirroring the session dropdown and `session:` field it already has,
  including an explicit "no client" selection.
- **The dashboard gets a two-pane side-by-side Traffic mode.** Pick a client
  for the left pane and one for the right; both panes render rows on one
  shared, interleaved row axis, so a call that exists on one side and not
  the other leaves a visible gap opposite it. Both panes are fed by the one
  existing traffic stream, partitioned in the browser — side-by-side opens
  no second SSE connection.
- **A retrace run and its ensemble hops become one navigable object.** A
  `retrace run` in ensemble-attached mode records the control plane it
  attached to and the session id it registered in its manifest, so the
  retrace dashboard can link out to that run's traffic in the ensemble
  dashboard, and the ensemble Traffic view can be deep-linked to one run's
  hops by URL.

## Capabilities

### New Capabilities
- `client-traffic-filtering`: client as a first-class filter on the traffic
  API, the CLI and the dashboard's query grammar, including the transitive
  trace-based attribution rule and the unattributed bucket.
- `client-side-by-side`: the dashboard's two-pane Traffic mode — pane
  selection, the shared row axis, and what an empty slot opposite a row
  means.
- `retrace-run-traffic-link`: the manifest's record of the ensemble control
  plane and session a run was captured against, and the navigation both
  ways between a retrace run and its ensemble traffic.

### Modified Capabilities
(none — no existing spec covers traffic filtering or the Traffic view; the
behavior added here is additive to every surface it touches)

## Out of Scope

- **Cross-run diff mode** (pick client A's run and client B's run, and
  label status/payload-shape deltas as intentional vs. actionable). This is
  the natural follow-on, and the `retrace-run-traffic-link` capability is
  the prerequisite it needs; `retrace diff`'s existing wire-diff machinery
  and the `comparison-sides` spec's intentional-vs-actionable framing are
  where it should land, not in a fifth Traffic-view mode. Sketched in
  `design.md` §Deferred so the shape is on the record; not specified or
  built here.
- A2E capture of a side-by-side comparison in the dashboard summary. Same
  reason: it depends on cross-run diff, and the short-term ask is the live
  view.
- The TUI. The API change makes a TUI client filter straightforward later;
  `ensemble-tui` is untouched by this change.

## Impact

- `core/trace`: new read-time client-attribution helper (trace-id index +
  per-hop resolution) and its shared testdata fixture. No change to the
  `Hop` schema, and nothing derived is ever written — the persisted hop
  keeps only what was observed.
- `ensemble/server`: `client=` on `handleTraffic`, `handleTrafficHistory`
  and `handleTrafficStream`; `openapi.go` updated for all three.
- `ensemble/cmd/ensemble`: `--client` on `cmd_traffic.go`, threaded through
  `client.go`'s `TrafficFiltered`/`TrafficStream`.
- `dashboard/ensemble-ui`: `trafficFilter.ts` gains a `client` colon field;
  `TrafficView.tsx` gains the client dropdown and the side-by-side mode;
  `urlState.ts` gains the pane/client URL keys; a new two-pane component
  beside `HopTable`.
- `retrace/runs`: `Manifest` gains an optional `Ensemble` link (pointer,
  absent in standalone mode, matching `Stack`/`Device`/`Fixtures`).
- `retrace/capture`: `StartAttached` records the link it just established.
- `dashboard/retrace-ui` + `dashboard/design-system`: a link out to the
  ensemble dashboard on a run captured against one.
- Non-breaking everywhere: every request with no `client` param, every
  existing CLI invocation, and every manifest written before this change
  behave exactly as they do today.
