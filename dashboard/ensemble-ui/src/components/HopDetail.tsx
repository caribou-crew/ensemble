// Side panel for one selected hop: headers, bodies (via JsonView), a
// start→firstByte→done timing strip, and export. Every field it renders
// already lives on the Hop the caller has in hand (from /api/traffic or
// the SSE stream) — no extra fetch needed, only the export endpoint is a
// live network call.
import { useEffect, useRef, useState } from 'react';
import { Badge, Tabs } from '@ensemble/design-system';
import type { Hop, Timings } from '../api/types';
import { isRedactedValue } from '../redaction';
import { CALLER_ATTRIBUTION_TITLE, callerAttribution } from './attribution';
import JsonView from './JsonView';
import './HopDetail.css';

export interface HopDetailProps {
  hop: Hop;
  onClose: () => void;
  /** Jumps the dashboard into topology's causal view for this hop's trace
   * (see TrafficView, which wires this to a `?view=topology&trace=` URL
   * patch — TopologyView already reads `?trace=` on its own). Omitted
   * (hop has no traceId) hides the link. */
  onViewTrace?: (traceId: string) => void;
}

type ExportFormat = 'har' | 'curl' | 'raw';

function exportUrl(traceId: string, format: ExportFormat): string {
  return `/api/traces/${encodeURIComponent(traceId)}/export?format=${format}`;
}

// Per-hop copy actions: unlike the trace-level export above, these work for
// every hop (no traceId required — see /api/hops/{seq}/export) and are
// always a plain-text clipboard copy, never a new tab.
type CopyFormat = 'curl' | 'request' | 'response' | 'har';

const COPY_LABELS: Record<CopyFormat, string> = {
  curl: 'copy as curl',
  request: 'copy request',
  response: 'copy response',
  har: 'copy as har',
};

function hopExportUrl(seq: number, format: CopyFormat): string {
  return `/api/hops/${seq}/export?format=${format}`;
}

function HeadersTable({ headers }: { headers?: Record<string, string> }) {
  const entries = Object.entries(headers ?? {});
  if (entries.length === 0) {
    return <p className="hop-detail__empty">no headers</p>;
  }
  return (
    <table className="hop-detail__headers">
      <tbody>
        {entries.map(([name, value]) => {
          const redacted = isRedactedValue(value);
          return (
            <tr key={name}>
              <td className="hop-detail__header-name">{name}</td>
              <td className={redacted ? 'redacted' : undefined} title={redacted ? 'revealed in task 4.8' : undefined}>
                {value}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function TimingStrip({ t, injectedDelayMs }: { t: Timings; injectedDelayMs?: number }) {
  return (
    <dl className="hop-detail__timing">
      <dt>start</dt>
      <dd>{new Date(t.start).toLocaleTimeString()}</dd>
      <dt>first byte</dt>
      <dd>{t.firstByteMs !== undefined ? `${Math.round(t.firstByteMs)}ms` : '—'}</dd>
      <dt>done</dt>
      <dd>{t.doneMs !== undefined ? `${Math.round(t.doneMs)}ms` : '—'}</dd>
      <dt>injected delay</dt>
      <dd>{injectedDelayMs ? `+${Math.round(injectedDelayMs)}ms` : '—'}</dd>
    </dl>
  );
}

export default function HopDetail({ hop, onClose, onViewTrace }: HopDetailProps) {
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const [tab, setTab] = useState<'request' | 'response'>('request');
  const isError = (hop.status ?? 0) >= 400 || Boolean(hop.err);

  // Per-hop copy toolbar: separate from copyState/idleTimerRef above (that
  // pair is the pre-existing trace-level curl export) since up to four of
  // these buttons can show feedback independently — copyAction tracks WHICH
  // one just ran so only that button's label flips to copied!/copy failed.
  const [copyAction, setCopyAction] = useState<CopyFormat | null>(null);
  const [copyResult, setCopyResult] = useState<'copied' | 'failed' | null>(null);
  const copyIdleTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (copyIdleTimerRef.current !== null) window.clearTimeout(copyIdleTimerRef.current);
    };
  }, []);

  async function handleCopy(format: CopyFormat) {
    try {
      const res = await fetch(hopExportUrl(hop.seq, format));
      const text = await res.text();
      await navigator.clipboard.writeText(text);
      setCopyAction(format);
      setCopyResult('copied');
    } catch {
      setCopyAction(format);
      setCopyResult('failed');
    } finally {
      if (copyIdleTimerRef.current !== null) window.clearTimeout(copyIdleTimerRef.current);
      copyIdleTimerRef.current = window.setTimeout(() => {
        copyIdleTimerRef.current = null;
        setCopyAction(null);
        setCopyResult(null);
      }, 1500);
    }
  }

  // A second "curl" export within the 1.5s window used to schedule a SECOND idle timer on
  // top of the first, so the earlier one reset copyState back to 'idle' mid-display of the
  // newer export's own result — and an unmount mid-window wrote to a dead component (final
  // review M7). Tracking the pending timer lets a new export clear the old one before
  // scheduling its own, and the cleanup effect clears it on unmount.
  const idleTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (idleTimerRef.current !== null) window.clearTimeout(idleTimerRef.current);
    };
  }, []);

  async function handleExport(format: ExportFormat) {
    if (!hop.traceId) return;
    const url = exportUrl(hop.traceId, format);
    window.open(url, '_blank', 'noopener');
    if (format !== 'curl') return;
    try {
      const res = await fetch(url);
      const text = await res.text();
      await navigator.clipboard.writeText(text);
      setCopyState('copied');
    } catch {
      // Best-effort clipboard copy — the export tab already opened
      // regardless, so a clipboard-API failure (no permission, no
      // navigator.clipboard in this context) isn't fatal.
      setCopyState('failed');
    } finally {
      if (idleTimerRef.current !== null) window.clearTimeout(idleTimerRef.current);
      idleTimerRef.current = window.setTimeout(() => {
        idleTimerRef.current = null;
        setCopyState('idle');
      }, 1500);
    }
  }

  return (
    <aside className="hop-detail">
      <div className="hop-detail__header">
        <h3>
          #{hop.seq} {hop.method} {hop.path}
        </h3>
        <button type="button" className="hop-detail__close" onClick={onClose} aria-label="close">
          ×
        </button>
      </div>

      <div className="hop-detail__meta">
        <Badge tone={isError ? 'red' : 'green'}>{hop.err ? 'err' : (hop.status ?? '—')}</Badge>
        <span className="hop-detail__route">
          {callerAttribution(hop) ? (
            <span
              className={`hop-detail__caller--${callerAttribution(hop)}`}
              title={CALLER_ATTRIBUTION_TITLE[callerAttribution(hop) as 'inferred' | 'declared']}
            >
              {hop.from}
            </span>
          ) : (
            hop.from ?? 'client'
          )} → {hop.to}
        </span>
        {hop.traceId && onViewTrace && (
          <button type="button" className="hop-detail__trace-link" onClick={() => onViewTrace(hop.traceId as string)}>
            view in topology →
          </button>
        )}
      </div>

      {hop.err && <p className="hop-detail__err">{hop.err}</p>}

      <TimingStrip t={hop.t} injectedDelayMs={hop.injectedDelayMs} />

      <div className="hop-detail__copy-toolbar">
        <span className="hop-detail__export-label">copy</span>
        {(Object.keys(COPY_LABELS) as CopyFormat[]).map((format) => (
          <button type="button" key={format} onClick={() => void handleCopy(format)}>
            {copyAction === format ? (copyResult === 'copied' ? 'copied!' : 'copy failed') : COPY_LABELS[format]}
          </button>
        ))}
      </div>

      <Tabs
        items={[
          { id: 'request', label: 'request' },
          { id: 'response', label: 'response' },
        ]}
        active={tab}
        onSelect={(id) => setTab(id as 'request' | 'response')}
      />

      {tab === 'request' ? (
        <section className="hop-detail__section">
          <h4>request headers</h4>
          <HeadersTable headers={hop.req?.headers} />
          <h4>request body</h4>
          <JsonView body={hop.req?.body} truncated={hop.req?.truncated} />
        </section>
      ) : (
        <section className="hop-detail__section">
          <h4>response headers</h4>
          <HeadersTable headers={hop.resp?.headers} />
          <h4>response body</h4>
          <JsonView body={hop.resp?.body} truncated={hop.resp?.truncated} />
        </section>
      )}

      {hop.traceId && (
        <div className="hop-detail__export">
          <span className="hop-detail__export-label">export</span>
          <button type="button" onClick={() => void handleExport('har')}>
            har
          </button>
          <button type="button" onClick={() => void handleExport('curl')}>
            {copyState === 'copied' ? 'copied!' : copyState === 'failed' ? 'copy failed' : 'curl'}
          </button>
          <button type="button" onClick={() => void handleExport('raw')}>
            raw
          </button>
        </div>
      )}
    </aside>
  );
}
