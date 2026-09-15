// Which client application a hop belongs to.
//
// This is the TypeScript half of a rule that also lives in Go
// (core/trace/client.go). Both are pinned to one shared fixture,
// core/trace/testdata/client_attribution.json — see
// clientAttribution.test.ts. Change the rule in one language without the
// other and that language's suite stays green while the other fails, which
// is the point.
//
// The rule exists twice because the two halves answer at different moments:
// the server filters a stream and a history page, the browser partitions
// side-by-side panes out of the one stream it already has. Asking the
// server to partition instead would mean a second SSE connection per pane,
// and two panes that can reconnect independently can sit at different
// points in time — which is the one thing a side-by-side comparison must
// never do.
import type { Hop } from './api/types';

/** Selects the hops that belong to no client. Unrepresentable as a real
 * client identity (core/proxy.ValidClient requires a leading [a-z0-9]), so
 * no application can ever be named this and collide with the filter. */
export const UNATTRIBUTED_CLIENT = '(none)';

/** Index value for a trace whose hops disagree about which client started
 * it. '' is free because only non-empty identities are ever inserted. */
const CONFLICTED = '';

export type ClientIndex = Map<string, string>;

/** Folds one hop into an index. Order-independent: once a trace is marked
 * conflicted nothing un-marks it. */
export function observeClient(idx: ClientIndex, hop: Hop): void {
  const traceId = hop.traceId;
  const client = hop.client;
  if (!traceId || !client) return;
  const prev = idx.get(traceId);
  if (prev === undefined) {
    idx.set(traceId, client);
    return;
  }
  if (prev !== CONFLICTED && prev !== client) idx.set(traceId, CONFLICTED);
}

/** traceId -> the client that trace belongs to, for resolveClient to read.
 * A trace carrying two different identities maps to a conflict and resolves
 * to unattributed rather than to whichever hop came first — putting a hop
 * in a pane it doesn't belong to is the failure this view cannot survive. */
export function buildClientIndex(hops: readonly Hop[]): ClientIndex {
  const idx: ClientIndex = new Map();
  for (const h of hops) observeClient(idx, h);
  return idx;
}

/** The client a hop belongs to, or null if it belongs to none.
 *
 * A hop's own `client` wins when its trace agrees; a hop with none inherits
 * through `traceId`. The fallback is what makes a routing difference
 * visible: an internal service-to-service hop only carries a client
 * identity if the chain propagated it, and the fan-out is exactly where two
 * apps diverge. */
export function resolveClient(hop: Hop, idx: ClientIndex): string | null {
  if (hop.traceId) {
    const c = idx.get(hop.traceId);
    if (c !== undefined) return c === CONFLICTED ? null : c;
    // Not indexed: no hop in this window claimed a client for the trace,
    // including this one. Fall through so a lone hop's own field still
    // answers.
  }
  return hop.client ? hop.client : null;
}

/** Whether a hop belongs to the client a filter asked for. An empty `want`
 * matches everything; UNATTRIBUTED_CLIENT selects the hops belonging to
 * none. */
export function matchesClient(hop: Hop, idx: ClientIndex, want: string): boolean {
  if (!want) return true;
  const got = resolveClient(hop, idx);
  if (want === UNATTRIBUTED_CLIENT) return got === null;
  return got === want;
}

/** Every client present in a window, in first-seen order — the selector's
 * options. Resolved rather than read off `hop.client`, so a chain whose
 * entry hop has scrolled out of view doesn't make its client vanish from
 * the list while its hops are still on screen. */
export function distinctClients(hops: readonly Hop[], idx: ClientIndex): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const h of hops) {
    const c = resolveClient(h, idx);
    if (c && !seen.has(c)) {
      seen.add(c);
      out.push(c);
    }
  }
  return out;
}

/** Whether any hop in the window belongs to no client — whether the
 * unattributed option is worth offering at all. */
export function hasUnattributed(hops: readonly Hop[], idx: ClientIndex): boolean {
  return hops.some((h) => resolveClient(h, idx) === null);
}
