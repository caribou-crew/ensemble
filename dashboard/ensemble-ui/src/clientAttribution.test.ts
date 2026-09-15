import { describe, expect, it } from 'vitest';
import {
  buildClientIndex,
  distinctClients,
  hasUnattributed,
  matchesClient,
  observeClient,
  resolveClient,
  UNATTRIBUTED_CLIENT,
  type ClientIndex,
} from './clientAttribution';
import type { Hop } from './api/types';

// The cross-language contract. core/trace's TestClientAttributionFixture
// reads this same file; a rule change that updates only one language fails
// the other.
import rawFixture from '../../../core/trace/testdata/client_attribution.json';

interface Fixture {
  window: Hop[];
  expect: { seq: number; client: string; attributed: boolean; why: string }[];
}

function loadFixture(): Fixture {
  return rawFixture as unknown as Fixture;
}

describe('the shared attribution fixture', () => {
  const fixture = loadFixture();
  const idx = buildClientIndex(fixture.window);
  const bySeq = new Map(fixture.window.map((h) => [h.seq, h]));

  it('covers every hop in the window', () => {
    expect(fixture.expect).toHaveLength(fixture.window.length);
  });

  for (const want of fixture.expect) {
    it(`seq ${want.seq}: ${want.why}`, () => {
      const hop = bySeq.get(want.seq);
      expect(hop, `seq ${want.seq} is not in the window`).toBeDefined();
      const got = resolveClient(hop as Hop, idx);
      expect(got).toBe(want.attributed ? want.client : null);
    });
  }
});

describe('resolveClient', () => {
  it('inherits a client through traceId', () => {
    const entry: Hop = { seq: 1, traceId: 't1', client: 'app-legacy', to: 'gateway', t: { start: '' } } as Hop;
    const inner: Hop = { seq: 2, traceId: 't1', to: 'wallet', t: { start: '' } } as Hop;
    expect(resolveClient(inner, buildClientIndex([entry, inner]))).toBe('app-legacy');
  });

  it('does not join a hop with no traceId to anything in the window', () => {
    const entry: Hop = { seq: 1, traceId: 't1', client: 'app-legacy', to: 'gateway', t: { start: '' } } as Hop;
    const loose: Hop = { seq: 2, to: 'health', t: { start: '' } } as Hop;
    expect(resolveClient(loose, buildClientIndex([entry, loose]))).toBeNull();
  });

  // Order-independence is the property that makes the conflict rule
  // meaningful; a first-wins or last-wins implementation passes one
  // ordering and fails the other.
  it.each([
    ['legacy first', ['app-legacy', 'app-next']],
    ['next first', ['app-next', 'app-legacy']],
  ])('resolves a conflicting trace to unattributed (%s)', (_name, [a, b]) => {
    const window: Hop[] = [
      { seq: 1, traceId: 't4', client: a, to: 'gateway', t: { start: '' } } as Hop,
      { seq: 2, traceId: 't4', client: b, to: 'risk', t: { start: '' } } as Hop,
      { seq: 3, traceId: 't4', to: 'audit', t: { start: '' } } as Hop,
    ];
    const idx = buildClientIndex(window);
    for (const h of window) expect(resolveClient(h, idx)).toBeNull();
  });
});

describe('matchesClient', () => {
  const entry: Hop = { seq: 1, traceId: 't1', client: 'app-legacy', to: 'gateway', t: { start: '' } } as Hop;
  const inner: Hop = { seq: 2, traceId: 't1', to: 'wallet', t: { start: '' } } as Hop;
  const orphan: Hop = { seq: 3, traceId: 't9', to: 'health', t: { start: '' } } as Hop;
  const idx = buildClientIndex([entry, inner, orphan]);

  it('matches everything when nothing is asked for', () => {
    expect(matchesClient(orphan, idx, '')).toBe(true);
  });

  it('matches a downstream hop by its inherited client', () => {
    expect(matchesClient(inner, idx, 'app-legacy')).toBe(true);
    expect(matchesClient(inner, idx, 'app-next')).toBe(false);
  });

  it('selects only orphans for the unattributed sentinel', () => {
    expect(matchesClient(orphan, idx, UNATTRIBUTED_CLIENT)).toBe(true);
    expect(matchesClient(inner, idx, UNATTRIBUTED_CLIENT)).toBe(false);
  });
});

describe('distinctClients', () => {
  it('lists clients in first-seen order, resolved not raw', () => {
    const window: Hop[] = [
      { seq: 1, traceId: 't1', client: 'app-legacy', to: 'gateway', t: { start: '' } } as Hop,
      { seq: 2, traceId: 't1', to: 'wallet', t: { start: '' } } as Hop,
      { seq: 3, traceId: 't2', client: 'app-next', to: 'edge', t: { start: '' } } as Hop,
      { seq: 4, traceId: 't3', to: 'health', t: { start: '' } } as Hop,
    ];
    const idx = buildClientIndex(window);
    expect(distinctClients(window, idx)).toEqual(['app-legacy', 'app-next']);
    expect(hasUnattributed(window, idx)).toBe(true);
  });

  it('reports no unattributed hops when every hop resolves', () => {
    const window: Hop[] = [{ seq: 1, traceId: 't1', client: 'app-next', to: 'edge', t: { start: '' } } as Hop];
    expect(hasUnattributed(window, buildClientIndex(window))).toBe(false);
  });
});

describe('observeClient', () => {
  it('folds hops one at a time to the same index buildClientIndex produces', () => {
    const window: Hop[] = [
      { seq: 1, traceId: 't1', client: 'app-legacy', to: 'gateway', t: { start: '' } } as Hop,
      { seq: 2, traceId: 't1', to: 'wallet', t: { start: '' } } as Hop,
    ];
    const incremental: ClientIndex = new Map();
    for (const h of window) observeClient(incremental, h);
    expect([...incremental.entries()]).toEqual([...buildClientIndex(window).entries()]);
  });
});
