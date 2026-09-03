import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Badge, Spinner } from '../primitives';
import { useAsync } from '../useAsync';
import { mergeCandidates, pickLatestPerWorkflow, sinceParam } from '../syncCandidates';
import { retraceMessageOf, type RetraceClient } from '../retraceClient';
import type { SyncCandidate } from '../retraceTypes';
import RetraceItemScreen from './RetraceItemScreen';
import './RetraceSyncPanel.css';

/** One "app/flow/run-id" — SourcesByURL's / sync.Result's own encoding —
 * parsed into its three components. app/flow/run-id are each validated
 * (runs.ValidateComponents) to contain no path separator, so a plain split
 * on "/" always yields exactly these three parts. */
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
// Keyed on repo+databaseId, not databaseId alone: a config that fans sync
// out across several GitHub repos (ensemble.yaml's retrace.repos) can see
// the same numeric databaseId from two different repos.
function candidateKey(c: SyncCandidate): string {
  return `${c.repo}#${c.databaseId}`;
}

/** The read-only run-detail view: this panel's click target for one flow.
 * Reuses RetraceItemScreen exactly as a main queue's own "open a flow"
 * does, pointed at run-scoped routes so a non-latest run's generated
 * diff/overlay images are read from their own cache instead of the
 * "latest" queue's. RetraceItemScreen carries no mutation buttons of its
 * own, so nesting it here risks nothing in a main queue's own state. */
function RunDetail({ client, app, flow, runId, onBack }: { client: RetraceClient } & RunRef & { onBack: () => void }) {
  const item = useAsync(() => client.itemAtRun(app, flow, runId).then((r) => r.summary), [client, app, flow, runId]);

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
        <RetraceItemScreen
          client={client}
          app={app}
          flow={flow}
          summary={item.data}
          selectedField={null}
          onSelectField={() => {}}
          resolveShotUrl={(a, f, side, name) =>
            client.shotUrlAtRun(a, f, runId, side as 'a' | 'b' | 'diff' | 'overlay', name)
          }
          onReveal={() => client.itemAtRun(app, flow, runId).then((r) => r.summary.sections)}
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
 * Discover -> filter -> select -> pull-and-view, over a RetraceClient's own
 * syncCandidates/sync. Each candidate row is the click target — a row
 * already pulled (localRuns non-empty) opens straight into RunDetail with
 * no network call; a row not yet pulled triggers a pull scoped to just that
 * one candidate, then opens from whatever it produced. Either path can land
 * on more than one flow, which RunChooser mediates.
 */
export default function RetraceSyncPanel({
  client,
  onClose,
  onSynced,
  requireRepo = true,
}: {
  client: RetraceClient;
  onClose: () => void;
  onSynced: () => void;
  /**
   * true (the default — retrace-ui's behavior): show a repo input the
   * reviewer must submit before candidates load, since retrace.yaml carries
   * no sync default and nothing here can pre-fill it.
   *
   * false (ensemble-ui's behavior): repo(s) are already configured
   * server-side (ensemble.yaml's `retrace:` block), so the repo box is
   * pointless — candidates load immediately on mount instead.
   */
  requireRepo?: boolean;
}) {
  const [repo, setRepo] = useState('');
  const [candidates, setCandidates] = useState<SyncCandidate[] | null>(null);
  const [loading, setLoading] = useState(!requireRepo);
  const [refreshing, setRefreshing] = useState(false);
  const [pullingKey, setPullingKey] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [chooser, setChooser] = useState<RunRef[] | null>(null);
  const [detail, setDetail] = useState<RunRef | null>(null);
  const [filterText, setFilterText] = useState('');

  // requireRepo === false means the server already knows which repo(s) to
  // list, so the panel loads on mount rather than waiting on a form submit
  // this mode never renders.
  useEffect(() => {
    if (requireRepo) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    client
      .syncCandidates(undefined)
      .then((res) => {
        if (!cancelled) setCandidates(res.candidates);
      })
      .catch((err) => {
        if (!cancelled) setError(retraceMessageOf(err, 'could not list candidates'));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [requireRepo, client]);

  // When requireRepo is true, the server may STILL have a configured default
  // (retrace.repo.yaml's `repo:`, exposed at /sync/config). Prefill the repo
  // box from it and load candidates immediately, so the reviewer doesn't
  // retype what the config already declares. If no default is configured
  // (empty repo), the manual form stays exactly as before.
  useEffect(() => {
    if (!requireRepo) return;
    let cancelled = false;
    client
      .syncConfig()
      .then((cfg) => {
        if (cancelled || !cfg.repo) return;
        setRepo(cfg.repo);
        setLoading(true);
        return client
          .syncCandidates(cfg.repo)
          .then((res) => {
            if (!cancelled) setCandidates(res.candidates);
          })
          .catch((err) => {
            if (!cancelled) setError(retraceMessageOf(err, 'could not list candidates'));
          })
          .finally(() => {
            if (!cancelled) setLoading(false);
          });
      })
      .catch(() => {
        // No /sync/config (older server) or a transient error — fall back to
        // the manual repo form, unchanged.
      });
    return () => {
      cancelled = true;
    };
  }, [requireRepo, client]);

  const load = async (e: FormEvent) => {
    e.preventDefault();
    const trimmed = repo.trim();
    if (trimmed === '') return;
    setLoading(true);
    setError(null);
    try {
      const res = await client.syncCandidates(trimmed);
      setCandidates(res.candidates);
    } catch (err) {
      setError(retraceMessageOf(err, 'could not list candidates'));
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
      const res = await client.syncCandidates(requireRepo ? repo.trim() : undefined, since ? { since } : {});
      setCandidates((prev) => mergeCandidates(prev ?? [], res.candidates));
    } catch (err) {
      setError(retraceMessageOf(err, 'refresh failed'));
    } finally {
      setRefreshing(false);
    }
  };

  const list = candidates ?? [];
  const visible = useMemo(() => {
    const needle = filterText.trim().toLowerCase();
    if (needle === '') return list;
    return list.filter((c) => [c.workflowName, c.headBranch, c.actor, c.event].some((f) => f.toLowerCase().includes(needle)));
  }, [list, filterText]);

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
    const key = candidateKey(c);
    setPullingKey(key);
    try {
      const res = await client.sync(requireRepo ? repo.trim() : undefined, [{ repo: c.repo, databaseId: c.databaseId }]);
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
      setCandidates((prev) => (prev ? prev.map((x) => (candidateKey(x) === key ? { ...x, localRuns: res.synced } : x)) : prev));
      onSynced();
      openPaths(res.synced);
    } catch (err) {
      setError(retraceMessageOf(err, 'pull failed'));
    } finally {
      setPullingKey(null);
    }
  };

  // One-click "pull latest": sync the most recent runs across the
  // configured workflows in a single call (no per-CI-run drilling), then
  // refresh the queue underneath — which shows one row per app/flow at its
  // latest verdict. This is the common reviewer action ("just get me the
  // latest of everything"), distinct from openCandidate's pull-one-run path.
  const [pullingLatest, setPullingLatest] = useState(false);
  const pullLatest = async () => {
    setError(null);
    setPullingLatest(true);
    try {
      // Selecting the freshest candidate per workflow name gives "latest of
      // each lane" without asking the reviewer to pick runs.
      const picks = pickLatestPerWorkflow(list);
      if (picks.length === 0) {
        setError('no runs with artifacts to pull — try refreshing');
        return;
      }
      const res = await client.sync(
        requireRepo ? repo.trim() : undefined,
        picks.map((c) => ({ repo: c.repo, databaseId: c.databaseId })),
      );
      onSynced();
      if (res.synced.length === 0) {
        const reason = res.skipped[0]?.reason;
        setError(reason ? `pull latest produced no new flows: ${reason}` : 'pull latest produced no new flows — everything may already be synced');
        return;
      }
      // Land back on the refreshed queue rather than a chooser: the queue is
      // the "latest per lane" view the reviewer wanted.
      onClose();
    } catch (err) {
      setError(retraceMessageOf(err, 'pull latest failed'));
    } finally {
      setPullingLatest(false);
    }
  };

  if (detail) {
    return (
      <div className="picker sync-panel" role="dialog" aria-label="sync from GitHub Actions">
        <RunDetail client={client} {...detail} onBack={() => setDetail(null)} />
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

      {requireRepo ? (
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
      ) : null}

      {error ? <p className="sync-panel__error">{error}</p> : null}
      {loading && !candidates ? (
        <p className="loading">
          <Spinner /> loading candidates…
        </p>
      ) : null}

      {candidates ? (
        candidates.length === 0 ? (
          <p className="sync-panel__empty">No matching runs found{requireRepo ? ` in ${repo.trim()}` : ''}.</p>
        ) : (
          <>
            <div className="sync-panel__toolbar">
              <input
                type="text"
                className="sync-panel__filter"
                placeholder="filter by workflow, branch, actor, event…"
                value={filterText}
                onChange={(e) => setFilterText(e.target.value)}
              />
              <button type="button" onClick={() => void refresh()} disabled={refreshing || loading}>
                {refreshing ? <Spinner /> : '↻ refresh'}
              </button>
              <button
                type="button"
                className="sync-panel__pull-latest"
                onClick={() => void pullLatest()}
                disabled={pullingLatest || refreshing || loading}
                title="Pull the latest run of each workflow and refresh the queue"
              >
                {pullingLatest ? <Spinner /> : '⇩ pull latest'}
              </button>
            </div>
            <ul className="sync-panel__candidates">
              {visible.map((c) => {
                const tone = c.conclusion === 'success' ? 'green' : c.conclusion === 'failure' ? 'red' : 'neutral';
                const pulled = c.localRuns.length > 0;
                const clickable = pulled || c.hasArtifacts;
                const key = candidateKey(c);
                const pullingThis = pullingKey === key;
                return (
                  <li key={key} className="sync-panel__candidate">
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
              {visible.length === 0 ? <p className="sync-panel__empty">No candidates match this filter.</p> : null}
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
