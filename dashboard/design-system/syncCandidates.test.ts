import { describe, expect, it } from 'vitest';
import { mergeCandidates, pickLatestPerWorkflow, repoFromRunUrl, sinceParam } from './syncCandidates';

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

interface W {
  databaseId: number;
  createdAt: string;
  workflowName: string;
  hasArtifacts: boolean;
  conclusion: string;
}
const w = (over: Partial<W> = {}): W => ({
  databaseId: 1,
  createdAt: '2026-08-28T20:00:00Z',
  workflowName: 'e2e',
  hasArtifacts: true,
  conclusion: 'success',
  ...over,
});

describe('pickLatestPerWorkflow', () => {
  it('picks the freshest candidate per workflow name, one lane per workflow', () => {
    const picks = pickLatestPerWorkflow([
      w({ databaseId: 1, workflowName: 'e2e', createdAt: '2026-08-28T20:00:00Z' }),
      w({ databaseId: 2, workflowName: 'e2e', createdAt: '2026-08-28T22:00:00Z' }), // newer, same lane
      w({ databaseId: 3, workflowName: 'unit', createdAt: '2026-08-28T21:00:00Z' }),
    ]);
    expect(picks.map((c) => c.databaseId).sort()).toEqual([2, 3]);
  });

  it('skips a candidate with nothing to pull — it cannot contribute a lane', () => {
    const picks = pickLatestPerWorkflow([w({ databaseId: 1, hasArtifacts: false })]);
    expect(picks).toHaveLength(0);
  });

  it('skips a failed run even with artifacts — it has logs but no replay bundle to pull', () => {
    const picks = pickLatestPerWorkflow([w({ databaseId: 1, conclusion: 'failure', hasArtifacts: true })]);
    expect(picks).toHaveLength(0);
  });

  it('a newer FAILED run does not shadow an older SUCCESS in the same lane', () => {
    const picks = pickLatestPerWorkflow([
      w({ databaseId: 1, workflowName: 'e2e', createdAt: '2026-08-28T20:00:00Z', conclusion: 'success' }),
      w({ databaseId: 2, workflowName: 'e2e', createdAt: '2026-08-28T22:00:00Z', conclusion: 'failure' }), // newer but failed
    ]);
    expect(picks.map((c) => c.databaseId)).toEqual([1]);
  });

  it('returns nothing for an empty candidate list', () => {
    expect(pickLatestPerWorkflow([])).toEqual([]);
  });
});

describe('repoFromRunUrl', () => {
  it('extracts owner/repo from a GitHub Actions run URL', () => {
    expect(repoFromRunUrl('https://github.com/acme/widgets/actions/runs/12345')).toBe('acme/widgets');
  });

  it('extracts owner/repo even with a trailing /attempts/N', () => {
    expect(repoFromRunUrl('https://github.com/acme/widgets/actions/runs/12345/attempts/2')).toBe('acme/widgets');
  });

  it('is null for anything that is not a GitHub Actions run URL — a defensive parse failure, not a path this app expects', () => {
    expect(repoFromRunUrl('https://example.com/not-github')).toBeNull();
    expect(repoFromRunUrl('')).toBeNull();
  });
});
