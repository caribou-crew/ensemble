import { describe, expect, it } from 'vitest';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import TopologyGraph from './TopologyGraph';
import { layoutTrace } from '../topology/traceLayout';
import { LINEAR_HOPS } from '../topology/fixtures';

// A caller ensemble doesn't manage can self-declare its name via the X-Ensemble-Caller
// header (core/proxy.CallerHeader) — Hop.attribution "declared". Distinct from "inferred"
// (a static called_by config guess): both are "not a trace fact", but a declared caller
// asserted its own identity live, on the request itself.

describe('TopologyGraph: declared attribution', () => {
  it('marks a declared-caller edge with its own class and tooltip, distinct from inferred', () => {
    const markup = renderToStaticMarkup(
      createElement(TopologyGraph, {
        layout: layoutTrace([{ ...LINEAR_HOPS[0], from: 'external-app', to: 'edge-gateway', attribution: 'declared' }]),
        showLegend: false,
      }),
    );
    expect(markup).toContain('topo-edge-declared');
    expect(markup).not.toContain('topo-edge-inferred');
    expect(markup).toContain('caller self-declared');
  });

  it('still marks an inferred-caller edge with its own class, not declared', () => {
    const markup = renderToStaticMarkup(
      createElement(TopologyGraph, {
        layout: layoutTrace([{ ...LINEAR_HOPS[0], from: 'bff', to: 'edge-gateway', attribution: 'inferred' }]),
        showLegend: false,
      }),
    );
    expect(markup).toContain('topo-edge-inferred');
    expect(markup).not.toContain('topo-edge-declared');
  });
});
