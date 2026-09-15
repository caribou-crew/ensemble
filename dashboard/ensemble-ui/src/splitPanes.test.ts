import { describe, expect, it } from 'vitest';
import { splitPanes } from './splitPanes';
import { buildClientIndex, UNATTRIBUTED_CLIENT } from './clientAttribution';
import type { Hop } from './api/types';

function hop(seq: number, start: string, extra: Partial<Hop> = {}): Hop {
  return { seq, to: 'svc', t: { start }, ...extra } as Hop;
}

const T = (n: number) => `2026-09-14T10:00:${String(n).padStart(2, '0')}.000Z`;

describe('splitPanes', () => {
  it('interleaves both panes on one shared order', () => {
    const hops = [
      hop(1, T(1), { traceId: 'a', client: 'app-legacy' }),
      hop(2, T(2), { traceId: 'b', client: 'app-next' }),
      hop(3, T(3), { traceId: 'a' }),
    ];
    const { rows } = splitPanes(hops, buildClientIndex(hops), 'app-legacy', 'app-next');
    expect(rows.map((r) => [r.left?.seq ?? null, r.right?.seq ?? null])).toEqual([
      [1, null],
      [null, 2],
      [3, null],
    ]);
  });

  it('leaves the opposite column empty for a call only one client makes', () => {
    const hops = [
      hop(1, T(1), { traceId: 'a', client: 'app-legacy', to: 'wallet' }),
      hop(2, T(2), { traceId: 'b', client: 'app-next', to: 'edge' }),
    ];
    const { rows } = splitPanes(hops, buildClientIndex(hops), 'app-legacy', 'app-next');
    expect(rows).toHaveLength(2);
    expect(rows[0].right).toBeNull();
    expect(rows[1].left).toBeNull();
  });

  // The tie-break must be seq, not the input array's order. The input here
  // is deliberately in the REVERSE of seq order at an identical timestamp,
  // so an implementation that leans on sort stability alone fails.
  it('tie-breaks an identical start time by seq, not input order', () => {
    const hops = [
      hop(9, T(1), { traceId: 'a', client: 'app-legacy' }),
      hop(4, T(1), { traceId: 'b', client: 'app-next' }),
    ];
    const { rows } = splitPanes(hops, buildClientIndex(hops), 'app-legacy', 'app-next');
    expect(rows.map((r) => r.key)).toEqual([4, 9]);
  });

  it('withholds hops belonging to neither pane', () => {
    const hops = [
      hop(1, T(1), { traceId: 'a', client: 'app-legacy' }),
      hop(2, T(2), { traceId: 'c', client: 'app-third' }),
      hop(3, T(3), { traceId: 'd' }),
    ];
    const { rows, withheld } = splitPanes(hops, buildClientIndex(hops), 'app-legacy', 'app-next');
    expect(rows).toHaveLength(1);
    expect(withheld.map((h) => h.seq)).toEqual([2, 3]);
  });

  it('puts an unattributed hop in neither pane', () => {
    const hops = [hop(1, T(1), { traceId: 'a', client: 'app-legacy' }), hop(2, T(2), { traceId: 'z' })];
    const { rows, withheld } = splitPanes(hops, buildClientIndex(hops), 'app-legacy', 'app-next');
    expect(rows.every((r) => r.right === null)).toBe(true);
    expect(withheld.map((h) => h.seq)).toEqual([2]);
  });

  it('can put the unattributed bucket in a pane when asked for explicitly', () => {
    const hops = [hop(1, T(1), { traceId: 'a', client: 'app-legacy' }), hop(2, T(2), { traceId: 'z' })];
    const { rows, withheld } = splitPanes(hops, buildClientIndex(hops), 'app-legacy', UNATTRIBUTED_CLIENT);
    expect(rows.map((r) => [r.left?.seq ?? null, r.right?.seq ?? null])).toEqual([
      [1, null],
      [null, 2],
    ]);
    expect(withheld).toEqual([]);
  });

  it('resolves a downstream hop into its client pane', () => {
    const hops = [
      hop(1, T(1), { traceId: 'b', client: 'app-next', to: 'edge' }),
      hop(2, T(2), { traceId: 'b', to: 'toolkit-api', status: 404 }),
    ];
    const { rows } = splitPanes(hops, buildClientIndex(hops), 'app-legacy', 'app-next');
    expect(rows.map((r) => r.right?.seq ?? null)).toEqual([1, 2]);
  });

  it('orders by start time even when seq order disagrees', () => {
    const hops = [
      hop(1, T(5), { traceId: 'a', client: 'app-legacy' }),
      hop(2, T(2), { traceId: 'b', client: 'app-next' }),
    ];
    const { rows } = splitPanes(hops, buildClientIndex(hops), 'app-legacy', 'app-next');
    expect(rows.map((r) => r.key)).toEqual([2, 1]);
  });
});
