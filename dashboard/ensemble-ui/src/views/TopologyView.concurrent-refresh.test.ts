import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TopologyView from './TopologyView';
import { api } from '../api/client';
import type { ProfilesState, ServiceState, Topology } from '../api/types';

// Re-review N1, second site. `useTopologyPoll.refresh` is awaited by BOTH the service
// panel's restart/flip/setVariant AND (via `refreshTopology`) by `useProfiles.toggle`.
// ProfileStrip and ServicePanel hold independent `busy` state and are simultaneously
// clickable, so two refreshes can genuinely be waiting on the same reload. A refresh that
// parks only the most-recent resolver drops the earlier one, and here the symptom is a
// PERMANENT SPINNER rather than a silently dead control: ProfileStrip renders one directly
// from `busy === p.name`, and `busy` is cleared in the toggle's `finally`, which never runs.

const TOPOLOGY: Topology = {
  nodes: [{ name: 'edge', category: 'service', status: 'healthy' }],
  edges: [],
};

const PROFILES: ProfilesState = {
  active: [],
  profiles: [{ name: 'lane-x', active: false, services: ['edge'] }],
};

function statusOf(status: string): ServiceState[] {
  return [{ name: 'edge', status, placement: 'docker' }];
}

describe('TopologyView: concurrent refresh waiters', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'traffic').mockResolvedValue([]);
    // Every api.* method TopologyView can reach is mocked, not only the ones this test
    // clicks — see TopologyView.poll-race.test.ts and testSetup.ts (F.18).
    vi.spyOn(api, 'profiles').mockResolvedValue(PROFILES);
    vi.spyOn(api, 'profileUp').mockResolvedValue(PROFILES);
    vi.spyOn(api, 'profileDown').mockResolvedValue(PROFILES);
    vi.spyOn(api, 'flip').mockResolvedValue(statusOf('healthy')[0]);
    vi.spyOn(api, 'setVariant').mockResolvedValue(statusOf('healthy')[0]);
    vi.spyOn(api, 'restart').mockResolvedValue(statusOf('healthy')[0]);
    vi.spyOn(api, 'trace').mockRejectedValue(new Error('this test never sets ?trace='));
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
    window.history.replaceState(null, '', '/');
  });

  it('clears the lane spinner when a service action refreshes on top of a profile toggle (N1)', async () => {
    const pending: ((s: ServiceState[]) => void)[] = [];
    let call = 0;
    vi.spyOn(api, 'status').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(statusOf('healthy'));
      return new Promise<ServiceState[]>((r) => {
        pending.push(r);
      });
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });

    // Open the service panel so its Restart button is on screen alongside the lane strip.
    const node = container.querySelector('.topo-node');
    expect(node, 'expected one rendered topology node').toBeTruthy();
    await act(async () => {
      node!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(container.querySelector('.topo-panel')).toBeTruthy();

    // Toggle the lane. profileUp resolves, useProfiles' own reload resolves, and the
    // toggle then parks a waiter on refreshTopology() whose status() call is held pending.
    const lane = Array.from(container.querySelectorAll<HTMLButtonElement>('.topo-view__profile')).find(
      (b) => b.textContent?.includes('lane-x'),
    );
    expect(lane, 'expected the lane-x profile button').toBeTruthy();
    await act(async () => {
      lane!.click();
    });
    expect(pending.length, "the toggle's refreshTopology() must have started a reload").toBe(1);
    expect(
      container.querySelector('.topo-view__profiles .ds-spinner'),
      'the lane must still be spinning while its refresh is in flight (F7)',
    ).toBeTruthy();

    // Restart, from the independently-enabled service panel, parks a second waiter on the
    // very same refresh.
    const restart = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Restart');
    expect(restart, 'expected a Restart button once a node is selected').toBeTruthy();
    await act(async () => {
      restart!.click();
    });
    expect(pending.length, "Restart's own refresh() must have started its own reload").toBe(2);

    // Everything settles. Both waiters must be resolved, so the toggle's
    // `finally { setBusy(null) }` runs and the lane stops spinning.
    await act(async () => {
      pending.forEach((resolve) => resolve(statusOf('healthy')));
    });

    const strip = container.querySelector('.topo-view__profiles');
    expect(
      strip?.querySelector('.ds-spinner'),
      `the lane spinner must clear once every refresh has landed (strip: ${strip?.innerHTML})`,
    ).toBeNull();
    const stuck = Array.from(strip?.querySelectorAll('button') ?? []).filter((b) => b.disabled);
    expect(stuck.length, 'no lane button may be left disabled').toBe(0);
    expect(container.querySelector('.topo-panel .ds-spinner'), 'Restart must not still be busy').toBeNull();
  });

  // Final review F7's third site. `useProfiles.reload` was fixed alongside the other two but
  // nothing pinned it: the re-review reverted it to the resolve-immediately shape and the
  // whole suite stayed green, because the test above is satisfied by the WAIT that
  // `refreshTopology()` does one line later. Isolating it needs the topology refresh to
  // complete while the profiles reload is still in flight — then a `reload()` that resolves
  // early lets `toggle`'s `finally` run, and the lane un-busies while the strip still shows
  // its pre-toggle state, which is precisely the bug F7 exists for.
  it('keeps the lane busy until the profiles reload IT triggered has landed (F7)', async () => {
    vi.spyOn(api, 'status').mockResolvedValue(statusOf('healthy'));

    let profilesCall = 0;
    let resolveReload!: (p: ProfilesState) => void;
    const ACTIVATED: ProfilesState = {
      active: ['lane-x'],
      profiles: [{ name: 'lane-x', active: true, services: ['edge'] }],
    };
    vi.spyOn(api, 'profiles').mockImplementation(() => {
      profilesCall += 1;
      if (profilesCall === 1) return Promise.resolve(PROFILES);
      return new Promise<ProfilesState>((r) => {
        resolveReload = r;
      });
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });

    const lane = Array.from(container.querySelectorAll<HTMLButtonElement>('.topo-view__profile')).find(
      (b) => b.textContent?.includes('lane-x'),
    );
    expect(lane, 'expected the lane-x profile button').toBeTruthy();
    await act(async () => {
      lane!.click();
    });

    // api.profileUp has resolved and the topology refresh (whose calls all resolve
    // immediately here) is done — the ONLY thing still outstanding is the profiles reload.
    expect(profilesCall, "the toggle's own reload must have started").toBe(2);
    const strip = container.querySelector('.topo-view__profiles');
    expect(
      strip?.querySelector('.ds-spinner'),
      'the lane must stay busy until its own profiles reload lands, not un-busy as soon as ' +
        'the tick bump is scheduled',
    ).toBeTruthy();
    expect(strip?.textContent, 'and the strip must still show its pre-toggle state').toBeTruthy();
    expect(
      strip?.querySelector('.topo-view__profile--active'),
      'lane-x must not read as active before the reload that confirms it has landed',
    ).toBeNull();

    await act(async () => {
      resolveReload(ACTIVATED);
    });
    expect(container.querySelector('.topo-view__profiles .ds-spinner')).toBeNull();
    expect(container.querySelector('.topo-view__profile--active')).toBeTruthy();
  });
});
