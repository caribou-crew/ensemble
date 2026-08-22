import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { usePendingRefresh } from './usePendingRefresh';

// Re-review N1. The view-level probes (ServicesView/TopologyView.concurrent-refresh) pin the
// user-visible consequence; these pin the hook's contract clauses directly, including the
// ones no view can currently observe.

interface Harness {
  refresh: () => Promise<void>;
  land: (value: unknown) => void;
  landNull: () => void;
  fail: (err: Error) => void;
}

let harness: Harness;

function Probe() {
  // Stands in for useAsync, INCLUDING the detail this hook now turns on: `loading` goes true
  // the instant a reload starts and false when it settles, whatever it settled to — while
  // `data` goes back to null on a reload AND stays null for a load that resolved null.
  const [state, setState] = useState<{ data: unknown; error: unknown; loading: boolean }>({
    data: null,
    error: null,
    loading: false,
  });
  const refresh = usePendingRefresh(state.loading, () =>
    setState({ data: null, error: null, loading: true }),
  );
  harness = {
    refresh,
    land: (value) => setState({ data: value, error: null, loading: false }),
    landNull: () => setState({ data: null, error: null, loading: false }),
    fail: (err) => setState({ data: null, error: err, loading: false }),
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

  // Re-review N4. The hook used to drain on `data !== null || error`, which asks about the
  // SHAPE OF THE RESULT in place of the fact it actually needs — did the load settle. A load
  // that legitimately resolves `null` settles to the exact value "in flight" is encoded as,
  // so it never drained and every waiter's `await refresh()` hung forever: the caller's
  // `finally { setBusy(null) }` never ran and its control stayed disabled or spinning for the
  // life of the page. Reachable today, not a corner case — `InspectorView.useRows` and
  // `TopologyView.useTracePoll` both `return null` deliberately, and Go marshals a nil slice
  // to a bare JSON `null` that `request<T>` parses straight through, so an empty result from
  // a healthy API is a `null` result here.
  //
  // This is the clause that must not be re-expressed as a test about `data`: any predicate
  // that inspects the value will have this bug again the next time a legitimate value
  // resembles an absent one. `loading` going false is the signal.
  it('settles its waiters for a load that legitimately resolves null (N4)', async () => {
    await act(async () => {
      root.render(createElement(Probe));
    });

    let settled = false;
    await act(async () => {
      void harness.refresh().then(() => {
        settled = true;
      });
    });
    expect(settled, 'nothing may settle while the load is still in flight').toBe(false);

    await act(async () => {
      harness.landNull();
    });
    expect(
      settled,
      'a load that resolved null is a SETTLED load — a nil slice from the API is data, not ' +
        'an unfinished request — and its waiters must be released',
    ).toBe(true);

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

  // Re-review N5, the other half of the clause above. Draining the ALREADY-PARKED waiters on
  // unmount says nothing about a `refresh()` that starts afterwards, and the hook's comment
  // claimed the whole class was closed while that half was still open. Reachable through the
  // UI as written: `ServiceRow.run` awaits `api.stop(name)`, the user clicks another tab,
  // `App.tsx` unmounts the view, and the continuation then reaches `await refresh()` — which
  // pushed a resolver onto a ref no effect would ever read again. Nothing is left to wait
  // for once the hook is closed, so the honest answer is to settle immediately.
  it('settles a refresh() that STARTS after unmount (N5)', async () => {
    await act(async () => {
      root.render(createElement(Probe));
    });
    const refresh = harness.refresh;

    await act(async () => {
      root.unmount();
    });

    let settled = false;
    await act(async () => {
      void refresh().then(() => {
        settled = true;
      });
    });
    expect(
      settled,
      'a refresh() begun after the hook is gone must still settle: the continuation that ' +
        'awaits it outlives the component, and its `finally` never runs otherwise',
    ).toBe(true);
  });
});
