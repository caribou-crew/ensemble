// Two clients' hops on one shared row axis.
//
// Both panes are merged into a single ordered sequence, and each position
// in that sequence is one row slot spanning both columns. A hop renders in
// its own client's column at its slot; the opposite column is empty there.
// An empty slot opposite a row therefore means exactly one thing: that call
// happened on one client and not the other, at that point in the sequence.
// That is the readable claim the whole view exists to make.
//
// Deliberately NOT a proportional wall-clock axis. Proportional time is
// more honest about duration and bursts, and unusable here: tap one app,
// think for thirty seconds, tap the other, and the two things being
// compared are separated by thirty seconds of blank pixels. Per-hop timing
// is one click away in HopDetail, and TraceWaterfall already gives the
// proportional view of a single trace.
import type { Hop } from './api/types';
import { resolveClient, UNATTRIBUTED_CLIENT, type ClientIndex } from './clientAttribution';

export interface PaneRow {
  /** Stable across renders: the seq of whichever hop occupies the slot. */
  key: number;
  left: Hop | null;
  right: Hop | null;
}

export interface SplitResult {
  rows: PaneRow[];
  /** Hops in the window belonging to neither selected client — a third
   * client's, or none at all. Reported, never silently dropped: "the call I
   * am looking for went missing" and "the call I am looking for is
   * unattributed" are opposite diagnoses, and a silent filter hands you the
   * wrong one. */
  withheld: Hop[];
}

/** Ordering key for the shared axis: start time, tie-broken by seq.
 *
 * The tie-break is not decoration. Hops recorded in the same millisecond —
 * routine on a local stack — would otherwise fall back on the input array's
 * order, and the input is completion order, which within one trace is
 * inner-first. Two hops that started together would then read in the
 * reverse of the order they happened. */
function before(a: Hop, b: Hop): number {
  const at = Date.parse(a.t?.start ?? '');
  const bt = Date.parse(b.t?.start ?? '');
  // An unparseable timestamp sorts by seq alone rather than poisoning the
  // comparison with NaN, which would make the sort order implementation-
  // defined.
  if (Number.isFinite(at) && Number.isFinite(bt) && at !== bt) return at - bt;
  return a.seq - b.seq;
}

/**
 * Splits hops into row slots for two panes.
 *
 * `left` and `right` are client identities (or UNATTRIBUTED_CLIENT). A hop
 * matching neither goes to `withheld`. Selecting the same client on both
 * sides is allowed and puts each of its hops in both columns — a degenerate
 * but harmless arrangement, and refusing it would be a rule to explain for
 * no benefit.
 */
export function splitPanes(hops: readonly Hop[], idx: ClientIndex, left: string, right: string): SplitResult {
  // Unattributed resolves to the sentinel, not to '', so a pane deliberately
  // set to the unattributed bucket can hold those hops — while a pane with no
  // selection at all ('') still matches nothing.
  const paneOf = (h: Hop) => resolveClient(h, idx) ?? UNATTRIBUTED_CLIENT;

  const mine: Hop[] = [];
  const withheld: Hop[] = [];
  for (const h of hops) {
    const c = paneOf(h);
    if (c === left || c === right) mine.push(h);
    else withheld.push(h);
  }

  const ordered = [...mine].sort(before);
  const rows: PaneRow[] = ordered.map((h) => {
    const c = paneOf(h);
    return {
      key: h.seq,
      left: c === left ? h : null,
      right: c === right ? h : null,
    };
  });
  return { rows, withheld };
}
