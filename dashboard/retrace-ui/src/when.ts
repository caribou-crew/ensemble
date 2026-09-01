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
 * A human-readable local date+time for a run. `iso` is the manifest
 * timestamp (may be '', undefined, or the Go zero value); `runId` is the
 * fallback source. Returns the raw runId when neither yields a valid date, so
 * the cell is never blank and never a wrong date.
 */
export function formatWhen(iso: string | undefined, runId: string): string {
  const fromIso = realMs(iso);
  const ms = Number.isNaN(fromIso) ? parseRunIdStamp(runId) : fromIso;
  if (Number.isNaN(ms)) return runId || '—';
  return new Date(ms).toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  });
}
