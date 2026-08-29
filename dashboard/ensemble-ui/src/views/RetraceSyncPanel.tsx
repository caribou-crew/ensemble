// Discover → filter → select → pull: GET /api/retrace/sync/candidates
// lists what's out there without downloading anything; only rows checked
// here get pulled via POST /api/retrace/sync's {selections} body. Replaces
// the old single "Sync now" button, which pulled everything in the
// configured window and aborted the whole batch on one bad run.
import { useMemo, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import type { RetraceCandidate, RetraceSyncResult } from '../api/types';
import './RetraceView.css';

function candidateKey(c: RetraceCandidate): string {
  return `${c.repo}#${c.databaseId}`;
}

function isPullable(c: RetraceCandidate): boolean {
  return c.status === 'completed' && c.hasArtifacts;
}

function whyNotPullable(c: RetraceCandidate): string {
  if (c.status !== 'completed') return `still ${c.status}`;
  if (!c.hasArtifacts) return 'no artifacts';
  return '';
}

function matchesFilter(c: RetraceCandidate, filter: string): boolean {
  if (!filter) return true;
  const needle = filter.toLowerCase();
  return [c.workflowName, c.headBranch, c.actor, c.event].some((field) => field.toLowerCase().includes(needle));
}

export default function RetraceSyncPanel({
  onClose,
  onSynced,
}: {
  onClose: () => void;
  onSynced: () => void;
}) {
  const { data, error, loading } = useAsync(() => api.retraceSyncCandidates(), []);
  const [filter, setFilter] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [pulling, setPulling] = useState(false);
  const [pullError, setPullError] = useState<string | null>(null);
  const [result, setResult] = useState<RetraceSyncResult | null>(null);

  const candidates = data?.candidates ?? [];
  const visible = useMemo(() => candidates.filter((c) => matchesFilter(c, filter)), [candidates, filter]);

  function toggle(c: RetraceCandidate) {
    const key = candidateKey(c);
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function selectAllPullable() {
    setSelected(new Set(visible.filter(isPullable).map(candidateKey)));
  }

  async function pullSelected() {
    const chosen = candidates.filter((c) => selected.has(candidateKey(c)));
    if (chosen.length === 0) return;
    setPulling(true);
    setPullError(null);
    try {
      const res = await api.retraceSync(chosen.map((c) => ({ repo: c.repo, databaseId: c.databaseId })));
      setResult(res);
      onSynced();
    } catch (err) {
      setPullError(messageOf(err, 'sync failed'));
    } finally {
      setPulling(false);
    }
  }

  return (
    <div className="retrace-sync-panel">
      <div className="retrace-sync-panel__header">
        <h3>Browse candidate runs</h3>
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>

      <input
        type="text"
        className="retrace-sync-panel__filter"
        placeholder="filter by workflow, branch, actor, event…"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />

      {error && <p className="retrace-view__sync-error">{messageOf(error, 'failed to load candidates')}</p>}
      {loading && !data && <Spinner />}

      {data && (
        <>
          <table className="retrace-sync-panel__table">
            <thead>
              <tr>
                <th />
                <th>workflow</th>
                <th>branch</th>
                <th>actor</th>
                <th>event</th>
                <th>status</th>
                <th>artifacts</th>
                <th>when</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((c) => {
                const pullable = isPullable(c);
                const key = candidateKey(c);
                return (
                  <tr key={key} className={pullable ? undefined : 'retrace-sync-panel__row--unpullable'}>
                    <td>
                      <input type="checkbox" checked={selected.has(key)} disabled={!pullable} onChange={() => toggle(c)} />
                    </td>
                    <td>{c.workflowName}</td>
                    <td>{c.headBranch}</td>
                    <td>{c.actor}</td>
                    <td>{c.event}</td>
                    <td title={pullable ? undefined : whyNotPullable(c)}>
                      <Badge tone={pullable ? 'green' : 'amber'}>{c.status}</Badge>
                    </td>
                    <td>{c.hasArtifacts ? 'yes' : 'no'}</td>
                    <td>{new Date(c.createdAt).toLocaleString()}</td>
                  </tr>
                );
              })}
              {visible.length === 0 && (
                <tr>
                  <td colSpan={8} className="retrace-view__empty">
                    No candidate runs match.
                  </td>
                </tr>
              )}
            </tbody>
          </table>

          <div className="retrace-sync-panel__footer">
            <button type="button" onClick={selectAllPullable}>
              Select all pullable
            </button>
            <button type="button" onClick={() => void pullSelected()} disabled={pulling || selected.size === 0}>
              {pulling ? <Spinner /> : `Pull ${selected.size} selected`}
            </button>
            {pullError && <span className="retrace-view__sync-error">{pullError}</span>}
            {result && (
              <span className="retrace-view__hint">
                pulled {result.synced.length}, skipped {result.skipped.length}
              </span>
            )}
          </div>
        </>
      )}
    </div>
  );
}
