// Cross-app retrace review queue, embedded directly from retrace/serve (no
// separate `retrace serve` process — see openspec/changes/retrace-ci-sync).
// Manual "Sync now" only, no auto-poll (design.md D3: a developer-triggered
// GitHub Actions pull, not something to run silently every few seconds).
import { useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import CaptureBanner from '@ensemble/design-system/components/CaptureBanner';
import HopDeltaList from '@ensemble/design-system/components/HopDeltaList';
import ShotCompare from '@ensemble/design-system/components/ShotCompare';
import WireDiffTable, { entryKey } from '@ensemble/design-system/components/WireDiffTable';
import type { Entry, FieldDiff } from '@ensemble/design-system/diffTypes';
import { api, messageOf, resolveRetraceShotUrl } from '../api/client';
import type { RetraceItem, RetraceSummary } from '../api/types';
import './RetraceView.css';

// Same four-way tone mapping retrace-ui's tone.ts uses — quarantined reads
// amber ("could not evaluate"), not red ("evaluated and bad").
function verdictTone(v: RetraceItem['verdict']): 'green' | 'amber' | 'red' {
  switch (v) {
    case 'pass':
      return 'green';
    case 'changed':
      return 'amber';
    case 'failed':
      return 'red';
    case 'quarantined':
      return 'amber';
  }
}

// Mirrors retrace-ui/src/components/QueueList.tsx's countsStrip: every plane
// diff.changed() keys on, so a reorder-only or conformance-only flow still
// says why it's flagged instead of rendering an amber badge with nothing
// beside it.
function countsStrip(item: RetraceItem): string {
  const c = item.counts;
  const parts: string[] = [];
  if (c.pixelChanged > 0) parts.push(`${c.pixelChanged} shots`);
  const wire = c.wireChanged + c.wireMissing + c.wireExtra;
  if (wire > 0) parts.push(`${wire} wire`);
  if (c.wireMoved > 0) parts.push(`${c.wireMoved} reordered`);
  if (c.hopNew > 0) parts.push(`+${c.hopNew} hop`);
  if (c.hopGone > 0) parts.push(`-${c.hopGone} hop`);
  if (c.violations > 0) parts.push(`${c.violations} violations`);
  if (c.unexpectedStatuses > 0) parts.push(`${c.unexpectedStatuses} unexpected statuses`);
  if (c.conformance > 0) parts.push(`${c.conformance} conformance`);
  return parts.length > 0 ? parts.join(' · ') : 'no differences';
}

/** `runId` is `runs.NewRunID`'s `<YYYYMMDDTHHMMSSZ>-<sha>` — the queue row
 * carries no separate timestamp field, so "when" is read off the run id's
 * own prefix rather than duplicating it on the wire. */
function recordedAt(runId: string): Date | null {
  const m = /^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z/.exec(runId);
  if (!m) return null;
  const [, y, mo, d, h, mi, s] = m;
  const parsed = new Date(`${y}-${mo}-${d}T${h}:${mi}:${s}Z`);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

function SourceBadge({ item }: { item: RetraceItem }) {
  if (!item.source) return <Badge tone="neutral">local</Badge>;
  return (
    <span title={`${item.source.workflow} — ${item.source.sha.slice(0, 7)}`}>
      <Badge tone="blue">CI</Badge>
    </span>
  );
}

function QueueTable({
  items,
  selected,
  onSelect,
}: {
  items: RetraceItem[];
  selected: { app: string; flow: string } | null;
  onSelect: (app: string, flow: string) => void;
}) {
  if (items.length === 0) {
    return <p className="retrace-view__empty">No retrace runs recorded yet.</p>;
  }
  return (
    <table className="retrace-table">
      <thead>
        <tr>
          <th>flow</th>
          <th>verdict</th>
          <th>what changed</th>
          <th>when</th>
          <th>source</th>
        </tr>
      </thead>
      <tbody>
        {items.map((item) => {
          const at = recordedAt(item.runId);
          const isSelected = selected?.app === item.app && selected?.flow === item.flow;
          return (
            <tr
              key={`${item.app}/${item.flow}`}
              className={`retrace-table__row${isSelected ? ' retrace-table__row--selected' : ''}`}
              onClick={() => onSelect(item.app, item.flow)}
            >
              <td className="retrace-table__flow">
                {item.app}/{item.flow}
              </td>
              <td>
                <Badge tone={verdictTone(item.verdict)}>{item.verdict}</Badge>
              </td>
              <td className="retrace-table__changed">{countsStrip(item)}</td>
              <td className="retrace-table__when">{at ? at.toLocaleString() : '—'}</td>
              <td>
                <SourceBadge item={item} />
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function DetailPane({ app, flow, summary }: { app: string; flow: string; summary: RetraceSummary }) {
  const [selectedField, setSelectedField] = useState<string | null>(null);

  function onSelectField(entry: Entry, field: FieldDiff) {
    setSelectedField(`${entryKey(entry)}|${field.scope}:${field.path}`);
  }

  return (
    <div className="retrace-detail">
      <header className="retrace-detail__header">
        <h2>
          {app}/{flow}
        </h2>
        <Badge tone={verdictTone(summary.verdict)}>{summary.verdict}</Badge>
        <span className="retrace-detail__runs">
          {summary.a.runId || 'no reference'} → {summary.b.runId || 'no run'}
        </span>
      </header>

      <CaptureBanner capture={summary.capture} detail />

      {summary.verdict === 'quarantined' ? (
        <div className="retrace-detail__quarantine">
          <p>This flow was not compared — every plane below is empty because the comparison never ran.</p>
          <ul>
            {summary.quarantined.map((q) => (
              <li key={`${q.side}:${q.reason}`}>
                side {q.side}: {q.reason}
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <>
          <section className="retrace-detail__plane">
            <h3>shots</h3>
            {summary.checkpoints.length === 0 ? (
              <p className="retrace-detail__none">This flow captured no checkpoints.</p>
            ) : (
              summary.checkpoints.map((cp) => (
                <ShotCompare key={cp.name} app={app} flow={flow} checkpoint={cp} resolveShotUrl={resolveRetraceShotUrl} />
              ))
            )}
          </section>

          <section className="retrace-detail__plane">
            <h3>wire</h3>
            <WireDiffTable
              sections={summary.sections}
              selectedField={selectedField}
              onSelectField={onSelectField}
              onReveal={() => api.retraceItem(app, flow).then((s) => s.sections)}
            />
          </section>

          <section className="retrace-detail__plane">
            <h3>hops</h3>
            <HopDeltaList hops={summary.hops} />
          </section>
        </>
      )}
    </div>
  );
}

export default function RetraceView() {
  const [tick, setTick] = useState(0);
  const { data, error, loading } = useAsync(() => api.retraceQueue(), [tick]);

  const [selected, setSelected] = useState<{ app: string; flow: string } | null>(null);
  const { data: summary, error: itemError } = useAsync(async () => {
    if (!selected) return null;
    return api.retraceItem(selected.app, selected.flow);
  }, [selected]);

  const [syncing, setSyncing] = useState(false);
  const [syncError, setSyncError] = useState<string | null>(null);
  const [lastSynced, setLastSynced] = useState<Date | null>(null);

  async function syncNow() {
    setSyncing(true);
    setSyncError(null);
    try {
      await api.retraceSync();
      setLastSynced(new Date());
      setTick((t) => t + 1);
    } catch (err) {
      setSyncError(messageOf(err, 'sync failed'));
    } finally {
      setSyncing(false);
    }
  }

  if (error) {
    return (
      <div className="retrace-view retrace-view--error">
        <Badge tone="red">offline</Badge>
        <span>{messageOf(error, 'failed to reach the ensemble API')}</span>
      </div>
    );
  }

  if (loading && !data) {
    return (
      <div className="retrace-view retrace-view--loading">
        <Spinner />
        <span>loading retrace queue…</span>
      </div>
    );
  }

  const items = data?.items ?? [];

  return (
    <div className="retrace-view">
      <div className="retrace-view__toolbar">
        <button type="button" onClick={() => void syncNow()} disabled={syncing}>
          {syncing ? <Spinner /> : 'Sync now'}
        </button>
        {lastSynced && <span className="retrace-view__hint">last synced {lastSynced.toLocaleTimeString()}</span>}
        {syncError && <span className="retrace-view__sync-error">{syncError}</span>}
        {data?.empty === 'no-runs' && <span className="retrace-view__hint">no runs recorded yet</span>}
        {data?.empty === 'all-clear' && <span className="retrace-view__hint">all clear</span>}
      </div>
      <div className="retrace-view__body">
        <QueueTable items={items} selected={selected} onSelect={(app, flow) => setSelected({ app, flow })} />
        {selected && summary && <DetailPane app={selected.app} flow={selected.flow} summary={summary} />}
        {selected && itemError && (
          <p className="retrace-view__item-error">{messageOf(itemError, 'failed to load flow detail')}</p>
        )}
      </div>
    </div>
  );
}
