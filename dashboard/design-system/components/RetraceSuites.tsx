import { useState } from 'react';
import { Spinner } from '../primitives';
import SuiteReviewWorkspace, { type ReviewClient } from './SuiteReviewWorkspace';
import { useAsync } from '../useAsync';
import type { SuiteAttemptResult, SuiteCounts, SuiteEvidence, SuitePlaneStatus, SuiteSelection, SuiteFlowCell } from '../suiteTypes';
import './RetraceSuites.css';

const platformNames = { web: 'Web', ios: 'iOS', android: 'Android' };
const planes = ['functional', 'wire', 'visual'] as const;
const statusNames: Record<SuitePlaneStatus, string> = {
  pass: 'Passed', failed: 'Failed', incomplete: 'Incomplete', 'not-run': 'Not run', 'not-applicable': 'Not applicable',
};
function Status({ value }: { value: SuitePlaneStatus }) {
  return <span className={`suites__status suites__status--${value}`}>{statusNames[value] ?? 'Unknown'}</span>;
}
function Counts({ counts, compact = false }: { counts: SuiteCounts; compact?: boolean }) {
  return <div className={compact ? 'suites__counts suites__counts--compact' : 'suites__counts'}>
    <strong>{counts.passed}<span> / {counts.total} passed</span></strong>
    {!compact || counts.failed > 0 ? <span className="suites__count-failed">{counts.failed} failed</span> : null}
    {!compact || counts.incomplete > 0 ? <span>{counts.incomplete} incomplete</span> : null}
    {!compact || counts.notRun > 0 ? <span>{counts.notRun} not run</span> : null}
    {counts.total === 0 ? <span>No applicable flows</span> : null}
  </div>;
}
function PlaneCoverage({ cells }: { cells: SuiteFlowCell[] }) {
  return <div className="suites__plane-coverage" aria-label="Required plane coverage">{planes.map(plane => {
    const expected = cells.filter(cell => cell.requiredPlanes.includes(plane));
    const passed = expected.filter(cell => cell.latest?.planes[plane] === 'pass').length;
    return <div key={plane}><span>{plane}</span><strong>{passed} / {expected.length}</strong><span>required cells passed</span></div>;
  })}</div>;
}
function Attempt({ attempt, onOpenEvidence }: { attempt: SuiteAttemptResult; onOpenEvidence: (e: SuiteEvidence) => void }) {
  return <div className="suites__attempt">
    <div><code>{attempt.attemptId}</code> <time dateTime={attempt.finishedAt}>{attempt.finishedAt}</time></div>
    <div className="suites__planes">{planes.map(plane => <span key={plane}>{plane} <Status value={attempt.planes[plane]} /></span>)}</div>
    {attempt.reason ? <p>{attempt.reason}</p> : null}
    {attempt.evidence ? <button type="button" onClick={() => onOpenEvidence(attempt.evidence!)}>Open {attempt.evidence.pairId ? 'pair comparison' : 'run evidence'}</button> : <span className="suites__muted">No linked comparison evidence</span>}
  </div>;
}

export interface RetraceSuitesProps {
  client: ReviewClient;
  selection: SuiteSelection;
  onSelect: (selection: SuiteSelection) => void;
  onOpenEvidence: (evidence: SuiteEvidence) => void;
}
export default function RetraceSuites({ client, selection, onSelect, onOpenEvidence }: RetraceSuitesProps) {
  const [revision, setRevision] = useState(0);
  const { data, error, loading } = useAsync(() => client.suites(), [client, revision]);
  if (loading) return <p className="suites__state" role="status"><Spinner /> Loading comparison suites…</p>;
  if (error || !data || !Array.isArray(data.suites)) return <section className="suites__state" role="alert"><h2>Unable to load suites</h2><p>{error?.message ?? 'Invalid suite response'}</p><p>Coverage is unknown until the configured inventory and reports can be read.</p><button type="button" onClick={() => setRevision(v => v + 1)}>Try again</button></section>;
  if (!data.suites.length) return <section className="suites__state"><p className="suites__eyebrow">Commit comparisons</p><h2>No suites configured yet</h2><p>Define expected features, flows, and platforms in <code>retrace.suites.json</code>, then import a runner report.</p><code>retrace suite import --file report.json</code><p>Missing results remain not run. Reports join only when their source, baseline, and policy identities match.</p></section>;
  const suite = selection.suiteId ? data.suites.find(s => s.id === selection.suiteId) : data.suites[0];
  const build = selection.buildId ? suite?.builds.find(b => b.id === selection.buildId) : suite?.builds[0];
  const feature = selection.featureId ? build?.features.find(f => f.id === selection.featureId) : undefined;
  const select = (patch: Partial<SuiteSelection>) => onSelect({ ...selection, suiteId: suite?.id, buildId: build?.id, ...patch });
  return <section className="suites">
    <header className="suites__heading"><div><p className="suites__eyebrow">Commit comparisons</p><h1>Comparison suites</h1><p>Expected coverage across features and platforms.</p></div><button type="button" onClick={() => setRevision(v => v + 1)}>Refresh suites</button></header>
    <p className="suites__provenance">External runner reports · statuses are runner assertions. Open linked evidence to inspect recorded comparisons.</p>
    <details className="suites__sources"><summary>Choose suite or source revision</summary><div className="suites__layout">
      <aside className="suites__sidebar" aria-label="Suites and builds">
        <label className="suites__label" htmlFor="suite-picker">Suite</label>
        <select id="suite-picker" value={suite?.id ?? ''} onChange={e => onSelect({ suiteId: e.target.value })}>
          {!suite ? <option value="">Suite unavailable</option> : null}
          {data.suites.map(s => <option key={s.id} value={s.id}>{s.title}</option>)}
        </select>
        {suite ? <><p className="suites__muted">Inventory {suite.version}</p><h2>Source revisions</h2><div className="suites__builds">{suite.builds.map(b => <button key={b.id} className="suites__build" type="button" aria-pressed={build?.id === b.id} onClick={() => onSelect({ suiteId: suite.id, buildId: b.id })}>
          <span><code>{b.git.sha.slice(0, 10)}</code>{b.git.dirty ? <span className="suites__dirty">Dirty snapshot</span> : null}</span>
          <span>{b.git.branch || 'Detached revision'}</span>
          {b.git.dirty ? <span className="suites__muted suites__truncate" title={b.workspaceId}>{b.workspaceId}</span> : null}
          <Counts counts={b.counts} compact />
          <span className="suites__muted suites__truncate" title={`Baseline ${b.baselineId} · policy ${b.policyId}`}>Baseline {b.baselineId} · policy {b.policyId}</span>
        </button>)}</div></> : null}
      </aside>
    </div></details>
      <div className="suites__content">
        {!suite ? <p role="alert">The selected suite is unavailable. Choose a suite from the list.</p> : !build ? <div className="suites__state"><h2>{selection.buildId ? 'Build unavailable' : 'No reports imported yet'}</h2><p>{selection.buildId ? 'Choose an available source revision.' : `${suite.title} is configured. Import a runner report to begin comparing expected coverage.`}</p><code>retrace suite import --file report.json</code></div> : <>
          <header className="suites__build-heading"><p className="suites__eyebrow">{suite.title}</p><h2><code>{build.git.sha.slice(0, 10)}</code> <span>{build.git.branch || 'Detached revision'}</span></h2>
            {build.git.dirty ? <p className="suites__dirty">Dirty source snapshot</p> : null}
            <details className="suites__provenance-details"><summary>Source, baseline, and policy identity</summary><dl className="suites__identity"><div><dt>Source</dt><dd><code>{build.git.sha}</code></dd></div>{build.git.dirty ? <div><dt>Snapshot</dt><dd><code>{build.workspaceId}</code></dd></div> : null}<div><dt>Baseline</dt><dd>{build.baselineId}</dd></div><div><dt>Policy</dt><dd>{build.policyId}</dd></div><div><dt>Updated</dt><dd><time dateTime={build.updatedAt}>{build.updatedAt}</time></dd></div></dl></details>
            <Counts counts={build.counts} />
            <PlaneCoverage cells={build.features.flatMap(f => f.flows.flatMap(flow => flow.platforms))} />
            <p className="suites__muted">Counts represent expected flow × platform cells, including missing results.</p>
          </header>
          <div className="suites__platforms" aria-label="Platform coverage">{suite.platforms.map(platform => {
            const summary = build.platforms.find(p => p.platform === platform);
            return <button type="button" key={platform} aria-pressed={selection.platform === platform} onClick={() => select({ platform: selection.platform === platform ? undefined : platform })}><h3>{platformNames[platform]}</h3>{summary ? <Counts counts={summary.counts} compact /> : <span>Coverage unavailable</span>}</button>;
          })}</div>
          <SuiteReviewWorkspace key={`${suite.id}/${build.id}`} build={build} client={client} selection={{ ...selection, suiteId: suite.id, buildId: build.id }} onSelect={onSelect} onOpenEvidence={onOpenEvidence} />
          <details className="suites__overview"><summary>Coverage matrix & all report details</summary><section aria-label="Feature coverage"><div className="suites__section-heading"><h2>Feature coverage</h2><span className="suites__muted">Select a cell to inspect its flows</span></div><div className="suites__table-scroll"><table className="suites__matrix"><thead><tr><th scope="col">Feature</th>{suite.platforms.map(p => <th scope="col" key={p}>{platformNames[p]}</th>)}</tr></thead><tbody>{build.features.map(f => <tr key={f.id}><th scope="row"><button type="button" aria-pressed={feature?.id === f.id} onClick={() => select({ featureId: f.id, platform: undefined })}>{f.title}</button></th>{suite.platforms.map(p => { const cell = f.platforms.find(c => c.platform === p); return <td key={p}>{cell && cell.counts.total > 0 ? <button type="button" aria-label={`${f.title}, ${platformNames[p]}: ${cell.counts.passed} of ${cell.counts.total} passed, ${cell.counts.failed} failed, ${cell.counts.incomplete} incomplete, ${cell.counts.notRun} not run`} aria-pressed={feature?.id === f.id && selection.platform === p} onClick={() => select({ featureId: f.id, platform: p })}><Counts counts={cell.counts} compact /></button> : <span className="suites__muted">Not applicable</span>}</td>; })}</tr>)}</tbody></table></div></section>
          <section aria-label="Flow details" tabIndex={-1}><div className="suites__section-heading"><h2>{feature?.title ?? 'Flow details'}{selection.platform ? ` · ${platformNames[selection.platform]}` : ''}</h2>{selection.featureId || selection.platform ? <button type="button" onClick={() => select({ featureId: undefined, platform: undefined })}>Clear selection</button> : null}</div>
            {!selection.featureId && !selection.platform ? <p className="suites__drilldown-prompt">Select a feature or platform above to inspect flow results, required planes, and attempt history.</p> : selection.featureId && !feature ? <p role="alert">The selected feature is unavailable.</p> : (feature ? [feature] : build.features).map(f => <div key={f.id}>{!feature ? <h3>{f.title}</h3> : null}{f.flows.map(flow => {
              const cells = flow.platforms.filter(c => !selection.platform || c.platform === selection.platform);
              if (!cells.length) return null;
              return <article className="suites__flow" key={flow.id}><h3>{flow.title}</h3>{cells.map(cell => <div className="suites__flow-cell" key={cell.platform}>
                <div className="suites__section-heading"><h4>{platformNames[cell.platform]}</h4><Status value={cell.status} /></div>
                <p className="suites__muted">Required: {cell.requiredPlanes.join(', ')}</p>
                {cell.latest ? <Attempt attempt={cell.latest} onOpenEvidence={onOpenEvidence} /> : <p>No runner result. This expected flow has not run.</p>}
                {cell.history.length ? <details className="suites__history"><summary>Attempt history ({cell.history.length})</summary><p className="suites__muted">All imported attempts, including earlier failures. Latest finished result determines coverage.</p>{cell.history.map(a => <Attempt key={a.attemptId} attempt={a} onOpenEvidence={onOpenEvidence} />)}</details> : null}
              </div>)}</article>;
            })}</div>)}
          </section></details>
        </>}
      </div>
  </section>;
}


/** Run evidence pins the candidate only; its comparison is recomputed today. */
export function SuiteRunEvidenceNotice() {
  return <aside className="suites__run-provenance" role="note" aria-label="Run comparison provenance">
    <strong>This run is pinned.</strong>{' '}Its comparison uses the current reference and policy; it does not reproduce the imported suite verdict. For historical baseline evidence, link a saved pair made from retained, concrete run IDs.
  </aside>;
}
