// Live traffic tail: seeds from GET /api/traffic, then stays open on the
// SSE stream for the rest of the session. Follow mode controls auto-scroll
// only — the SSE subscription itself runs for the component's whole
// lifetime, so switching follow off just freezes the viewport, it never
// drops data out of the ring.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Badge, Spinner, Tabs } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import { subscribeHops } from '../api/sse';
import type { Hop } from '../api/types';
import { categoryOf } from '../topology/categories';
import { collapseGatewayHops } from '../topology/gatewayCollapse';
import HopTable from '../components/HopTable';
import HopDetail from '../components/HopDetail';
import TraceDrawer from '../components/TraceDrawer';
import './TrafficView.css';

/** Seed page size for the initial GET /api/traffic — comfortably covers a
 * busy local stack's recent history without hauling down the whole
 * session. */
const INITIAL_LIMIT = 500;
/** Client-side ring cap once the SSE stream is live — bounds memory/DOM
 * for a session left open a long time; oldest hops fall off the front. */
const RING_MAX = 2000;
/** How far from the bottom (px) still counts as "at the bottom" — a
 * scrollbar rendering a px or two short of true bottom shouldn't read as
 * "the user scrolled up". */
const BOTTOM_SLOP_PX = 24;

type SessionFilter = 'all' | 'session' | 'ambient';

const SESSION_FILTER_ITEMS = [
  { id: 'all', label: 'all' },
  { id: 'session', label: 'session' },
  { id: 'ambient', label: 'ambient' },
];

function useHopRing() {
  const [hops, setHops] = useState<Hop[]>([]);
  // The seed load and the live SSE stream are one race-safety problem, not two: the initial
  // GET races nothing else here (deps: []), so useAsync's generation guard alone is enough to
  // keep a slow/duplicate seed load from clobbering hops the stream has already appended —
  // the subscription itself only ever starts once, from the effect below, keyed on the
  // load's own result rather than re-deriving its own cancellation flag.
  const { data: initial, error, loading } = useAsync(() => api.traffic({ limit: INITIAL_LIMIT }), []);

  useEffect(() => {
    if (initial === null) return;
    setHops(initial);
    const lastSeq = initial.reduce((max, h) => Math.max(max, h.seq), 0);
    const unsubscribe = subscribeHops(lastSeq, (hop) => {
      setHops((cur) => {
        // The stream can, at worst, redeliver the cursor hop itself on
        // reconnect — never accept anything at or behind what's
        // already at the tail. Comparing against the tail alone (rather
        // than requiring monotonic growth from the ring's start) is also
        // what makes `clear` safe: after it empties the ring, the very
        // next delivered hop always passes (cur.length === 0) regardless
        // of its seq.
        if (cur.length > 0 && hop.seq <= cur[cur.length - 1].seq) return cur;
        const next = cur.length >= RING_MAX ? cur.slice(cur.length - RING_MAX + 1) : cur.slice();
        next.push(hop);
        return next;
      });
    });
    return unsubscribe;
  }, [initial]);

  // Visual-only: empties the client-side ring so the table reads clean.
  // The SSE subscription above keeps running — new hops still land as
  // they happen — and nothing server-side is touched, so a page reload
  // (or another dashboard tab) still sees the full history.
  const clear = useCallback(() => setHops([]), []);

  return { hops, clear, error: error ? messageOf(error, 'failed to reach the ensemble API') : null, loading };
}

export default function TrafficView() {
  const { hops, clear, error, loading } = useHopRing();
  // Best-effort: the gateway show/hide split is a nice-to-have, so a topology fetch failure
  // just leaves showGateways' default (collapse nothing configured) rather than erroring the
  // whole Traffic tab.
  const { data: topology } = useAsync(() => api.topology(), []);

  const [textFilter, setTextFilter] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  // Off by default: a CORS preflight ensemble answers itself is real debugging signal but noisy
  // (one per cross-origin request), so it stays out of the way until asked for.
  const [showPreflight, setShowPreflight] = useState(false);
  const [sessionFilter, setSessionFilter] = useState<SessionFilter>('all');
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);
  const [following, setFollowing] = useState(true);
  // Off by default: a gateway hop collapses into its target's unless the gateway opted in via
  // ensemble.yaml's `expose_in_traffic: true`, or the user flips this on for the session.
  const [showGateways, setShowGateways] = useState(false);
  const [drawerTraceId, setDrawerTraceId] = useState<string | null>(null);

  const scrollRef = useRef<HTMLDivElement | null>(null);

  const categoryByName = useMemo(() => {
    const m = new Map(topology?.nodes.map((n) => [n.name, categoryOf(n)]) ?? []);
    return m;
  }, [topology]);

  // Gateways with `expose_in_traffic: true` stay visible regardless of the session toggle;
  // flipping showGateways on reveals every gateway hop, overriding that default for the
  // session without touching config.
  const collapse = useMemo(() => {
    if (showGateways) return new Set<string>();
    return new Set(
      (topology?.nodes ?? [])
        .filter((n) => n.category === 'gateway' && !n.exposeInTraffic)
        .map((n) => n.name),
    );
  }, [topology, showGateways]);

  const collapsed = useMemo(() => collapseGatewayHops(hops, collapse), [hops, collapse]);

  const filtered = useMemo(() => {
    const needle = textFilter.trim().toLowerCase();
    return collapsed.filter((h) => {
      if (needle && !`${h.to} ${h.path ?? ''}`.toLowerCase().includes(needle)) return false;
      if (errorsOnly && !((h.status ?? 0) >= 400 || h.err)) return false;
      if (!showPreflight && h.preflight) return false;
      if (sessionFilter === 'session' && !h.session) return false;
      if (sessionFilter === 'ambient' && h.session) return false;
      return true;
    });
  }, [collapsed, textFilter, errorsOnly, showPreflight, sessionFilter]);

  // From `collapsed`, not raw `hops` — the detail panel should mirror whatever `to` the table
  // row it was opened from actually shows.
  const selectedHop = useMemo(() => collapsed.find((h) => h.seq === selectedSeq) ?? null, [collapsed, selectedSeq]);

  // Auto-scroll to bottom whenever the visible rows grow, but only while
  // following — this is the ONLY effect of the toggle; the SSE
  // subscription itself never stops.
  useEffect(() => {
    if (!following) return;
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
  }, [filtered, following]);

  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_SLOP_PX;
    if (!atBottom && following) {
      // The user scrolled away from the tail — pause, don't fight them.
      setFollowing(false);
    }
  }, [following]);

  const resumeFollowing = useCallback(() => {
    setFollowing(true);
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, []);

  const handleClear = useCallback(() => {
    clear();
    setSelectedSeq(null);
  }, [clear]);

  // Datadog-style: opens the trace drawer in place, docked over the right side of this same
  // view, rather than navigating to the Topology tab — closing it returns to Traffic exactly
  // where it was left.
  const viewTrace = useCallback((traceId: string) => setDrawerTraceId(traceId), []);

  if (error) {
    return (
      <div className="traffic-view traffic-view--error">
        <Badge tone="red">offline</Badge>
        <span>{error}</span>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="traffic-view traffic-view--loading">
        <Spinner />
        <span>loading traffic…</span>
      </div>
    );
  }

  return (
    <div className="traffic-view">
      <div className="traffic-view__toolbar">
        <input
          type="text"
          className="traffic-view__search"
          placeholder="filter by service or path…"
          value={textFilter}
          onChange={(e) => setTextFilter(e.target.value)}
        />
        <button
          type="button"
          className={`traffic-view__toggle${errorsOnly ? ' traffic-view__toggle--active' : ''}`}
          onClick={() => setErrorsOnly((v) => !v)}
        >
          errors only
        </button>
        <button
          type="button"
          className={`traffic-view__toggle${showPreflight ? ' traffic-view__toggle--active' : ''}`}
          onClick={() => setShowPreflight((v) => !v)}
        >
          show CORS preflight
        </button>
        <Tabs
          items={SESSION_FILTER_ITEMS}
          active={sessionFilter}
          onSelect={(id) => setSessionFilter(id as SessionFilter)}
        />
        <span className="traffic-view__count">
          {filtered.length} / {collapsed.length}
        </span>
        <button
          type="button"
          className={`traffic-view__toggle${showGateways ? ' traffic-view__toggle--active' : ''}`}
          onClick={() => setShowGateways((v) => !v)}
          title="Show client -> gateway -> target as separate hops instead of collapsing the gateway leg"
        >
          {showGateways ? 'hide gateways' : 'show gateways'}
        </button>
        <button
          type="button"
          className="traffic-view__clear"
          onClick={handleClear}
          disabled={hops.length === 0}
          title="Clear the traffic list (visual only — new requests still stream in)"
        >
          clear
        </button>
        <button
          type="button"
          className={`traffic-view__follow${following ? ' traffic-view__follow--active' : ''}`}
          onClick={() => (following ? setFollowing(false) : resumeFollowing())}
        >
          {following ? '● following' : '○ resume'}
        </button>
      </div>
      <div className="traffic-view__body">
        <div className="traffic-view__table" ref={scrollRef} onScroll={handleScroll}>
          {filtered.length === 0 ? (
            <p className="traffic-view__empty">no traffic matches these filters</p>
          ) : (
            <HopTable
              hops={filtered}
              selectedSeq={selectedSeq}
              onSelectHop={(h) => setSelectedSeq(h.seq)}
              onViewTrace={viewTrace}
            />
          )}
        </div>
        <div className="traffic-view__detail">
          {selectedHop ? (
            <HopDetail hop={selectedHop} onClose={() => setSelectedSeq(null)} onViewTrace={viewTrace} />
          ) : (
            <p className="traffic-view__detail-empty">select a request to inspect it</p>
          )}
        </div>
      </div>
      <TraceDrawer
        traceId={drawerTraceId}
        onClose={() => setDrawerTraceId(null)}
        categoryByName={categoryByName}
      />
    </div>
  );
}
