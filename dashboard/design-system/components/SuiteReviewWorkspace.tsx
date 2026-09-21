import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import type { RetraceClient } from '../retraceClient';
import type { SuiteBuild, SuiteEvidence, SuiteSelection } from '../suiteTypes';
import SuitePairEvidence from './SuitePairEvidence';

export type ReviewClient = Pick<RetraceClient, 'suites'> & Partial<Pick<RetraceClient, 'pair' | 'pairShotUrl' | 'suiteGallery' | 'suiteScreenUrl'>>;
const names = { web: 'Web', ios: 'iOS', android: 'Android' };
const labels = { pass: 'Passed', failed: 'Failed', incomplete: 'Incomplete', 'not-run': 'Not run', 'not-applicable': 'Not applicable' };
export default function SuiteReviewWorkspace({ build, client, selection, onSelect, onOpenEvidence }: {
  build: SuiteBuild; client: ReviewClient; selection: SuiteSelection;
  onSelect: (next: SuiteSelection) => void; onOpenEvidence: (e: SuiteEvidence) => void;
}) {
  const detailRef = useRef<HTMLDivElement>(null);
  const rowRef = useRef<HTMLButtonElement>(null);
  const [search, setSearch] = useState(selection.reviewSearch ?? '');
  const [filter, setFilter] = useState(selection.reviewFilter ?? (selection.flowId ? 'all' : 'unresolved'));
  useEffect(() => {
    setSearch(selection.reviewSearch ?? '');
    setFilter(selection.reviewFilter ?? (selection.flowId ? 'all' : 'unresolved'));
  }, [selection.reviewSearch, selection.reviewFilter]);
  const all = build.features.flatMap(feature => feature.flows.flatMap(flow => flow.platforms.map(cell => ({ feature, flow, cell }))));
  const rows = all.filter(({ feature, flow, cell }) =>
    (!selection.featureId || feature.id === selection.featureId) &&
    (!selection.platform || cell.platform === selection.platform) &&
    (filter === 'all' || (filter === 'unresolved' ? cell.status !== 'pass' : cell.status === filter)) &&
    `${feature.title} ${flow.title}`.toLowerCase().includes(search.toLowerCase()),
  );
  const requested = rows.findIndex(r => r.flow.id === selection.flowId && r.cell.platform === selection.reviewPlatform);
  const index = requested >= 0 ? requested : 0;
  const active = rows[index];
  function selectRow(i: number) {
    const row = rows[i];
    if (row) onSelect({ ...selection, reviewFilter: filter, reviewSearch: search, flowId: row.flow.id, reviewPlatform: row.cell.platform });
  }
  function keyDown(e: KeyboardEvent) {
    const target = e.target as HTMLElement;
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || target.closest('input, select, textarea, [contenteditable=true]')) return;
    if (e.key === 'j' || e.key === 'k') { e.preventDefault(); selectRow(index + (e.key === 'j' ? 1 : -1)); }
  }
  useEffect(() => {
    if (detailRef.current) detailRef.current.scrollTop = 0;
    if (document.activeElement?.closest('.suite-review__rows')) rowRef.current?.focus({ preventScroll: true });
    rowRef.current?.scrollIntoView?.({ block: 'nearest' });
  }, [active?.flow.id, active?.cell.platform]);
  const latest = active?.cell.latest;
  const evidence = latest?.evidence;
  return <section className="suite-review" aria-label="Review workspace" onKeyDown={keyDown}>
    <aside className="suite-review__queue" aria-label="Flow queue">
      <div className="suite-review__filters">
        <div className="suites__section-heading"><h2>Flows</h2><span className="suites__muted">{rows.length} of {all.length}</span></div>
        <input aria-label="Search flows" placeholder="Search flows…" value={search} onChange={e => { setSearch(e.target.value); onSelect({ ...selection, reviewFilter: filter, reviewSearch: e.target.value }); }} />
        <select aria-label="Result filter" value={filter} onChange={e => { setFilter(e.target.value); onSelect({ ...selection, reviewFilter: e.target.value, reviewSearch: search }); }}>
          <option value="unresolved">Needs attention</option><option value="all">All results</option><option value="failed">Failed</option><option value="incomplete">Incomplete</option><option value="not-run">Not run</option><option value="pass">Passed</option>
        </select>
        <select aria-label="Feature filter" value={selection.featureId ?? ''} onChange={e => onSelect({ ...selection, reviewFilter: filter, reviewSearch: search, featureId: e.target.value || undefined, flowId: undefined, reviewPlatform: undefined })}>
          <option value="">All features</option>{build.features.map(f => <option key={f.id} value={f.id}>{f.title}</option>)}
        </select>
      </div>
      <div className="suite-review__rows">{rows.map((row, i) => <button type="button" className="suite-review__row" key={`${row.feature.id}/${row.flow.id}/${row.cell.platform}`} ref={i === index ? rowRef : undefined} aria-pressed={i === index} onClick={() => selectRow(i)}>
        <span className="suite-review__feature">{row.feature.title} · {names[row.cell.platform]}</span>
        <strong>{row.flow.title}</strong><span className={`suites__status suites__status--${row.cell.status}`}>{labels[row.cell.status]}</span>
      </button>)}</div>
    </aside>
    <div className="suite-review__detail" ref={detailRef}>
      <div className="suite-review__toolbar"><span>{active ? `${index + 1} of ${rows.length}` : '0 flows'} <span className="suites__muted">· J / K to navigate</span></span><div><button type="button" disabled={index === 0 || !active} onClick={() => selectRow(index - 1)}>← Previous flow</button><button type="button" disabled={index >= rows.length - 1 || !active} onClick={() => selectRow(index + 1)}>Next flow →</button></div></div>
      {!active ? <p className="suites__state">No flows match. Change the filters to see other results.</p> : <div key={`${active.feature.id}/${active.flow.id}/${active.cell.platform}`}>
        <header className="suite-review__flow-heading"><p className="suites__eyebrow">{active.feature.title} · {names[active.cell.platform]}</p><h2>{active.flow.title}</h2>
          <div className="suites__planes">{(['functional', 'wire', 'visual'] as const).map(plane => <span key={plane}>{plane}<span className={`suites__status suites__status--${latest?.planes[plane] ?? 'not-run'}`}>{labels[latest?.planes[plane] ?? 'not-run']}</span></span>)}</div>
          {latest?.reason ? <details className="suite-review__notes"><summary>Runner report notes</summary><p className="suite-review__reason">{latest.reason}</p></details> : null}
        </header>
        {evidence?.pairId && client.pair && client.pairShotUrl ? <SuitePairEvidence client={{ pair: client.pair, pairShotUrl: client.pairShotUrl }} evidence={evidence} onOpenFull={() => onOpenEvidence(evidence)} /> : <div className="suites__state">{!latest ? 'No runner result. This expected flow has not run.' : !evidence ? 'No linked comparison evidence. The reported status alone cannot show what changed.' : <><p>This run is pinned, but its comparison uses the current reference and policy.</p><button type="button" onClick={() => onOpenEvidence(evidence)}>Open {evidence.pairId ? 'pair comparison' : 'run evidence'}</button></>}</div>}
        <details className="suite-review__attempts"><summary>Report details & attempt history ({active.cell.history.length})</summary><p>Required: {active.cell.requiredPlanes.join(', ')}</p>{[...(latest ? [latest] : []), ...active.cell.history.filter(a => a.attemptId !== latest?.attemptId)].map(a => <div key={a.attemptId}><code>{a.attemptId}</code> · {a.finishedAt}<p>{Object.entries(a.planes).map(([p, v]) => `${p}: ${labels[v]}`).join(' · ')}</p>{a.evidence ? <button type="button" onClick={() => onOpenEvidence(a.evidence!)}>Open attempt evidence</button> : null}</div>)}</details>
      </div>}
    </div>
  </section>;
}
