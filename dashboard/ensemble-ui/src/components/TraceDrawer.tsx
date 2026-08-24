// Datadog-style trace panel: docked to the right edge of the screen so the Traffic list stays
// visible (dimmed) on the left, rather than navigating away to the Topology tab. Reuses the
// exact same waterfall + graph the Topology tab's trace mode renders (see TraceWaterfall.tsx).
import { useEffect, useMemo, useState } from "react";
import { Badge, Spinner } from "@ensemble/design-system";
import { layoutTrace } from "../topology/traceLayout";
import type { CategoryId } from "../topology/types";
import TopologyGraph from "./TopologyGraph";
import InlineError from "./InlineError";
import HopDetail from "./HopDetail";
import { useTracePoll, HopTimingPanel } from "./TraceWaterfall";
import "./TraceDrawer.css";

export default function TraceDrawer({
  traceId,
  onClose,
  categoryByName,
}: {
  traceId: string | null;
  onClose: () => void;
  /** Real node categories from the live topology, patched onto trace-mode's otherwise
   * category-less nodes — same trick TopologyView's trace mode uses. */
  categoryByName?: Map<string, CategoryId>;
}) {
  const { hops, error } = useTracePoll(traceId);
  const [selectedHop, setSelectedHop] = useState<number | null>(null);

  // A fresh trace drops whatever hop was selected inside the previous one.
  useEffect(() => setSelectedHop(null), [traceId]);

  useEffect(() => {
    if (!traceId) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [traceId, onClose]);

  const layout = useMemo(() => {
    if (!hops) return null;
    const base = layoutTrace(hops);
    return {
      ...base,
      nodes: base.nodes.map((n) => {
        const category = categoryByName?.get(n.id);
        return category ? { ...n, category } : n;
      }),
    };
  }, [hops, categoryByName]);

  const selectedHopData = useMemo(
    () => hops?.find((h) => h.seq === selectedHop) ?? null,
    [hops, selectedHop],
  );

  if (!traceId) return null;

  return (
    <div className="trace-drawer">
      <button
        type="button"
        className="trace-drawer__backdrop"
        onClick={onClose}
        aria-label="close trace"
      />
      <div className="trace-drawer__panel" role="dialog" aria-label={`trace ${traceId}`}>
        <div className="trace-drawer__header">
          <Badge tone="blue">trace {traceId}</Badge>
          {error && <InlineError message={error} className="trace-drawer__error" />}
          <button type="button" className="trace-drawer__close" onClick={onClose} aria-label="close">
            ×
          </button>
        </div>
        {!layout ? (
          <div className="trace-drawer__loading">
            <Spinner />
            <span>loading trace…</span>
          </div>
        ) : (
          <>
            <div className="trace-drawer__graph">
              <TopologyGraph layout={layout} showLegend={false} selectedHop={selectedHop} onSelectHop={setSelectedHop} />
            </div>
            {hops && <HopTimingPanel hops={hops} selectedHop={selectedHop} onSelectHop={setSelectedHop} />}
            {selectedHopData && (
              <div className="trace-drawer__detail">
                <HopDetail hop={selectedHopData} onClose={() => setSelectedHop(null)} />
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
