import { useEffect, useState } from 'react';
import type { Summary } from '@ensemble/design-system/retraceTypes';
import { loadSummary, type Pairing } from './reportData';

export interface SummaryState {
  sum?: Summary;
  error?: string;
}

const memo = new Map<string, Promise<Summary>>();

export const pairKey = (p: Pairing) => `${p.app}/${p.flow}/${p.run?.runId ?? ''}/${p.base?.runId ?? ''}`;

export function forgetSummaries(): void {
  memo.clear();
}

function fetchSummary(p: Pairing): Promise<Summary> {
  const k = pairKey(p);
  let pr = memo.get(k);
  if (!pr) {
    pr = loadSummary(p);
    memo.set(k, pr);
    pr.catch(() => memo.delete(k));
  }
  return pr;
}

/** Loads every pairing's summary in parallel, publishing each as it lands. */
export function useSummaries(ps: Pairing[]): Map<string, SummaryState> {
  const [state, setState] = useState<Map<string, SummaryState>>(new Map());
  const keys = ps.filter((p) => p.run && !p.missingBase && !p.noBaseline).map(pairKey).join('|');

  useEffect(() => {
    let live = true;
    setState(new Map());
    for (const p of ps) {
      // noBaseline: the server would 409 (see reportData.hasBaseline) —
      // skip the request rather than turn "no baseline yet" into a
      // network-tab error on every load.
      if (!p.run || p.missingBase || p.noBaseline) continue;
      const k = pairKey(p);
      fetchSummary(p).then(
        (sum) => live && setState((m) => new Map(m).set(k, { sum })),
        (err: unknown) => live && setState((m) => new Map(m).set(k, { error: err instanceof Error ? err.message : String(err) })),
      );
    }
    return () => {
      live = false;
    };
  }, [keys]);

  return state;
}
