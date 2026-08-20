import type { Hop } from '../api/types';
import { GUTTER, NODE_H, NODE_W } from './layout';
import type { GraphEdge, GraphLayout, GraphNode, Point } from './types';

const ROW_GAP = 20;
const PAD = 32;

/** Synthetic caller for a root hop — ensemble's Hop.from is optional (nothing calls the
    entry service), unlike the old TraceHop, which always carried an explicit 'client'
    caller. Substituting the same sentinel here keeps every downstream algorithm (BFS depth,
    routing, node identity) working over plain strings rather than threading `| undefined`
    through all of them. */
function callerOf(h: Hop): string {
  return h.from ?? 'client';
}

/**
 * Three shapes, picked by where the target sits. Routing everything rightward is what put
 * a last-column edge's lane outside the canvas and got it clipped — a lane is only safe
 * when there is a column gap to put it in.
 */
function route(a: GraphNode, b: GraphNode): Point[] {
  // Forward: a lane in the gap between the two columns.
  if (b.x > a.x + a.w) {
    const ay = a.y + a.h / 2;
    const by = b.y + b.h / 2;
    const laneX = a.x + a.w + GUTTER / 2;
    return [
      { x: a.x + a.w, y: ay },
      { x: laneX, y: ay },
      { x: laneX, y: by },
      { x: b.x, y: by },
    ];
  }
  // Same column: a straight vertical between the facing edges.
  if (Math.abs(a.x - b.x) < 1) {
    const cx = a.x + a.w / 2;
    return b.y > a.y
      ? [{ x: cx, y: a.y + a.h }, { x: cx, y: b.y }]
      : [{ x: cx, y: a.y }, { x: cx, y: b.y + b.h }];
  }
  // Backward: drop under both boxes and come up into the target, never crossing either.
  const ax = a.x + a.w / 2;
  const bx = b.x + b.w / 2;
  const belowY = Math.max(a.y + a.h, b.y + b.h) + ROW_GAP / 2;
  return [
    { x: ax, y: a.y + a.h },
    { x: ax, y: belowY },
    { x: bx, y: belowY },
    { x: bx, y: b.y + b.h },
  ];
}

/**
 * BFS depth of every node from the trace's entry, keyed by node id. Shared by layoutTrace
 * (column = depth) and causalHopOrder (row order = depth of the calling node) so both agree
 * on the same notion of "earlier in the request".
 *
 * The entry is picked structurally — the node nobody ever calls (`to` never names it) —
 * not by which hop happens to sit first in the array. Producer timestamps come from
 * independent local processes (the proxy vs. an upstream service) and race by a few ms; a
 * downstream sub-call's recorded start can land ahead of its own causal parent's. In-degree
 * has no such noise: nothing calls "client", trace after trace, so it's always the root.
 */
export function callDepths(hops: Hop[]): Map<string, number> {
  const depth = new Map<string, number>();
  if (hops.length === 0) return depth;

  // Order of first appearance is the deterministic tiebreak everywhere below.
  const order: string[] = [];
  hops.forEach((h) => {
    [callerOf(h), h.to].forEach((id) => {
      if (!order.includes(id)) order.push(id);
    });
  });

  const calls = new Map<string, string[]>();
  order.forEach((id) => calls.set(id, []));
  const calledInto = new Set<string>();
  hops.forEach((h) => {
    const from = callerOf(h);
    const outs = calls.get(from) as string[];
    if (!outs.includes(h.to)) outs.push(h.to);
    calledInto.add(h.to);
  });

  // Fall back to the first hop's caller only for a trace where every node is someone's
  // callee (a cycle, or a synthetic fixture) — a real trace always has a client leg.
  const entry = order.find((id) => !calledInto.has(id)) ?? callerOf(hops[0]);

  // BFS from the entry. `depth` doubles as the visited set, so a cyclic trace terminates.
  const queue = [entry];
  depth.set(entry, 0);
  while (queue.length > 0) {
    const id = queue.shift() as string;
    const d = depth.get(id) as number;
    (calls.get(id) ?? []).forEach((next) => {
      if (depth.has(next)) return;
      depth.set(next, d + 1);
      queue.push(next);
    });
  }
  // Anything unreachable from the entry (a trace with several roots) is appended rightmost.
  const maxDepth = Math.max(0, ...depth.values());
  order.forEach((id) => {
    if (!depth.has(id)) depth.set(id, maxDepth + 1);
  });
  return depth;
}

/**
 * Row order for a trace's hop list: primarily by the causal depth of the calling node
 * (callDepths), `t.start` only to break ties among true siblings (concurrent calls from the
 * same caller). Depth is graph-structural and immune to the cross-process clock noise that
 * makes a plain start-time sort occasionally put a sub-call ahead of the request that
 * spawned it.
 */
export function causalHopOrder(hops: Hop[]): Hop[] {
  const depth = callDepths(hops);
  return [...hops].sort((a, b) => {
    const da = depth.get(callerOf(a)) ?? 0;
    const db = depth.get(callerOf(b)) ?? 0;
    if (da !== db) return da - db;
    return a.t.start < b.t.start ? -1 : a.t.start > b.t.start ? 1 : 0;
  });
}

/**
 * Call-order layout for one trace: column = BFS depth from the entry node, so the request
 * reads left to right and fan-out drops to extra rows. The declared dependency graph is not
 * consulted — a trace shows what actually happened, including calls `depends_on:` never
 * described.
 */
export function layoutTrace(hops: Hop[]): GraphLayout {
  if (hops.length === 0) return { nodes: [], edges: [], clusters: [], width: 0, height: 0 };

  const order: string[] = [];
  hops.forEach((h) => {
    [callerOf(h), h.to].forEach((id) => {
      if (!order.includes(id)) order.push(id);
    });
  });

  const depth = callDepths(hops);

  const columns = new Map<number, string[]>();
  order.forEach((id) => {
    const d = depth.get(id) as number;
    if (!columns.has(d)) columns.set(d, []);
    columns.get(d)?.push(id);
  });
  const levels = [...columns.keys()].sort((a, b) => a - b);
  const tallest = Math.max(...levels.map((l) => (columns.get(l)?.length ?? 0) * (NODE_H + ROW_GAP) - ROW_GAP));

  const nodes: GraphNode[] = [];
  levels.forEach((l, col) => {
    const ids = columns.get(l) as string[];
    const colHeight = ids.length * (NODE_H + ROW_GAP) - ROW_GAP;
    ids.forEach((id, row) => {
      nodes.push({
        id,
        x: PAD + col * (NODE_W + GUTTER),
        y: PAD + (tallest - colHeight) / 2 + row * (NODE_H + ROW_GAP),
        w: NODE_W,
        h: NODE_H,
        // A trace is a pure function of its hops — it never sees the live Topology fetch
        // that carries per-node category, so every node (real service or the synthetic
        // "client" root) renders in the same neutral 'other' bucket. See traceLayout.test.ts
        // for the rationale; TopologyView is the seam where cross-referencing against the
        // fetched topology could restore per-node color if that's ever wanted.
        category: 'other',
        health: 'unknown',
        entry: false,
        synthetic: id === 'client',
      });
    });
  });

  // One edge per distinct pair, carrying every hop ordinal seen on it.
  const nodeAt = new Map(nodes.map((n) => [n.id, n]));
  const pairs = new Map<string, GraphEdge>();
  hops.forEach((h) => {
    const from = callerOf(h);
    const key = `${from}->${h.to}`;
    const existing = pairs.get(key);
    if (existing) {
      existing.hopLabels?.push(h.seq);
      return;
    }
    const a = nodeAt.get(from);
    const b = nodeAt.get(h.to);
    if (!a || !b) return;
    pairs.set(key, {
      key,
      from,
      to: h.to,
      points: route(a, b),
      hopLabels: [h.seq],
    });
  });

  const edges = [...pairs.values()];

  // Extent is measured from the geometry actually emitted, not predicted from the column
  // count — an edge routed past the last column used to fall outside the viewBox and get
  // silently clipped. Anything drawn is inside the canvas by construction.
  let maxX = 0;
  let maxY = 0;
  for (const n of nodes) {
    maxX = Math.max(maxX, n.x + n.w);
    maxY = Math.max(maxY, n.y + n.h);
  }
  for (const e of edges) {
    for (const p of e.points) {
      maxX = Math.max(maxX, p.x);
      maxY = Math.max(maxY, p.y);
    }
  }

  return {
    nodes,
    edges,
    clusters: [],
    width: maxX + PAD,
    height: maxY + PAD,
  };
}
