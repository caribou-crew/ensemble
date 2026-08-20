// Generic entity CRUD over whatever /api/entities/{name} passes through to
// its configured upstream. Deliberately knows NOTHING about any particular
// entity's shape: list columns are the union of keys actually seen on the
// returned rows (see format.ts's unionKeys), and create/edit are raw-JSON
// textareas. Navigation (?entity=&id=&new=1) is plain useUrlParam state —
// no router — matching the rest of the dashboard.
import { useEffect, useState } from 'react';
import { Badge, Spinner, Tabs } from '@ensemble/design-system';
import { api, ApiError } from '../api/client';
import type { EntityInfo } from '../api/types';
import { renderCellValue, unionKeys } from '../format';
import { useUrlParam } from '../urlState';
import JsonView from '../components/JsonView';
import './EntityView.css';

const MUTATION_NOTE =
  "Mutations here only show up in Traffic when this entity's configured base points at an ensemble intercept port — a raw upstream base leaves them unrecorded.";

function messageOf(err: unknown, fallback: string): string {
  return err instanceof ApiError ? err.message : fallback;
}

function useEntities() {
  const [entities, setEntities] = useState<EntityInfo[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .entities()
      .then((r) => {
        if (!cancelled) {
          setEntities(r);
          setError(null);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(messageOf(err, 'failed to reach the ensemble API'));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { entities, error };
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
  const [data, setData] = useState<unknown>(undefined);
  // See EntityDetail: `undefined` alone cannot distinguish "not fetched yet" from
  // "fetched, and the upstream sent an empty body".
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setData(undefined);
    setError(null);
    api
      .entityList(name)
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(messageOf(err, `failed to load ${name}`));
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [name]);

  if (error) {
    return (
      <div className="entity-view__panel-error">
        <Badge tone="red">error</Badge>
        <span>{error}</span>
      </div>
    );
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
                {unionKeys(rows).map((k) => (
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
                    {unionKeys(rows).map((k) => (
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
  const [data, setData] = useState<unknown>(undefined);
  // Tracked separately from `data`: a 200 with an empty body also yields undefined,
  // and overloading one sentinel for "not fetched yet" and "fetched nothing" showed
  // the user a permanent spinner with no error.
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [draftText, setDraftText] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // EntityDetail is NOT remounted when id changes (EntityView renders it without a
  // key prop), so this effect re-fires on the same instance and a previous in-flight
  // request would otherwise still be live. Without the cancelled guard a stale
  // response overwrites both the rendered record and draftText — and save() PUTs
  // draftText against the CURRENT id, so that writes one row's data onto another.
  // Guarded here and pinned by EntityView.detail-race.test.ts.
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setData(undefined);
    setError(null);
    api
      .entityGet(name, id)
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setDraftText(JSON.stringify(d, null, 2) ?? '');
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(messageOf(err, `failed to load ${name}/${id}`));
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [name, id]);

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
      const result = await api.entityUpdate(name, id, parsed);
      setData(result ?? parsed);
      setEditing(false);
      setFormError(null);
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
      setError(messageOf(err, 'failed to delete'));
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
        <div className="entity-view__panel-error">
          <Badge tone="red">error</Badge>
          <span>{error}</span>
        </div>
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
        <div className="entity-view__panel-error">
          <Badge tone="red">error</Badge>
          <span>{error}</span>
        </div>
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

  const idField = activeInfo?.id ?? 'id';

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
