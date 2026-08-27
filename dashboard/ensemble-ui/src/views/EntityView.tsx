// Generic entity CRUD over whatever /api/entities/{name} passes through to
// its configured upstream. Deliberately knows NOTHING about any particular
// entity's shape: list columns are the union of keys actually seen on the
// returned rows (see format.ts's unionKeys), and create/edit are raw-JSON
// textareas. Navigation (?entity=&id=&new=1) is plain useUrlParam state —
// no router — matching the rest of the dashboard.
import { useEffect, useRef, useState } from 'react';
import { Badge, Spinner, Tabs } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import { buildExecCommand, renderCellValue, resolveLinkTemplate, unionKeys } from '../format';
import { useUrlParam } from '../urlState';
import JsonView from '../components/JsonView';
import InlineError from '../components/InlineError';
import type { EntityLink } from '../api/types';
import './EntityView.css';

const MUTATION_NOTE =
  "Mutations here only show up in Traffic when this entity's configured base points at an ensemble intercept port — a raw upstream base leaves them unrecorded.";

function useEntities() {
  const { data: entities, error } = useAsync(() => api.entities(), []);
  return { entities, error: error ? messageOf(error, 'failed to reach the ensemble API') : null };
}

/** GET /api/entities/{name}'s body is whatever the upstream returns —
 * only a bare JSON array of plain objects renders as a table; anything
 * else (a wrapper object, a scalar, a non-object element) falls back to
 * raw JSON rather than guessing at an API-specific envelope shape. */
function asRowArray(data: unknown): Record<string, unknown>[] | null {
  if (!Array.isArray(data)) return null;
  if (!data.every((el) => el !== null && typeof el === 'object' && !Array.isArray(el))) return null;
  return data as Record<string, unknown>[];
}

/** Opens a resolved link's URL: http(s) targets open in a new tab (`noopener` so the
 * opened host app can't reach back into `window.opener`); anything else — a custom scheme
 * like `acmewallet://` — navigates the current page instead, since `window.open` silently
 * no-ops on non-http(s) schemes in most browsers. */
function openResolvedLink(url: string) {
  if (/^https?:\/\//i.test(url)) {
    window.open(url, '_blank', 'noopener');
  } else {
    window.location.assign(url);
  }
}

function extractId(data: unknown, idField: string): string | null {
  if (data !== null && typeof data === 'object' && !Array.isArray(data)) {
    const v = (data as Record<string, unknown>)[idField];
    if (v !== undefined && v !== null) return String(v);
  }
  return null;
}

/** Shared "copied"/"copy failed" feedback state machine: an async copy action plus a
    self-clearing status pill (~1s). Used by both CopyButton (per-cell) and ExecLinkButton
    (per-row exec link) — each caller gets its own hook instance, so many of these can be on
    screen at once without sharing state, matching the toast-per-instance pattern below. */
function useCopyFeedback() {
  const [status, setStatus] = useState<'idle' | 'copied' | 'failed'>('idle');
  const idleTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (idleTimerRef.current !== null) window.clearTimeout(idleTimerRef.current);
    };
  }, []);

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setStatus('copied');
    } catch {
      setStatus('failed');
    } finally {
      if (idleTimerRef.current !== null) window.clearTimeout(idleTimerRef.current);
      idleTimerRef.current = window.setTimeout(() => {
        idleTimerRef.current = null;
        setStatus('idle');
      }, 1000);
    }
  }

  return { status, copy };
}

/** A small copy-to-clipboard icon after one cell's value — its own click must not bubble
    into the row's onClick (which opens the record). The "copied"/"copy failed" toast is
    this button's own tooltip-like bubble rather than a page-level toast: many of these can
    exist on screen at once (one per cell), so each tracks its own feedback independently. */
function CopyButton({ value, label }: { value: string; label: string }) {
  const { status, copy } = useCopyFeedback();

  return (
    <span className="entity-table__copy">
      <button
        type="button"
        className="entity-table__copy-btn"
        aria-label={`copy ${label}`}
        title={`copy ${label}`}
        onClick={(e) => {
          e.stopPropagation();
          void copy(value);
        }}
      >
        <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true">
          <rect x="5.5" y="5.5" width="8" height="8" rx="1.3" fill="none" stroke="currentColor" strokeWidth="1.3" />
          <path d="M2.7 10V3.3A1.3 1.3 0 0 1 4 2h6.7" fill="none" stroke="currentColor" strokeWidth="1.3" />
        </svg>
      </button>
      {status !== 'idle' && (
        <span className="entity-table__copy-toast" role="status">
          {status === 'copied' ? 'copied' : 'copy failed'}
        </span>
      )}
    </span>
  );
}

/** One `kind: exec` link's button for one row: builds the local CLI command (see
    format.ts's buildExecCommand) and copies it to the clipboard on click, rather than
    navigating like a `kind: url` link. The full command is always the button's title —
    a developer should never be asked to paste something they haven't been shown — and a
    row that can't produce a safe/complete command (missing template column, a control
    character in the resolved command) renders the button disabled with that reason as its
    title instead of ever copying something wrong. */
function ExecLinkButton({ link, row }: { link: EntityLink; row: Record<string, unknown> }) {
  const result = buildExecCommand(link, row);
  const { status, copy } = useCopyFeedback();

  if ('disabledReason' in result) {
    return (
      <button type="button" className="entity-table__link-btn" disabled title={result.disabledReason}>
        {link.label}
      </button>
    );
  }

  return (
    <span className="entity-table__link-wrap">
      <button
        type="button"
        className="entity-table__link-btn"
        title={result.command}
        onClick={() => void copy(result.command)}
      >
        {link.label}
      </button>
      {status !== 'idle' && (
        <span className="entity-table__copy-toast" role="status">
          {status === 'copied' ? 'copied' : 'copy failed'}
        </span>
      )}
    </span>
  );
}

function EntityList({
  name,
  idField,
  links,
  onSelectRow,
  onCreate,
}: {
  name: string;
  idField: string;
  links: EntityLink[];
  onSelectRow: (id: string) => void;
  onCreate: () => void;
}) {
  // See EntityDetail: `undefined` alone cannot distinguish "not fetched yet" from
  // "fetched, and the upstream sent an empty body" — useAsync's `loading` still makes that
  // distinction, same as the hand-rolled version did.
  const { data, error, loading } = useAsync(() => api.entityList(name), [name]);

  if (error) {
    return <InlineError message={messageOf(error, `failed to load ${name}`)} />;
  }

  if (loading) {
    return (
      <div className="entity-list__loading">
        <Spinner />
      </div>
    );
  }

  if (data === undefined) {
    return <div className="entity-view__panel-empty">{name} returned an empty response body.</div>;
  }

  const rows = asRowArray(data);
  // Computed once for the whole table, not per row — unionKeys is O(rows × keys), so calling
  // it again inside rows.map would run it once per row, O(n²) overall (final review M6).
  const keys = rows ? unionKeys(rows) : [];

  return (
    <div className="entity-list">
      <div className="entity-list__toolbar">
        <span className="entity-list__count">
          {rows ? `${rows.length} row${rows.length === 1 ? '' : 's'}` : 'unshaped response'}
        </span>
        <Badge tone="neutral">id field: {idField}</Badge>
        <button type="button" className="entity-view__primary" onClick={onCreate}>
          + create
        </button>
      </div>
      {!rows ? (
        <div className="entity-list__unshaped">
          <p>
            The upstream response for &quot;{name}&quot; isn&apos;t a JSON array of objects, so it can&apos;t be
            rendered as a table. Showing it raw:
          </p>
          <JsonView body={JSON.stringify(data, null, 2)} />
        </div>
      ) : rows.length === 0 ? (
        <p className="entity-list__empty">no rows</p>
      ) : (
        <div className="entity-list__scroll">
          <table className="entity-table">
            <thead>
              <tr>
                {keys.map((k) => (
                  <th key={k}>{k}</th>
                ))}
                {links.length > 0 && <th>links</th>}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                const rid = extractId(row, idField);
                return (
                  <tr
                    key={rid ?? JSON.stringify(row)}
                    className={`entity-table__row${rid ? '' : ' entity-table__row--no-id'}`}
                    onClick={() => rid && onSelectRow(rid)}
                    title={rid ? undefined : `row is missing its "${idField}" field — detail view unavailable`}
                  >
                    {keys.map((k) => {
                      const raw = row[k];
                      const text = renderCellValue(raw);
                      return (
                        <td key={k}>
                          <span className="entity-table__cell">
                            <span className="entity-table__cell-value">{text}</span>
                            {raw !== null && raw !== undefined && <CopyButton value={text} label={k} />}
                          </span>
                        </td>
                      );
                    })}
                    {links.length > 0 && (
                      <td onClick={(e) => e.stopPropagation()}>
                        {links.map((link) =>
                          link.kind === 'exec' ? (
                            <ExecLinkButton key={link.label} link={link} row={row} />
                          ) : (
                            <button
                              key={link.label}
                              type="button"
                              className="entity-table__link-btn"
                              onClick={() => openResolvedLink(resolveLinkTemplate(link.template, row))}
                            >
                              {link.label}
                            </button>
                          ),
                        )}
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function EntityDetail({
  name,
  id,
  onDeleted,
  onBack,
}: {
  name: string;
  id: string;
  onDeleted: () => void;
  onBack: () => void;
}) {
  // EntityDetail is NOT remounted when id changes (EntityView renders it without a
  // key prop), so this load re-fires on the same instance and a previous in-flight
  // request would otherwise still be live. useAsync's generation guard is what keeps a
  // stale response from overwriting both the rendered record and draftText — and save()
  // PUTs draftText against the CURRENT id, so that used to write one row's data onto
  // another. Pinned by EntityView.detail-race.test.ts.
  //
  // `version` replaces the old save()'s direct `setData(result)`: useAsync hands back no
  // setter, so a mutation can't write its own response into `data` without stranding it the
  // next time [name, id] changes — bumping `version` instead re-triggers this same load,
  // same pattern as LatencyView's mutations and Task 15's `mutate`. The reload is the
  // source of truth; the PUT's own response is discarded once it has done its job.
  //
  // WHAT THAT COSTS, STATED HERE RATHER THAN LEFT TO BE REDISCOVERED (re-review N8).
  // `useAsync` clears `data` on EVERY deps change, and the render below is a single chained
  // ternary in which the `loading` branch precedes the record branch — so bumping `version`
  // replaces the record on screen with a bare `<Spinner/>` for the whole duration of a
  // refetch of the SAME record. This is the one `useAsync` site in the package that re-fires
  // for an unchanged resource without keeping its last good value beside it; the four
  // polling sites (App's health strip, ServicesView, TopologyView's topology and profiles,
  // InspectorView's rows) each hold a sticky snapshot for exactly that reason (final review
  // F2). Round 2's enumeration said "no fifth F2 site" — which is true of the ERROR half and
  // not of the data half, and this is the site the difference lands on.
  //
  // Left as it is, on purpose. Those four re-fire unattended on a 5s timer, where a periodic
  // flash back to a spinner is precisely the defect F2 was raised for. This one fires only
  // from `save()`, one moment after the user pressed Save, where a brief spinner reads as an
  // acknowledgement and is arguably better than showing a record already known to be stale.
  // The error half genuinely is unreachable here, and by construction rather than by luck:
  // `save()` is reachable only through the editor, the editor is gated on `hasRecord`, and
  // `hasRecord` requires a load that succeeded — so no `version` bump can clear an error
  // that is on screen.
  //
  // If the spinner is ever judged not worth it, the fix is a keep-previous-data option on
  // `useAsync` itself, which would delete this clause and all four bespoke sticky-snapshot
  // wrappers together. A fifth bespoke wrapper here would move the opposite way, so there
  // deliberately is not one.
  const [version, setVersion] = useState(0);
  const { data, error: loadError, loading } = useAsync(() => api.entityGet(name, id), [name, id, version]);

  // Tracked separately from `data`: a 200 with an empty body also yields undefined,
  // and overloading one sentinel for "not fetched yet" and "fetched nothing" showed
  // the user a permanent spinner with no error.
  const [editing, setEditing] = useState(false);
  const [draftText, setDraftText] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // `data` is `null` for BOTH "still loading" and "the load already settled to a real
  // `null` body" — `loading` is what tells those apart. Gating on this (rather than on
  // `data !== undefined` alone, per useAsync's contract) is what keeps `edit`/`delete`
  // from firing mid-load (final review F3) and keeps the draft/cancel effects below from
  // writing the loading sentinel into the textarea as the literal string "null" (F12).
  const hasRecord = !loading && data !== undefined;

  useEffect(() => {
    if (hasRecord) setDraftText(JSON.stringify(data, null, 2) ?? '');
  }, [data, hasRecord]);

  // actionError (a failed delete) has no relation to which record is on screen — it must
  // not outlive the record it was raised against. EntityDetail is deliberately NOT
  // remounted when `?id=` changes (see the comment above), so without this a failed
  // delete on row 1 would keep replacing every row selected afterwards with "failed to
  // delete" for the rest of the view's life (final review F1). Pre-migration this reset
  // came for free from the shared `error` state's load effect; useAsync owns only the
  // load half now, so the action half needs its own reset, keyed the same way.
  useEffect(() => {
    setActionError(null);
  }, [name, id]);

  // The original single `error` state served both a load failure and a delete failure —
  // useAsync now owns the load half, so a delete failure gets its own state, combined the
  // same way for rendering: either one replaces the detail view with an error banner.
  const error = loadError ? messageOf(loadError, `failed to load ${name}/${id}`) : actionError;

  async function save() {
    let parsed: unknown;
    try {
      parsed = JSON.parse(draftText);
    } catch {
      setFormError('invalid JSON');
      return;
    }
    setBusy(true);
    try {
      await api.entityUpdate(name, id, parsed);
      setEditing(false);
      setFormError(null);
      setVersion((v) => v + 1);
    } catch (err) {
      setFormError(messageOf(err, 'failed to save'));
    } finally {
      setBusy(false);
    }
  }

  async function del() {
    if (!window.confirm(`Delete ${name}/${id}? This cannot be undone.`)) return;
    setBusy(true);
    try {
      await api.entityDelete(name, id);
      onDeleted();
    } catch (err) {
      setActionError(messageOf(err, 'failed to delete'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="entity-detail">
      <div className="entity-detail__toolbar">
        <button type="button" onClick={onBack}>
          ← back to {name}
        </button>
        <span className="entity-detail__id">
          {name}/{id}
        </span>
        {hasRecord && !error && !editing && (
          <button type="button" onClick={() => setEditing(true)} disabled={busy}>
            edit
          </button>
        )}
        {hasRecord && !error && (
          <button type="button" className="entity-view__danger" onClick={() => void del()} disabled={busy}>
            delete
          </button>
        )}
      </div>
      <p className="entity-view__hint">{MUTATION_NOTE}</p>
      {error ? (
        <InlineError message={error} />
      ) : loading ? (
        <Spinner />
      ) : data === undefined ? (
        <div className="entity-view__panel-empty">
          {name}/{id} returned an empty response body.
        </div>
      ) : editing ? (
        <div className="entity-detail__edit">
          {formError && <div className="entity-detail__form-error">{formError}</div>}
          <textarea
            className="entity-detail__textarea"
            value={draftText}
            onChange={(e) => setDraftText(e.target.value)}
            spellCheck={false}
          />
          <div className="entity-detail__edit-actions">
            <button type="button" onClick={() => void save()} disabled={busy}>
              save
            </button>
            <button
              type="button"
              onClick={() => {
                setEditing(false);
                if (hasRecord) setDraftText(JSON.stringify(data, null, 2));
                setFormError(null);
              }}
              disabled={busy}
            >
              cancel
            </button>
          </div>
        </div>
      ) : (
        <JsonView body={JSON.stringify(data, null, 2)} />
      )}
    </div>
  );
}

function EntityCreate({
  name,
  idField,
  onCreated,
  onCancel,
}: {
  name: string;
  idField: string;
  onCreated: (id: string | null) => void;
  onCancel: () => void;
}) {
  const [text, setText] = useState('{\n  \n}\n');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function create() {
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      setError('invalid JSON');
      return;
    }
    setBusy(true);
    try {
      const result = await api.entityCreate(name, parsed);
      setError(null);
      onCreated(extractId(result, idField) ?? extractId(parsed, idField));
    } catch (err) {
      setError(messageOf(err, 'failed to create'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="entity-create">
      <div className="entity-detail__toolbar">
        <span className="entity-detail__id">create {name}</span>
        <button type="button" onClick={onCancel} disabled={busy}>
          cancel
        </button>
      </div>
      <p className="entity-view__hint">{MUTATION_NOTE}</p>
      {error && (
        <InlineError message={error} />
      )}
      <textarea
        className="entity-detail__textarea"
        value={text}
        onChange={(e) => setText(e.target.value)}
        spellCheck={false}
      />
      <div className="entity-detail__edit-actions">
        <button type="button" onClick={() => void create()} disabled={busy}>
          create
        </button>
      </div>
    </div>
  );
}

export default function EntityView() {
  const { entities, error: listError } = useEntities();
  const [entityParam, setEntityParam] = useUrlParam('entity');
  const [idParam, setId] = useUrlParam('id');
  const [newParam, setNew] = useUrlParam('new');

  const activeEntity =
    entityParam && entities?.some((e) => e.name === entityParam) ? entityParam : (entities?.[0]?.name ?? null);
  const activeInfo = entities?.find((e) => e.name === activeEntity) ?? null;

  // An ?entity= that names nothing falls back to the first entity, but a leftover ?id=
  // would then be read against that fallback — leaving the URL, the breadcrumb, and the
  // rendered row disagreeing. Drop the whole stale selection, exactly as selectEntity
  // does when the user switches tabs by hand. Self-terminating: clearing entityParam
  // makes the guard below false on the next pass.
  useEffect(() => {
    if (!entities || !entityParam) return;
    if (entities.some((e) => e.name === entityParam)) return;
    setEntityParam(null);
    setId(null);
    setNew(null);
  }, [entities, entityParam, setEntityParam, setId, setNew]);

  function selectEntity(nextName: string) {
    setEntityParam(nextName);
    setId(null);
    setNew(null);
  }

  if (listError) {
    return (
      <div className="entity-view entity-view--error">
        <Badge tone="red">offline</Badge>
        <span>{listError}</span>
      </div>
    );
  }

  if (entities === null) {
    return (
      <div className="entity-view entity-view--loading">
        <Spinner />
        <span>loading entities…</span>
      </div>
    );
  }

  if (entities.length === 0) {
    return (
      <div className="entity-view entity-view--empty">
        <span>no entities are configured for this stack</span>
      </div>
    );
  }

  // `??` only coalesces null/undefined — an entity configured with no `id:` (a valid config;
  // config.Validate never required it) comes back as `id: ""` from the server, which `??`
  // lets straight through, making idField `""` and detail/edit/delete unreachable for every
  // row (final review I4). `||` also catches that empty-string case.
  const idField = activeInfo?.id || 'id';

  return (
    <div className="entity-view">
      <Tabs
        items={entities.map((e) => ({ id: e.name, label: e.name }))}
        active={activeEntity ?? entities[0].name}
        onSelect={selectEntity}
      />
      <div className="entity-view__body">
        {activeEntity &&
          (newParam === '1' ? (
            <EntityCreate
              name={activeEntity}
              idField={idField}
              onCreated={(id) => {
                setNew(null);
                setId(id);
              }}
              onCancel={() => setNew(null)}
            />
          ) : idParam ? (
            <EntityDetail
              name={activeEntity}
              id={idParam}
              onDeleted={() => setId(null)}
              onBack={() => setId(null)}
            />
          ) : (
            <EntityList
              name={activeEntity}
              idField={idField}
              links={activeInfo?.links ?? []}
              onSelectRow={(id) => setId(id)}
              onCreate={() => setNew('1')}
            />
          ))}
      </div>
    </div>
  );
}
