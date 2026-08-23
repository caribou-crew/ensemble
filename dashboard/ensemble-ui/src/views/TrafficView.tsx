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
import { writeParams } from '../urlState';
import HopTable from '../components/HopTable';
import HopDetail from '../components/HopDetail';
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
        // already at the tail.
        if (cur.length > 0 && hop.seq <= cur[cur.length - 1].seq) return cur;
        const next = cur.length >= RING_MAX ? cur.slice(cur.length - RING_MAX + 1) : cur.slice();
        next.push(hop);
        return next;
      });
    });
    return unsubscribe;
  }, [initial]);

  return { hops, error: error ? messageOf(error, 'failed to reach the ensemble API') : null, loading };
}

export default function TrafficView() {
  const { hops, error, loading } = useHopRing();

  const [textFilter, setTextFilter] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [sessionFilter, setSessionFilter] = useState<SessionFilter>('all');
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);
  const [following, setFollowing] = useState(true);

  const scrollRef = useRef<HTMLDivElement | null>(null);

  const filtered = useMemo(() => {
    const needle = textFilter.trim().toLowerCase();
    return hops.filter((h) => {
      if (needle && !`${h.to} ${h.path ?? ''}`.toLowerCase().includes(needle)) return false;
      if (errorsOnly && !((h.status ?? 0) >= 400 || h.err)) return false;
      if (sessionFilter === 'session' && !h.session) return false;
      if (sessionFilter === 'ambient' && h.session) return false;
      return true;
    });
  }, [hops, textFilter, errorsOnly, sessionFilter]);

  const selectedHop = useMemo(() => hops.find((h) => h.seq === selectedSeq) ?? null, [hops, selectedSeq]);

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

  const viewTrace = useCallback((traceId: string) => {
    // App's ?view= tab state and TopologyView's ?trace= state are separate
    // useUrlParam instances; writeParams alone patches the URL but neither
    // hook notices without a popstate — the same pattern used to
    // synchronize an external URL patch elsewhere in this dashboard (see
    // TopologyView.trace-race.test.ts).
    writeParams({ view: 'topology', trace: traceId });
    window.dispatchEvent(new PopStateEvent('popstate'));
  }, []);

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
        <Tabs
          items={SESSION_FILTER_ITEMS}
          active={sessionFilter}
          onSelect={(id) => setSessionFilter(id as SessionFilter)}
        />
        <span className="traffic-view__count">
          {filtered.length} / {hops.length}
        </span>
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
    </div>
  );
}
