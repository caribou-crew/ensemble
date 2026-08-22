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
 * THE SETTLE SIGNAL IS `loading`, NOT THE VALUE THAT CAME BACK. This hook used to take
 * `data`/`error` and drain on `data !== null || error`, which is a predicate about the SHAPE
 * OF THE RESULT standing in for a fact about the LOAD. It gets the one case wrong that looks
 * like the in-flight case: a load that legitimately resolves `null` sets `data` back to
 * exactly the value "still loading" is encoded as, so it never drained and every waiter hung
 * forever — the same never-settling `await` as the single-slot bug above, reached by a
 * different route (re-review N4). That is not hypothetical here: `InspectorView.useRows` and
 * `TopologyView.useTracePoll` both `return null` on purpose, and Go's `json.Marshal` writes a
 * bare `null` for a nil slice, which `request<T>` parses straight through. `loading` is
 * `useAsync`'s own answer to "is a load in flight" — true for exactly one in-flight load and
 * false afterwards whether it ended in data, `null`, or an error — so it cannot be confused
 * by a legitimate value that happens to resemble an absent one.
 *
 * ON UNMOUNT the waiters are resolved rather than dropped, and that holds for a `refresh()`
 * that STARTS after the unmount too. Dropping a resolver is a promise that never settles, and
 * an `await` that never returns is exactly the defect above. Draining only the already-parked
 * waiters left the other half open: a continuation that reaches `await refresh()` after its
 * component is gone — `await api.stop(name)` finishing after the user switched tabs, which
 * `App.tsx` unmounts the whole view on — pushed its resolver onto a ref no effect would ever
 * read again and hung (re-review N5). Once closed, `refresh()` resolves immediately instead of
 * parking: there is no load left to wait for, so "the reload landed" is vacuously true, and
 * the caller's `finally` runs.
 *
 * @param loading  the loading hook's `loading` flag — true while a load is in flight
 * @param start    stable callback that starts the reload (in practice, bumping a tick)
 */
export function usePendingRefresh(loading: boolean, start: () => void): () => Promise<void> {
  const waitingRef = useRef<(() => void)[]>([]);
  const closedRef = useRef(false);

  const drain = useCallback(() => {
    // Swap the list out BEFORE resolving: a resolver's continuation can call `refresh()`
    // again, and that new waiter belongs to the next load, not to this drain.
    const waiting = waitingRef.current;
    waitingRef.current = [];
    for (const resolve of waiting) resolve();
  }, []);

  useEffect(() => {
    if (!loading) drain();
  }, [loading, drain]);

  // `drain` is stable, so this runs once on mount and its cleanup runs only on unmount.
  // `closedRef` is re-armed on the way in rather than only set on the way out, so a
  // remount — StrictMode's deliberate mount/unmount/mount in development, or a keyed
  // component React chooses to recreate — gets a hook that parks waiters again rather than
  // one permanently stuck resolving them instantly.
  useEffect(() => {
    closedRef.current = false;
    return () => {
      closedRef.current = true;
      drain();
    };
  }, [drain]);

  return useCallback(
    () =>
      new Promise<void>((resolve) => {
        // Nothing will ever start, land, or drain again — settle now rather than park a
        // resolver on a ref no effect will read (re-review N5).
        if (closedRef.current) {
          resolve();
          return;
        }
        waitingRef.current.push(resolve);
        start();
      }),
    [start],
  );
}
