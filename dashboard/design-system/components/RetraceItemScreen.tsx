import { useCallback, useEffect, useRef, useState } from 'react';
import { Badge } from '../primitives';
import { useAsync } from '../useAsync';
import CaptureBanner from './CaptureBanner';
import HopDeltaList from './HopDeltaList';
import ShotCompare, { type ResolveShotUrl } from './ShotCompare';
import WireDiffTable from './WireDiffTable';
import { checkpointVideoOffsetSeconds } from '../videoSeek';
import type { RetraceClient } from '../retraceClient';
import type { Entry, FieldDiff } from '../diffTypes';
import type { Summary, TriageSignals } from '../retraceTypes';
import { verdictTone, verdictLabel } from '../retraceTone';
import { formatWhen } from '../retraceWhen';
import './RetraceItemScreen.css';

/**
 * `Gate.observed`/`.threshold` already carry a percent value (`budget_pct:
 * 0.1` means 0.1%, not a 0-1 fraction), so this only rounds and appends a
 * unit — it must never multiply by 100. Rounded to 2 decimals with
 * trailing zeros dropped via the Number round-trip, so a threshold of
 * exactly `5` reads "5%", not "5.00%".
 */
function formatPct(n: number): string {
  return `${Number(n.toFixed(2))}%`;
}

/**
 * The triage signals that MOVED, in the priority order the classifier reads
 * them — capture → wire → hop → spec → pixel — so the list reads as the rule
 * that produced the label rather than as an arbitrary set.
 */
function movedSignals(signals: TriageSignals): string[] {
  return (['capture', 'wire', 'hop', 'spec', 'pixel'] as const).filter((k) => signals[k]);
}

// A self-contained fetch, not a prop threaded down from ItemScreen's own
// summary useAsync: video/report attach to a run AFTER it finishes, so they
// are never part of Summary and are worth failing independently of it — a
// broken evidence fetch must not blank out the pixel/wire/hop planes it sits
// beside.
function EvidenceSection({
  client,
  app,
  flow,
  registerVideo,
  onVideoCountChange,
}: {
  client: RetraceClient;
  app: string;
  flow: string;
  registerVideo: (el: HTMLVideoElement | null) => void;
  onVideoCountChange: (count: number) => void;
}) {
  const { data } = useAsync(() => client.evidence(app, flow), [client, app, flow]);
  useEffect(() => {
    onVideoCountChange(data?.videos.length ?? 0);
    return () => onVideoCountChange(0);
  }, [data, onVideoCountChange]);
  if (!data || (data.videos.length === 0 && !data.hasReport)) return null;
  return (
    <section className="item__plane item__evidence">
      <h2>evidence</h2>
      {data.videos.map((name) => (
        <video key={name} ref={registerVideo} controls className="item__video" src={client.videoUrl(app, flow, name)} />
      ))}
      {data.hasReport ? (
        <a className="item__report-link" href={client.reportUrl(app, flow)} target="_blank" rel="noreferrer">
          View full test report ↗
        </a>
      ) : null}
    </section>
  );
}

/**
 * The single flow-review screen, shared by retrace-ui's main queue and both
 * apps' sync panels (which embed this exact rendering to let a reviewer
 * inspect any run in a CI candidate list, not only the "latest" one).
 * ItemScreen carries no accept/reject/rule/redact BUTTONS of its own — those
 * verbs, where they exist, are dispatched by the caller against its own
 * app/flow/run state — so embedding it anywhere is safe with no risk of a
 * misdirected mutation.
 *
 * `client` supplies the default `resolveShotUrl`/`onReveal` (the "latest"
 * queue's own routes) and EvidenceSection's video/report URLs — pass
 * run-scoped overrides explicitly (client.shotUrlAtRun / itemAtRun) for a
 * non-latest run, so its generated diff/overlay images are read from their
 * own cache rather than the "latest" queue's.
 */
export default function RetraceItemScreen({
  client,
  app,
  flow,
  summary,
  selectedField,
  onSelectField,
  resolveShotUrl,
  onReveal,
  onBack,
}: {
  client: RetraceClient;
  app: string;
  flow: string;
  summary: Summary;
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
  resolveShotUrl?: ResolveShotUrl;
  onReveal?: () => Promise<Summary['sections']>;
  // Optional: only a main queue view wires a back control. A sync panel's
  // run-detail view embeds this inside its own chrome (which has its own
  // back affordance), so it omits this and the button does not render.
  onBack?: () => void;
}) {
  // Collapsed by default: a flow with a dozen+ checkpoints, most of which
  // passed, otherwise means a long scroll past every unchanged screen to
  // reach wire/hops/budgets on every single review. Callers remount this
  // per flow (via a `key`), so this always starts collapsed on a freshly
  // opened flow rather than carrying over the previous flow's expanded
  // state.
  const [showPassingShots, setShowPassingShots] = useState(false);
  const [videoCount, setVideoCount] = useState(0);
  const videoRefs = useRef<HTMLVideoElement[]>([]);

  const resolveShot = resolveShotUrl ?? ((a, f, side, name) => client.shotUrl(a, f, side as 'a' | 'b' | 'diff' | 'overlay', name));
  const reveal = onReveal ?? (() => client.item(app, flow).then((r) => r.summary.sections));

  const registerVideo = useCallback((el: HTMLVideoElement | null) => {
    videoRefs.current = videoRefs.current.filter((v) => v !== el);
    if (el) videoRefs.current.push(el);
  }, []);

  // Video isn't associated with any one checkpoint — it's one recording of
  // the whole run — so "jump to video" seeks every rendered video to the
  // same offset rather than picking one. ShotCompare already withholds its
  // seek control unless checkpoint.at is a real (non-zero-value) timestamp,
  // so atIso here is always parseable.
  function seekToCheckpoint(atIso: string) {
    const offset = checkpointVideoOffsetSeconds(atIso, summary.b.manifest.startedAt);
    if (offset === null) return;
    for (const v of videoRefs.current) v.currentTime = offset;
    videoRefs.current[0]?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  function renderShot(cp: Summary['checkpoints'][number]) {
    return (
      <ShotCompare
        key={cp.name}
        app={app}
        flow={flow}
        checkpoint={cp}
        resolveShotUrl={resolveShot}
        onSeek={videoCount > 0 ? seekToCheckpoint : undefined}
      />
    );
  }
  // Four verdicts, not three. "quarantined" is the one where every other
  // field is empty ON PURPOSE — so the planes below are not rendered as
  // "nothing differed". The tone comes from verdictTone and from nowhere
  // else, so the queue row and this screen can never paint one verdict
  // differently.
  const tone = verdictTone(summary.verdict);

  return (
    <div className="item">
      {onBack ? (
        <button type="button" className="item__back" onClick={onBack}>
          ← back to queue
        </button>
      ) : null}
      <header className="item__header">
        <h1>
          {app}/{flow}
        </h1>
        <Badge tone={tone}>{verdictLabel(summary.verdict)}</Badge>
        <span
          className="item__runs"
          title={`${summary.a.runId || 'no reference'} → ${summary.b.runId || 'no run'}`}
        >
          {summary.b.runId ? <>ran {formatWhen(summary.b.manifest?.finishedAt, summary.b.runId)}</> : 'no run'}
        </span>
      </header>

      <CaptureBanner capture={summary.capture} detail />

      <EvidenceSection client={client} app={app} flow={flow} registerVideo={registerVideo} onVideoCountChange={setVideoCount} />

      {/*
        Above the gates and the planes, because "whose problem is this" is the
        question a reviewer arrives with and everything below is the evidence
        for the answer. `label` is NOT switched on: a project's own `triage:`
        rule may emit any string, and an exhaustive switch over the built-ins
        would drop it.
      */}
      {summary.triage.label ? (
        <p className="item__triage">
          <strong>{summary.triage.label}</strong>{' '}
          <span className="item__triage-rule">by rule {summary.triage.rule}</span>{' '}
          <span className="item__triage-signals">
            {movedSignals(summary.triage.signals).length > 0
              ? `signals moved: ${movedSignals(summary.triage.signals).join(', ')}`
              : 'signals moved: none'}
          </span>
        </p>
      ) : null}

      {summary.gates.length > 0 ? (
        <ul className="item__gates">
          {summary.gates.map((g) => (
            <li key={g}>{g}</li>
          ))}
        </ul>
      ) : null}

      {summary.verdict === 'quarantined' ? (
        <>
          <div className="item__quarantine">
            <p>
              This flow was not compared. The per-checkpoint diff below is empty because the
              comparison never ran, not because nothing changed — but the run still captured its
              screenshots, shown below.
            </p>
            <ul>
              {summary.quarantined.map((q) => (
                <li key={`${q.side}:${q.reason}`}>
                  side {q.side}: {q.reason}
                </li>
              ))}
            </ul>
          </div>

          {summary.b.manifest.checkpoints.length > 0 ? (
            <section className="item__plane">
              <h2>captured screenshots</h2>
              <div className="item__captures">
                {summary.b.manifest.checkpoints.map((cp) => (
                  <figure key={cp.name} className="item__capture">
                    <figcaption className="item__capture-name">{cp.name}</figcaption>
                    <a href={resolveShot(app, flow, 'b', cp.name)} target="_blank" rel="noreferrer">
                      <img
                        className="item__capture-img"
                        src={resolveShot(app, flow, 'b', cp.name)}
                        alt={`captured screenshot of ${cp.name}`}
                      />
                    </a>
                  </figure>
                ))}
              </div>
            </section>
          ) : (
            <p className="item__none">This run captured no screenshots.</p>
          )}
        </>
      ) : (
        <>
          <section className="item__plane">
            <h2>shots</h2>
            {summary.checkpoints.length === 0 ? (
              <p className="item__none">This flow captured no checkpoints.</p>
            ) : (
              (() => {
                const changed = summary.checkpoints.filter((cp) => cp.verdict !== 'ok');
                const passing = summary.checkpoints.filter((cp) => cp.verdict === 'ok');
                return (
                  <>
                    {changed.map(renderShot)}
                    {passing.length > 0 ? (
                      <>
                        <button
                          type="button"
                          className="item__shots-disclosure"
                          onClick={() => setShowPassingShots((v) => !v)}
                        >
                          {showPassingShots ? '▾' : '▸'} {passing.length} unchanged checkpoint
                          {passing.length === 1 ? '' : 's'}
                        </button>
                        {showPassingShots ? passing.map(renderShot) : null}
                      </>
                    ) : null}
                  </>
                );
              })()
            )}
          </section>

          <section className="item__plane">
            <h2>wire</h2>
            <WireDiffTable
              sections={summary.sections}
              selectedField={selectedField}
              onSelectField={onSelectField}
              onReveal={reveal}
            />
          </section>

          <section className="item__plane">
            <h2>hops</h2>
            <HopDeltaList hops={summary.hops} />
          </section>

          {/* The unmeasured planes are rendered INSIDE this section and
              open it on their own, because the reader's question is "did my
              gates run?" and a section that appears only when some budget
              was measured answers it with silence. */}
          {summary.budgets.length > 0 || summary.unmeasuredGates.length > 0 ? (
            <section className="item__plane">
              <h2>budgets</h2>
              <ul className="item__budgets">
                {summary.budgets.map((g) => (
                  <li key={g.plane}>
                    <Badge tone={g.failed ? 'red' : 'green'}>{g.plane}</Badge> {formatPct(g.observed)} (budget{' '}
                    {formatPct(g.threshold)}) {g.failed ? '✗' : '✓'}
                  </li>
                ))}
                {summary.unmeasuredGates.map((plane) => (
                  <li key={`unmeasured-${plane}`}>
                    <Badge tone="amber">{plane}</Badge> not evaluated — gated by this project's
                    config, and this run carried no evidence to measure it against. That is not a
                    gate that passed.
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
        </>
      )}
    </div>
  );
}
