import { useState } from 'react';
import type { RetraceClient } from '../retraceClient';
import type { SuiteEvidence } from '../suiteTypes';
import { useAsync } from '../useAsync';
import ShotCompare from './ShotCompare';
import WireDiffTable from './WireDiffTable';
import CaptureBanner from './CaptureBanner';

export default function SuitePairEvidence({ client, evidence, onOpenFull }: {
  client: Pick<RetraceClient, 'pair' | 'pairShotUrl'>; evidence: SuiteEvidence; onOpenFull: () => void;
}) {
  const [plane, setPlane] = useState('screenshots');
  const [shot, setShot] = useState(0);
  const [mode, setMode] = useState<'originals' | 'diff' | 'overlay'>('originals');
  const { data, loading, error } = useAsync(() => client.pair(evidence.app, evidence.flow, evidence.runId, evidence.pairId!).then(r => r.summary), [client.pair, evidence.app, evidence.flow, evidence.runId, evidence.pairId]);
  if (loading) return <p role="status" className="suites__state">Loading recorded comparison…</p>;
  if (error || !data) return <p role="alert" className="suites__state">Unable to load comparison: {error?.message ?? 'No comparison returned'}</p>;
  if (data.verdict === 'quarantined') return <div className="suites__state"><p>Capture quarantined. These recordings were not compared.</p><button type="button" onClick={onOpenFull}>Inspect capture details</button></div>;
  const checkpoint = data.checkpoints[shot] ?? data.checkpoints[0];
  return <div className="suite-evidence">
    <CaptureBanner capture={data.capture} />
    <div className="suite-evidence__tabs"><div role="group" aria-label="Evidence view"><button type="button" aria-pressed={plane === 'screenshots'} onClick={() => setPlane('screenshots')}>Screenshots ({data.checkpoints.length})</button><button type="button" aria-pressed={plane === 'wire'} onClick={() => setPlane('wire')}>Wire traffic</button></div><button type="button" onClick={onOpenFull}>Full comparison ↗</button></div>
    <p className="suites__muted">Saved pair · {data.a.manifest.app || 'Reference'} → {data.b.manifest.app || 'Candidate'} · Imported report status remains separate from this recorded diff.</p>
    {plane === 'wire' ? <WireDiffTable sections={data.sections} selectedField={null} onSelectField={() => {}} onReveal={() => client.pair(evidence.app, evidence.flow, evidence.runId, evidence.pairId!).then(r => r.summary.sections)} unexpectedStatuses={data.unexpectedStatuses} /> : <>
      {data.geometryNote ? <p className="suite-review__reason">Pixel comparison skipped: {data.geometryNote}</p> : null}
      {checkpoint ? <><div className="suite-evidence__controls"><label>Checkpoint <select aria-label="Checkpoint" value={shot} onChange={e => setShot(Number(e.target.value))}>{data.checkpoints.map((cp, i) => <option value={i} key={cp.name}>{i + 1}. {cp.name}</option>)}</select></label><div role="group" aria-label="Image comparison mode">{(['originals', 'diff', 'overlay'] as const).map(m => <button type="button" key={m} aria-pressed={mode === m} onClick={() => setMode(m)}>{m === 'originals' ? 'Side by side' : m === 'diff' ? 'Diff' : 'Overlay'}</button>)}</div></div>
        <ShotCompare app={evidence.app} flow={evidence.flow} checkpoint={checkpoint} displayMode={mode} resolveShotUrl={(_a, _f, side, name) => client.pairShotUrl(evidence.app, evidence.flow, evidence.runId, evidence.pairId!, side as 'a' | 'b' | 'diff' | 'overlay', name)} />
      </> : <p className="suites__state">No screenshots were recorded for this comparison. Visual coverage is unavailable.</p>}
    </>}
  </div>;
}
