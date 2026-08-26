// The Traffic search box's query grammar: `field:value` for exact/substring/
// bucket match (status:200, status:4xx, method:GET, path:/v1, session:a1b2),
// `field<op>value` with no colon for numeric comparisons (size>10kb,
// done<200ms) since equality on a byte-precise size or millisecond duration
// is rarely what anyone wants. Anything that doesn't parse as either shape
// is left as plain free text — a substring match against method+path+route,
// same as the box did before this grammar existed.
import type { Hop } from './api/types';

export const COLON_FIELDS = ['status', 'method', 'path', 'session'] as const;
export const COMPARISON_FIELDS = ['size', 'done'] as const;
export const FILTER_FIELDS = [...COLON_FIELDS, ...COMPARISON_FIELDS] as const;
export type FilterField = (typeof FILTER_FIELDS)[number];

export type ComparisonOp = '>' | '<' | '>=' | '<=';
export type FilterOp = ':' | ComparisonOp;

export interface FilterToken {
  field: FilterField;
  op: FilterOp;
  /** The value text as typed, unparsed — reused verbatim for a pill's
   * label and re-parsed per-field by matchesToken. */
  value: string;
}

// Longer operators first so `>=` isn't matched as `>` followed by a
// literal `=`.
const COMPARISON_RE = /^(size|done)(>=|<=|>|<)(.+)$/i;
const COLON_RE = /^(status|method|path|session):(.+)$/i;

/** Parses one whitespace-delimited word into a FilterToken, or null if it
 * doesn't match a known field's grammar — the caller's cue to treat it as
 * free text instead. */
export function parseFilterToken(word: string): FilterToken | null {
  const trimmed = word.trim();
  if (!trimmed) return null;

  const cmp = COMPARISON_RE.exec(trimmed);
  if (cmp) {
    const [, field, op, value] = cmp;
    return { field: field.toLowerCase() as FilterField, op: op as ComparisonOp, value };
  }

  const colon = COLON_RE.exec(trimmed);
  if (colon) {
    const [, field, value] = colon;
    return { field: field.toLowerCase() as FilterField, op: ':', value };
  }

  return null;
}

/** Renders a token back to the query text it came from — used for a
 * pill's label. */
export function formatFilterToken(token: FilterToken): string {
  return `${token.field}${token.op}${token.value}`;
}

const textEncoder = new TextEncoder();
function byteLength(body?: string): number {
  return body ? textEncoder.encode(body).length : 0;
}

/** Combined request+response payload size in bytes — the same figure
 * HopTable's size column displays, factored out here so the filter and
 * the column can never drift apart on what "size" means. */
export function hopPayloadBytes(hop: Hop): number {
  return byteLength(hop.req?.body) + byteLength(hop.resp?.body);
}

/** hop.status bucketed the same way the status column colors/icons it. */
export function statusBucket(status?: number): '2xx' | '3xx' | '4xx' | '5xx' | '' {
  if (status === undefined) return '';
  if (status >= 500) return '5xx';
  if (status >= 400) return '4xx';
  if (status >= 300) return '3xx';
  if (status >= 200) return '2xx';
  return '';
}

/** Parses a size value: a bare number is bytes, otherwise a b/kb/mb suffix
 * (case-insensitive). Null on anything else — an unparseable comparison
 * value never matches, it doesn't throw. */
function parseSizeValue(raw: string): number | null {
  const m = /^([\d.]+)\s*(b|kb|mb)?$/i.exec(raw.trim());
  if (!m) return null;
  const n = Number(m[1]);
  if (Number.isNaN(n)) return null;
  switch ((m[2] ?? 'b').toLowerCase()) {
    case 'mb':
      return n * 1024 * 1024;
    case 'kb':
      return n * 1024;
    default:
      return n;
  }
}

/** Parses a done/duration value: a bare number or an `ms` suffix is
 * milliseconds, an `s` suffix is seconds. */
function parseDoneValue(raw: string): number | null {
  const m = /^([\d.]+)\s*(ms|s)?$/i.exec(raw.trim());
  if (!m) return null;
  const n = Number(m[1]);
  if (Number.isNaN(n)) return null;
  return (m[2] ?? 'ms').toLowerCase() === 's' ? n * 1000 : n;
}

function compare(actual: number, op: ComparisonOp, target: number): boolean {
  switch (op) {
    case '>':
      return actual > target;
    case '<':
      return actual < target;
    case '>=':
      return actual >= target;
    case '<=':
      return actual <= target;
  }
}

/** Whether hop satisfies one token. Colon tokens on `status` accept either
 * an exact code ("200") or a bucket ("4xx"); every other colon field is a
 * case-insensitive substring/prefix match. Comparison tokens with an
 * unparseable value, or a `done` comparison against a hop with no
 * doneMs yet, never match. */
export function matchesToken(hop: Hop, token: FilterToken): boolean {
  switch (token.field) {
    case 'status': {
      if (token.op === ':') {
        const v = token.value.toLowerCase();
        if (/^\dxx$/.test(v)) return statusBucket(hop.status) === v;
        return String(hop.status ?? '') === v;
      }
      const target = Number(token.value);
      if (Number.isNaN(target) || hop.status === undefined) return false;
      return compare(hop.status, token.op, target);
    }
    case 'method':
      return (hop.method ?? '').toLowerCase() === token.value.toLowerCase();
    case 'path':
      return (hop.path ?? '').toLowerCase().includes(token.value.toLowerCase());
    case 'session':
      return (hop.session ?? '').toLowerCase().startsWith(token.value.toLowerCase());
    case 'size': {
      if (token.op === ':') return false;
      const target = parseSizeValue(token.value);
      if (target === null) return false;
      return compare(hopPayloadBytes(hop), token.op, target);
    }
    case 'done': {
      if (token.op === ':' || hop.t.doneMs === undefined) return false;
      const target = parseDoneValue(token.value);
      if (target === null) return false;
      return compare(hop.t.doneMs, token.op, target);
    }
  }
}

/** Whether hop passes the whole query: tokens combine as AND across
 * distinct fields, OR within repeats of the same field (status:404
 * status:500 means "either"), and freeText — whatever in the box didn't
 * parse as a token — is ANDed in as a case-insensitive substring match
 * against method, path, and route (to/from), matching the box's
 * pre-grammar behavior. */
export function hopMatchesQuery(hop: Hop, tokens: FilterToken[], freeText: string): boolean {
  const byField = new Map<FilterField, FilterToken[]>();
  for (const t of tokens) {
    const group = byField.get(t.field);
    if (group) group.push(t);
    else byField.set(t.field, [t]);
  }
  for (const group of byField.values()) {
    if (!group.some((t) => matchesToken(hop, t))) return false;
  }

  const needle = freeText.trim().toLowerCase();
  if (needle) {
    const haystack = `${hop.method ?? ''} ${hop.path ?? ''} ${hop.from ?? ''} ${hop.to}`.toLowerCase();
    if (!haystack.includes(needle)) return false;
  }
  return true;
}

/** Field-name suggestions for the box's autocomplete dropdown, filtered by
 * whatever's typed so far. Includes an exact match — typing the full word
 * "status" still needs to suggest "status" itself so Tab can append the
 * trailing `:` (comparison-only fields size/done complete to the bare
 * name; the user types their own operator next). */
export function fieldSuggestions(prefix: string): FilterField[] {
  const p = prefix.trim().toLowerCase();
  if (!p) return [];
  return FILTER_FIELDS.filter((f) => f.startsWith(p));
}

export function isComparisonOnlyField(field: FilterField): boolean {
  return (COMPARISON_FIELDS as readonly string[]).includes(field);
}

/** Value suggestions once a colon field's `field:` prefix is complete —
 * pulled from what's actually present in `hops` right now (same idea as
 * the session dropdown: only ever offer values that exist), capped and
 * first-seen ordered so the list stays short and stable while streaming. */
export function valueSuggestions(field: FilterField, hops: Hop[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  const take = (v: string | undefined) => {
    if (v && !seen.has(v)) {
      seen.add(v);
      out.push(v);
    }
  };
  for (const h of hops) {
    if (out.length >= 8) break;
    if (field === 'status') take(h.status !== undefined ? String(h.status) : undefined);
    else if (field === 'method') take(h.method);
    else if (field === 'session') take(h.session);
  }
  return out;
}
