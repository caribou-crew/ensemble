// Thin typed fetch wrappers over ensemble/server's REST surface. One
// function per endpoint the dashboard calls; every non-2xx response throws
// ApiError. No caching, no retries — that belongs to whichever view needs
// it (SSE reconnection, polling, etc. land in later tasks).

import type {
  GatewayStatus,
  Hop,
  LatencyRule,
  LogicalHop,
  ServiceState,
  Topology,
  ProfilesState,
  WiringWarning,
} from "./types";
import type { SeedStepResult } from "./types";
import type { DatabaseInfo, EntityInfo, Table } from "./types";
import type { RetraceQueueResponse } from "./types";

export class ApiError extends Error {
  readonly status: number;
  /** The parsed JSON error body, when the response had one — e.g. POST
   * /api/seed/{name}'s 500 still carries {results, ok:false, error}, and a
   * caller that wants the partial results can read it off here. */
  readonly body: unknown;

  constructor(status: number, message: string, body?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

/** Every view's catch block wants the same thing: an ApiError's own message when there is
 * one, else a caller-supplied fallback for anything else (a network failure, a thrown
 * non-ApiError). Was six near-identical copies across the dashboard's views before this. */
export function messageOf(err: unknown, fallback: string): string {
  return err instanceof ApiError ? err.message : fallback;
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
      body &&
      typeof body === "object" &&
      "error" in body &&
      typeof (body as { error: unknown }).error === "string"
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
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  };
}

function query(
  params: Record<string, string | number | boolean | undefined>,
): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === "") continue;
    search.set(key, String(value));
  }
  const s = search.toString();
  return s ? `?${s}` : "";
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

export interface TrafficHistoryParams {
  before?: number;
  limit?: number;
  errorsOnly?: boolean;
  session?: string;
  method?: string;
  path?: string;
  status?: number;
  // See TrafficParams above for why this is explicit rather than inferred.
  [key: string]: string | number | boolean | undefined;
}

export interface TrafficHistoryResponse {
  /** Newest-first, per GET /api/traffic/history's contract. */
  hops: Hop[];
  /** Malformed hops.jsonl lines skipped while building this page. */
  corruptLines: number;
  /** Whether hops older than the oldest one in this page still exist. */
  hasMore: boolean;
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
  /** `withMem` opts into `?mem=1`, which populates each service's `rssKB`
   * at the cost of a `ps`/`docker stats` shell-out per running node —
   * leave it off for tight polling loops (health strip, other views). */
  status(withMem = false): Promise<ServiceState[]> {
    return request<{ services: ServiceState[] }>(
      `/api/status${withMem ? "?mem=1" : ""}`,
    ).then((r) => r.services);
  },

  topology(): Promise<Topology> {
    return request<Topology>("/api/topology");
  },

  /** Proxy-wiring warnings (GET /api/status's `warnings` field) — a separate call from
   * `status()` rather than widening its return type, since most `status()` callers (the
   * health strip, TopologyView) have no use for them and `status()`'s own shape is already
   * relied on elsewhere as `ServiceState[]`. Never null on the wire. */
  wiringWarnings(): Promise<WiringWarning[]> {
    return request<{ warnings?: WiringWarning[] }>("/api/status").then(
      (r) => r.warnings ?? [],
    );
  },

  traffic(params: TrafficParams = {}): Promise<Hop[]> {
    return request<{ hops: Hop[] }>(`/api/traffic${query(params)}`).then(
      (r) => r.hops,
    );
  },

  /** Persisted history beyond the live ring — GET /api/traffic/history,
   * paged backwards by `before`. Powers the Traffic view's "load
   * earlier" affordance. */
  trafficHistory(
    params: TrafficHistoryParams = {},
  ): Promise<TrafficHistoryResponse> {
    return request<TrafficHistoryResponse>(
      `/api/traffic/history${query(params)}`,
    );
  },

  trace(id: string): Promise<TraceResponse> {
    return request<TraceResponse>(`/api/traces/${encodeURIComponent(id)}`);
  },

  latencyList(): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>("/api/latency").then(
      (r) => r.rules,
    );
  },

  latencyUpsert(rule: LatencyRule): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>(
      "/api/latency",
      jsonInit("PUT", rule),
    ).then((r) => r.rules);
  },

  latencyDelete(target: string, path: string): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>(
      `/api/latency${query({ target, path })}`,
      {
        method: "DELETE",
      },
    ).then((r) => r.rules);
  },

  latencyArmAll(enabled: boolean): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>(
      "/api/latency/arm-all",
      jsonInit("POST", { enabled }),
    ).then((r) => r.rules);
  },

  latencyReset(): Promise<LatencyRule[]> {
    return request<{ rules: LatencyRule[] }>(
      "/api/latency/reset",
      jsonInit("POST"),
    ).then((r) => r.rules);
  },

  restart(name: string): Promise<ServiceState> {
    return request<ServiceState>(
      `/api/services/${encodeURIComponent(name)}/restart`,
      jsonInit("POST"),
    );
  },

  /** With no `target`, toggles between a service's two placements (legacy binary behavior).
   * With `target` ("native" | "docker" | "passthrough"), switches to exactly that one —
   * needed once a service has three declared placements, since "the other one" is ambiguous. */
  flip(
    name: string,
    target?: "native" | "docker" | "passthrough",
  ): Promise<ServiceState> {
    return request<ServiceState>(
      `/api/services/${encodeURIComponent(name)}/flip`,
      jsonInit("POST", target ? { target } : undefined),
    );
  },

  stop(name: string): Promise<ServiceState> {
    return request<ServiceState>(
      `/api/services/${encodeURIComponent(name)}/stop`,
      jsonInit("POST"),
    );
  },

  /** GET /api/status's `gateways` field — a separate call from `status()` for the same
   * reason `wiringWarnings()` is: most `status()` callers have no use for it, and
   * `status()`'s own shape is already relied on elsewhere as `ServiceState[]`. */
  gatewayStatus(): Promise<GatewayStatus[]> {
    return request<{ gateways?: GatewayStatus[] }>("/api/status").then(
      (r) => r.gateways ?? [],
    );
  },

  /** Flips a gateway to `target` ("local" or one of its declared upstream names) — unlike
   * `flip()`, `target` is always required; there's no legacy binary toggle for a gateway to
   * fall back to. */
  flipGateway(name: string, target: string): Promise<GatewayStatus[]> {
    return request<GatewayStatus[]>(
      `/api/gateways/${encodeURIComponent(name)}/flip`,
      jsonInit("POST", { target }),
    );
  },

  profiles(): Promise<ProfilesState> {
    return request<ProfilesState>("/api/profiles");
  },

  profileUp(name: string): Promise<ProfilesState> {
    return request<ProfilesState>(
      `/api/profiles/${encodeURIComponent(name)}/up`,
      jsonInit("POST"),
    );
  },

  profileDown(name: string): Promise<ProfilesState> {
    return request<ProfilesState>(
      `/api/profiles/${encodeURIComponent(name)}/down`,
      jsonInit("POST"),
    );
  },

  setVariant(name: string, variant: string): Promise<ServiceState> {
    return request<ServiceState>(
      `/api/services/${encodeURIComponent(name)}/variant`,
      jsonInit("POST", { variant }),
    );
  },

  seed(name: string): Promise<SeedResult> {
    return request<SeedResult>(
      `/api/seed/${encodeURIComponent(name)}`,
      jsonInit("POST"),
    );
  },

  /** Triggers an immediate freshness pass over every eligible service,
   * outside the normal poll schedule, and returns the resulting status —
   * the Services tab's "check now" control. */
  freshnessCheck(): Promise<ServiceState[]> {
    return request<{ services: ServiceState[] }>(
      "/api/freshness/check",
      jsonInit("POST"),
    ).then((r) => r.services);
  },

  // --- inspector (Task 3.5): databases/schema/rows. Every one of these
  // 501s when no inspector is configured for the stack — callers should
  // treat `err.status === 501` as "inspection unavailable", not a generic
  // failure. ---

  databases(): Promise<DatabaseInfo[]> {
    return request<{ databases: DatabaseInfo[] }>("/api/databases").then(
      (r) => r.databases,
    );
  },

  databaseSchema(name: string): Promise<Table[]> {
    return request<{ tables: Table[] }>(
      `/api/databases/${encodeURIComponent(name)}/schema`,
    ).then((r) => r.tables);
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
    return request<{ entities: EntityInfo[] }>("/api/entities").then(
      (r) => r.entities,
    );
  },

  entityList(name: string): Promise<unknown> {
    return request<unknown>(`/api/entities/${encodeURIComponent(name)}`);
  },

  entityGet(name: string, id: string): Promise<unknown> {
    return request<unknown>(
      `/api/entities/${encodeURIComponent(name)}/${encodeURIComponent(id)}`,
    );
  },

  entityCreate(name: string, body: unknown): Promise<unknown> {
    return request<unknown>(
      `/api/entities/${encodeURIComponent(name)}`,
      jsonInit("POST", body),
    );
  },

  entityUpdate(name: string, id: string, body: unknown): Promise<unknown> {
    return request<unknown>(
      `/api/entities/${encodeURIComponent(name)}/${encodeURIComponent(id)}`,
      jsonInit("PUT", body),
    );
  },

  entityDelete(name: string, id: string): Promise<unknown> {
    return request<unknown>(
      `/api/entities/${encodeURIComponent(name)}/${encodeURIComponent(id)}`,
      jsonInit("DELETE"),
    );
  },

  // Cross-app review queue, embedded from retrace/serve. 501s when no
  // `retrace:` block is configured. This is the only retrace call left
  // here — App.tsx's useRetraceAvailable() probes it once to decide
  // whether the Retrace tab should even be shown. RetraceView itself (and
  // its sync panel) fetch through @ensemble/design-system/retraceClient's
  // createRetraceClient("/api/retrace") instead, the same shared client
  // retrace-ui's standalone dashboard uses against "/api" — see
  // RetraceView.tsx.
  retraceQueue(): Promise<RetraceQueueResponse> {
    return request<RetraceQueueResponse>("/api/retrace/queue");
  },
};
