// Small generic-rendering helpers shared by the two views that render
// unknown-shaped JSON as table cells (InspectorView's DB rows, EntityView's
// entity list) — deliberately NOT per-domain: both views only know a
// column name and a JS value, never a schema they can special-case.

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
