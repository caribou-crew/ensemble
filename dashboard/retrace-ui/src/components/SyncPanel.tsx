import { useState, type FormEvent } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { api, messageOf } from '../api/client';
import type { SyncCandidate } from '../api/types';

/**
 * Discover -> filter -> select -> pull, over retrace/serve's own
 * GET /api/sync/candidates and POST /api/sync. Unlike ensemble-ui's
 * equivalent panel, this package's config carries no repo default —
 * retrace.yaml has no `sync:` block — so the repo box here is required and
 * nothing pre-fills it.
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
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [loading, setLoading] = useState(false);
  const [pulling, setPulling] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  const load = async (e: FormEvent) => {
    e.preventDefault();
    const trimmed = repo.trim();
    if (trimmed === '') return;
    setLoading(true);
    setError(null);
    setResult(null);
    try {
      const res = await api.syncCandidates(trimmed);
      setCandidates(res.candidates);
      setSelected(new Set());
    } catch (err) {
      setError(messageOf(err, 'could not list candidates'));
      setCandidates(null);
    } finally {
      setLoading(false);
    }
  };

  const toggle = (databaseId: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(databaseId)) next.delete(databaseId);
      else next.add(databaseId);
      return next;
    });
  };

  const pull = async () => {
    if (!candidates || selected.size === 0) return;
    setPulling(true);
    setError(null);
    setResult(null);
    try {
      const selections = candidates
        .filter((c) => selected.has(c.databaseId))
        .map((c) => ({ repo: c.repo, databaseId: c.databaseId }));
      const res = await api.sync(repo.trim(), selections);
      const skippedNote = res.skipped.length > 0 ? `, skipped ${res.skipped.length}` : '';
      setResult(`pulled ${res.synced.length}${skippedNote}`);
      onSynced();
    } catch (err) {
      setError(messageOf(err, 'pull failed'));
    } finally {
      setPulling(false);
    }
  };

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
      {result ? <p className="sync-panel__result">{result}</p> : null}

      {candidates ? (
        candidates.length === 0 ? (
          <p className="sync-panel__empty">No matching runs found in {repo.trim()}.</p>
        ) : (
          <>
            <ul className="sync-panel__candidates">
              {candidates.map((c) => {
                const pullable = c.hasArtifacts;
                const tone = c.conclusion === 'success' ? 'green' : c.conclusion === 'failure' ? 'red' : 'neutral';
                return (
                  <li key={c.databaseId} className="sync-panel__candidate">
                    <label>
                      <input
                        type="checkbox"
                        checked={selected.has(c.databaseId)}
                        disabled={!pullable}
                        onChange={() => toggle(c.databaseId)}
                      />
                      <span className="sync-panel__workflow">{c.workflowName}</span>
                      <Badge tone={tone}>{c.conclusion || c.status}</Badge>
                      <span className="sync-panel__meta">
                        {c.headBranch} · {c.actor} · {c.event} · {c.createdAt}
                      </span>
                      {!pullable ? (
                        <span className="sync-panel__no-artifacts">no artifacts yet</span>
                      ) : null}
                    </label>
                  </li>
                );
              })}
            </ul>
            <div className="picker__buttons">
              <button type="button" onClick={onClose} disabled={pulling}>
                close
              </button>
              <button type="button" onClick={() => void pull()} disabled={pulling || selected.size === 0}>
                {pulling ? <Spinner /> : `Pull ${selected.size} selected`}
              </button>
            </div>
          </>
        )
      ) : null}
    </div>
  );
}
