import { useState } from 'react';
import { Badge } from '@ensemble/design-system';
import type { EmptyReason, Item } from '../api/types';
import { verdictTone } from '../tone';
import CaptureBanner from './CaptureBanner';
import './QueueList.css';

export const keyOf = (item: { app: string; flow: string }) => `${item.app}/${item.flow}`;

// The one-line counts strip. Only planes with something to say appear: "0
// shots · 0 wire" on every row is noise that trains a reviewer to skip the
// strip, which is the opposite of what it is for.
function countsStrip(item: Item): string {
  const parts: string[] = [];
  const c = item.counts;
  if (c.pixelChanged > 0) parts.push(`${c.pixelChanged} shots`);
  const wire = c.wireChanged + c.wireMissing + c.wireExtra;
  if (wire > 0) parts.push(`${wire} wire`);
  if (c.hopNew > 0) parts.push(`+${c.hopNew} hop`);
  if (c.hopGone > 0) parts.push(`-${c.hopGone} hop`);
  if (c.violations > 0) parts.push(`${c.violations} violations`);
  return parts.join(' · ');
}

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
    <li className={`queue-row${selected ? ' queue-row--selected' : ''}`}>
      <button
        type="button"
        className="queue-row__button"
        aria-current={selected ? 'true' : undefined}
        onClick={onSelect}
        onDoubleClick={onOpen}
      >
        <span className="queue-row__flow">
          {item.app}/{item.flow}
        </span>
        <Badge tone={verdictTone(item.verdict)}>{item.verdict}</Badge>
        {/* item.gates is ALWAYS an array — see the presence note in
            api/types.ts and TestAPassingItemSerialisesGatesAsAnEmptyArray on
            the Go side. It used to be omitted on exactly these healthy rows. */}
        <span className="queue-row__gates">
          {item.gates.length} {item.gates.length === 1 ? 'gate' : 'gates'}
        </span>
        <span className="queue-row__counts">{strip}</span>
      </button>
      <CaptureBanner capture={item.capture} />
      {item.gates.length > 0 ? (
        <ul className="queue-row__reasons">
          {item.gates.map((g) => (
            <li key={g}>{g}</li>
          ))}
        </ul>
      ) : null}
    </li>
  );
}

// R-V. The two empty worlds render DIFFERENTLY, and neither is derived from
// items.length here: the server decided, EmptyReasonFor is the one place that
// decides, and this renders what it decided.
//
// `EmptyAllClear` requires positive evidence on the server side (at least one
// flow, compared, all scoring zero). Re-deriving it from an empty list would
// make the reassuring answer the one nobody has to earn.
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
      // The zero value, and it promises nothing. The server sends it when the
      // queue has rows; reaching it with none means the server did not say
      // which world this is, and the one thing this must not do is guess the
      // reassuring one.
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

// An unhandled fourth value is a TYPE error at this line, not a blank pane at
// the reviewer's desk.
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
export default function QueueList({
  items,
  empty,
  selected,
  onSelect,
  onOpen,
}: {
  items: Item[];
  empty: EmptyReason;
  selected: string | null;
  onSelect: (item: Item) => void;
  onOpen: (item: Item) => void;
}) {
  const [showPassing, setShowPassing] = useState(false);
  const needsAttention = items.filter((i) => i.score > 0);
  const passing = items.filter((i) => i.score === 0);

  if (items.length === 0) {
    return <Empty reason={empty} />;
  }

  return (
    <div className="queue">
      {needsAttention.length === 0 ? <Empty reason={empty} /> : null}
      <ul className="queue__list">
        {needsAttention.map((item) => (
          <Row
            key={keyOf(item)}
            item={item}
            selected={selected === keyOf(item)}
            onSelect={() => onSelect(item)}
            onOpen={() => onOpen(item)}
          />
        ))}
      </ul>
      {passing.length > 0 ? (
        <div className="queue__passing">
          <button
            type="button"
            className="queue__disclosure"
            aria-expanded={showPassing}
            onClick={() => setShowPassing((v) => !v)}
          >
            {showPassing ? '▾' : '▸'} {passing.length} passing
          </button>
          {showPassing ? (
            <ul className="queue__list">
              {passing.map((item) => (
                <Row
                  key={keyOf(item)}
                  item={item}
                  selected={selected === keyOf(item)}
                  onSelect={() => onSelect(item)}
                  onOpen={() => onOpen(item)}
                />
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
