/**
 * Shared logic for a "discover -> filter -> select -> pull" candidate
 * panel's REFRESH — used by both retrace-ui's SyncPanel and ensemble-ui's
 * RetraceSyncPanel, which otherwise each hand-roll an identical
 * fetch-merge-dedupe against their own `{repo, sync}` REST surface.
 *
 * Generic over any candidate shape carrying at least `databaseId` and
 * `createdAt`, since the two apps' Candidate types are structurally
 * identical but independently generated from their own Go mirrors.
 */

interface HasCreatedAt {
  createdAt: string;
}
interface HasIdentity extends HasCreatedAt {
  databaseId: number;
}

/**
 * A `since` value for the NEXT candidates fetch that only asks for runs
 * newer than the newest one already known — not "newest already pulled": a
 * candidate the reviewer hasn't selected yet is still a run this list
 * already paid to discover, and re-listing it every refresh would defeat
 * the point. `since` is a DURATION ("Ns"/"Nh"/"Nd" — see
 * retrace/sync.ParseSince), not an absolute timestamp, so this converts
 * "newest known createdAt" into "how long ago that was", with a minute of
 * overlap for clock skew between this browser and GitHub's clock. The
 * overlap can reintroduce an already-known run; mergeCandidates below
 * de-dupes that by databaseId.
 */
export function sinceParam(candidates: readonly HasCreatedAt[]): string | undefined {
  if (candidates.length === 0) return undefined;
  let newestMs = -Infinity;
  for (const c of candidates) {
    const ms = Date.parse(c.createdAt);
    if (!Number.isNaN(ms) && ms > newestMs) newestMs = ms;
  }
  if (!Number.isFinite(newestMs)) return undefined;
  const overlapMs = 60_000;
  const ageSeconds = Math.max(1, Math.ceil((Date.now() - newestMs + overlapMs) / 1000));
  return `${ageSeconds}s`;
}

/**
 * Merges a refresh's results into the list already on screen — `fresh`
 * wins on a shared id (a run's status can advance between refreshes), and
 * anything only `existing` had (already known, not re-asked-for this time)
 * is kept rather than dropped. Newest first, matching sync.List's own
 * order.
 */
export function mergeCandidates<T extends HasIdentity>(existing: readonly T[], fresh: readonly T[]): T[] {
  const byId = new Map(existing.map((c) => [c.databaseId, c]));
  for (const c of fresh) byId.set(c.databaseId, c);
  return Array.from(byId.values()).sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
}
