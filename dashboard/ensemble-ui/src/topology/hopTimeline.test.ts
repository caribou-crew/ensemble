import { describe, expect, it } from 'vitest';
import { heatTier, hopDepths, hopTimeline } from './hopTimeline';
import type { Hop } from '../api/types';

function hop(from: string | undefined, to: string, start: string, doneMs: number): Hop {
  return { schema: 'ensemble/1', seq: 0, from, to, method: 'GET', path: '/x', status: 200, t: { start, doneMs } };
}

describe('hopDepths', () => {
  it('the root call is depth 0', () => {
    const hops = [hop(undefined, 'edge-gateway', '2026-01-01T00:00:00.000Z', 50)];
    expect(hopDepths(hops)).toEqual([0]);
  });

  it('calls nested inside an open parent are depth+1', () => {
    const hops = [
      hop('edge-gateway', 'orders', '2026-01-01T00:00:00.000Z', 100),
      hop('orders', 'orders-db', '2026-01-01T00:00:00.010Z', 20),
      hop('orders', 'inventory', '2026-01-01T00:00:00.040Z', 20),
    ];
    expect(hopDepths(hops)).toEqual([0, 1, 1]);
  });

  it('sequential sibling calls from the same node share depth, not stacked', () => {
    const hops = [
      hop(undefined, 'a', '2026-01-01T00:00:00.000Z', 100),
      hop('a', 'b', '2026-01-01T00:00:00.010Z', 20),
      hop('a', 'c', '2026-01-01T00:00:00.040Z', 20),
    ];
    expect(hopDepths(hops)).toEqual([0, 1, 1]);
  });

  it('an unrelated caller after a parent closes resets to root', () => {
    const hops = [
      hop(undefined, 'a', '2026-01-01T00:00:00.000Z', 10),
      hop('a', 'b', '2026-01-01T00:00:00.001Z', 5),
      hop(undefined, 'd', '2026-01-01T00:00:00.020Z', 10),
    ];
    expect(hopDepths(hops)).toEqual([0, 1, 0]);
  });

  it('array order need not match start-time order', () => {
    const hops = [
      hop('orders', 'orders-db', '2026-01-01T00:00:00.010Z', 20),
      hop('edge-gateway', 'orders', '2026-01-01T00:00:00.000Z', 100),
    ];
    expect(hopDepths(hops)).toEqual([1, 0]);
  });

  it('falls back to firstByteMs when doneMs is absent (still-open upstream)', () => {
    const hops: Hop[] = [
      { schema: 'ensemble/1', seq: 0, from: undefined, to: 'a', t: { start: '2026-01-01T00:00:00.000Z', firstByteMs: 5 } },
      { schema: 'ensemble/1', seq: 1, from: 'a', to: 'b', t: { start: '2026-01-01T00:00:00.001Z' } },
    ];
    expect(hopDepths(hops)).toEqual([0, 1]);
  });
});

describe('hopTimeline', () => {
  it('sequential hops occupy disjoint ranges', () => {
    const hops = [
      hop('a', 'b', '2026-01-01T00:00:00.000Z', 100),
      hop('b', 'c', '2026-01-01T00:00:00.100Z', 100),
    ];
    const [first, second] = hopTimeline(hops);
    expect(first.startPct).toBe(0);
    expect(first.startPct + first.widthPct).toBeLessThanOrEqual(second.startPct + 0.001);
  });

  it('parallel hops overlap in the same window', () => {
    const hops = [
      hop('orders', 'orders-db', '2026-01-01T00:00:00.000Z', 100),
      hop('orders', 'inventory', '2026-01-01T00:00:00.010Z', 80),
    ];
    const [first, second] = hopTimeline(hops);
    expect(first.startPct).toBeLessThan(second.startPct);
    expect(first.startPct + first.widthPct).toBeGreaterThan(second.startPct);
  });

  it('a single hop spans the full width', () => {
    const hops = [hop('a', 'b', '2026-01-01T00:00:00.000Z', 50)];
    const [only] = hopTimeline(hops);
    expect(only.startPct).toBe(0);
    expect(only.widthPct).toBe(100);
    expect(only.heat).toBe(1);
  });

  it('heat is relative to the slowest hop in the trace', () => {
    const hops = [
      hop('a', 'b', '2026-01-01T00:00:00.000Z', 100),
      hop('b', 'c', '2026-01-01T00:00:00.100Z', 25),
    ];
    const [slow, fast] = hopTimeline(hops);
    expect(slow.heat).toBe(1);
    expect(fast.heat).toBe(0.25);
  });

  it('empty input returns empty', () => {
    expect(hopTimeline([])).toEqual([]);
  });

  it('a hop still in flight (no doneMs) contributes zero duration, not NaN', () => {
    const hops: Hop[] = [{ schema: 'ensemble/1', seq: 0, from: 'a', to: 'b', t: { start: '2026-01-01T00:00:00.000Z' } }];
    const [only] = hopTimeline(hops);
    expect(Number.isFinite(only.startPct)).toBe(true);
    expect(Number.isFinite(only.widthPct)).toBe(true);
    expect(Number.isFinite(only.heat)).toBe(true);
  });
});

describe('heatTier', () => {
  it('buckets into normal/warm/hot at fixed thresholds', () => {
    expect(heatTier(0)).toBe('normal');
    expect(heatTier(0.49)).toBe('normal');
    expect(heatTier(0.5)).toBe('warm');
    expect(heatTier(0.84)).toBe('warm');
    expect(heatTier(0.85)).toBe('hot');
    expect(heatTier(1)).toBe('hot');
  });
});
