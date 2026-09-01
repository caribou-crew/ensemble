import { describe, expect, it } from 'vitest';
import { formatWhen, parseRunIdStamp } from './when';

describe('formatWhen', () => {
  it('prefers a real ISO manifest timestamp', () => {
    const out = formatWhen('2026-08-31T13:27:29Z', '20260101T000000Z-abc');
    // Renders the ISO date (Aug 31), not the runId stamp (Jan 1).
    expect(out).toMatch(/Aug 31, 2026/);
  });

  it('treats the Go zero-value date as absent and falls back to the runId stamp', () => {
    // The bug: Date.parse("0001-01-01T00:00:00Z") succeeds (year 1), so a run
    // whose manifest never recorded finishedAt rendered as "Dec 31, 1". The
    // runId stamp is the truth here.
    const out = formatWhen('0001-01-01T00:00:00Z', '20260831T132729Z-6a540b1');
    expect(out).toMatch(/Aug 31, 2026/);
    expect(out).not.toMatch(/\b1\b(?!\d)/); // no year-1 leaking through
  });

  it('falls back to the runId stamp when there is no ISO at all', () => {
    expect(formatWhen(undefined, '20260831T132729Z-6a540b1')).toMatch(/Aug 31, 2026/);
    expect(formatWhen('', '20260831T132729Z-6a540b1')).toMatch(/Aug 31, 2026/);
  });

  it('shows the raw runId when neither the ISO nor the runId parses', () => {
    expect(formatWhen(undefined, 'not-a-stamp')).toBe('not-a-stamp');
    expect(formatWhen('', '')).toBe('—');
  });
});

describe('parseRunIdStamp', () => {
  it('parses the leading YYYYMMDDTHHMMSSZ', () => {
    expect(Number.isNaN(parseRunIdStamp('20260831T132729Z-6a540b1'))).toBe(false);
  });
  it('is NaN for a non-stamp', () => {
    expect(Number.isNaN(parseRunIdStamp('nope'))).toBe(true);
  });
});
