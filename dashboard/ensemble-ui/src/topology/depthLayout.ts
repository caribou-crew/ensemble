import type { ServiceState, Topology } from '../api/types';
import { CATEGORIES, categoryOf, normalizeTopology } from './categories';
import { healthOf, NODE_H, NODE_W } from './layout';
import type { GraphEdge, GraphLayout, GraphNode, Point } from './types';

const NODE_GAP_X = 24;
/** Vertical gap between rows — a forward edge's horizontal lane runs through the middle of
    this, so it needs enough room to read as a distinct segment rather than hugging a box. */
const ROW_GAP_Y = 72;
/** Rightward swing for a same-row or back-edge (a call into an equal-or-shallower row —
    only possible via a cycle, since a real DAG only ever calls deeper). Mirrors
    traceLayout.ts's routeUnder, rotated 90°. */
const LOOP_GAP = 40;
const PAD = 32;

const CATEGORY_ORDER = new Map(CATEGORIES.map((c, i) => [c.id, i]));

/**
 * Longest-path depth per NODE (not per category, unlike layoutClustered's cluster-level
 * levelClusters) — a service that calls another service sinks one row below it even though
 * both share a category. `seen` guards cycles between individual nodes the same way
 * levelClusters guards cycles between clusters: a node revisited mid-walk contributes 0
 * rather than recursing forever.
 */
function depthsOf(names: string[], callers: Map<string, string[]>): Map<string, number> {
  const depth = new Map<string, number>();
  function depthOf(id: string, seen: Set<string>): number {
    const memo = depth.get(id);
    if (memo !== undefined) return memo;
    if (seen.has(id)) return 0;
    seen.add(id);
    const from = callers.get(id) ?? [];
    const d = from.length === 0 ? 0 : 1 + Math.max(...from.map((c) => depthOf(c, seen)));
    seen.delete(id);
    depth.set(id, d);
    return d;
  }
  names.forEach((id) => depthOf(id, new Set()));
  return depth;
}

/**
 * Three shapes, mirroring traceLayout.ts's route() rotated 90° (rows instead of columns):
 * forward drops through a horizontal lane in the row gap, same-row runs a straight
 * horizontal between facing edges, backward (only reachable via a cycle, since a real
 * dependency DAG never calls back to an equal-or-shallower row) swings right of both boxes.
 */
function route(a: GraphNode, b: GraphNode): Point[] {
  if (b.y > a.y + a.h) {
    const ax = a.x + a.w / 2;
    const bx = b.x + b.w / 2;
    const laneY = a.y + a.h + ROW_GAP_Y / 2;
    return [
      { x: ax, y: a.y + a.h },
      { x: ax, y: laneY },
      { x: bx, y: laneY },
      { x: bx, y: b.y },
    ];
  }
  if (Math.abs(a.y - b.y) < 1) {
    const cy = a.y + a.h / 2;
    return b.x > a.x
      ? [{ x: a.x + a.w, y: cy }, { x: b.x, y: cy }]
      : [{ x: a.x, y: cy }, { x: b.x + b.w, y: cy }];
  }
  const ay = a.y + a.h / 2;
  const by = b.y + b.h / 2;
  const rightX = Math.max(a.x + a.w, b.x + b.w) + LOOP_GAP;
  return [
    { x: a.x + a.w, y: ay },
    { x: rightX, y: ay },
    { x: rightX, y: by },
    { x: b.x + b.w, y: by },
  ];
}

/**
 * Top-down call-flow layout: row = each node's own longest-path call depth from an entry
 * point (nothing calls it), so a caller always sits above everything it calls, however many
 * hops deep — unlike layoutClustered, this does not stop at "which category" a node belongs
 * to. Category survives only as the node's accent color (see TopologyGraph's colorVarOf);
 * there are no cluster boxes (clusters is always empty, the same contract layoutTrace uses
 * for its own boxless mode).
 */
export function layoutDepth(topology: Topology, statuses: Map<string, ServiceState>): GraphLayout {
  const t = normalizeTopology(topology);
  const byName = new Map(t.nodes.map((n) => [n.name, n]));
  const names = t.nodes.map((n) => n.name);

  const callers = new Map<string, string[]>();
  names.forEach((id) => callers.set(id, []));
  const inDegree = new Map<string, number>();
  names.forEach((id) => inDegree.set(id, 0));
  t.edges.forEach((e) => {
    callers.get(e.to)?.push(e.from);
    inDegree.set(e.to, (inDegree.get(e.to) ?? 0) + 1);
  });

  const depth = depthsOf(names, callers);

  const rows = new Map<number, string[]>();
  names.forEach((id) => {
    const d = depth.get(id) ?? 0;
    if (!rows.has(d)) rows.set(d, []);
    rows.get(d)?.push(id);
  });
  const levels = [...rows.keys()].sort((a, b) => a - b);
  levels.forEach((l) => {
    rows.get(l)?.sort((a, b) => {
      const ca = CATEGORY_ORDER.get(categoryOf(byName.get(a) as (typeof t.nodes)[number])) ?? 0;
      const cb = CATEGORY_ORDER.get(categoryOf(byName.get(b) as (typeof t.nodes)[number])) ?? 0;
      return ca - cb || (inDegree.get(b) ?? 0) - (inDegree.get(a) ?? 0) || a.localeCompare(b);
    });
  });

  const rowWidth = new Map<number, number>();
  levels.forEach((l) => {
    const n = rows.get(l)?.length ?? 0;
    rowWidth.set(l, n * NODE_W + (n - 1) * NODE_GAP_X);
  });
  const widest = Math.max(...levels.map((l) => rowWidth.get(l) ?? 0));

  const nodes: GraphNode[] = [];
  levels.forEach((l) => {
    const ids = rows.get(l) as string[];
    const w = rowWidth.get(l) ?? 0;
    const startX = PAD + (widest - w) / 2;
    ids.forEach((id, i) => {
      const meta = byName.get(id);
      const state = statuses.get(id);
      nodes.push({
        id,
        x: startX + i * (NODE_W + NODE_GAP_X),
        y: PAD + l * (NODE_H + ROW_GAP_Y),
        w: NODE_W,
        h: NODE_H,
        category: meta ? categoryOf(meta) : 'other',
        health: healthOf(state),
        entry: meta?.entry ?? false,
        port: state?.port,
      });
    });
  });

  const nodeAt = new Map(nodes.map((n) => [n.id, n]));
  const edges: GraphEdge[] = [];
  t.edges.forEach((e) => {
    const a = nodeAt.get(e.from);
    const b = nodeAt.get(e.to);
    if (!a || !b) return;
    edges.push({ key: `${e.from}->${e.to}`, from: e.from, to: e.to, points: route(a, b) });
  });

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

  return { nodes, edges, clusters: [], width: maxX + PAD, height: maxY + PAD };
}
