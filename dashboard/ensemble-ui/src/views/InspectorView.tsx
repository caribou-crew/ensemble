// DB inspector: pick a database -> browse its schema -> page through one
// table's rows, staying live via /api/inspector/stream. Mirrors
// TopologyView/App's "compute a fallback from the URL param, don't force a
// write" idiom for ?db=&table= — an invalid/missing param just falls back
// to the first available option rather than erroring.
import { useCallback, useEffect, useRef, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { api, ApiError, messageOf } from '../api/client';
import { subscribeChanges } from '../api/sse';
import type { Column, DatabaseInfo, Table } from '../api/types';
import { renderCellValue } from '../format';
import { useUrlParam } from '../urlState';
import InlineError from '../components/InlineError';
import './InspectorView.css';

const ROWS_LIMIT = 50;
/** How long a changed table stays visually flashed in the sidebar. */
const FLASH_MS = 1200;

function useDatabases() {
  const [databases, setDatabases] = useState<DatabaseInfo[] | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .databases()
      .then((dbs) => {
        if (cancelled) return;
        setDatabases(dbs);
        setError(null);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 501) {
          setUnavailable(true);
          setDatabases([]);
        } else {
          setError(messageOf(err, 'failed to reach the ensemble API'));
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { databases, unavailable, error };
}

function useSchema(db: string | null) {
  const [tables, setTables] = useState<Table[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!db) {
      setTables(null);
      setError(null);
      return;
    }
    let cancelled = false;
    setTables(null);
    api
      .databaseSchema(db)
      .then((t) => {
        if (cancelled) return;
        setTables(t);
        setError(null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(messageOf(err, `failed to load schema for ${db}`));
      });
    return () => {
      cancelled = true;
    };
  }, [db]);

  return { tables, error };
}

function useRows(db: string | null, table: string | null, limit: number, offset: number) {
  const [rows, setRows] = useState<Record<string, unknown>[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [refreshToken, setRefreshToken] = useState(0);

  // Clears `rows` when db/table/offset actually change (a stale table's rows must never
  // render under a different table's just-switched headers — final review I1) but NOT on the
  // SSE-driven `refreshToken` bump alone, which deliberately wants to stay flicker-free for
  // the currently-selected table.
  const keyRef = useRef('');

  useEffect(() => {
    if (!db || !table) {
      keyRef.current = '';
      setRows(null);
      setError(null);
      return;
    }
    const key = `${db} ${table} ${offset}`;
    if (keyRef.current !== key) {
      keyRef.current = key;
      setRows(null);
    }
    setError(null); // an error must never outlive the request that produced it
    let cancelled = false;
    setLoading(true);
    api
      .databaseRows(db, table, limit, offset)
      .then((r) => {
        if (cancelled) return;
        setRows(r);
        setError(null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(messageOf(err, `failed to load rows for ${table}`));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // refreshToken is a manual re-fetch trigger (SSE-driven), not itself data.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [db, table, limit, offset, refreshToken]);

  const refresh = useCallback(() => setRefreshToken((t) => t + 1), []);

  return { rows, error, loading, refresh };
}

function columnsFor(tables: Table[] | null, table: string | null): Column[] {
  if (!tables || !table) return [];
  return tables.find((t) => t.name === table)?.columns ?? [];
}

export default function InspectorView() {
  const { databases, unavailable, error: dbError } = useDatabases();
  const [dbParam, setDb] = useUrlParam('db');
  const [tableParam, setTable] = useUrlParam('table');

  const activeDb = dbParam && databases?.some((d) => d.name === dbParam) ? dbParam : (databases?.[0]?.name ?? null);
  const { tables, error: schemaError } = useSchema(activeDb);
  const activeTable =
    tableParam && tables?.some((t) => t.name === tableParam) ? tableParam : (tables?.[0]?.name ?? null);

  // A ?db= that names nothing falls back to the first database, but a leftover ?table=
  // would then be read against that fallback's schema — leaving the URL, and any link copied
  // from the address bar, disagreeing with what's on screen. Drop the whole stale selection,
  // mirroring EntityView.tsx:413-419's identical ?entity=/?id= fix (final review I2).
  // Self-terminating: clearing dbParam makes the guard below false on the next pass.
  useEffect(() => {
    if (!databases || !dbParam) return;
    if (databases.some((d) => d.name === dbParam)) return;
    setDb(null);
    setTable(null);
  }, [databases, dbParam, setDb, setTable]);

  // ?table= is only meaningful relative to ?db= — once that db's schema is loaded, a
  // ?table= that names nothing in it gets the same treatment, independently of the guard
  // above (a VALID ?db= can still carry a stale ?table=).
  useEffect(() => {
    if (!tables || !tableParam) return;
    if (tables.some((t) => t.name === tableParam)) return;
    setTable(null);
  }, [tables, tableParam, setTable]);

  const [offset, setOffset] = useState(0);
  const { rows, error: rowsError, loading: rowsLoading, refresh } = useRows(activeDb, activeTable, ROWS_LIMIT, offset);
  const [flashed, setFlashed] = useState<Set<string>>(new Set());

  useEffect(() => {
    setOffset(0);
  }, [activeDb, activeTable]);

  // The SSE subscription lives for the component's whole lifetime — only
  // `unavailable` flipping (at most once, right after mount) should tear it
  // down and reconnect. Everything it needs to know about "what's
  // currently selected" is read through refs so switching db/table doesn't
  // reopen the stream.
  const dbRef = useRef(activeDb);
  const tableRef = useRef(activeTable);
  const refreshRef = useRef(refresh);
  useEffect(() => {
    dbRef.current = activeDb;
  }, [activeDb]);
  useEffect(() => {
    tableRef.current = activeTable;
  }, [activeTable]);
  useEffect(() => {
    refreshRef.current = refresh;
  }, [refresh]);

  useEffect(() => {
    if (unavailable) return;
    const unsubscribe = subscribeChanges((ev) => {
      if (ev.db !== dbRef.current) return;
      setFlashed((cur) => {
        const next = new Set(cur);
        next.add(ev.table);
        return next;
      });
      window.setTimeout(() => {
        setFlashed((cur) => {
          const next = new Set(cur);
          next.delete(ev.table);
          return next;
        });
      }, FLASH_MS);
      if (ev.table === tableRef.current) refreshRef.current();
    });
    return unsubscribe;
  }, [unavailable]);

  if (dbError) {
    return (
      <div className="inspector-view inspector-view--error">
        <Badge tone="red">offline</Badge>
        <span>{dbError}</span>
      </div>
    );
  }

  if (databases === null) {
    return (
      <div className="inspector-view inspector-view--loading">
        <Spinner />
        <span>loading databases…</span>
      </div>
    );
  }

  if (unavailable) {
    return (
      <div className="inspector-view inspector-view--empty">
        <Badge tone="neutral">unavailable</Badge>
        <span>inspection isn't configured for this stack — no database driver is wired up</span>
      </div>
    );
  }

  if (databases.length === 0) {
    return (
      <div className="inspector-view inspector-view--empty">
        <span>no inspectable databases are registered for this stack</span>
      </div>
    );
  }

  const cols = columnsFor(tables, activeTable);

  return (
    <div className="inspector-view">
      <aside className="inspector-view__sidebar">
        <select
          className="inspector-view__db-select"
          value={activeDb ?? ''}
          onChange={(e) => setDb(e.target.value)}
        >
          {databases.map((d) => (
            <option key={d.name} value={d.name}>
              {d.name} ({d.type})
            </option>
          ))}
        </select>
        {schemaError && <InlineError message={schemaError} />}
        {!tables ? (
          <Spinner />
        ) : (
          <ul className="inspector-view__tables">
            {tables.map((t) => (
              <li key={t.name}>
                <button
                  type="button"
                  className={`inspector-view__table-btn${activeTable === t.name ? ' inspector-view__table-btn--active' : ''}${
                    flashed.has(t.name) ? ' inspector-view__table-btn--flash' : ''
                  }`}
                  onClick={() => setTable(t.name)}
                  title={t.columns.map((c) => `${c.name}: ${c.type}`).join(', ')}
                >
                  <span>{t.name}</span>
                  <span className="inspector-view__col-count">{t.columns.length}</span>
                </button>
              </li>
            ))}
            {tables.length === 0 && <li className="inspector-view__no-tables">no tables</li>}
          </ul>
        )}
      </aside>
      <div className="inspector-view__rows">
        <div className="inspector-view__rows-toolbar">
          <span className="inspector-view__rows-table">{activeTable ?? '—'}</span>
          <div className="inspector-view__paging">
            <button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - ROWS_LIMIT))}>
              prev
            </button>
            <span className="inspector-view__paging-range">
              {rows && rows.length > 0 ? `${offset + 1}–${offset + rows.length}` : '—'}
            </span>
            <button
              type="button"
              disabled={!rows || rows.length < ROWS_LIMIT}
              onClick={() => setOffset(offset + ROWS_LIMIT)}
            >
              next
            </button>
            <button type="button" onClick={refresh} disabled={rowsLoading || !activeTable}>
              refresh
            </button>
          </div>
        </div>
        {rowsError && <InlineError message={rowsError} />}
        {rowsLoading && !rows ? (
          <div className="inspector-view__rows-loading">
            <Spinner />
          </div>
        ) : !rows || rows.length === 0 ? (
          <p className="inspector-view__no-rows">no rows</p>
        ) : (
          <div className="inspector-view__table-scroll">
            <table className="inspector-table">
              <thead>
                <tr>
                  {cols.map((c) => (
                    <th key={c.name} title={c.type}>
                      {c.name}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row, i) => (
                  <tr key={i}>
                    {cols.map((c) => (
                      <td key={c.name}>{renderCellValue(row[c.name])}</td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}
