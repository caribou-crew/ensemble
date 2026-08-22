import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { usePendingRefresh } from './usePendingRefresh';

// Re-review N1. The view-level probes (ServicesView/TopologyView.concurrent-refresh) pin the
// user-visible consequence; these pin the hook's two contract clauses directly, including
// the one no view can currently observe.

interface Harness {
  refresh: () => Promise<void>;
  land: (value: unknown) => void;
  fail: (err: Error) => void;
}

let harness: Harness;

function Probe() {
  // Stands in for useAsync: `data` goes back to null the instant a reload starts, and
  // becomes non-null (or `error` becomes set) when it lands.
  const [state, setState] = useState<{ data: unknown; error: unknown }>({ data: null, error: null });
  const refresh = usePendingRefresh(state.data, state.error, () =>
    setState({ data: null, error: null }),
  );
  harness = {
    refresh,
    land: (value) => setState({ data: value, error: null }),
    fail: (err) => setState({ data: null, error: err }),
  };
  return null;
}

describe('usePendingRefresh', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    container.remove();
    vi.restoreAllMocks();
  });

  it('resolves EVERY waiting caller on one load completion, not just the newest', async () => {
    await act(async () => {
      root.render(createElement(Probe));
    });

    const settled: string[] = [];
    await act(async () => {
      void harness.refresh().then(() => settled.push('first'));
      void harness.refresh().then(() => settled.push('second'));
      void harness.refresh().then(() => settled.push('third'));
    });
    expect(settled, 'nothing may settle before the load lands').toEqual([]);

    await act(async () => {
      harness.land({ ok: true });
    });
    expect(settled.sort()).toEqual(['first', 'second', 'third']);

    // A later load must not re-resolve the drained waiters or wedge new ones.
    settled.length = 0;
    await act(async () => {
      void harness.refresh().then(() => settled.push('fourth'));
    });
    await act(async () => {
      harness.fail(new Error('boom'));
    });
    expect(settled, 'an error is a settled load too, and must release its waiters').toEqual([
      'fourth',
    ]);

    act(() => root.unmount());
  });

  it('resolves waiters on unmount instead of dropping them', async () => {
    await act(async () => {
      root.render(createElement(Probe));
    });

    let settled = false;
    await act(async () => {
      void harness.refresh().then(() => {
        settled = true;
      });
    });
    expect(settled).toBe(false);

    await act(async () => {
      root.unmount();
    });
    // Without this, the caller's `await refresh()` never returns and whatever it holds —
    // a busy flag, a lock, a queue slot — is never released. Harmless while the component
    // is the only holder; not harmless the moment anything outlives it.
    expect(settled, 'an unmount must settle pending refreshes, not orphan them').toBe(true);
  });
});
