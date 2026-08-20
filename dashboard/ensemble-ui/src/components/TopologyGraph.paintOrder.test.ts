import { describe, expect, it } from 'vitest';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import TopologyGraph from './TopologyGraph';
import { SAMPLE_TOPOLOGY, BRANCHING_HOPS } from '../topology/fixtures';
import { layoutClustered } from '../topology/layout';
import { layoutTrace } from '../topology/traceLayout';

// SVG has no z-index: paint order IS document order, so a label emitted before a node card
// is painted under it and stops receiving clicks. That is not a styling nit — it made one
// bundle pill impossible to expand at all, while looking correct in a screenshot.

describe('TopologyGraph paint order', () => {
  it('renders bundle pills after node cards, so they stay clickable', () => {
    const layout = layoutClustered(SAMPLE_TOPOLOGY, new Map(), new Set());
    const markup = renderToStaticMarkup(createElement(TopologyGraph, { layout }));

    const lastNode = markup.lastIndexOf('topo-node-box');
    const firstPill = markup.indexOf('topo-edge-pill');
    expect(lastNode).not.toBe(-1);
    expect(firstPill).not.toBe(-1);
    expect(lastNode).toBeLessThan(firstPill);
  });

  it('renders trace-mode hop badges after node cards, so they stay clickable', () => {
    const markup = renderToStaticMarkup(
      createElement(TopologyGraph, { layout: layoutTrace(BRANCHING_HOPS), showLegend: false }),
    );

    const lastNode = markup.lastIndexOf('topo-node-box');
    const firstBadge = markup.indexOf('topo-edge-hop-number');
    expect(lastNode).not.toBe(-1);
    expect(firstBadge).not.toBe(-1);
    expect(lastNode).toBeLessThan(firstBadge);
  });
});
