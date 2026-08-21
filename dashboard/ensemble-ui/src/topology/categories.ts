import type { Topology, TopologyNode } from "../api/types";
import type { CategoryId } from "./types";

export interface CategoryDef {
  id: CategoryId;
  label: string;
  /** CSS custom property holding this category's accent colour. */
  colorVar: string;
}

// Render order is legend order is cluster-stacking tiebreak order — one list, so they
// can't disagree.
//
// colorVar reuses tokens.css's existing --topo-cat-* palette (ported verbatim from
// local-stack-console — see tokens.css's comment on the colorblind validation), just
// remapped from the old six fintech planes onto ensemble's three real categories. 'service'
// takes the edge accent (ensemble's services are the caller side of nearly every hop, the
// same role 'edge' played in the old graph) and 'database' takes the infra accent (a
// datastore reads as shared infrastructure, not a distinct plane). 'gateway' — a config-
// defined path router in front of services (ensemble.yaml `gateways:`) — is the outermost
// thing a client touches, so it leads the legend and takes the debit accent, the one unused
// high-contrast colour left in the palette. --topo-cat-credit is still unused.
export const CATEGORIES: CategoryDef[] = [
  { id: "gateway", label: "Gateways", colorVar: "--topo-cat-debit" },
  { id: "service", label: "Services", colorVar: "--topo-cat-edge" },
  { id: "database", label: "Databases", colorVar: "--topo-cat-infra" },
  { id: "stub", label: "Stubs", colorVar: "--topo-cat-stub" },
  { id: "other", label: "Ungrouped", colorVar: "--topo-cat-other" },
];

/** The server already classifies every node it emits (gateway/service/database/stub — see
    ensemble/server/routes.go's buildTopology); this is a straight passthrough plus a
    defensive 'other' bucket for a category string the palette doesn't recognize. Nothing in
    today's API produces that case — it exists so a future server-side category addition
    degrades to a visible "Ungrouped" cluster instead of a runtime crash, the same contract
    the old id-inference map gave unmapped services. */
export function categoryOf(node: TopologyNode): CategoryId {
  return node.category === "gateway" ||
    node.category === "service" ||
    node.category === "database" ||
    node.category === "stub"
    ? node.category
    : "other";
}

/** Ensemble has no equivalent of the old stack's `console` dev-tool carve-out — every node
    buildTopology emits is a real member of the graph. What can still happen is a stale edge:
    a topology snapshot is read while services are being reconfigured, and an edge names a
    node that isn't in this snapshot's node list. Layout indexes nodes by name and would
    silently drop such an edge anyway; normalizing it away here, once, keeps that assumption
    explicit and in one place instead of implicit in every consumer. */
export function normalizeTopology(t: Topology): Topology {
  const known = new Set(t.nodes.map((n) => n.name));
  return {
    nodes: t.nodes,
    edges: t.edges.filter((e) => known.has(e.from) && known.has(e.to)),
  };
}

/** CATEGORIES' id and colorVar deliberately don't share a name (see the comment above) — a
    node/cluster renderer can no longer string-template `--topo-cat-${category}` the way the
    old component did (category ids and CSS var suffixes were the same set there) and must
    look the accent up through this table instead. */
export function colorVarOf(id: CategoryId): string {
  return CATEGORIES.find((c) => c.id === id)?.colorVar ?? "--topo-cat-other";
}
