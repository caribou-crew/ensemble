import { useEffect, useRef, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { entryKey } from '@ensemble/design-system/components/WireDiffTable';
import RetraceQueueList, { keyOf, visibleRows } from '@ensemble/design-system/components/RetraceQueueList';
import RetraceQueueFilters from '@ensemble/design-system/components/RetraceQueueFilters';
import RetraceRunsList from '@ensemble/design-system/components/RetraceRunsList';
import RetraceBreadcrumb from '@ensemble/design-system/components/RetraceBreadcrumb';
import RetraceItemScreen from '@ensemble/design-system/components/RetraceItemScreen';
import RetraceSyncPanel from '@ensemble/design-system/components/RetraceSyncPanel';
import { createRetraceClient, type QueueFilter } from '@ensemble/design-system/retraceClient';
import { formatWhen } from '@ensemble/design-system/retraceWhen';
import {
  api,
  leafFieldName,
  messageOf,
  redactBlastRadius,
  redactRequestFor,
  ruleBlastRadius,
  ruleRequestFor,
  secretFindingsOf,
  type AcceptBundle,
  type RedactRequest,
  type RejectResult,
  type SecretFinding,
} from './api/client';
import { DEFAULT_MATCHER, MATCHER_NAMES } from './api/matchers';
import type { Entry, FieldDiff } from './api/types';
import { KEY_HELP, actionFor, type Action } from './keys';
import { useUrlParam } from './urlState';
import './App.css';

// A single, stable client instance — basePath is static for the life of
// this app, and useAsync's deps arrays close over it by identity, so a
// value recreated on every render would refetch on every render too.
const client = createRetraceClient('/api');

function Problem({ message }: { message: string }) {
  return (
    <div className="problem">
      <Badge tone="red">error</Badge>
      <span>{message}</span>
    </div>
  );
}

/**
 * What the accept verb just did, in the reviewer's words — F1.
 *
 * `retrace ref accept` prints TWO warnings to stderr on every promotion that
 * warrants them, and refs.AcceptResult carries both as typed values so this
 * surface can say the same thing without parsing prose. A notice that reads
 * "accepted … as the new reference" with identical confidence either way is
 * two faces of one verb disagreeing.
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
  if ((bundle.secretFindings ?? []).length > 0) {
    warnings.push(
      `it was FORCED past the secret scan (${bundle.secretFindings.map((f) => f.path).join(', ')}) — the bundle manifest records acceptedWithSecrets: true, and every clone of this repository now carries those values`,
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

/** The rule picker. It states the blast radius BEFORE the confirm rather
 * than receiving it from the server's 400 afterwards: a reviewer who clicks
 * a button believing they scoped the rule to one flow and one direction has
 * already formed the wrong belief by the time a refusal arrives, and nobody
 * reads a REST call in a pull request. See ruleBlastRadius. */
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
 * in the clear." Field name is pre-filled from the selected field, and
 * stays editable: the field a reviewer clicked on may not be the field
 * they mean to protect, especially once they read the blast-radius sentence
 * and realize it matches every occurrence of the name, project-wide. */
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

/** The accept-time secret scan's refusal, rendered for the reviewer who just
 * pressed accept — the pickers' sibling, and the same say-it-before-the-
 * confirm principle: each finding's field path and fix-for-good command are
 * on screen BEFORE the force button, because a reference bundle is committed
 * and a secret in git cannot be taken back. */
export function SecretScanPanel({
  findings,
  busy,
  onCancel,
  onForce,
}: {
  findings: SecretFinding[];
  busy: boolean;
  onCancel: () => void;
  onForce: () => void;
}) {
  return (
    <div className="picker" role="dialog" aria-label="likely credentials found">
      <h2>Likely credentials in this bundle</h2>
      <p className="picker__radius">
        The accept was refused: a reference bundle is COMMITTED, and these values would land in git
        for every clone, forever. Redact the fields and re-record — or accept anyway, which records
        acceptedWithSecrets: true in the bundle manifest for the pull-request reviewer to see.
      </p>
      <ul className="picker__findings">
        {findings.map((f) => (
          <li key={`${f.file}:${f.seq}:${f.path}`}>
            <code>{f.path}</code> ({f.kind}, {f.file} seq {f.seq}) — {f.suggestion}
          </li>
        ))}
      </ul>
      <div className="picker__buttons">
        <button type="button" onClick={onCancel} disabled={busy}>
          cancel
        </button>
        <button type="button" onClick={onForce} disabled={busy}>
          accept anyway (--force)
        </button>
      </div>
    </div>
  );
}

export default function App() {
  const [app, setApp] = useUrlParam('app');
  const [flow, setFlow] = useUrlParam('flow');
  // The open RUN, if any. The navigation level is derived from which of
  // app/flow/run are set: none -> queue, app+flow -> that surface's runs
  // list, app+flow+run -> that run's own detail. A deep link naming any of
  // them survives a refresh (the Go side's SPA fallback) and lands on the
  // right level.
  const [run, setRun] = useUrlParam('run');
  const [version, setVersion] = useState(0);
  const [showHelp, setShowHelp] = useState(false);
  // Owned here, not inside RetraceQueueList: j/k must walk the rows that are
  // ON SCREEN, and "is the passing group expanded" is half of that answer.
  const [showPassing, setShowPassing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [picker, setPicker] = useState<{ entry: Entry; field: FieldDiff } | null>(null);
  const [redactPicker, setRedactPicker] = useState<{ entry: Entry; field: FieldDiff } | null>(null);
  // The secret-scan refusal for the accept the reviewer just attempted —
  // non-null renders SecretScanPanel, whose force button re-runs the same
  // accept with force: true. Cleared on cancel, on success, and on flow
  // change.
  const [secretGate, setSecretGate] = useState<SecretFinding[] | null>(null);
  const [selectedField, setSelectedField] = useState<string | null>(null);
  const [showSyncPanel, setShowSyncPanel] = useState(false);
  // The queue-level keyboard highlight: which row j/k has landed on. A
  // HIGHLIGHT, not navigation — arrows move it, enter opens it.
  const [highlightKey, setHighlightKey] = useState<string | null>(null);

  // The navigation level, derived — never stored, so it can never disagree
  // with the URL.
  const level: 'queue' | 'surface' | 'run' = app && flow && run ? 'run' : app && flow ? 'surface' : 'queue';

  // The queue filter lives in the URL like every other bit of view state in
  // this app (see urlState) — so a filtered queue is a shareable link and
  // survives a refresh.
  const [sourceParam, setSourceParam] = useUrlParam('source');
  const [appParam, setAppParam] = useUrlParam('buildApp');
  const filter: QueueFilter = {
    source: sourceParam === 'local' || sourceParam === 'ci' ? sourceParam : undefined,
    app: appParam ?? undefined,
  };
  const setFilter = (next: QueueFilter) => {
    setSourceParam(next.source ?? null);
    setAppParam(next.app ?? null);
  };

  // Every fetch in this app goes through useAsync — the queue load, the item
  // load, and the post-mutation refetch, which is this same hook with
  // `version` in its deps. There is no hand-rolled cancellation anywhere.
  const queue = useAsync(() => client.queue(filter), [version, filter.source, filter.app]);
  const items = queue.data?.items ?? [];
  // The app chips need the FULL set of apps for the current source, not the
  // already app-filtered `items` — once a reviewer picks one app, `items`
  // only ever contains that app, and deriving the chip list from it would
  // make every other app's chip disappear. Only fetched when an app filter
  // is actually set; otherwise `items` already is that full set.
  const appsForChips = useAsync(
    () => (filter.app ? client.queue({ source: filter.source }) : Promise.resolve(null)),
    [version, filter.source, filter.app],
  );
  const apps = Array.from(new Set((filter.app ? appsForChips.data?.items : items)?.map((i) => i.app) ?? [])).sort();

  // Fetches the run-scoped summary — the specific run named by ?run=, not
  // "latest". Only fires at the run level.
  const item = useAsync(async () => {
    if (level !== 'run' || !app || !flow || !run) return null;
    return (await client.itemAtRun(app, flow, run)).summary;
  }, [level, app, flow, run, version]);

  // --- navigation. Every transition writes URL params (via useUrlParam), so
  // the level is always derivable from the URL and every view is a deep
  // link. Notice/error/selection are flow-scoped and cleared on any move.
  const clearTransient = () => {
    setSelectedField(null);
    setNotice(null);
    setActionError(null);
    setSecretGate(null);
  };
  const openSurface = (next: { app: string; flow: string }) => {
    setApp(next.app);
    setFlow(next.flow);
    setRun(null);
    clearTransient();
  };
  const openRun = (runId: string) => {
    setRun(runId);
    clearTransient();
  };
  const backToQueue = () => {
    setApp(null);
    setFlow(null);
    setRun(null);
    clearTransient();
  };
  const backToSurface = () => {
    setRun(null);
    clearTransient();
  };

  const mutate = async (label: string, fn: () => Promise<string>) => {
    setBusy(true);
    setActionError(null);
    setNotice(null);
    try {
      setNotice(await fn());
      setVersion((v) => v + 1);
    } catch (err) {
      setActionError(messageOf(err, `${label} failed`));
    } finally {
      setBusy(false);
    }
  };

  // Accept in both of its forms: the plain attempt, and — after the server
  // refused it over the secret scan — the forced retry the SecretScanPanel's
  // button fires.
  const doAccept = (force: boolean) => {
    if (!app || !flow || busy) return;
    void mutate('accept', async () => {
      try {
        const res = await api.accept(app, flow, force || undefined);
        setSecretGate(null);
        return acceptNotice(app, flow, res.bundle);
      } catch (err) {
        const findings = secretFindingsOf(err);
        if (findings && !force) setSecretGate(findings);
        throw err;
      }
    });
  };

  const onAction = (action: Action) => {
    if (action === 'help') {
      setShowHelp((v) => !v);
      return;
    }
    if (picker !== null || redactPicker !== null || secretGate !== null || showSyncPanel) {
      if (action === 'back') {
        setPicker(null);
        setRedactPicker(null);
        setSecretGate(null);
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
        // Queue level only: j/k move the HIGHLIGHT over the surfaces on
        // screen (the passing group may be collapsed).
        if (level !== 'queue') return;
        const rows = visibleRows(items, showPassing);
        if (rows.length === 0) return;
        const at = rows.findIndex((i) => keyOf(i) === highlightKey);
        const step = action === 'next' ? 1 : -1;
        const nextAt = at < 0 ? 0 : Math.min(rows.length - 1, Math.max(0, at + step));
        setHighlightKey(keyOf(rows[nextAt]));
        return;
      }
      case 'open': {
        // Enter opens the highlighted surface's runs list.
        if (level !== 'queue') return;
        const rows = visibleRows(items, showPassing);
        const row = rows.find((r) => keyOf(r) === highlightKey) ?? rows[0];
        if (row) openSurface(row);
        return;
      }
      case 'back':
        // Step up exactly one level: run -> surface -> queue.
        if (level === 'run') backToSurface();
        else if (level === 'surface') backToQueue();
        return;
      case 'accept':
        // Gated on the RUN level, and that gate is the point. Note this
        // always promotes the LATEST run for the surface (api.accept takes
        // no runId) — the same "latest" a fresh queue click always opened
        // before runs history existed.
        if (level !== 'run' || !app || !flow || busy) return;
        doAccept(false);
        return;
      case 'reject':
        // Same gate, same reason: reject removes and rewrites a directory.
        if (level !== 'run' || !app || !flow || busy) return;
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
        <button type="button" className="app-header__brand" onClick={backToQueue}>
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
        {/* The breadcrumb is the inline back/forward — shown at every level
            below the queue, each earlier segment clickable to step up. */}
        {level !== 'queue' ? (
          <RetraceBreadcrumb
            app={app}
            flow={flow}
            runLabel={
              level === 'run' && item.data ? formatWhen(item.data.b.manifest?.finishedAt, item.data.b.runId) : null
            }
            onQueue={backToQueue}
            onSurface={backToSurface}
          />
        ) : null}

        {level === 'run' ? (
          item.loading ? (
            <p className="loading">
              <Spinner /> loading {app}/{flow}…
            </p>
          ) : item.error ? (
            <Problem message={item.error.message} />
          ) : item.data && app && flow && run ? (
            <RetraceItemScreen
              key={`${app}/${flow}/${run}`}
              client={client}
              app={app}
              flow={flow}
              summary={item.data}
              selectedField={selectedField}
              onSelectField={(entry, field) =>
                setSelectedField(`${entryKey(entry)}|${field.scope}:${field.path}`)
              }
              resolveShotUrl={(a, f, side, name) =>
                client.shotUrlAtRun(a, f, run, side as 'a' | 'b' | 'diff' | 'overlay', name)
              }
              onReveal={() => client.itemAtRun(app, flow, run).then((r) => r.summary.sections)}
              onBack={backToSurface}
            />
          ) : (
            <p className="loading">Nothing selected.</p>
          )
        ) : level === 'surface' && app && flow ? (
          <RetraceRunsList client={client} app={app} flow={flow} selectedRun={run} onOpenRun={openRun} />
        ) : queue.loading ? (
          <p className="loading">
            <Spinner /> loading the review queue…
          </p>
        ) : queue.error ? (
          <Problem message={queue.error.message} />
        ) : queue.data ? (
          <>
            <RetraceQueueFilters apps={apps} filter={filter} onChange={setFilter} />
            <RetraceQueueList
              items={items}
              empty={queue.data.empty}
              selected={highlightKey}
              showPassing={showPassing}
              onShowPassingChange={setShowPassing}
              onSelect={(next) => setHighlightKey(keyOf(next))}
              onOpen={openSurface}
            />
          </>
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

      {secretGate && app && flow ? (
        <SecretScanPanel
          findings={secretGate}
          busy={busy}
          onCancel={() => setSecretGate(null)}
          onForce={() => doAccept(true)}
        />
      ) : null}

      {showSyncPanel ? (
        <RetraceSyncPanel
          client={client}
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
