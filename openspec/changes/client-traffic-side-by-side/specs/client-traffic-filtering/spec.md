## Purpose

Makes the client application that originated a request a filter dimension
on every traffic surface — the REST/SSE API, the `ensemble traffic` CLI,
and the dashboard's query grammar — so traffic from two client apps sharing
one stack can be separated without post-processing, and so a hop deep in a
chain is attributed to the client that started it rather than to nothing.

## ADDED Requirements

### Requirement: A client identity propagates through trace context
Once a client identity has been established from a client-identity header,
the proxy SHALL carry it in trace baggage under a well-known key and SHALL
record it on every subsequent hop whose request arrives carrying that
baggage, so a chain that forwards trace context reports the originating
client on each of its hops rather than only at its edge.

An identity inherited from baggage SHALL take precedence over a
client-identity header present on that later request. An inherited value
SHALL be validated with the same charset rule that governs a header-supplied
one, and an inherited value failing validation SHALL be discarded — NOT
recorded as the malformed-header fallback bucket — leaving that hop to fall
back to its own header.

#### Scenario: A downstream hop records the client that started the chain
- **WHEN** a request carrying `x-source-client: app-legacy` enters the stack
  and the service it reaches forwards trace context to a second service
- **THEN** both recorded hops carry `client: app-legacy`

#### Scenario: A later header cannot relabel the chain
- **WHEN** an internal service forwards trace context but also sets
  `x-source-client: app-next` on its own downstream call, in a chain started
  by `app-legacy`
- **THEN** the downstream hop records `app-legacy`

#### Scenario: A service that drops baggage stops the propagation honestly
- **WHEN** a service forwards `traceparent` but not `baggage`
- **THEN** the downstream hop records no client of its own, rather than
  guessing one, and remains resolvable by the transitive rule below

#### Scenario: Malformed propagated identity is discarded, not bucketed
- **WHEN** a request arrives carrying a client identity in baggage that
  fails the client-identity charset rule
- **THEN** it is discarded rather than recorded as the fallback bucket

### Requirement: A hop's client is resolved transitively through its trace
`core/trace` SHALL provide a read-time resolution of a hop's originating
client over a window of hops — the fallback for chains that did not
propagate the identity above — defined as: the hop's own `client` field when
non-empty; otherwise the client of a hop that carries one and shares the
hop's `traceId`; otherwise unattributed. The resolved value SHALL NOT be
written to the hop, to `hops.jsonl`, or to any recording — it is derived per
read.

A trace whose hops carry two or more different client identities SHALL
resolve to unattributed for every hop in that trace, rather than to any one
of them. A hop with an empty `traceId` SHALL resolve to its own `client`
field alone, joining no trace group.

#### Scenario: A downstream hop inherits the entry hop's client
- **WHEN** an entry hop records `client: app-legacy` and two later hops
  share its `traceId` but carry no `client` of their own
- **THEN** all three hops resolve to `app-legacy`

#### Scenario: A trace with no client anywhere is unattributed
- **WHEN** no hop sharing a `traceId` carries a `client` value
- **THEN** every hop in that trace resolves to unattributed, and to no
  client identity

#### Scenario: A conflicting trace is unattributed, not first-wins
- **WHEN** two hops sharing one `traceId` carry `client: app-legacy` and
  `client: app-next` respectively
- **THEN** every hop in that trace resolves to unattributed

#### Scenario: A hop with no traceId does not join a trace
- **WHEN** a hop has an empty `traceId` and no `client`, and other hops in
  the window do carry a client
- **THEN** that hop resolves to unattributed

#### Scenario: The entry hop is outside the window
- **WHEN** the window contains a hop whose `traceId` matches no
  client-carrying hop in that same window
- **THEN** the hop resolves to unattributed rather than to a client
  resolved from outside the window

### Requirement: Traffic reads accept a client filter
`GET /api/traffic`, `GET /api/traffic/history` and `GET
/api/traffic/stream` SHALL each accept a `client` query parameter and
return only hops whose resolved client (per the resolution requirement
above) equals it. The parameter SHALL compose with every filter each route
already accepts. An absent or empty `client` parameter SHALL filter
nothing, leaving each route's behavior identical to today's.

#### Scenario: Live traffic scoped to one client includes its chain
- **WHEN** `GET /api/traffic?client=app-legacy` is requested and the ring
  holds an `app-legacy` entry hop plus two downstream hops sharing its
  `traceId`, and an unrelated `app-next` entry hop
- **THEN** the response contains the three `app-legacy` hops and not the
  `app-next` hop

#### Scenario: The client filter composes with the existing filters
- **WHEN** `GET /api/traffic?client=app-next&errorsOnly=true` is requested
- **THEN** the response contains only hops that both resolve to `app-next`
  and have `status >= 400` or a transport error

#### Scenario: The stream filters server-side
- **WHEN** a client connects to `GET /api/traffic/stream?client=app-legacy`
  and hops from both clients are recorded afterwards
- **THEN** only hops resolving to `app-legacy` are delivered on that stream

#### Scenario: No client parameter changes nothing
- **WHEN** `GET /api/traffic` is requested with no `client` parameter
- **THEN** the response is identical to the response the same request
  returned before this capability existed

### Requirement: The unattributed bucket is selectable by a sentinel that cannot collide
`client=(none)` SHALL select exactly the hops that resolve to no client.
The sentinel SHALL be a value that `core/proxy.ValidClient` rejects, so no
real client identity can ever collide with it.

#### Scenario: Selecting unattributed hops
- **WHEN** `GET /api/traffic?client=(none)` is requested
- **THEN** the response contains only hops resolving to unattributed, and
  no hop resolving to any client identity

#### Scenario: The fallback bucket is a real client, not the unattributed one
- **WHEN** a request arrives with a malformed client-identity header, so
  the hop records `core/proxy.FallbackClient`, and `GET
  /api/traffic?client=(none)` is requested
- **THEN** that hop is NOT in the response, and `GET
  /api/traffic?client=client` does return it

### Requirement: `ensemble traffic` filters by client
`ensemble traffic` SHALL accept `--client <identity>`, passed through to
the API rather than applied locally, and SHALL honor it in listing mode,
in `--follow` mode, and alongside `--session`, `--since`, `--errors-only`
and `--export`.

#### Scenario: Following one client's traffic
- **WHEN** `ensemble traffic --follow --client app-legacy` runs while both
  clients are exercising the stack
- **THEN** only hops resolving to `app-legacy` are printed, with no
  external filtering step

#### Scenario: Exporting one client's hops within a session
- **WHEN** `ensemble traffic --session <id> --client app-next --json` runs
- **THEN** the output contains only hops that are both in that session and
  resolve to `app-next`

### Requirement: The dashboard exposes client as a filter control and a query field
The Traffic view SHALL offer a client selector listing every client
identity present in the current window plus an explicit unattributed
option, mirroring the existing session selector's ergonomics — including
falling back to "all" when a previously selected client is no longer
present in the window. The query grammar SHALL accept a `client:<value>`
token alongside its existing `session:` token, and both controls SHALL be
reflected in URL state.

#### Scenario: Selecting a client narrows the table to its chain
- **WHEN** the selector is set to `app-legacy`
- **THEN** the table shows that client's entry hops and the downstream hops
  resolving to it, and no hops resolving to another client

#### Scenario: A selected client that ages out falls back to all
- **WHEN** `app-legacy` is selected and every hop resolving to it has aged
  out of the window
- **THEN** the selector falls back to "all clients" rather than showing an
  empty table under a stale selection

#### Scenario: The query grammar accepts a client token
- **WHEN** `client:app-next status:4xx` is typed into the filter box
- **THEN** it parses as two filter pills and the table shows only failing
  hops resolving to `app-next`
