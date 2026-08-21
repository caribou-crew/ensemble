import { describe, expect, it } from "vitest";
import { CATEGORIES, categoryOf, normalizeTopology } from "./categories";
import type { Topology, TopologyNode } from "../api/types";

function node(
  name: string,
  category: TopologyNode["category"],
  overrides: Partial<TopologyNode> = {},
): TopologyNode {
  return { name, category, status: "healthy", ...overrides };
}

describe("categoryOf", () => {
  it("passes through the server-supplied category for a gateway node", () => {
    expect(categoryOf(node("public", "gateway", { entry: true }))).toBe(
      "gateway",
    );
  });

  it("passes through the server-supplied category for a service node", () => {
    expect(categoryOf(node("orders", "service"))).toBe("service");
  });

  it("passes through the server-supplied category for a database node", () => {
    expect(categoryOf(node("orders-db", "database"))).toBe("database");
  });

  it("passes through the server-supplied category for a stub node", () => {
    expect(categoryOf(node("email-stub", "stub"))).toBe("stub");
  });

  it('falls to "other" for a category string the palette does not recognize', () => {
    const weird = node("mystery", "service") as TopologyNode;
    // Cast past the literal union: this simulates a future server sending a category the
    // dashboard hasn't shipped support for yet, which is exactly the case 'other' exists for.
    (weird as { category: string }).category = "queue";
    expect(categoryOf(weird)).toBe("other");
  });
});

describe("CATEGORIES", () => {
  it("has an entry for every id categoryOf can return", () => {
    const ids = new Set(CATEGORIES.map((c) => c.id));
    expect(ids.has("gateway")).toBe(true);
    expect(ids.has("service")).toBe(true);
    expect(ids.has("database")).toBe(true);
    expect(ids.has("stub")).toBe(true);
    expect(ids.has("other")).toBe(true);
  });
});

describe("normalizeTopology", () => {
  const topology: Topology = {
    nodes: [node("orders", "service"), node("orders-db", "database")],
    edges: [
      { from: "orders", to: "orders-db" },
      // stale: 'inventory' isn't in this snapshot's node list.
      { from: "orders", to: "inventory" },
      { from: "ghost-service", to: "orders" },
    ],
  };

  it("drops edges that reference a node missing from this snapshot", () => {
    const t = normalizeTopology(topology);
    expect(t.edges).toEqual([{ from: "orders", to: "orders-db" }]);
  });

  it("leaves the node list untouched", () => {
    const t = normalizeTopology(topology);
    expect(t.nodes).toBe(topology.nodes);
  });

  it("is a no-op when every edge already resolves", () => {
    const clean: Topology = {
      nodes: [node("orders", "service"), node("orders-db", "database")],
      edges: [{ from: "orders", to: "orders-db" }],
    };
    expect(normalizeTopology(clean)).toEqual(clean);
  });
});
