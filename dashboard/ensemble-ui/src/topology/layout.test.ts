import { describe, expect, it } from 'vitest';
import { layoutClustered } from './layout';
import { normalizeTopology } from './categories';
import { SAMPLE_TOPOLOGY } from './fixtures';
import type { ServiceState } from '../api/types';
import type { GraphNode } from './types';

const NO_STATUS = new Map<string, ServiceState>();
const NO_EXPANDED = new Set<string>();

function overlaps(a: GraphNode, b: GraphNode): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
}

describe('layoutClustered', () => {
  it('gives every node a finite position', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const expected = normalizeTopology(SAMPLE_TOPOLOGY).nodes.length;
    expect(l.nodes.length).toBe(expected);
    for (const n of l.nodes) {
      expect(Number.isFinite(n.x) && Number.isFinite(n.y)).toBe(true);
    }
  });

  it('never overlaps two node rects', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    for (let i = 0; i < l.nodes.length; i++) {
      for (let j = i + 1; j < l.nodes.length; j++) {
        expect(overlaps(l.nodes[i], l.nodes[j])).toBe(false);
      }
    }
  });

  it('contains every cluster member inside its cluster rect', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    for (const c of l.clusters) {
      const members = l.nodes.filter((n) => n.category === c.id);
      expect(members.length).toBeGreaterThan(0);
      for (const n of members) {
        expect(n.x >= c.x && n.y >= c.y && n.x + n.w <= c.x + c.w && n.y + n.h <= c.y + c.h).toBe(true);
      }
    }
  });

  it('never emits an empty cluster', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    // Every category present in SAMPLE_TOPOLOGY has at least one member, so 'other' — the
    // fallback for a category the palette doesn't recognize — must never appear.
    expect(l.clusters.map((c) => c.id)).not.toContain('other');
  });

  it('places the service cluster left of the database and stub clusters it calls into', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const at = (id: string) => l.clusters.find((c) => c.id === id);
    const service = at('service');
    const database = at('database');
    const stub = at('stub');
    expect(service).toBeDefined();
    expect(database).toBeDefined();
    expect(stub).toBeDefined();
    expect((service as NonNullable<typeof service>).x).toBeLessThan((database as NonNullable<typeof database>).x);
    expect((service as NonNullable<typeof service>).x).toBeLessThan((stub as NonNullable<typeof stub>).x);
  });

  it('is deterministic', () => {
    const a = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const b = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    expect(a).toEqual(b);
  });

  it('derives health from the status map, defaulting to unknown', () => {
    const statuses = new Map<string, ServiceState>([
      ['orders', { name: 'orders', status: 'healthy', placement: 'docker' }],
      ['payments', { name: 'payments', status: 'unhealthy', placement: 'native' }],
      ['accounts', { name: 'accounts', status: 'starting', placement: 'native' }],
    ]);
    const l = layoutClustered(SAMPLE_TOPOLOGY, statuses, NO_EXPANDED);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('orders').health).toBe('up-container');
    expect(at('payments').health).toBe('down');
    expect(at('accounts').health).toBe('starting');
    expect(at('edge-gateway').health).toBe('unknown');
  });

  it('carries the entry flag from the topology node', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('edge-gateway').entry).toBe(true);
    expect(at('orders').entry).toBe(false);
  });

  it('carries the port from the status map, not the topology', () => {
    const statuses = new Map<string, ServiceState>([
      ['orders', { name: 'orders', status: 'healthy', placement: 'native', port: 4210 }],
    ]);
    const l = layoutClustered(SAMPLE_TOPOLOGY, statuses, NO_EXPANDED);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('orders').port).toBe(4210);
    expect(at('payments').port).toBeUndefined();
  });

  it('covers every cluster with the graph extent', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    for (const c of l.clusters) {
      expect(c.x + c.w).toBeLessThanOrEqual(l.width);
      expect(c.y + c.h).toBeLessThanOrEqual(l.height);
    }
  });

  it('bundles a three-target stub fan-out into one edge', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const stubEdges = l.edges.filter((e) => e.from === 'notifications' && e.bundleKey === 'notifications->stub');
    expect(stubEdges.length).toBe(1);
    expect(stubEdges[0].bundleCount).toBe(3);
    expect(stubEdges[0].bundleTargets).toEqual(['email-stub', 'push-stub', 'sms-stub']);
  });

  it('does not bundle a two-target stub fan-out', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const e = l.edges.filter((x) => x.from === 'payments' && x.to === 'fraud-stub' || x.from === 'payments' && x.to === 'email-stub');
    expect(e.length).toBe(2);
    for (const edge of e) {
      expect(edge.bundleCount).toBeUndefined();
    }
  });

  it('expanding a bundle restores its individual member edges', () => {
    const collapsed = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const expanded = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, new Set(['notifications->stub']));
    const bundled = collapsed.edges.find((e) => e.bundleKey === 'notifications->stub');
    const count = bundled?.bundleCount ?? 0;
    expect(count).toBeGreaterThanOrEqual(3);
    expect(expanded.edges.length).toBe(collapsed.edges.length - 1 + count);
    const members = expanded.edges.filter((e) => e.bundleKey === 'notifications->stub');
    expect(members.length).toBe(1);
    expect(members[0].bundleExpanded).toBe(true);
    expect(members[0].bundleCount).toBe(count);
  });

  it('keeps a handle to re-collapse an expanded bundle', () => {
    const key = 'notifications->stub';
    const expanded = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, new Set([key]));
    const control = expanded.edges.find((e) => e.bundleExpanded);
    expect(control?.bundleKey).toBe(key);
    const plain = expanded.edges.filter((e) => e.from === 'notifications' && e.bundleKey === undefined);
    expect(plain.length).toBeGreaterThan(0);
  });

  it('starts every edge polyline on its source rect and ends it on its target', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const nodeAt = new Map(l.nodes.map((n) => [n.id, n]));
    const clusterAt = new Map(l.clusters.map((c) => [String(c.id), c]));
    const onRect = (p: { x: number; y: number }, r: { x: number; y: number; w: number; h: number }) =>
      p.x >= r.x - 0.5 && p.x <= r.x + r.w + 0.5 && p.y >= r.y - 0.5 && p.y <= r.y + r.h + 0.5;

    for (const e of l.edges) {
      expect(e.points.length).toBeGreaterThanOrEqual(2);
      const src = nodeAt.get(e.from);
      expect(src).toBeDefined();
      expect(onRect(e.points[0], src as GraphNode)).toBe(true);
      const end = e.points[e.points.length - 1];
      const dst = e.bundleCount ? clusterAt.get(e.to) : nodeAt.get(e.to);
      expect(dst).toBeDefined();
      expect(onRect(end, dst as GraphNode)).toBe(true);
    }
  });

  it('gives distinct lanes to edges leaving the same node through the same gutter', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const groups = new Map<number, number[]>();
    for (const e of l.edges) {
      if (e.points[0].x === e.points[1].x) continue;
      const gutterStart = e.points[0].x;
      if (!groups.has(gutterStart)) groups.set(gutterStart, []);
      groups.get(gutterStart)?.push(e.points[1].x);
    }
    expect(groups.size).toBeGreaterThan(0);
    for (const [, lanes] of groups) {
      expect(new Set(lanes).size).toBe(lanes.length);
    }
  });

  it('keeps intra-cluster edges inside their cluster rather than dropping to the canvas floor', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    const clusterAt = new Map(l.clusters.map((c) => [String(c.id), c]));
    const nodeAt = new Map(l.nodes.map((n) => [n.id, n]));
    const under = l.edges.filter((e) => e.points[0].x === e.points[1].x);
    expect(under.length).toBeGreaterThan(0);
    for (const e of under) {
      const src = nodeAt.get(e.from) as GraphNode;
      const cluster = clusterAt.get(src.category);
      if (!cluster) continue;
      const lowest = Math.max(...e.points.map((p) => p.y));
      expect(lowest).toBeLessThanOrEqual(cluster.y + cluster.h);
    }
  });

  it('gives every edge a unique key', () => {
    const l = layoutClustered(SAMPLE_TOPOLOGY, NO_STATUS, NO_EXPANDED);
    expect(new Set(l.edges.map((e) => e.key)).size).toBe(l.edges.length);
  });
});
