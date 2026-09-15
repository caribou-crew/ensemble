// Two clients' traffic on one shared row axis.
//
// Each row slot spans both columns; a hop occupies its own client's column
// and leaves the other empty. A blank cell opposite a call is the whole
// point of the view — it says that call happened on one client and not the
// other, at that point in the sequence.
//
// The cells are compact rather than the merged table's twelve columns: half
// the width cannot carry seq/trace/route/size/delay and still leave the
// method and path readable, and the path is what a divergence looks like.
// Everything a cell DOES render goes through hopFormat, the same helpers
// the merged table uses, so the two shapes cannot drift on what a status
// or a size means. Full detail stays one click away in HopDetail.
import { Badge } from '@ensemble/design-system';
import type { Hop } from '../api/types';
import type { PaneRow } from '../splitPanes';
import { formatTimestamp, payloadSize, statusClass, statusIcon } from './hopFormat';
import './SplitHopPanes.css';

export interface SplitHopPanesProps {
  rows: PaneRow[];
  leftLabel: string;
  rightLabel: string;
  selectedSeq: number | null;
  onSelectHop: (hop: Hop) => void;
}

function HopCell({
  hop,
  selected,
  onSelect,
}: {
  hop: Hop | null;
  selected: boolean;
  onSelect: (hop: Hop) => void;
}) {
  if (!hop) {
    // Not empty-but-interactive: an absent call is not a thing to click, and
    // making it look like one would invite reading the gap as a row.
    return <td className="split-panes__cell split-panes__cell--empty" aria-label="no call" />;
  }
  return (
    <td
      className={`split-panes__cell${selected ? ' split-panes__cell--selected' : ''}`}
      data-seq={hop.seq}
      onClick={() => onSelect(hop)}
    >
      <div className="split-panes__line">
        <span className={`split-panes__status-icon ${statusClass(hop)}`} aria-hidden="true">
          {statusIcon(hop)}
        </span>
        <span className="split-panes__method">{hop.method ?? '—'}</span>
        <span className="split-panes__path" title={hop.path}>
          {hop.path}
        </span>
        <span className={`split-panes__status ${statusClass(hop)}`}>{hop.err ? 'err' : (hop.status ?? '—')}</span>
      </div>
      <div className="split-panes__meta">
        <span className="split-panes__to">{hop.to}</span>
        <span className="split-panes__time">{formatTimestamp(hop.t.start)}</span>
        <span className="split-panes__size">{payloadSize(hop)}</span>
        {hop.t.doneMs !== undefined ? (
          <span className="split-panes__done">{Math.round(hop.t.doneMs)}ms</span>
        ) : null}
        {hop.preflight && <Badge tone="blue">preflight</Badge>}
        {hop.unsupported && <Badge tone="red">{hop.unsupported}</Badge>}
      </div>
    </td>
  );
}

export default function SplitHopPanes({
  rows,
  leftLabel,
  rightLabel,
  selectedSeq,
  onSelectHop,
}: SplitHopPanesProps) {
  return (
    <table className="split-panes">
      <thead>
        <tr>
          <th className="split-panes__head">{leftLabel}</th>
          <th className="split-panes__head">{rightLabel}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr key={row.key} className="split-panes__row" data-row-key={row.key}>
            <HopCell hop={row.left} selected={row.left?.seq === selectedSeq} onSelect={onSelectHop} />
            <HopCell hop={row.right} selected={row.right?.seq === selectedSeq} onSelect={onSelectHop} />
          </tr>
        ))}
      </tbody>
    </table>
  );
}
