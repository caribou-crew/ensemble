import { useEffect, useMemo, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { verdictTone, verdictLabel } from '@ensemble/design-system/retraceTone';
import FlowCompare from './FlowCompare';
import {
  LOCAL,
  REFERENCE,
  appLabel,
  branchLabel,
  branchesOf,
  countsParts,
  flowLabel,
  loadSurfaces,
  orderApps,
  orderFlows,
  pairings,
  relTime,
  runMs,
  shotUrl,
  summaryMarkdown,
  type Pairing,
} from './reportData';
import { pairKey, useSummaries, type SummaryState } from './useSummaries';
import { useUrlParam } from './urlState';
import './BranchReport.css';

export type ReportTab = 'screens' | 'videos' | 'detail';

function Cell({ p, st, onOpen }: { p: Pairing; st?: SummaryState; onOpen: () => void }) {
  if (p.missingBase) {
    return (
      <div className="report-cell report-cell--muted">
        <span className="report-cell__app">{appLabel(p.app)}</span>
        <span className="report-cell__note">no run on the base branch</span>
      </div>
    );
  }
  const sum = st?.sum;
  const tone = sum ? verdictTone(sum.verdict) : 'neutral';
  const shown = sum?.checkpoints.find((c) => c.verdict !== 'ok' && c.images.b) ?? sum?.checkpoints.find((c) => c.images.b);
  return (
    <button type="button" className={`report-cell report-cell--${tone}`} onClick={onOpen}>
      <div className="report-cell__header">
        <span className="report-cell__app">{appLabel(p.app)}</span>
        {sum ? <Badge tone={tone}>{verdictLabel(sum.verdict)}</Badge> : st?.error ? <Badge tone="amber">error</Badge> : <Spinner />}
      </div>
      {sum ? (
        <p className="report-cell__counts">{countsParts(sum.counts).join(' · ') || `${sum.counts.checkpoints} screens identical`}</p>
      ) : st?.error ? (
        <p className="report-cell__counts report-cell__counts--error">{st.error}</p>
      ) : null}
      {sum && shown ? (
        <div className="report-cell__thumbs">
          <img className="report-cell__thumb" src={shotUrl(p, 'a', shown.name)} alt={`${shown.name} before`} loading="lazy" />
          <span className="report-cell__thumb-arrow" aria-hidden>→</span>
          <img className="report-cell__thumb" src={shotUrl(p, 'b', shown.name)} alt={`${shown.name} after`} loading="lazy" />
        </div>
      ) : null}
      <span className="report-cell__when">{relTime(p.run ? runMs(p.run) : 0)}</span>
    </button>
  );
}

export default function BranchReport({
  version,
  selectedBranch,
  onSelectBranch,
}: {
  version: number;
  selectedBranch: string | null;
  onSelectBranch: (branch: string | null) => void;
}) {
  const [baseParam, setBase] = useUrlParam('reportBase');
  const [flow, setFlow] = useUrlParam('reportFlow');
  const [app, setApp] = useUrlParam('reportApp');
  const [tabParam, setTab] = useUrlParam('reportTab');
  const baseBranch = baseParam ?? REFERENCE;
  const tab: ReportTab = tabParam === 'videos' || tabParam === 'detail' ? tabParam : 'screens';
  const [copied, setCopied] = useState(false);

  const surfaces = useAsync(() => loadSurfaces(), [version]);
  const branches = useMemo(() => branchesOf(surfaces.data ?? []), [surfaces.data]);

  useEffect(() => {
    if (selectedBranch === null && branches.length > 0) onSelectBranch((branches.find((b) => b.name !== LOCAL) ?? branches[0]).name);
  }, [branches, selectedBranch, onSelectBranch]);

  const ps = useMemo(
    () => (selectedBranch && surfaces.data ? pairings(surfaces.data, selectedBranch, baseBranch) : []),
    [surfaces.data, selectedBranch, baseBranch],
  );
  const summaries = useSummaries(ps);

  const open = (next: { flow: string | null; app?: string | null; tab?: ReportTab | null }) => {
    setFlow(next.flow);
    setApp(next.app ?? null);
    setTab(next.tab ?? null);
  };

  // Capture phase so Esc steps up inside the report before App's global handler leaves it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || !flow || document.querySelector('.report-lightbox')) return;
      e.stopPropagation();
      e.preventDefault();
      if (tab === 'detail') open({ flow, tab: 'screens' });
      else open({ flow: null });
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  });

  const verdicts = [...summaries.values()].map((s) => s.sum?.verdict);
  const count = (v: string) => verdicts.filter((x) => x === v).length;
  const pending = ps.filter((p) => p.run && !p.missingBase).length - summaries.size;

  const copySummary = async () => {
    const md = summaryMarkdown(selectedBranch ?? '', baseBranch, ps.map((p) => ({ p, ...summaries.get(pairKey(p)) })));
    try {
      await navigator.clipboard.writeText(md);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      window.prompt('Copy the summary', md);
    }
  };

  const flows = orderFlows(ps.map((p) => p.flow));

  return (
    <div className="branch-report">
      <div className="branch-report__toolbar">
        <label className="branch-report__field">
          <span className="branch-report__label">branch</span>
          <select
            className="branch-report__select"
            value={selectedBranch ?? ''}
            onChange={(e) => {
              onSelectBranch(e.target.value || null);
              if (e.target.value === baseBranch) setBase(null);
            }}
          >
            <option value="" disabled>— pick a branch —</option>
            {branches.map((b) => (
              <option key={b.name} value={b.name}>
                {branchLabel(b.name)} · {b.surfaces} surface{b.surfaces === 1 ? '' : 's'} · {relTime(b.lastRunAt)}
              </option>
            ))}
          </select>
        </label>
        <label className="branch-report__field">
          <span className="branch-report__label">compared with</span>
          <select className="branch-report__select" value={baseBranch} onChange={(e) => setBase(e.target.value || null)}>
            <option value={REFERENCE}>accepted baseline (checked in)</option>
            {branches
              .filter((b) => b.name !== selectedBranch)
              .map((b) => (
                <option key={b.name} value={b.name}>
                  latest run on {branchLabel(b.name)}
                </option>
              ))}
          </select>
        </label>
        {surfaces.loading ? <Spinner /> : null}
        <div className="branch-report__summary">
          {count('pass') > 0 && <span className="branch-report__pill branch-report__pill--green">{count('pass')} identical</span>}
          {count('changed') > 0 && <span className="branch-report__pill branch-report__pill--amber">{count('changed')} changed</span>}
          {count('failed') > 0 && <span className="branch-report__pill branch-report__pill--red">{count('failed')} failed</span>}
          {count('quarantined') > 0 && <span className="branch-report__pill branch-report__pill--amber">{count('quarantined')} not compared</span>}
          {pending > 0 && <span className="branch-report__pill">{pending} loading…</span>}
        </div>
        <button
          type="button"
          className="branch-report__btn"
          onClick={() => void copySummary()}
          disabled={ps.length === 0 || pending > 0}
          title="Markdown digest for a PR comment, Slack or an agent"
        >
          {copied ? 'copied ✓' : 'copy summary'}
        </button>
      </div>

      {surfaces.error ? <p className="branch-report__empty">{surfaces.error.message}</p> : null}
      {surfaces.loading ? <p className="branch-report__empty">loading runs…</p> : null}
      {!surfaces.loading && selectedBranch && ps.length === 0 ? (
        <p className="branch-report__empty">
          No runs found for <strong>{branchLabel(selectedBranch)}</strong>. Use ⇩ check all to pull CI results.
        </p>
      ) : null}

      {flow && selectedBranch ? (
        <FlowCompare
          flow={flow}
          baseBranch={baseBranch}
          pairings={ps.filter((p) => p.flow === flow)}
          summaries={summaries}
          tab={tab}
          app={app}
          onNavigate={(next) => open({ flow, ...next })}
          onBack={() => open({ flow: null })}
        />
      ) : (
        flows.map((f) => {
          const byApp = new Map(ps.filter((p) => p.flow === f).map((p) => [p.app, p]));
          return (
            <section key={f} className="branch-report__flow">
              <div className="branch-report__flow-head">
                <h2 className="branch-report__flow-title">
                  {flowLabel(f)}
                  <span className="branch-report__flow-sub">{f}</span>
                </h2>
                <button type="button" className="branch-report__btn" onClick={() => open({ flow: f, tab: 'screens' })}>
                  compare screens
                </button>
                <button type="button" className="branch-report__btn" onClick={() => open({ flow: f, tab: 'videos' })}>
                  watch videos
                </button>
              </div>
              <div className="branch-report__cells">
                {orderApps(byApp.keys()).map((a) => {
                  const p = byApp.get(a)!;
                  return <Cell key={a} p={p} st={summaries.get(pairKey(p))} onOpen={() => open({ flow: f, app: a, tab: 'detail' })} />;
                })}
              </div>
            </section>
          );
        })
      )}
    </div>
  );
}
