import { useState } from 'react';
import { Badge } from '@ensemble/design-system';
import type { Entry, FieldDiff, Section } from '../api/types';
import './WireDiffTable.css';

export const entryKey = (e: Entry) => `${e.method} ${e.normalizedPath} #${e.seqB || e.seqA}`;

// The two redaction markers a captured value can carry: the literal
// "[redacted]" a mask writes, and the "$enc:v1:" envelope Phase 4b adds.
// They are MARKED, never revealed — there is deliberately no reveal control
// in this task, so the marker plus a tooltip saying why is the whole
// treatment. The two get different sentences because they state different
// facts: a mask DESTROYS the value at capture (core/trace/redact.go writes
// the literal over it before the hop reaches disk), so no later feature can
// bring it back; an envelope keeps the value and withholds the key.
const REDACTED_TITLE =
  'This value was redacted at capture. The recording does not contain it, so there is nothing here to reveal.';
const ENCRYPTED_TITLE =
  'This value was encrypted at capture. The report holds no key for it, so it cannot be shown here.';

export function isRedacted(value: unknown): boolean {
  return typeof value === 'string' && (value === '[redacted]' || value.startsWith('$enc:v1:'));
}

function Value({ value }: { value: unknown }) {
  const text = value === undefined ? '—' : typeof value === 'string' ? value : JSON.stringify(value);
  if (isRedacted(value)) {
    return (
      <code
        className="wire-value redacted"
        title={typeof value === 'string' && value.startsWith('$enc:v1:') ? ENCRYPTED_TITLE : REDACTED_TITLE}
      >
        {text}
      </code>
    );
  }
  return <code className="wire-value">{text}</code>;
}

function FieldRow({
  field,
  kind,
  selected,
  onSelect,
}: {
  field: FieldDiff;
  kind: 'changed' | 'violation' | 'tolerated' | 'ordering' | 'ignored';
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <li className={`wire-field wire-field--${kind}${selected ? ' wire-field--selected' : ''}`}>
      <button type="button" className="wire-field__button" onClick={onSelect}>
        <span className="wire-field__scope">{field.scope}</span>
        <code className="wire-field__path">{field.path}</code>
        <Value value={field.a} />
        <span className="wire-field__arrow">→</span>
        <Value value={field.b} />
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
}: {
  entry: Entry;
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
}) {
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
                    field={f}
                    kind="violation"
                    selected={selectedField === `${entryKey(entry)}|${f.scope}:${f.path}`}
                    onSelect={() => onSelectField(entry, f)}
                  />
                ))}
                {entry.bodyDiff.map((f) => (
                  <FieldRow
                    key={`d:${f.scope}:${f.path}`}
                    field={f}
                    kind="changed"
                    selected={selectedField === `${entryKey(entry)}|${f.scope}:${f.path}`}
                    onSelect={() => onSelectField(entry, f)}
                  />
                ))}
                {entry.bodyTolerated.map((f) => (
                  <FieldRow
                    key={`t:${f.scope}:${f.path}`}
                    field={f}
                    kind="tolerated"
                    selected={selectedField === `${entryKey(entry)}|${f.scope}:${f.path}`}
                    onSelect={() => onSelectField(entry, f)}
                  />
                ))}
                {entry.orderingChanges.map((f) => (
                  <FieldRow
                    key={`o:${f.scope}:${f.path}`}
                    field={f}
                    kind="ordering"
                    selected={selectedField === `${entryKey(entry)}|${f.scope}:${f.path}`}
                    onSelect={() => onSelectField(entry, f)}
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
}: {
  sections: Section[];
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
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
