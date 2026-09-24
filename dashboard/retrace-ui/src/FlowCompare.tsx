import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { verdictLabel, verdictTone } from '@ensemble/design-system/retraceTone';
import ShotCompare from '@ensemble/design-system/components/ShotCompare';
import RetraceItemScreen from '@ensemble/design-system/components/RetraceItemScreen';
import { entryKey } from '@ensemble/design-system/components/WireDiffTable';
import type { CheckpointVerdict } from '@ensemble/design-system/diffTypes';
import type { Summary, SurfaceRun } from '@ensemble/design-system/retraceTypes';
import type { ReportTab } from './BranchReport';
import { REFERENCE, appLabel, branchLabel, client, countsParts, flowLabel, orderApps, relTime, runMs, shotUrl, type Pairing } from './reportData';
import { pairKey, type SummaryState } from './useSummaries';

type Mode = 'side' | 'after' | 'diff';

interface Props {
  flow: string;
  baseBranch: string;
  pairings: Pairing[];
  summaries: Map<string, SummaryState>;
  tab: ReportTab;
  app: string | null;
  onNavigate: (next: { app?: string | null; tab?: ReportTab }) => void;
  onBack: () => void;
}

const cpTone = (v: CheckpointVerdict['verdict']) => (v === 'ok' ? 'green' : v === 'changed' ? 'amber' : 'red');

function checkpointNames(sums: (Summary | undefined)[]): string[] {
  const names: string[] = [];
  for (const s of sums) {
    for (const c of s?.checkpoints ?? []) if (!names.includes(c.name)) names.push(c.name);
    for (const c of s?.b.manifest.checkpoints ?? []) if (!names.includes(c.name)) names.push(c.name);
  }
  return names;
}

function Img({ src, alt }: { src: string; alt: string }) {
  const [failed, setFailed] = useState(false);
  if (failed) return <span className="fc-img fc-img--none">no image</span>;
  return <img className="fc-img" src={src} alt={alt} loading="lazy" onError={() => setFailed(true)} />;
}

function Labeled({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="fc-labeled">
      {label ? <span className="fc-labeled__label">{label}</span> : null}
      {children}
    </span>
  );
}

function MatrixCell({ p, cp, mode, onOpen }: { p: Pairing; cp?: CheckpointVerdict; mode: Mode; onOpen: () => void }) {
  if (!cp) return <td className="fc-cell fc-cell--none">not captured</td>;
  const changed = cp.verdict !== 'ok';
  const hasA = Boolean(cp.images.a);
  const hasB = Boolean(cp.images.b);
  return (
    <td className={`fc-cell fc-cell--${cpTone(cp.verdict)}`}>
      <button type="button" className="fc-cell__btn" onClick={onOpen} title="open large compare">
        <div className="fc-cell__imgs">
          {mode === 'side' && hasA ? <Labeled label="before"><Img src={shotUrl(p, 'a', cp.name)} alt={`${cp.name} before`} /></Labeled> : null}
          {mode !== 'diff' && hasB ? <Labeled label={mode === 'side' ? 'after' : ''}><Img src={shotUrl(p, 'b', cp.name)} alt={`${cp.name} after`} /></Labeled> : null}
          {mode === 'diff' ? (
            changed && cp.images.diff ? <Img src={shotUrl(p, 'diff', cp.name)} alt={`${cp.name} diff`} /> : <span className="fc-img fc-img--none">identical</span>
          ) : null}
        </div>
        <span className="fc-cell__verdict">
          {cp.verdict === 'ok' ? 'identical' : cp.verdict}
          {cp.diffPct > 0 ? ` · ${cp.diffPct.toFixed(2)}%` : ''}
        </span>
      </button>
    </td>
  );
}

function Lightbox({
  cols,
  names,
  at,
  onMove,
  onClose,
  onDetail,
}: {
  cols: { p: Pairing; sum?: Summary }[];
  names: string[];
  at: { col: number; row: number };
  onMove: (next: { col: number; row: number }) => void;
  onClose: () => void;
  onDetail: (app: string) => void;
}) {
  const { p, sum } = cols[at.col];
  const cp = sum?.checkpoints.find((c) => c.name === names[at.row]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const step: Record<string, [number, number]> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };
      if (e.key === 'Escape') onClose();
      else if (step[e.key]) {
        const [dc, dr] = step[e.key];
        onMove({
          col: (at.col + dc + cols.length) % cols.length,
          row: Math.min(names.length - 1, Math.max(0, at.row + dr)),
        });
      } else return;
      e.preventDefault();
      e.stopPropagation();
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  }, [at, cols.length, names.length, onMove, onClose]);

  return (
    <div className="report-lightbox" role="dialog" aria-label="screen compare" onClick={onClose}>
      <div className="report-lightbox__panel" onClick={(e) => e.stopPropagation()}>
        <div className="report-lightbox__bar">
          <strong>{appLabel(p.app)}</strong>
          <span className="report-lightbox__cp">{names[at.row]}</span>
          <span className="report-lightbox__hint">← → platform · ↑ ↓ screen · esc close</span>
          <button type="button" className="branch-report__btn" onClick={() => onDetail(p.app)}>
            full run: wire + video →
          </button>
          <button type="button" className="branch-report__btn" onClick={onClose}>
            ✕
          </button>
        </div>
        <div className="report-lightbox__tabs">
          {cols.map((c, i) => {
            const v = c.sum?.checkpoints.find((x) => x.name === names[at.row])?.verdict;
            return (
              <button
                key={c.p.app}
                type="button"
                className={`fc-chip${i === at.col ? ' fc-chip--active' : ''}${v ? ` fc-chip--${cpTone(v)}` : ''}`}
                onClick={() => onMove({ ...at, col: i })}
              >
                {appLabel(c.p.app)}
              </button>
            );
          })}
        </div>
        {cp ? (
          <ShotCompare app={p.app} flow={p.flow} checkpoint={cp} resolveShotUrl={(_a, _f, side, name) => shotUrl(p, side as 'a', name)} />
        ) : (
          <p className="branch-report__empty">{appLabel(p.app)} did not capture “{names[at.row]}”.</p>
        )}
      </div>
    </div>
  );
}

function Screens({ cols, onDetail }: { cols: { p: Pairing; st?: SummaryState }[]; onDetail: (app: string) => void }) {
  const [mode, setMode] = useState<Mode>('side');
  const [changedOnly, setChangedOnly] = useState(false);
  const [box, setBox] = useState<{ col: number; row: number } | null>(null);
  const ready = cols.map((c) => ({ p: c.p, sum: c.st?.sum }));
  const all = checkpointNames(ready.map((c) => c.sum));
  const names = changedOnly
    ? all.filter((n) => ready.some((c) => c.sum?.checkpoints.some((cp) => cp.name === n && cp.verdict !== 'ok')))
    : all;

  return (
    <>
      <div className="fc-controls">
        <div className="fc-seg" role="group" aria-label="image mode">
          {(
            [
              ['side', 'before · after'],
              ['after', 'after only'],
              ['diff', 'diff'],
            ] as const
          ).map(([m, label]) => (
            <button key={m} type="button" className={`fc-seg__btn${mode === m ? ' fc-seg__btn--active' : ''}`} onClick={() => setMode(m)}>
              {label}
            </button>
          ))}
        </div>
        <label className="fc-toggle">
          <input type="checkbox" checked={changedOnly} onChange={(e) => setChangedOnly(e.target.checked)} /> changed screens only
        </label>
        <span className="fc-hint">click any screen for a large before/after/diff with keyboard navigation</span>
      </div>
      <div className="fc-matrix-wrap">
        <table className={`fc-matrix fc-matrix--${mode}`}>
          <thead>
            <tr>
              <th className="fc-matrix__corner">screen</th>
              {cols.map(({ p, st }) => (
                <th key={p.app} className="fc-matrix__col">
                  <button type="button" className="fc-col-head" onClick={() => onDetail(p.app)} title="open this platform's full run">
                    <span className="fc-col-head__app">{appLabel(p.app)}</span>
                    {st?.sum ? <Badge tone={verdictTone(st.sum.verdict)}>{verdictLabel(st.sum.verdict)}</Badge> : st?.error ? <Badge tone="amber">error</Badge> : <Spinner />}
                    <span className="fc-col-head__counts">{st?.sum ? countsParts(st.sum.counts).filter((s) => !s.includes('screens')).join(' · ') || 'wire identical' : (st?.error ?? '')}</span>
                  </button>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {names.map((n) => (
              <tr key={n}>
                <th className="fc-matrix__row">{n}</th>
                {ready.map(({ p, sum }, col) =>
                  sum ? (
                    <MatrixCell key={p.app} p={p} mode={mode} cp={sum.checkpoints.find((c) => c.name === n)} onOpen={() => setBox({ col, row: all.indexOf(n) })} />
                  ) : (
                    <td key={p.app} className="fc-cell fc-cell--none" />
                  ),
                )}
              </tr>
            ))}
          </tbody>
        </table>
        {names.length === 0 && ready.every((c) => c.sum) ? <p className="branch-report__empty">No screens differ on any platform.</p> : null}
      </div>
      {box ? <Lightbox cols={ready} names={all} at={box} onMove={setBox} onClose={() => setBox(null)} onDetail={onDetail} /> : null}
    </>
  );
}

function RunVideo({ app, flow, run, label, register }: { app: string; flow: string; run: SurfaceRun; label: string; register: (el: HTMLVideoElement | null) => void }) {
  const ev = useAsync(() => client.evidenceAtRun(app, flow, run.runId), [app, flow, run.runId]);
  const name = ev.data?.videos[0];
  return (
    <figure className="fc-video">
      <figcaption>{label}</figcaption>
      {ev.loading ? <Spinner /> : name ? <video ref={register} src={client.videoUrlAtRun(app, flow, run.runId, name)} muted playsInline controls preload="metadata" /> : <span className="fc-img fc-img--none">no video recorded</span>}
    </figure>
  );
}

function Videos({ cols, baseBranch }: { cols: { p: Pairing; st?: SummaryState }[]; baseBranch: string }) {
  const refs = useRef(new Set<HTMLVideoElement>());
  const [rate, setRate] = useState(1);
  const register = (el: HTMLVideoElement | null) => {
    if (el) {
      refs.current.add(el);
      el.playbackRate = rate;
    }
  };
  const each = (fn: (v: HTMLVideoElement) => void) => refs.current.forEach((v) => (v.isConnected ? fn(v) : refs.current.delete(v)));

  return (
    <>
      <div className="fc-controls">
        <button type="button" className="branch-report__btn" onClick={() => each((v) => void v.play())}>▶ play all</button>
        <button type="button" className="branch-report__btn" onClick={() => each((v) => v.pause())}>❚❚ pause all</button>
        <button type="button" className="branch-report__btn" onClick={() => each((v) => (v.currentTime = 0))}>⟲ restart all</button>
        <div className="fc-seg" role="group" aria-label="playback speed">
          {[1, 2, 4].map((s) => (
            <button
              key={s}
              type="button"
              className={`fc-seg__btn${rate === s ? ' fc-seg__btn--active' : ''}`}
              onClick={() => {
                setRate(s);
                each((v) => (v.playbackRate = s));
              }}
            >
              {s}x
            </button>
          ))}
        </div>
      </div>
      <div className="fc-videos">
        {cols.map(({ p, st }) => (
          <div key={p.app} className="fc-videos__col">
            <div className="fc-videos__head">
              <strong>{appLabel(p.app)}</strong>
              {st?.sum ? <Badge tone={verdictTone(st.sum.verdict)}>{verdictLabel(st.sum.verdict)}</Badge> : null}
            </div>
            {p.run ? <RunVideo app={p.app} flow={p.flow} run={p.run} label={`this run · ${relTime(runMs(p.run))}`} register={register} /> : null}
            {baseBranch !== REFERENCE && p.base ? (
              <RunVideo app={p.app} flow={p.flow} run={p.base} label={`base · ${branchLabel(baseBranch)}`} register={register} />
            ) : null}
          </div>
        ))}
      </div>
    </>
  );
}

function Detail({ p, st }: { p: Pairing; st?: SummaryState }) {
  const [selectedField, setSelectedField] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const run = p.run!;
  const accept = `retrace ref accept --app ${p.app} --flow ${p.flow} --run ${run.runId}`;
  return (
    <div className="fc-detail">
      <div className="fc-detail__meta">
        <span>
          run <code>{run.runId}</code>
          {p.base ? (
            <>
              {' '}vs <code>{p.base.runId}</code>
            </>
          ) : (
            ' vs accepted baseline'
          )}
        </span>
        {run.source?.runUrl ? (
          <a href={run.source.runUrl} target="_blank" rel="noreferrer">
            CI run ↗
          </a>
        ) : null}
        <button
          type="button"
          className="branch-report__btn"
          title={accept}
          onClick={() => {
            void navigator.clipboard.writeText(accept).then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            });
          }}
        >
          {copied ? 'copied ✓' : 'copy accept command'}
        </button>
      </div>
      <div className="fc-detail__video">
        <RunVideo app={p.app} flow={p.flow} run={run} label="run video" register={() => undefined} />
        {p.base ? <RunVideo app={p.app} flow={p.flow} run={p.base} label="base run video" register={() => undefined} /> : null}
      </div>
      {st?.sum ? (
        <RetraceItemScreen
          key={pairKey(p)}
          client={client}
          app={p.app}
          flow={p.flow}
          summary={st.sum}
          showLatestEvidence={false}
          selectedField={selectedField}
          onSelectField={(entry, field) => setSelectedField(`${entryKey(entry)}|${field.scope}:${field.path}`)}
          resolveShotUrl={(_a, _f, side, name) => shotUrl(p, side as 'a', name)}
          onReveal={() => client.itemAtRun(p.app, p.flow, run.runId, false, p.base?.runId).then((r) => r.summary.sections)}
        />
      ) : st?.error ? (
        <p className="branch-report__empty">{st.error}</p>
      ) : (
        <p className="branch-report__empty">
          <Spinner /> loading…
        </p>
      )}
    </div>
  );
}

export default function FlowCompare({ flow, baseBranch, pairings, summaries, tab, app, onNavigate, onBack }: Props) {
  const usable = pairings.filter((p) => p.run && !p.missingBase);
  const byApp = new Map(usable.map((p) => [p.app, p]));
  const cols = orderApps(byApp.keys()).map((a) => {
    const p = byApp.get(a)!;
    return { p, st: summaries.get(pairKey(p)) };
  });
  const missing = pairings.filter((p) => p.missingBase).map((p) => appLabel(p.app));
  const detail = tab === 'detail' && app ? cols.find((c) => c.p.app === app) : undefined;

  return (
    <div className="fc">
      <div className="fc-head">
        <button type="button" className="branch-report__btn" onClick={onBack}>
          ← all flows
        </button>
        <h2 className="branch-report__flow-title">
          {flowLabel(flow)}
          <span className="branch-report__flow-sub">{flow}</span>
        </h2>
      </div>
      <nav className="fc-tabs">
        <button type="button" className={`fc-tab${tab === 'screens' ? ' fc-tab--active' : ''}`} onClick={() => onNavigate({ tab: 'screens' })}>
          screens · all platforms
        </button>
        <button type="button" className={`fc-tab${tab === 'videos' ? ' fc-tab--active' : ''}`} onClick={() => onNavigate({ tab: 'videos' })}>
          videos · all platforms
        </button>
        <span className="fc-tabs__sep" />
        {cols.map(({ p, st }) => (
          <button
            key={p.app}
            type="button"
            className={`fc-chip${detail?.p.app === p.app ? ' fc-chip--active' : ''}${st?.sum ? ` fc-chip--${verdictTone(st.sum.verdict)}` : ''}`}
            onClick={() => onNavigate({ tab: 'detail', app: p.app })}
          >
            {appLabel(p.app)}
          </button>
        ))}
      </nav>
      {missing.length > 0 ? <p className="fc-hint">No run on {branchLabel(baseBranch)} for: {missing.join(', ')}.</p> : null}

      {tab === 'videos' ? (
        <Videos cols={cols} baseBranch={baseBranch} />
      ) : detail ? (
        <Detail p={detail.p} st={detail.st} />
      ) : (
        <Screens cols={cols} onDetail={(a) => onNavigate({ tab: 'detail', app: a })} />
      )}
    </div>
  );
}
