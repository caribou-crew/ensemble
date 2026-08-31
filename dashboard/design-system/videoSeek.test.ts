import { describe, expect, it } from 'vitest';
import { checkpointVideoOffsetSeconds } from './videoSeek';

describe('checkpointVideoOffsetSeconds', () => {
  it('is the elapsed seconds from run start to the checkpoint', () => {
    const got = checkpointVideoOffsetSeconds('2026-08-31T10:00:12.500Z', '2026-08-31T10:00:00Z');
    expect(got).toBe(12.5);
  });

  it('is null when the checkpoint carries no timestamp — an old manifest, not a bogus zero offset', () => {
    expect(checkpointVideoOffsetSeconds('', '2026-08-31T10:00:00Z')).toBeNull();
  });

  it('is null when the run carries no startedAt', () => {
    expect(checkpointVideoOffsetSeconds('2026-08-31T10:00:12Z', '')).toBeNull();
  });

  it('is null for an unparseable timestamp rather than throwing', () => {
    expect(checkpointVideoOffsetSeconds('not-a-date', '2026-08-31T10:00:00Z')).toBeNull();
  });

  it('is null when the checkpoint predates the run start — clock skew, not a real offset', () => {
    expect(checkpointVideoOffsetSeconds('2026-08-31T09:59:59Z', '2026-08-31T10:00:00Z')).toBeNull();
  });

  it('is zero when the checkpoint lands exactly at run start', () => {
    expect(checkpointVideoOffsetSeconds('2026-08-31T10:00:00Z', '2026-08-31T10:00:00Z')).toBe(0);
  });
});
