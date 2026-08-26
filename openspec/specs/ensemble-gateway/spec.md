# ensemble-gateway

## Purpose
TBD

## Requirements

### Requirement: Config-defined gateway listeners
Ensemble SHALL accept a top-level `gateways:` map in `ensemble.yaml`. Each
entry SHALL declare a `port` and a non-empty `routes` list of `{prefix,
service, strip_prefix?}`; `service` SHALL name a configured service or stub.
`ensemble up` SHALL bind one intercept listener on `127.0.0.1:<port>` per
gateway inside the same process as service proxies.

#### Scenario: One public port, three targets
- **WHEN** `gateways.public` declares `port: 9000` with routes for `/products`
  → `catalog`, `/cart` → `storefront`, `/pay` → `payments` (a stub)
- **THEN** `ensemble up` listens on `127.0.0.1:9000` and forwards each prefix
  to its target

#### Scenario: Absent block is a no-op
- **WHEN** `ensemble.yaml` has no `gateways:` key
- **THEN** no gateway listener is bound, no topology node is added, and
  validation output is unchanged from before this capability existed

### Requirement: Longest segment-aware prefix routing
A gateway SHALL route each request by the longest configured prefix that
matches `r.URL.Path`, where prefix `/` matches every path and any other
prefix `p` matches exactly `p` or any path beginning with `p + "/"`.

#### Scenario: Longest prefix wins
- **WHEN** routes `/api` → `a` and `/api/orders` → `b` exist and a request
  arrives for `/api/orders/7`
- **THEN** the request is forwarded to `b`

#### Scenario: Segment boundary respected
- **WHEN** route `/cart` → `storefront` exists and a request arrives for
  `/cartoon`
- **THEN** the request does not match `/cart`

#### Scenario: No route
- **WHEN** a request matches no configured prefix and no `/` catch-all exists
- **THEN** the gateway answers `404`, and a hop is recorded with
  `To: <gateway>`, `Status: 404`, and a non-empty `Err`

### Requirement: Prefix stripping
A route with `strip_prefix: true` SHALL forward the path with the matched
prefix removed (an empty remainder becomes `/`), preserving the query string.
A route without it SHALL forward the path unchanged.

#### Scenario: Strip with query
- **WHEN** route `/cart` → `storefront` has `strip_prefix: true` and a request
  arrives for `/cart/items?limit=5`
- **THEN** `storefront` receives `/items?limit=5`

#### Scenario: Strip to root
- **WHEN** the same route receives a request for `/cart`
- **THEN** `storefront` receives `/`

### Requirement: Routes resolve onto ensemble's own ports
A route target SHALL resolve to the service's `proxy` port when it is set,
otherwise to the service's `port`; a stub target SHALL resolve to the stub's
`port`.

#### Scenario: Proxied service yields two captured hops
- **WHEN** `catalog` has `proxy: 9081` and a request passes through gateway
  `public` to `/products`
- **THEN** two hops are recorded: `From: client-side, To: public` and
  `From: public, To: catalog`, the second captured by catalog's own intercept
  listener

#### Scenario: Unproxied service
- **WHEN** `legacy` has `port: 8090` and no `proxy`, and a route targets it
- **THEN** the gateway forwards to `127.0.0.1:8090` and only the gateway hop
  is recorded

### Requirement: Gateway is a first-class hop node
A gateway SHALL behave as an intercept target: it SHALL record a hop for every
request with `To: <gateway name>`, advance the trace context so the
downstream hop's `From` is the gateway name, and SHALL honour latency rules
whose target is the gateway name, evaluated against the incoming path.

#### Scenario: Latency on the gateway
- **WHEN** `ensemble latency set --target public --path /products --fixed 300 --enabled`
- **THEN** requests for `/products` through `public` are delayed ~300ms and the
  gateway hop records `InjectedDelayMs` ≈ 300

### Requirement: Validation
`Validate` SHALL reject, reporting all violations together: a gateway `port`
of 0; a gateway `port` colliding with any service, proxy, database, or stub
port; a gateway name equal to a service, database, or stub name; an empty
`routes` list; a route whose `prefix` is empty or does not start with `/`; a
route whose `service` is unknown or has no resolvable port; and two routes in
one gateway with the same normalised prefix (trailing `/` ignored).

#### Scenario: Unknown target
- **WHEN** a route declares `service: nope` and no service or stub is named
  `nope`
- **THEN** `Load` fails naming the gateway, the route index, and `nope`

#### Scenario: Port collision
- **WHEN** gateway `public` declares `port: 9081` and service `catalog`
  declares `proxy: 9081`
- **THEN** `Load` fails with a duplicate-port error naming both

#### Scenario: Duplicate prefix
- **WHEN** one gateway declares routes for `/cart` and `/cart/`
- **THEN** `Load` fails naming the duplicate prefix

### Requirement: Topology surface
`GET /api/topology` SHALL include each gateway as a node with
`category: "gateway"`, `status: "static"`, and `entry: true`, and SHALL
include one edge from the gateway to each distinct route target.

#### Scenario: Gateway node and edges
- **WHEN** gateway `public` routes to `catalog` twice and `payments` once
- **THEN** the topology contains node `public` (gateway, entry) and exactly
  the edges `public → catalog` and `public → payments`

### Requirement: Gateway as session entry
`POST /api/sessions` SHALL accept a gateway name as `entry`, fronting the
gateway's port with the session's client-edge listener, and gateway names
SHALL count as entry nodes for propagation-gap detection.

#### Scenario: Session through a gateway
- **WHEN** a session starts with `entry: "public"` and the client calls the
  returned `edgeAddr`
- **THEN** the gateway hop and every downstream hop carry the session's
  `retrace-run` baggage and are partitioned to that session

#### Scenario: Unstamped gateway traffic is not a gap
- **WHEN** a session is active and ambient traffic with no trace context
  arrives at a gateway
- **THEN** the session's verdict is not degraded for that hop
