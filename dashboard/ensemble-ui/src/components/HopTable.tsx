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

/** Groups consecutive same-traceId hops into a "chain", reorders each
 * chain by causal call order, and indents by call-stack depth within it.
 * A hop with no traceId (or one that breaks the run) is its own
 * single-hop chain at depth 0. Exported for the grouping test. */
export function buildRows(hops: Hop[]): Row[] {
  const rows: Row[] = [];
  let i = 0;
  while (i < hops.length) {
    const traceId = hops[i].traceId;
    let j = i + 1;
    if (traceId) {
      while (j < hops.length && hops[j].traceId === traceId) j += 1;
    }
    const chain = hops.slice(i, j);
    const ordered = traceId ? causalHopOrder(chain) : chain;
    const depths = traceId ? hopDepths(ordered) : [0];
    ordered.forEach((hop, idx) => {
      rows.push({ hop, depth: depths[idx] ?? 0, chainStart: idx === 0 });
    });
    i = j;
  }
  return rows;
}

function sessionLabel(session?: string): string {
  return session ? session.slice(0, 8) : 'ambient';
}

export default function HopTable({ hops, selectedSeq, onSelectHop, onViewTrace }: HopTableProps) {
  const rows = useMemo(() => buildRows(hops), [hops]);

  return (
    <table className="hop-table">
      <thead>
        <tr>
          <th>seq</th>
          <th>session</th>
          <th>trace</th>
          <th>route</th>
          <th>request</th>
          <th>status</th>
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
                  {hop.from ?? 'client'} → {hop.to}
                </span>
              </td>
              <td className="hop-table__request">
                {hop.method && <span className="hop-table__method">{hop.method}</span>} {hop.path}
              </td>
              <td className={`hop-table__status${isError ? ' hop-table__status--error' : ''}`}>
                {hop.err ? 'err' : (hop.status ?? '—')}
              </td>
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
