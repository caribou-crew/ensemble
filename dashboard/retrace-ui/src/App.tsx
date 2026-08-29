import { useEffect, useRef, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import CaptureBanner from '@ensemble/design-system/components/CaptureBanner';
import HopDeltaList from '@ensemble/design-system/components/HopDeltaList';
import ShotCompare, { type ResolveShotUrl } from '@ensemble/design-system/components/ShotCompare';
import WireDiffTable, { entryKey } from '@ensemble/design-system/components/WireDiffTable';
import {
  api,
  leafFieldName,
  messageOf,
  redactBlastRadius,
  redactRequestFor,
  reportUrl,
  ruleBlastRadius,
  ruleRequestFor,
  videoUrl,
  type AcceptBundle,
  type RedactRequest,
  type RejectResult,
} from './api/client';
import { DEFAULT_MATCHER, MATCHER_NAMES } from './api/matchers';
import type { Entry, FieldDiff, Item, Summary, TriageSignals } from './api/types';
import { KEY_HELP, actionFor, type Action } from './keys';
import { verdictTone } from './tone';
import { useUrlParam } from './urlState';
import QueueList, { keyOf, visibleRows } from './components/QueueList';
import SyncPanel from './components/SyncPanel';
import './App.css';

/**
 * Builds this app's own shot URLs — `/api/shots/{app}/{flow}/{side}/{name}` —
 * the same route `api.shotUrl` built before ShotCompare moved to
 * @ensemble/design-system and traded its `api/client` import for this prop
 * (design.md D5). `encodeURIComponent` per segment matches `api.ts`'s `seg`
 * helper.
 */
const resolveShotUrl: ResolveShotUrl = (app, flow, side, name) =>
  `/api/shots/${encodeURIComponent(app)}/${encodeURIComponent(flow)}/${encodeURIComponent(side)}/${encodeURIComponent(name)}`;

/**
 * What the accept verb just did, in the reviewer's words — F1.
 *
 * `retrace ref accept` prints TWO warnings to stderr on every promotion that
 * warrants them, and refs.AcceptResult carries both as typed values so this
 * surface can say the same thing without parsing prose. A notice that reads
 * "accepted … as the new reference" with identical confidence either way is
 * two faces of one verb disagreeing about what the operation did.
 *
 * `unmatchedMasks` is the expensive one: refs.go reports rather than refuses
 * it precisely because a typo that silently redacts nothing is the one that
 * ends with pixels in git — and those pixels are now in a bundle that gets
 * COMMITTED.
 */
export function acceptNotice(app: string, flow: string, bundle: AcceptBundle): string {
  const done = `accepted ${app}/${flow} as the new reference (${bundle.runId})`;
  const warnings: string[] = [];
  if (bundle.captureStatus !== 'ok') {
    warnings.push(
      `its capture verdict is "${bundle.captureStatus === '' ? 'not assessed' : bundle.captureStatus}" — every diff against this reference now inherits that doubt`,
    );
  }
  if (bundle.unmatchedMasks.length > 0) {
    warnings.push(
      `the project-wide masks: entry for ${bundle.unmatchedMasks.join(', ')} matched no checkpoint in this flow, so it redacted NOTHING here — check the spelling before these shots are committed`,
    );
  }
  if (warnings.length === 0) return done;
  return `${done} — WARNING: ${warnings.join('; and ')}`;
}

/**
 * What the reject verb just did — D3.
 *
 * The warning replaces the unqualified sentence rather than sitting beside
 * it. handleReject sets it when the diff that would EXPLAIN the rejection
 * could not be computed, so there is no summary.json in the bundle; a
 * reviewer who reads "repro bundle written to <dir>" believes they have a
 * bundle that explains the rejection, and they have a directory.
 */
export function rejectNotice(app: string, flow: string, res: RejectResult): string {
  if (res.warning) {
    return `wrote a repro bundle for ${app}/${flow} to ${res.repro.dir}, but it does NOT explain the rejection: ${res.warning}`;
  }
  return `repro bundle written to ${res.repro.dir}`;
}

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

function Problem({ message }: { message: string }) {
  return (
    <div className="problem">
      <Badge tone="red">error</Badge>
      <span>{message}</span>
    </div>
  );
}

/** The rule picker. It states the blast radius BEFORE the confirm rather
 * than receiving it from the server's 400 afterwards: a reviewer who clicks
 * a button believing they scoped the rule to one flow and one direction has
 * already formed the wrong belief by the time a refusal arrives, and nobody
 * reads a REST call in a pull request.
 *
 * And it keeps saying it: the sentence is recomputed from the CURRENT method
 * and path values on every render, so clearing the path box — which widens
 * the rule to every path, because an empty glob matches everything — changes
 * the copy in the same keystroke. See ruleBlastRadius. */
export function RulePicker({
  entry,
  field,
  busy,
  onCancel,
  onConfirm,
}: {
  entry: Entry;
  field: FieldDiff;
  busy: boolean;
  onCancel: () => void;
  onConfirm: (matcher: string, method: string, path: string) => void;
}) {
  // A SELECT over the dialect, seeded with a member of it. The dialect is a
  // closed set that the server validates before writing, so a free-text box
  // here is a control whose every typo is a 400 — and its shipped default,
  // "any", was not a matcher at all, which broke the rule verb on the path
  // nobody edits. See api/matchers.ts, whose list a Go test pins against
  // rules.Names().
  const [matcher, setMatcher] = useState<string>(DEFAULT_MATCHER);
  const [method, setMethod] = useState(entry.method);
  const [path, setPath] = useState(entry.normalizedPath);

  return (
    <div className="picker" role="dialog" aria-label="write a wire rule">
      <h2>
        Tolerate <code>{field.path}</code>
      </h2>
      <p className="picker__radius">{ruleBlastRadius(method, path)}</p>
      <label>
        matcher
        <select
          className="picker__matcher"
          value={matcher}
          onChange={(e) => setMatcher(e.target.value)}
        >
          {MATCHER_NAMES.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
      </label>
      <label>
        method
        <input value={method} onChange={(e) => setMethod(e.target.value)} />
      </label>
      <label>
        path
        <input value={path} onChange={(e) => setPath(e.target.value)} />
      </label>
      <div className="picker__buttons">
        <button type="button" onClick={onCancel} disabled={busy}>
          cancel
        </button>
        <button type="button" onClick={() => onConfirm(matcher, method, path)} disabled={busy}>
          write the rule
        </button>
      </div>
    </div>
  );
}

/** The redact picker — RulePicker's sibling for "stop capturing this field
 * in the clear." Field name is pre-filled from the selected field (the
 * bare leaf key redaction actually matches on, not the full dotted path —
 * see leafFieldName), and stays editable: the field a reviewer clicked on
 * may not be the field they mean to protect, especially once they read the
 * blast-radius sentence and realize it matches every occurrence of the
 * name, project-wide. */
export function RedactPicker({
  field,
  busy,
  onCancel,
  onConfirm,
}: {
  field: FieldDiff;
  busy: boolean;
  onCancel: () => void;
  onConfirm: (fieldName: string, mode: RedactRequest['mode'], why: string) => void;
}) {
  const [fieldName, setFieldName] = useState(leafFieldName(field.path));
  const [mode, setMode] = useState<RedactRequest['mode']>('destroy');
  const [why, setWhy] = useState('');

  return (
    <div className="picker" role="dialog" aria-label="add a redaction rule">
      <h2>
        Redact <code>{field.path}</code>
      </h2>
      <p className="picker__radius">{redactBlastRadius(fieldName, mode)}</p>
      <label>
        field name
        <input value={fieldName} onChange={(e) => setFieldName(e.target.value)} />
      </label>
      <label>
        mode
        <select value={mode} onChange={(e) => setMode(e.target.value as RedactRequest['mode'])}>
          <option value="destroy">destroy — irreversible, the default</option>
          <option value="encrypt">encrypt — recoverable with the team key</option>
          <option value="display">display — kept in the clear on disk, masked on screen</option>
        </select>
      </label>
      <label>
        why
        <input value={why} onChange={(e) => setWhy(e.target.value)} placeholder="optional" />
      </label>
      <div className="picker__buttons">
        <button type="button" onClick={onCancel} disabled={busy}>
          cancel
        </button>
        <button
          type="button"
          onClick={() => onConfirm(fieldName, mode, why)}
          disabled={busy || fieldName.trim() === ''}
        >
          write the rule
        </button>
      </div>
    </div>
  );
}

/**
 * The triage signals that MOVED, in the priority order the classifier reads
 * them — capture → wire → hop → spec → pixel — so the list reads as the rule
 * that produced the label rather than as an arbitrary set.
 *
 * Keys are listed explicitly rather than derived with Object.keys: object key
 * order is an implementation detail of the JSON decoder, and this order is
 * the explanation.
 */
function movedSignals(signals: TriageSignals): string[] {
  return (['capture', 'wire', 'hop', 'spec', 'pixel'] as const).filter((k) => signals[k]);
}

// A self-contained fetch, not a prop threaded down from ItemScreen's own
// summary useAsync: video/report attach to a run AFTER it finishes, so they
// are never part of Summary and are worth failing independently of it — a
// broken evidence fetch must not blank out the pixel/wire/hop planes it sits
// beside.
function EvidenceSection({ app, flow }: { app: string; flow: string }) {
  const { data } = useAsync(() => api.evidence(app, flow), [app, flow]);
  if (!data || (data.videos.length === 0 && !data.hasReport)) return null;
  return (
    <section className="item__plane item__evidence">
      <h2>evidence</h2>
      {data.videos.map((name) => (
        <video key={name} controls className="item__video" src={videoUrl(app, flow, name)} />
      ))}
      {data.hasReport ? (
        <a className="item__report-link" href={reportUrl(app, flow)} target="_blank" rel="noreferrer">
          View full test report ↗
        </a>
      ) : null}
    </section>
  );
}

function ItemScreen({
  app,
  flow,
  summary,
  selectedField,
  onSelectField,
}: {
  app: string;
  flow: string;
  summary: Summary;
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
}) {
  // Collapsed by default: a flow with a dozen+ checkpoints, most of which
  // passed, otherwise means a long scroll past every unchanged screen to
  // reach wire/hops/budgets on every single review — the screens that
  // actually need a look are what should be on screen. ItemScreen is
  // remounted per flow (see App's `key` below), so this always starts
  // collapsed on a freshly opened flow rather than carrying over the
  // previous flow's expanded state.
  const [showPassingShots, setShowPassingShots] = useState(false);
  // Four verdicts, not three. "quarantined" is the one where every other
  // field is empty ON PURPOSE — so the planes below are not rendered as
  // "nothing differed", which is what an empty checkpoint list and an empty
  // section list would otherwise read as.
  //
  // The tone comes from verdictTone and from nowhere else. It used to be
  // special-cased here, which was a second home for "what colour is this
  // verdict" and let the queue row and the item screen paint the same verdict
  // differently — verdictTone is now total over all four (D1), so there is
  // one answer.
  const tone = verdictTone(summary.verdict);

  return (
    <div className="item">
      <header className="item__header">
        <h1>
          {app}/{flow}
        </h1>
        <Badge tone={tone}>{summary.verdict}</Badge>
        <span className="item__runs">
          {summary.a.runId || 'no reference'} → {summary.b.runId || 'no run'}
        </span>
      </header>

      <CaptureBanner capture={summary.capture} detail />

      <EvidenceSection app={app} flow={flow} />

      {/*
        Above the gates and the planes, because "whose problem is this" is the
        question a reviewer arrives with and everything below is the evidence
        for the answer. The signal vector rides along for the same reason it
        is on the wire: a label a reviewer cannot check against the evidence
        is one they have to take on faith.

        `label` is NOT switched on. A project's own `triage:` rule may emit
        any string, and an exhaustive switch over the built-ins would drop it.
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
        <div className="item__quarantine">
          <p>
            This flow was not compared. Every plane below is empty because the comparison never
            ran, not because nothing changed.
          </p>
          <ul>
            {summary.quarantined.map((q) => (
              <li key={`${q.side}:${q.reason}`}>
                side {q.side}: {q.reason}
              </li>
            ))}
          </ul>
        </div>
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
                    {changed.map((cp) => (
                      <ShotCompare key={cp.name} app={app} flow={flow} checkpoint={cp} resolveShotUrl={resolveShotUrl} />
                    ))}
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
                        {showPassingShots
                          ? passing.map((cp) => (
                              <ShotCompare key={cp.name} app={app} flow={flow} checkpoint={cp} resolveShotUrl={resolveShotUrl} />
                            ))
                          : null}
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
              onReveal={() => api.item(app, flow).then((r) => r.summary.sections)}
            />
          </section>

          <section className="item__plane">
            <h2>hops</h2>
            <HopDeltaList hops={summary.hops} />
          </section>

          {/* The unmeasured planes are rendered INSIDE this section and
              open it on their own, because the reader's question is "did my
              gates run?" and a section that appears only when some budget
              was measured answers it with silence. `budgets.length > 0`
              alone hid the very case the section is most needed for: every
              plane the project gated was unmeasurable, so nothing rendered
              at all, so the page read as "not gated". */}
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
                    {/* amber, the same tone `quarantined` gets: "could not
                        evaluate", not "evaluated and bad" — and emphatically
                        not green, which is what an absent row rendered as
                        before. */}
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

export default function App() {
  const [app, setApp] = useUrlParam('app');
  const [flow, setFlow] = useUrlParam('flow');
  // A deep link that names a flow IS a request to look at that flow — the Go
  // side's SPA fallback exists so that URL survives a hard refresh, and
  // landing it on the queue instead would waste it.
  const [open, setOpen] = useState(() => Boolean(app && flow));
  const [version, setVersion] = useState(0);
  const [showHelp, setShowHelp] = useState(false);
  // Owned here, not inside QueueList: j/k must walk the rows that are ON
  // SCREEN, and "is the passing group expanded" is half of that answer.
  const [showPassing, setShowPassing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [picker, setPicker] = useState<{ entry: Entry; field: FieldDiff } | null>(null);
  const [redactPicker, setRedactPicker] = useState<{ entry: Entry; field: FieldDiff } | null>(null);
  const [selectedField, setSelectedField] = useState<string | null>(null);
  const [showSyncPanel, setShowSyncPanel] = useState(false);

  // Every fetch in this app goes through useAsync — the queue load, the item
  // load, and the post-mutation refetch, which is this same hook with
  // `version` in its deps. There is no hand-rolled cancellation anywhere.
  const queue = useAsync(() => api.queue(), [version]);
  const items = queue.data?.items ?? [];
  const selectedKey = app && flow ? `${app}/${flow}` : null;
  const item = useAsync(async () => {
    if (!open || !app || !flow) return null;
    return (await api.item(app, flow)).summary;
  }, [open, app, flow, version]);

  const select = (next: Item) => {
    setApp(next.app);
    setFlow(next.flow);
    setSelectedField(null);
    // The notice and the error belong to the flow they were produced for.
    // "accepted web/search as the new reference" sitting above web/cart names
    // its own flow and so does not strictly lie, but a stale success message
    // over a different flow is the reassuring direction, and this phase's
    // whole problem is the reassuring direction.
    setNotice(null);
    setActionError(null);
  };

  const mutate = async (label: string, fn: () => Promise<string>) => {
    setBusy(true);
    setActionError(null);
    setNotice(null);
    try {
      // Nothing optimistic: the notice is written from what came BACK, and
      // the refetch is what puts the new state on screen. Accepting a
      // reference is a filesystem mutation, and a UI that lies about it is
      // worse than a slow one.
      setNotice(await fn());
      setVersion((v) => v + 1);
    } catch (err) {
      setActionError(messageOf(err, `${label} failed`));
    } finally {
      setBusy(false);
    }
  };

  const onAction = (action: Action) => {
    if (action === 'help') {
      setShowHelp((v) => !v);
      return;
    }
    if (picker !== null || redactPicker !== null || showSyncPanel) {
      if (action === 'back') {
        setPicker(null);
        setRedactPicker(null);
        setShowSyncPanel(false);
      }
      return;
    }
    if (showHelp && action === 'back') {
      setShowHelp(false);
      return;
    }
    switch (action) {
      case 'next':
      case 'prev': {
        // The RENDERED rows, not queue.data.items. The queue screen shows two
        // groups and collapses the passing one, so stepping through the raw
        // server list walked the selection straight off the bottom of the
        // visible set: nothing on screen was selected any more, the keypress
        // looked like a no-op, and `enter` then opened a flow the reviewer
        // had never seen.
        const rows = visibleRows(items, showPassing);
        if (rows.length === 0) return;
        const at = rows.findIndex((i) => keyOf(i) === selectedKey);
        const step = action === 'next' ? 1 : -1;
        const nextAt = at < 0 ? 0 : Math.min(rows.length - 1, Math.max(0, at + step));
        select(rows[nextAt]);
        return;
      }
      case 'open':
        if (selectedKey) setOpen(true);
        return;
      case 'back':
        setOpen(false);
        return;
      case 'accept':
        // Gated on `open`, and that gate is the point. Accepting a reference
        // is a filesystem mutation, and ungated it fired from the QUEUE
        // screen against whatever ?app=&flow= happened to hold — including a
        // selection that had walked onto a collapsed row the reviewer could
        // not see. A verb this expensive fires only from the screen that is
        // showing you what you are about to promote.
        if (!open || !app || !flow || busy) return;
        void mutate('accept', async () => {
          const res = await api.accept(app, flow);
          return acceptNotice(app, flow, res.bundle);
        });
        return;
      case 'reject':
        // Same gate, same reason: reject removes and rewrites a directory.
        if (!open || !app || !flow || busy) return;
        void mutate('reject', async () => {
          return rejectNotice(app, flow, await api.reject(app, flow));
        });
        return;
      case 'rule': {
        if (!selectedField || !item.data) return;
        for (const section of item.data.sections) {
          for (const entry of section.entries) {
            for (const field of [...entry.bodyViolations, ...entry.bodyDiff, ...entry.bodyTolerated]) {
              if (`${entryKey(entry)}|${field.scope}:${field.path}` === selectedField) {
                setPicker({ entry, field });
                return;
              }
            }
          }
        }
        return;
      }
      case 'redact': {
        if (!selectedField || !item.data) return;
        for (const section of item.data.sections) {
          for (const entry of section.entries) {
            for (const field of [...entry.bodyViolations, ...entry.bodyDiff, ...entry.bodyTolerated]) {
              if (`${entryKey(entry)}|${field.scope}:${field.path}` === selectedField) {
                setRedactPicker({ entry, field });
                return;
              }
            }
          }
        }
        return;
      }
    }
  };

  // The handler is read through a ref so the listener is installed once and
  // still sees current state — re-subscribing on every state change would
  // reinstall a window listener on every keystroke.
  const actionRef = useRef(onAction);
  actionRef.current = onAction;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const action = actionFor(e);
      if (action === null) return;
      e.preventDefault();
      actionRef.current(action);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <div className="app-shell">
      <header className="app-header">
        <button type="button" className="app-header__brand" onClick={() => setOpen(false)}>
          retrace review
        </button>
        {busy ? <Spinner /> : null}
        <button type="button" className="app-header__sync" onClick={() => setShowSyncPanel(true)}>
          sync
        </button>
        <button type="button" className="app-header__help" onClick={() => setShowHelp((v) => !v)}>
          ? help
        </button>
      </header>

      {actionError ? <Problem message={actionError} /> : null}
      {notice ? <p className="notice">{notice}</p> : null}

      <main className="app-main">
        {open ? (
          item.loading ? (
            <p className="loading">
              <Spinner /> loading {app}/{flow}…
            </p>
          ) : item.error ? (
            <Problem message={item.error.message} />
          ) : item.data && app && flow ? (
            <ItemScreen
              key={`${app}/${flow}`}
              app={app}
              flow={flow}
              summary={item.data}
              selectedField={selectedField}
              onSelectField={(entry, field) =>
                setSelectedField(`${entryKey(entry)}|${field.scope}:${field.path}`)
              }
            />
          ) : (
            <p className="loading">Nothing selected.</p>
          )
        ) : queue.loading ? (
          <p className="loading">
            <Spinner /> loading the review queue…
          </p>
        ) : queue.error ? (
          <Problem message={queue.error.message} />
        ) : queue.data ? (
          <QueueList
            items={queue.data.items}
            empty={queue.data.empty}
            selected={selectedKey}
            showPassing={showPassing}
            onShowPassingChange={setShowPassing}
            onSelect={select}
            onOpen={(next) => {
              select(next);
              setOpen(true);
            }}
          />
        ) : null}
      </main>

      {picker && app && flow ? (
        <RulePicker
          entry={picker.entry}
          field={picker.field}
          busy={busy}
          onCancel={() => setPicker(null)}
          onConfirm={(matcher, method, path) => {
            const { entry, field } = picker;
            void mutate('writing the rule', async () => {
              await api.rule(app, flow, {
                ...ruleRequestFor(field, matcher, entry),
                method,
                path,
              });
              setPicker(null);
              return `wrote a wire rule for ${field.path}`;
            });
          }}
        />
      ) : null}

      {redactPicker && app && flow ? (
        <RedactPicker
          field={redactPicker.field}
          busy={busy}
          onCancel={() => setRedactPicker(null)}
          onConfirm={(fieldName, mode, why) => {
            const { field } = redactPicker;
            void mutate('writing the redaction rule', async () => {
              await api.redact(app, flow, {
                ...redactRequestFor(field, mode, why),
                field: fieldName.trim(),
              });
              setRedactPicker(null);
              return `wrote a redaction rule for "${fieldName.trim()}" (${mode})`;
            });
          }}
        />
      ) : null}

      {showSyncPanel ? (
        <SyncPanel
          onClose={() => setShowSyncPanel(false)}
          onSynced={() => setVersion((v) => v + 1)}
        />
      ) : null}

      {showHelp ? (
        <div className="help" role="dialog" aria-label="keyboard help">
          <table>
            <tbody>
              {KEY_HELP.map((h) => (
                <tr key={h.keys}>
                  <th>{h.keys}</th>
                  <td>{h.what}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}
