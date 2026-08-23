import { describe, expect, it } from 'vitest';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import HopTable from './HopTable';
import type { Hop } from '../api/types';

describe('HopTable time and size columns', () => {
  it('renders HH:MM:SS:mmm from t.start in the local time zone', () => {
    const hop: Hop = {
      schema: 'ensemble/1',
      seq: 1,
      to: 'catalog',
      method: 'GET',
      path: '/products',
      status: 200,
      t: { start: '2026-01-01T14:32:07.123Z', doneMs: 5 },
    };
    const markup = renderToStaticMarkup(
      createElement(HopTable, { hops: [hop], selectedSeq: null, onSelectHop: () => {} }),
    );
    const d = new Date('2026-01-01T14:32:07.123Z');
    const pad = (n: number, len = 2) => String(n).padStart(len, '0');
    const want = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}:${pad(d.getMilliseconds(), 3)}`;
    expect(markup).toContain(`>${want}<`);
  });

  it('renders combined request+response payload size in bytes/KB', () => {
    const hop: Hop = {
      schema: 'ensemble/1',
      seq: 1,
      to: 'catalog',
      method: 'POST',
      path: '/products',
      status: 200,
      t: { start: '2026-01-01T00:00:00.000Z', doneMs: 5 },
      req: { body: 'x'.repeat(500) },
      resp: { body: 'y'.repeat(524) },
    };
    const markup = renderToStaticMarkup(
      createElement(HopTable, { hops: [hop], selectedSeq: null, onSelectHop: () => {} }),
    );
    // 500 + 524 = 1024 bytes exactly = 1.0KB.
    expect(markup).toContain('>1.0KB<');
  });

  it('flags a truncated payload rather than reporting it as the true wire size', () => {
    const hop: Hop = {
      schema: 'ensemble/1',
      seq: 1,
      to: 'catalog',
      method: 'POST',
      path: '/products',
      status: 200,
      t: { start: '2026-01-01T00:00:00.000Z', doneMs: 5 },
      req: { body: 'x'.repeat(10), truncated: true },
    };
    const markup = renderToStaticMarkup(
      createElement(HopTable, { hops: [hop], selectedSeq: null, onSelectHop: () => {} }),
    );
    expect(markup).toContain('>10B+<');
  });

  it('counts a multi-byte UTF-8 body by its real byte size, not its character count', () => {
    // Each '€' is 1 UTF-16 code unit but 3 UTF-8 bytes — .length would
    // under-report the wire size for anything outside ASCII.
    const hop: Hop = {
      schema: 'ensemble/1',
      seq: 1,
      to: 'catalog',
      method: 'POST',
      path: '/products',
      status: 200,
      t: { start: '2026-01-01T00:00:00.000Z', doneMs: 5 },
      req: { body: '€€€€' }, // 4 chars, 12 bytes
    };
    const markup = renderToStaticMarkup(
      createElement(HopTable, { hops: [hop], selectedSeq: null, onSelectHop: () => {} }),
    );
    expect(markup).toContain('>12B<');
  });

  it('marks a config-inferred caller visually distinct from a real, trace-derived one', () => {
    const inferred: Hop = {
      schema: 'ensemble/1',
      seq: 1,
      from: 'bff',
      attribution: 'inferred',
      to: 'backend',
      t: { start: '2026-01-01T00:00:00.000Z' },
    };
    const real: Hop = {
      schema: 'ensemble/1',
      seq: 2,
      from: 'bff',
      to: 'other-backend',
      t: { start: '2026-01-01T00:00:01.000Z' },
    };
    const markup = renderToStaticMarkup(
      createElement(HopTable, { hops: [inferred, real], selectedSeq: null, onSelectHop: () => {} }),
    );
    // Exactly the inferred hop's row gets the marker class — the real one must not.
    expect(markup.match(/hop-table__caller--inferred/g)?.length).toBe(1);
  });
});
