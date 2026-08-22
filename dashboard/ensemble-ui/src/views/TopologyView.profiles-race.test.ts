import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TopologyView from './TopologyView';
import { api } from '../api/client';
import type { ProfilesState, ServiceState, Topology } from '../api/types';

// Regression test for final review F6: `useProfiles` ran `load()` from a 5s interval AND
// wrote `setProfiles` directly from `toggle()`, so two `api.profiles()` calls could be in
// flight with no ordering guarantee and no generation guard — the same I3 race class
// TopologyView.poll-race.test.ts and ServicesView.poll-race.test.ts already pin for their
// own polls. Migrating `useProfiles` onto `useAsync` (toggle() reloads instead of writing
// its own response) means there is only ever one writer, generation-guarded the same way.

const TOPOLOGY: Topology = {
  nodes: [{ name: 'edge', category: 'service', status: 'healthy' }],
  edges: [],
};

const POLL_MS = 5000;

function statusOf(status: string): ServiceState[] {
  return [{ name: 'edge', status, placement: 'docker' }];
}

function profilesOf(active: boolean): ProfilesState {
  return {
    active: active ? ['canary'] : [],
    profiles: [{ name: 'canary', services: ['edge'], active }],
  };
}

describe('TopologyView: useProfiles race', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'status').mockResolvedValue(statusOf('healthy'));
    vi.spyOn(api, 'traffic').mockResolvedValue([]);
    vi.spyOn(api, 'trace').mockRejectedValue(new Error('this test never sets ?trace='));
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
    window.history.replaceState(null, '', '/');
  });

  it('applies only the LATEST-started profiles reload, even when an older interval poll resolves after it', async () => {
    let call = 0;
    let resolveIntervalPoll!: (p: ProfilesState) => void;
    let resolveToggleReload!: (p: ProfilesState) => void;
    vi.spyOn(api, 'profiles').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(profilesOf(false));
      if (call === 2) {
        return new Promise<ProfilesState>((r) => {
          resolveIntervalPoll = r;
        });
      }
      if (call === 3) {
        return new Promise<ProfilesState>((r) => {
          resolveToggleReload = r;
        });
      }
      throw new Error(`unexpected profiles() call #${call}`);
    });
    vi.spyOn(api, 'profileUp').mockResolvedValue(profilesOf(true));

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });
    expect(container.querySelector('.topo-view__profiles')?.textContent).toContain('canary');

    // The 5s interval fires (call #2, the OLDER of the two racing calls) and is held
    // pending — stands in for "the periodic profiles poll that was already in flight".
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });

    // The user toggles the profile on before the periodic poll above resolves: profileUp()
    // resolves immediately, then toggle()'s own reload() call is #3, the NEWER racing call.
    const toggleButton = container.querySelector('.topo-view__profile') as HTMLButtonElement | null;
    expect(toggleButton).toBeTruthy();
    await act(async () => {
      toggleButton!.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    // Resolve the NEWER call (#3) first, active — then the OLDER, periodic call (#2)
    // afterward, inactive. A correct generation guard must keep showing active: call #2 was
    // superseded the moment call #3 started, regardless of which resolves first.
    await act(async () => {
      resolveToggleReload(profilesOf(true));
    });
    await act(async () => {
      resolveIntervalPoll(profilesOf(false));
    });

    const strip = container.querySelector('.topo-view__profiles');
    expect(strip?.querySelector('.topo-view__profile--active')).toBeTruthy();
  });
});
