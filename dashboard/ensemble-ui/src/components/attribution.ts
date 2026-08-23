import type { Hop } from '../api/types';

/** Tooltip text for a hop whose caller came from a config-declared called_by hint rather
 * than real trace-context propagation — shared verbatim by every place that renders
 * hop.from, so the wording (and the "this is a guess" framing) stays one source of truth. */
export const INFERRED_CALLER_TITLE =
  'Inferred from config (called_by) — not derived from trace context';

export function isInferredCaller(hop: Hop): boolean {
  return hop.attribution === 'inferred';
}
