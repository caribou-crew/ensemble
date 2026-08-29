import { afterEach, describe, expect, it, vi } from 'vitest';
import type { FieldDiff } from './types';
import { ApiError, api, ruleRequestFor } from './client';

function fakeResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? 'OK' : 'Error',
    text: () => Promise.resolve(JSON.stringify(body)),
  };
}

/** Captures the one request the call under test makes. */
function captureFetch(response = fakeResponse({ ok: true })) {
  const calls: { url: string; init?: RequestInit }[] = [];
  const mock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: typeof input === 'string' ? input : input.toString(), init });
    return Promise.resolve(response);
  });
  vi.stubGlobal('fetch', mock);
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// R-U. The seed for a rule is a FieldDiff, and a FieldDiff HAS a `scope` —
// "resp" here, because the reviewer selected a response-body field. The
// server refuses that field with a 400 by design (routes.go: a wire rule is
// scoped by neither flow nor request/response), so a client that passes the
// seed's scope through does not merely send a field the server drops: EVERY
// rule the UI writes 400s.
//
// The fixture deliberately carries the scope. A seed field with no `scope` at
// all would prove nothing — that is value symmetry, and it is the costume
// this exact seam invites, because the natural fixture to write is the one
// that never had the field.
describe('the rule verb', () => {
  const seed: FieldDiff = {
    scope: 'resp',
    path: 'placedAt',
    type: 'changed',
    a: '2026-08-20T10:00:00Z',
    b: '2026-08-21T10:00:00Z',
  };

  it('sends no "scope" for a field that has one', async () => {
    expect(seed.scope).toBe('resp'); // the seed really does carry it

    const calls = captureFetch();
    await api.rule('web', 'checkout', ruleRequestFor(seed, 'iso8601', {
      method: 'GET',
      normalizedPath: '/orders/{id}',
    }));

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/queue/web/checkout/rule');
    const body = JSON.parse(String(calls[0].init?.body));
    expect(Object.keys(body).sort()).toEqual(['field', 'matcher', 'method', 'path']);
    expect('scope' in body).toBe(false);
    // `flow` is a path parameter and never belongs in the body either — the
    // server refuses it in the same breath, for the same reason.
    expect('flow' in body).toBe(false);
    expect(body).toEqual({
      field: 'placedAt',
      matcher: 'iso8601',
      method: 'GET',
      path: '/orders/{id}',
    });
  });

  it('surfaces the server refusal as an ApiError carrying its sentence', async () => {
    captureFetch(
      fakeResponse(
        { error: '"scope" is not a dimension a wire rule has, and that is deliberate' },
        400,
      ),
    );
    await expect(
      api.rule('web', 'checkout', { field: 'placedAt', matcher: 'iso8601' }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});

describe('shotUrl', () => {
  it('builds the four comparison panes', () => {
    expect(api.shotUrl('web', 'search', 'diff', 'results')).toBe(
      '/api/shots/web/search/diff/results',
    );
  });

  it('throws rather than building a URL for a side the summary never wrote', () => {
    // The empty string means images.<side> was "" — the side was never
    // written, and `/api/shots/web/search/diff/` would 404 as a mystery.
    //
    // What this throw does NOT do is make the mistake survivable. Every caller
    // builds an `src` during render, and a render-phase throw does not reach
    // useAsync's error state — there is no error boundary under dashboard/, so
    // React unmounts the root and the reviewer gets a blank page. The
    // component-side guard is what keeps that from happening; see
    // ShotCompare.test.tsx, which drives the exact JSON summary.go emits for a
    // missing checkpoint.
    expect(() => api.shotUrl('web', 'search', 'diff', '')).toThrow(/no diff-side image/);
  });
});

describe('itemAtRun', () => {
  it('hits the run-scoped item route rather than the plain (latest) one', async () => {
    const calls = captureFetch(fakeResponse({ summary: {} }));
    await api.itemAtRun('web', 'search', '20260821T101000Z-bbbbbbb');

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/queue/web/search/runs/20260821T101000Z-bbbbbbb');
  });
});

describe('shotUrlAtRun', () => {
  it('builds a run-scoped shot URL, distinct from the plain (latest) route', () => {
    expect(api.shotUrlAtRun('web', 'search', '20260821T101000Z-bbbbbbb', 'diff', 'results')).toBe(
      '/api/shots/web/search/runs/20260821T101000Z-bbbbbbb/diff/results',
    );
  });

  it('throws rather than building a URL for a side the summary never wrote', () => {
    expect(() => api.shotUrlAtRun('web', 'search', '20260821T101000Z-bbbbbbb', 'diff', '')).toThrow(
      /no diff-side image/,
    );
  });
});
