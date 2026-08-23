// The trace-mode hop timing waterfall — extracted out of TopologyView so a second surface
// (TrafficView's trace drawer) can render the exact same waterfall without duplicating it.
import { useMemo } from "react";
import { useAsync } from "@ensemble/design-system/useAsync";
import { api, messageOf } from "../api/client";
import type { Hop } from "../api/types";
import { causalHopOrder } from "../topology/traceLayout";
import { heatTier, hopDepths, hopTimeline } from "../topology/hopTimeline";
import { INFERRED_CALLER_TITLE, isInferredCaller } from "./attribution";
import "./TraceWaterfall.css";

export function useTracePoll(traceId: string | null) {
  // useAsync clears `data` to null synchronously on every deps change — exactly the "clear the
  // previous trace's hops immediately" behaviour af48831 fixed by hand, now for free from the
  // hook (TopologyView.trace-race.test.ts's third case pins this).
  const { data: hops, error } = useAsync(async () => {
    if (!traceId) return null;
    const r = await api.trace(traceId);
    return r.hops;
  }, [traceId]);

  return { hops, error: error ? messageOf(error, `failed to load trace ${traceId}`) : null };
}

/** What fraction of a hop's bar (0..100) is the hatched, injected-delay leading segment.
    Mirrors hopTimeline.ts's durationOf() total (injectedDelayMs + doneMs/firstByteMs) so the
    hatch's width is always a sub-portion of the bar it's drawn inside. */
function delayFraction(h: Hop): number {
  const delay = h.injectedDelayMs ?? 0;
  if (delay <= 0) return 0;
  const total = delay + (h.t.doneMs ?? h.t.firstByteMs ?? 0);
  return total > 0 ? Math.min(100, (delay / total) * 100) : 0;
}

export function HopTimingPanel({
  hops,
  selectedHop,
  onSelectHop,
}: {
  hops: Hop[];
  selectedHop: number | null;
  onSelectHop: (hop: number) => void;
}) {
  const ordered = useMemo(() => causalHopOrder(hops), [hops]);
  const timings = useMemo(() => hopTimeline(ordered), [ordered]);
  // hopDepths sorts by start time internally and returns depths indexed to match its INPUT
  // array's order, not any canonical order — calling it with `ordered` (rather than the raw
  // `hops` prop) means depths[i] lines up directly with ordered[i] below, no re-keying by
  // seq needed.
  const depths = useMemo(() => hopDepths(ordered), [ordered]);

  return (
    <div className="topo-hop-panel">
      {ordered.map((h, i) => {
        const t = timings[i];
        const depth = depths[i] ?? 0;
        // injectedDelayMs runs BEFORE the upstream clock starts (see hopTimeline.ts's
        // durationOf), so the hatched segment is the bar's leading edge — the caller was
        // blocked on artificial latency before any real work began.
        const delayFrac = delayFraction(h);
        return (
          <button
            type="button"
            key={`${h.seq}-${h.to}`}
            className={`topo-hop-row${selectedHop === h.seq ? " topo-hop-row-selected" : ""}`}
            onClick={() => onSelectHop(h.seq)}
          >
            <span className="topo-hop-meta">
              <span className="topo-hop-seq">#{h.seq}</span>{" "}
              <span
                className="topo-hop-consumer"
                style={{ paddingLeft: depth * 12 }}
              >
                {depth > 0 && <span className="topo-hop-nest-glyph">↳</span>}
                {isInferredCaller(h) ? (
                  <span className="topo-hop-caller--inferred" title={INFERRED_CALLER_TITLE}>
                    {h.from}
                  </span>
                ) : (
                  h.from ?? "client"
                )}
              </span>
              {" → "}
              {h.to}
              {h.method && <span className="topo-hop-method"> {h.method}</span>}
              {h.status !== undefined && (
                <span className="topo-hop-status"> {h.status}</span>
              )}
            </span>
            <span className="topo-hop-track">
              <span
                className={`topo-hop-bar topo-hop-bar-${heatTier(t.heat)}`}
                style={{ left: `${t.startPct}%`, width: `${t.widthPct}%` }}
              >
                {delayFrac > 0 && (
                  <span
                    className="topo-hop-bar-delay"
                    style={{ width: `${delayFrac}%` }}
                    title={`+${h.injectedDelayMs}ms injected`}
                  />
                )}
              </span>
            </span>
          </button>
        );
      })}
    </div>
  );
}
