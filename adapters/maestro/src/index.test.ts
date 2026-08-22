import { describe, expect, it } from 'vitest';
import { MISSING_HANDSHAKE_MESSAGE } from '@caribou-crew/retrace-js';
import { markerRequest } from './index.js';

describe('markerRequest', () => {
  it('builds a start marker request', () => {
    const req = markerRequest(['group', 'checkout'], { RETRACE_MARKER_URL: 'http://127.0.0.1:9999' });
    expect(req).toEqual({ url: 'http://127.0.0.1:9999/group', body: JSON.stringify({ name: 'checkout' }) });
  });

  it('builds an end marker request', () => {
    const req = markerRequest(['group', '--end'], { RETRACE_MARKER_URL: 'http://127.0.0.1:9999' });
    expect(req).toEqual({ url: 'http://127.0.0.1:9999/group/end', body: '{}' });
  });

  it('returns null (a silent no-op) outside a run when strict is off', () => {
    expect(markerRequest(['group', 'checkout'], {})).toBeNull();
  });

  // F-6 (task-17-review.md): assert against the imported constant itself,
  // not a loose /no active run/ regex — this enforces the "all three
  // packages say the same thing" invariant handshake.ts's own comment
  // claims, catching a local re-derivation that merely happened to still
  // mention "no active run".
  it('throws the handshake message in strict mode', () => {
    expect(() => markerRequest(['group', 'checkout'], { RETRACE_STRICT: '1' })).toThrow(MISSING_HANDSHAKE_MESSAGE);
  });

  // R-AE: pin the rejection — a bad group name must never reach the marker
  // door at all, on the CLI path just as much as the file/HTTP paths in
  // @caribou-crew/retrace-js.
  it('throws on a group name that is not a safe path component', () => {
    expect(() => markerRequest(['group', 'cart/item'], { RETRACE_MARKER_URL: 'http://127.0.0.1:9999' })).toThrow(
      /invalid group name/,
    );
  });

  // R-AD: an unrecognised RETRACE_STRICT value is a loud error here too —
  // markerRequest reuses @caribou-crew/retrace-js's handshake(), so this
  // pins that the sharing actually happened rather than a local re-parse.
  it('throws on an unrecognised RETRACE_STRICT value', () => {
    expect(() => markerRequest(['group', 'checkout'], { RETRACE_STRICT: 'enabled' })).toThrow(/RETRACE_STRICT/);
  });

  it('rejects an unknown command', () => {
    expect(() => markerRequest(['end'], {})).toThrow(/unknown command/);
  });

  it('rejects "group" with neither a name nor --end', () => {
    expect(() => markerRequest(['group'], {})).toThrow(/requires a name/);
  });
});
