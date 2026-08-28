import { useState } from 'react';
import { Badge } from '../primitives';
import type { Entry, FieldDiff, Section } from '../diffTypes';
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
    <li className={`wire-field wire-field--${kind}${selected ? ' wire-field--selected' : ''}`}>
      <button type="button" className="wire-field__button" onClick={onSelect}>
        <span className="wire-field__scope">{field.scope}</span>
        <code className="wire-field__path">{field.path}</code>
        <RevealableValue value={field.a} entryK={entryK} kind={kind} field={field} side="a" onReveal={onReveal} />
        <span className="wire-field__arrow">→</span>
        <RevealableValue value={field.b} entryK={entryK} kind={kind} field={field} side="b" onReveal={onReveal} />
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
      </button>
    </li>
  );
}

function EntryRows({
  entry,
  selectedField,
  onSelectField,
  onReveal,
}: {
  entry: Entry;
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
  onReveal?: RevealFields;
}) {
  const entryK = entryKey(entry);
  const [open, setOpen] = useState(false);
  const changes = entry.bodyDiff.length + entry.bodyViolations.length + entry.headerDiff.length;

  return (
    <tbody className={`wire-row ${entry.classes.map((c) => `wire-row--${c}`).join(' ')}`}>
      <tr>
        <td>
          <button
            type="button"
            className="wire-row__toggle"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
          >
            {open ? '▾' : '▸'} {entry.method} <code>{entry.normalizedPath}</code>
          </button>
        </td>
        <td>
          {entry.statusChange ? (
            <Badge tone="red">
              {entry.statusChange.a} → {entry.statusChange.b}
            </Badge>
          ) : null}
          {entry.moved ? <Badge tone="blue">moved</Badge> : null}
          {entry.truncated ? <Badge tone="amber">truncated</Badge> : null}
        </td>
        <td className="wire-row__counts">
          {changes > 0 ? `${changes} changed` : 'identical'}
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
              <ul className="wire-fields">
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
                {entry.headerDiff.length > 0 ? (
                  <li className="wire-field wire-field--headers">
                    {entry.headerDiff.map((h) => (
                      <span key={`${h.scope}:${h.name}`} className={`wire-header wire-header--${h.type}`}>
                        <code>{h.name}</code> {h.type}
                        {h.matcher ? ` (${h.matcher})` : ''}
                      </span>
                    ))}
                  </li>
                ) : null}
              </ul>
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
}: {
  sections: Section[];
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
  onReveal?: RevealFields;
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
                {section.entries.map((entry) => (
                  <EntryRows
                    key={entryKey(entry)}
                    entry={entry}
                    selectedField={selectedField}
                    onSelectField={onSelectField}
                    onReveal={onReveal}
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
