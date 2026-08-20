import type { Hop } from '../api/types';

/** A hop's true wall-clock span, start to finish. `t.start` is stamped before an injected
    latency rule runs, but `doneMs`/`firstByteMs` are measured from AFTER it (see
    core/proxy/proxy.go: "Artificial latency runs before the upstream clock starts") — so
    the recorded upstream duration alone understates how long the caller actually waited.
    Adding injectedDelayMs back in is what makes this hop's bar end where the next one on the
    same caller frame can legitimately begin, and what keeps hopDepths' active-frame clock
    from closing a parent's frame before control actually returned to it. Falls back to
    firstByteMs for a hop that's still streaming (no doneMs yet), and to 0 for one with no
    timing signal at all rather than propagating NaN through every downstream percentage. */
function durationOf(h: Hop): number {
  return (h.injectedDelayMs ?? 0) + (h.t.doneMs ?? h.t.firstByteMs ?? 0);
}

/** ensemble's Hop.from is optional — the root hop of a trace has no caller (nothing calls
    the entry service). The old TraceHop always carried an explicit 'client' pseudo-caller;
    this substitutes the same sentinel so the call-stack reconstruction below still has a
    `to` to match against. */
function callerOf(h: Hop): string {
  return h.from ?? 'client';
}

/**
 * Nesting depth of each hop, reconstructed from a synchronous call stack: hop i is a child
 * of the innermost still-open ancestor whose `to` matches hop i's caller (control is
 * currently inside that service when it makes the next call). Hops are walked in start-time
 * order, not array order — the trace log's storage order isn't guaranteed to match it.
 * Returns depths indexed to match the input array's original order.
 */
export function hopDepths(hops: Hop[]): number[] {
  const withIndex = hops.map((h, i) => ({ h, i }));
  withIndex.sort((a, b) => (a.h.t.start < b.h.t.start ? -1 : a.h.t.start > b.h.t.start ? 1 : 0));

  const depths = new Array<number>(hops.length).fill(0);
  const active: { to: string; end: number }[] = [];

  for (const { h, i } of withIndex) {
    const start = new Date(h.t.start).getTime();
    while (active.length > 0) {
      const top = active[active.length - 1];
      if (top.end <= start || top.to !== callerOf(h)) active.pop();
      else break;
    }
    depths[i] = active.length;
    active.push({ to: h.to, end: start + durationOf(h) });
  }
  return depths;
}

export interface HopTiming {
  /** % offset from the trace's earliest start. */
  startPct: number;
  /** % of the trace's total span; floored so a 0ms hop still draws a visible sliver. */
  widthPct: number;
  /** durationMs relative to the slowest hop in this trace, 0..1. Width already encodes exact
      relative duration, so this only feeds heatTier()'s discrete outlier flag, not a color
      ramp — a continuous hue rotation is non-monotonic in contrast and fails on red/green
      colorblindness (measured: the slowest hop rendered as the FAINTEST bar on screen). */
  heat: number;
}

export type HeatTier = 'normal' | 'warm' | 'hot';

/** Bucket a heat value into a discrete, status-style signal instead of a continuous ramp —
    "is this one worth noticing" rather than a precise magnitude (the bar's width already
    gives you that). Also reused by TopologyView for the graph's per-node "recent activity"
    glow (a hop-count ratio run through the same 0.5/0.85 thresholds), not just this file's
    own within-trace heat value — the bucketing makes sense for any 0..1 "how hot is this"
    ratio, not only a hop duration. */
export function heatTier(heat: number): HeatTier {
  if (heat >= 0.85) return 'hot';
  if (heat >= 0.5) return 'warm';
  return 'normal';
}

/**
 * Position + width of each hop's bar within the trace's overall time span, for a waterfall
 * view — overlapping bars read as parallel calls, bars in file as sequential ones.
 */
export function hopTimeline(hops: Hop[]): HopTiming[] {
  if (hops.length === 0) return [];
  const starts = hops.map((h) => new Date(h.t.start).getTime());
  const ends = hops.map((h, i) => starts[i] + durationOf(h));
  const min = Math.min(...starts);
  const max = Math.max(...ends);
  const span = Math.max(1, max - min);
  const maxDuration = Math.max(1, ...hops.map(durationOf));
  return hops.map((h, i) => {
    const startPct = ((starts[i] - min) / span) * 100;
    const widthPct = Math.min(100 - startPct, Math.max(1, (durationOf(h) / span) * 100));
    return { startPct, widthPct, heat: durationOf(h) / maxDuration };
  });
}
