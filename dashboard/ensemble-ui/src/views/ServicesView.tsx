// A flat, whole-stack view of every service: variant, native/container
// placement, ports, health, memory, and uptime, with row-level lifecycle
// controls (start/restart, stop, flip, change variant). TopologyView's
// ServicePanel already covers this per-node in a graph context; this view
// is the "just show me the list" counterpart the graph doesn't serve well
// once a stack has more than a handful of services.
import { useCallback, useEffect, useRef, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { api, messageOf } from '../api/client';
import type { ServiceState, Topology } from '../api/types';
import InlineError from '../components/InlineError';
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

/** Same generation-counter pattern as TopologyView's useTopologyPoll: refresh() runs both from
    the poll interval and out-of-band after a mutation, so an older in-flight call resolving
    after a newer one must not clobber it. */
function useServicesPoll() {
  const [services, setServices] = useState<ServiceState[] | null>(null);
  const [topology, setTopology] = useState<Topology | null>(null);
  const [error, setError] = useState<string | null>(null);
  const generationRef = useRef(0);

  const refresh = useCallback(async () => {
    const generation = ++generationRef.current;
    try {
      const [s, t] = await Promise.all([api.status(true), api.topology()]);
      if (generation !== generationRef.current) return;
      setServices(s);
      setTopology(t);
      setError(null);
    } catch (err) {
      if (generation !== generationRef.current) return;
      setError(messageOf(err, 'failed to reach the ensemble API'));
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
      generationRef.current++;
    };
  }, [refresh]);

  return { services, topology, error, refresh };
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

export default function ServicesView() {
  const { services, topology, error, refresh } = useServicesPoll();

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

  return (
    <div className="services-view">
      <table className="services-table">
        <thead>
          <tr>
            <th>name</th>
            <th>status</th>
            <th>placement</th>
            <th>variant</th>
            <th>port</th>
            <th>proxy</th>
            <th>rss</th>
            <th>uptime</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {services.length === 0 && (
            <tr>
              <td colSpan={9} className="services-table__empty">
                no services configured
              </td>
            </tr>
          )}
          {services.map((s) => (
            <ServiceRow
              key={s.name}
              state={s}
              variants={variantsByName.get(s.name) ?? []}
              onAction={(action, extra) => handleAction(s.name, action, extra)}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}
