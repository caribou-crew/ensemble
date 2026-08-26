// Shared redaction-rendering logic for HopDetail/JsonView. core/trace's
// Redactor (core/trace/redact.go) overwrites a matching header with the
// literal "[redacted]" and a matching JSON body FIELD with the same
// literal, in place — so a body value shows up as a substring inside a
// larger pretty-printed blob, not as the whole string. `$enc:v1:`-prefixed
// values are a forward-looking form (task 4.8's reveal-eyeball encrypts
// rather than drops the value) — recognized here so this rendering doesn't
// need to change when that lands, even though nothing produces it yet.
//
// Reveal/decrypt is deliberately NOT implemented here — task 4.8's job.

export interface RedactionSegment {
  text: string;
  redacted: boolean;
}

// A quoted JSON string literal is tried first so JSON.stringify's
// surrounding quotes land inside the highlighted span rather than
// dangling outside it; the bare forms cover a plain (non-JSON) header
// value or raw body text.
const REDACTED_RE = /"\[redacted\]"|\[redacted\]|"\$enc:v1:[^"]*"|\$enc:v1:[\w+/=:.-]*/g;

/** Splits `text` into alternating redacted/non-redacted segments for
 * rendering — used for JSON bodies and raw (non-JSON) bodies, where a
 * redacted token can appear anywhere inside a larger string. */
export function splitRedacted(text: string): RedactionSegment[] {
  const segments: RedactionSegment[] = [];
  let lastIndex = 0;
  for (const match of text.matchAll(REDACTED_RE)) {
    const idx = match.index ?? 0;
    if (idx > lastIndex) segments.push({ text: text.slice(lastIndex, idx), redacted: false });
    segments.push({ text: match[0], redacted: true });
    lastIndex = idx + match[0].length;
  }
  if (lastIndex < text.length) segments.push({ text: text.slice(lastIndex), redacted: false });
  return segments;
}

/** Tooltip copy for a redacted value. Two sentences, not one, because the
 * two markers state materially different facts. A masked value was
 * DESTROYED at capture — core/trace/redact.go writes the literal
 * "[redacted]" over it before the hop reaches disk, so no later feature can
 * bring it back and the tooltip must not imply one will. An `$enc:v1:`
 * value is still present in the recording; it is this viewer that holds no
 * key. Accepts the raw marker text, which may arrive quoted from a
 * pretty-printed JSON body, hence includes() rather than startsWith(). */
export function redactedTitle(marker: string): string {
  return marker.includes('$enc:v1:')
    ? 'This value was encrypted at capture. The dashboard holds no key for it, so it cannot be shown here.'
    : 'This value was redacted at capture. The recording does not contain it, so there is nothing here to reveal.';
}

/** Whole-value redaction check for header cells: core/trace/redact.go
 * replaces a matching header's ENTIRE value, never a substring, so a
 * header only ever needs an equality/prefix check, not a scan. */
export function isRedactedValue(value: string): boolean {
  return value === '[redacted]' || value.startsWith('$enc:v1:');
}
