import { Badge } from '../primitives';
import type { PairItem } from '../retraceTypes';
import { verdictLabel, verdictTone } from '../retraceTone';
import { formatWhen } from '../retraceWhen';
import './RetracePairsList.css';

/** A stable React key + the shape a caller uses to build a deep link — one
 * persisted pairing is addressed by side B's identity plus which A side it
 * was compared against. */
export function pairKey(p: PairItem): string {
  return `${p.appB}/${p.flowB}/${p.runB}/${p.pairId}`;
}

/**
 * The cross-app compare listing — every persisted diff `retrace diff -a/-b`
 * left behind (retrace/pairs), worst-verdict styling aside; unlike the
 * same-app queue this is a plain inspection list, not a triage worklist, so
 * there is no worst-first sort or collapse here — rows are whatever order
 * the server sent (newest computed first).
 */
export default function RetracePairsList({
  pairs,
  onOpen,
}: {
  pairs: PairItem[];
  onOpen: (p: PairItem) => void;
}) {
  if (pairs.length === 0) {
    return (
      <p className="pairs__none">
        No cross-app diffs yet — run <code>retrace diff -a &lt;appA&gt;@&lt;sel&gt; -b &lt;appB&gt;@&lt;sel&gt;</code>{' '}
        naming two different apps to persist one.
      </p>
    );
  }
  return (
    <table className="pairs__table">
      <thead>
        <tr>
          <th>apps</th>
          <th>flow</th>
          <th>computed</th>
          <th>verdict</th>
          <th>pixel</th>
          <th>wire</th>
          <th>hop</th>
        </tr>
      </thead>
      <tbody>
        {pairs.map((p) => (
          <tr
            key={pairKey(p)}
            className="pairs__row"
            role="button"
            tabIndex={0}
            onClick={() => onOpen(p)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                onOpen(p);
              }
            }}
          >
            <td>
              {p.appA} → {p.appB}
            </td>
            <td>{p.flowB}</td>
            <td>{formatWhen(p.computedAt, p.pairId)}</td>
            <td>
              <Badge tone={verdictTone(p.verdict)}>{verdictLabel(p.verdict)}</Badge>
            </td>
            <td>{p.counts.pixelChanged}</td>
            <td>
              {p.counts.wireChanged + p.counts.wireMissing + p.counts.wireExtra}
              {p.counts.wireMoved > 0 ? ` (+${p.counts.wireMoved} reordered)` : ''}
            </td>
            <td>{p.counts.hopNew + p.counts.hopGone}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
