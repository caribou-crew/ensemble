import { describe, expect, it } from 'vitest';
import { isRedactedValue, splitRedacted } from './redaction';

describe('splitRedacted', () => {
  it('marks a quoted [redacted] JSON string literal, quotes included', () => {
    const segs = splitRedacted('{"token":"[redacted]","ok":true}');
    expect(segs.filter((s) => s.redacted)).toEqual([{ text: '"[redacted]"', redacted: true }]);
  });

  it('marks a bare $enc:v1: token inside a larger string', () => {
    const segs = splitRedacted('Authorization: $enc:v1:abc123==');
    const redacted = segs.filter((s) => s.redacted);
    expect(redacted).toHaveLength(1);
    expect(redacted[0].text).toBe('$enc:v1:abc123==');
  });

  it('finds every occurrence, not just the first', () => {
    const segs = splitRedacted('{"a":"[redacted]","b":"[redacted]"}');
    expect(segs.filter((s) => s.redacted)).toHaveLength(2);
  });

  it('returns one non-redacted segment when nothing matches', () => {
    expect(splitRedacted('plain text, nothing to see')).toEqual([
      { text: 'plain text, nothing to see', redacted: false },
    ]);
  });
});

describe('isRedactedValue', () => {
  it('matches the exact literal and the encrypted-value prefix', () => {
    expect(isRedactedValue('[redacted]')).toBe(true);
    expect(isRedactedValue('$enc:v1:xyz')).toBe(true);
  });

  it('does not match a plain value, even one that merely contains the token', () => {
    expect(isRedactedValue('not [redacted] really')).toBe(false);
    expect(isRedactedValue('Bearer abc123')).toBe(false);
  });
});
