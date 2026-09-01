## Context

`Gateway` (`ensemble/config/config.go:697`) is a config-defined edge
router: one intercept port fanning requests out to services/stubs by
`GatewayRoute` prefix/regex match, wired at `ensemble up` by
`wireGateways`/`wireOneGateway` (`ensemble/orchestrator/orchestrator.go:
1339`) into a single `proxy.Target` per gateway, bound via
`o.px.ServeStoppable`. `core/proxy.Target` (`core/proxy/proxy.go:20`)
already has two forwarding modes on the *same* struct: `Routes []Route`
(gateway mode — upstream picked per request by longest-prefix match) or
plain `Upstream string` (single-target mode, used by a passthrough
`Service` today). The request handler picks between them generically
(`len(t.Routes) > 0` at `proxy.go:582`), and three things already key off
`Target.Passthrough`/`Target.AllowWrites`/`Target.TLS` regardless of which
mode is active: the read-only-by-default safety rail (`proxy.go:547`),
fault/latency-skip (`proxy.go:561`), and the per-target mTLS transport.
`passthrough-mode` built all of this for services; this proposal is
mostly about *reaching* that existing machinery from a gateway's listener,
not building new forwarding logic.

`ensemble/orchestrator`'s `wireProxy` (services) already proves out the
re-wire pattern this needs: tear down a live listener, rebind with
`retryOnBindConflict` to absorb the brief OS-level close→rebind window,
track the resolved upstream (`o.wiredUpstream`) so a no-op re-wire is
skipped. Gateways currently have no equivalent re-wire path — `wireGateways`
only ever runs once, at `Up`/`Reconcile`, with the routed `proxy.Target`.

## Goals / Non-Goals

**Goals:**
- A gateway can be flipped, at runtime, between its normal routed
  behavior and forwarding its entire path space verbatim to any one of
  its declared upstreams — functionally identical to a client pointing at
  that host directly (no local CORS, no route rewrite, no fault
  injection).
- Same safety posture as service-level passthrough: read-only by default,
  mTLS support, fault/latency injection skipped, reduced-scope capture
  honesty in `retrace`.
- Runtime-only state (matches `Flip` today) — no config-file mutation.

**Non-Goals:**
- Environment-level grouping (flip N gateways together) — explicitly
  deferred; this change only makes gateway-level flipping possible, which
  a grouping feature would build on.
- A CLI verb — consistent with service-level flip having none.
- A cross-gateway shared upstream registry — each gateway's `Upstreams`
  list is its own, per the approved design.
- Changing `core/proxy.Target`'s shape or behavior — it already
  generalizes across both modes; see Decisions.

## Decisions

**No changes to `core/proxy` at all.** `Target` already forwards in
either "routes" or "single upstream" mode, and already gates the safety
rail/fault-skip/TLS generically off `Passthrough`/`AllowWrites`/`TLS`
regardless of mode (`proxy.go:547,561`, TLS transport resolution).
Flipping a gateway to an upstream is just building a different
`proxy.Target` for the same listener — `Routes: nil, Upstream: <url>,
Passthrough: true, AllowWrites: <from GatewayUpstream>, TLS: <resolved
cert, if any>, CORS: nil` — and re-`ServeStoppable`-ing it. Alternative
considered: give `Target` an explicit `Mode` enum. Rejected — the
existing `len(Routes) > 0` check already is that enum in practice, and
introducing a second one would just be a rename with no new behavior.

**Full CORS bypass in passthrough mode, not "keep local CORS, skip only
routing."** A passthrough-flipped gateway's `Target.CORS` is `nil` for
the duration it's flipped, so no preflight is answered locally and no
`Access-Control-*` header is injected — the browser sees exactly what the
remote edge sends. This was the explicit design ask ("behave as if I had
set the client to point to the upstream host directly") over the
alternative of keeping local CORS handling for dev-flow consistency; the
tradeoff (a flow that depends on ensemble's local CORS answering could
break in passthrough mode if the real edge doesn't allowlist the dev
origin) is accepted as correct behavior, not a bug — see Risks.

**`GatewayUpstream` is a sibling type in a new `gateway_upstream.go`, not
a merge into `passthrough.go`'s `Service`-shaped validation.** A
`GatewayUpstream` has no `Proxy`/local-placement fields to cross-check
against (unlike `Service.Upstream`, which requires `Proxy > 0` since a
flippable service needs a listen port). Mirroring `passthrough.go`'s
structure (same field names/semantics: `URL`, `AllowWrites`,
`ClientCertFile`, `ClientKeyEnv`, `ClientKeyPassphraseEnv`) rather than
abstracting a shared helper matches how this codebase already handles two
similar-but-not-identical config shapes (e.g. `latency_profiles.go`
mirroring `readiness.go` byte-for-byte rather than factoring a generic
loader).

**`GatewayUpstream` has no separate `Passthrough` label field.**
`Service.Passthrough` is a free-text environment label distinct from
nothing else on `Service` (a service only ever has one `Upstream`).
`GatewayUpstream.Name` already serves double duty as both the flip target
identifier *and* the label — no reason to carry a second string that
would always just repeat it.

**Gateway upstream client certs are cached in a separate map, keyed by
`(gateway, upstream name)`, not merged into `Config.clientCerts` (keyed by
service name today).** Nothing in config validation currently prevents a
gateway and a service from sharing a name — they're different top-level
maps (`Gateways`, `Services`). Reusing the same `map[string]tls.Certificate`
keyed by bare name risks a gateway's cert silently aliasing (or being
aliased by) a same-named service's cert. `GatewayUpstreamClientCert(gateway,
upstream string)` reads from its own `map[string]map[string]tls.Certificate`
instead.

**Per-gateway flip lock reuses `lockService`'s map with a namespaced key
(`"gateway:" + name`), not a second lock map.** Same reasoning as the cert
cache: gateway and service names aren't guaranteed disjoint, and
`FlipGateway`/`Flip`/`FlipTo`/`Restart`/`Down` never need to serialize
against each other across the two namespaces, only within one — a prefix
is enough to prevent an accidental collision without standing up a
parallel locking mechanism for one more entity type.

**`wireOneGateway` gains a `target string` parameter** (`"local"` is what
every existing caller — `wireGateways`, `Reconcile` — passes); `FlipGateway`
is the only caller that ever passes an upstream name. This keeps the
existing wiring path's shape (build `proxy.Target`, `ServeStoppable`,
record `gatewayStop`) as the single source of truth for *how* a gateway
gets bound, whether at startup or via a runtime flip, rather than
duplicating that logic in `FlipGateway`.

## Risks / Trade-offs

- **[Trade-off] A dev flow relying on the gateway's local CORS answering
  breaks when that gateway is flipped to passthrough, if the remote edge
  doesn't allowlist the calling origin.** Accepted — this is the
  requested behavior ("as if the client pointed at the upstream
  directly"), and the alternative (partial local CORS injection on top of
  a real remote response) would make captured/diffed traffic dishonest
  about what the remote edge actually does.
- **[Risk] Flipping a gateway with active traffic drops in-flight
  requests during the brief stop→rebind window.** Mitigation: reuse
  `retryOnBindConflict`, the same mechanism `wireProxy` already relies on
  for services; this is an accepted, already-shipped trade-off for
  service-level flip today, not a new exposure.
- **[Risk] A gateway meant for local stub/dev traffic gets flipped to a
  remote upstream by mistake, and writes silently reach a real QA
  backend.** Mitigation: read-only-by-default is inherited unchanged —
  any non-GET/HEAD is refused unless that upstream sets `allow_writes:
  true`; the dashboard/TUI flip control makes the active target visible,
  not a hidden toggle.
- **[Risk] Gateway/service name collision aliasing cached certs or
  locks.** Mitigation: namespaced maps/keys, see Decisions.

## Open Questions

- Exact JSON shape for exposing a gateway's active target + declared
  upstream names on the status/topology endpoint — left to
  implementation to mirror `TopologyNode.Placements`'s existing pattern
  rather than speculatively designed here.
- Whether the dashboard's `FlipControl` (built for services, 2-vs-3+
  placement button/select) can be reused as-is for gateways or needs a
  small generalization (a gateway always has `"local"` plus N upstreams,
  never just 1) — an implementation detail, not a design fork.
