// A retrace run's link back to its traffic in the ensemble dashboard.
//
// No new endpoint and no new id: `retrace run` already registers its run id
// AS ensemble's session id, so every hop of the run already carries it, and
// the ensemble Traffic view already filters by session. All this does is
// assemble the URL from what the manifest recorded.
import type { Manifest } from './retraceTypes';

/**
 * The URL of this run's traffic in the ensemble dashboard, or null when the
 * run was not captured against one.
 *
 * Null is the whole point of the return type: a standalone run has no
 * control plane, and a caller that rendered a link anyway would send a
 * reviewer to a dead address — or, worse, to whatever stack happens to be
 * running on the default port, showing them another run's traffic under
 * this run's heading.
 */
export function ensembleTrafficUrl(manifest?: Manifest | null): string | null {
  const link = manifest?.ensemble;
  if (!link?.api || !link.session) return null;
  const base = link.api.replace(/\/+$/, '');
  return `${base}/?view=traffic&session=${encodeURIComponent(link.session)}`;
}
