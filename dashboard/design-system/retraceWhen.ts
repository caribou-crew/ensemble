// Readable run timestamps, shared by the runs list and the run detail
// header. A retrace runId is `<YYYYMMDDTHHMMSSZ>-<sha>`, which encodes the
// time but reads as a serial number; a run's manifest carries a real ISO
// `finishedAt`/`when`. Prefer the ISO, fall back to parsing the runId
// stamp, and show the raw id only when neither parses.

/** Parse the leading `YYYYMMDDTHHMMSSZ` of a runId into epoch ms, or NaN. */
export function parseRunIdStamp(runId: string): number {
  const m = /^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z/.exec(runId);
  if (!m) return NaN;
  const [, y, mo, d, h, mi, s] = m;
  return Date.parse(`${y}-${mo}-${d}T${h}:${mi}:${s}Z`);
}

// Go's time.Time zero value marshals to "0001-01-01T00:00:00Z", which
// Date.parse HAPPILY parses (to year 1) rather than rejecting — so a run
// whose manifest never recorded a real finishedAt would otherwise render as
// "Dec 31, 1". Anything at or before this cutoff is treated as "no
// timestamp" so the runId-stamp fallback kicks in.
const ZERO_DATE_CUTOFF_MS = Date.parse('0002-01-01T00:00:00Z');

function realMs(iso: string | undefined): number {
  if (!iso) return NaN;
  const ms = Date.parse(iso);
  if (Number.isNaN(ms) || ms < ZERO_DATE_CUTOFF_MS) return NaN;
  return ms;
}

/**
 * The same iso-then-runId-stamp resolution `formatWhen` renders, as a raw
 * epoch ms for sorting. NaN (neither source parsed) sorts a run to the
 * bottom of a newest-first list rather than crashing the comparator.
 */
export function whenMs(iso: string | undefined, runId: string): number {
  const fromIso = realMs(iso);
  return Number.isNaN(fromIso) ? parseRunIdStamp(runId) : fromIso;
}

/**
 * A human-readable local date+time for a run. `iso` is the manifest
 * timestamp (may be '', undefined, or the Go zero value); `runId` is the
 * fallback source. Returns the raw runId when neither yields a valid date,
 * so the cell is never blank and never a wrong date.
 */
export function formatWhen(iso: string | undefined, runId: string): string {
  const ms = whenMs(iso, runId);
  if (Number.isNaN(ms)) return runId || '—';
  return new Date(ms).toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  });
}

/**
 * A human-readable local date+time for when a run was SYNCED (`Source.syncedAt`)
 * — distinct from formatWhen (when the run itself happened) and with no
 * runId-stamp fallback: a runId's timestamp is when the flow ran, which is
 * not an honest stand-in for when `retrace sync` pulled it, so an absent or
 * unparseable syncedAt renders as "—" rather than a plausible-looking wrong
 * date. There is no case where iso is present but not a real timestamp
 * (source.json is only ever written by `retrace sync`, which always stamps
 * a real time.Now()) — the zero-cutoff guard is defensive, matching
 * formatWhen's own stance on a Go zero-time value that could reach here.
 */
export function formatSyncedAt(iso: string | undefined): string {
  const ms = realMs(iso);
  if (Number.isNaN(ms)) return '—';
  return new Date(ms).toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  });
}

/** Default staleness threshold for isStale: a day. Not configurable —
 * see isStale's own doc comment for why this is a fixed constant rather
 * than a setting. */
export const STALE_THRESHOLD_MS = 24 * 60 * 60 * 1000;

/**
 * Whether a run is old enough that a reviewer should be told, right on the
 * queue row, rather than discovering it only after opening the flow. Reuses
 * whenMs's own iso-then-runId-stamp resolution so "stale" always agrees
 * with what formatWhen renders — a row's own displayed timestamp and its
 * staleness badge computed from two different sources would be confusing
 * in exactly the case a reviewer is most likely to double-check it.
 *
 * `now` is a parameter (defaulting to the real clock) so a test can pin it
 * without stubbing Date.now globally, the same shape whenMs's own callers
 * already use for sinceParam's tests.
 */
export function isStale(iso: string | undefined, runId: string, now: number = Date.now(), thresholdMs: number = STALE_THRESHOLD_MS): boolean {
  const ms = whenMs(iso, runId);
  if (Number.isNaN(ms)) return false;
  return now - ms > thresholdMs;
}
