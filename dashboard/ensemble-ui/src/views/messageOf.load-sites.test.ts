import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import App from '../App';
import EntityView from './EntityView';
import InspectorView from './InspectorView';
import LatencyView from './LatencyView';
import TopologyView from './TopologyView';
import TrafficView from './TrafficView';
import { api } from '../api/client';
import { writeParams } from '../urlState';
import type { DatabaseInfo, EntityInfo } from '../api/types';

vi.mock('../api/sse', () => ({
  subscribeChanges: () => () => {},
  subscribeHops: () => () => {},
}));

// Final review F5: the migration onto useAsync dropped `messageOf`'s fallback at every load
// site, rendering `error.message` raw instead. For an ApiError that is the same string; for
// anything else — a network failure, a JSON parse failure, a thrown TypeError — the user got
// developer text like "Failed to fetch" or "Unexpected token < in JSON at position 0" in
// place of "failed to reach the ensemble API".
//
// All twelve load sites were restored, but the re-review found only ONE of the twelve pinned:
// reverting all twelve at once killed exactly one test. Nine more are pinned here;
// `ServicesView` is pinned by ServicesView.stale-error.test.ts and `InspectorView.useRows` by
// InspectorView.rows-error.test.ts, both of which assert the same two things this file does.
// That is 11 of 12. The twelfth is unreachable in the rendered UI — see the note below it.
//
// Every case rejects with a raw `TypeError`, deliberately NOT an `ApiError`: messageOf
// returns an ApiError's own message, so an ApiError cannot tell the two implementations
// apart. And every case asserts BOTH halves — the fallback is present AND the raw message is
// absent — because a site that renders both would otherwise pass on the first assertion.
//
// The count is twelve, and I counted it from HEAD rather than taking any of the three
// numbers that have been quoted for it: 22 `messageOf` call sites outside its own definition,
// of which 10 are pre-existing mutation handlers (a caught `err` from an api.* write) and 12
// are load sites (a `useAsync` error, directly or via a sticky mirror). Per file:
//   App 1 · EntityView 3 · InspectorView 3 · LatencyView 1 · ServicesView 1 · TopologyView 2
//   · TrafficView 1. No raw `.message` on an error survives anywhere outside messageOf.

const RAW = 'Failed to fetch';
const API_FALLBACK = 'failed to reach the ensemble API';

function netFail() {
  return Promise.reject(new TypeError(RAW));
}

function expectFallback(container: HTMLElement, fallback: string) {
  expect(container.textContent, `expected messageOf's fallback "${fallback}"`).toContain(fallback);
  expect(
    container.textContent,
    `the raw Error#message ("${RAW}") must never reach the screen — that is the whole point ` +
      'of messageOf',
  ).not.toContain(RAW);
}

describe('messageOf fallback at every load site', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    window.history.replaceState(null, '', '/');
  });

  async function render(view: Parameters<typeof createElement>[0]) {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(view));
    });
    // Some of these views settle their error through a second hop (a sticky-error mirror
    // effect, or a load whose deps only become non-null once an outer load resolved), so one
    // act() is not always enough to reach the rendered banner.
    for (let i = 0; i < 6; i += 1) {
      // eslint-disable-next-line no-await-in-loop
      await act(async () => {
        await Promise.resolve();
      });
    }
  }

  it('App.tsx — the health strip poll', async () => {
    window.history.replaceState(null, '', '/?view=entities');
    vi.spyOn(api, 'entities').mockResolvedValue([]);
    vi.spyOn(api, 'status').mockImplementation(netFail);
    await render(App);
    expectFallback(container, API_FALLBACK);
  });

  it('EntityView.tsx — the entity index', async () => {
    vi.spyOn(api, 'entities').mockImplementation(netFail);
    await render(EntityView);
    expectFallback(container, API_FALLBACK);
  });

  it('EntityView.tsx — one entity\'s list', async () => {
    vi.spyOn(api, 'entities').mockResolvedValue([{ name: 'users', id: 'id' }] as EntityInfo[]);
    vi.spyOn(api, 'entityList').mockImplementation(netFail);
    writeParams({ entity: 'users' });
    await render(EntityView);
    expectFallback(container, 'failed to load users');
  });

  it('EntityView.tsx — one record\'s detail', async () => {
    vi.spyOn(api, 'entities').mockResolvedValue([{ name: 'users', id: 'id' }] as EntityInfo[]);
    vi.spyOn(api, 'entityList').mockResolvedValue([]);
    vi.spyOn(api, 'entityGet').mockImplementation(netFail);
    writeParams({ entity: 'users', id: '1' });
    await render(EntityView);
    expectFallback(container, 'failed to load users/1');
  });

  it('InspectorView.tsx — the database list', async () => {
    // Not a 501: that status has its own rendered "unavailable" state and never reaches
    // messageOf (InspectorView.unavailable.test.ts pins that branch).
    vi.spyOn(api, 'databases').mockImplementation(netFail);
    await render(InspectorView);
    expectFallback(container, API_FALLBACK);
  });

  it('InspectorView.tsx — one database\'s schema', async () => {
    vi.spyOn(api, 'databases').mockResolvedValue([{ name: 'maindb', type: 'postgres' }] as DatabaseInfo[]);
    vi.spyOn(api, 'databaseSchema').mockImplementation(netFail);
    await render(InspectorView);
    expectFallback(container, 'failed to load schema for maindb');
  });

  it('LatencyView.tsx — the rule list', async () => {
    vi.spyOn(api, 'latencyList').mockImplementation(netFail);
    await render(LatencyView);
    expectFallback(container, API_FALLBACK);
  });

  it('TopologyView.tsx — the topology poll', async () => {
    vi.spyOn(api, 'topology').mockImplementation(netFail);
    vi.spyOn(api, 'status').mockImplementation(netFail);
    vi.spyOn(api, 'traffic').mockImplementation(netFail);
    vi.spyOn(api, 'profiles').mockResolvedValue({ active: [], profiles: [] });
    await render(TopologyView);
    expectFallback(container, API_FALLBACK);
  });

  // The twelfth load site — `useTracePoll`'s `failed to load trace ${traceId}` at
  // TopologyView.tsx:244 — is NOT pinned here, and cannot be pinned through the UI, because
  // the banner that would render it is unreachable. Its JSX lives inside the main return,
  // which is guarded by `if (!layout) return <loading/>`; in trace mode `layout` is null
  // whenever `traceHops` is null, and useAsync sets `data` OR `error`, never both — so
  // exactly when `traceError` is non-null, the view has already returned the spinner. A
  // failing trace load renders "loading trace abc123…" forever and shows the error nowhere.
  // Verified against HEAD by writing this case and watching it receive
  // 'loading trace abc123…'. That is a rendering defect that predates F5 and is out of this
  // round's scope; it is REPORTED rather than fixed, and this comment is here so nobody
  // records the site as covered. Fixing the reachability is what would make it pinnable.

  it('TrafficView.tsx — the seed load', async () => {
    vi.spyOn(api, 'traffic').mockImplementation(netFail);
    vi.spyOn(api, 'topology').mockResolvedValue({ nodes: [], edges: [] });
    await render(TrafficView);
    expectFallback(container, API_FALLBACK);
  });
});
