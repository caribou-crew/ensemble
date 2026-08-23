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

  it('reorders a same-trace pair even when unrelated concurrent traffic lands between their recorded positions', () => {
    // Mirrors a gateway request's real recording order: the inner leg
    // (gateway -> bff) completes and gets Recorded first, the outer leg
    // (client -> gateway) only completes — and gets Recorded — once the
    // inner one has returned (see core/proxy/proxy.go). An unrelated
    // concurrent request (health-check, seq 2) completes in between,
    // breaking strict array-adjacency for the trace's own two hops.
    const hops: Hop[] = [
      hop(1, 't1', 'gateway', 'bff', '2026-01-01T00:00:00.010Z', 5),
      hop(2, 't2', undefined, 'health-check', '2026-01-01T00:00:00.015Z', 1),
      hop(3, 't1', undefined, 'gateway', '2026-01-01T00:00:00.000Z', 20),
    ];

    const rows = buildRows(hops);
    // client -> gateway (seq 3) is causally first even though it was
    // recorded last; the unrelated health-check (seq 2) keeps its own
    // place, not spliced into the middle of the reordered chain.
    expect(rows.map((r) => r.hop.seq)).toEqual([3, 1, 2]);
    expect(rows.map((r) => r.depth)).toEqual([0, 1, 0]);
    expect(rows.map((r) => r.chainStart)).toEqual([true, false, true]);
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
