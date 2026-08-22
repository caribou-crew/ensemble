import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Badge, Spinner } from "@ensemble/design-system";
import { api, messageOf } from "../api/client";
import type {
  Hop,
  ServiceState,
  Topology,
  ProfileInfo,
  ProfilesState,
} from "../api/types";
import { categoryOf } from "../topology/categories";
import { layoutClustered } from "../topology/layout";
import { layoutTrace, causalHopOrder } from "../topology/traceLayout";
import {
  heatTier,
  hopDepths,
  hopTimeline,
  type HeatTier,
} from "../topology/hopTimeline";
import type { CategoryId } from "../topology/types";
import TopologyGraph from "../components/TopologyGraph";
import InlineError from "../components/InlineError";
import { useUrlParam } from "../urlState";
import "./TopologyView.css";

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

/** Profiles ("lanes") and their live state, polled alongside the topology. Toggling one
    switches it on the orchestrator and then re-fetches, so the strip reflects what the
    server did rather than what was clicked. */
function useProfiles(refreshTopology: () => Promise<void>) {
  const [profiles, setProfiles] = useState<ProfilesState | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setProfiles(await api.profiles());
    } catch {
      // Silently ignored. NOT "leaves the strip at its last known state" (F.18) — `profiles`
      // starts `null`, ProfileStrip only mounts once it is non-null, and a failure here never
      // sets it to anything — so a profiles miss on the very first load means the lane strip
      // never appears at all, not that it freezes at a prior value. The topology poll already
      // surfaces connectivity errors for the rest of the view, which is the actual reason a
      // second error banner here would be redundant.
    }
  }, []);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), 5000);
    return () => clearInterval(id);
  }, [load]);

  const toggle = useCallback(
    async (p: ProfileInfo) => {
      setBusy(p.name);
      setError(null);
      try {
        setProfiles(
          p.active
            ? await api.profileDown(p.name)
            : await api.profileUp(p.name),
        );
        await refreshTopology();
      } catch (err) {
        setError(messageOf(err, `could not switch profile ${p.name}`));
      } finally {
        setBusy(null);
      }
    },
    [refreshTopology],
  );

  return { profiles, busy, error, toggle };
}

function ProfileStrip({
  profiles,
  busy,
  error,
  onToggle,
}: {
  profiles: ProfilesState;
  busy: string | null;
  error: string | null;
  onToggle: (p: ProfileInfo) => void;
}) {
  if (profiles.profiles.length === 0) return null;
  return (
    <div className="topo-view__profiles" role="group" aria-label="profiles">
      <span className="topo-view__profiles-label">lanes</span>
      {profiles.profiles.map((p) => (
        <button
          key={p.name}
          type="button"
          className={
            p.active
              ? "topo-view__profile topo-view__profile--active"
              : "topo-view__profile"
          }
          aria-pressed={p.active}
          disabled={busy !== null}
          title={p.services.join(", ")}
          onClick={() => onToggle(p)}
        >
          {busy === p.name ? <Spinner /> : null}
          {p.name}
        </button>
      ))}
      {error && (
        <InlineError message={error} className="topo-view__trace-error" />
      )}
    </div>
  );
}

function useTopologyPoll() {
  const [topology, setTopology] = useState<Topology | null>(null);
  const [statuses, setStatuses] = useState<ServiceState[] | null>(null);
  const [traffic, setTraffic] = useState<Hop[]>([]);
  const [error, setError] = useState<string | null>(null);

  // Generation guard: refresh() is called both from the 5s poll interval AND out-of-band
  // from restart()/flip() (:321-329), so more than one call can be in flight at once with no
  // guarantee which resolves first. A `cancelled` boolean threaded from the effect (App.tsx's
  // shape) only covers unmount — it says nothing about an OLDER refresh() resolving after a
  // NEWER one, which is exactly the case that matters here (see final review I3). Comparing
  // against a ref counter bumped on every entry means only the latest-STARTED call's result
  // is ever applied, regardless of resolution order.
  const generationRef = useRef(0);

  const refresh = useCallback(async () => {
    const generation = ++generationRef.current;
    try {
      const [t, s, hops] = await Promise.all([
        api.topology(),
        api.status(),
        api.traffic({ limit: TRAFFIC_SAMPLE }),
      ]);
      if (generation !== generationRef.current) return;
      setTopology(t);
      setStatuses(s);
      setTraffic(hops);
      setError(null);
    } catch (err) {
      if (generation !== generationRef.current) return;
      setError(messageOf(err, "failed to reach the ensemble API"));
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
      // Bumping here retires every in-flight refresh() too. `cancelled` only stops the
      // interval from STARTING another one; a request already awaiting Promise.all would
      // still pass the :62 guard and set state after unmount. React 19 makes those setters
      // no-ops rather than warnings, so this is hygiene, not a live bug — but it is the one
      // clause of final review I3 the generation counter did not yet cover.
      generationRef.current++;
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
    // Clear the previous trace's hops immediately — otherwise switching ?trace= ids flashes
    // the OLD trace's graph/waterfall until the new fetch resolves.
    setHops(null);
    setError(null);
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
          setError(messageOf(err, `failed to load trace ${traceId}`));
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
  variants,
  onClose,
  onRestart,
  onFlip,
  onSetVariant,
}: {
  state: ServiceState;
  /** The service's declared `variants:` (from the topology node); empty = no selector. */
  variants: string[];
  onClose: () => void;
  onRestart: () => Promise<void>;
  onFlip: () => Promise<void>;
  onSetVariant: (variant: string) => Promise<void>;
}) {
  const [busy, setBusy] = useState<"restart" | "flip" | "variant" | null>(null);

  const statusTone =
    state.status === "healthy"
      ? "green"
      : state.status === "unhealthy"
        ? "red"
        : "amber";

  async function run(
    action: "restart" | "flip" | "variant",
    fn: () => Promise<void>,
  ) {
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
        <button
          type="button"
          className="topo-panel__close"
          onClick={onClose}
          aria-label="close"
        >
          ×
        </button>
      </div>
      <div className="topo-panel__row">
        <Badge tone={statusTone}>{state.status}</Badge>
        <Badge tone="neutral">{state.placement}</Badge>
        {state.variant && <Badge tone="neutral">{state.variant}</Badge>}
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
          onClick={() => void run("restart", onRestart)}
        >
          {busy === "restart" ? <Spinner /> : "Restart"}
        </button>
        <button
          type="button"
          disabled={busy !== null}
          onClick={() => void run("flip", onFlip)}
        >
          {busy === "flip" ? (
            <Spinner />
          ) : (
            `Flip to ${state.placement === "docker" ? "native" : "docker"}`
          )}
        </button>
        {variants.length > 0 && (
          <label className="topo-panel__variant">
            <span>variant</span>
            <select
              value={state.variant ?? ""}
              disabled={busy !== null}
              onChange={(e) =>
                void run("variant", () => onSetVariant(e.target.value))
              }
            >
              {variants.map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
            </select>
            {busy === "variant" && <Spinner />}
          </label>
        )}
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
                {h.from ?? "client"}
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

export default function TopologyView() {
  const { topology, statuses, traffic, error, refresh } = useTopologyPoll();
  const lanes = useProfiles(refresh);
  const [traceId, setTraceId] = useUrlParam("trace");
  const { hops: traceHops, error: traceError } = useTracePoll(traceId);

  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [selectedHop, setSelectedHop] = useState<number | null>(null);
  const [expandedBundles, setExpandedBundles] = useState<Set<string>>(
    new Set(),
  );

  // Leaving trace mode drops whatever was selected inside it — a stale hop selection
  // pointing at a node the cluster graph doesn't have would just dim nothing, silently.
  useEffect(() => {
    setSelectedHop(null);
    setSelectedNodeId(null);
  }, [traceId]);

  const statusMap = useMemo(
    () => new Map((statuses ?? []).map((s) => [s.name, s])),
    [statuses],
  );
  const nodeHeat = useMemo(() => heatByService(traffic), [traffic]);

  // layoutTrace stays a pure (hops) => GraphLayout function with no per-node category (see
  // traceLayout.ts) — every trace-mode node renders 'other'. TopologyView is the seam that
  // already holds a live Topology fetch, so it can restore the real accent by name after the
  // fact; a hop endpoint the topology doesn't know (an untraced upstream, or the synthetic
  // "client" root) is left 'other', same as before.
  const categoryByName = useMemo(() => {
    const m = new Map<string, CategoryId>();
    (topology?.nodes ?? []).forEach((n) => m.set(n.name, categoryOf(n)));
    return m;
  }, [topology]);

  const layout = useMemo(() => {
    if (traceId) {
      if (!traceHops) return null;
      const base = layoutTrace(traceHops);
      return {
        ...base,
        nodes: base.nodes.map((n) => {
          const category = categoryByName.get(n.id);
          return category ? { ...n, category } : n;
        }),
      };
    }
    return topology
      ? layoutClustered(topology, statusMap, expandedBundles)
      : null;
  }, [
    traceId,
    traceHops,
    topology,
    statusMap,
    expandedBundles,
    categoryByName,
  ]);

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

  async function setVariant(name: string, variant: string) {
    await api.setVariant(name, variant);
    await refresh();
  }

  const selectedState = selectedNodeId
    ? statusMap.get(selectedNodeId)
    : undefined;

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
        <span>
          {traceId ? `loading trace ${traceId}…` : "loading topology…"}
        </span>
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
          {traceError && (
            <InlineError
              message={traceError}
              className="topo-view__trace-error"
            />
          )}
        </div>
      )}
      {!traceId && lanes.profiles && (
        <ProfileStrip
          profiles={lanes.profiles}
          busy={lanes.busy}
          error={lanes.error}
          onToggle={(p) => void lanes.toggle(p)}
        />
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
            variants={
              topology?.nodes.find((n) => n.name === selectedState.name)
                ?.variants ?? []
            }
            onClose={() => setSelectedNodeId(null)}
            onRestart={() => restart(selectedState.name)}
            onFlip={() => flip(selectedState.name)}
            onSetVariant={(v) => setVariant(selectedState.name, v)}
          />
        )}
      </div>
      {traceId && traceHops && (
        <HopTimingPanel
          hops={traceHops}
          selectedHop={selectedHop}
          onSelectHop={setSelectedHop}
        />
      )}
    </div>
  );
}
