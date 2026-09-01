// A flat, whole-stack view of every service: variant, native/container
// placement, ports, health, memory, and uptime, with row-level lifecycle
// controls (start/restart, stop, flip, change variant). TopologyView's
// ServicePanel already covers this per-node in a graph context; this view
// is the "just show me the list" counterpart the graph doesn't serve well
// once a stack has more than a handful of services.
import { useCallback, useEffect, useRef, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import { subscribeServiceLog } from '../api/sse';
import type { FreshnessState, ServiceState, Topology, TopologyNode, WiringWarning } from '../api/types';
import InlineError from '../components/InlineError';
import { usePendingRefresh } from '../usePendingRefresh';
import './ServicesView.css';

const POLL_MS = 5000;

function statusTone(status: string): 'green' | 'red' | 'amber' | 'neutral' {
  switch (status) {
    case 'healthy':
      return 'green';
    case 'unhealthy':
    case 'failed':
    case 'crashed':
      return 'red';
    case 'stopped':
    case 'exited':
      return 'neutral';
    default:
      return 'amber';
  }
}

/** Badge text for the status cell — the exited/crashed states carry how the process ended
    ("crashed (exit 1)"), so the supervision detail is visible without opening anything. */
function statusLabel(s: ServiceState): string {
  if (s.exitCode !== undefined) return `${s.status} (exit ${s.exitCode})`;
  if (s.signal) return `${s.status} (${s.signal})`;
  return s.status;
}

/** "service" (the default, unlabeled) is deliberately unremarkable; anything the user
    actually configured (`kind: stub`, `kind: mock`, ...) gets called out. */
function kindTone(kind: string | undefined): 'amber' | 'neutral' {
  return kind ? 'amber' : 'neutral';
}

function formatRSS(kb: number | undefined): string {
  if (!kb) return '—';
  if (kb < 1024) return `${kb} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  return `${(mb / 1024).toFixed(2)} GB`;
}

/** Renders one service's freshness state: nothing for a service that was never eligible
    (no `freshness:` configured, or ineligible Dir); an "unknown" badge for one that's never
    been successfully checked or whose last check failed (never a false "clean" or a false
    "behind" from a stale/failed read); amber badge(s) naming exactly which counts are
    nonzero; nothing (same as "never eligible") for a genuinely clean, checked service — so a
    row with no news to report stays visually quiet. */
function FreshnessCell({ freshness }: { freshness: FreshnessState | undefined }) {
  if (!freshness) {
    return <span className="services-table__dash">—</span>;
  }
  if (!freshness.checkedAt || freshness.error) {
    return (
      <span title={freshness.error || 'never checked'}>
        <Badge tone="neutral">unknown</Badge>
      </span>
    );
  }
  if (freshness.behindBranch === 0 && freshness.behindDefault === 0) {
    return <span className="services-table__dash">—</span>;
  }
  const detail = `branch ${freshness.branch} — checked ${new Date(freshness.checkedAt).toLocaleString()}`;
  return (
    <span className="services-table__freshness" title={detail}>
      {freshness.behindBranch > 0 && <Badge tone="amber">↓{freshness.behindBranch}</Badge>}
      {freshness.behindDefault > 0 && (
        <Badge tone="amber">
          {freshness.defaultBranch} ↓{freshness.behindDefault}
        </Badge>
      )}
    </span>
  );
}

function formatUptime(startedAt: string | undefined): string {
  if (!startedAt) return '—';
  const ms = Date.now() - new Date(startedAt).getTime();
  if (ms < 0) return '—';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

type SortKey = 'name' | 'status' | 'placement' | 'kind' | 'variant' | 'port' | 'proxyPort' | 'rss' | 'uptime';
type SortDir = 'asc' | 'desc';
interface SortState {
  key: SortKey;
  dir: SortDir;
}

const COLUMNS: { key: SortKey; label: string; numeric?: boolean }[] = [
  { key: 'name', label: 'name' },
  { key: 'status', label: 'status' },
  { key: 'placement', label: 'placement' },
  { key: 'kind', label: 'kind' },
  { key: 'variant', label: 'variant' },
  { key: 'port', label: 'port', numeric: true },
  { key: 'proxyPort', label: 'proxy', numeric: true },
  { key: 'rss', label: 'rss', numeric: true },
  { key: 'uptime', label: 'uptime', numeric: true },
];

/** A missing value sorts as "least" in either direction, so e.g. a stopped service (no
    uptime/rss) always lands at the ends of the list rather than jumping around when the
    sort direction flips. */
function sortValue(s: ServiceState, key: SortKey): string | number {
  switch (key) {
    case 'name':
      return s.name;
    case 'status':
      return s.status;
    case 'placement':
      return s.placement;
    case 'kind':
      return s.kind || 'service';
    case 'variant':
      return s.variant ?? '';
    case 'port':
      return s.port ?? -1;
    case 'proxyPort':
      return s.proxyPort ?? -1;
    case 'rss':
      return s.rssKB ?? -1;
    case 'uptime':
      return s.startedAt ? new Date(s.startedAt).getTime() : -1;
  }
}

function sortServices(services: ServiceState[], sort: SortState | null): ServiceState[] {
  if (!sort) return services;
  const { key, dir } = sort;
  const sign = dir === 'asc' ? 1 : -1;
  return [...services].sort((a, b) => {
    const va = sortValue(a, key);
    const vb = sortValue(b, key);
    const cmp = typeof va === 'number' && typeof vb === 'number' ? va - vb : String(va).localeCompare(String(vb));
    return cmp * sign;
  });
}

interface ServicesSnapshot {
  services: ServiceState[];
  topology: Topology;
  warnings: WiringWarning[];
}

/** refresh() runs both from the poll interval and out-of-band after a row mutation, so an
    older in-flight call resolving after a newer one must not clobber it — that generation
    guard is now useAsync's (keyed on `tick`), not a hand-rolled ref. `snapshot` exists only to
    keep the table on screen between polls: useAsync clears its data the instant `tick`
    changes (by design — see its own doc comment), which is right for "a different resource"
    but would otherwise flash the whole table back to a loading spinner every 5s. */
function useServicesPoll() {
  const [tick, setTick] = useState(0);
  const { data, error, loading } = useAsync<ServicesSnapshot>(async () => {
    const [s, t, w] = await Promise.all([api.status(true), api.topology(), api.wiringWarnings()]);
    return { services: s, topology: t, warnings: w };
  }, [tick]);

  const [snapshot, setSnapshot] = useState<ServicesSnapshot | null>(null);
  useEffect(() => {
    if (data !== null) setSnapshot(data);
  }, [data]);

  // Sticky error, mirroring the sticky `snapshot` above: useAsync clears BOTH `data` and
  // `error` to null the instant a new poll starts (tick bumps), so reading `error` straight
  // off the hook flashed the "offline" banner back to the stale-but-good table for the
  // whole duration of every in-flight poll while the backend was down (final review F2).
  // Cleared only on an actual successful load, never merely because the next poll started —
  // matching pre-migration's `setError(null)` on the success path only.
  const [staleError, setStaleError] = useState<string | null>(null);
  useEffect(() => {
    if (error) setStaleError(messageOf(error, 'failed to reach the ensemble API'));
    else if (data !== null) setStaleError(null);
  }, [error, data]);

  useEffect(() => {
    const id = window.setInterval(() => setTick((t) => t + 1), POLL_MS);
    return () => window.clearInterval(id);
  }, []);

  // Bumping `tick` alone resolves as soon as the state update is scheduled, not once the
  // triggered reload actually lands — callers that `await refresh()` (below) need the
  // promise to settle only once the NEW data (or a new error) is actually on screen, or
  // their `busy` flag clears while the row still shows the pre-action state (final review
  // F7). Every ServiceRow owns its own `busy` and disables only its OWN buttons, so two
  // rows are concurrently actionable and two refreshes can be waiting at once — see
  // usePendingRefresh for why ALL of them are resolved rather than only the newest
  // (re-review N1).
  const bumpTick = useCallback(() => setTick((t) => t + 1), []);
  const refresh = usePendingRefresh(loading, bumpTick);

  return {
    services: snapshot?.services ?? null,
    topology: snapshot?.topology ?? null,
    warnings: snapshot?.warnings ?? [],
    error: staleError,
    refresh,
  };
}

type Action = 'start' | 'restart' | 'stop' | 'flip' | 'variant';

/** Lines kept in a log pane's buffer — a follow of a chatty service must not grow the DOM
    unbounded, and an SSE reconnect replays the tail (see subscribeServiceLog), so trimming
    from the top is always safe. */
const LOG_PANE_MAX_LINES = 2000;

/** One service's live log: subscribes to the SSE follow on mount (the server replays a
    ~200-line tail first, then streams appended lines — build output included), pins the
    scroll to the bottom as text arrives, and unsubscribes on unmount/close. */
function LogPane({ name }: { name: string }) {
  const [text, setText] = useState('');
  const preRef = useRef<HTMLPreElement>(null);

  useEffect(() => {
    setText('');
    return subscribeServiceLog(name, (chunk) => {
      setText((prev) => {
        const next = prev ? `${prev}\n${chunk}` : chunk;
        const lines = next.split('\n');
        return lines.length > LOG_PANE_MAX_LINES ? lines.slice(-LOG_PANE_MAX_LINES).join('\n') : next;
      });
    });
  }, [name]);

  useEffect(() => {
    const el = preRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [text]);

  return (
    <pre ref={preRef} className="services-table__log">
      {text || '(no log output yet)'}
    </pre>
  );
}

/** Flip's control shape depends on how many placements a service declares: nothing to flip
    to (0 or 1), a single "Flip to X" button (exactly 2 — the original native/docker case,
    generalized to whichever two placements are declared), or a target-picking select (3,
    once passthrough joins native/docker) since "the other one" stops being well-defined. */
function FlipControl({
  placement,
  placements,
  busy,
  disabled,
  onFlip,
}: {
  placement: string;
  placements: string[];
  busy: boolean;
  disabled: boolean;
  onFlip: (target: string) => void;
}) {
  const others = placements.filter((p) => p !== placement);
  if (others.length === 0) {
    return <span className="services-table__dash">—</span>;
  }
  if (others.length === 1) {
    return (
      <button type="button" disabled={disabled} onClick={() => onFlip(others[0])}>
        {busy ? <Spinner /> : `Flip to ${others[0]}`}
      </button>
    );
  }
  return (
    <select
      value=""
      disabled={disabled}
      onChange={(e) => {
        if (e.target.value) onFlip(e.target.value);
      }}
    >
      <option value="">{busy ? 'Flipping…' : 'Flip to…'}</option>
      {others.map((p) => (
        <option key={p} value={p}>
          {p}
        </option>
      ))}
    </select>
  );
}

function ServiceRow({
  state,
  variants,
  placements,
  warnings,
  onAction,
}: {
  state: ServiceState;
  variants: string[];
  placements: string[];
  warnings: WiringWarning[];
  onAction: (action: Action, extra?: string) => Promise<void>;
}) {
  const [busy, setBusy] = useState<Action | null>(null);
  const [rowError, setRowError] = useState<string | null>(null);
  const [logsOpen, setLogsOpen] = useState(false);

  async function run(action: Action, extra?: string) {
    setBusy(action);
    setRowError(null);
    try {
      await onAction(action, extra);
    } catch (err) {
      setRowError(messageOf(err, `${action} failed`));
    } finally {
      setBusy(null);
    }
  }

  // exited/crashed are "not running" the same way stopped/failed are — the process is gone
  // and the only lifecycle action that makes sense is a fresh start.
  const stopped = ['stopped', 'failed', 'exited', 'crashed'].includes(state.status);

  return (
    <>
    <tr className="services-table__row">
      <td className="services-table__name">
        {state.name}
        {warnings.length > 0 && (
          <span
            className="services-table__wiring-warning"
            title={warnings.map((w) => w.message).join('\n')}
          >
            <Badge tone="red">wiring</Badge>
          </span>
        )}
      </td>
      <td>
        {/* A crash's lastErr carries the log tail — surfaced as the badge tooltip so the
            reason is one hover away without opening the log pane. */}
        <span title={state.lastErr || undefined}>
          <Badge tone={statusTone(state.status)}>{statusLabel(state)}</Badge>
        </span>
      </td>
      <td>
        <Badge tone="neutral">{state.placement}</Badge>
      </td>
      <td>
        <Badge tone={kindTone(state.kind)}>{state.kind || 'service'}</Badge>
      </td>
      <td className="services-table__variant">
        {variants.length > 0 ? (
          <select
            value={state.variant ?? ''}
            disabled={busy !== null}
            onChange={(e) => void run('variant', e.target.value)}
          >
            {variants.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        ) : (
          <span className="services-table__dash">—</span>
        )}
      </td>
      <td className="services-table__num">{state.port ?? '—'}</td>
      <td className="services-table__num">{state.proxyPort ?? '—'}</td>
      <td className="services-table__num">{formatRSS(state.rssKB)}</td>
      <td className="services-table__num">{formatUptime(state.startedAt)}</td>
      <td>
        <FreshnessCell freshness={state.freshness} />
      </td>
      <td className="services-table__actions">
        {stopped ? (
          <button type="button" disabled={busy !== null} onClick={() => void run('start')}>
            {busy === 'start' ? <Spinner /> : 'Start'}
          </button>
        ) : (
          <>
            <button type="button" disabled={busy !== null} onClick={() => void run('restart')}>
              {busy === 'restart' ? <Spinner /> : 'Restart'}
            </button>
            <button type="button" disabled={busy !== null} onClick={() => void run('stop')}>
              {busy === 'stop' ? <Spinner /> : 'Stop'}
            </button>
          </>
        )}
        <FlipControl
          placement={state.placement}
          placements={placements}
          busy={busy === 'flip'}
          disabled={busy !== null}
          onFlip={(target) => void run('flip', target)}
        />
        <button type="button" onClick={() => setLogsOpen((v) => !v)}>
          {logsOpen ? 'Hide logs' : 'Logs'}
        </button>
        {rowError && <InlineError message={rowError} className="services-table__row-error" />}
      </td>
    </tr>
    {logsOpen && (
      <tr className="services-table__log-row">
        <td colSpan={11}>
          <LogPane name={state.name} />
        </td>
      </tr>
    )}
    </>
  );
}

/** A cfg.Stubs entry, rendered from Topology's existing "stub" category — stubs never get a
    ServiceState (they aren't orchestrator-supervised lifecycle nodes the way services are),
    so this row has no placement/variant/port/rss/uptime/actions, just a name and status. */
function StubRow({ node }: { node: TopologyNode }) {
  return (
    <tr className="services-table__row">
      <td className="services-table__name">{node.name}</td>
      <td>
        <Badge tone={statusTone(node.status)}>{node.status}</Badge>
      </td>
      <td>
        <span className="services-table__dash">—</span>
      </td>
      <td>
        <Badge tone="amber">stub</Badge>
      </td>
      <td className="services-table__variant">
        <span className="services-table__dash">—</span>
      </td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td>
        <span className="services-table__dash">—</span>
      </td>
      <td className="services-table__actions" />
    </tr>
  );
}

/** A cfg.Gateways entry, rendered from Topology's existing "gateway" category — gateways are
    static listeners the proxy binds at Up, not orchestrator-supervised nodes, so like stubs
    they never get a ServiceState. */
function GatewayRow({ node }: { node: TopologyNode }) {
  return (
    <tr className="services-table__row">
      <td className="services-table__name">{node.name}</td>
      <td>
        <Badge tone={statusTone(node.status)}>{node.status}</Badge>
      </td>
      <td>
        <span className="services-table__dash">—</span>
      </td>
      <td>
        <Badge tone="amber">gateway</Badge>
      </td>
      <td className="services-table__variant">
        <span className="services-table__dash">—</span>
      </td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td>
        <span className="services-table__dash">—</span>
      </td>
      <td className="services-table__actions" />
    </tr>
  );
}

export default function ServicesView() {
  const { services, topology, warnings, error, refresh } = useServicesPoll();
  const [sort, setSort] = useState<SortState | null>(null);
  const [checkingFreshness, setCheckingFreshness] = useState(false);
  const [freshnessError, setFreshnessError] = useState<string | null>(null);

  async function handleFreshnessCheck() {
    setCheckingFreshness(true);
    setFreshnessError(null);
    try {
      await api.freshnessCheck();
      await refresh();
    } catch (err) {
      setFreshnessError(messageOf(err, 'freshness check failed'));
    } finally {
      setCheckingFreshness(false);
    }
  }

  const toggleSort = useCallback((key: SortKey) => {
    setSort((prev) =>
      prev?.key === key ? { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: 'asc' },
    );
  }, []);

  async function handleAction(name: string, action: Action, extra?: string) {
    switch (action) {
      case 'start':
      case 'restart':
        await api.restart(name);
        break;
      case 'stop':
        await api.stop(name);
        break;
      case 'flip':
        await api.flip(name, extra as 'native' | 'docker' | 'passthrough' | undefined);
        break;
      case 'variant':
        if (extra !== undefined) await api.setVariant(name, extra);
        break;
    }
    await refresh();
  }

  if (error) {
    return (
      <div className="services-view services-view--error">
        <Badge tone="red">offline</Badge>
        <span>{error}</span>
      </div>
    );
  }

  if (!services) {
    return (
      <div className="services-view services-view--loading">
        <Spinner />
        <span>loading services…</span>
      </div>
    );
  }

  const variantsByName = new Map(
    (topology?.nodes ?? []).map((n) => [n.name, n.variants ?? []]),
  );
  const placementsByName = new Map(
    (topology?.nodes ?? []).map((n) => [n.name, n.placements ?? []]),
  );
  const warningsByService = new Map<string, WiringWarning[]>();
  for (const w of warnings) {
    const list = warningsByService.get(w.service);
    if (list) list.push(w);
    else warningsByService.set(w.service, [w]);
  }
  const sorted = sortServices(services, sort);
  const stubNodes = (topology?.nodes ?? [])
    .filter((n) => n.category === 'stub')
    .sort((a, b) => a.name.localeCompare(b.name));
  const gatewayNodes = (topology?.nodes ?? [])
    .filter((n) => n.category === 'gateway')
    .sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div className="services-view">
      <div className="services-view__toolbar">
        <button type="button" disabled={checkingFreshness} onClick={() => void handleFreshnessCheck()}>
          {checkingFreshness ? <Spinner /> : 'Check freshness'}
        </button>
        {freshnessError && <InlineError message={freshnessError} />}
      </div>
      <table className="services-table">
        <thead>
          <tr>
            {COLUMNS.map((col) => (
              <th
                key={col.key}
                className={col.numeric ? 'services-table__num' : undefined}
                aria-sort={sort?.key === col.key ? (sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}
              >
                <button type="button" className="services-table__sort" onClick={() => toggleSort(col.key)}>
                  {col.label}
                  <span className="services-table__sort-indicator">
                    {sort?.key === col.key ? (sort.dir === 'asc' ? '▲' : '▼') : ''}
                  </span>
                </button>
              </th>
            ))}
            <th>freshness</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {sorted.length === 0 && stubNodes.length === 0 && gatewayNodes.length === 0 && (
            <tr>
              <td colSpan={11} className="services-table__empty">
                no services configured
              </td>
            </tr>
          )}
          {sorted.map((s) => (
            <ServiceRow
              key={s.name}
              state={s}
              variants={variantsByName.get(s.name) ?? []}
              placements={placementsByName.get(s.name) ?? []}
              warnings={warningsByService.get(s.name) ?? []}
              onAction={(action, extra) => handleAction(s.name, action, extra)}
            />
          ))}
          {stubNodes.map((n) => (
            <StubRow key={n.name} node={n} />
          ))}
          {gatewayNodes.map((n) => (
            <GatewayRow key={n.name} node={n} />
          ))}
        </tbody>
      </table>
    </div>
  );
}
