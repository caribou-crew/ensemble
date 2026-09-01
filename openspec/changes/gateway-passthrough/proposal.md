## Why

`passthrough-mode` (shipped) lets one `Service` forward to a real remote
environment instead of a local process. But a `Gateway` fans requests out
to *many* services/stubs by its own route table (`GatewayRoute`
prefix/regex/rewrite), and today there is no way to point a gateway's
entire path space at a real remote edge in one action. Pointing a client
at QA today means either flipping every backing service individually
(tedious, and the local route table's rewrite rules may not even match how
QA's real edge routes its own paths) or bypassing ensemble's gateway
entirely and hand-pointing the client at QA — which loses capture,
redaction, and wire-diff against the local run, the exact machinery
`passthrough-mode` exists to keep in the loop.

The remote edge (a real envoy/gateway in QA) already owns its own routing.
This proposal lets a gateway say "forward everything to that edge, as if
the client had pointed there directly" — one flip per gateway, reusing the
same TLS/safety-rail primitives `passthrough-mode` already built.

## What Changes

- `Gateway` gains `Upstreams []GatewayUpstream`: tagged remote targets
  declared inline on the gateway (`name` — the flip tag, e.g. `qa`;
  `url`; `allow_writes`; `client_cert_file`/`client_key_env`/
  `client_key_passphrase_env` for mTLS) — the same shape and safety rails
  `Service`'s passthrough fields already have, reused rather than
  reinvented. Any number of upstreams per gateway; no shared/global
  registry (each gateway's list is its own).
- A gateway now has an implicit `"local"` mode (today's route-matching
  behavior, unchanged) plus one mode per declared upstream `name`.
  Flipping to an upstream makes the gateway forward its entire path space
  verbatim to that upstream — no `GatewayRoute` matching/rewrite, no
  local CORS injection/preflight answering, no fault/latency injection —
  functionally identical to the client pointing at that host directly.
  Flipping back to `"local"` restores today's behavior exactly.
- New orchestrator entry point, `FlipGateway(ctx, name, target string)`,
  deliberately separate from `Flip`/`FlipTo` (which model a *service's*
  placement — a concept gateways don't have) but reusing the low-level
  `core/proxy.Target` primitives `passthrough-mode` already built
  (per-target TLS transport, the read-only-by-default safety rail,
  fault/latency-skip) — see design.md's Decisions for why `core/proxy`
  itself needs no changes at all.
- `POST /api/gateways/{name}/flip {"target": "..."}`, mirroring
  `handleServiceFlip`'s shape.
- Dashboard/TUI: gateways (already listed in the Services view as their
  own "kind") gain a flip control next to the existing service placement
  badges, offering `local` plus every declared upstream name.
- `retrace/runs.Stack.Passthrough` gains gateway entries when a run passed
  through a passthrough-flipped gateway, extending the existing
  reduced-scope capture-honesty mechanism (today service-only) to
  gateways.
- State is runtime-only, matching `Flip` today: resets on `ensemble up`,
  no config-file mutation endpoint.

## Capabilities

### New Capabilities
- `gateway-passthrough-mode`: a `Gateway` can forward its entire path
  space to a real remote edge instead of routing locally, with mTLS, the
  same read-only-by-default safety rail as service-level passthrough, and
  reduced-scope capture honesty — REST and dashboard/TUI surfaces, no new
  CLI verb (service-level passthrough has none either).

### Modified Capabilities
- `passthrough-mode`: its TLS-transport/safety-rail/fault-skip primitives
  in `core/proxy.Target` gain a second caller (gateways, via their single
  listener's `Target`) alongside services — no shape changes to those
  primitives themselves.
- `ensemble-gateway`: `Gateway` config schema gains `Upstreams`.

## Impact

- `ensemble/config/config.go`: `Gateway` gains `Upstreams
  []GatewayUpstream`.
- `ensemble/config/gateway_upstream.go` (new, mirroring
  `passthrough.go`'s `validatePassthrough`/`ClientCert` shape):
  `GatewayUpstream` type, `validateGatewayUpstreams` (unique names per
  gateway, valid http(s) URL, cert/key pairing, cert resolved and cached
  at load time), `GatewayUpstreamClientCert(gateway, upstream string)`.
  Kept a sibling of `passthrough.go` rather than merged into it — a
  `GatewayUpstream` has no `Proxy`/local-placement fields to cross-check
  against, and a gateway/service name collision must not alias their
  cached certs (see design.md).
- `ensemble/orchestrator/orchestrator.go`: `wireOneGateway` gains a
  `target string` parameter (`"local"` today's only caller value); new
  `gatewayActive map[string]string` tracks each gateway's current target
  alongside the existing `gatewayStop`. New `FlipGateway` tears down and
  re-`ServeStoppable`s a gateway's listener with either the routed
  `proxy.Target` (local) or a single-upstream passthrough `proxy.Target`
  (an upstream tag), reusing the same `retryOnBindConflict` rebind
  handling `wireProxy` already relies on for services.
- `ensemble/server/routes.go`/`openapi.go`: new
  `POST /api/gateways/{name}/flip` route and handler; gateway
  representation in the status/topology response gains its active target
  + declared upstream names (mirroring how `TopologyNode.Placements` was
  added for services).
- `dashboard/ensemble-ui/src/views/ServicesView.tsx` (or wherever gateways
  render as a "kind" today) and `ensemble/tui/services.go`: flip control
  for a gateway's declared upstreams, reusing/generalizing the
  `FlipControl` built for services where practical.
- `retrace/runs/stack.go`, `retrace/cmd/retrace/client.go`: extend
  `Stack.Passthrough` population to include passthrough-flipped gateways
  from the status response, alongside services.
- No changes needed to `core/proxy/proxy.go` — `Target` already
  generalizes cleanly across "routes" and "single upstream" modes and
  already gates the safety rail/fault-skip/TLS generically off
  `Passthrough`/`AllowWrites`/`TLS`, independent of which mode is active.

### Explicitly out of scope
- **Environment-level grouping** (a named environment that several
  gateways reference and flip together in one action) — a natural
  follow-up once gateway-level flipping exists and has real usage, not
  built now.
- **A CLI verb** for gateway flip — service-level flip has none either
  (REST + TUI/dashboard only); no reason for gateways to be first.
- **A shared/global upstream registry across gateways** — each gateway's
  `Upstreams` list is its own; no cross-gateway reuse of a named upstream
  in this change.
