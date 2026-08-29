// Discover → filter → click-to-pull-and-view: GET /api/retrace/sync/
// candidates lists what's out there without downloading anything; clicking
// a row either opens it directly (already pulled — runs.SourcesByURL's
// localRuns join, no network call) or pulls just that one candidate via
// POST /api/retrace/sync's {selections} body and opens from the result.
// Replaces the old checkbox + "Pull N selected" bar, which asked a
// reviewer to select rows blind and only showed a pulled-count afterwards
// — this shows the flow itself.
import { useEffect, useMemo, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { mergeCandidates, sinceParam } from '@ensemble/design-system/syncCandidates';
import { api, messageOf, resolveRetraceShotUrlAtRun } from '../api/client';
import type { RetraceCandidate } from '../api/types';
import { DetailPane } from './RetraceView';
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

/** One "app/flow/run-id" — runs.SourcesByURL's / sync.Result's own
 * encoding — parsed into its three components. app/flow/run-id are each
 * validated (runs.ValidateComponents) to contain no path separator, so a
 * plain split on "/" always yields exactly these three parts. */
interface RunRef {
  app: string;
  flow: string;
  runId: string;
}
function parseRunPath(path: string): RunRef {
  const [app, flow, runId] = path.split('/');
  return { app, flow, runId };
}
function runKey(r: RunRef): string {
  return `${r.app}/${r.flow}/${r.runId}`;
}

/** The read-only run-detail view: this panel's click target for one flow.
 * Reuses DetailPane exactly as RetraceView's own main queue does, pointed
 * at run-scoped routes (retraceItemAtRun / resolveRetraceShotUrlAtRun) so a
 * non-latest run's generated diff/overlay images are read from their own
 * cache instead of the "latest" queue's. DetailPane carries no mutation
 * actions of its own, so nesting it here risks nothing in the main queue's
 * own state. */
function RunDetail({ app, flow, runId, onBack }: RunRef & { onBack: () => void }) {
  const item = useAsync(() => api.retraceItemAtRun(app, flow, runId), [app, flow, runId]);

  return (
    <div className="retrace-sync-panel__detail">
      <button type="button" className="retrace-sync-panel__back" onClick={onBack}>
        ← back to results
      </button>
      {item.loading ? (
        <p className="retrace-view__hint">
          <Spinner /> loading {app}/{flow}…
        </p>
      ) : item.error ? (
        <p className="retrace-view__sync-error">{messageOf(item.error, 'failed to load flow detail')}</p>
      ) : item.data ? (
        <DetailPane
          app={app}
          flow={flow}
          summary={item.data}
          resolveShotUrl={resolveRetraceShotUrlAtRun(runId)}
          onReveal={() => api.retraceItemAtRun(app, flow, runId).then((s) => s.sections)}
        />
      ) : null}
    </div>
  );
}

/** Shown when a candidate's CI run produced more than one flow — a single
 * job can record several — so the reviewer picks which one to look at
 * instead of the panel guessing. */
function RunChooser({ refs, onPick, onBack }: { refs: RunRef[]; onPick: (r: RunRef) => void; onBack: () => void }) {
  return (
    <div className="retrace-sync-panel__chooser" role="dialog" aria-label="choose a flow">
      <button type="button" className="retrace-sync-panel__back" onClick={onBack}>
        ← back to results
      </button>
      <p>This run produced {refs.length} flows — pick one:</p>
      <ul>
        {refs.map((r) => (
          <li key={runKey(r)}>
            <button type="button" className="retrace-sync-panel__chooser-option" onClick={() => onPick(r)}>
              {r.app}/{r.flow}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

export default function RetraceSyncPanel({
  onClose,
  onSynced,
}: {
  onClose: () => void;
  onSynced: () => void;
}) {
  const [candidates, setCandidates] = useState<RetraceCandidate[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [filter, setFilter] = useState('');
  const [pullingKey, setPullingKey] = useState<string | null>(null);
  const [pullError, setPullError] = useState<string | null>(null);
  const [chooser, setChooser] = useState<RunRef[] | null>(null);
  const [detail, setDetail] = useState<RunRef | null>(null);

  // The initial load, once on mount. Unlike useAsync, this state is never
  // cleared by a later fetch — see refresh below, whose whole point is
  // that a later fetch must NOT blank what is already on screen.
  useEffect(() => {
    let cancelled = false;
    api
      .retraceSyncCandidates()
      .then((res) => {
        if (cancelled) return;
        setCandidates(res.candidates);
        setLoading(false);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err : new Error(String(err)));
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Asks only for runs newer than the newest one already known (see
  // sinceParam), and merges the result into the list already on screen
  // rather than replacing it — a refresh that fails, or finds nothing new,
  // leaves the reviewer's current view exactly as it was.
  async function refresh() {
    if (!candidates || refreshing) return;
    setRefreshing(true);
    setError(null);
    try {
      const since = sinceParam(candidates);
      const res = await api.retraceSyncCandidates(since ? { since } : {});
      setCandidates((prev) => mergeCandidates(prev ?? [], res.candidates));
    } catch (err) {
      setError(err instanceof Error ? err : new Error(String(err)));
    } finally {
      setRefreshing(false);
    }
  }

  const list = candidates ?? [];
  const visible = useMemo(() => list.filter((c) => matchesFilter(c, filter)), [list, filter]);

  function openPaths(paths: string[]) {
    const refs = paths.map(parseRunPath);
    if (refs.length === 0) {
      setPullError('the pull completed but produced no flows for this run — it may already be fully synced; try refreshing');
      return;
    }
    if (refs.length === 1) {
      setChooser(null);
      setDetail(refs[0]);
      return;
    }
    setChooser(refs);
  }

  async function openCandidate(c: RetraceCandidate) {
    setPullError(null);
    if (c.localRuns.length > 0) {
      openPaths(c.localRuns);
      return;
    }
    if (!isPullable(c)) return;
    const key = candidateKey(c);
    setPullingKey(key);
    try {
      const res = await api.retraceSync([{ repo: c.repo, databaseId: c.databaseId }]);
      if (res.synced.length === 0) {
        const reason = res.skipped[0]?.reason;
        setPullError(
          reason
            ? `could not pull ${c.workflowName}: ${reason}`
            : `the pull for ${c.workflowName} produced no flows — it may already be fully synced; try refreshing`,
        );
        return;
      }
      // Reflected locally so a reviewer who backs out to the list sees this
      // row as already pulled, rather than inviting a second, redundant
      // pull of the same run.
      setCandidates((prev) =>
        prev ? prev.map((x) => (candidateKey(x) === key ? { ...x, localRuns: res.synced } : x)) : prev,
      );
      onSynced();
      openPaths(res.synced);
    } catch (err) {
      setPullError(messageOf(err, 'sync failed'));
    } finally {
      setPullingKey(null);
    }
  }

  if (detail) {
    return (
      <div className="retrace-sync-panel">
        <div className="retrace-sync-panel__header">
          <h3>Browse candidate runs</h3>
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
        <RunDetail {...detail} onBack={() => setDetail(null)} />
      </div>
    );
  }

  if (chooser) {
    return (
      <div className="retrace-sync-panel">
        <div className="retrace-sync-panel__header">
          <h3>Browse candidate runs</h3>
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
        <RunChooser
          refs={chooser}
          onPick={(r) => {
            setChooser(null);
            setDetail(r);
          }}
          onBack={() => setChooser(null)}
        />
      </div>
    );
  }

  return (
    <div className="retrace-sync-panel">
      <div className="retrace-sync-panel__header">
        <h3>Browse candidate runs</h3>
        {candidates && (
          <button type="button" onClick={() => void refresh()} disabled={refreshing}>
            {refreshing ? <Spinner /> : '↻ refresh'}
          </button>
        )}
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
      {pullError && <p className="retrace-view__sync-error">{pullError}</p>}
      {loading && !candidates && <Spinner />}

      {candidates && (
        <table className="retrace-sync-panel__table">
          <thead>
            <tr>
              <th>workflow</th>
              <th>branch</th>
              <th>actor</th>
              <th>event</th>
              <th>status</th>
              <th>artifacts</th>
              <th>when</th>
              <th>action</th>
            </tr>
          </thead>
          <tbody>
            {visible.map((c) => {
              const pulled = c.localRuns.length > 0;
              const pullable = isPullable(c);
              const clickable = pulled || pullable;
              const key = candidateKey(c);
              const pullingThis = pullingKey === key;
              return (
                <tr
                  key={key}
                  className={`retrace-sync-panel__row${clickable ? ' retrace-sync-panel__row--clickable' : ' retrace-sync-panel__row--unpullable'}`}
                  onClick={clickable && !pullingThis ? () => void openCandidate(c) : undefined}
                  aria-disabled={!clickable || pullingThis}
                >
                  <td>{c.workflowName}</td>
                  <td>{c.headBranch}</td>
                  <td>{c.actor}</td>
                  <td>{c.event}</td>
                  <td title={pullable ? undefined : whyNotPullable(c)}>
                    <Badge tone={pullable ? 'green' : 'amber'}>{c.status}</Badge>
                  </td>
                  <td>{c.hasArtifacts ? 'yes' : 'no'}</td>
                  <td>{new Date(c.createdAt).toLocaleString()}</td>
                  <td>
                    {pullingThis ? (
                      <Spinner />
                    ) : pulled ? (
                      <span className="retrace-sync-panel__pulled">already pulled — view</span>
                    ) : pullable ? (
                      <span className="retrace-sync-panel__pull-cta">pull &amp; view</span>
                    ) : null}
                  </td>
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
      )}
    </div>
  );
}
