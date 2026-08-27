import { useEffect, useState } from 'react';
import { Badge, Spinner, Tabs, type TabItem } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from './api/client';
import type { ServiceState } from './api/types';
import { useUrlParam } from './urlState';
import TopologyView from './views/TopologyView';
import TrafficView from './views/TrafficView';
import LatencyView from './views/LatencyView';
import InspectorView from './views/InspectorView';
import EntityView from './views/EntityView';
import ServicesView from './views/ServicesView';
import RetraceView from './views/RetraceView';
import './App.css';

const BASE_VIEWS: TabItem[] = [
  { id: 'topology', label: 'Topology' },
  { id: 'services', label: 'Services' },
  { id: 'traffic', label: 'Traffic' },
  { id: 'latency', label: 'Latency' },
  { id: 'inspector', label: 'Inspector' },
  { id: 'entities', label: 'Entities' },
];

const DEFAULT_VIEW = BASE_VIEWS[0].id;

/**
 * Whether this stack has a `retrace:` block configured, probed once via the
 * queue route itself rather than a separate "am I configured" endpoint —
 * `GET /api/retrace/queue` 501s when it isn't (mirroring the inspector
 * routes' own convention; see InspectorView's `useDatabases`). Any OTHER
 * failure (a genuine outage) also keeps the tab hidden rather than showing
 * a broken one — there is no way to tell "not configured" from "briefly
 * unreachable" from here, and hidden is the safe default for a tab whose
 * entire premise (CI test results exist to review) most stacks opt out of.
 */
function useRetraceAvailable(): boolean {
  const { data } = useAsync(() => api.retraceQueue(), []);
  return data !== null;
}

function useHealthPoll(intervalMs = 5000) {
  // A tick counter, not a mount-only load — useAsync re-runs `fn` whenever `deps` changes, so
  // bumping `tick` on the interval is what keeps this polling rather than fetching once.
  const [tick, setTick] = useState(0);
  const { data, error } = useAsync(() => api.status(), [tick]);

  // useAsync clears `data` to null the instant `tick` changes (deliberately — see its own
  // doc comment), which is correct for "a different record" but wrong for "the same poll,
  // again": without this, the strip would flash back to "connecting…" every intervalMs. This
  // mirrors the last-good-value it kept before migration, sourced from useAsync's `data`
  // instead of a hand-rolled setter.
  const [services, setServices] = useState<ServiceState[] | null>(null);
  useEffect(() => {
    if (data !== null) setServices(data);
  }, [data]);

  // Sticky error, mirroring the sticky `services` snapshot above: useAsync clears BOTH
  // `data` and `error` to null the instant a new poll starts (tick bumps), so reading
  // `error` straight off the hook flashed the "offline" banner back to the stale-but-good
  // table for the whole duration of every in-flight poll while the backend was down (final
  // review F2) — for a poll that keeps failing every ~5s, that is effectively the entire
  // outage. Pre-migration `setError(null)` ran only on the SUCCESS path, so once the
  // banner appeared it stayed until a poll actually succeeded again; this reproduces that
  // by clearing only on an actual successful load (`data !== null`), never merely because
  // the next poll started.
  const [staleError, setStaleError] = useState<string | null>(null);
  useEffect(() => {
    if (error) setStaleError(messageOf(error, 'failed to reach the ensemble API'));
    else if (data !== null) setStaleError(null);
  }, [error, data]);

  useEffect(() => {
    const id = window.setInterval(() => setTick((t) => t + 1), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);

  return { services, error: staleError };
}

function HealthStrip() {
  const { services, error } = useHealthPoll();

  if (error) {
    return (
      <div className="health-strip health-strip--error">
        <Badge tone="red">offline</Badge>
        <span>{error}</span>
      </div>
    );
  }

  if (!services) {
    return (
      <div className="health-strip">
        <Spinner />
        <span>connecting…</span>
      </div>
    );
  }

  const unhealthy = services.filter((s) => s.status !== 'healthy').length;
  return (
    <div className="health-strip">
      <Badge tone={unhealthy === 0 ? 'green' : 'amber'}>
        {services.length} service{services.length === 1 ? '' : 's'}
      </Badge>
      {unhealthy > 0 && <Badge tone="amber">{unhealthy} unhealthy</Badge>}
    </div>
  );
}

export default function App() {
  const [view, setView] = useUrlParam('view');
  const retraceAvailable = useRetraceAvailable();
  const views = retraceAvailable ? [...BASE_VIEWS, { id: 'retrace', label: 'Retrace' }] : BASE_VIEWS;
  const activeView = view && views.some((v) => v.id === view) ? view : DEFAULT_VIEW;

  return (
    <div className="app-shell">
      <header className="app-header">
        <div className="app-header__brand">ensemble</div>
        <Tabs items={views} active={activeView} onSelect={setView} />
        <HealthStrip />
      </header>
      <main className="app-main">
        {activeView === 'topology' ? (
          <TopologyView />
        ) : activeView === 'services' ? (
          <ServicesView />
        ) : activeView === 'traffic' ? (
          <TrafficView />
        ) : activeView === 'latency' ? (
          <LatencyView />
        ) : activeView === 'inspector' ? (
          <InspectorView />
        ) : activeView === 'retrace' ? (
          <RetraceView />
        ) : (
          <EntityView />
        )}
      </main>
    </div>
  );
}
