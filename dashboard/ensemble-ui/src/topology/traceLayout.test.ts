import { describe, expect, it } from 'vitest';
import { causalHopOrder, layoutTrace } from './traceLayout';
import { BRANCHING_HOPS, LINEAR_HOPS } from './fixtures';
import type { GraphNode } from './types';
import type { Hop } from '../api/types';

describe('layoutTrace', () => {
  it('the node set is exactly the unique from/to of the hops, with an undefined caller becoming "client"', () => {
    const l = layoutTrace(LINEAR_HOPS);
    expect(l.nodes.map((n) => n.id).sort()).toEqual(
      ['client', 'edge-gateway', 'inventory', 'orders', 'orders-db'].sort(),
    );
  });

  // Independent local processes (the proxy vs. an upstream service) stamp `t.start` off
  // their own clock reads — a downstream sub-call's start can land a few ms ahead of the
  // entry hop that caused it, exactly like this fixture, reproduced from the shape of a live
  // trace.
  const RACY_HOPS: Hop[] = [
    { ...LINEAR_HOPS[2], from: 'orders', to: 'inventory', t: { start: '2026-08-19T21:35:08.901Z' } },
    { ...LINEAR_HOPS[0], from: undefined, to: 'edge-gateway', t: { start: '2026-08-19T21:35:08.906Z' } },
    { ...LINEAR_HOPS[1], from: 'edge-gateway', to: 'orders', t: { start: '2026-08-19T21:35:08.906Z' } },
  ];

  it('picks the entry node by in-degree, not array/ts order — a racy downstream start cannot steal the root', () => {
    const l = layoutTrace(RACY_HOPS);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('client').x).toBeLessThan(at('edge-gateway').x);
    expect(at('edge-gateway').x).toBeLessThan(at('orders').x);
    expect(at('orders').x).toBeLessThan(at('inventory').x);
  });

  it('causalHopOrder puts the entry hop first despite its start trailing a sub-call\'s', () => {
    const ordered = causalHopOrder(RACY_HOPS);
    expect(ordered.map((h) => `${h.from ?? 'client'}->${h.to}`)).toEqual([
      'client->edge-gateway',
      'edge-gateway->orders',
      'orders->inventory',
    ]);
  });

  it('the entry node sits leftmost and the chain reads left to right', () => {
    const l = layoutTrace(LINEAR_HOPS);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('client').x).toBeLessThan(at('edge-gateway').x);
    expect(at('edge-gateway').x).toBeLessThan(at('orders').x);
    expect(at('orders').x).toBeLessThan(at('inventory').x);
    expect(at('inventory').x).toBeLessThan(at('orders-db').x);
  });

  it('a branch drops to a second row without overlapping', () => {
    const l = layoutTrace(BRANCHING_HOPS);
    const ordersDb = l.nodes.find((n) => n.id === 'orders-db') as GraphNode;
    const paymentsDb = l.nodes.find((n) => n.id === 'payments-db') as GraphNode;
    expect(ordersDb.x).toBe(paymentsDb.x);
    expect(ordersDb.y).not.toBe(paymentsDb.y);
  });

  it('repeated calls on one pair collapse to one edge carrying both hop numbers', () => {
    const l = layoutTrace(BRANCHING_HOPS);
    const e = l.edges.filter((x) => x.from === 'inventory' && x.to === 'orders-db');
    expect(e.length).toBe(1);
    expect(e[0].hopLabels).toEqual([4, 6]);
  });

  it('nodes get category "other" in trace mode — no per-node category travels with a hop', () => {
    // Unlike the old fintech id-map, ensemble's category comes only from a live Topology
    // fetch, which layoutTrace (a pure function of hops) never sees. Every node — real
    // service or synthetic "client" alike — renders as 'other'; TopologyView is where a
    // trace's nodes could be cross-referenced against the fetched topology if per-node
    // category ever mattered in trace mode.
    const l = layoutTrace(LINEAR_HOPS);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('edge-gateway').category).toBe('other');
    expect(at('orders-db').category).toBe('other');
  });

  it('marks only the synthetic "client" node, not real hop endpoints', () => {
    const l = layoutTrace(LINEAR_HOPS);
    const at = (id: string) => l.nodes.find((n) => n.id === id) as GraphNode;
    expect(at('client').synthetic).toBe(true);
    expect(at('edge-gateway').synthetic).toBeFalsy();
  });

  it('never emits clusters in trace mode', () => {
    expect(layoutTrace(BRANCHING_HOPS).clusters).toEqual([]);
  });

  it('lays out a single-hop trace without error', () => {
    const l = layoutTrace([LINEAR_HOPS[0]]);
    expect(l.nodes.length).toBe(2);
    expect(l.edges.length).toBe(1);
  });

  it('empty hops yield an empty layout rather than throwing', () => {
    const l = layoutTrace([]);
    expect(l.nodes).toEqual([]);
    expect(l.edges).toEqual([]);
    expect(l.width).toBe(0);
    expect(l.height).toBe(0);
  });

  it('keeps all geometry inside the viewBox — nothing can be silently clipped', () => {
    const cases: [string, Hop[]][] = [
      ['linear', LINEAR_HOPS],
      ['branching', BRANCHING_HOPS],
      // A same-column pair in the LAST column: the shape that used to route its lane past
      // the canvas edge and get clipped.
      [
        'last-column pair',
        [
          { ...LINEAR_HOPS[0], from: 'inventory', to: 'orders-db' },
          { ...LINEAR_HOPS[1], from: 'inventory', to: 'payments-db' },
          { ...LINEAR_HOPS[2], from: 'orders-db', to: 'payments-db' },
        ],
      ],
    ];
    for (const [name, hops] of cases) {
      const l = layoutTrace(hops);
      for (const n of l.nodes) {
        expect(l.width, `${name}: node ${n.id} exceeds width`).toBeGreaterThanOrEqual(n.x + n.w);
        expect(l.height, `${name}: node ${n.id} exceeds height`).toBeGreaterThanOrEqual(n.y + n.h);
      }
      for (const e of l.edges) {
        for (const p of e.points) {
          expect(p.x, `${name}: edge ${e.key} x`).toBeLessThanOrEqual(l.width);
          expect(p.y, `${name}: edge ${e.key} y`).toBeLessThanOrEqual(l.height);
          expect(p.x).toBeGreaterThanOrEqual(0);
          expect(p.y).toBeGreaterThanOrEqual(0);
        }
      }
    }
  });

  it('a same-column edge is a straight vertical, not a detour past the last column', () => {
    const l = layoutTrace([
      { ...LINEAR_HOPS[0], from: 'inventory', to: 'orders-db' },
      { ...LINEAR_HOPS[1], from: 'inventory', to: 'payments-db' },
      { ...LINEAR_HOPS[2], from: 'orders-db', to: 'payments-db' },
    ]);
    const e = l.edges.find((x) => x.from === 'orders-db' && x.to === 'payments-db');
    expect(e).toBeDefined();
    expect(e?.points.length).toBe(2);
    expect(e?.points[0].x).toBe(e?.points[1].x);
  });

  it('a cyclic trace terminates', () => {
    const l = layoutTrace([
      { ...LINEAR_HOPS[0], from: 'a', to: 'b' },
      { ...LINEAR_HOPS[1], from: 'b', to: 'a' },
    ]);
    expect(l.nodes.length).toBe(2);
  });
});
