import { useCallback, useEffect, useMemo, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { api, ApiError } from '../api/client';
import type { Hop, ServiceState, Topology } from '../api/types';
import { layoutClustered } from '../topology/layout';
import { layoutTrace, causalHopOrder } from '../topology/traceLayout';
import { heatTier, hopTimeline, type HeatTier } from '../topology/hopTimeline';
import TopologyGraph from '../components/TopologyGraph';
import { useUrlParam } from '../urlState';
import './TopologyView.css';

const POLL_MS = 5000;
/** Recent-activity window for the graph's per-node heat glow. */
const HEAT_WINDOW_MS = 60_000;
/** /api/traffic's `since` is a hop-sequence cursor, not a timestamp (see
    ensemble/server/routes.go's handleTraffic) — there is no server-side "last 60s" filter to
    ask for. `limit` alone means "the most recent N", which this then filters client-side by
    t.start. 500 comfortably covers a busy local stack's traffic in a minute without hauling
    down the whole session's history on every 5s poll. */
const TRAFFIC_SAMPLE = 500;

function heatByService(hops: Hop[]): Map<string, HeatTier> {
  const cutoff = Date.now() - HEAT_WINDOW_MS;
  const counts = new Map<string, number>();
  for (const h of hops) {
    if (new Date(h.t.start).getTime() < cutoff) continue;
    counts.set(h.to, (counts.get(h.to) ?? 0) + 1);
  }
  const max = Math.max(0, ...counts.values());
  const tiers = new Map<string, HeatTier>();
  if (max === 0) return tiers;
  counts.forEach((n, service) => tiers.set(service, heatTier(n / max)));
  return tiers;
}

function useTopologyPoll() {
  const [topology, setTopology] = useState<Topology | null>(null);
  const [statuses, setStatuses] = useState<ServiceState[] | null>(null);
  const [traffic, setTraffic] = useState<Hop[]>([]);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [t, s, hops] = await Promise.all([
        api.topology(),
        api.status(),
        api.traffic({ limit: TRAFFIC_SAMPLE }),
      ]);
      setTopology(t);
      setStatuses(s);
      setTraffic(hops);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'failed to reach the ensemble API');
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    const tick = () => {
      if (!cancelled) void refresh();
    };
    tick();
    const id = window.setInterval(tick, POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [refresh]);

  return { topology, statuses, traffic, error, refresh };
}

function useTracePoll(traceId: string | null) {
  const [hops, setHops] = useState<Hop[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!traceId) {
      setHops(null);
      setError(null);
      return;
    }
    let cancelled = false;
    api
      .trace(traceId)
      .then((r) => {
        if (!cancelled) {
          setHops(r.hops);
          setError(null);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setHops(null);
          setError(err instanceof ApiError ? err.message : `failed to load trace ${traceId}`);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [traceId]);

  return { hops, error };
}

function ServicePanel({
  state,
  onClose,
  onRestart,
  onFlip,
}: {
  state: ServiceState;
  onClose: () => void;
  onRestart: () => Promise<void>;
  onFlip: () => Promise<void>;
}) {
  const [busy, setBusy] = useState<'restart' | 'flip' | null>(null);

  const statusTone = state.status === 'healthy' ? 'green' : state.status === 'unhealthy' ? 'red' : 'amber';

  async function run(action: 'restart' | 'flip', fn: () => Promise<void>) {
    setBusy(action);
    try {
      await fn();
    } finally {
      setBusy(null);
    }
  }

  return (
    <aside className="topo-panel">
      <div className="topo-panel__header">
        <h3>{state.name}</h3>
        <button type="button" className="topo-panel__close" onClick={onClose} aria-label="close">
          ×
        </button>
      </div>
      <div className="topo-panel__row">
        <Badge tone={statusTone}>{state.status}</Badge>
        <Badge tone="neutral">{state.placement}</Badge>
      </div>
      <dl className="topo-panel__meta">
        {state.pid !== undefined && (
          <>
            <dt>pid</dt>
            <dd>{state.pid}</dd>
          </>
        )}
        {state.port !== undefined && (
          <>
            <dt>port</dt>
            <dd>{state.port}</dd>
          </>
        )}
        {state.proxyPort !== undefined && (
          <>
            <dt>proxy port</dt>
            <dd>{state.proxyPort}</dd>
          </>
        )}
        {state.startedAt && (
          <>
            <dt>started</dt>
            <dd>{new Date(state.startedAt).toLocaleTimeString()}</dd>
          </>
        )}
      </dl>
      {state.lastErr && <p className="topo-panel__err">{state.lastErr}</p>}
      <div className="topo-panel__actions">
        <button
          type="button"
          disabled={busy !== null}
          onClick={() => void run('restart', onRestart)}
        >
          {busy === 'restart' ? <Spinner /> : 'Restart'}
        </button>
        <button type="button" disabled={busy !== null} onClick={() => void run('flip', onFlip)}>
          {busy === 'flip' ? <Spinner /> : `Flip to ${state.placement === 'docker' ? 'native' : 'docker'}`}
        </button>
      </div>
    </aside>
  );
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

function HopTimingPanel({
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

  return (
    <div className="topo-hop-panel">
      {ordered.map((h, i) => {
        const t = timings[i];
        // injectedDelayMs runs BEFORE the upstream clock starts (see hopTimeline.ts's
        // durationOf), so the hatched segment is the bar's leading edge — the caller was
        // blocked on artificial latency before any real work began.
        const delayFrac = delayFraction(h);
        return (
          <button
            type="button"
            key={`${h.seq}-${h.to}`}
            className={`topo-hop-row${selectedHop === h.seq ? ' topo-hop-row-selected' : ''}`}
            onClick={() => onSelectHop(h.seq)}
          >
            <span className="topo-hop-meta">
              #{h.seq} {h.from ?? 'client'} → {h.to}
              {h.method && <span className="topo-hop-method"> {h.method}</span>}
              {h.status !== undefined && <span className="topo-hop-status"> {h.status}</span>}
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

export default function TopologyView() {
  const { topology, statuses, traffic, error, refresh } = useTopologyPoll();
  const [traceId, setTraceId] = useUrlParam('trace');
  const { hops: traceHops, error: traceError } = useTracePoll(traceId);

  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [selectedHop, setSelectedHop] = useState<number | null>(null);
  const [expandedBundles, setExpandedBundles] = useState<Set<string>>(new Set());

  // Leaving trace mode drops whatever was selected inside it — a stale hop selection
  // pointing at a node the cluster graph doesn't have would just dim nothing, silently.
  useEffect(() => {
    setSelectedHop(null);
    setSelectedNodeId(null);
  }, [traceId]);

  const statusMap = useMemo(() => new Map((statuses ?? []).map((s) => [s.name, s])), [statuses]);
  const nodeHeat = useMemo(() => heatByService(traffic), [traffic]);

  const layout = useMemo(() => {
    if (traceId) return traceHops ? layoutTrace(traceHops) : null;
    return topology ? layoutClustered(topology, statusMap, expandedBundles) : null;
  }, [traceId, traceHops, topology, statusMap, expandedBundles]);

  const toggleBundle = useCallback((key: string) => {
    setExpandedBundles((cur) => {
      const next = new Set(cur);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  async function restart(name: string) {
    await api.restart(name);
    await refresh();
  }

  async function flip(name: string) {
    await api.flip(name);
    await refresh();
  }

  const selectedState = selectedNodeId ? statusMap.get(selectedNodeId) : undefined;

  if (error) {
    return (
      <div className="topo-view topo-view--error">
        <Badge tone="red">offline</Badge>
        <span>{error}</span>
      </div>
    );
  }

  if (!layout) {
    return (
      <div className="topo-view topo-view--loading">
        <Spinner />
        <span>{traceId ? `loading trace ${traceId}…` : 'loading topology…'}</span>
      </div>
    );
  }

  return (
    <div className="topo-view">
      {traceId && (
        <div className="topo-view__trace-bar">
          <Badge tone="blue">trace {traceId}</Badge>
          <button type="button" onClick={() => setTraceId(null)}>
            back to topology
          </button>
          {traceError && <span className="topo-view__trace-error">{traceError}</span>}
        </div>
      )}
      <div className="topo-view__body">
        <TopologyGraph
          layout={layout}
          showLegend={!traceId}
          onToggleBundle={traceId ? undefined : toggleBundle}
          onSelectNode={traceId ? undefined : setSelectedNodeId}
          nodeHeat={traceId ? undefined : nodeHeat}
          selectedHop={selectedHop}
          onSelectHop={setSelectedHop}
        />
        {!traceId && selectedState && (
          <ServicePanel
            state={selectedState}
            onClose={() => setSelectedNodeId(null)}
            onRestart={() => restart(selectedState.name)}
            onFlip={() => flip(selectedState.name)}
          />
        )}
      </div>
      {traceId && traceHops && (
        <HopTimingPanel hops={traceHops} selectedHop={selectedHop} onSelectHop={setSelectedHop} />
      )}
    </div>
  );
}
