import { describe, expect, it } from 'vitest';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import HopTable, { buildRows } from './HopTable';
import type { Hop } from '../api/types';

// Reuses traceLayout's/hopTimeline's own nested-call shape (see
// hopTimeline.test.ts's "calls nested inside an open parent are depth+1")
// rather than inventing new ordering logic — buildRows just has to hand
// causalHopOrder/hopDepths the right per-chain slice.

function hop(seq: number, traceId: string, from: string | undefined, to: string, start: string, doneMs: number): Hop {
  return {
    schema: 'ensemble/1',
    seq,
    traceId,
    from,
    to,
    method: 'GET',
    path: `/x${seq}`,
    status: 200,
    t: { start, doneMs },
  };
}

describe('HopTable buildRows', () => {
  it('reorders a chain into causal order and indents nested calls', () => {
    // Deliberately out of causal order in the input array: hop3 (inventory,
    // starts later) sits before hop2 (orders, starts earlier) — both are
    // called by edge-gateway, so causalHopOrder must resolve the tie by
    // start time and put hop2 first.
    const hops: Hop[] = [
      hop(1, 't1', undefined, 'edge-gateway', '2026-01-01T00:00:00.000Z', 100),
      hop(3, 't1', 'edge-gateway', 'inventory', '2026-01-01T00:00:00.040Z', 20),
      hop(2, 't1', 'edge-gateway', 'orders', '2026-01-01T00:00:00.010Z', 20),
    ];

    const rows = buildRows(hops);
    expect(rows.map((r) => r.hop.seq)).toEqual([1, 2, 3]);
    expect(rows.map((r) => r.depth)).toEqual([0, 1, 1]);
    expect(rows.map((r) => r.chainStart)).toEqual([true, false, false]);
  });

  it('breaks the chain at a traceId boundary — a standalone hop is its own depth-0 chain', () => {
    const hops: Hop[] = [
      hop(1, 't1', undefined, 'edge-gateway', '2026-01-01T00:00:00.000Z', 100),
      hop(2, 't1', 'edge-gateway', 'orders', '2026-01-01T00:00:00.010Z', 20),
      hop(3, 't2', undefined, 'health-check', '2026-01-01T00:00:01.000Z', 5),
    ];

    const rows = buildRows(hops);
    expect(rows.map((r) => r.hop.seq)).toEqual([1, 2, 3]);
    expect(rows.map((r) => r.depth)).toEqual([0, 1, 0]);
    // The standalone hop starts a new chain of its own, distinct from the
    // preceding trace's chain.
    expect(rows[2].chainStart).toBe(true);
  });

  it('a hop with no traceId at all is always its own standalone chain', () => {
    const hops: Hop[] = [
      { schema: 'ensemble/1', seq: 1, to: 'health', method: 'GET', path: '/healthz', status: 200, t: { start: '2026-01-01T00:00:00.000Z', doneMs: 1 } },
      { schema: 'ensemble/1', seq: 2, to: 'health', method: 'GET', path: '/healthz', status: 200, t: { start: '2026-01-01T00:00:01.000Z', doneMs: 1 } },
    ];
    const rows = buildRows(hops);
    expect(rows.every((r) => r.depth === 0 && r.chainStart)).toBe(true);
  });

  it('renders rows in the reordered sequence with matching indent, via the component itself', () => {
    const hops: Hop[] = [
      hop(1, 't1', undefined, 'edge-gateway', '2026-01-01T00:00:00.000Z', 100),
      hop(3, 't1', 'edge-gateway', 'inventory', '2026-01-01T00:00:00.040Z', 20),
      hop(2, 't1', 'edge-gateway', 'orders', '2026-01-01T00:00:00.010Z', 20),
    ];
    const markup = renderToStaticMarkup(
      createElement(HopTable, { hops, selectedSeq: null, onSelectHop: () => {} }),
    );
    const order = [...markup.matchAll(/data-seq="(\d+)"/g)].map((m) => Number(m[1]));
    expect(order).toEqual([1, 2, 3]);
    const depths = [...markup.matchAll(/data-depth="(\d+)"/g)].map((m) => Number(m[1]));
    expect(depths).toEqual([0, 1, 1]);
    // Nested rows (depth > 0) carry the ↳ glyph; the root row does not.
    expect((markup.match(/hop-table__nest-glyph/g) ?? []).length).toBe(2);
  });
});
