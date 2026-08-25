import { describe, expect, it } from 'vitest';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import TopologyGraph from './TopologyGraph';
import { layoutDepth } from '../topology/depthLayout';
import { SAMPLE_TOPOLOGY } from '../topology/fixtures';
import type { ServiceState } from '../api/types';

// layoutDepth (like layoutTrace) never emits cluster boxes — category rides only as node
// color. The legend used to derive its category swatches from layout.clusters, which meant
// a boxless-but-legend-visible layout showed an empty legend even though every node was
// still colored by category.
describe('TopologyGraph legend in a boxless layout', () => {
  it('still lists every category present among the nodes', () => {
    const layout = layoutDepth(SAMPLE_TOPOLOGY, new Map<string, ServiceState>());
    expect(layout.clusters).toEqual([]);
    const markup = renderToStaticMarkup(createElement(TopologyGraph, { layout, showLegend: true }));
    expect(markup).toContain('Services');
    expect(markup).toContain('Databases');
    expect(markup).toContain('Stubs');
  });
});
