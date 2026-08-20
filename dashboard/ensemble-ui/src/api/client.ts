// Thin typed fetch wrappers over ensemble/server's REST surface. One
// function per endpoint the dashboard calls; every non-2xx response throws
// ApiError. No caching, no retries — that belongs to whichever view needs
// it (SSE reconnection, polling, etc. land in later tasks).

import type { Hop, LatencyRule, LogicalHop, ServiceState, Topology } from './types';
import type { SeedStepResult } from './types';
import type { DatabaseInfo, EntityInfo, Table } from './types';

export class ApiError extends Error {
  readonly status: number;
  /** The parsed JSON error body, when the response had one — e.g. POST
   * /api/seed/{name}'s 500 still carries {results, ok:false, error}, and a
   * caller that wants the partial results can read it off here. */
  readonly body: unknown;

  constructor(status: number, message: string, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  const text = await res.text();
  let body: unknown;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = undefined;
    }
  }

  if (!res.ok) {
    const message =
      body && typeof body === 'object' && 'error' in body && typeof (body as { error: unknown }).error === 'string'
        ? (body as { error: string }).error
        : res.statusText || `request failed with status ${res.status}`;
    throw new ApiError(res.status, message, body);
  }

  return body as T;
}

function jsonInit(method: string, payload?: unknown): RequestInit {
  if (payload === undefined) {
    return { method };
  }
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  };
}

function query(params: Record<string, string | number | boolean | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === '') continue;
    search.set(key, String(value));
  }
  const s = search.toString();
  return s ? `?${s}` : '';
}

export interface TrafficParams {
  since?: number;
  limit?: number;
  errorsOnly?: boolean;
  session?: string;
  // Explicit index signature: passing a named/stored type (as opposed to
  // an inline object literal) to query()'s Record<string, ...> parameter
  // requires one under TS's index-signature assignability rules.
  [key: string]: string | number | boolean | undefined;
}

export interface TraceResponse {
  hops: Hop[];
  logical: LogicalHop[];
}

export interface SeedResult {
  ok: boolean;
  results: SeedStepResult[];
}

export const api = {
  status(): Promise<ServiceState[]> {
    return request<{ services: ServiceState[] }>('/api/status').then((r) => r.services);
  },

  topology(): Promise<Topology> {
    return request<Topology>('/api/topology');
  },

  traffic(params: TrafficParams = {}): Promise<Hop[]> {
    return request<{ hops: Hop[] }>(`/api/traffic${query(params)}`).then((r) => r.hops);
  },

  trace(id: string): Promise<TraceResponse> {
    return request<TraceResponse>(`/api/traces/${encodeURIComponent(id)}`);
  },

  latencyList(): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>('/api/latency').then((r) => r.rules);
  },

  latencyUpsert(rule: LatencyRule): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>('/api/latency', jsonInit('PUT', rule)).then((r) => r.rules);
  },

  latencyDelete(target: string, path: string): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>(`/api/latency${query({ target, path })}`, {
      method: 'DELETE',
    }).then((r) => r.rules);
  },

  latencyArmAll(enabled: boolean): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>('/api/latency/arm-all', jsonInit('POST', { enabled })).then(
      (r) => r.rules,
    );
  },

  latencyReset(): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>('/api/latency/reset', jsonInit('POST')).then((r) => r.rules);
  },

  restart(name: string): Promise<ServiceState> {
    return request<ServiceState>(`/api/services/${encodeURIComponent(name)}/restart`, jsonInit('POST'));
  },

  flip(name: string): Promise<ServiceState> {
    return request<ServiceState>(`/api/services/${encodeURIComponent(name)}/flip`, jsonInit('POST'));
  },

  seed(name: string): Promise<SeedResult> {
    return request<SeedResult>(`/api/seed/${encodeURIComponent(name)}`, jsonInit('POST'));
  },

  // --- inspector (Task 3.5): databases/schema/rows. Every one of these
  // 501s when no inspector is configured for the stack — callers should
  // treat `err.status === 501` as "inspection unavailable", not a generic
  // failure. ---

  databases(): Promise<DatabaseInfo[]> {
    return request<{ databases: DatabaseInfo[] }>('/api/databases').then((r) => r.databases);
  },

  databaseSchema(name: string): Promise<Table[]> {
    return request<{ tables: Table[] }>(`/api/databases/${encodeURIComponent(name)}/schema`).then(
      (r) => r.tables,
    );
  },

  databaseRows(
    name: string,
    table: string,
    limit?: number,
    offset?: number,
  ): Promise<Record<string, unknown>[]> {
    return request<{ rows: Record<string, unknown>[] }>(
      `/api/databases/${encodeURIComponent(name)}/rows${query({ table, limit, offset })}`,
    ).then((r) => r.rows);
  },

  // --- entities (Task 3.5): discovery + generic passthrough CRUD. The
  // passthrough endpoints reverse-proxy to whatever the configured entity's
  // base returns — the body shape is unknown JSON by design, so callers
  // must render it defensively rather than assume a contract. ---

  entities(): Promise<EntityInfo[]> {
    return request<{ entities: EntityInfo[] }>('/api/entities').then((r) => r.entities);
  },

  entityList(name: string): Promise<unknown> {
    return request<unknown>(`/api/entities/${encodeURIComponent(name)}`);
  },

  entityGet(name: string, id: string): Promise<unknown> {
    return request<unknown>(`/api/entities/${encodeURIComponent(name)}/${encodeURIComponent(id)}`);
  },

  entityCreate(name: string, body: unknown): Promise<unknown> {
    return request<unknown>(`/api/entities/${encodeURIComponent(name)}`, jsonInit('POST', body));
  },

  entityUpdate(name: string, id: string, body: unknown): Promise<unknown> {
    return request<unknown>(
      `/api/entities/${encodeURIComponent(name)}/${encodeURIComponent(id)}`,
      jsonInit('PUT', body),
    );
  },

  entityDelete(name: string, id: string): Promise<unknown> {
    return request<unknown>(`/api/entities/${encodeURIComponent(name)}/${encodeURIComponent(id)}`, jsonInit('DELETE'));
  },
};
