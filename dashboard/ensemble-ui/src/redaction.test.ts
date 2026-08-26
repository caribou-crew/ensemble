import { describe, expect, it } from 'vitest';
import { isRedactedValue, redactedTitle, splitRedacted } from './redaction';

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

describe('redactedTitle', () => {
  it('tells a masked value it is gone, not merely hidden', () => {
    const title = redactedTitle('[redacted]');
    expect(title).toMatch(/does not contain it/);
    expect(title).toMatch(/nothing here to reveal/);
  });

  it('tells an encrypted value it is present but keyless', () => {
    expect(redactedTitle('$enc:v1:abc==')).toMatch(/encrypted at capture/);
  });

  it('reads the encrypted marker through the quotes a JSON body adds', () => {
    // splitRedacted hands back '"$enc:v1:abc=="' with the quotes attached,
    // so a startsWith() check here would silently fall through to the
    // destroyed-value wording and tell the user a recoverable value is gone.
    expect(redactedTitle('"$enc:v1:abc=="')).toMatch(/encrypted at capture/);
  });

  it('never names an internal task number in user-facing copy', () => {
    // The bug this replaced: both call sites shipped title="revealed in
    // task 4.8" — a tracker id leaked into the UI, promising a reveal that
    // a destroyed value can never receive.
    for (const marker of ['[redacted]', '"[redacted]"', '$enc:v1:abc==']) {
      expect(redactedTitle(marker)).not.toMatch(/task \d/i);
    }
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
