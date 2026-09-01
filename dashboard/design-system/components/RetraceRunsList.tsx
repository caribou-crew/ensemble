import { Badge, Spinner } from '../primitives';
import { useAsync } from '../useAsync';
import { retraceMessageOf, type RetraceClient } from '../retraceClient';
import type { RunRow } from '../retraceTypes';
import { verdictTone, verdictLabel } from '../retraceTone';
import { formatWhen } from '../retraceWhen';
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
  const runs = data?.runs ?? [];
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
            <th className="queue-table__col-when">when</th>
            <th className="queue-table__col-source">source</th>
            <th className="queue-table__col-verdict">verdict</th>
            <th className="queue-table__col-counts">what changed</th>
            <th className="queue-table__col-chevron" aria-label="open" />
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
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
