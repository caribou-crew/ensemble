// How a hop READS: its session label, timestamp, payload size, and the
// status class/icon that colors it.
//
// Extracted from HopTable so every surface that renders a hop agrees about
// them. The Traffic view now shows hops in two shapes — the full merged
// table and the narrow side-by-side panes, which cannot reuse a twelve-
// column <tr> — and "a transport error reads as an error, a 404 reads as a
// client error" has to mean the same thing in both. Duplicating these
// would let one shape quietly disagree with the other about what a hop is.
import type { Hop } from '../api/types';
import { hopPayloadBytes } from '../trafficFilter';

export function sessionLabel(session?: string): string {
  return session ? session.slice(0, 8) : 'ambient';
}

/** HH:MM:SS:mmm in the viewer's local time, from t.start (when the proxy
 * first saw the request, before any injected latency). */
export function formatTimestamp(start: string): string {
  const d = new Date(start);
  if (Number.isNaN(d.getTime())) return '—';
  const pad = (n: number, len = 2) => String(n).padStart(len, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}:${pad(d.getMilliseconds(), 3)}`;
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`;
  return `${(n / 1024 / 1024).toFixed(1)}MB`;
}

/** Combined request+response payload size. Bodies are captured up to
 * core/proxy.CaptureLimit — a truncated one reports only what was
 * actually captured, flagged with a trailing "+" so it doesn't read as
 * the true wire size. hopPayloadBytes is shared with trafficFilter's
 * `size` comparisons so the column and the filter can never disagree on
 * what "size" means. */
export function payloadSize(hop: Hop): string {
  const bytes = hopPayloadBytes(hop);
  const truncated = Boolean(hop.req?.truncated || hop.resp?.truncated);
  return formatBytes(bytes) + (truncated ? '+' : '');
}

/** Charles-style status coloring: green success, blue redirect, red
 * client/server error. A hop with no status but a transport-level err
 * (e.g. connection refused) still reads as an error. */
export function statusClass(hop: Hop): string {
  const status = hop.status ?? 0;
  if (status >= 500) return 'hop-table__status--5xx';
  if (status >= 400) return 'hop-table__status--4xx';
  if (status >= 300) return 'hop-table__status--3xx';
  if (status >= 200) return 'hop-table__status--2xx';
  if (hop.err) return 'hop-table__status--error';
  return '';
}

/** A small glyph ahead of the status code so 3xx/4xx/5xx/transport errors
 * are scannable without relying on color alone (colorblind users, a
 * washed-out external display). 2xx is left unmarked — it's the expected
 * case, not one that needs flagging. */
export function statusIcon(hop: Hop): string {
  const status = hop.status ?? 0;
  if (status >= 500) return '✕';
  if (status >= 400) return '⚠';
  if (status >= 300) return '↪';
  if (status < 200 && hop.err) return '✕';
  return '';
}
