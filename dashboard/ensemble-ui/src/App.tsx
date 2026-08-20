import { useEffect, useState } from 'react';
import { Badge, Spinner, Tabs, type TabItem } from '@ensemble/design-system';
import { api, ApiError } from './api/client';
import type { ServiceState } from './api/types';
import { useUrlParam } from './urlState';
import './App.css';

// Task 3.2/3.3/3.5 replace these placeholders with the real views; the
// shell, tab wiring, and ?view= deep link are this task's job.
const VIEWS: TabItem[] = [
  { id: 'topology', label: 'Topology' },
  { id: 'traffic', label: 'Traffic' },
  { id: 'latency', label: 'Latency' },
  { id: 'inspector', label: 'Inspector' },
];

const DEFAULT_VIEW = VIEWS[0].id;

function useHealthPoll(intervalMs = 5000) {
  const [services, setServices] = useState<ServiceState[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function poll() {
      try {
        const result = await api.status();
        if (!cancelled) {
          setServices(result);
          setError(null);
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof ApiError ? err.message : 'failed to reach the ensemble API');
        }
      }
    }

    poll();
    const id = window.setInterval(poll, intervalMs);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [intervalMs]);

  return { services, error };
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

function PlaceholderView({ id }: { id: string }) {
  const view = VIEWS.find((v) => v.id === id);
  return (
    <div className="view-placeholder">
      <h2>{view?.label ?? id}</h2>
      <p>This view ships in a later Phase 3 task.</p>
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
        <PlaceholderView id={activeView} />
      </main>
    </div>
  );
}
