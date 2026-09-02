import { useState } from 'react';
import { Badge, Spinner } from '../primitives';
import { useAsync } from '../useAsync';
import { retraceMessageOf, type RetraceClient } from '../retraceClient';
import type { RunRow } from '../retraceTypes';
import { verdictTone, verdictLabel } from '../retraceTone';
import { formatWhen, whenMs } from '../retraceWhen';
import './RetraceQueueList.css';

// The same counts strip RetraceQueueList's row uses, over one run's counts.
function countsStrip(counts: RunRow['counts']): string {
  const parts: string[] = [];
  const c = counts;
  if (c.pixelChanged > 0) parts.push(`${c.pixelChanged} shots`);
  const wire = c.wireChanged + c.wireMissing + c.wireExtra;
  if (wire > 0) parts.push(`${wire} wire`);
  if (c.wireMoved > 0) parts.push(`${c.wireMoved} reordered`);
  if (c.hopNew > 0) parts.push(`+${c.hopNew} hop`);
  if (c.hopGone > 0) parts.push(`-${c.hopGone} hop`);
  if (c.violations > 0) parts.push(`${c.violations} violations`);
  if (c.unexpectedStatuses > 0) parts.push(`${c.unexpectedStatuses} unexpected statuses`);
  if (c.conformance > 0) parts.push(`${c.conformance} conformance`);
  return parts.join(' · ');
}

type SortKey = 'default' | 'when' | 'source' | 'verdict' | 'counts';
type SortDir = 'asc' | 'desc';

// No `score` field on a RunRow (unlike Item), so verdict severity is ranked
// locally — failed worst, pass best, matching the server's own worst-first
// intent for the queue.
function verdictRank(r: RunRow): number {
  switch (r.verdict) {
    case 'failed':
      return 3;
    case 'quarantined':
      return 2;
    case 'changed':
      return 1;
    default:
      return 0;
  }
}

function countsTotal(c: RunRow['counts']): number {
  return (
    c.pixelChanged +
    c.wireChanged +
    c.wireMissing +
    c.wireExtra +
    c.wireMoved +
    c.hopNew +
    c.hopGone +
    c.violations +
    c.unexpectedStatuses +
    c.conformance
  );
}

// `source` sorts CI runs above local ones, then by workflow name — the same
// grouping the badge itself shows (CI vs local).
function sourceLabel(r: RunRow): string {
  return r.source ? r.source.workflow || 'CI' : '';
}

/**
 * `default` is newest-first — the server's own order (`client.runs` promises
 * "every run of a surface, newest first") — reproduced explicitly here so a
 * reviewer who sorts by another column and clicks back to `default` lands on
 * the same order every time, not on whatever the server happened to send.
 * Every other column ties back to `when` descending, so a sort that leaves
 * two runs equal (two local runs, say) still reads newest-first.
 */
function sortRunsBy(runs: RunRow[], key: SortKey, dir: SortDir): RunRow[] {
  const whenOf = (r: RunRow) => whenMs(r.when, r.runId);
  if (key === 'default') return [...runs].sort((a, b) => whenOf(b) - whenOf(a));
  const sign = dir === 'asc' ? 1 : -1;
  const cmp = (a: RunRow, b: RunRow): number => {
    switch (key) {
      case 'when':
        return whenOf(a) - whenOf(b);
      case 'source':
        return sourceLabel(a).localeCompare(sourceLabel(b)) || whenOf(b) - whenOf(a);
      case 'verdict':
        return verdictRank(a) - verdictRank(b) || whenOf(b) - whenOf(a);
      case 'counts':
        return countsTotal(a.counts) - countsTotal(b.counts) || whenOf(b) - whenOf(a);
    }
  };
  return [...runs].sort((a, b) => sign * cmp(a, b));
}

/**
 * The runs-list drill-down: every run of one surface (app/flow), newest
 * first — the screen between a queue row and a specific run's detail view.
 * `client` is a createRetraceClient(basePath) instance, so this renders
 * identically whether it's fetching retrace-ui's `/api/queue/.../runs` or
 * ensemble-ui's `/api/retrace/queue/.../runs`.
 */
export default function RetraceRunsList({
  client,
  app,
  flow,
  selectedRun,
  onOpenRun,
}: {
  client: RetraceClient;
  app: string;
  flow: string;
  selectedRun: string | null;
  onOpenRun: (runId: string) => void;
}) {
  const { data, loading, error } = useAsync(() => client.runs(app, flow), [client, app, flow]);

  if (loading) {
    return (
      <p className="loading">
        <Spinner /> loading runs for {app}/{flow}…
      </p>
    );
  }
  if (error) {
    return (
      <div className="problem">
        <Badge tone="red">error</Badge>
        <span>{retraceMessageOf(error, 'could not load the runs for this surface')}</span>
      </div>
    );
  }
  return <RunsTable runs={data?.runs ?? []} app={app} flow={flow} selectedRun={selectedRun} onOpenRun={onOpenRun} />;
}

function RunsTable({
  runs,
  app,
  flow,
  selectedRun,
  onOpenRun,
}: {
  runs: RunRow[];
  app: string;
  flow: string;
  selectedRun: string | null;
  onOpenRun: (runId: string) => void;
}) {
  // Default = newest first, matching the server's own order. Clicking a
  // header sorts by that column; the active header again flips direction; a
  // third click returns to newest-first, mirroring RetraceQueueList's cycle
  // so a reviewer is never stuck in a sort.
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({ key: 'default', dir: 'asc' });
  const cycle = (key: SortKey) =>
    setSort((s) =>
      s.key !== key ? { key, dir: 'asc' } : s.dir === 'asc' ? { key, dir: 'desc' } : { key: 'default', dir: 'asc' },
    );
  const arrow = (key: SortKey) => (sort.key !== key ? '' : sort.dir === 'asc' ? ' ▲' : ' ▼');
  const rows = sortRunsBy(runs, sort.key, sort.dir);

  const Th = ({ col, label, sortKey }: { col: string; label: string; sortKey: SortKey }) => (
    <th className={col}>
      <button
        type="button"
        className={`queue-table__sort${sort.key === sortKey ? ' queue-table__sort--active' : ''}`}
        onClick={() => cycle(sortKey)}
        aria-label={`sort by ${label}`}
      >
        {label}
        {arrow(sortKey)}
      </button>
    </th>
  );

  if (runs.length === 0) {
    return (
      <div className="queue-empty">
        <Badge tone="neutral">no runs</Badge>
        <p>No runs are recorded for {app}/{flow} yet.</p>
      </div>
    );
  }

  return (
    <div className="queue">
      <table className="queue-table">
        <thead>
          <tr>
            <Th col="queue-table__col-when" label="when" sortKey="when" />
            <Th col="queue-table__col-source" label="source" sortKey="source" />
            <Th col="queue-table__col-verdict" label="verdict" sortKey="verdict" />
            <Th col="queue-table__col-counts" label="what changed" sortKey="counts" />
            <th className="queue-table__col-chevron" aria-label="open" />
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr
              key={r.runId}
              className={`queue-row${selectedRun === r.runId ? ' queue-row--selected' : ''}`}
              aria-current={selectedRun === r.runId ? 'true' : undefined}
              role="button"
              tabIndex={0}
              onClick={() => onOpenRun(r.runId)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  onOpenRun(r.runId);
                }
              }}
              title={r.runId}
            >
              <td className="queue-row__when">
                <span className="queue-row__open">{formatWhen(r.when, r.runId)}</span>
              </td>
              <td className="queue-row__source">
                <Badge tone={r.source ? 'blue' : 'neutral'}>{r.source ? 'CI' : 'local'}</Badge>
                {r.source?.workflow ? (
                  <span className="queue-row__workflow" title={r.source.workflow}>
                    {r.source.workflow}
                  </span>
                ) : null}
              </td>
              <td className="queue-row__verdict">
                <Badge tone={verdictTone(r.verdict)}>{verdictLabel(r.verdict)}</Badge>
              </td>
              <td className="queue-row__counts">{countsStrip(r.counts)}</td>
              <td className="queue-row__chevron" aria-hidden="true">
                ›
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
