import { describe, expect, it } from "vitest";
import { collapseGatewayHops } from "./gatewayCollapse";
import type { Hop } from "../api/types";

function hop(partial: Partial<Hop> & Pick<Hop, "seq" | "to">): Hop {
  return {
    schema: "hop.v1",
    traceId: "t1",
    t: { start: "2026-08-22T00:00:00.000Z" },
    ...partial,
  };
}

describe("collapseGatewayHops", () => {
  it("returns hops unchanged when nothing is collapsed", () => {
    const hops = [hop({ seq: 1, to: "public", spanId: "s1" })];
    expect(collapseGatewayHops(hops, new Set())).toBe(hops);
  });

  it("collapses client -> gateway -> target into client -> target", () => {
    const outer = hop({ seq: 1, from: undefined, to: "public", spanId: "s1", status: 200 });
    const inner = hop({ seq: 2, from: "public", to: "storefront", spanId: "s2", parentSpanId: "s1" });
    const out = collapseGatewayHops([outer, inner], new Set(["public"]));

    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ seq: 1, from: undefined, to: "storefront", status: 200 });
  });

  it("collapses a chain of two gateways in one pass", () => {
    const a = hop({ seq: 1, to: "gwA", spanId: "s1" });
    const b = hop({ seq: 2, from: "gwA", to: "gwB", spanId: "s2", parentSpanId: "s1" });
    const c = hop({ seq: 3, from: "gwB", to: "storefront", spanId: "s3", parentSpanId: "s2" });
    const out = collapseGatewayHops([a, b, c], new Set(["gwA", "gwB"]));

    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ seq: 1, to: "storefront" });
  });

  it("leaves an opted-in gateway visible while collapsing another in the same chain", () => {
    const a = hop({ seq: 1, to: "gwA", spanId: "s1" });
    const b = hop({ seq: 2, from: "gwA", to: "gwB", spanId: "s2", parentSpanId: "s1" });
    const c = hop({ seq: 3, from: "gwB", to: "storefront", spanId: "s3", parentSpanId: "s2" });
    // Only gwA is collapsed; gwB opted in (exposeInTraffic: true) and stays a visible hop.
    const out = collapseGatewayHops([a, b, c], new Set(["gwA"]));

    expect(out).toHaveLength(2);
    expect(out[0]).toMatchObject({ seq: 1, to: "gwB" });
    expect(out[1]).toMatchObject({ seq: 3, from: "gwB", to: "storefront" });
  });

  it("falls back to showing the gateway hop when its target hop isn't in the current window", () => {
    const outer = hop({ seq: 1, to: "public", spanId: "s1" });
    const out = collapseGatewayHops([outer], new Set(["public"]));

    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ seq: 1, to: "public" });
  });

  it("does not disturb an unrelated concurrent trace through the same gateway", () => {
    const outer1 = hop({ seq: 1, traceId: "t1", to: "public", spanId: "s1" });
    const inner1 = hop({ seq: 2, traceId: "t1", from: "public", to: "storefront", spanId: "s2", parentSpanId: "s1" });
    const outer2 = hop({ seq: 3, traceId: "t2", to: "public", spanId: "s3" });
    const inner2 = hop({ seq: 4, traceId: "t2", from: "public", to: "catalog", spanId: "s4", parentSpanId: "s3" });
    const out = collapseGatewayHops([outer1, inner1, outer2, inner2], new Set(["public"]));

    expect(out).toHaveLength(2);
    expect(out[0]).toMatchObject({ seq: 1, to: "storefront" });
    expect(out[1]).toMatchObject({ seq: 3, to: "catalog" });
  });
});
