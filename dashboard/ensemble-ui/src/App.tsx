import { useEffect, useState } from 'react';
import { Badge, Spinner, Tabs, type TabItem } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api } from './api/client';
import type { ServiceState } from './api/types';
import { useUrlParam } from './urlState';
import TopologyView from './views/TopologyView';
import TrafficView from './views/TrafficView';
import LatencyView from './views/LatencyView';
import InspectorView from './views/InspectorView';
import EntityView from './views/EntityView';
import ServicesView from './views/ServicesView';
import './App.css';

const VIEWS: TabItem[] = [
  { id: 'topology', label: 'Topology' },
  { id: 'services', label: 'Services' },
  { id: 'traffic', label: 'Traffic' },
  { id: 'latency', label: 'Latency' },
  { id: 'inspector', label: 'Inspector' },
  { id: 'entities', label: 'Entities' },
];

const DEFAULT_VIEW = VIEWS[0].id;

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

  useEffect(() => {
    const id = window.setInterval(() => setTick((t) => t + 1), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);

  return { services, error: error?.message ?? null };
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
  const activeView = view && VIEWS.some((v) => v.id === view) ? view : DEFAULT_VIEW;

  return (
    <div className="app-shell">
      <header className="app-header">
        <div className="app-header__brand">ensemble</div>
        <Tabs items={VIEWS} active={activeView} onSelect={setView} />
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
        ) : (
          <EntityView />
        )}
      </main>
    </div>
  );
}
