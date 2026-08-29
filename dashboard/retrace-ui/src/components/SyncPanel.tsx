import { useState, type FormEvent } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { mergeCandidates, sinceParam } from '@ensemble/design-system/syncCandidates';
import { api, messageOf } from '../api/client';
import type { SyncCandidate } from '../api/types';
import { ItemScreen } from '../App';

/** One "app/flow/run-id" — SourcesByURL's / sync.Result's own encoding —
 * parsed into its three components. app/flow/run-id are each validated
 * (runs.ValidateComponents) to contain no path separator, so a plain split
 * on "/" always yields exactly these three parts; there is nothing here
 * that could produce a fourth. */
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

/** The read-only run-detail view: SyncPanel's click target for one flow.
 * Reuses ItemScreen exactly as the main queue's own "open a flow" does,
 * pointed at run-scoped routes (api.itemAtRun / api.shotUrlAtRun) so a
 * non-latest run's generated diff/overlay images are read from their own
 * cache instead of the "latest" queue's. ItemScreen carries no mutation
 * buttons of its own — see its doc comment in App.tsx — so nesting it here
 * risks nothing in the main queue's keyboard state machine. */
function RunDetail({ app, flow, runId, onBack }: RunRef & { onBack: () => void }) {
  const item = useAsync(() => api.itemAtRun(app, flow, runId).then((r) => r.summary), [app, flow, runId]);

  return (
    <div className="sync-panel__detail">
      <button type="button" className="sync-panel__back" onClick={onBack}>
        ← back to results
      </button>
      {item.loading ? (
        <p className="loading">
          <Spinner /> loading {app}/{flow}…
        </p>
      ) : item.error ? (
        <p className="sync-panel__error">{item.error.message}</p>
      ) : item.data ? (
        <ItemScreen
          app={app}
          flow={flow}
          summary={item.data}
          selectedField={null}
          onSelectField={() => {}}
          resolveShotUrl={(a, f, side, name) =>
            api.shotUrlAtRun(a, f, runId, side as 'a' | 'b' | 'diff' | 'overlay', name)
          }
          onReveal={() => api.itemAtRun(app, flow, runId).then((r) => r.summary.sections)}
        />
      ) : null}
    </div>
  );
}

/** Shown when a candidate's CI run produced more than one flow — a single
 * `retrace-web` job can record several — so the reviewer picks which one to
 * look at instead of the panel guessing. */
function RunChooser({ refs, onPick, onBack }: { refs: RunRef[]; onPick: (r: RunRef) => void; onBack: () => void }) {
  return (
    <div className="sync-panel__chooser" role="dialog" aria-label="choose a flow">
      <button type="button" className="sync-panel__back" onClick={onBack}>
        ← back to results
      </button>
      <p>This run produced {refs.length} flows — pick one:</p>
      <ul>
        {refs.map((r) => (
          <li key={runKey(r)}>
            <button type="button" className="sync-panel__chooser-option" onClick={() => onPick(r)}>
              {r.app}/{r.flow}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * Discover -> filter -> select -> pull-and-view, over retrace/serve's own
 * GET /api/sync/candidates and POST /api/sync. Unlike ensemble-ui's
 * equivalent panel, this package's config carries no repo default —
 * retrace.yaml has no `sync:` block — so the repo box here is required and
 * nothing pre-fills it.
 *
 * Each candidate row is the click target — there is no checkbox and no bulk
 * "pull N selected" any more. A row already pulled (localRuns non-empty)
 * opens straight into RunDetail with no network call; a row not yet pulled
 * triggers a pull scoped to just that one candidate, then opens from
 * whatever it produced. Either path can land on more than one flow, which
 * RunChooser mediates.
 */
export default function SyncPanel({
  onClose,
  onSynced,
}: {
  onClose: () => void;
  onSynced: () => void;
}) {
  const [repo, setRepo] = useState('');
  const [candidates, setCandidates] = useState<SyncCandidate[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [pullingId, setPullingId] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [chooser, setChooser] = useState<RunRef[] | null>(null);
  const [detail, setDetail] = useState<RunRef | null>(null);

  const load = async (e: FormEvent) => {
    e.preventDefault();
    const trimmed = repo.trim();
    if (trimmed === '') return;
    setLoading(true);
    setError(null);
    try {
      const res = await api.syncCandidates(trimmed);
      setCandidates(res.candidates);
    } catch (err) {
      setError(messageOf(err, 'could not list candidates'));
      setCandidates(null);
    } finally {
      setLoading(false);
    }
  };

  // Never clears `candidates` — a refresh that fails, or that simply finds
  // nothing new, must leave the reviewer's current view exactly as it was.
  // Only sinceParam(candidates) is asked for, so this costs GitHub only the
  // runs that showed up since the last check.
  const refresh = async () => {
    if (!candidates || refreshing) return;
    setRefreshing(true);
    setError(null);
    try {
      const since = sinceParam(candidates);
      const res = await api.syncCandidates(repo.trim(), since ? { since } : {});
      setCandidates((prev) => mergeCandidates(prev ?? [], res.candidates));
    } catch (err) {
      setError(messageOf(err, 'refresh failed'));
    } finally {
      setRefreshing(false);
    }
  };

  const openPaths = (paths: string[]) => {
    const refs = paths.map(parseRunPath);
    if (refs.length === 0) {
      setError('the pull completed but produced no flows for this run — it may already be fully synced; try refreshing');
      return;
    }
    if (refs.length === 1) {
      setChooser(null);
      setDetail(refs[0]);
      return;
    }
    setChooser(refs);
  };

  const openCandidate = async (c: SyncCandidate) => {
    setError(null);
    if (c.localRuns.length > 0) {
      openPaths(c.localRuns);
      return;
    }
    if (!c.hasArtifacts) return;
    setPullingId(c.databaseId);
    try {
      const res = await api.sync(repo.trim(), [{ repo: c.repo, databaseId: c.databaseId }]);
      if (res.synced.length === 0) {
        const reason = res.skipped[0]?.reason;
        setError(
          reason
            ? `could not pull ${c.workflowName}: ${reason}`
            : `the pull for ${c.workflowName} produced no flows — it may already be fully synced; try refreshing`,
        );
        return;
      }
      // Reflected locally so a reviewer who backs out to the list sees this
      // row as already pulled, rather than "pull & view" inviting a second,
      // redundant pull of the same run.
      setCandidates((prev) =>
        prev ? prev.map((x) => (x.databaseId === c.databaseId ? { ...x, localRuns: res.synced } : x)) : prev,
      );
      onSynced();
      openPaths(res.synced);
    } catch (err) {
      setError(messageOf(err, 'pull failed'));
    } finally {
      setPullingId(null);
    }
  };

  if (detail) {
    return (
      <div className="picker sync-panel" role="dialog" aria-label="sync from GitHub Actions">
        <RunDetail {...detail} onBack={() => setDetail(null)} />
        <div className="picker__buttons">
          <button type="button" onClick={onClose}>
            close
          </button>
        </div>
      </div>
    );
  }

  if (chooser) {
    return (
      <div className="picker sync-panel" role="dialog" aria-label="sync from GitHub Actions">
        <RunChooser
          refs={chooser}
          onPick={(r) => {
            setChooser(null);
            setDetail(r);
          }}
          onBack={() => setChooser(null)}
        />
        <div className="picker__buttons">
          <button type="button" onClick={onClose}>
            close
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="picker sync-panel" role="dialog" aria-label="sync from GitHub Actions">
      <h2>Sync from GitHub Actions</h2>
      <form onSubmit={load}>
        <label>
          repo
          <input
            name="repo"
            value={repo}
            onChange={(e) => setRepo(e.target.value)}
            placeholder="org/repo"
            disabled={loading}
          />
        </label>
        <button type="submit" disabled={loading || repo.trim() === ''}>
          {loading ? <Spinner /> : 'list runs'}
        </button>
      </form>

      {error ? <p className="sync-panel__error">{error}</p> : null}

      {candidates ? (
        candidates.length === 0 ? (
          <p className="sync-panel__empty">No matching runs found in {repo.trim()}.</p>
        ) : (
          <>
            <div className="sync-panel__toolbar">
              <button type="button" onClick={() => void refresh()} disabled={refreshing || loading}>
                {refreshing ? <Spinner /> : '↻ refresh'}
              </button>
            </div>
            <ul className="sync-panel__candidates">
              {candidates.map((c) => {
                const tone = c.conclusion === 'success' ? 'green' : c.conclusion === 'failure' ? 'red' : 'neutral';
                const pulled = c.localRuns.length > 0;
                const clickable = pulled || c.hasArtifacts;
                const pullingThis = pullingId === c.databaseId;
                return (
                  <li key={c.databaseId} className="sync-panel__candidate">
                    <button
                      type="button"
                      className="sync-panel__candidate-row"
                      disabled={!clickable || pullingThis}
                      onClick={() => void openCandidate(c)}
                    >
                      <span className="sync-panel__workflow">{c.workflowName}</span>
                      <Badge tone={tone}>{c.conclusion || c.status}</Badge>
                      <span className="sync-panel__meta">
                        {c.headBranch} · {c.actor} · {c.event} · {c.createdAt}
                      </span>
                      {pullingThis ? (
                        <Spinner />
                      ) : pulled ? (
                        <span className="sync-panel__pulled">already pulled — view</span>
                      ) : c.hasArtifacts ? (
                        <span className="sync-panel__pull-cta">pull &amp; view</span>
                      ) : (
                        <span className="sync-panel__no-artifacts">no artifacts yet</span>
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          </>
        )
      ) : null}

      <div className="picker__buttons">
        <button type="button" onClick={onClose}>
          close
        </button>
      </div>
    </div>
  );
}
