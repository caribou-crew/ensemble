# Proposal: gateway-routes

Status: proposed (2026-08-21).

## Why

A service's `proxy:` field is a one-in, one-out intercept port: one listener,
one fixed upstream. Real stacks sit behind edge routers that fan a single
public port out to many services by path prefix, and ensemble has no built-in
way to express that — today the only option is to run your own reverse proxy
as a service (the sample stack's `sample/services/edge-gw` exists purely to
fill this role). A `gateways:` block that maps path prefixes onto ensemble's
own resolved intercept ports closes the gap without anyone writing router
code, and keeps the client → gateway → service chain fully captured.

## What Changes

- New top-level `gateways:` map in `ensemble.yaml`. Each gateway declares the
  `port` clients call and an ordered list of `routes`, each `{prefix,
  service, strip_prefix?}`. `service` may name a service **or a stub**.
- A gateway is an intercept listener like any other: every request through it
  is a captured hop (`To: <gateway>`), latency rules target it by name, and
  the forwarded request carries advanced trace context so the next hop's
  `From` is the gateway.
- Routing is longest-prefix, segment-aware (`/cart` matches `/cart` and
  `/cart/x`, never `/cartoon`). A request matching no route is answered 404 and
  still recorded as a hop with an `Err`.
- A route forwards to the target's **proxy port** when it has one (so the
  gateway → service hop is captured and per-service latency still applies),
  otherwise to its real `port`; a stub target forwards to `stub.Port`.
- Validation: gateway names share the node namespace with services,
  databases, and stubs; `port` joins the shared `usedPorts` collision check;
  every route needs a non-empty `/`-rooted prefix, a known target with a
  resolvable port, and no duplicate prefix within a gateway.
- Gateways appear in `GET /api/topology` as `category: "gateway"` nodes
  (implicitly `entry`) with an edge to each routed target, and are accepted as
  the `entry` of a recording session (`POST /api/sessions`) and counted as an
  entry for propagation-gap detection.
- README documents the block. No breaking changes: configs without
  `gateways:` behave exactly as before.

## Capabilities

### New Capabilities
- `ensemble-gateway`: path-prefix routing gateway listeners defined in
  config, resolving onto ensemble's own intercept/stub ports, captured as hops
  and surfaced in topology and sessions.

### Modified Capabilities
<!-- No main specs exist yet under openspec/specs/; the session-entry and
     topology behaviour this touches is captured as ADDED requirements in the
     new capability rather than as deltas against a spec that has not been
     synced. -->

## Impact

- `core/proxy`: `Target` gains optional `Routes`; the handler resolves the
  upstream per request when routes are present. Existing single-upstream
  targets are untouched.
- `ensemble/config`: `Gateway`/`GatewayRoute` types, validation, and a shared
  "resolve this node's routable port" helper.
- `ensemble/orchestrator`: wires gateway listeners during `Up`.
- `ensemble/server`: topology nodes/edges for gateways; session entry accepts
  a gateway.
- `ensemble/cmd/ensemble`: gateway names included in the session manager's
  entry set.
- `dashboard/ensemble-ui`: `gateway` added to the topology category union
  and palette (legend + accent); no renderer changes.
- Docs: README config reference; `templates/company-stack/ensemble.yaml`
  carries a worked `gateways:` block.
