import { createRetraceClient } from '@ensemble/design-system/retraceClient';
import { parseRunIdStamp } from '@ensemble/design-system/retraceWhen';
import type { Counts, Summary, SurfaceRun } from '@ensemble/design-system/retraceTypes';

export const client = createRetraceClient('/api');

export const LOCAL = '__local';
/** `reportBase` value meaning "diff against the accepted reference bundle". */
export const REFERENCE = '';

export const APP_LABELS: Record<string, string> = {
  'uxt-web': 'Web',
  'uxt-rn-ios': 'RN iOS',
  'uxt-rn-android': 'RN Android',
  'uxt-native-ios': 'Native iOS',
  'uxt-native-android': 'Native Android',
  'uxt-flutter-ios': 'Flutter iOS',
  'uxt-flutter-android': 'Flutter Android',
};

const APP_ORDER = Object.keys(APP_LABELS);

export const FLOW_LABELS: Record<string, string> = {
  'card-views': 'Card Views',
  disputes: 'Disputes',
  'modules-sweep': 'Modules',
  statements: 'Statements',
  'statements-debit': 'Statements (Debit)',
};

export const appLabel = (app: string) => APP_LABELS[app] ?? app;
export const flowLabel = (flow: string) => FLOW_LABELS[flow] ?? flow;
export const branchLabel = (b: string) => (b === LOCAL ? 'local recordings' : b);

export function orderApps(apps: Iterable<string>): string[] {
  const set = new Set(apps);
  return [...APP_ORDER.filter((a) => set.has(a)), ...[...set].filter((a) => !APP_ORDER.includes(a)).sort()];
}

export function orderFlows(flows: Iterable<string>): string[] {
  const set = new Set(flows);
  const known = Object.keys(FLOW_LABELS);
  return [...known.filter((f) => set.has(f)), ...[...set].filter((f) => !known.includes(f)).sort()];
}

export const branchOf = (r: SurfaceRun): string => r.source?.headBranch ?? LOCAL;

export interface Surface {
  app: string;
  flow: string;
  runs: SurfaceRun[];
}

export async function loadSurfaces(): Promise<Surface[]> {
  return (await client.surfaces()).surfaces;
}

/** CI-synced runs carry a zero `when`; the run id's leading stamp is the fallback. */
export function runMs(r: SurfaceRun): number {
  const ms = Date.parse(r.when ?? '');
  return !Number.isNaN(ms) && ms > Date.parse('0002-01-01T00:00:00Z') ? ms : parseRunIdStamp(r.runId);
}

export interface BranchInfo {
  name: string;
  lastRunAt: number;
  surfaces: number;
}

export function branchesOf(surfaces: Surface[]): BranchInfo[] {
  const m = new Map<string, BranchInfo>();
  for (const s of surfaces) {
    const seen = new Set<string>();
    for (const r of s.runs) {
      const name = branchOf(r);
      const info = m.get(name) ?? { name, lastRunAt: 0, surfaces: 0 };
      if (!seen.has(name)) {
        info.surfaces += 1;
        seen.add(name);
      }
      info.lastRunAt = Math.max(info.lastRunAt, runMs(r) || 0);
      m.set(name, info);
    }
  }
  return [...m.values()].sort((a, b) => b.lastRunAt - a.lastRunAt);
}

export function newestOn(s: Surface, branch: string): SurfaceRun | undefined {
  let best: SurfaceRun | undefined;
  for (const r of s.runs) if (branchOf(r) === branch && (!best || runMs(r) > runMs(best))) best = r;
  return best;
}

export interface Pairing {
  app: string;
  flow: string;
  run?: SurfaceRun;
  base?: SurfaceRun;
  /** True when a base branch was chosen but has no run for this surface. */
  missingBase: boolean;
}

export function pairings(surfaces: Surface[], branch: string, baseBranch: string): Pairing[] {
  const out: Pairing[] = [];
  for (const s of surfaces) {
    const run = newestOn(s, branch);
    if (!run) continue;
    const base = baseBranch === REFERENCE ? undefined : newestOn(s, baseBranch);
    out.push({ app: s.app, flow: s.flow, run, base, missingBase: baseBranch !== REFERENCE && !base });
  }
  return out;
}

export function loadSummary(p: Pairing): Promise<Summary> {
  if (!p.run) return Promise.reject(new Error('no run'));
  return client.itemAtRun(p.app, p.flow, p.run.runId, false, p.base?.runId).then((r) => r.summary);
}

export function shotUrl(p: Pairing, side: 'a' | 'b' | 'diff' | 'overlay', name: string): string {
  return client.shotUrlAtRun(p.app, p.flow, p.run!.runId, side, name, false, p.base?.runId);
}

export function countsParts(c: Counts): string[] {
  const parts: string[] = [];
  if (c.pixelChanged > 0) parts.push(`${c.pixelChanged}/${c.checkpoints} screens changed`);
  const wire = c.wireChanged + c.wireMissing + c.wireExtra;
  if (wire > 0) parts.push(`${wire} wire diff${wire === 1 ? '' : 's'}`);
  if (c.wireMoved > 0) parts.push(`${c.wireMoved} reordered`);
  if (c.unexpectedStatuses > 0) parts.push(`${c.unexpectedStatuses} bad status`);
  if (c.conformance > 0) parts.push(`${c.conformance} conformance`);
  return parts;
}

export function relTime(ms: number): string {
  if (!ms || Number.isNaN(ms)) return '';
  const mins = Math.round((Date.now() - ms) / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.round(mins / 60);
  if (hrs < 48) return `${hrs}h ago`;
  return `${Math.round(hrs / 24)}d ago`;
}

/** Markdown digest of a comparison, for pasting into a PR, Slack or an agent. */
export function summaryMarkdown(branch: string, baseBranch: string, rows: { p: Pairing; sum?: Summary; error?: string }[]): string {
  const vs = baseBranch === REFERENCE ? 'accepted baseline' : branchLabel(baseBranch);
  const lines = [`## retrace: ${branchLabel(branch)} vs ${vs}`, ''];
  for (const flow of orderFlows(rows.map((r) => r.p.flow))) {
    lines.push(`### ${flowLabel(flow)} (\`${flow}\`)`, '', '| platform | verdict | details | run |', '| --- | --- | --- | --- |');
    const byApp = new Map(rows.filter((r) => r.p.flow === flow).map((r) => [r.p.app, r]));
    for (const app of orderApps(byApp.keys())) {
      const r = byApp.get(app)!;
      const run = r.p.run?.runId ?? '';
      if (r.p.missingBase) {
        lines.push(`| ${appLabel(app)} | no base run | — | ${run} |`);
        continue;
      }
      if (!r.sum) {
        lines.push(`| ${appLabel(app)} | error | ${r.error ?? 'loading'} | ${run} |`);
        continue;
      }
      const changed = r.sum.checkpoints.filter((c) => c.verdict !== 'ok').map((c) => `${c.name} (${c.verdict}${c.diffPct ? ` ${c.diffPct.toFixed(2)}%` : ''})`);
      const details = [...countsParts(r.sum.counts), ...(changed.length ? [`screens: ${changed.join(', ')}`] : [])].join('; ') || 'no differences';
      lines.push(`| ${appLabel(app)} | ${r.sum.verdict} | ${details} | ${run} |`);
    }
    lines.push('');
  }
  return lines.join('\n');
}
