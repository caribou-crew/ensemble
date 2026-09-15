// Live traffic tail: seeds from GET /api/traffic, then stays open on the
// SSE stream for the rest of the session. Follow mode controls auto-scroll
// only — the SSE subscription itself runs for the component's whole
// lifetime, so switching follow off just freezes the viewport, it never
// drops data out of the ring.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import { subscribeHops } from '../api/sse';
import type { Hop } from '../api/types';
import { categoryOf } from '../topology/categories';
import { collapseGatewayHops } from '../topology/gatewayCollapse';
import HopTable from '../components/HopTable';
import HopDetail from '../components/HopDetail';
import TraceDrawer from '../components/TraceDrawer';
import QueryFilterInput from '../components/QueryFilterInput';
import { hopMatchesQuery, parseFilterToken, type FilterToken } from '../trafficFilter';
import {
  buildClientIndex,
  distinctClients,
  hasUnattributed,
  matchesClient,
  UNATTRIBUTED_CLIENT,
} from '../clientAttribution';
import { splitPanes } from '../splitPanes';
import SplitHopPanes from '../components/SplitHopPanes';
import { CLIENT_IDENTITY_TITLE } from '../components/attribution';
import { readParam, useUrlParam, writeParams } from '../urlState';
import './TrafficView.css';

/** Seed page size for the initial GET /api/traffic — comfortably covers a
 * busy local stack's recent history without hauling down the whole
 * session. */
const INITIAL_LIMIT = 500;
/** Client-side ring cap once the SSE stream is live — bounds memory/DOM
 * for a session left open a long time; oldest hops fall off the front. */
const RING_MAX = 2000;
/** Page size for each "load earlier" click against GET
 * /api/traffic/history. */
const HISTORY_PAGE_SIZE = 200;
/** How far from the bottom (px) still counts as "at the bottom" — a
 * scrollbar rendering a px or two short of true bottom shouldn't read as
 * "the user scrolled up". */
const BOTTOM_SLOP_PX = 24;

/** 'all' and 'ambient' are fixed buckets; anything else is a real
 * hop.session id, letting you isolate exactly one session once several are
 * live at once. */
type SessionFilter = 'all' | 'ambient' | string;

/** 'all' or a resolved client identity — including UNATTRIBUTED_CLIENT for
 * the hops that belong to none. Mirrors SessionFilter deliberately: the two
 * controls sit beside each other and answer the same shape of question. */
type ClientFilter = 'all' | string;

/** What the client selector calls the two buckets that aren't a plain app
 * name. `core/proxy.FallbackClient` is the literal string "client": a hop
 * records it when a client-identity header ARRIVED and was malformed, which
 * is a different fact from no identity at all, and a developer whose header
 * is wrong needs to see that rather than an empty pane. */
const FALLBACK_CLIENT = 'client';
const CLIENT_OPTION_LABELS: Record<string, string> = {
  [UNATTRIBUTED_CLIENT]: 'no client',
  [FALLBACK_CLIENT]: 'client (malformed header)',
};
function clientLabel(client: string): string {
  return CLIENT_OPTION_LABELS[client] ?? client;
}
const CLIENT_SELECT_TITLE =
  'Filter by originating client application. ' +
  CLIENT_IDENTITY_TITLE +
  ' A hop with no identity of its own inherits its trace\'s; a chain that propagated none belongs to no client.';

/** Matches HopTable's own truncation so a session reads the same wherever
 * it's shown (the per-row badge, this dropdown). */
function sessionLabel(session: string): string {
  return session.slice(0, 8);
}

function useHopRing() {
  const [hops, setHops] = useState<Hop[]>([]);
  // Hops paged in from disk via "load earlier" — always strictly older
  // than anything `oldestSeqRef` has ever pointed at, so `[...olderHops,
  // ...hops]` stays chronological without a merge/sort step. Kept apart
  // from `hops` so the live ring's RING_MAX eviction (which only ever
  // trims `hops`' front) can't silently swallow a page the user
  // deliberately loaded.
  const [olderHops, setOlderHops] = useState<Hop[]>([]);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [noMoreHistory, setNoMoreHistory] = useState(false);
  const [historyError, setHistoryError] = useState<string | null>(null);
  // The paging cursor for the next "load earlier" call. A ref, not
  // derived from `hops`/`olderHops` state, so it survives `clear()`
  // (visual-only) and RING_MAX evicting the live ring's front — both of
  // which would otherwise erase the one piece of state that remembers
  // how far back the user has already paged.
  const oldestSeqRef = useRef<number | null>(null);

  // The seed load and the live SSE stream are one race-safety problem, not two: the initial
  // GET races nothing else here (deps: []), so useAsync's generation guard alone is enough to
  // keep a slow/duplicate seed load from clobbering hops the stream has already appended —
  // the subscription itself only ever starts once, from the effect below, keyed on the
  // load's own result rather than re-deriving its own cancellation flag.
  const { data: initial, error, loading } = useAsync(() => api.traffic({ limit: INITIAL_LIMIT }), []);

  useEffect(() => {
    if (initial === null) return;
    setHops(initial);
    let lastSeq = 0;
    for (const h of initial) {
      lastSeq = Math.max(lastSeq, h.seq);
      oldestSeqRef.current = oldestSeqRef.current === null ? h.seq : Math.min(oldestSeqRef.current, h.seq);
    }
    const unsubscribe = subscribeHops(
      lastSeq,
      (hop) => {
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
      },
      (hop) => {
        // hop.updated: a streaming hop finalizing in place — same seq,
        // now with its duration and final body. Upsert, never append; a
        // seq no longer in the ring (evicted, or cleared) is dropped.
        setHops((cur) => {
          const i = cur.findIndex((h) => h.seq === hop.seq);
          if (i < 0) return cur;
          const next = cur.slice();
          next[i] = hop;
          return next;
        });
      },
    );
    return unsubscribe;
  }, [initial]);

  // Visual-only: empties the client-side ring so the table reads clean.
  // The SSE subscription above keeps running — new hops still land as
  // they happen — and nothing server-side is touched, so a page reload
  // (or another dashboard tab) still sees the full history. `olderHops`
  // clears with it (it's part of the same visible list); the paging
  // cursor does not, so "load earlier" afterward resumes rather than
  // restarting from whatever's newly at the live ring's front.
  const clear = useCallback(() => {
    setHops([]);
    setOlderHops([]);
  }, []);

  // Pages one older batch in from GET /api/traffic/history, prepending
  // it. A no-op while already loading or once the endpoint has reported
  // nothing older is left (both re-checked here, not just at the call
  // site, so a fast double-click can't fire two overlapping requests).
  const loadEarlier = useCallback(() => {
    if (loadingEarlier || noMoreHistory) return;
    setLoadingEarlier(true);
    setHistoryError(null);
    const before = oldestSeqRef.current ?? undefined;
    api
      .trafficHistory({ before, limit: HISTORY_PAGE_SIZE })
      .then((page) => {
        if (page.hops.length === 0) {
          setNoMoreHistory(true);
          return;
        }
        // The endpoint returns newest-first; olderHops (like hops) reads
        // chronologically, so reverse before prepending.
        const ascending = page.hops.slice().reverse();
        oldestSeqRef.current = ascending[0].seq;
        setOlderHops((cur) => [...ascending, ...cur]);
        if (!page.hasMore) setNoMoreHistory(true);
      })
      .catch((err: unknown) => setHistoryError(messageOf(err, 'failed to load earlier traffic')))
      .finally(() => setLoadingEarlier(false));
  }, [loadingEarlier, noMoreHistory]);

  const allHops = useMemo(() => [...olderHops, ...hops], [olderHops, hops]);

  return {
    hops: allHops,
    clear,
    error: error ? messageOf(error, 'failed to reach the ensemble API') : null,
    loading,
    loadEarlier,
    loadingEarlier,
    noMoreHistory,
    historyError,
  };
}

export default function TrafficView() {
  const { hops, clear, error, loading, loadEarlier, loadingEarlier, noMoreHistory, historyError } = useHopRing();
  // Best-effort: the gateway show/hide split is a nice-to-have, so a topology fetch failure
  // just leaves showGateways' default (collapse nothing configured) rather than erroring the
  // whole Traffic tab.
  const { data: topology } = useAsync(() => api.topology(), []);

  const [pills, setPills] = useState<FilterToken[]>([]);
  const [draftText, setDraftText] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  // Off by default: a CORS preflight ensemble answers itself is real debugging signal but noisy
  // (one per cross-origin request), so it stays out of the way until asked for.
  const [showPreflight, setShowPreflight] = useState(false);
  const [sessionFilter, setSessionFilter] = useState<SessionFilter>(() => readParam('session') ?? 'all');
  // A session that arrived in the URL is a deep link — from retrace's
  // "traffic in ensemble" link, or a pasted one. It is held to a different
  // rule than a dropdown selection below: a run whose hops are not in the
  // window must show an EMPTY scope, never silently widen to the whole
  // stack. Someone who followed a link to one run's traffic and was shown
  // every client's would read it as that run's.
  const linkedSessionRef = useRef<string | null>(readParam('session'));
  // URL-backed so a side-by-side arrangement is a link someone can paste
  // into a bug report — the whole value of the view is "look at these two
  // together", which is a thing you hand to another person.
  const [clientParam, setClientParam] = useUrlParam('client');
  const [splitParam, setSplitParam] = useUrlParam('split');
  const [leftParam, setLeftParam] = useUrlParam('splitLeft');
  const [rightParam, setRightParam] = useUrlParam('splitRight');
  const clientFilter: ClientFilter = clientParam ?? 'all';
  const splitMode = splitParam === '1';
  const [showWithheld, setShowWithheld] = useState(false);
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

  // First-seen order, not sorted — reads chronologically alongside the
  // table itself. Distinct from `sessionFilter`'s own value so selecting
  // a session doesn't shrink this list out from under the dropdown.
  const distinctSessions = useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const h of collapsed) {
      if (h.session && !seen.has(h.session)) {
        seen.add(h.session);
        out.push(h.session);
      }
    }
    return out;
  }, [collapsed]);

  // Indexed over the whole visible window, not over `filtered`: a hop
  // inherits its client from its trace's entry hop, and an entry hop the
  // current query happens to exclude must still attribute the hops it
  // explains. Filtering first would make a client's chain disappear the
  // moment you typed a path filter that matched only its downstream call.
  const clientIndex = useMemo(() => buildClientIndex(collapsed), [collapsed]);

  const distinctClientList = useMemo(() => distinctClients(collapsed, clientIndex), [collapsed, clientIndex]);
  const windowHasUnattributed = useMemo(() => hasUnattributed(collapsed, clientIndex), [collapsed, clientIndex]);

  /** Every selectable client bucket, in the order the selector offers them. */
  const clientOptions = useMemo(
    () => (windowHasUnattributed ? [...distinctClientList, UNATTRIBUTED_CLIENT] : distinctClientList),
    [distinctClientList, windowHasUnattributed],
  );

  // Same stale-selection rule the session dropdown follows, and for the same
  // reason: a selection whose hops have aged out of the ring would otherwise
  // keep filtering everything away with no obvious way back.
  useEffect(() => {
    if (clientFilter === 'all') return;
    if (!clientOptions.includes(clientFilter)) setClientParam(null);
  }, [clientOptions, clientFilter, setClientParam]);

  // The dropdown only ever renders once there's more than one session to
  // choose between (see below) — so a stale selection, whether because
  // its session aged out of the ring or because the ring dropped back to
  // <=1 session, must fall back to 'all' rather than silently keep
  // filtering with no control left to change it back.
  useEffect(() => {
    if (sessionFilter === 'all' || sessionFilter === 'ambient') return;
    // Exempt: see linkedSessionRef. The dropdown is forced visible for a
    // linked session (below), so the user is never stuck with an invisible
    // filter — which is the only thing this fallback existed to prevent.
    if (sessionFilter === linkedSessionRef.current) return;
    if (distinctSessions.length <= 1 || !distinctSessions.includes(sessionFilter)) {
      setSessionFilter('all');
    }
  }, [distinctSessions, sessionFilter]);

  // Keep the URL in step with the control, so a scope reached by clicking is
  // as shareable as one reached by link.
  const selectSession = useCallback(
    (next: SessionFilter) => {
      setSessionFilter(next);
      // Stop exempting the linked session once the user steers away from it
      // themselves — from then on it is an ordinary selection.
      if (next !== linkedSessionRef.current) linkedSessionRef.current = null;
      writeParams({ session: next === 'all' ? null : next });
    },
    [],
  );

  /** Sessions the dropdown offers: what is in the window, plus a linked
   * session that is not (so the scope it produces is escapable). */
  const sessionOptions = useMemo(() => {
    const linked = linkedSessionRef.current;
    if (sessionFilter !== 'all' && sessionFilter === linked && !distinctSessions.includes(linked)) {
      return [linked, ...distinctSessions];
    }
    return distinctSessions;
  }, [distinctSessions, sessionFilter]);

  // The word currently in the box, live — before Tab/Space commits it to a
  // pill — still applies as a filter the moment it forms a complete token,
  // exactly like a committed pill would (see QueryFilterInput's doc
  // comment). Otherwise the whole draft is free text, same as before the
  // query grammar existed.
  const draftToken = useMemo(() => parseFilterToken(draftText), [draftText]);

  const filtered = useMemo(() => {
    const tokens = draftToken ? [...pills, draftToken] : pills;
    const freeText = draftToken ? '' : draftText;
    return collapsed.filter((h) => {
      if (!hopMatchesQuery(h, tokens, freeText, clientIndex)) return false;
      if (errorsOnly && !((h.status ?? 0) >= 400 || h.err)) return false;
      if (!showPreflight && h.preflight) return false;
      if (sessionFilter === 'ambient') {
        if (h.session) return false;
      } else if (sessionFilter !== 'all' && h.session !== sessionFilter) {
        return false;
      }
      // Skipped in split mode: the panes ARE the client filter there, and
      // applying the single-view selection on top would silently empty one
      // side whenever the two disagreed.
      if (!splitMode && !matchesClient(h, clientIndex, clientFilter === 'all' ? '' : clientFilter)) return false;
      return true;
    });
  }, [
    collapsed,
    pills,
    draftToken,
    draftText,
    errorsOnly,
    showPreflight,
    sessionFilter,
    clientIndex,
    clientFilter,
    splitMode,
  ]);

  // Which client sits in which pane. Defaults to the first two the window
  // offers so entering split mode shows something immediately rather than
  // two empty columns and a pair of dropdowns to discover.
  const leftClient = leftParam ?? clientOptions[0] ?? '';
  const rightClient = rightParam ?? clientOptions.find((c) => c !== leftClient) ?? '';

  const split = useMemo(
    () => splitPanes(filtered, clientIndex, leftClient, rightClient),
    [filtered, clientIndex, leftClient, rightClient],
  );

  // Revealing withheld hops does not fold them into a pane they don't
  // belong to — it lists them underneath, labeled. Putting a third client's
  // call into one of two columns would be exactly the lie the view exists
  // to avoid.
  const withheldRows = showWithheld ? split.withheld : [];

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
        <QueryFilterInput
          pills={pills}
          onPillsChange={setPills}
          draft={draftText}
          onDraftChange={setDraftText}
          hops={collapsed}
          placeholder="filter by service or path… (try status:200, size>10kb)"
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
        {(sessionOptions.length > 1 || sessionFilter === linkedSessionRef.current) && (
          <select
            className="traffic-view__session-select"
            value={sessionFilter}
            onChange={(e) => selectSession(e.target.value)}
            title="Filter by session"
          >
            <option value="all">all sessions</option>
            <option value="ambient">ambient</option>
            {sessionOptions.map((s) => (
              <option key={s} value={s}>
                {sessionLabel(s)}
              </option>
            ))}
          </select>
        )}
        {clientOptions.length > 0 && !splitMode && (
          <select
            className="traffic-view__client-select"
            value={clientFilter}
            onChange={(e) => setClientParam(e.target.value === 'all' ? null : e.target.value)}
            title={CLIENT_SELECT_TITLE}
          >
            <option value="all">all clients</option>
            {clientOptions.map((c) => (
              <option key={c} value={c}>
                {clientLabel(c)}
              </option>
            ))}
          </select>
        )}
        {/* Two clients in the window is the entire precondition — offering a
            side-by-side of one app against nothing would be a control that
            can only disappoint. */}
        {clientOptions.length > 1 && (
          <button
            type="button"
            className={`traffic-view__toggle${splitMode ? ' traffic-view__toggle--active' : ''}`}
            onClick={() => setSplitParam(splitMode ? null : '1')}
            title="Put two clients side by side on one shared row axis"
          >
            {splitMode ? 'merge' : 'split by client'}
          </button>
        )}
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
        <div className="traffic-view__load-earlier">
          {noMoreHistory ? (
            <span className="traffic-view__load-earlier-end">— beginning of history —</span>
          ) : (
            <button type="button" className="traffic-view__toggle" onClick={loadEarlier} disabled={loadingEarlier}>
              {loadingEarlier ? <Spinner /> : null}
              {loadingEarlier ? 'loading earlier…' : 'load earlier'}
            </button>
          )}
          {historyError ? <span className="traffic-view__load-earlier-error">{historyError}</span> : null}
        </div>
        {splitMode && (
          <div className="traffic-view__split-bar">
            <select
              className="traffic-view__client-select"
              value={leftClient}
              onChange={(e) => setLeftParam(e.target.value)}
              title={CLIENT_SELECT_TITLE}
            >
              {clientOptions.map((c) => (
                <option key={c} value={c}>
                  {clientLabel(c)}
                </option>
              ))}
            </select>
            <span className="traffic-view__split-vs">vs</span>
            <select
              className="traffic-view__client-select"
              value={rightClient}
              onChange={(e) => setRightParam(e.target.value)}
              title={CLIENT_SELECT_TITLE}
            >
              {clientOptions.map((c) => (
                <option key={c} value={c}>
                  {clientLabel(c)}
                </option>
              ))}
            </select>
            {/* Never a silent drop. "the call I'm looking for went missing"
                and "the call I'm looking for belongs to no client" are
                opposite diagnoses, and hiding the count hands you the wrong
                one. */}
            {split.withheld.length > 0 && (
              <button
                type="button"
                className={`traffic-view__toggle${showWithheld ? ' traffic-view__toggle--active' : ''}`}
                onClick={() => setShowWithheld((v) => !v)}
                title="Hops in view that belong to neither selected client — a third client's, or none at all"
              >
                {showWithheld ? 'hide' : 'show'} {split.withheld.length} not in either pane
              </button>
            )}
          </div>
        )}
        <div className="traffic-view__table" ref={scrollRef} onScroll={handleScroll}>
          {filtered.length === 0 ? (
            <p className="traffic-view__empty">no traffic matches these filters</p>
          ) : splitMode ? (
            <>
              <SplitHopPanes
                rows={split.rows}
                leftLabel={clientLabel(leftClient)}
                rightLabel={clientLabel(rightClient)}
                selectedSeq={selectedSeq}
                onSelectHop={(h) => setSelectedSeq(h.seq)}
              />
              {withheldRows.length > 0 && (
                <div className="traffic-view__withheld">
                  <p className="traffic-view__withheld-label">
                    belongs to neither pane — shown for completeness, not folded into a column
                  </p>
                  <HopTable
                    hops={withheldRows}
                    selectedSeq={selectedSeq}
                    onSelectHop={(h) => setSelectedSeq(h.seq)}
                    onViewTrace={viewTrace}
                  />
                </div>
              )}
            </>
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
