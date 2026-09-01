import { Badge } from '../primitives';
import CaptureBanner from './CaptureBanner';
import type { EmptyReason, Item } from '../retraceTypes';
import { verdictTone, verdictLabel } from '../retraceTone';
import './RetraceQueueList.css';

export const keyOf = (item: { app: string; flow: string }) => `${item.app}/${item.flow}`;

/**
 * The partition this component renders, and the ONE definition of it.
 *
 * `score > 0` is the server's own contract for "needs attention" — ScoreOf
 * floors every non-`pass` verdict above zero precisely so this line can stay
 * an exact test rather than an approximation. It is exported because a
 * consumer's keyboard navigation must walk the same list the screen shows;
 * two copies of this filter is how `j` came to move the selection onto a
 * row nobody can see.
 */
export function partitionQueue(items: Item[]): { needsAttention: Item[]; passing: Item[] } {
  return {
    needsAttention: items.filter((i) => i.score > 0),
    passing: items.filter((i) => i.score === 0),
  };
}

/**
 * The rows actually ON SCREEN, in the order they are rendered.
 *
 * This is what `j`/`k` walk. Walking the raw server list instead put the
 * selection on a collapsed row: nothing on screen carried aria-current any
 * more, so the key looked like a no-op and the reviewer pressed it again —
 * and `enter` then opened a flow they never saw, while `a` (a filesystem
 * mutation) fired against it.
 */
export function visibleRows(items: Item[], showPassing: boolean): Item[] {
  const { needsAttention, passing } = partitionQueue(items);
  return showPassing ? [...needsAttention, ...passing] : needsAttention;
}

// The one-line counts strip. Only planes with something to say appear: "0
// shots · 0 wire" on every row is noise that trains a reviewer to skip the
// strip, which is the opposite of what it is for. It covers EVERY count
// diff.changed() keys on: wireMoved, conformance and unexpectedStatuses
// alone would otherwise flag amber with an empty strip and no explanation.
function countsStrip(item: Item): string {
  const parts: string[] = [];
  const c = item.counts;
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
 * `app` and `flow` are separate columns rather than one "app/flow" string —
 * a repo with several build variants recorded under one .retrace/runs tree
 * (retrace.repo.yaml's `apps:` map — web, ios-native, ios-rn, ios-flutter,
 * android x3, say) needs the HOST/FRAMEWORK to scan as its own column, not
 * be read out of a slash-joined string one row at a time.
 */
function Row({
  item,
  selected,
  onSelect,
  onOpen,
}: {
  item: Item;
  selected: boolean;
  onSelect: () => void;
  onOpen: () => void;
}) {
  const strip = countsStrip(item);
  return (
    <>
      <tr
        className={`queue-row${selected ? ' queue-row--selected' : ''}`}
        aria-current={selected ? 'true' : undefined}
        role="button"
        tabIndex={0}
        onClick={onSelect}
        onDoubleClick={onOpen}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            onSelect();
          }
        }}
      >
        <td className="queue-row__app">{item.app}</td>
        <td className="queue-row__flowname">{item.flow}</td>
        <td className="queue-row__verdict">
          <Badge tone={verdictTone(item.verdict)}>{verdictLabel(item.verdict)}</Badge>
        </td>
        <td className="queue-row__gates">
          {item.gates.length} {item.gates.length === 1 ? 'gate' : 'gates'}
        </td>
        <td className="queue-row__counts">{strip}</td>
      </tr>
      <tr className="queue-row__detail-row">
        <td colSpan={5}>
          <CaptureBanner capture={item.capture} />
          {item.gates.length > 0 ? (
            <ul className="queue-row__reasons">
              {item.gates.map((g) => (
                <li key={g}>{g}</li>
              ))}
            </ul>
          ) : null}
        </td>
      </tr>
    </>
  );
}

function QueueTable({
  items,
  selected,
  onSelect,
  onOpen,
}: {
  items: Item[];
  selected: string | null;
  onSelect: (item: Item) => void;
  onOpen: (item: Item) => void;
}) {
  return (
    <table className="queue-table">
      <thead>
        <tr>
          <th className="queue-table__col-app">app</th>
          <th className="queue-table__col-flow">flow</th>
          <th className="queue-table__col-verdict">verdict</th>
          <th className="queue-table__col-gates">gates</th>
          <th className="queue-table__col-counts">what changed</th>
        </tr>
      </thead>
      <tbody>
        {items.map((item) => (
          <Row
            key={keyOf(item)}
            item={item}
            selected={selected === keyOf(item)}
            onSelect={() => onSelect(item)}
            onOpen={() => onOpen(item)}
          />
        ))}
      </tbody>
    </table>
  );
}

// The two empty worlds render DIFFERENTLY, and neither is derived from
// items.length here: the server decided (EmptyReasonFor), and this renders
// what it decided.
function Empty({ reason }: { reason: EmptyReason }) {
  switch (reason) {
    case 'all-clear':
      return (
        <div className="queue-empty queue-empty--all-clear">
          <Badge tone="green">all clear</Badge>
          <p>
            Every recorded flow was compared against its reference and none of them needs
            attention.
          </p>
        </div>
      );
    case 'no-runs':
      // The dangerous arm. Rendered as the SETUP STEP it is, never as
      // reassurance: a reviewer who reads this as "all clear" concludes the
      // project is clean on the strength of never having recorded anything.
      return (
        <div className="queue-empty queue-empty--no-runs">
          <Badge tone="amber">nothing recorded</Badge>
          <p>
            No runs have been recorded yet, so nothing has been compared and this queue can say
            nothing about whether the project is healthy.
          </p>
          <p className="queue-empty__setup">
            Record a flow with <code>retrace run --flow &lt;flow&gt;</code>, then bless a
            known-good one with <code>retrace ref accept --flow &lt;flow&gt;</code>. Until both
            have happened there is nothing here to review.
          </p>
        </div>
      );
    case '':
      return (
        <div className="queue-empty">
          <Badge tone="neutral">no rows</Badge>
          <p>The server did not say why this queue is empty.</p>
        </div>
      );
    default:
      return assertNever(reason);
  }
}

// An unhandled fourth value is a TYPE error at this line, not a blank pane
// at the reviewer's desk.
function assertNever(reason: never): null {
  void reason;
  return null;
}

/**
 * The review queue, worst first.
 *
 * The order is the SERVER'S — items arrive sorted by serve.ScoreOf with its
 * own app/flow tiebreak, and re-sorting here (even "by the same score") would
 * drop the tiebreak and put a second ordering rule in a second language.
 * Score-zero rows collapse under a disclosure, which is what the score-0
 * contract is for.
 */
export default function RetraceQueueList({
  items,
  empty,
  selected,
  showPassing,
  onShowPassingChange,
  onSelect,
  onOpen,
}: {
  items: Item[];
  empty: EmptyReason;
  selected: string | null;
  // The disclosure is CONTROLLED by the caller rather than owned here,
  // because keyboard navigation has to know which rows are on screen.
  showPassing: boolean;
  onShowPassingChange: (next: boolean) => void;
  onSelect: (item: Item) => void;
  onOpen: (item: Item) => void;
}) {
  const { needsAttention, passing } = partitionQueue(items);

  if (items.length === 0) {
    return <Empty reason={empty} />;
  }

  return (
    <div className="queue">
      {needsAttention.length === 0 ? <Empty reason={empty} /> : null}
      {needsAttention.length > 0 ? (
        <QueueTable items={needsAttention} selected={selected} onSelect={onSelect} onOpen={onOpen} />
      ) : null}
      {passing.length > 0 ? (
        <div className="queue__passing">
          <button
            type="button"
            className="queue__disclosure"
            aria-expanded={showPassing}
            onClick={() => onShowPassingChange(!showPassing)}
          >
            {showPassing ? '▾' : '▸'} {passing.length} passing
          </button>
          {showPassing ? (
            <QueueTable items={passing} selected={selected} onSelect={onSelect} onOpen={onOpen} />
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
