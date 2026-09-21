import { Fragment, useState } from 'react';
import { Spinner } from '../primitives';
import { useAsync } from '../useAsync';
import type { RetraceClient } from '../retraceClient';
import type { SuiteEvidence, SuiteGallery as Gallery, SuiteGalleryRow, SuiteGalleryTile, SuiteGallerySource, SuiteGalleryWireCounts, SuitePlatform } from '../suiteTypes';
import './SuiteGallery.css';

export type GalleryClient = Pick<RetraceClient, 'suiteGallery' | 'suiteScreenUrl'> & Partial<Pick<RetraceClient, 'pairShotUrl'>>;
const names: Record<SuitePlatform, string> = { web: 'Web', ios: 'iOS', android: 'Android' };
const statusLabel = { pass: 'Passed', failed: 'Failed', incomplete: 'Incomplete', 'not-run': 'Not run' } as const;
const planeLabel = { pass: 'passed', failed: 'failed', incomplete: 'incomplete', 'not-run': 'not run', 'not-applicable': 'not applicable' } as const;
const nonNative = (p: SuitePlatform) => p === 'web';

/** Differences Retrace counted between the paired network exchanges. */
export const wireDiffCount = (c: SuiteGalleryWireCounts) => c.changed + c.missing + c.extra + c.moved + c.violations;
/** Requests that appeared, vanished or broke a rule: the differences most likely to be real problems. */
export const wireStructural = (c: SuiteGalleryWireCounts) => c.missing + c.extra + c.violations;
const wireOf = (t: SuiteGalleryTile) => (t.wire.represented && t.wire.counts ? t.wire.counts : undefined);
const short = (s: SuiteGallerySource) => `${s.sha.slice(0, 7)}${s.dirty ? ' dirty' : ''}`;
const day = (iso: string) => (iso ? iso.slice(0, 10) : '');

function Thumb({ src, alt, note }: { src: string; alt: string; note?: string }) {
  return <a className="gallery__thumb" href={src} target="_blank" rel="noreferrer" title={note ? `${alt} · ${note}` : alt}><img src={src} alt={alt} loading="lazy" />{note ? <span>{note}</span> : null}</a>;
}
const Empty = ({ why }: { why: string }) => <span className="gallery__empty" title={why}>—</span>;

function pairImages(t: SuiteGalleryTile, flow: string, client: GalleryClient) {
  const p = t.pair;
  if (!p?.available || !p.checkpoint || !client.pairShotUrl) return undefined;
  const cp = p.checkpoints?.find(c => c.name === p.checkpoint);
  const url = (side: 'a' | 'b' | 'diff') => client.pairShotUrl!(p.app, p.flow, p.runId, p.pairId, side, p.checkpoint!);
  return {
    a: <Thumb src={url('a')} alt={`${names[t.platform]} reference — ${flow}`} />,
    b: <Thumb src={url('b')} alt={`${names[t.platform]} candidate — ${flow}`} />,
    diff: <Thumb src={url('diff')} alt={`${names[t.platform]} diff — ${flow}`} note={cp ? `${cp.verdict}${cp.diffPct ? ` ${cp.diffPct.toFixed(1)}%` : ''}` : undefined} />,
  };
}

function Dots({ tiles }: { tiles: SuiteGalleryTile[] }) {
  return <span className="gallery__dots" aria-label="Result per platform">{tiles.map(t => <span key={t.platform} className={`gallery__dot gallery__dot--${t.status}`} title={`${names[t.platform]}: ${statusLabel[t.status] ?? t.status}${t.reason ? ` — ${t.reason}` : ''}`}>{names[t.platform][0]}</span>)}</span>;
}

function NetworkChip({ row, onOpenEvidence }: { row: SuiteGalleryRow; onOpenEvidence: (e: SuiteEvidence) => void }) {
  const t = row.tiles.find(x => wireOf(x));
  if (!t) return <Empty why="No saved network comparison for this flow" />;
  const c = wireOf(t)!, n = wireDiffCount(c);
  const label = n ? `${c.changed} changed${c.missing ? ` · ${c.missing} missing` : ''}${c.extra ? ` · ${c.extra} extra` : ''}${c.moved ? ` · ${c.moved} moved` : ''}` : 'no diff';
  return <button type="button" className={`gallery__chip ${wireStructural(c) ? 'gallery__chip--diff' : n ? 'gallery__chip--minor' : 'gallery__chip--clean'}`} title={`${c.paired} paired exchanges. Runner asserted wire ${planeLabel[t.wire.plane]}. Open the comparison.`}
    onClick={() => t.evidence && onOpenEvidence(t.evidence)}>{label}</button>;
}

function LaneHeader({ gallery }: { gallery: Gallery }) {
  return <div className="gallery__lanes" aria-label="Source revision per platform">{(gallery.lanes ?? []).map(l => {
    const [latest, ...older] = l.sources;
    return <span key={l.platform} className="gallery__lane" title={l.sources.map(s => `${short(s)} · ${s.branch || 'detached'} · ${s.baselineId} · ${s.finishedAt}`).join('\n')}>
      <strong>{names[l.platform]}</strong>{' '}
      {latest ? <><code>{short(latest)}</code> <span className="gallery__muted">{day(latest.finishedAt)}{older.length ? ` · +${older.length} other revision${older.length > 1 ? 's' : ''}` : ''}</span></> : <span className="gallery__muted">no results</span>}
    </span>;
  })}</div>;
}

const filters = { all: 'All flows', attention: 'Needs attention', visual: 'Visual differences', network: 'Network differences' } as const;
type Filter = keyof typeof filters;
const match: Record<Filter, (r: SuiteGalleryRow) => boolean> = {
  all: () => true,
  attention: r => r.tiles.some(t => t.status !== 'pass'),
  visual: r => r.tiles.some(t => t.pair?.available && t.pair.verdict && t.pair.verdict !== 'ok'),
  network: r => r.tiles.some(t => { const c = wireOf(t); return !!c && wireDiffCount(c) > 0; }),
};

function Screens({ gallery, rows, client, onOpenEvidence }: { gallery: Gallery; rows: SuiteGalleryRow[]; client: GalleryClient; onOpenEvidence: (e: SuiteEvidence) => void }) {
  const platforms = gallery.platforms;
  let last = '';
  return <table className="gallery__table">
    <thead><tr><th>Flow</th>{platforms.map(p => nonNative(p) ? <Fragment key={p}><th>{names[p]} reference</th><th>{names[p]} candidate</th><th>{names[p]} diff</th></Fragment> : <th key={p}>{names[p]}</th>)}<th>Network</th></tr></thead>
    <tbody>{rows.map(row => {
      const head = row.feature.id !== last ? row.feature.title : null;
      last = row.feature.id;
      return <Fragment key={`${row.feature.id}/${row.flow.id}`}>
        {head ? <tr className="gallery__group"><th colSpan={1 + platforms.reduce((n, p) => n + (nonNative(p) ? 3 : 1), 0) + 1}>{head}</th></tr> : null}
        <tr>
          <th scope="row" className="gallery__flow"><span>{row.flow.title}</span><Dots tiles={row.tiles} /></th>
          {platforms.map(p => {
            const t = row.tiles.find(x => x.platform === p);
            const pair = t ? pairImages(t, row.flow.title, client) : undefined;
            if (nonNative(p)) {
              const why = !t || t.status === 'not-run' && !t.attemptId ? 'No result imported for this lane' : t.pair && !t.pair.available ? `Linked comparison unavailable${t.pair.error ? `: ${t.pair.error}` : ''}` : 'No comparison images';
              return pair ? <Fragment key={p}><td>{pair.a}</td><td>{pair.b}</td><td>{pair.diff}</td></Fragment> : <td key={p} colSpan={3}><Empty why={why} />{t?.pair && !t.pair.available ? <span className="gallery__warn" role="alert"> comparison unavailable</span> : null}</td>;
            }
            return <td key={p}>{t?.screens.length ? t.screens.map(s => <Thumb key={s.sha256} src={client.suiteScreenUrl(gallery.suiteId, t.attemptId!, s.sha256)} alt={`${names[p]} ${s.label} — ${row.flow.title}`} />) : <Empty why={t?.attemptId ? 'No screenshot attached' : 'No result imported for this lane'} />}</td>;
          })}
          <td><NetworkChip row={row} onOpenEvidence={onOpenEvidence} /></td>
        </tr>
      </Fragment>;
    })}</tbody>
  </table>;
}

function Network({ gallery, rows, onOpenEvidence }: { gallery: Gallery; rows: SuiteGalleryRow[]; onOpenEvidence: (e: SuiteEvidence) => void }) {
  const compared = rows.flatMap(r => r.tiles.filter(t => wireOf(t)).map(t => ({ row: r, t, c: wireOf(t)! })));
  const withDiff = compared.filter(x => wireDiffCount(x.c) > 0).sort((a, b) => wireStructural(b.c) - wireStructural(a.c) || wireDiffCount(b.c) - wireDiffCount(a.c));
  const structural = withDiff.filter(x => wireStructural(x.c) > 0).length;
  const broken = rows.flatMap(r => r.tiles.filter(t => t.pair && !t.pair.available).map(t => ({ row: r, t })));
  const missing = rows.flatMap(r => r.tiles.filter(t => t.attemptId && !wireOf(t) && !(t.pair && !t.pair.available)).map(t => ({ row: r, t })));
  const laneNames = (gallery.platforms.filter(p => !compared.some(x => x.t.platform === p))).map(p => names[p]);
  return <section className="gallery__network" aria-label="Network differences">
    <p className="gallery__muted">{compared.length} saved network comparison{compared.length === 1 ? '' : 's'}: {structural} with missing or extra requests or rule violations (listed first), {withDiff.length - structural} with only changed or moved exchanges. Counts are Retrace's and can include differences a policy already approves, such as header changes, so read them next to what the runner asserted.
      {laneNames.length ? ` No network comparison exists for: ${laneNames.join(', ')}.` : ''}</p>
    {broken.length ? <div role="alert" className="gallery__warn">{broken.length} linked comparison{broken.length === 1 ? ' is' : 's are'} unreadable: {broken.map(b => `${b.row.flow.title} (${names[b.t.platform]})`).join('; ')}.</div> : null}
    {withDiff.length ? <table className="gallery__table gallery__table--net">
      <thead><tr><th>Flow</th><th>Lane</th><th>Paired</th><th>Changed</th><th>Missing</th><th>Extra</th><th>Moved</th><th>Violations</th><th>Runner asserted</th><th /></tr></thead>
      <tbody>{withDiff.map(({ row, t, c }) => <tr key={`${row.flow.id}/${t.platform}`}>
        <th scope="row" className="gallery__flow">{row.flow.title}</th><td>{names[t.platform]}</td><td>{c.paired}</td><td>{c.changed}</td><td>{c.missing}</td><td>{c.extra}</td><td>{c.moved}</td><td>{c.violations}</td>
        <td>{planeLabel[t.wire.plane]}</td><td>{t.evidence ? <button type="button" onClick={() => onOpenEvidence(t.evidence!)}>Open comparison</button> : null}</td></tr>)}</tbody>
    </table> : <p className="suites__state">No network differences in the saved comparisons shown.</p>}
    {missing.length ? <details className="gallery__nowire"><summary>{missing.length} result{missing.length === 1 ? '' : 's'} without a network comparison</summary>
      <ul>{missing.map(m => <li key={`${m.row.flow.id}/${m.t.platform}`}>{m.row.flow.title} · {names[m.t.platform]}{m.t.wire.note ? ` — ${m.t.wire.note}` : ''}</li>)}</ul></details> : null}
  </section>;
}

export interface SuiteGalleryProps { client: GalleryClient; suiteId: string; buildId?: string; onOpenEvidence: (e: SuiteEvidence) => void }

/** Scrollable review board: every flow across platforms, plus a separate network section. */
export default function SuiteGallery({ client, suiteId, buildId, onOpenEvidence }: SuiteGalleryProps) {
  const [scope, setScope] = useState<'latest' | 'build'>('latest');
  const [tab, setTab] = useState<'screens' | 'network'>('screens');
  const [filter, setFilter] = useState<Filter>('all');
  const [search, setSearch] = useState('');
  const wanted = scope === 'build' && buildId ? buildId : undefined;
  const { data, error, loading } = useAsync(() => client.suiteGallery(suiteId, wanted), [client, suiteId, wanted]);
  if (loading) return <p className="suites__state" role="status"><Spinner /> Loading gallery…</p>;
  if (error || !data || !Array.isArray(data.rows)) return <section className="suites__state" role="alert"><h2>Unable to load the gallery</h2><p>{error?.message ?? 'Invalid gallery response'}</p></section>;
  const q = search.trim().toLowerCase();
  const rows = data.rows.filter(r => match[filter](r) && (!q || `${r.feature.title} ${r.flow.title}`.toLowerCase().includes(q)));
  // The badge counts flows with missing/extra requests or violations; the filter includes every difference.
  const netCount = data.rows.filter(r => r.tiles.some(t => { const c = wireOf(t); return !!c && wireStructural(c) > 0; })).length;
  return <section className="gallery" aria-label="Build gallery">
    <div className="gallery__bar">
      <div className="gallery__tabs" role="group" aria-label="Gallery section">
        <button type="button" aria-pressed={tab === 'screens'} onClick={() => setTab('screens')}>Screens</button>
        <button type="button" aria-pressed={tab === 'network'} title="Flows with missing or extra requests or rule violations" onClick={() => setTab('network')}>Network{netCount ? ` (${netCount})` : ''}</button>
      </div>
      <select aria-label="Gallery filter" value={filter} onChange={e => setFilter(e.target.value as Filter)}>{(Object.keys(filters) as Filter[]).map(k => <option key={k} value={k}>{filters[k]}</option>)}</select>
      <input aria-label="Search gallery flows" placeholder="Search flows…" value={search} onChange={e => setSearch(e.target.value)} />
      {buildId ? <select aria-label="Gallery scope" value={scope} onChange={e => setScope(e.target.value as 'latest' | 'build')}><option value="latest">Newest result per platform</option><option value="build">This source revision only</option></select> : null}
      <span className="gallery__muted">{rows.length} of {data.rows.length} flows</span>
    </div>
    <LaneHeader gallery={data} />
    <p className="gallery__note" role="note">Web compares a reference with a candidate; other platforms show the final screen only. Each platform can come from a different revision, shown above. Runner statuses are assertions; nothing here is visually accepted.</p>
    {!rows.length ? <p className="suites__state">No flows match this filter.</p>
      : tab === 'screens' ? <Screens gallery={data} rows={rows} client={client} onOpenEvidence={onOpenEvidence} /> : <Network gallery={data} rows={rows} onOpenEvidence={onOpenEvidence} />}
  </section>;
}
