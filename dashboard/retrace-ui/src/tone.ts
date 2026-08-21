import type { Item } from './api/types';

/**
 * Presentation only.
 *
 * There is deliberately NO TypeScript copy of serve.ScoreOf here. The server
 * computes the score, puts it on the wire as `Item.score`, and this app sorts
 * and renders that number. A mirrored formula is two implementations of one
 * rule, and the day they disagree the queue silently orders itself
 * differently from the CI report.
 */
export function verdictTone(v: Item['verdict']): 'green' | 'amber' | 'red' {
  switch (v) {
    case 'pass':
      return 'green';
    case 'changed':
      return 'amber';
    case 'failed':
      return 'red';
  }
}
