import { useCallback, useEffect, useRef } from 'react';

/**
 * Turns a "bump a tick and let `useAsync` reload" refresh into a promise that settles only
 * once the reload it asked for has actually landed on screen.
 *
 * Bumping a tick alone resolves as soon as the state update is *scheduled*, not once the new
 * data (or a new error) is rendered — so a caller that does
 * `await api.stop(name); await refresh()` inside a `try/finally { setBusy(null) }` un-busies
 * its control while the row still shows the pre-action state (final review F7).
 *
 * EVERY WAITING CALLER IS RESOLVED, NOT JUST THE LAST ONE. The first version of the F7 fix
 * parked a single resolver in a ref, so a second `refresh()` overwrote the first WITHOUT
 * calling it. That is not a corner case: the controls that call `refresh()` live in
 * components that own their own `busy` state and disable only their OWN buttons
 * (`ServicesView`'s per-row actions; `TopologyView`'s profile strip vs. its service panel),
 * so two of them are concurrently actionable by design. The orphaned caller's `await` never
 * settled, its `finally` never ran, and the control was left disabled forever — with no
 * spinner and no error, because by then the refreshed data had flipped the row to a state
 * whose buttons the stale `busy` value matches no spinner condition for. That is strictly
 * worse than the cosmetic early-un-busy F7 set out to fix (re-review N1).
 *
 * Holding a list is not merely the conservative choice, it is the correct one: `useAsync`'s
 * generation guard means the load that lands is the latest-STARTED one, which is at least as
 * fresh as every request still waiting. One completion legitimately satisfies all of them —
 * there is no reason to pick a winner, and no caller wants to be the loser.
 *
 * ON UNMOUNT the waiters are resolved rather than dropped. Today that is close to
 * academic — the component asking is going away — but a dropped resolver is a promise that
 * never settles, and an `await` that never returns is exactly the defect above. A caller
 * that survives the unmount (or one added later) must not inherit it.
 *
 * @param data   the loading hook's current data — `null` while a load is in flight
 * @param error  the loading hook's current error
 * @param start  stable callback that starts the reload (in practice, bumping a tick)
 */
export function usePendingRefresh(
  data: unknown,
  error: unknown,
  start: () => void,
): () => Promise<void> {
  const waitingRef = useRef<(() => void)[]>([]);

  const drain = useCallback(() => {
    // Swap the list out BEFORE resolving: a resolver's continuation can call `refresh()`
    // again, and that new waiter belongs to the next load, not to this drain.
    const waiting = waitingRef.current;
    waitingRef.current = [];
    for (const resolve of waiting) resolve();
  }, []);

  useEffect(() => {
    if (data !== null || error) drain();
  }, [data, error, drain]);

  // `drain` is stable, so this cleanup runs only on unmount.
  useEffect(() => drain, [drain]);

  return useCallback(
    () =>
      new Promise<void>((resolve) => {
        waitingRef.current.push(resolve);
        start();
      }),
    [start],
  );
}
