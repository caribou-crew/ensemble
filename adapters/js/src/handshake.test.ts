import { describe, expect, it } from 'vitest';
import { handshake, requireHandshake, MISSING_HANDSHAKE_MESSAGE } from './handshake.js';
import { group } from './index.js';

describe('handshake', () => {
  it('reads all four variables', () => {
    const h = handshake({
      RETRACE_RUN_DIR: '/tmp/a-run',
      RETRACE_PROXY_URL: 'http://127.0.0.1:1',
      RETRACE_MARKER_URL: 'http://127.0.0.1:2',
      RETRACE_UPSTREAM_URL: 'http://127.0.0.1:3',
    });
    expect(h.runDir).toBe('/tmp/a-run');
    expect(h.proxyUrl).toBe('http://127.0.0.1:1');
    expect(h.markerUrl).toBe('http://127.0.0.1:2');
    expect(h.upstreamUrl).toBe('http://127.0.0.1:3');
  });

  it('treats an absent env as fully empty, not a run', () => {
    const h = handshake({});
    expect(h).toEqual({ runDir: null, proxyUrl: null, markerUrl: null, upstreamUrl: null, strict: false });
  });

  // upstreamUrl is conditional (design.md §6.1.2): a capture session with no
  // configured upstream (e.g. attached mode against a flow with no
  // URL-bound auth) omits it, and that must read as null, not "".
  it('treats upstreamUrl as absent when the session had nothing to point at', () => {
    const h = handshake({ RETRACE_RUN_DIR: '/tmp/a-run', RETRACE_PROXY_URL: 'http://127.0.0.1:1' });
    expect(h.upstreamUrl).toBeNull();
  });

  it('reports strict from RETRACE_STRICT=1', () => {
    expect(handshake({ RETRACE_STRICT: '1' }).strict).toBe(true);
    expect(handshake({}).strict).toBe(false);
  });

  // R-AD: RETRACE_STRICT is a small explicit set, not a single literal — a
  // careful user typing "true" must turn strict mode ON, not off. This pins
  // against the `=== '1'` implementation the ruling names as the trap.
  it('accepts true/yes/on and 0/false/no/off, case-insensitively, as well as 1/0', () => {
    for (const v of ['true', 'YES', 'On', '1']) {
      expect(handshake({ RETRACE_STRICT: v }).strict, `RETRACE_STRICT=${v}`).toBe(true);
    }
    for (const v of ['false', 'NO', 'Off', '0', '']) {
      expect(handshake({ RETRACE_STRICT: v }).strict, `RETRACE_STRICT=${v}`).toBe(false);
    }
  });

  // R-AD's third outcome: an unrecognised value must be a loud error, never
  // a silent "not strict" — that would defeat the entire point of the
  // switch, and a value the parser doesn't recognise is exactly the case a
  // careful-but-wrong user produces.
  it('throws on an unrecognised RETRACE_STRICT value instead of silently treating it as not strict', () => {
    expect(() => handshake({ RETRACE_STRICT: 'enabled' })).toThrow(/RETRACE_STRICT/);
    expect(() => handshake({ RETRACE_STRICT: 'enabled' })).toThrow(/enabled/);
  });

  it('is a no-op outside a run when strict is off', async () => {
    const saved = process.env.RETRACE_RUN_DIR;
    const savedMarker = process.env.RETRACE_MARKER_URL;
    const savedStrict = process.env.RETRACE_STRICT;
    delete process.env.RETRACE_RUN_DIR;
    delete process.env.RETRACE_MARKER_URL;
    delete process.env.RETRACE_STRICT;
    try {
      // group('x') with an empty env resolves without throwing and writes
      // nothing — the contract that lets a test suite run normally outside
      // retrace.
      await expect(group('x')).resolves.toBeUndefined();
    } finally {
      if (saved !== undefined) process.env.RETRACE_RUN_DIR = saved;
      if (savedMarker !== undefined) process.env.RETRACE_MARKER_URL = savedMarker;
      if (savedStrict !== undefined) process.env.RETRACE_STRICT = savedStrict;
    }
  });

  it('throws MISSING_HANDSHAKE_MESSAGE in strict mode', () => {
    expect(() => requireHandshake({ RETRACE_STRICT: '1' })).toThrow(MISSING_HANDSHAKE_MESSAGE);
  });

  // F-6 (task-17-review.md): the test above compares the thrown message to
  // the very constant it throws, so it cannot fail no matter what that
  // constant says — it would still pass if MISSING_HANDSHAKE_MESSAGE lost
  // the spec's "explaining how to invoke retrace" half entirely. This pins
  // that half's actual content directly, independent of the constant.
  it('MISSING_HANDSHAKE_MESSAGE explains how to invoke retrace, per the spec', () => {
    expect(MISSING_HANDSHAKE_MESSAGE).toMatch(/retrace run --flow <name> -- <your test command>/);
    expect(MISSING_HANDSHAKE_MESSAGE).toMatch(/unset RETRACE_STRICT/);
  });

  it('does not throw in strict mode when a marker URL alone is present', () => {
    expect(() => requireHandshake({ RETRACE_STRICT: '1', RETRACE_MARKER_URL: 'http://127.0.0.1:1' })).not.toThrow();
  });
});
