import type { Hop } from '../api/types';

/** Tooltip text per non-trace-derived attribution kind — shared verbatim by every place
 * that renders hop.from, so the wording (and the "this isn't a hard fact" framing) stays
 * one source of truth. Keyed by Hop.attribution. */
export const CALLER_ATTRIBUTION_TITLE: Record<'inferred' | 'declared', string> = {
  inferred: 'Inferred from config (called_by) — not derived from trace context',
  declared:
    'Caller self-declared via the X-Ensemble-Caller header — not derived from trace context',
};

/** "Inferred from config (called_by)" — kept for the common case's exact prior text. */
export const INFERRED_CALLER_TITLE = CALLER_ATTRIBUTION_TITLE.inferred;

export function isInferredCaller(hop: Hop): boolean {
  return hop.attribution === 'inferred';
}

export function isDeclaredCaller(hop: Hop): boolean {
  return hop.attribution === 'declared';
}

/** hop.attribution if it's a non-trace-derived kind, else null — lets a render site pick
 * one class/title lookup instead of chaining isInferredCaller/isDeclaredCaller. */
export function callerAttribution(hop: Hop): 'inferred' | 'declared' | null {
  return hop.attribution ?? null;
}

/** Tooltip for the client-identity badge. One source of truth, same as
 * CALLER_ATTRIBUTION_TITLE — the framing matters: `client` is a validated
 * identifier the app declared about ITSELF, not a trace-derived fact about
 * the service graph, and a reader who conflates it with `from` will read the
 * topology wrong. */
export const CLIENT_IDENTITY_TITLE =
  'Client application, self-declared via a client-identity header ' +
  '(client_identity_headers, default x-source-client / x-local-client). ' +
  'Validated at capture; a malformed value is recorded as "client".';

/** hop.client if the hop carries one, else null. A render site can then do
 * one lookup instead of repeating the empty check, and — the reason this is
 * not just `hop.client` — an empty string is normalized to null so a hop
 * whose header arrived blank cannot render an empty badge. */
export function clientIdentity(hop: Hop): string | null {
  return hop.client ? hop.client : null;
}
