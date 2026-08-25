import { describe, expect, it } from 'vitest';
import { layoutDepth } from './depthLayout';
import { normalizeTopology } from './categories';
import { SAMPLE_TOPOLOGY } from './fixtures';
import type { ServiceState, Topology } from '../api/types';
import type { GraphNode } from './types';

const NO_STATUS = new Map<string, ServiceState>();

function overlaps(a: GraphNode, b: GraphNode): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
}

describe('layoutDepth', () => {
  it('gives every node a finite position', () => {
    const l = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    const expected = normalizeTopology(SAMPLE_TOPOLOGY).nodes.length;
    expect(l.nodes.length).toBe(expected);
    for (const n of l.nodes) {
      expect(Number.isFinite(n.x) && Number.isFinite(n.y)).toBe(true);
    }
  });

  it('never overlaps two node rects', () => {
    const l = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    for (let i = 0; i < l.nodes.length; i++) {
      for (let j = i + 1; j < l.nodes.length; j++) {
        expect(overlaps(l.nodes[i], l.nodes[j])).toBe(false);
      }
    }
  });

  it('never emits clusters — category is carried as node color only', () => {
    const l = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    expect(l.clusters).toEqual([]);
  });

  it('keeps each node\'s real category (no boxless-mode fallback to "other")', () => {
    const l = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('edge-gateway').category).toBe('service');
    expect(at('orders-db').category).toBe('database');
    expect(at('email-stub').category).toBe('stub');
  });

  it('an entry point with no caller sits in the topmost row', () => {
    const l = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('edge-gateway').y).toBeLessThan(at('orders').y);
    expect(at('edge-gateway').y).toBeLessThan(at('payments').y);
  });

  it('a service called by another service sinks below its caller, even though both are "service"', () => {
    // orders -> inventory: same category, but inventory is one hop deeper.
    const l = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('orders').y).toBeLessThan(at('inventory').y);
  });

  it('a database with no outgoing calls sits at the bottom of its chain', () => {
    const l = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('orders').y).toBeLessThan(at('orders-db').y);
    expect(at('edge-gateway').y).toBeLessThan(at('orders-db').y);
  });

  it('reflects live status through the same health mapping as the clustered layout', () => {
    const statuses = new Map<string, ServiceState>([
      ['orders', { name: 'orders', status: 'healthy', placement: 'native' }],
    ]);
    const l = layoutDepth(SAMPLE_TOPOLOGY, statuses);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('orders').health).toBe('up-native');
    expect(at('accounts').health).toBe('unknown');
  });

  it('is deterministic', () => {
    const a = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    const b = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    expect(a).toEqual(b);
  });

  it('does not spin on a cycle between two nodes', () => {
    const cyclic: Topology = {
      nodes: [
        { name: 'a', category: 'service', status: 'healthy' },
        { name: 'b', category: 'service', status: 'healthy' },
      ],
      edges: [
        { from: 'a', to: 'b' },
        { from: 'b', to: 'a' },
      ],
    };
    const l = layoutDepth(cyclic, NO_STATUS);
    expect(l.nodes.length).toBe(2);
    for (const n of l.nodes) {
      expect(Number.isFinite(n.x) && Number.isFinite(n.y)).toBe(true);
    }
  });

  it('every edge\'s points are finite and start/end on its node rects', () => {
    const l = layoutDepth(SAMPLE_TOPOLOGY, NO_STATUS);
    for (const e of l.edges) {
      expect(e.points.length).toBeGreaterThanOrEqual(2);
      for (const p of e.points) {
        expect(Number.isFinite(p.x) && Number.isFinite(p.y)).toBe(true);
      }
    }
  });
});
