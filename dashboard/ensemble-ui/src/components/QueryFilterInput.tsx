// The Traffic search box: a combobox that turns a `field:value`/
// `field<op>value` word into a removable pill on Tab or Space, offers
// field-name completion while typing (Tab-accept), and value suggestions
// pulled from what's actually in view once a colon field is complete
// (click-accept only — see below). Plain words with no recognized field
// stay as free text in the box, exactly like before this existed; see
// ../trafficFilter for the grammar and matching semantics.
import { useMemo, type KeyboardEvent } from 'react';
import type { Hop } from '../api/types';
import {
  COLON_FIELDS,
  fieldSuggestions,
  formatFilterToken,
  isComparisonOnlyField,
  parseFilterToken,
  valueSuggestions,
  type FilterField,
  type FilterToken,
} from '../trafficFilter';
import './QueryFilterInput.css';

export interface QueryFilterInputProps {
  pills: FilterToken[];
  onPillsChange: (pills: FilterToken[]) => void;
  draft: string;
  onDraftChange: (draft: string) => void;
  /** Hops currently in view — the source for value suggestions (distinct
   * status codes/methods/sessions actually present), same idea as the
   * session dropdown only ever listing sessions that exist. */
  hops: Hop[];
  placeholder?: string;
}

type Suggestions =
  | { kind: 'fields'; fields: FilterField[] }
  | { kind: 'values'; field: FilterField; values: string[] }
  | null;

/** Suggestions apply to the whole draft, not just its last word — once the
 * draft contains a space it's already committed to being free text (a
 * completed token would have been pulled out into a pill on that space
 * keystroke), so autocomplete stops offering anything. */
function computeSuggestions(draft: string, hops: Hop[]): Suggestions {
  if (draft.includes(' ')) return null;
  const trimmed = draft.trim();
  if (!trimmed) return null;

  const colonPrefix = /^([a-zA-Z]+):$/.exec(trimmed);
  if (colonPrefix) {
    const field = colonPrefix[1].toLowerCase();
    if ((COLON_FIELDS as readonly string[]).includes(field)) {
      const values = valueSuggestions(field as FilterField, hops);
      return values.length ? { kind: 'values', field: field as FilterField, values } : null;
    }
    return null;
  }

  if (/[:><]/.test(trimmed)) return null; // already past field-name typing
  const fields = fieldSuggestions(trimmed);
  return fields.length ? { kind: 'fields', fields } : null;
}

export default function QueryFilterInput({
  pills,
  onPillsChange,
  draft,
  onDraftChange,
  hops,
  placeholder,
}: QueryFilterInputProps) {
  const suggestions = useMemo(() => computeSuggestions(draft, hops), [draft, hops]);

  const applyFieldCompletion = (field: FilterField) => {
    onDraftChange(isComparisonOnlyField(field) ? field : `${field}:`);
  };

  const commitToken = (token: FilterToken) => {
    onPillsChange([...pills, token]);
    onDraftChange('');
  };

  const removePill = (index: number) => {
    onPillsChange(pills.filter((_, i) => i !== index));
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Tab' && suggestions?.kind === 'fields') {
      e.preventDefault();
      applyFieldCompletion(suggestions.fields[0]);
      return;
    }
    if (e.key === 'Tab' || e.key === ' ') {
      const token = parseFilterToken(draft);
      if (token) {
        e.preventDefault();
        commitToken(token);
        return;
      }
    }
    if (e.key === 'Backspace' && draft === '' && pills.length > 0) {
      removePill(pills.length - 1);
    }
  };

  return (
    <div className="query-filter">
      {pills.map((p, i) => (
        <span key={`${formatFilterToken(p)}-${i}`} className="query-filter__pill">
          {formatFilterToken(p)}
          <button
            type="button"
            className="query-filter__pill-remove"
            aria-label={`remove filter ${formatFilterToken(p)}`}
            onClick={() => removePill(i)}
          >
            ×
          </button>
        </span>
      ))}
      <input
        type="text"
        className="query-filter__input"
        placeholder={pills.length === 0 ? placeholder : ''}
        value={draft}
        onChange={(e) => onDraftChange(e.target.value)}
        onKeyDown={handleKeyDown}
      />
      {suggestions && (
        <ul className="query-filter__suggestions">
          {suggestions.kind === 'fields'
            ? suggestions.fields.map((f) => (
                <li key={f}>
                  <button
                    type="button"
                    // mousedown, not click: fires before the input blurs, so
                    // focus never leaves the box mid-selection.
                    onMouseDown={(e) => {
                      e.preventDefault();
                      applyFieldCompletion(f);
                    }}
                  >
                    {f}
                    {isComparisonOnlyField(f) ? '' : ':'}
                  </button>
                </li>
              ))
            : suggestions.values.map((v) => (
                <li key={v}>
                  <button
                    type="button"
                    onMouseDown={(e) => {
                      e.preventDefault();
                      commitToken({ field: suggestions.field, op: ':', value: v });
                    }}
                  >
                    {suggestions.field}:{v}
                  </button>
                </li>
              ))}
        </ul>
      )}
    </div>
  );
}
