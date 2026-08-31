/**
 * Shared arithmetic for seeking a run's evidence video to the moment a
 * checkpoint was captured — used by both retrace-ui's ItemScreen and
 * ensemble-ui's DetailPane, which otherwise each hand-roll the same
 * checkpoint-timestamp-to-video-offset math against their own
 * independently generated Manifest mirror.
 */

/**
 * Seconds from a run's start to one checkpoint's own timestamp, for
 * seeking a `<video>` element that (approximately) began recording when
 * the run itself started — retrace has no independent record of when
 * video capture began, so `Manifest.startedAt` is the closest available
 * anchor (see runs.Checkpoint.At's doc comment).
 *
 * Returns null when either timestamp is missing or unparseable (a
 * manifest written before Checkpoint.At existed reports "" for a
 * checkpoint's `at`, never a bogus zero-time offset) or when the
 * checkpoint's timestamp precedes the run's start — clock skew between
 * the adapter process that stamped the shot's mtime and this run's own
 * StartedAt — rather than seeking to a negative, meaningless offset.
 */
export function checkpointVideoOffsetSeconds(checkpointAt: string, runStartedAt: string): number | null {
  if (!checkpointAt || !runStartedAt) return null;
  const at = Date.parse(checkpointAt);
  const startedAt = Date.parse(runStartedAt);
  if (Number.isNaN(at) || Number.isNaN(startedAt)) return null;
  const offsetSeconds = (at - startedAt) / 1000;
  return offsetSeconds >= 0 ? offsetSeconds : null;
}
