// Generic entity CRUD over whatever /api/entities/{name} passes through to
// its configured upstream. Deliberately knows NOTHING about any particular
// entity's shape: list columns are the union of keys actually seen on the
// returned rows (see format.ts's unionKeys), and create/edit are raw-JSON
// textareas. Navigation (?entity=&id=&new=1) is plain useUrlParam state —
// no router — matching the rest of the dashboard.
import { useEffect, useState } from 'react';
import { Badge, Spinner, Tabs } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import { renderCellValue, unionKeys } from '../format';
import { useUrlParam } from '../urlState';
import JsonView from '../components/JsonView';
import InlineError from '../components/InlineError';
import './EntityView.css';

const MUTATION_NOTE =
  "Mutations here only show up in Traffic when this entity's configured base points at an ensemble intercept port — a raw upstream base leaves them unrecorded.";

function useEntities() {
  const { data: entities, error } = useAsync(() => api.entities(), []);
  return { entities, error: error?.message ?? null };
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

function extractId(data: unknown, idField: string): string | null {
  if (data !== null && typeof data === 'object' && !Array.isArray(data)) {
    const v = (data as Record<string, unknown>)[idField];
    if (v !== undefined && v !== null) return String(v);
  }
  return null;
}

function EntityList({
  name,
  idField,
  onSelectRow,
  onCreate,
}: {
  name: string;
  idField: string;
  onSelectRow: (id: string) => void;
  onCreate: () => void;
}) {
  // See EntityDetail: `undefined` alone cannot distinguish "not fetched yet" from
  // "fetched, and the upstream sent an empty body" — useAsync's `loading` still makes that
  // distinction, same as the hand-rolled version did.
  const { data, error, loading } = useAsync(() => api.entityList(name), [name]);

  if (error) {
    return <InlineError message={error.message} />;
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
                    {keys.map((k) => (
                      <td key={k}>{renderCellValue(row[k])}</td>
                    ))}
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

  useEffect(() => {
    if (data !== undefined) setDraftText(JSON.stringify(data, null, 2) ?? '');
  }, [data]);

  // The original single `error` state served both a load failure and a delete failure —
  // useAsync now owns the load half, so a delete failure gets its own state, combined the
  // same way for rendering: either one replaces the detail view with an error banner.
  const error = loadError ? loadError.message : actionError;

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
        {data !== undefined && !error && !editing && (
          <button type="button" onClick={() => setEditing(true)} disabled={busy}>
            edit
          </button>
        )}
        {data !== undefined && !error && (
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
                setDraftText(JSON.stringify(data, null, 2));
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
              onSelectRow={(id) => setId(id)}
              onCreate={() => setNew('1')}
            />
          ))}
      </div>
    </div>
  );
}
