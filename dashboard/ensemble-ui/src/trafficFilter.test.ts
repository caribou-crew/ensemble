import { describe, expect, it } from 'vitest';
import {
  fieldSuggestions,
  formatFilterToken,
  hopMatchesQuery,
  hopPayloadBytes,
  isComparisonOnlyField,
  matchesToken,
  parseFilterToken,
  valueSuggestions,
} from './trafficFilter';
import type { Hop } from './api/types';

function hop(overrides: Partial<Hop> = {}): Hop {
  return {
    schema: 'hop.v1',
    seq: 1,
    to: 'catalog',
    method: 'GET',
    path: '/v1/products',
    status: 200,
    t: { start: '2026-01-01T00:00:00.000Z', doneMs: 42 },
    ...overrides,
  };
}

describe('parseFilterToken', () => {
  it('parses colon fields', () => {
    expect(parseFilterToken('status:200')).toEqual({ field: 'status', op: ':', value: '200' });
    expect(parseFilterToken('method:GET')).toEqual({ field: 'method', op: ':', value: 'GET' });
    expect(parseFilterToken('path:/v1/internal')).toEqual({ field: 'path', op: ':', value: '/v1/internal' });
    expect(parseFilterToken('session:a1b2c3d4')).toEqual({ field: 'session', op: ':', value: 'a1b2c3d4' });
  });

  it('parses comparison fields without a colon, longest operator first', () => {
    expect(parseFilterToken('size>10kb')).toEqual({ field: 'size', op: '>', value: '10kb' });
    expect(parseFilterToken('done<100ms')).toEqual({ field: 'done', op: '<', value: '100ms' });
    expect(parseFilterToken('size>=1mb')).toEqual({ field: 'size', op: '>=', value: '1mb' });
    expect(parseFilterToken('done<=50')).toEqual({ field: 'done', op: '<=', value: '50' });
  });

  it('is case-insensitive on the field name', () => {
    expect(parseFilterToken('STATUS:200')).toEqual({ field: 'status', op: ':', value: '200' });
  });

  it('returns null for plain free text', () => {
    expect(parseFilterToken('checkout')).toBeNull();
    expect(parseFilterToken('')).toBeNull();
    expect(parseFilterToken('notafield:x')).toBeNull();
  });

  it('formats a token back to its original query text', () => {
    expect(formatFilterToken({ field: 'status', op: ':', value: '200' })).toBe('status:200');
    expect(formatFilterToken({ field: 'size', op: '>', value: '10kb' })).toBe('size>10kb');
  });
});

describe('matchesToken', () => {
  it('matches status by exact code or bucket', () => {
    const h = hop({ status: 404 });
    expect(matchesToken(h, { field: 'status', op: ':', value: '404' })).toBe(true);
    expect(matchesToken(h, { field: 'status', op: ':', value: '200' })).toBe(false);
    expect(matchesToken(h, { field: 'status', op: ':', value: '4xx' })).toBe(true);
    expect(matchesToken(h, { field: 'status', op: ':', value: '5xx' })).toBe(false);
  });

  it('matches status comparisons', () => {
    const h = hop({ status: 404 });
    expect(matchesToken(h, { field: 'status', op: '>=', value: '400' })).toBe(true);
    expect(matchesToken(h, { field: 'status', op: '<', value: '400' })).toBe(false);
  });

  it('matches method case-insensitively and exactly', () => {
    const h = hop({ method: 'POST' });
    expect(matchesToken(h, { field: 'method', op: ':', value: 'post' })).toBe(true);
    expect(matchesToken(h, { field: 'method', op: ':', value: 'GET' })).toBe(false);
  });

  it('matches path as a substring', () => {
    const h = hop({ path: '/v1/internal/widgets' });
    expect(matchesToken(h, { field: 'path', op: ':', value: 'internal' })).toBe(true);
    expect(matchesToken(h, { field: 'path', op: ':', value: 'nope' })).toBe(false);
  });

  it('matches session as a prefix', () => {
    const h = hop({ session: 'a1b2c3d4-ffff' });
    expect(matchesToken(h, { field: 'session', op: ':', value: 'a1b2c3d4' })).toBe(true);
    expect(matchesToken(h, { field: 'session', op: ':', value: 'ffff' })).toBe(false);
  });

  it('matches size comparisons in bytes/kb/mb', () => {
    const h = hop({ req: { body: 'x'.repeat(2000) } }); // 2000 bytes
    expect(matchesToken(h, { field: 'size', op: '>', value: '1kb' })).toBe(true);
    expect(matchesToken(h, { field: 'size', op: '<', value: '1kb' })).toBe(false);
    expect(matchesToken(h, { field: 'size', op: '<', value: '1mb' })).toBe(true);
  });

  it('matches done comparisons in ms/s, and never matches a hop with no doneMs', () => {
    const h = hop({ t: { start: '2026-01-01T00:00:00.000Z', doneMs: 250 } });
    expect(matchesToken(h, { field: 'done', op: '>', value: '100ms' })).toBe(true);
    expect(matchesToken(h, { field: 'done', op: '<', value: '1s' })).toBe(true);
    expect(matchesToken(h, { field: 'done', op: '>', value: '1s' })).toBe(false);

    const pending = hop({ t: { start: '2026-01-01T00:00:00.000Z' } });
    expect(matchesToken(pending, { field: 'done', op: '>', value: '0' })).toBe(false);
  });

  it('never matches an unparseable comparison value', () => {
    expect(matchesToken(hop(), { field: 'size', op: '>', value: 'not-a-size' })).toBe(false);
  });
});

describe('hopMatchesQuery', () => {
  it('ANDs across distinct fields, ORs within the same field', () => {
    const notFound = hop({ seq: 1, status: 404, method: 'GET' });
    const serverErr = hop({ seq: 2, status: 500, method: 'POST' });
    const ok = hop({ seq: 3, status: 200, method: 'GET' });

    const tokens = [
      { field: 'status', op: ':', value: '404' } as const,
      { field: 'status', op: ':', value: '500' } as const,
    ];
    expect(hopMatchesQuery(notFound, [...tokens], '')).toBe(true);
    expect(hopMatchesQuery(serverErr, [...tokens], '')).toBe(true);
    expect(hopMatchesQuery(ok, [...tokens], '')).toBe(false);

    const andTokens = [
      { field: 'status', op: ':', value: '4xx' } as const,
      { field: 'method', op: ':', value: 'POST' } as const,
    ];
    expect(hopMatchesQuery(notFound, andTokens, '')).toBe(false); // 4xx but GET
    expect(hopMatchesQuery(serverErr, andTokens, '')).toBe(false); // POST but 5xx
    const notFoundPost = hop({ status: 404, method: 'POST' });
    expect(hopMatchesQuery(notFoundPost, andTokens, '')).toBe(true);
  });

  it('ANDs free text as a substring on method/path/route, alongside tokens', () => {
    const h = hop({ to: 'catalog', from: 'gateway', path: '/v1/products', status: 200 });
    expect(hopMatchesQuery(h, [], 'catalog')).toBe(true);
    expect(hopMatchesQuery(h, [], 'products')).toBe(true);
    expect(hopMatchesQuery(h, [], 'nope')).toBe(false);
    expect(hopMatchesQuery(h, [{ field: 'status', op: ':', value: '200' }], 'nope')).toBe(false);
  });
});

describe('autocomplete helpers', () => {
  it('suggests field names by prefix, including an exact match so Tab can still append the colon', () => {
    expect(fieldSuggestions('st')).toEqual(['status']);
    expect(fieldSuggestions('status')).toEqual(['status']);
    expect(fieldSuggestions('')).toEqual([]);
    expect(fieldSuggestions('z')).toEqual([]);
  });

  it('flags comparison-only fields', () => {
    expect(isComparisonOnlyField('size')).toBe(true);
    expect(isComparisonOnlyField('done')).toBe(true);
    expect(isComparisonOnlyField('status')).toBe(false);
    expect(isComparisonOnlyField('method')).toBe(false);
  });

  it('suggests distinct values actually present, first-seen, capped', () => {
    const hops = [hop({ status: 200 }), hop({ status: 404 }), hop({ status: 200 }), hop({ status: 500 })];
    expect(valueSuggestions('status', hops)).toEqual(['200', '404', '500']);
    expect(valueSuggestions('path', hops)).toEqual([]);
  });
});

describe('hopPayloadBytes', () => {
  it('sums request and response body bytes', () => {
    const h = hop({ req: { body: 'x'.repeat(10) }, resp: { body: 'y'.repeat(20) } });
    expect(hopPayloadBytes(h)).toBe(30);
  });

  it('is 0 for a hop with no bodies', () => {
    expect(hopPayloadBytes(hop())).toBe(0);
  });
});
