# Phase 3 porting inventory — local-stack/web (Explore report, 2026-08-20)

## Port as goldens (pure, tested)
- topology/categories.ts — CATEGORIES, categoryOf(id), normalizeTopology (+test)
- topology/layout.ts (323L) — layoutClustered(topology, statuses, expandedBundles) → GraphLayout; clusters by category, DAG-levels, edge bundling (+test)
- topology/hopTimeline.ts — hopDepths(hops), hopTimeline(hops) → {startPct,widthPct,heat}, heatTier (+test)
- topology/traceLayout.ts (212L) — callDepths (BFS from inferred root), causalHopOrder, layoutTrace (+test)
- events/rows.ts — interleave hop rows + annotation events by ts (+test)
- events/spans.ts — derive spans from TraceEvent stream (+test)
- components/TopologyGraph.paintOrder.test.ts — only .tsx test
- trace/collapse.ts + export.ts ALREADY ported to Go (core/trace)

Note: "trace" logic is split across topology/hopTimeline, events/, and
TrafficTab.tsx inline — src/trace/ alone is NOT the complete trace unit.

## Architecture facts
- No router (hand-rolled tab nav, URL-as-state via history.replaceState —
  ?tab=&user=&trace=&windowId= deep links are a REAL FEATURE to preserve).
- No state/query lib — useState/useEffect + typed api.ts client (520L).
- SSE only for inspector stream; latency/stack/traffic used polling
  (ensemble's new server has proper SSE traffic — upgrade in rewrite).
- TrafficTab.tsx 835L is the largest component; split in rewrite.

## Design system
- No component lib; CSS custom-property token system in :root (keep) with
  colorblind-verified topology category palette (keep, documented deltaE).
- Dark-mode-only by deliberate choice. Fonts: @fontsource IBM Plex Sans/Mono.
- App.css is 2821 lines flat/global — biggest rewrite risk; componentize.
- Retro/confetti Easter egg in App.tsx (~100L, flag-gated) — CUT.

## Test setup (old)
- node:test via tsx, 9 test files, plain unit tests on pure fns + fixtures.
- New repo standard is vitest (per roadmap) — port tests to vitest.

## Rewrite decisions to make in the Phase 3 plan
- Keep dependency-light approach vs adopt router/query lib (lean: keep
  light; hand-rolled tabs + URL state worked, ensemble server has SSE).
- Endpoint mapping: old /stack/*, /latency/*, /trace/* → new /api/* server
  surface (status/topology/traffic SSE/traces/latency CRUD/sessions).
- Old app had product-specific tabs (cards/users/balances/bff1) — those map
  to the GENERIC `entities:` config pages in the new design, not ports.
- Zero TODO/FIXME in old code; comments explain why — good porting docs.
- The old app's useState/useEffect + `let cancelled` fetch idiom (above) was
  itself the source of three race bugs once ported into `ensemble-ui`. Phase
  4 replaced every hand-rolled copy across both dashboard apps with the
  shared `@ensemble/design-system/useAsync` hook (Task 14, consumed by
  Tasks 15 and 18) — a future reader of this inventory should reach for that
  hook rather than reintroduce the old prototype's fetch-on-mount pattern.
