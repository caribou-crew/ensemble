import type { ServiceState, Topology } from '../api/types';
import { CATEGORIES, categoryOf, normalizeTopology } from './categories';
import type { CategoryId, GraphCluster, GraphLayout, GraphNode, Health } from './types';

export const NODE_W = 180;
export const NODE_H = 48;
const NODE_GAP_X = 16;
const NODE_GAP_Y = 12;
/** Space inside a cluster rect, around its node grid. */
const CLUSTER_PAD = 18;
/** Extra top padding inside a cluster rect, reserved for its label. */
const CLUSTER_LABEL_H = 24;
const CLUSTER_GAP_Y = 36;
/** Horizontal space between cluster columns — edges route through this. */
export const GUTTER = 110;
const PAD = 32;
/** A node with this many edges into one other cluster gets them collapsed into one line. */
export const BUNDLE_MIN = 3;
const LANE_INSET = 18;

interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/**
 * Forward route: exit the source's right edge, run to `laneX`, drop vertically, enter the
 * target's left edge. Four points, all segments axis-aligned.
 */
function routeForward(a: Rect, b: Rect, laneX: number) {
  const ay = a.y + a.h / 2;
  const by = b.y + b.h / 2;
  return [
    { x: a.x + a.w, y: ay },
    { x: laneX, y: ay },
    { x: laneX, y: by },
    { x: b.x, y: by },
  ];
}

/**
 * Fallback for targets that are not to the right (same column, same cluster, or a
 * back-edge): drop out of the source's bottom, run under both boxes, come up into the
 * target's bottom. Never crosses either rect.
 *
 * The horizontal run hugs the lower of the two boxes rather than dropping to the canvas
 * bottom — otherwise every intra-cluster edge converges onto one line at the foot of the
 * whole graph, nowhere near the nodes it connects.
 */
function routeUnder(a: Rect, b: Rect) {
  const ax = a.x + a.w / 2;
  const bx = b.x + b.w / 2;
  const belowY = Math.max(a.y + a.h, b.y + b.h) + NODE_GAP_Y / 2;
  return [
    { x: ax, y: a.y + a.h },
    { x: ax, y: belowY },
    { x: bx, y: belowY },
    { x: bx, y: b.y + b.h },
  ];
}

/** ensemble's ServiceState.status is a real lifecycle enum (stopped/starting/healthy/
    unhealthy — see ensemble/orchestrator/orchestrator.go's Status const), not the old
    stack's binary health field, so this maps that lifecycle onto the render-facing Health
    tiers instead of reading a pre-computed `.health`/`.mode` pair. */
export function healthOf(s: ServiceState | undefined): Health {
  if (!s) return 'unknown';
  if (s.status === 'healthy') return s.placement === 'docker' ? 'up-container' : 'up-native';
  if (s.status === 'starting') return 'starting';
  if (s.status === 'unhealthy' || s.status === 'stopped') return 'down';
  return 'unknown';
}

/**
 * Longest-path layering over the cluster-level DAG. Consumers left, providers right:
 * a cluster's level is one past the deepest cluster that calls into it.
 * `seen` guards against cycles between clusters — node-level `depends_on:` can't cycle, but
 * two clusters can legitimately call each other, and that must not spin.
 */
function levelClusters(
  clusterEdges: Map<CategoryId, Set<CategoryId>>,
  ids: CategoryId[],
): Map<CategoryId, number> {
  const callers = new Map<CategoryId, CategoryId[]>();
  ids.forEach((id) => callers.set(id, []));
  clusterEdges.forEach((tos, from) => tos.forEach((to) => callers.get(to)?.push(from)));

  const level = new Map<CategoryId, number>();
  function levelOf(id: CategoryId, seen: Set<CategoryId>): number {
    const memo = level.get(id);
    if (memo !== undefined) return memo;
    if (seen.has(id)) return 0;
    seen.add(id);
    const from = callers.get(id) ?? [];
    const l = from.length === 0 ? 0 : 1 + Math.max(...from.map((c) => levelOf(c, seen)));
    seen.delete(id);
    level.set(id, l);
    return l;
  }
  ids.forEach((id) => levelOf(id, new Set()));
  return level;
}

export function layoutClustered(
  topology: Topology,
  statuses: Map<string, ServiceState>,
  expanded: Set<string>,
): GraphLayout {
  const t = normalizeTopology(topology);
  const byName = new Map(t.nodes.map((n) => [n.name, n]));
  // Safe after normalizeTopology: every surviving edge's endpoints are guaranteed present.
  const catOf = (name: string): CategoryId => categoryOf(byName.get(name) as (typeof t.nodes)[number]);

  // in-degree drives intra-cluster ordering: the most depended-on service leads its cluster.
  const inDegree = new Map<string, number>();
  t.nodes.forEach((n) => inDegree.set(n.name, 0));
  t.edges.forEach((e) => inDegree.set(e.to, (inDegree.get(e.to) ?? 0) + 1));

  // Group by category, in CATEGORIES order so cluster order never depends on node order.
  const members = new Map<CategoryId, string[]>();
  t.nodes.forEach((n) => {
    const c = categoryOf(n);
    if (!members.has(c)) members.set(c, []);
    members.get(c)?.push(n.name);
  });
  const activeIds = CATEGORIES.map((c) => c.id).filter((id) => (members.get(id)?.length ?? 0) > 0);
  activeIds.forEach((id) => {
    members.get(id)?.sort((a, b) => (inDegree.get(b) ?? 0) - (inDegree.get(a) ?? 0) || a.localeCompare(b));
  });

  // Cluster-level DAG, self-edges ignored.
  const clusterEdges = new Map<CategoryId, Set<CategoryId>>();
  t.edges.forEach((e) => {
    const from = catOf(e.from);
    const to = catOf(e.to);
    if (from === to) return;
    if (!clusterEdges.has(from)) clusterEdges.set(from, new Set());
    clusterEdges.get(from)?.add(to);
  });
  const level = levelClusters(clusterEdges, activeIds);

  // Cluster box sizes from their node grids.
  const grid = new Map<CategoryId, { cols: number; rows: number; w: number; h: number }>();
  activeIds.forEach((id) => {
    const n = members.get(id)?.length ?? 0;
    const cols = Math.ceil(Math.sqrt(n));
    const rows = Math.ceil(n / cols);
    grid.set(id, {
      cols,
      rows,
      w: cols * NODE_W + (cols - 1) * NODE_GAP_X + CLUSTER_PAD * 2,
      h: rows * NODE_H + (rows - 1) * NODE_GAP_Y + CLUSTER_PAD * 2 + CLUSTER_LABEL_H,
    });
  });

  // Columns by level; within a column, CATEGORIES order.
  const columns = new Map<number, CategoryId[]>();
  activeIds.forEach((id) => {
    const l = level.get(id) ?? 0;
    if (!columns.has(l)) columns.set(l, []);
    columns.get(l)?.push(id);
  });
  const levels = [...columns.keys()].sort((a, b) => a - b);

  const columnWidth = new Map<number, number>();
  const columnHeight = new Map<number, number>();
  levels.forEach((l) => {
    const ids = columns.get(l) as CategoryId[];
    columnWidth.set(l, Math.max(...ids.map((id) => grid.get(id)?.w ?? 0)));
    columnHeight.set(
      l,
      ids.reduce((sum, id) => sum + (grid.get(id)?.h ?? 0), 0) + (ids.length - 1) * CLUSTER_GAP_Y,
    );
  });
  const tallest = Math.max(...levels.map((l) => columnHeight.get(l) ?? 0));

  // Place clusters, then nodes inside them.
  const clusters: GraphCluster[] = [];
  const nodes: GraphNode[] = [];
  let x = PAD;
  levels.forEach((l) => {
    const ids = columns.get(l) as CategoryId[];
    let y = PAD + (tallest - (columnHeight.get(l) ?? 0)) / 2;
    ids.forEach((id) => {
      const g = grid.get(id) as { cols: number; rows: number; w: number; h: number };
      const def = CATEGORIES.find((c) => c.id === id);
      clusters.push({ id, label: def?.label ?? id, x, y, w: g.w, h: g.h });
      (members.get(id) ?? []).forEach((nodeName, i) => {
        const meta = byName.get(nodeName);
        const state = statuses.get(nodeName);
        nodes.push({
          id: nodeName,
          x: x + CLUSTER_PAD + (i % g.cols) * (NODE_W + NODE_GAP_X),
          y: y + CLUSTER_PAD + CLUSTER_LABEL_H + Math.floor(i / g.cols) * (NODE_H + NODE_GAP_Y),
          w: NODE_W,
          h: NODE_H,
          category: id,
          health: healthOf(state),
          entry: meta?.entry ?? false,
          port: state?.port,
        });
      });
      y += g.h + CLUSTER_GAP_Y;
    });
    x += (columnWidth.get(l) ?? 0) + GUTTER;
  });

  const nodeAt = new Map(nodes.map((n) => [n.id, n]));
  const clusterAt = new Map(clusters.map((c) => [c.id, c]));
  const width = x - GUTTER + PAD;
  const height = PAD * 2 + tallest;

  // 1. Decide which edges bundle. Key is `${from}->${targetCategory}`; TopologyView's
  //    expanded set holds exactly these strings.
  const fanOut = new Map<string, string[]>();
  t.edges.forEach((e) => {
    const toCat = catOf(e.to);
    if (catOf(e.from) === toCat) return;
    const key = `${e.from}->${toCat}`;
    if (!fanOut.has(key)) fanOut.set(key, []);
    fanOut.get(key)?.push(e.to);
  });
  // Bundleable is the group's own property; bundled is that minus whatever the user expanded.
  // The expanded members still need to carry their bundleKey — it is the only handle the graph
  // has for collapsing them again.
  const bundleable = new Set(
    [...fanOut.entries()].filter(([, targets]) => targets.length >= BUNDLE_MIN).map(([key]) => key),
  );
  const bundled = new Set([...bundleable].filter((key) => !expanded.has(key)));

  // 2. Build the logical edge list — one entry per drawn line.
  interface Pending {
    key: string;
    from: string;
    to: string;
    target: Rect;
    bundleKey?: string;
    bundleCount?: number;
    bundleTargets?: string[];
    bundleExpanded?: boolean;
  }
  const pending: Pending[] = [];
  const emittedBundles = new Set<string>();
  t.edges.forEach((e) => {
    const toCat = catOf(e.to);
    const bundleKey = `${e.from}->${toCat}`;
    if (bundled.has(bundleKey)) {
      if (emittedBundles.has(bundleKey)) return;
      emittedBundles.add(bundleKey);
      const cluster = clusterAt.get(toCat);
      if (!cluster) return;
      const targets = fanOut.get(bundleKey) ?? [];
      pending.push({
        key: bundleKey,
        from: e.from,
        to: toCat,
        target: cluster,
        bundleKey,
        bundleCount: targets.length,
        bundleTargets: [...targets].sort(),
      });
      return;
    }
    const target = nodeAt.get(e.to);
    if (!target) return;
    // An expanded group hands its collapse control to exactly ONE member edge. Marking every
    // member would take over their click, which still belongs to edge selection; marking none
    // is the bug — expanding a bundle used to be one-way, with nothing left to click.
    const carriesCollapse = bundleable.has(bundleKey) && !emittedBundles.has(bundleKey);
    if (carriesCollapse) emittedBundles.add(bundleKey);
    pending.push({
      key: `${e.from}->${e.to}`,
      from: e.from,
      to: e.to,
      target,
      ...(carriesCollapse
        ? { bundleKey, bundleCount: (fanOut.get(bundleKey) ?? []).length, bundleExpanded: true }
        : {}),
    });
  });

  // 3. Allocate gutter lanes. Edges are grouped by the gutter immediately right of their
  //    source, and sorted by target y so vertical runs nest instead of crossing.
  // Only edges with a real gutter to cross get a lane. A target sitting a mere node-gap to
  // the right (an intra-cluster neighbour) has no room to spread lanes into — its lane
  // would collapse onto its neighbours' and land right of the target anyway — so it falls
  // through to routeUnder below.
  const MIN_LANE_ROOM = LANE_INSET * 2 + 8;
  const forward = pending.filter((p) => {
    const src = nodeAt.get(p.from);
    return src !== undefined && p.target.x - (src.x + src.w) >= MIN_LANE_ROOM;
  });
  const byGutter = new Map<number, Pending[]>();
  forward.forEach((p) => {
    const src = nodeAt.get(p.from) as GraphNode;
    const gutterStart = src.x + src.w;
    if (!byGutter.has(gutterStart)) byGutter.set(gutterStart, []);
    byGutter.get(gutterStart)?.push(p);
  });
  const laneOf = new Map<string, number>();
  byGutter.forEach((group, gutterStart) => {
    const sorted = [...group].sort((a, b) => a.target.y - b.target.y || a.key.localeCompare(b.key));
    // Lanes are spread across the gap between this source's right edge and its nearest
    // target's left edge, insetting so a lane never sits flush against a box.
    const nearest = Math.min(...sorted.map((p) => p.target.x));
    // MIN_LANE_ROOM guarantees span > LANE_INSET * 2, so every index gets a distinct lane
    // and the rightmost still lands left of the nearest target.
    const span = nearest - gutterStart;
    sorted.forEach((p, i) => {
      laneOf.set(p.key, gutterStart + LANE_INSET + ((span - LANE_INSET * 2) * (i + 1)) / (sorted.length + 1));
    });
  });

  // 4. Route. Anything without a lane (same column, back-edge, intra-cluster) goes under.
  const edges = pending.map((p) => {
    const src = nodeAt.get(p.from) as GraphNode;
    const laneX = laneOf.get(p.key);
    return {
      key: p.key,
      from: p.from,
      to: p.to,
      points: laneX === undefined ? routeUnder(src, p.target) : routeForward(src, p.target, laneX),
      bundleKey: p.bundleKey,
      bundleCount: p.bundleCount,
      bundleTargets: p.bundleTargets,
      bundleExpanded: p.bundleExpanded,
    };
  });

  return { nodes, edges, clusters, width, height };
}
