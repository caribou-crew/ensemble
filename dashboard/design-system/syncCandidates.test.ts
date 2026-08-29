import { describe, expect, it } from 'vitest';
import { mergeCandidates, sinceParam } from './syncCandidates';

interface C {
  databaseId: number;
  createdAt: string;
}

describe('sinceParam', () => {
  it('is undefined for an empty list — a full, un-filtered fetch', () => {
    expect(sinceParam([])).toBeUndefined();
  });

  it('is a duration string covering back to the newest known createdAt, plus overlap', () => {
    const now = Date.parse('2026-08-28T23:10:00Z');
    const real = Date.now;
    Date.now = () => now;
    try {
      // newest known run is 5 minutes old (300s) — the overlap adds 60s.
      const p = sinceParam([{ createdAt: '2026-08-28T23:05:00Z' } as C]);
      expect(p).toBe('360s');
    } finally {
      Date.now = real;
    }
  });
});

describe('mergeCandidates', () => {
  it('keeps everything already known and adds what is new, newest first', () => {
    const existing: C[] = [
      { databaseId: 1, createdAt: '2026-08-28T20:00:00Z' },
      { databaseId: 2, createdAt: '2026-08-28T21:00:00Z' },
    ];
    const fresh: C[] = [{ databaseId: 3, createdAt: '2026-08-28T23:00:00Z' }];
    expect(mergeCandidates(existing, fresh).map((c) => c.databaseId)).toEqual([3, 2, 1]);
  });

  it('a fresh row with the same id overwrites the stale one — status can advance between refreshes', () => {
    const existing: C[] = [{ databaseId: 1, createdAt: '2026-08-28T20:00:00Z' }];
    const fresh: C[] = [{ databaseId: 1, createdAt: '2026-08-28T20:00:00Z' }];
    const merged = mergeCandidates(existing, fresh);
    expect(merged).toHaveLength(1);
    expect(merged[0]).toBe(fresh[0]);
  });
});
