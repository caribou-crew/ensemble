// URL-as-state: deep links (?view=&trace=&db=&table=&entity=) are a
// feature, not an accident. No router library — just the querystring, kept
// in sync via history.replaceState so navigating a view never grows the
// browser history stack.
import { useCallback, useEffect, useState } from 'react';

/** Reads a single query-string parameter from the current URL. */
export function readParam(key: string): string | null {
  return new URLSearchParams(window.location.search).get(key);
}

/**
 * Patches one or more query-string parameters on the current URL in place
 * via history.replaceState — no navigation, no new history entry. A patch
 * value of `null` deletes that key.
 */
export function writeParams(patch: Record<string, string | null>): void {
  const params = new URLSearchParams(window.location.search);
  for (const [key, value] of Object.entries(patch)) {
    if (value === null) {
      params.delete(key);
    } else {
      params.set(key, value);
    }
  }
  const qs = params.toString();
  const url = `${window.location.pathname}${qs ? `?${qs}` : ''}${window.location.hash}`;
  window.history.replaceState(window.history.state, '', url);
}

/**
 * React binding for one URL query param: initializes from the current URL,
 * writes through `writeParams` on set, and stays in sync with URL changes
 * driven from elsewhere (browser back/forward, another component's
 * writeParams call) via the `popstate` event.
 */
export function useUrlParam(key: string): [string | null, (value: string | null) => void] {
  const [value, setValue] = useState<string | null>(() => readParam(key));

  useEffect(() => {
    const onPopState = () => setValue(readParam(key));
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, [key]);

  const set = useCallback(
    (next: string | null) => {
      writeParams({ [key]: next });
      setValue(next);
    },
    [key],
  );

  return [value, set];
}
