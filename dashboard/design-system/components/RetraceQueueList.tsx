import { useState } from 'react';
import { Badge } from '../primitives';
import type { EmptyReason, Item } from '../retraceTypes';
import { verdictTone, verdictLabel } from '../retraceTone';
import { formatWhen, whenMs } from '../retraceWhen';
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
 * One row per app/flow, latest run wins.
 *
 * A dashboard that aggregates more than one runs root (retrace serve across
 * two .retrace-ref trees, or ensemble's multi-instance config) can emit the
 * same app/flow twice with different verdicts — the same key rendered as two
 * rows, which is both a duplicate-key React warning and exactly the "why is
 * uxt-rn-ios here twice" confusion. Collapse to the newest run per key
 * (runId is a sortable UTC timestamp prefix), so the queue is what a reviewer
 * expects: each flow once, showing its latest verdict. The dropped older
 * duplicate is still reachable in that flow's runs list on the detail page.
 */
export function dedupeByKey(items: Item[]): Item[] {
  const latest = new Map<string, Item>();
  for (const it of items) {
    const k = keyOf(it);
    const prev = latest.get(k);
    if (!prev || (it.runId ?? '') > (prev.runId ?? '')) latest.set(k, it);
  }
  // Preserve the server's worst-first order using the first appearance of each key.
  const seen = new Set<string>();
  const out: Item[] = [];
  for (const it of items) {
    const k = keyOf(it);
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(latest.get(k)!);
  }
  return out;
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
  // All rows are on screen now (needs-attention then passing, one table), so
  // keyboard nav walks the full deduped set. showPassing is retained in the
  // signature for call-site compatibility but no longer hides rows.
  void showPassing;
  const { needsAttention, passing } = partitionQueue(dedupeByKey(items));
  return [...needsAttention, ...passing];
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
// The short, scannable reason for the DETAILS column — a code, not a
// sentence, so every row stays one line and the column reads like a column.
// The full sentence is the row's title= tooltip and the expand-on-select
// region. Codes are derived from the capture TrustReason.Code the server
// already sends (capture-not-assessed, capture-broken, …) and the shape of
// the gate text, never re-litigated here.
function reasonCode(item: Item): string {
  if (item.verdict === 'pass') return '';
  if (item.verdict === 'changed') return ''; // the counts strip already says what changed
  // quarantined / failed: prefer the machine-readable capture reason code.
  const codes = [item.capture.a, item.capture.b]
    .filter((t) => t && t.status !== 'ok')
    .flatMap((t) => (t.reasons ?? []).map((r) => r.code))
    .filter(Boolean);
  if (codes.includes('capture-broken')) return 'capture broken';
  if (codes.includes('capture-not-assessed')) {
    // Distinguish the two shapes that land here, from the gate sentence.
    const g = item.gates[0] ?? '';
    if (/comparing a run against itself|run under review/.test(g)) return 'self-reference only';
    return 'no reference yet';
  }
  return codes[0] ?? 'not compared';
}

// The full reason for the tooltip and the expand-on-select region — the
// first gate carries BuildQueue's whole sentence; fall back to the capture
// summary.
function detailSummary(item: Item): string {
  if (item.gates.length > 0) return item.gates[0];
  const sides = [item.capture.a, item.capture.b].filter((t) => t && t.status !== 'ok');
  if (sides.length > 0) return sides[0].summary;
  return '';
}

function Row({
  item,
  selected,
  onOpen,
}: {
  item: Item;
  selected: boolean;
  onOpen: () => void;
}) {
  const strip = countsStrip(item);
  const code = reasonCode(item);
  const detail = detailSummary(item);
  const gateCount = item.gates.length;
  // One line per app/flow, and clicking it opens the detail page — the row
  // is a link, not an accordion. The full reason lives on the detail screen
  // (images / video / wire); here it is a short code plus a title= tooltip.
  return (
    <tr
      className={`queue-row${selected ? ' queue-row--selected' : ''}`}
      aria-current={selected ? 'true' : undefined}
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onOpen();
        }
      }}
    >
      <td className="queue-row__app">{item.app}</td>
      <td className="queue-row__flowname">{item.flow}</td>
      <td className="queue-row__verdict">
        <Badge tone={verdictTone(item.verdict)}>{verdictLabel(item.verdict)}</Badge>
      </td>
      <td className="queue-row__when">{formatWhen(item.when, item.runId)}</td>
      <td className="queue-row__counts">{strip}</td>
      <td className="queue-row__detail" title={detail}>
        {code}
        {code && gateCount > 0 ? (
          <span className="queue-row__gatecount"> · {gateCount} gate{gateCount === 1 ? '' : 's'}</span>
        ) : null}
      </td>
    </tr>
  );
}

type SortKey = 'default' | 'app' | 'flow' | 'verdict' | 'when';
type SortDir = 'asc' | 'desc';

// Verdict severity for sorting — worst first when descending, matching the
// server's own worst-first intent. `score` already ranks failed/quarantined
// above changed above pass, so it is the honest sort key for the verdict
// column rather than an alphabetical one (which would put "changed" before
// "pass" before "failed", meaningless to a reviewer).
function verdictRank(item: Item): number {
  return item.score;
}

function sortItemsBy(items: Item[], key: SortKey, dir: SortDir): Item[] {
  if (key === 'default') return items; // server worst-first order, untouched
  const sign = dir === 'asc' ? 1 : -1;
  const cmp = (a: Item, b: Item): number => {
    switch (key) {
      case 'app':
        return a.app.localeCompare(b.app) || a.flow.localeCompare(b.flow);
      case 'flow':
        return a.flow.localeCompare(b.flow) || a.app.localeCompare(b.app);
      case 'verdict':
        return verdictRank(a) - verdictRank(b) || keyOf(a).localeCompare(keyOf(b));
      case 'when':
        return whenMs(a.when, a.runId) - whenMs(b.when, b.runId) || keyOf(a).localeCompare(keyOf(b));
    }
  };
  return [...items].sort((a, b) => sign * cmp(a, b));
}

function QueueTable({
  items,
  selected,
  onOpen,
}: {
  items: Item[];
  selected: string | null;
  onOpen: (item: Item) => void;
}) {
  // Default = the server's worst-first order. Clicking a header sorts by that
  // column; clicking the active header again flips direction; a third click
  // returns to the default order (so a reviewer is never stuck in a sort).
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({ key: 'default', dir: 'asc' });
  const cycle = (key: SortKey) =>
    setSort((s) =>
      s.key !== key ? { key, dir: 'asc' } : s.dir === 'asc' ? { key, dir: 'desc' } : { key: 'default', dir: 'asc' },
    );
  const arrow = (key: SortKey) => (sort.key !== key ? '' : sort.dir === 'asc' ? ' ▲' : ' ▼');
  const rows = sortItemsBy(items, sort.key, sort.dir);

  const Th = ({ col, label, sortKey }: { col: string; label: string; sortKey?: SortKey }) =>
    sortKey ? (
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
    ) : (
      <th className={col}>{label}</th>
    );

  return (
    <table className="queue-table">
      <thead>
        <tr>
          <Th col="queue-table__col-app" label="app" sortKey="app" />
          <Th col="queue-table__col-flow" label="flow" sortKey="flow" />
          <Th col="queue-table__col-verdict" label="verdict" sortKey="verdict" />
          <Th col="queue-table__col-when" label="last ran" sortKey="when" />
          <Th col="queue-table__col-counts" label="what changed" />
          <Th col="queue-table__col-detail" label="details" />
        </tr>
      </thead>
      <tbody>
        {rows.map((item) => (
          <Row
            key={keyOf(item)}
            item={item}
            selected={selected === keyOf(item)}
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
  onOpen,
}: {
  items: Item[];
  empty: EmptyReason;
  selected: string | null;
  // The disclosure is CONTROLLED by the caller rather than owned here,
  // because keyboard navigation has to know which rows are on screen.
  showPassing: boolean;
  onShowPassingChange: (next: boolean) => void;
  onOpen: (item: Item) => void;
}) {
  // Retained for call-site compatibility; the queue no longer collapses
  // passing rows, so these no longer gate anything.
  void showPassing;
  void onShowPassingChange;
  const { needsAttention, passing } = partitionQueue(dedupeByKey(items));

  if (items.length === 0) {
    return <Empty reason={empty} />;
  }

  // One list, worst first: needs-attention rows on top, passing rows right
  // below in the same table — no collapse. Seeing the passing rows IS the
  // point (they prove the flow is green), so they are not hidden behind a
  // disclosure. verticalTone on each row's badge still distinguishes them.
  const rows = [...needsAttention, ...passing];

  return (
    <div className="queue">
      <QueueTable items={rows} selected={selected} onOpen={onOpen} />
    </div>
  );
}
