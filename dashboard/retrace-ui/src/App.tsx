import { useEffect, useRef, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { RULE_BLAST_RADIUS, api, messageOf, ruleRequestFor } from './api/client';
import type { Entry, FieldDiff, Item, Summary } from './api/types';
import { KEY_HELP, actionFor, type Action } from './keys';
import { verdictTone } from './tone';
import { useUrlParam } from './urlState';
import CaptureBanner from './components/CaptureBanner';
import HopDeltaList from './components/HopDeltaList';
import QueueList, { keyOf } from './components/QueueList';
import ShotCompare from './components/ShotCompare';
import WireDiffTable, { entryKey } from './components/WireDiffTable';
import './App.css';

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
 * reads a REST call in a pull request. */
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
  const [matcher, setMatcher] = useState('any');
  const [method, setMethod] = useState(entry.method);
  const [path, setPath] = useState(entry.normalizedPath);

  return (
    <div className="picker" role="dialog" aria-label="write a wire rule">
      <h2>
        Tolerate <code>{field.path}</code>
      </h2>
      <p className="picker__radius">{RULE_BLAST_RADIUS}</p>
      <label>
        matcher
        <input value={matcher} onChange={(e) => setMatcher(e.target.value)} />
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

function ItemScreen({
  app,
  flow,
  summary,
  overlay,
  onOverlayChange,
  position,
  onPositionChange,
  selectedField,
  onSelectField,
}: {
  app: string;
  flow: string;
  summary: Summary;
  overlay: boolean;
  onOverlayChange: (next: boolean) => void;
  position: number;
  onPositionChange: (next: number) => void;
  selectedField: string | null;
  onSelectField: (entry: Entry, field: FieldDiff) => void;
}) {
  // Four verdicts, not three. "quarantined" is the one where every other
  // field is empty ON PURPOSE — so the planes below are not rendered as
  // "nothing differed", which is what an empty checkpoint list and an empty
  // section list would otherwise read as.
  const tone = summary.verdict === 'quarantined' ? 'red' : verdictTone(summary.verdict);

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
              summary.checkpoints.map((cp) => (
                <ShotCompare
                  key={cp.name}
                  app={app}
                  flow={flow}
                  checkpoint={cp}
                  overlay={overlay}
                  onOverlayChange={onOverlayChange}
                  position={position}
                  onPositionChange={onPositionChange}
                />
              ))
            )}
          </section>

          <section className="item__plane">
            <h2>wire</h2>
            <WireDiffTable
              sections={summary.sections}
              selectedField={selectedField}
              onSelectField={onSelectField}
            />
          </section>

          <section className="item__plane">
            <h2>hops</h2>
            <HopDeltaList hops={summary.hops} />
          </section>

          {summary.budgets.length > 0 ? (
            <section className="item__plane">
              <h2>budgets</h2>
              <ul className="item__budgets">
                {summary.budgets.map((g) => (
                  <li key={g.plane}>
                    <Badge tone={g.failed ? 'red' : 'green'}>{g.plane}</Badge> {g.observed} of{' '}
                    {g.threshold}
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
  const [overlay, setOverlay] = useState(false);
  const [position, setPosition] = useState(50);
  const [showHelp, setShowHelp] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [picker, setPicker] = useState<{ entry: Entry; field: FieldDiff } | null>(null);
  const [selectedField, setSelectedField] = useState<string | null>(null);

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
    if (picker !== null) {
      if (action === 'back') setPicker(null);
      return;
    }
    if (showHelp && action === 'back') {
      setShowHelp(false);
      return;
    }
    switch (action) {
      case 'next':
      case 'prev': {
        if (items.length === 0) return;
        const at = items.findIndex((i) => keyOf(i) === selectedKey);
        const step = action === 'next' ? 1 : -1;
        const nextAt = at < 0 ? 0 : Math.min(items.length - 1, Math.max(0, at + step));
        select(items[nextAt]);
        return;
      }
      case 'open':
        if (selectedKey) setOpen(true);
        return;
      case 'back':
        setOpen(false);
        return;
      case 'toggleOverlay':
        setOverlay((v) => !v);
        return;
      case 'scrubLeft':
        setPosition((p) => Math.max(0, p - 5));
        return;
      case 'scrubRight':
        setPosition((p) => Math.min(100, p + 5));
        return;
      case 'accept':
        if (!app || !flow || busy) return;
        void mutate('accept', async () => {
          await api.accept(app, flow);
          return `accepted ${app}/${flow} as the new reference`;
        });
        return;
      case 'reject':
        if (!app || !flow || busy) return;
        void mutate('reject', async () => {
          const res = await api.reject(app, flow);
          return `repro bundle written to ${res.repro.dir}`;
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
              app={app}
              flow={flow}
              summary={item.data}
              overlay={overlay}
              onOverlayChange={setOverlay}
              position={position}
              onPositionChange={setPosition}
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
