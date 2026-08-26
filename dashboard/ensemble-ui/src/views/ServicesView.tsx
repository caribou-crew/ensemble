// A flat, whole-stack view of every service: variant, native/container
// placement, ports, health, memory, and uptime, with row-level lifecycle
// controls (start/restart, stop, flip, change variant). TopologyView's
// ServicePanel already covers this per-node in a graph context; this view
// is the "just show me the list" counterpart the graph doesn't serve well
// once a stack has more than a handful of services.
import { useCallback, useEffect, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import type { ServiceState, Topology, TopologyNode } from '../api/types';
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
      return 'red';
    case 'stopped':
      return 'neutral';
    default:
      return 'amber';
  }
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
    const [s, t] = await Promise.all([api.status(true), api.topology()]);
    return { services: s, topology: t };
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
    error: staleError,
    refresh,
  };
}

type Action = 'start' | 'restart' | 'stop' | 'flip' | 'variant';

function ServiceRow({
  state,
  variants,
  onAction,
}: {
  state: ServiceState;
  variants: string[];
  onAction: (action: Action, extra?: string) => Promise<void>;
}) {
  const [busy, setBusy] = useState<Action | null>(null);
  const [rowError, setRowError] = useState<string | null>(null);

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

  const stopped = state.status === 'stopped' || state.status === 'failed';

  return (
    <tr className="services-table__row">
      <td className="services-table__name">{state.name}</td>
      <td>
        <Badge tone={statusTone(state.status)}>{state.status}</Badge>
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
        <button type="button" disabled={busy !== null} onClick={() => void run('flip')}>
          {busy === 'flip' ? <Spinner /> : `Flip to ${state.placement === 'docker' ? 'native' : 'docker'}`}
        </button>
        {rowError && <InlineError message={rowError} className="services-table__row-error" />}
      </td>
    </tr>
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
      <td className="services-table__actions" />
    </tr>
  );
}

export default function ServicesView() {
  const { services, topology, error, refresh } = useServicesPoll();
  const [sort, setSort] = useState<SortState | null>(null);

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
        await api.flip(name);
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
  const sorted = sortServices(services, sort);
  const stubNodes = (topology?.nodes ?? [])
    .filter((n) => n.category === 'stub')
    .sort((a, b) => a.name.localeCompare(b.name));
  const gatewayNodes = (topology?.nodes ?? [])
    .filter((n) => n.category === 'gateway')
    .sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div className="services-view">
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
            <th />
          </tr>
        </thead>
        <tbody>
          {sorted.length === 0 && stubNodes.length === 0 && gatewayNodes.length === 0 && (
            <tr>
              <td colSpan={10} className="services-table__empty">
                no services configured
              </td>
            </tr>
          )}
          {sorted.map((s) => (
            <ServiceRow
              key={s.name}
              state={s}
              variants={variantsByName.get(s.name) ?? []}
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
