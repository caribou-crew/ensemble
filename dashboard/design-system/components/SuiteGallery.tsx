import { useState } from 'react';
import { Spinner } from '../primitives';
import { useAsync } from '../useAsync';
import type { RetraceClient } from '../retraceClient';
import type { SuiteEvidence, SuiteGalleryRow, SuiteGalleryTile, SuiteGalleryWire, SuitePlatform, SuiteStatus } from '../suiteTypes';
import './SuiteGallery.css';

export type GalleryClient = Pick<RetraceClient, 'suiteGallery' | 'suiteScreenUrl'> & Partial<Pick<RetraceClient, 'pairShotUrl'>>;
const platformNames: Record<SuitePlatform, string> = { web: 'Web', ios: 'iOS', android: 'Android' };
const statusNames: Record<SuiteStatus, string> = { pass: 'Passed', failed: 'Failed', incomplete: 'Incomplete', 'not-run': 'Not run' };
const planeNames = { pass: 'passed', failed: 'failed', incomplete: 'incomplete', 'not-run': 'not run', 'not-applicable': 'not applicable' } as const;

/** The explicit wire callout: counts only when a saved comparison supplied them. */
export function WireCallout({ wire }: { wire: SuiteGalleryWire }) {
  if (wire.represented && wire.counts) {
    const c = wire.counts;
    const moved = c.changed + c.missing + c.extra + c.moved + c.violations;
    return <div className={`gallery__wire ${moved ? 'gallery__wire--changed' : 'gallery__wire--clean'}`}>
      <strong>Wire diff</strong>
      <span>{c.paired} paired · {c.changed} changed · {c.missing} missing · {c.extra} extra{c.moved ? ` · ${c.moved} moved` : ''}{c.violations ? ` · ${c.violations} violations` : ''}</span>
      <span className="gallery__muted">runner: wire {planeNames[wire.plane]}</span>
    </div>;
  }
  return <div className="gallery__wire gallery__wire--absent">
    <strong>Wire diff not represented</strong>
    <span>{wire.note}</span>
    <span className="gallery__muted">runner: wire {planeNames[wire.plane]} (an assertion; no diff is shown)</span>
  </div>;
}

function Figure({ src, label, caption }: { src: string; label: string; caption?: string }) {
  return <figure className="gallery__figure">
    <a href={src} target="_blank" rel="noreferrer"><img src={src} alt={label} loading="lazy" /></a>
    <figcaption>{label}{caption ? <span className="gallery__muted"> · {caption}</span> : null}</figcaption>
  </figure>;
}

function Tile({ tile, suiteId, flowTitle, client, onOpenEvidence }: { tile: SuiteGalleryTile; suiteId: string; flowTitle: string; client: GalleryClient; onOpenEvidence: (e: SuiteEvidence) => void }) {
  const pair = tile.pair;
  const canShowPair = pair?.available && pair.checkpoint && client.pairShotUrl;
  return <div className={`gallery__tile gallery__tile--${tile.status}`} data-platform={tile.platform}>
    <header><h4>{platformNames[tile.platform]}</h4><span className={`suites__status suites__status--${tile.status}`}>{statusNames[tile.status] ?? 'Unknown'}</span></header>
    {tile.reason ? <p className="gallery__reason">{tile.reason}</p> : null}
    <div className="gallery__images">
      {canShowPair ? (['a', 'b', 'diff'] as const).map(side => {
        const name = side === 'a' ? 'Reference' : side === 'b' ? 'Candidate' : 'Diff';
        const cp = pair!.checkpoints?.find(c => c.name === pair!.checkpoint);
        return <Figure key={side} src={client.pairShotUrl!(pair!.app, pair!.flow, pair!.runId, pair!.pairId, side, pair!.checkpoint!)} label={`${platformNames[tile.platform]} ${name.toLowerCase()} — ${flowTitle}`}
          caption={side === 'diff' && cp ? `${cp.verdict}${cp.diffPct ? ` ${cp.diffPct.toFixed(1)}%` : ''}` : side === 'b' ? pair!.checkpoint : undefined} />;
      }) : null}
      {pair && !pair.available ? <p className="gallery__missing" role="alert">Linked comparison unavailable{pair.error ? `: ${pair.error}` : ''}.</p> : null}
      {tile.screens.map(s => <Figure key={s.sha256} src={client.suiteScreenUrl(suiteId, tile.attemptId!, s.sha256)} label={`${platformNames[tile.platform]} ${s.label} — ${flowTitle}`} caption={s.label} />)}
      {!canShowPair && !tile.screens.length && !(pair && !pair.available) ? <p className="gallery__missing">{tile.attemptId ? 'No screenshots attached to this result.' : 'No result imported for this lane.'}</p> : null}
    </div>
    <WireCallout wire={tile.wire} />
    {tile.evidence ? <button type="button" onClick={() => onOpenEvidence(tile.evidence!)}>Open {tile.evidence.pairId ? 'comparison' : 'run'}</button> : null}
  </div>;
}

const filters = { all: 'All flows', attention: 'Needs attention', 'wire-absent': 'Wire not represented', 'wire-changed': 'Wire changed' } as const;
type Filter = keyof typeof filters;
const needsAttention = (row: SuiteGalleryRow) => row.tiles.some(t => t.status !== 'pass');
const wireAbsent = (row: SuiteGalleryRow) => row.tiles.some(t => t.status !== 'not-run' && !t.wire.represented);
const wireChanged = (row: SuiteGalleryRow) => row.tiles.some(t => t.wire.counts && (t.wire.counts.changed + t.wire.counts.missing + t.wire.counts.extra + t.wire.counts.moved + t.wire.counts.violations) > 0);
const rowMatches: Record<Filter, (r: SuiteGalleryRow) => boolean> = { all: () => true, attention: needsAttention, 'wire-absent': wireAbsent, 'wire-changed': wireChanged };

export interface SuiteGalleryProps { client: GalleryClient; suiteId: string; buildId: string; onOpenEvidence: (e: SuiteEvidence) => void }

/** Whole-build review board: every flow across every platform lane at a glance. */
export default function SuiteGallery({ client, suiteId, buildId, onOpenEvidence }: SuiteGalleryProps) {
  const { data, error, loading } = useAsync(() => client.suiteGallery(suiteId, buildId), [client, suiteId, buildId]);
  const [filter, setFilter] = useState<Filter>('all');
  const [search, setSearch] = useState('');
  if (loading) return <p className="suites__state" role="status"><Spinner /> Loading gallery…</p>;
  if (error || !data || !Array.isArray(data.rows)) return <section className="suites__state" role="alert"><h2>Unable to load the gallery</h2><p>{error?.message ?? 'Invalid gallery response'}</p></section>;
  const q = search.trim().toLowerCase();
  const rows = data.rows.filter(r => rowMatches[filter](r) && (!q || `${r.feature.title} ${r.flow.title}`.toLowerCase().includes(q)));
  let lastFeature = '';
  return <section className="gallery" aria-label="Build gallery">
    <aside className="gallery__notice" role="note">
      Shots come from different runs and media: web pairs are reference vs candidate; other platforms show the final screen only. Runner statuses are assertions; nothing here is visually accepted.
    </aside>
    <div className="gallery__filters">
      <label>Show <select aria-label="Gallery filter" value={filter} onChange={e => setFilter(e.target.value as Filter)}>{(Object.keys(filters) as Filter[]).map(k => <option key={k} value={k}>{filters[k]}</option>)}</select></label>
      <input aria-label="Search gallery flows" placeholder="Search flows…" value={search} onChange={e => setSearch(e.target.value)} />
      <span className="gallery__muted">{rows.length} of {data.rows.length} flows</span>
    </div>
    {!rows.length ? <p className="suites__state">No flows match this filter.</p> : rows.map(row => {
      const heading = row.feature.id !== lastFeature ? row.feature.title : null;
      lastFeature = row.feature.id;
      return <div key={`${row.feature.id}/${row.flow.id}`}>
        {heading ? <h3 className="gallery__feature">{heading}</h3> : null}
        <article className="gallery__row" aria-label={row.flow.title}>
          <h4 className="gallery__flow">{row.flow.title}</h4>
          <div className="gallery__tiles">{row.tiles.map(t => <Tile key={t.platform} tile={t} suiteId={data.suiteId} flowTitle={row.flow.title} client={client} onOpenEvidence={onOpenEvidence} />)}</div>
        </article>
      </div>;
    })}
  </section>;
}
