import { useEffect, useRef, useState } from 'react';
import { CATEGORIES, colorVarOf } from '../topology/categories';
import type { GraphEdge, GraphLayout, GraphNode, Health } from '../topology/types';
import type { HeatTier } from '../topology/hopTimeline';
import './TopologyGraph.css';

export interface TopologyGraphProps {
  layout: GraphLayout;
  /** Category + health legend at the bottom. Off in trace mode's compact view. */
  showLegend?: boolean;
  selectedEdgeKey?: string | null;
  onSelectEdge?: (key: string | null) => void;
  onToggleBundle?: (bundleKey: string) => void;
  /** Fired with the clicked node's id, or null when the click deselects it — the seam
      TopologyView uses to open/close its service detail side panel. The old component had
      no such hook: it only tracked selection internally to drive the dim/highlight visuals,
      which this keeps doing regardless of whether a caller is listening. */
  onSelectNode?: (id: string | null) => void;
  /** Recent-activity glow, keyed by node id: TopologyView derives this from a rolling count
      of the last 60s of traffic per service, run through heatTier() — it has nothing to do
      with a node's health (a perfectly healthy service can be red-hot busy) so it travels as
      its own map rather than folding into GraphNode. Absent/'normal' draws no glow. */
  nodeHeat?: Map<string, HeatTier>;
  /** Trace mode only: the hop ordinal selected in the hop list below, so its edge lights up
      even when that edge's badge carries several ordinals (repeated calls on one pair collapse
      onto a single edge — see traceLayout.ts). */
  selectedHop?: number | null;
  /** Trace mode only: fired with a specific ordinal when its badge number is clicked, so the
      caller can select and scroll to that exact hop row (not just "this edge"). */
  onSelectHop?: (hop: number) => void;
}

const HEALTH_LABEL: Record<Health, string> = {
  'up-native': 'up · native',
  'up-container': 'up · container',
  starting: 'starting',
  down: 'down',
  unknown: 'unknown',
};

const MIN_SCALE = 0.3;
const MAX_SCALE = 4;
const RESET_VIEW = { scale: 1, tx: 0, ty: 0 };

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

/** Maps a screen-space point into the SVG root's own (untransformed) user space via its CTM —
    stable throughout a drag/zoom since the root's CTM never includes the inner pan/zoom <g>. */
function toUserPoint(svg: SVGSVGElement, clientX: number, clientY: number): { x: number; y: number } {
  const ctm = svg.getScreenCTM();
  if (!ctm) return { x: clientX, y: clientY };
  const pt = svg.createSVGPoint();
  pt.x = clientX;
  pt.y = clientY;
  const p = pt.matrixTransform(ctm.inverse());
  return { x: p.x, y: p.y };
}

/** N = native, C = container — placement is a glyph, not a colour, so it can't collide
    with the category accents. */
function placementGlyph(h: Health): string {
  if (h === 'up-native') return 'N';
  if (h === 'up-container') return 'C';
  return '';
}

function pathOf(e: GraphEdge): string {
  return e.points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${p.x} ${p.y}`).join(' ');
}

function midpoint(e: GraphEdge) {
  const mid = Math.floor((e.points.length - 1) / 2);
  const a = e.points[mid];
  const b = e.points[mid + 1] ?? a;
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
}

function NodeCard({
  node,
  selected,
  dimmed,
  heat = 'normal',
  onClick,
}: {
  node: GraphNode;
  selected: boolean;
  dimmed: boolean;
  heat?: HeatTier;
  onClick: () => void;
}) {
  return (
    <g
      transform={`translate(${node.x}, ${node.y})`}
      className={`topo-node${selected ? ' topo-node-selected' : ''}${dimmed ? ' topo-node-dim' : ''}${heat !== 'normal' ? ` topo-node-heat-${heat}` : ''}`}
      onClick={onClick}
    >
      <title>{node.id}</title>
      <rect
        width={node.w}
        height={node.h}
        rx={8}
        className={`topo-node-box${node.synthetic ? ' topo-node-synthetic' : ''}`}
      />
      <rect width={4} height={node.h} rx={2} fill={`var(${colorVarOf(node.category)})`} />
      <text x={16} y={node.h / 2 - 2} className="topo-node-label">
        {node.id}
      </text>
      {node.port !== undefined && node.port > 0 && (
        <text x={16} y={node.h / 2 + 13} className="topo-node-sub">
          :{node.port} {placementGlyph(node.health)}
        </text>
      )}
      <circle cx={node.w - 14} cy={node.h / 2} r={4} className={`topo-health topo-health-${node.health}`}>
        <title>{HEALTH_LABEL[node.health]}</title>
      </circle>
    </g>
  );
}

export default function TopologyGraph({
  layout,
  showLegend = true,
  selectedEdgeKey = null,
  onSelectEdge,
  onToggleBundle,
  onSelectNode,
  nodeHeat,
  selectedHop = null,
  onSelectHop,
}: TopologyGraphProps) {
  const svgRef = useRef<SVGSVGElement | null>(null);
  const dragRef = useRef<{ x: number; y: number; tx: number; ty: number } | null>(null);
  const [view, setView] = useState(RESET_VIEW);
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  // Falls back to local state when the caller doesn't wire selectedEdgeKey/onSelectEdge —
  // otherwise clicking an edge would do nothing at all.
  const [internalSelectedEdgeKey, setInternalSelectedEdgeKey] = useState<string | null>(null);
  const effectiveSelectedEdgeKey = onSelectEdge ? selectedEdgeKey : internalSelectedEdgeKey;

  const selectEdge = (key: string | null) => {
    if (onSelectEdge) onSelectEdge(key);
    else setInternalSelectedEdgeKey(key);
  };

  const selectNode = (id: string) => {
    setSelectedNodeId((cur) => {
      const next = cur === id ? null : id;
      onSelectNode?.(next);
      return next;
    });
  };

  function zoomAt(clientX: number, clientY: number, factor: number) {
    const svg = svgRef.current;
    if (!svg) return;
    setView((v) => {
      const newScale = clamp(v.scale * factor, MIN_SCALE, MAX_SCALE);
      const k = newScale / v.scale;
      const p = toUserPoint(svg, clientX, clientY);
      return { scale: newScale, tx: p.x - k * (p.x - v.tx), ty: p.y - k * (p.y - v.ty) };
    });
  }

  function zoomButton(factor: number) {
    const svg = svgRef.current;
    if (!svg) return;
    const rect = svg.getBoundingClientRect();
    zoomAt(rect.left + rect.width / 2, rect.top + rect.height / 2, factor);
  }

  // React's onWheel is passive by default (React 17+), so preventDefault() inside it silently
  // no-ops — a native listener with {passive:false} is the only way to actually stop page scroll
  // while wheeling over the graph.
  useEffect(() => {
    const svg = svgRef.current;
    if (!svg) return undefined;
    const onWheel = (evt: WheelEvent) => {
      evt.preventDefault();
      if (evt.ctrlKey || evt.metaKey) {
        zoomAt(evt.clientX, evt.clientY, Math.exp(-evt.deltaY * 0.01));
        return;
      }
      const ctm = svg.getScreenCTM();
      const s = ctm ? ctm.a : 1;
      setView((v) => ({ ...v, tx: v.tx - evt.deltaX / s, ty: v.ty - evt.deltaY / s }));
    };
    svg.addEventListener('wheel', onWheel, { passive: false });
    return () => svg.removeEventListener('wheel', onWheel);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function onBgPointerDown(evt: React.PointerEvent<SVGRectElement>) {
    dragRef.current = { x: evt.clientX, y: evt.clientY, tx: view.tx, ty: view.ty };
    try {
      evt.currentTarget.setPointerCapture(evt.pointerId);
    } catch {
      // Safari can throw NotFoundError for a pointer id it doesn't consider active — harmless.
    }
  }

  function onBgPointerMove(evt: React.PointerEvent<SVGRectElement>) {
    const drag = dragRef.current;
    const svg = svgRef.current;
    if (!drag || !svg) return;
    const ctm = svg.getScreenCTM();
    const s = ctm ? ctm.a : 1;
    const dx = (evt.clientX - drag.x) / s;
    const dy = (evt.clientY - drag.y) / s;
    setView((v) => ({ ...v, tx: drag.tx + dx, ty: drag.ty + dy }));
  }

  function onBgPointerUp(evt: React.PointerEvent<SVGRectElement>) {
    dragRef.current = null;
    try {
      evt.currentTarget.releasePointerCapture(evt.pointerId);
    } catch {
      // ignore — see onBgPointerDown
    }
  }

  if (layout.nodes.length === 0) {
    return <div className="topo-empty-state">Nothing to draw.</div>;
  }

  const isSelected = (e: GraphEdge) =>
    effectiveSelectedEdgeKey === e.key || (selectedHop != null && (e.hopLabels?.includes(selectedHop) ?? false));

  // Selecting a node highlights it and its direct neighbors, dimming everything else.
  const neighborNodeIds = new Set<string>();
  const neighborEdgeKeys = new Set<string>();
  if (selectedNodeId) {
    for (const e of layout.edges) {
      if (e.from === selectedNodeId || e.to === selectedNodeId) {
        neighborEdgeKeys.add(e.key);
        neighborNodeIds.add(e.from);
        neighborNodeIds.add(e.to);
      }
    }
  }
  const isDimmedNode = (n: GraphNode) => selectedNodeId != null && n.id !== selectedNodeId && !neighborNodeIds.has(n.id);
  const isDimmedEdge = (e: GraphEdge) => selectedNodeId != null && !neighborEdgeKeys.has(e.key);

  // Every edge leaving a shared source node overlaps the same initial segment (route() puts
  // them all through one lane before diverging — see traceLayout.ts), so whichever draws
  // LAST wins that shared pixel. Selected edges must render last or their highlight color
  // gets painted over by a later, merely-gray sibling with no visual difference to lose.
  const orderedEdges = [...layout.edges].sort((a, b) => Number(isSelected(a)) - Number(isSelected(b)));

  const groupTransform = `translate(${view.tx}, ${view.ty}) scale(${view.scale})`;

  return (
    <div className="topo-graph">
      <div className="topo-graph-canvas">
        <svg
          ref={svgRef}
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          preserveAspectRatio="xMidYMid meet"
          className="topo-svg"
          // Scale is clamped on BOTH ends, and the natural width is what caps it at 1.0:
          // sizing the SVG to its own layout units means a container wider than the graph
          // leaves it at 1:1 rather than magnifying it. maxWidth lets it shrink to fit, and
          // minWidth stops the shrink at ~55%, where the 13px node labels stop being
          // readable — past that the canvas scrolls instead.
          style={{
            width: layout.width,
            height: layout.height,
            maxWidth: '100%',
            minWidth: Math.round(layout.width * 0.55),
          }}
          onDoubleClick={() => setView(RESET_VIEW)}
          role="img"
          aria-label="ensemble topology"
        >
          <defs>
            <marker id="topo-arrow" viewBox="0 0 10 10" refX="8.5" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
              <path d="M0,0 L10,5 L0,10 z" className="topo-edge-arrow" />
            </marker>
            <marker
              id="topo-arrow-selected"
              viewBox="0 0 10 10"
              refX="8.5"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 z" className="topo-edge-arrow-selected" />
            </marker>
          </defs>

          {/* Sits outside the pan/zoom group so it always covers the full viewport regardless
              of the current transform — a drag must be catchable no matter where content sits. */}
          <rect
            x={-10000}
            y={-10000}
            width={20000}
            height={20000}
            className="topo-pan-bg"
            onPointerDown={onBgPointerDown}
            onPointerMove={onBgPointerMove}
            onPointerUp={onBgPointerUp}
          />

          <g transform={groupTransform}>
            {layout.clusters.map((c) => (
              <g key={c.id}>
                <rect
                  x={c.x}
                  y={c.y}
                  width={c.w}
                  height={c.h}
                  rx={12}
                  className="topo-cluster"
                  stroke={`var(${colorVarOf(c.id)})`}
                />
                <text x={c.x + 14} y={c.y + 18} className="topo-cluster-label" fill={`var(${colorVarOf(c.id)})`}>
                  {c.label.toUpperCase()}
                </text>
              </g>
            ))}

            {orderedEdges.map((e) => {
              // A hop badge is one-to-many: repeated calls on the same from->to pair collapse
              // onto a single edge carrying every ordinal (traceLayout.ts), so "this edge is
              // selected" must check membership in hopLabels, not equality against one key.
              const selected = isSelected(e);
              const dimmed = isDimmedEdge(e);
              const d = pathOf(e);
              return (
                <g
                  key={e.key}
                  className={`topo-edge${selected ? ' topo-edge-selected' : ''}${e.bundleCount ? ' topo-edge-bundled' : ''}${dimmed ? ' topo-edge-dim' : ''}${e.inferred ? ' topo-edge-inferred' : ''}`}
                  onClick={() => {
                    if (e.bundleKey && onToggleBundle) onToggleBundle(e.bundleKey);
                    else if (!e.hopLabels) selectEdge(selected ? null : e.key);
                  }}
                >
                  <path
                    d={d}
                    className="topo-edge-line"
                    markerEnd={selected ? 'url(#topo-arrow-selected)' : 'url(#topo-arrow)'}
                  />
                  {selected && (
                    <circle r={4} className="topo-edge-flow-dot">
                      <animateMotion dur="1.4s" repeatCount="indefinite" path={d} />
                    </circle>
                  )}
                  <title>
                    {(e.bundleExpanded
                      ? `${e.from} → ${e.to} — one of ${e.bundleCount} (click to collapse)`
                      : e.bundleCount
                        ? `${e.from} → ${e.bundleCount} services in ${e.to} (click to expand)`
                        : `${e.from} → ${e.to}`) + (e.inferred ? ' (inferred from config, not trace context)' : '')}
                  </title>
                </g>
              );
            })}

            {layout.nodes.map((n) => (
              <NodeCard
                key={n.id}
                node={n}
                selected={selectedNodeId === n.id}
                dimmed={isDimmedNode(n)}
                heat={nodeHeat?.get(n.id)}
                onClick={() => selectNode(n.id)}
              />
            ))}

            {/* Labels render in their own pass, after nodes, so a pill whose midpoint lands on
                top of a node card (any backward or same-column hop, or a bundle next to its
                own target) stays clickable — nodes paint after lines and would otherwise
                intercept the pointer. */}
            {orderedEdges.map((e) => {
              const selected = isSelected(e);
              const dimmed = isDimmedEdge(e);
              // '⌄' on the expanded group's representative edge — same pill, opposite action.
              const bundleLabel = e.bundleExpanded ? `${e.bundleCount} ⌃`
                : e.bundleCount ? `${e.bundleCount} ×` : undefined;
              if (!bundleLabel && !e.hopLabels) return null;
              const m = midpoint(e);
              const pillWidth = e.hopLabels ? Math.max(24, 12 + e.hopLabels.length * 14) : 32;
              return (
                <g
                  key={`${e.key}-label`}
                  className={`topo-edge${selected ? ' topo-edge-selected' : ''}${e.bundleCount ? ' topo-edge-bundled' : ''}${dimmed ? ' topo-edge-dim' : ''}`}
                  onClick={() => {
                    if (e.bundleKey && onToggleBundle) onToggleBundle(e.bundleKey);
                  }}
                >
                  {bundleLabel && (
                    <>
                      <rect x={m.x - 16} y={m.y - 9} width={32} height={18} rx={9} className="topo-edge-pill" />
                      <text x={m.x} y={m.y + 4} textAnchor="middle" className="topo-edge-pill-text">
                        {bundleLabel}
                      </text>
                    </>
                  )}
                  {e.hopLabels && (
                    <>
                      <rect
                        x={m.x - pillWidth / 2}
                        y={m.y - 9}
                        width={pillWidth}
                        height={18}
                        rx={9}
                        className="topo-edge-pill"
                      />
                      {e.hopLabels.map((n, i) => {
                        const slot = pillWidth / (e.hopLabels as number[]).length;
                        const cx = m.x - pillWidth / 2 + slot * (i + 0.5);
                        return (
                          <text
                            key={n}
                            x={cx}
                            y={m.y + 4}
                            textAnchor="middle"
                            className={`topo-edge-pill-text topo-edge-hop-number${
                              selectedHop === n ? ' topo-edge-hop-number-selected' : ''
                            }`}
                            onClick={(evt) => {
                              evt.stopPropagation();
                              onSelectHop?.(n);
                            }}
                          >
                            {n}
                            {i < (e.hopLabels as number[]).length - 1 ? ',' : ''}
                          </text>
                        );
                      })}
                    </>
                  )}
                  <title>
                    {e.bundleExpanded
                      ? `${e.from} → ${e.to} — one of ${e.bundleCount} (click to collapse)`
                      : e.bundleCount
                        ? `${e.from} → ${e.bundleCount} services in ${e.to} (click to expand)`
                        : `${e.from} → ${e.to}`}
                  </title>
                </g>
              );
            })}
          </g>
        </svg>

        <div className="topo-zoom-controls">
          <button type="button" onClick={() => zoomButton(1.25)} aria-label="zoom in">+</button>
          <button type="button" onClick={() => zoomButton(0.8)} aria-label="zoom out">−</button>
          <button type="button" onClick={() => setView(RESET_VIEW)} aria-label="reset view">⟲</button>
        </div>
      </div>

      {showLegend && (
        <div className="topo-legend">
          <div className="topo-legend-group">
            {CATEGORIES.filter((c) => layout.clusters.some((cl) => cl.id === c.id)).map((c) => (
              <span key={c.id} className="topo-legend-item">
                <span className="topo-legend-bar" style={{ background: `var(${c.colorVar})` }} />
                {c.label}
              </span>
            ))}
          </div>
          <div className="topo-legend-group">
            <span className="topo-legend-item">
              <span className="topo-health-dot topo-health-up-native" /> up
            </span>
            <span className="topo-legend-item">
              <span className="topo-health-dot topo-health-starting" /> starting
            </span>
            <span className="topo-legend-item">
              <span className="topo-health-dot topo-health-down" /> down
            </span>
            <span className="topo-legend-item">
              <span className="topo-health-dot topo-health-unknown" /> unknown
            </span>
            <span className="topo-legend-item dim">N native · C container</span>
          </div>
        </div>
      )}
    </div>
  );
}
