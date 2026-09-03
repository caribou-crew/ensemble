import { describe, expect, it } from 'vitest';
import { formatSyncedAt, isStale, STALE_THRESHOLD_MS } from './retraceWhen';

describe('isStale', () => {
  const now = Date.parse('2026-08-22T10:00:00Z');

  it('is false right at the threshold, true just past it', () => {
    const justUnder = new Date(now - STALE_THRESHOLD_MS + 1000).toISOString();
    const justOver = new Date(now - STALE_THRESHOLD_MS - 1000).toISOString();
    expect(isStale(justUnder, 'irrelevant-runid', now)).toBe(false);
    expect(isStale(justOver, 'irrelevant-runid', now)).toBe(true);
  });

  it('falls back to the runId stamp when iso is absent, same as formatWhen', () => {
    // 25h before `now`.
    expect(isStale(undefined, '20260821T090000Z-aaa', now)).toBe(true);
    // 1h before `now`.
    expect(isStale(undefined, '20260822T090000Z-aaa', now)).toBe(false);
  });

  it('is false rather than thrown when neither source parses — never crash the row over a display detail', () => {
    expect(isStale('not-a-date', 'not-a-runid-either', now)).toBe(false);
  });
});

describe('formatSyncedAt', () => {
  it('renders a real timestamp', () => {
    expect(formatSyncedAt('2026-08-21T10:12:00Z')).toContain('2026');
  });

  it('renders "—" for an absent syncedAt — a locally recorded run was never synced', () => {
    expect(formatSyncedAt(undefined)).toBe('—');
    expect(formatSyncedAt('')).toBe('—');
  });

  it('renders "—" for Go\'s zero time.Time, never the misleading "Dec 31, 1"', () => {
    expect(formatSyncedAt('0001-01-01T00:00:00Z')).toBe('—');
  });
});
