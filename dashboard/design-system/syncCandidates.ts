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
interface HasWorkflow extends HasIdentity {
  workflowName: string;
  hasArtifacts: boolean;
  conclusion: string;
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

/**
 * "Latest of each lane": the freshest `hasArtifacts` candidate per workflow
 * name. Shared by RetraceSyncPanel's one-click "pull latest" and the queue's
 * own one-click "check all" header button — both want the exact same
 * picture of "the newest run per workflow", and a second hand-rolled copy of
 * this reduction is how the two buttons would quietly drift apart on what
 * "latest" means. A run with nothing to pull can't contribute a lane, so
 * `hasArtifacts` rows only. A failed run is excluded too: it uploads
 * artifacts (logs/debug bundles) but no replay bundle, so picking it as the
 * lane's "latest" only yields a bundle-less pull ("no manifest / no
 * pixel-replay shots"). Only a succeeded run carries a promotable bundle.
 */
export function pickLatestPerWorkflow<T extends HasWorkflow>(candidates: readonly T[]): T[] {
  const byWorkflow = new Map<string, T>();
  for (const c of candidates) {
    if (!c.hasArtifacts) continue;
    if (c.conclusion !== 'success') continue;
    const prev = byWorkflow.get(c.workflowName);
    if (!prev || c.createdAt > prev.createdAt) byWorkflow.set(c.workflowName, c);
  }
  return [...byWorkflow.values()];
}

/**
 * The repo a CI run's own web URL points at — `Source.runUrl` (the sidecar
 * `retrace sync` stamps onto every pulled run) already names it, so a
 * per-row "check for a newer run" never has to ask the reviewer or guess at
 * a server-configured default: it reads the repo the row's OWN last sync
 * actually came from. Correct even for a dashboard aggregating several
 * repos (ensemble.yaml's multi-repo case), where a server-side default
 * would be ambiguous. null for anything that isn't a
 * `github.com/<owner>/<repo>/actions/runs/...` URL — a locally recorded
 * run's Source is absent in the first place (see Source's own doc comment),
 * so this is a defensive parse failure, not a path this app expects to hit.
 */
export function repoFromRunUrl(url: string): string | null {
  const m = /^https:\/\/github\.com\/([^/]+\/[^/]+)\/actions\/runs\//.exec(url);
  return m ? m[1] : null;
}
