import { useEffect, useRef, useState } from 'react';

export interface AsyncState<T> {
  data: T | null;
  error: Error | null;
  loading: boolean;
}

/**
 * Loads `fn()` on mount and whenever `deps` change, reporting
 * `{data, error, loading}`.
 *
 * This exists because the hand-rolled version of it —
 * `let cancelled = false; … .then(d => { if (cancelled) return; … })` —
 * was written ten times across Phase 3's five views and drifted nine ways.
 * Three separate race bugs came out of that drift.
 *
 * Two details are load-bearing:
 *
 *   - **The guard is a generation counter, not a boolean.** A boolean is
 *     scoped to the effect closure that created it, so it can only guard
 *     that one effect's resolution. The counter lives on the hook, so every
 *     path that can ever start a load — a deps change, StrictMode's
 *     deliberate double-invoke in development, and any refetch a future
 *     caller adds by bumping a dep — is guarded by construction rather than
 *     by each call site remembering to.
 *   - **State is cleared synchronously when deps change.** Leaving the
 *     previous deps' data on screen while the new load is in flight is not
 *     a cosmetic nicety: it renders one record's body under another
 *     record's heading, which is exactly the bug the Phase 3 review found
 *     in EntityDetail.
 *
 * `fn` is intentionally NOT in the dependency list: callers pass an inline
 * arrow, which is a new function identity every render, and depending on it
 * would re-fetch forever. `deps` is the caller's explicit statement of what
 * the load actually depends on.
 */
export function useAsync<T>(fn: () => Promise<T>, deps: readonly unknown[]): AsyncState<T> {
  const [state, setState] = useState<AsyncState<T>>({ data: null, error: null, loading: true });
  const generation = useRef(0);

  useEffect(() => {
    const mine = ++generation.current;
    setState({ data: null, error: null, loading: true });
    fn().then(
      (data) => {
        if (generation.current === mine) setState({ data, error: null, loading: false });
      },
      (cause: unknown) => {
        if (generation.current !== mine) return;
        setState({
          data: null,
          error: cause instanceof Error ? cause : new Error(String(cause)),
          loading: false,
        });
      },
    );
    // Bumping the generation on cleanup is what makes clause 4 hold: after
    // unmount (or before the next load starts) no in-flight promise can
    // still match `mine`.
    return () => {
      generation.current++;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- deps is the caller's contract; see the doc comment
  }, deps);

  return state;
}
