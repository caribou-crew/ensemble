// Virtualization-free traffic table — the recorder's ring is bounded (see
// core/proxy.Recorder), so even the whole live buffer is small enough to
// render as a plain <table>. Consecutive rows sharing a traceId are one
// "chain" — reordered by causalHopOrder and indented by hopDepths, the
// exact pair TopologyView's trace-mode waterfall already uses (Task 3.2),
// so a request's shape reads the same way in both views.
import { useMemo } from 'react';
import { Badge } from '@ensemble/design-system';
import type { Hop } from '../api/types';
import { causalHopOrder } from '../topology/traceLayout';
import { hopDepths } from '../topology/hopTimeline';
import { INFERRED_CALLER_TITLE, isInferredCaller } from './attribution';
import './HopTable.css';

export interface HopTableProps {
  hops: Hop[];
  selectedSeq: number | null;
  onSelectHop: (hop: Hop) => void;
  /** Jumps straight to the trace's topology waterfall, bypassing row
   * selection — same destination as HopDetail's "view in topology" link. */
  onViewTrace?: (traceId: string) => void;
}

interface Row {
  hop: Hop;
  depth: number;
  chainStart: boolean;
}

/** Groups every hop sharing a traceId into one "chain" — wherever in the
 * array they landed, not just when they happen to sit consecutively —
 * then reorders each chain by causal call order and indents by
 * call-stack depth within it. A hop with no traceId is its own
 * single-hop chain at depth 0. Chains are emitted in the order their
 * first hop originally appeared, so unrelated traffic keeps its own
 * relative position.
 *
 * Hops within one trace are RECORDED inner-first (see core/proxy/proxy.go:
 * a gateway's own "client → gateway" hop only completes, and gets
 * Recorded, after the "gateway → bff" hop it's waiting on) — the two legs
 * of a single request are adjacent in the array only when nothing else
 * completes in between. On a live stack, concurrent unrelated traffic
 * (parallel page-load fetches, health-check polling) routinely lands a
 * hop between them; a strict-adjacency scan used to treat that as two
 * separate one-hop "chains" and leave both in raw completion order —
 * "gateway → bff" reading before "client → gateway", the reverse of what
 * actually happened. Exported for the grouping test. */
export function buildRows(hops: Hop[]): Row[] {
  const order: string[] = [];
  const groups = new Map<string, Hop[]>();
  hops.forEach((hop, i) => {
    // A hop with no traceId never groups with anything else, including
    // another traceId-less hop — each key is unique to its index.
    const key = hop.traceId ?? `__no-trace-${i}`;
    let chain = groups.get(key);
    if (!chain) {
      chain = [];
      groups.set(key, chain);
      order.push(key);
    }
    chain.push(hop);
  });

  const rows: Row[] = [];
  order.forEach((key) => {
    const chain = groups.get(key) as Hop[];
    const hasTraceId = chain[0].traceId !== undefined;
    const ordered = hasTraceId ? causalHopOrder(chain) : chain;
    const depths = hasTraceId ? hopDepths(ordered) : [0];
    ordered.forEach((hop, idx) => {
      rows.push({ hop, depth: depths[idx] ?? 0, chainStart: idx === 0 });
    });
  });
  return rows;
}

function sessionLabel(session?: string): string {
  return session ? session.slice(0, 8) : 'ambient';
}

/** HH:MM:SS:mmm in the viewer's local time, from t.start (when the proxy
 * first saw the request, before any injected latency). */
function formatTimestamp(start: string): string {
  const d = new Date(start);
  if (Number.isNaN(d.getTime())) return '—';
  const pad = (n: number, len = 2) => String(n).padStart(len, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}:${pad(d.getMilliseconds(), 3)}`;
}

const textEncoder = new TextEncoder();

/** Byte length of a captured body — TextEncoder, not .length, so a
 * multi-byte UTF-8 body (unicode text, not just ASCII JSON) reports its
 * real wire size rather than its UTF-16 code-unit count. */
function byteLength(body?: string): number {
  return body ? textEncoder.encode(body).length : 0;
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`;
  return `${(n / 1024 / 1024).toFixed(1)}MB`;
}

/** Combined request+response payload size. Bodies are captured up to
 * core/proxy.CaptureLimit — a truncated one reports only what was
 * actually captured, flagged with a trailing "+" so it doesn't read as
 * the true wire size. */
function payloadSize(hop: Hop): string {
  const bytes = byteLength(hop.req?.body) + byteLength(hop.resp?.body);
  const truncated = Boolean(hop.req?.truncated || hop.resp?.truncated);
  return formatBytes(bytes) + (truncated ? '+' : '');
}

export default function HopTable({ hops, selectedSeq, onSelectHop, onViewTrace }: HopTableProps) {
  const rows = useMemo(() => buildRows(hops), [hops]);

  return (
    <table className="hop-table">
      <thead>
        <tr>
          <th>seq</th>
          <th>time</th>
          <th>session</th>
          <th>trace</th>
          <th>route</th>
          <th>request</th>
          <th>status</th>
          <th>size</th>
          <th>done</th>
          <th>delay</th>
        </tr>
      </thead>
      <tbody>
        {rows.map(({ hop, depth, chainStart }) => {
          const isError = (hop.status ?? 0) >= 400 || Boolean(hop.err);
          const classes = [
            'hop-table__row',
            chainStart ? 'hop-table__row--chain-start' : '',
            selectedSeq === hop.seq ? 'hop-table__row--selected' : '',
          ]
            .filter(Boolean)
            .join(' ');
          return (
            <tr
              key={hop.seq}
              data-seq={hop.seq}
              data-depth={depth}
              className={classes}
              onClick={() => onSelectHop(hop)}
            >
              <td className="hop-table__seq">#{hop.seq}</td>
              <td className="hop-table__time">{formatTimestamp(hop.t.start)}</td>
              <td>
                <Badge tone={hop.session ? 'accent' : 'neutral'}>{sessionLabel(hop.session)}</Badge>
              </td>
              <td className="hop-table__trace">
                {hop.traceId ? (
                  <button
                    type="button"
                    className="hop-table__trace-link"
                    title={hop.traceId}
                    onClick={(e) => {
                      // Don't also select the row underneath — this is a
                      // dedicated jump to the trace waterfall, not a detail lookup.
                      e.stopPropagation();
                      onViewTrace?.(hop.traceId as string);
                    }}
                  >
                    {hop.traceId.slice(0, 8)}
                  </button>
                ) : (
                  <span className="hop-table__trace-none">—</span>
                )}
              </td>
              <td className="hop-table__route">
                <span className="hop-table__route-indent" style={{ paddingLeft: depth * 12 }}>
                  {depth > 0 && <span className="hop-table__nest-glyph">↳</span>}
                  {isInferredCaller(hop) ? (
                    <span className="hop-table__caller--inferred" title={INFERRED_CALLER_TITLE}>
                      {hop.from}
                    </span>
                  ) : (
                    hop.from ?? 'client'
                  )} → {hop.to}
                </span>
              </td>
              <td className="hop-table__request">
                {hop.method && <span className="hop-table__method">{hop.method}</span>} {hop.path}
              </td>
              <td className={`hop-table__status${isError ? ' hop-table__status--error' : ''}`}>
                {hop.err ? 'err' : (hop.status ?? '—')}
              </td>
              <td className="hop-table__size">{payloadSize(hop)}</td>
              <td className="hop-table__done">
                {hop.t.doneMs !== undefined ? `${Math.round(hop.t.doneMs)}ms` : '—'}
              </td>
              <td className="hop-table__delay">
                {hop.injectedDelayMs ? <Badge tone="amber">+{Math.round(hop.injectedDelayMs)}ms</Badge> : null}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
