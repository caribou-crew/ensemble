// The single contract between the layout engines (layout.ts, traceLayout.ts) and the
// renderer (TopologyGraph.tsx). A mode is a choice of layout function, not a flag.
//
// Ported from local-stack/web/src/topology/types.ts. CategoryId shrank from the old
// fintech-plane set ('edge'|'debit'|'credit'|'infra'|'stub'|'other') to ensemble's actual
// domain: the server already classifies every node as 'service'|'database'|'stub' (see
// api/types.ts's TopologyNode), so there is no plane to infer — just a passthrough plus a
// defensive 'other' bucket (categories.ts). Health gained a 'starting' tier: ensemble's
// ServiceState.status is a real lifecycle enum (stopped/starting/healthy/unhealthy), not the
// old stack's binary up/down, and starting is a state worth distinguishing from unknown (no
// status data yet) rather than collapsing into it.

export type CategoryId = "gateway" | "service" | "database" | "stub" | "other";

export type Health =
  "up-native" | "up-container" | "starting" | "down" | "unknown";

export interface Point {
  x: number;
  y: number;
}

export interface GraphNode {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  category: CategoryId;
  health: Health;
  /** Clients call this service directly (TopologyNode.entry). Absent from the old
      StackTopology node shape entirely — ensemble's config declares this explicitly rather
      than it being inferred from an `edge` category. */
  entry: boolean;
  /** From the status map, not the topology (ensemble's TopologyNode carries no port — only
      ServiceState does). Undefined when the service has never reported in. */
  port?: number;
  /** True for nodes observed in a trace that the declared topology doesn't know (e.g. the
      synthetic "client" entry a trace invents for an untraced caller). */
  synthetic?: boolean;
}

export interface GraphEdge {
  key: string;
  from: string;
  to: string;
  /** Orthogonal polyline: first point on the source rect, last on the target rect. */
  points: Point[];
  /** >1 when this edge stands for N collapsed edges into one cluster. */
  bundleCount?: number;
  /** Node ids collapsed into this bundle; drives click-to-expand. */
  bundleTargets?: string[];
  /** Stable id of the bundle this edge represents, for the expanded-set. */
  bundleKey?: string;
  /** True on the one member edge that carries an expanded group's collapse control. */
  bundleExpanded?: boolean;
  /** Hop ordinals carried by this edge (trace mode only). */
  hopLabels?: number[];
  /** Mirrors Hop.attribution when this edge's caller isn't a real, trace-derived fact:
   * "declared" (the caller self-asserted its name via X-Ensemble-Caller) or "inferred" (a
   * config-declared called_by guess). Undefined means the edge is trace-derived. Trace mode
   * only. */
  attribution?: 'inferred' | 'declared';
}

export interface GraphCluster {
  id: CategoryId;
  label: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface GraphLayout {
  nodes: GraphNode[];
  edges: GraphEdge[];
  /** Empty in trace mode. */
  clusters: GraphCluster[];
  width: number;
  height: number;
}
