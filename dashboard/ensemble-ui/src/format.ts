// Small generic-rendering helpers shared by the two views that render
// unknown-shaped JSON as table cells (InspectorView's DB rows, EntityView's
// entity list) — deliberately NOT per-domain: both views only know a
// column name and a JS value, never a schema they can special-case.
import type { EntityLink } from './api/types';

/** Renders one arbitrary JSON value as compact cell text: nested
 * objects/arrays collapse to their JSON form, nullish reads as an em dash,
 * everything else stringifies plainly. */
export function renderCellValue(value: unknown): string {
  if (value === null || value === undefined) return '—';
  if (typeof value === 'object') return JSON.stringify(value);
  return String(value);
}

/** Resolves an EntityLink's raw template against one row's fields: every
 * `{{column}}` placeholder becomes that row's value for `column` (nullish
 * or missing resolves to empty string). No templating engine, no automatic
 * encoding — a template embedding a URL inside another query param must be
 * hand percent-encoded by whoever writes the config. */
export function resolveLinkTemplate(template: string, row: Record<string, unknown>): string {
  return template.replace(/\{\{(\w+)\}\}/g, (_match, column: string) => {
    const value = row[column];
    return value === null || value === undefined ? '' : String(value);
  });
}

/** Wraps a string in POSIX single-quote shell escaping, so it survives a
 * copy-paste into the developer's own shell as exactly one argument
 * regardless of spaces/`&`/`?`/etc inside it. An embedded `'` is escaped
 * with the standard `'\''` idiom (close the quote, an escaped literal
 * quote, reopen) rather than rejected — there is no adversarial input path
 * here (the command is built for, and reviewed by, the person about to
 * paste it), so escaping correctly beats banning a legal URL character. */
export function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

const CONTROL_CHAR_RE = /[\x00-\x1f\x7f]/;
const TEMPLATE_PLACEHOLDER_RE = /\{\{(\w+)\}\}/g;

/** Finds the first {{column}} placeholder in template whose row value is
 * missing, nullish, or empty — the case resolveLinkTemplate can't
 * distinguish from "resolved fine", since it substitutes an empty string
 * either way. For a `kind: exec` link a silently-empty substitution
 * produces a command that *looks* right and targets the wrong thing, which
 * is worse than the visibly-broken URL a `kind: url` link would render —
 * so exec links disable instead. */
function firstMissingColumn(template: string, row: Record<string, unknown>): string | null {
  TEMPLATE_PLACEHOLDER_RE.lastIndex = 0;
  let match: RegExpExecArray | null;
  while ((match = TEMPLATE_PLACEHOLDER_RE.exec(template)) !== null) {
    const value = row[match[1]];
    if (value === null || value === undefined || String(value) === '') {
      return match[1];
    }
  }
  return null;
}

/** The result of resolving a `kind: exec` link against one row: either the
 * full command string ready to copy, or a reason the button must render
 * disabled instead — a resolved command is never handed back partially
 * built. */
export type ExecCommandResult = { command: string } | { disabledReason: string };

/** Builds the local CLI command a `kind: exec` link's button copies to the
 * clipboard: resolves the template against the row, single-quotes the
 * result into the link's argv template's one `{{url}}` slot (see
 * shellQuote), and joins argv into one string. Refuses to produce a
 * command — disabled reason instead — if a referenced column is
 * missing/empty, or if the fully assembled command contains an ASCII
 * control character. The control-character check runs on the *resolved
 * command*, not just the raw row value, so it also catches anything a
 * future argv template might introduce, not only what came from the row.
 *
 * A newline in a row value is the concrete risk this guards: it would
 * turn one copied "command" into two lines, and in a terminal without
 * bracketed paste enabled, the second line runs on paste before anyone
 * has read or approved it. This is the one check in this function that
 * does real security work — everything else here is correctness (a
 * command that fails obviously beats one that looks right and isn't). */
export function buildExecCommand(link: EntityLink, row: Record<string, unknown>): ExecCommandResult {
  const argv = link.argv ?? [];
  if (!argv.includes('{{url}}')) {
    // config.Validate guarantees every table entry has exactly one
    // {{url}} sentinel — reachable only if the server ever serves a
    // malformed argv, which would itself be a bug worth surfacing loudly
    // rather than silently copying an incomplete command.
    return { disabledReason: 'exec link is misconfigured (no {{url}} in its command)' };
  }

  const missing = firstMissingColumn(link.template, row);
  if (missing) {
    return { disabledReason: `row is missing "${missing}"` };
  }

  const resolved = resolveLinkTemplate(link.template, row);
  const command = argv.map((arg) => (arg === '{{url}}' ? shellQuote(resolved) : arg)).join(' ');

  if (CONTROL_CHAR_RE.test(command)) {
    return { disabledReason: 'row value contains a control character' };
  }

  return { command };
}

/** The union of keys across a set of row objects, in first-seen order —
 * the generic stand-in for "table columns" when there's no schema to read
 * columns off of (EntityView's passthrough rows). */
export function unionKeys(rows: Record<string, unknown>[]): string[] {
  const seen = new Set<string>();
  const keys: string[] = [];
  for (const row of rows) {
    for (const k of Object.keys(row)) {
      if (!seen.has(k)) {
        seen.add(k);
        keys.push(k);
      }
    }
  }
  return keys;
}
