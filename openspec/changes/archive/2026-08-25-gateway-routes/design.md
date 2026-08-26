# Design: gateway-routes

## Context

`core/proxy.Target` is `{Name, Listen, Upstream}`: one listener forwarding
every request to one base URL. `orchestrator.wireProxy` binds one such target
per service that sets `proxy:`. Stubs (`core/stub`) bind their own port and
record their own hops. There is no component that picks an upstream per
request.

The sample stack demonstrates the missing piece by hand:
`sample/services/edge-gw` is a `net/http/httputil` reverse proxy routing
`/products*` → catalog's proxy port and `/cart/*` → storefront's proxy port.
Because it forwards to *proxy* ports, the capture model already handles the
shape: client → edge-gw is one hop (recorded by edge-gw's own intercept
listener), edge-gw → catalog is a second (recorded by catalog's). This design
makes that shape a config block instead of a Go program.

Constraints:
- The proxy handler owns trace propagation (`ParseCtx` → `Child` →
  `ClaimSpan`), hop capture, body capping, and latency injection. A gateway
  must get all of that identically or its hops lie.
- `Validate` already has one shared `usedPorts` space across services,
  databases, and stubs; gateways must join it.
- Sessions (`proxy.SessionManager`) need an entry-node set for
  propagation-gap detection and an upstream for the ephemeral client-edge
  listener; both are keyed by service name today.

## Goals / Non-Goals

**Goals:**
- `gateways:` in `ensemble.yaml`, one listener per gateway, routes by path
  prefix onto ensemble's resolved ports (service proxy port, else real port;
  stub port).
- A gateway is a first-class hop node: captured, latency-targetable, visible
  in topology, usable as a session entry.
- Zero change in behaviour for configs without `gateways:`.

**Non-Goals:**
- Host/header/method-based routing, regex paths, rewrites beyond
  `strip_prefix`, TLS, CORS, auth. The sample's `edge-gw` keeps demonstrating
  those "real edge" concerns.
- Orchestrator supervision of gateways (no start/stop/restart/flip, no
  `ensemble status` row). A gateway is a static listener like a stub.
- Dashboard layout/renderer changes. The dashboard only gains a `gateway`
  entry in its category palette (legend label + accent colour) so the node
  renders as a first-class cluster rather than falling into "Ungrouped".

## Decisions

### D1. Gateway is a `proxy.Target` with `Routes`, not a new component

`Target` gains:

```go
type Route struct {
    Prefix      string // "/"-rooted, no trailing slash (normalised)
    Upstream    string // base URL, e.g. "http://127.0.0.1:9081"
    StripPrefix bool
}
// Routes, when non-empty, selects Upstream per request by longest
// segment-aware prefix match; Target.Upstream is then unused.
Routes []Route
```

The handler calls `t.resolve(r.URL.Path)` → `(upstream, forwardPath, ok)`.
When `!ok` it records a hop with `Status: 404, Err: "no route for <path>"`
and answers 404. Everything else — trace context, `ClaimSpan`, capture,
latency (`DelayFor(t.Name, r.URL.Path)` on the *incoming* path) — is the
existing code path.

*Why over a separate `gateway` package:* a second handler would duplicate
~100 lines of propagation/capture logic that must stay byte-for-byte
consistent, and every future proxy fix would need applying twice. The
resolve step is ~30 lines.

*Alternative considered:* a gateway that forwards directly to real ports and
records a single hop `To: <service>`. Rejected (confirmed with the author):
it hides the gateway from the topology and bypasses per-service latency
rules.

### D2. Prefix matching semantics

- Normalise at validation: must start with `/`; a trailing `/` is dropped
  except for the bare `/` catch-all.
- Match on `r.URL.Path`: prefix `/` matches everything; otherwise
  `path == prefix || strings.HasPrefix(path, prefix+"/")`.
- Longest matching prefix wins. Duplicate prefixes within a gateway are a
  validation error, so there are no ties.
- `strip_prefix: true` forwards `path[len(prefix):]` (empty → `/`), query
  string preserved. The gateway's own hop records the original
  `RequestURI`; the downstream hop naturally records the stripped one.

### D3. Target port resolution lives in `config`

`func (c *Config) RoutablePort(name string) (port int, kind string, ok bool)`
returns a service's proxy port if > 0, else its real port, or a stub's port;
`kind` is `"service"`/`"stub"`. `Validate` uses it to reject a route whose
target has no resolvable port (e.g. a docker service with `port: 0`);
the orchestrator uses it to build `Route.Upstream`. One place decides "what
does `service: x` mean".

### D4. Wiring happens in `Orchestrator.Up`, before services start

`wireGateways()` runs first in `Up`. A port that can't bind fails `Up` before
any process is spawned (cheapest failure), and since every upstream is a
static `127.0.0.1:<port>`, ordering relative to service readiness doesn't
matter — a request before the upstream is up gets the same 502 hop a
service proxy produces. Like `wireProxy`, it runs once per `Up`; `Restart`
and `Flip` never touch it.

Gateway names are not `ServiceState`s: `States()`/`ensemble status` stay
process-only, matching how stubs are treated.

### D5. Namespace and validation

Gateway names share the node namespace (`hasServiceOrDatabase` grows into a
node lookup that also sees stubs and gateways): a gateway named like a
service would produce ambiguous hops and latency targets. Checks, all joined
via `errors.Join` as today:

- `port` > 0 and registered in `usedPorts` as `"gateway <name>"`.
- at least one route; each route: prefix non-empty and `/`-rooted; `service`
  resolves via `RoutablePort`; no duplicate normalised prefix.

### D6. Surfaces

- Topology: `TopologyNode{Category: "gateway", Status: "static", Entry: true}`
  plus an edge gateway → each distinct route target. Edges are config-derived
  like `depends_on` edges, not inferred from env.
- Sessions: `handleSessionStart` resolves `entry` as a service with a proxy
  port **or** a gateway; upstream is `http://127.0.0.1:<port>`. `cmd_up`
  adds gateway names to the `entries` slice so unstamped traffic arriving at
  a gateway is not flagged as a propagation gap.
- Latency: nothing to do — `LatencyStore` keys rules by target name string.

## Risks / Trade-offs

- [Two hops per routed request doubles hop volume for gateway traffic] →
  Accepted; it is the same cost the hand-written edge-gw already pays and
  is what makes the gateway → service edge visible.
- [A route targeting a service with no `proxy:` silently skips capture of
  the second hop] → The gateway hop still exists; `Validate` could warn but
  ensemble has no warning channel today, so this is documented in the README
  instead.
- [`strip_prefix` changes the path the downstream service sees, which a
  latency rule on that service keyed by path may not expect] → Documented;
  rules on the gateway itself see the original path.
- [Prefix `/` catch-all combined with a stub target can swallow typos
  silently] → Longest-prefix semantics mean explicit routes always win; the
  404 hop for unmatched paths only disappears when the user opts into a
  catch-all.

## Migration Plan

Purely additive. Existing configs parse unchanged; `gateways:` absent means
no listeners, no topology nodes, no validation. Rollback is removing the
block.

## Open Questions

None blocking. Possible round-two: `methods:` on a route, and a
`host:` matcher for multi-tenant edges.
