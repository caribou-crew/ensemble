import { useState } from 'react';
import { Badge } from '../primitives';
import type { Entry, FieldDiff, HeaderDiff, Section, StatusFinding } from '../diffTypes';
import './WireDiffTable.css';

export const entryKey = (e: Entry) => `${e.method} ${e.normalizedPath} #${e.seqB || e.seqA}`;

// The two redaction markers a captured value can carry: the literal
// "[redacted]" a mask writes, and the "$enc:v1:" envelope an `encrypt` rule
// adds. They get different treatments because they state different facts: a
// mask DESTROYS the value at capture (core/trace/redact.go writes the
// literal over it before the hop reaches disk), so nothing — not even the
// team key — can bring it back; an envelope keeps the value and withholds
// the key, so revealing it is a matter of asking the server whether the key
// resolves now.
const REDACTED_TITLE =
  'This value was redacted at capture. The recording does not contain it, so there is nothing here to reveal.';
const ENCRYPTED_TITLE =
  'This value was encrypted at capture. The report holds no key for it, so it cannot be shown here.';

export function isRedacted(value: unknown): boolean {
  return typeof value === 'string' && (value === '[redacted]' || value.startsWith('$enc:v1:'));
}

function isEncrypted(value: unknown): boolean {
  return typeof value === 'string' && value.startsWith('$enc:v1:');
}

const renderValue = (value: unknown) =>
  value === undefined ? '—' : typeof value === 'string' ? value : JSON.stringify(value);

export type FieldKind = 'changed' | 'violation' | 'tolerated' | 'ordering' | 'ignored';

function fieldListFor(entry: Entry, kind: FieldKind): FieldDiff[] {
  switch (kind) {
    case 'violation':
      return entry.bodyViolations;
    case 'changed':
      return entry.bodyDiff;
    case 'tolerated':
      return entry.bodyTolerated;
    case 'ordering':
      return entry.orderingChanges;
    case 'ignored':
      return entry.bodyIgnored;
  }
}

/**
 * Locates the SAME field (by entry identity + which list it lives in +
 * scope/path) inside a freshly-fetched set of sections — reveal-on-click
 * re-fetches the whole item rather than trusting the already-loaded
 * payload (design.md D6), so a click has to re-find its own field in the
 * new response rather than reuse anything from the render that triggered it.
 */
export function findRevealedField(
  sections: Section[],
  entryK: string,
  kind: FieldKind,
  scope: string,
  path: string,
): FieldDiff | undefined {
  for (const section of sections) {
    for (const entry of section.entries) {
      if (entryKey(entry) !== entryK) continue;
      return fieldListFor(entry, kind).find((f) => f.scope === scope && f.path === path);
    }
  }
  return undefined;
}

/** Re-fetches the sections for the entry/field currently being revealed.
 * Supplied by the app (ensemble-ui, retrace-ui) so this component never
 * needs to know an API base URL — the same seam ShotCompare's
 * `resolveShotUrl` prop uses, adapted to a fetch instead of a URL because a
 * reveal has to read a value out of the response, not just point at it. */
export type RevealFields = () => Promise<Section[]>;

type RevealState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'revealed'; value: unknown }
  | { status: 'unavailable' };

function RevealableValue({
  value,
  entryK,
  kind,
  field,
  side,
  onReveal,
}: {
  value: unknown;
  entryK: string;
  kind: FieldKind;
  field: FieldDiff;
  side: 'a' | 'b';
  onReveal?: RevealFields;
}) {
  const [state, setState] = useState<RevealState>({ status: 'idle' });

  if (state.status === 'revealed') {
    return <code className="wire-value">{renderValue(state.value)}</code>;
  }
  if (!isRedacted(value)) {
    return <code className="wire-value">{renderValue(value)}</code>;
  }

  const encrypted = isEncrypted(value);
  const reveal = async (e: React.MouseEvent | React.KeyboardEvent) => {
    // This value sits inside FieldRow's row-selection button — stop the
    // click short of it, or revealing a field would also select it.
    e.stopPropagation();
    if (!onReveal) return;
    setState({ status: 'loading' });
    try {
      const fresh = findRevealedField(await onReveal(), entryK, kind, field.scope, field.path);
      const freshValue = fresh ? fresh[side] : undefined;
      if (isEncrypted(freshValue)) {
        setState({ status: 'unavailable' });
      } else {
        setState({ status: 'revealed', value: freshValue });
      }
    } catch {
      setState({ status: 'unavailable' });
    }
  };

  return (
    <span className="wire-value-masked">
      <code className="wire-value redacted" title={encrypted ? ENCRYPTED_TITLE : REDACTED_TITLE}>
        {renderValue(value)}
      </code>
      {encrypted && onReveal ? (
        // A <span role="button">, not a real <button> — this already sits
        // inside FieldRow's row-selection button, and a nested <button> is
        // invalid HTML the browser is free to restructure.
        <span
          role="button"
          tabIndex={0}
          className="wire-value__reveal"
          aria-disabled={state.status === 'loading'}
          onClick={(e) => {
            if (state.status !== 'loading') void reveal(e);
          }}
          onKeyDown={(e) => {
            if ((e.key === 'Enter' || e.key === ' ') && state.status !== 'loading') {
              e.preventDefault();
              void reveal(e);
            }
          }}
        >
          {state.status === 'loading'
            ? 'revealing…'
            : state.status === 'unavailable'
              ? 'key not available'
              : 'reveal'}
        </span>
      ) : null}
    </span>
  );
}

// Each field is a ROW with two literal value columns — reference (a) on the
// left, candidate (b) on the right — the split-view a github.com side-by-side
// diff uses, rather than one line reading "path: a → b". A <tr> that is
// itself the click target (role="button", not a nested <button>) is what
// lets each of the three cells stay a real table cell; RevealableValue's
// reveal control still stops its own click short of this row exactly as it
// did with the old <button>-wrapped version.
function FieldRow({
  entryK,
  field,
  kind,
  selected,
  onSelect,
  onReveal,
}: {
  entryK: string;
  field: FieldDiff;
  kind: FieldKind;
  selected: boolean;
  onSelect: () => void;
  onReveal?: RevealFields;
}) {
  return (
    <tr
      className={`wire-field wire-field--${kind}${selected ? ' wire-field--selected' : ''}`}
      role="button"
      tabIndex={0}
      onClick={onSelect}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onSelect();
        }
      }}
    >
      <td className="wire-field__label">
        <span className="wire-field__scope">{field.scope}</span>
        <code className="wire-field__path">{field.path}</code>
        {/* A tolerated field is NOT a change: it is a field a rule already
            says may vary, and showing it with the matcher that tolerated it
            is what tells a reviewer why it is not counted. */}
        {field.matcher ? (
          <span className="wire-field__matcher">
            tolerated by <code>{field.matcher}</code>
            {field.glob ? (
              <>
                {' '}
                on <code>{field.glob}</code>
              </>
            ) : null}
          </span>
        ) : null}
      </td>
      <td className="wire-field__side wire-field__side--a">
        <RevealableValue value={field.a} entryK={entryK} kind={kind} field={field} side="a" onReveal={onReveal} />
      </td>
      <td className="wire-field__side wire-field__side--b">
        <RevealableValue value={field.b} entryK={entryK} kind={kind} field={field} side="b" onReveal={onReveal} />
      </td>
    </tr>
  );
}

// A header's a/b live directly on it rather than in a separate list per
// list-kind the way body fields do, so it renders through the same row shape
// with its own `type` standing in for FieldKind — a header carries no
// redaction marker, so its values render as plain code rather than through
// RevealableValue.
function HeaderRow({ h }: { h: HeaderDiff }) {
  return (
    <tr className={`wire-field wire-field--${h.type}`}>
      <td className="wire-field__label">
        <span className="wire-field__scope">{h.scope}</span>
        <code className="wire-field__path">{h.name}</code>
        {h.matcher ? (
          <span className="wire-field__matcher">
            tolerated by <code>{h.matcher}</code>
          </span>
        ) : null}
      </td>
      <td className="wire-field__side wire-field__side--a">
        <code className="wire-value">{h.a ?? '—'}</code>
      </td>
      <td className="wire-field__side wire-field__side--b">
        <code className="wire-value">{h.b ?? '—'}</code>
      </td>
    </tr>
  );
}

function EntryRows({
  entry,
  selectedField,
  onSelectField,
  onReveal,
  unexpectedStatus,
}: {
  entry: Entry;
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
  onReveal?: RevealFields;
  unexpectedStatus?: StatusFinding;
}) {
  const entryK = entryKey(entry);
  const [open, setOpen] = useState(false);
  const changes = entry.bodyDiff.length + entry.bodyViolations.length + entry.headerDiff.length;

  return (
    <tbody className={`wire-row ${entry.classes.map((c) => `wire-row--${c}`).join(' ')}`}>
      <tr>
        <td className="wire-row__call">
          {/* Reference and candidate positions sit SIDE BY SIDE, not folded
              into one shared number — a moved entry's whole story is that
              its rank in the candidate sequence differs from its rank in
              the reference sequence, and #posA next to #posB is what makes
              that visible on the row itself, before it's even opened. */}
          <button
            type="button"
            className="wire-row__toggle"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
          >
            <span className="wire-row__caret">{open ? '▾' : '▸'}</span>
            <span className="wire-row__pane wire-row__pane--a">
              <span className="wire-row__pos">#{entry.posA + 1}</span> {entry.method}{' '}
              <code>{entry.normalizedPath}</code>
            </span>
            <span className="wire-row__pane wire-row__pane--b">
              <span className="wire-row__pos">#{entry.posB + 1}</span> {entry.method}{' '}
              <code>{entry.normalizedPath}</code>
            </span>
          </button>
        </td>
        <td>
          {entry.statusChange ? (
            <Badge tone="red">
              {entry.statusChange.a} → {entry.statusChange.b}
            </Badge>
          ) : null}
          {/* A status the diff flagged as unexpected (e.g. 502) — badged even
              when both sides carry it, so statusChange is absent (identical)
              yet the call is not fine. Without this the 5xx shows only in a
              detached gate line and the row reads as "identical". */}
          {unexpectedStatus ? (
            <Badge tone="red">status {unexpectedStatus.status}</Badge>
          ) : null}
          {/* A bare "moved" badge names the fact but not the story: a
              reviewer scanning the candidate column for where a call ended
              up has to go find its row. The concrete posA→posB mapping is
              that story on the row itself. */}
          {entry.moved ? (
            <Badge tone="amber">
              moved {entry.posA + 1} → {entry.posB + 1}
            </Badge>
          ) : null}
          {entry.truncated ? <Badge tone="amber">truncated</Badge> : null}
        </td>
        <td className="wire-row__counts">
          {changes > 0 ? `${changes} changed` : unexpectedStatus ? `identical shape · status ${unexpectedStatus.status}` : 'identical'}
          {entry.bodyTolerated.length > 0 ? ` · ${entry.bodyTolerated.length} tolerated` : ''}
        </td>
      </tr>
      {open ? (
        <tr className="wire-row__detail">
          <td colSpan={3}>
            {/* A truncated entry was size-capped at capture, so its body was
                never field diffed. Rendering an empty diff for it would say
                "nothing differed" about a comparison that did not happen. */}
            {entry.truncated ? (
              <p className="wire-row__explanation">
                body was size-capped at capture — not field diffed
              </p>
            ) : (
              <table className="wire-fields">
                <thead>
                  <tr>
                    <th className="wire-fields__label-col" />
                    <th>reference</th>
                    <th>candidate</th>
                  </tr>
                </thead>
                <tbody>
                {entry.bodyViolations.map((f) => (
                  <FieldRow
                    key={`v:${f.scope}:${f.path}`}
                    entryK={entryK}
                    field={f}
                    kind="violation"
                    selected={selectedField === `${entryK}|${f.scope}:${f.path}`}
                    onSelect={() => onSelectField(entry, f)}
                    onReveal={onReveal}
                  />
                ))}
                {entry.bodyDiff.map((f) => (
                  <FieldRow
                    key={`d:${f.scope}:${f.path}`}
                    entryK={entryK}
                    field={f}
                    kind="changed"
                    selected={selectedField === `${entryK}|${f.scope}:${f.path}`}
                    onSelect={() => onSelectField(entry, f)}
                    onReveal={onReveal}
                  />
                ))}
                {entry.bodyTolerated.map((f) => (
                  <FieldRow
                    key={`t:${f.scope}:${f.path}`}
                    entryK={entryK}
                    field={f}
                    kind="tolerated"
                    selected={selectedField === `${entryK}|${f.scope}:${f.path}`}
                    onSelect={() => onSelectField(entry, f)}
                    onReveal={onReveal}
                  />
                ))}
                {entry.orderingChanges.map((f) => (
                  <FieldRow
                    key={`o:${f.scope}:${f.path}`}
                    entryK={entryK}
                    field={f}
                    kind="ordering"
                    selected={selectedField === `${entryK}|${f.scope}:${f.path}`}
                    onSelect={() => onSelectField(entry, f)}
                    onReveal={onReveal}
                  />
                ))}
                {entry.headerDiff.map((h) => (
                  <HeaderRow key={`h:${h.scope}:${h.name}`} h={h} />
                ))}
                </tbody>
              </table>
            )}
          </td>
        </tr>
      ) : null}
    </tbody>
  );
}

/**
 * The wire plane, one collapsible section per FLOW PART.
 *
 * The section names are `summary.sections[].name` verbatim — the UI end of
 * the marker → group → section chain. A section whose name is the EMPTY
 * STRING is the traffic that happened before any marker was placed, and it
 * says so rather than being dropped or silently merged into the first named
 * part.
 *
 * `""` and not `null`: diff.Section.Name is a Go `string` with a bare tag and
 * BuildSections constructs the unnamed section as `buildSection("", …)`, so
 * null never arrives. It matters far beyond the leading-traffic case — a run
 * that declared no group markers at all gets ONE section named `""`, so
 * `?? 'before any marker'` (which `''` sails straight through) put every
 * marker-less flow's entire wire plane under a blank header.
 */
export default function WireDiffTable({
  sections,
  selectedField,
  onSelectField,
  onReveal,
  unexpectedStatuses = [],
}: {
  sections: Section[];
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
  onReveal?: RevealFields;
  // Unexpected HTTP statuses (e.g. a 502) the diff flagged. Threaded in so a
  // bad status is badged ON its wire row — a call that 5xx'd on BOTH sides
  // diffs as `identical` (no statusChange), so without this the only sign of
  // the 502 is a disconnected gate line and the row reads as fine.
  unexpectedStatuses?: StatusFinding[];
}) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  if (sections.length === 0) {
    return <p className="wire-empty">No wire calls were paired for this flow.</p>;
  }

  return (
    <div className="wire-diff">
      {sections.map((section, i) => {
        const name = section.name === '' ? 'before any marker' : section.name;
        const id = `${i}:${name}`;
        const isCollapsed = collapsed[id] === true;
        return (
          <section className="wire-section" key={id}>
            <button
              type="button"
              className="wire-section__header"
              aria-expanded={!isCollapsed}
              onClick={() => setCollapsed((c) => ({ ...c, [id]: !isCollapsed }))}
            >
              {isCollapsed ? '▸' : '▾'} <span className="wire-section__name">{name}</span>
              <span className="wire-section__counts">
                {Object.entries(section.counts)
                  .filter(([, n]) => n > 0)
                  .map(([k, n]) => `${n} ${k}`)
                  .join(' · ')}
              </span>
            </button>
            {!isCollapsed ? (
              <table className="wire-table">
                <thead>
                  <tr className="wire-table__pane-header">
                    <th>
                      <span className="wire-row__pane wire-row__pane--a">reference</span>
                      <span className="wire-row__pane wire-row__pane--b">candidate</span>
                    </th>
                    <th />
                    <th />
                  </tr>
                </thead>
                {/* Rows render in REFERENCE fire order (posA), not the
                    server-bucket-then-align order they arrive in. A repeated
                    endpoint's hops otherwise clump together by bucket, so
                    the reference column reads out of sequence (e.g.
                    1,2,5,3,4,6) and a reviewer can't tell at a glance what
                    the reference run actually did. posA is already a dense,
                    gap-free rank, so sorting by it alone puts the reference
                    column in top-to-bottom order; the candidate column keeps
                    its own posB, so a moved entry visibly falls out of step. */}
                {[...section.entries]
                  .sort((a, b) => a.posA - b.posA)
                  .map((entry) => (
                    <EntryRows
                      key={entryKey(entry)}
                      entry={entry}
                      selectedField={selectedField}
                      onSelectField={onSelectField}
                      onReveal={onReveal}
                      unexpectedStatus={unexpectedStatuses.find(
                        (u) => u.seq === entry.seqB || u.seq === entry.seqA,
                      )}
                    />
                  ))}
              </table>
            ) : null}
          </section>
        );
      })}
    </div>
  );
}
