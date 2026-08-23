// Collapses "client -> gateway -> target" hop chains down to "client -> target" for gateways
// the caller wants hidden, so the Traffic tab reads as the logical call rather than the router
// hop in front of it. A gateway is identified purely by hop.to matching a name in `collapse` —
// callers build that set from GET /api/topology's gateway nodes (config.Gateway.ExposeInTraffic,
// see ensemble/config/config.go, and the Traffic tab's own show/hide toggle).
import type { Hop } from "../api/types";

export function collapseGatewayHops(hops: Hop[], collapse: Set<string>): Hop[] {
  if (collapse.size === 0) return hops;

  const bySpan = new Map<string, Hop>();
  for (const h of hops) {
    if (h.spanId) bySpan.set(h.spanId, h);
  }

  // The first hop recorded against a given parent span wins — a gateway route forwards to
  // exactly one target, so ties are not expected in practice.
  const childOf = new Map<string, Hop>();
  for (const h of hops) {
    if (h.parentSpanId && !childOf.has(h.parentSpanId)) childOf.set(h.parentSpanId, h);
  }

  // A hop is absorbed (dropped from output) iff its immediate parent hop routed through a
  // gateway being collapsed — true for every intermediate hop in a chained-gateway trace, not
  // just the last one, since the outer hop's own recorded status/timing already mirrors the
  // full round-trip (a gateway relays the downstream response verbatim).
  const dropped = new Set<Hop>();
  for (const h of hops) {
    const parent = h.parentSpanId ? bySpan.get(h.parentSpanId) : undefined;
    if (parent && collapse.has(parent.to)) dropped.add(h);
  }

  const out: Hop[] = [];
  for (const h of hops) {
    if (dropped.has(h)) continue;
    if (!collapse.has(h.to)) {
      out.push(h);
      continue;
    }
    // Walk the child chain to the first non-gateway (or unresolved) hop, so a gateway ->
    // gateway -> target chain collapses fully in one pass, not one hop per level.
    let to = h.to;
    let spanId = h.spanId;
    while (collapse.has(to) && spanId) {
      const child = childOf.get(spanId);
      // No recorded child yet (in-flight, evicted from the ring, or the gateway never
      // reached a target) — nothing to collapse into, so leave `to` pointing at the gateway.
      if (!child) break;
      to = child.to;
      spanId = child.spanId;
    }
    out.push(to === h.to ? h : { ...h, to });
  }
  return out;
}
