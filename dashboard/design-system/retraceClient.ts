// Thin typed fetch wrappers over retrace/serve's REST surface, shared by
// retrace-ui (standalone `retrace serve`, basePath "/api") and ensemble-ui's
// Retrace tab (embedded via ensemble/server, basePath "/api/retrace"). Both
// backends compute their responses from the same retrace/serve functions —
// see openspec/changes/retrace-ci-sync — so one client factory covers both
// route prefixes instead of two hand-copied fetch layers drifting apart.
//
// This is the READ + SYNC surface only (queue, item, runs, shots, evidence,
// sync). The filesystem-mutating verbs — accept/reject/rule/redact — exist
// only in retrace-ui today (ensemble's aggregate dashboard exposes no
// mutation UI), so they stay in retrace-ui's own api/client.ts rather than
// living here unused by one of the two callers.

import type {
  Evidence,
  ItemResponse,
  PairsResponse,
  QueueResponse,
  RunsResponse,
  SyncBranchesResponse,
  SyncCandidatesResponse,
  SyncConfigResponse,
  SyncResult,
  SyncSelection,
} from './retraceTypes';

export class RetraceApiError extends Error {
  readonly status: number;
  /** The parsed JSON error body when the response had one. */
  readonly body: unknown;

  constructor(status: number, message: string, body?: unknown) {
    super(message);
    this.name = 'RetraceApiError';
    this.status = status;
    this.body = body;
  }
}

/** A RetraceApiError's own message when there is one, else a
 * caller-supplied fallback for anything else (a network failure, a thrown
 * non-error). The server's refusals are written to be read by a human, so
 * throwing that sentence away in favour of "something went wrong" would
 * discard the most useful part of the response. */
export function retraceMessageOf(err: unknown, fallback: string): string {
  if (err instanceof RetraceApiError) return err.message;
  if (err instanceof Error && err.message) return err.message;
  return fallback;
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
    throw new RetraceApiError(res.status, message, body);
  }

  return body as T;
}

function jsonInit(method: string, payload?: unknown): RequestInit {
  if (payload === undefined) return { method };
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  };
}

const seg = (s: string) => encodeURIComponent(s);

/**
 * The queue filters the review surface offers — source (local vs CI) and one
 * exact app key. Mirrors serve.QueueFilter; both fields are optional and an
 * unset field matches everything, so the empty filter is the whole queue.
 * No platform/framework parsing here on purpose: the structure of an app key
 * belongs to the project's own retrace config, not to this dashboard, so an
 * app filter is always an exact match against whatever key the server sent.
 */
export interface QueueFilter {
  source?: 'local' | 'ci';
  app?: string;
}

/** Serialises a QueueFilter into a query string, omitting every empty field
 * — an absent or all-empty filter yields "". */
export function queueQuery(filter?: QueueFilter): string {
  if (!filter) return '';
  const params = new URLSearchParams();
  if (filter.source) params.set('source', filter.source);
  if (filter.app) params.set('app', filter.app);
  const qs = params.toString();
  return qs ? `?${qs}` : '';
}

/** Drops empty-string values so URLSearchParams never carries a filter the
 * caller left blank. */
function compact(o: Record<string, string | undefined>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(o)) {
    if (v) out[k] = v;
  }
  return out;
}

export interface RetraceClient {
  queue(filter?: QueueFilter): Promise<QueueResponse>;
  item(app: string, flow: string): Promise<ItemResponse>;
  itemAtRun(app: string, flow: string, runId: string): Promise<ItemResponse>;
  /** Every run of a surface, newest first — the runs-list drill-down. */
  runs(app: string, flow: string): Promise<RunsResponse>;
  shotUrl(app: string, flow: string, side: 'a' | 'b' | 'diff' | 'overlay', name: string): string;
  shotUrlAtRun(app: string, flow: string, runId: string, side: 'a' | 'b' | 'diff' | 'overlay', name: string): string;
  videoUrl(app: string, flow: string, name: string): string;
  reportUrl(app: string, flow: string): string;
  evidence(app: string, flow: string): Promise<Evidence>;
  /** Every persisted cross-app diff (retrace/pairs) — the listing for the
   * cross-app compare view. Never triggers a computation; reads only what
   * `retrace diff -a/-b` already persisted. */
  pairs(): Promise<PairsResponse>;
  /** One persisted cross-app diff's full Summary. */
  pair(appB: string, flowB: string, runB: string, pairId: string): Promise<ItemResponse>;
  pairShotUrl(
    appB: string,
    flowB: string,
    runB: string,
    pairId: string,
    side: 'a' | 'b' | 'diff' | 'overlay',
    name: string,
  ): string;
  /** The server's configured sync defaults (repo.yaml's `repo:` + `sync:`),
   * so the panel can prefill the repo instead of asking. Empty repo means no
   * configured default. Only the standalone `retrace serve` populates it;
   * ensemble-ui gets an empty object (its repos come from ensemble.yaml). */
  syncConfig(): Promise<SyncConfigResponse>;
  /** `repo` is required against retrace-ui's basePath ("/api") — retrace.yaml
   * carries no sync default. It's meaningless (and ignored) against
   * ensemble's basePath ("/api/retrace"), whose sync routes read repo(s)
   * from ensemble.yaml's own retrace: block server-side, so omit it there. */
  syncCandidates(
    repo?: string,
    filters?: { workflows?: string[]; branch?: string; actor?: string; event?: string; status?: string; since?: string },
  ): Promise<SyncCandidatesResponse>;
  /** Branches that have actually triggered a matching workflow recently —
   * the sync panel's "Choose source" picker. Unlike syncCandidates, an
   * omitted `workflows` filter falls back to the server's configured
   * default rather than "every workflow", so the picker only ever shows
   * branches relevant to what this dashboard tracks. */
  syncBranches(repo?: string, filters?: { workflows?: string[]; since?: string }): Promise<SyncBranchesResponse>;
  sync(repo: string | undefined, selections: SyncSelection[]): Promise<SyncResult>;
}

/** One entry in GET {basePath}/instances — key is what a RetraceClient's
 * `instance` constructor param expects back; label is what a picker shows.
 * Only ensemble-ui's basePath ("/api/retrace") ever has more than one —
 * retrace-ui's own `.retrace/` is inherently a single instance. */
export interface RetraceInstanceInfo {
  key: string;
  label: string;
}

/** Lists the instances configured at basePath — ensemble-ui calls this
 * before instantiating a RetraceClient to decide whether to show a picker
 * (more than one entry) or go straight to the single instance's queue (the
 * common case, and the only case retrace-ui's own basePath ever has). */
export function listRetraceInstances(basePath: string): Promise<{ instances: RetraceInstanceInfo[] }> {
  return request<{ instances: RetraceInstanceInfo[] }>(`${basePath}/instances`);
}

/**
 * Builds a RetraceClient bound to one route prefix. retrace-ui instantiates
 * this with `"/api"` (retrace/serve's own routes); ensemble-ui with
 * `"/api/retrace"` (ensemble/server's embedded routes) — same shapes, same
 * retrace/serve functions on the Go side, different mount point.
 *
 * `instance` selects which of ensemble.yaml's `retrace.instances` a request
 * targets, carried as `?instance=` on every call this client makes —
 * retrace-ui never passes one (there's no concept of instances against a
 * single `retrace serve` process), so it's a no-op there. ensemble-ui passes
 * the picker's current selection; when ensemble.yaml declares no more than
 * one instance the server accepts requests with the param omitted too (see
 * ensemble/server/retrace.go's retraceInstanceFor), so an unset instance is
 * only ambiguous once there's actually more than one to choose from.
 */
export function createRetraceClient(basePath: string, instance?: string): RetraceClient {
  const withInstance = (qs: string): string => {
    if (!instance) return qs;
    const params = new URLSearchParams(qs.replace(/^\?/, ''));
    params.set('instance', instance);
    return `?${params.toString()}`;
  };
  return {
    queue(filter) {
      return request<QueueResponse>(`${basePath}/queue${withInstance(queueQuery(filter))}`);
    },
    item(app, flow) {
      return request<ItemResponse>(`${basePath}/queue/${seg(app)}/${seg(flow)}${withInstance('')}`);
    },
    itemAtRun(app, flow, runId) {
      return request<ItemResponse>(`${basePath}/queue/${seg(app)}/${seg(flow)}/runs/${seg(runId)}${withInstance('')}`);
    },
    runs(app, flow) {
      return request<RunsResponse>(`${basePath}/queue/${seg(app)}/${seg(flow)}/runs${withInstance('')}`);
    },
    shotUrl(app, flow, side, name) {
      if (name === '') {
        throw new Error(`no ${side}-side image for this checkpoint in ${app}/${flow}`);
      }
      return `${basePath}/shots/${seg(app)}/${seg(flow)}/${seg(side)}/${seg(name)}${withInstance('')}`;
    },
    shotUrlAtRun(app, flow, runId, side, name) {
      if (name === '') {
        throw new Error(`no ${side}-side image for this checkpoint in ${app}/${flow}/${runId}`);
      }
      return `${basePath}/shots/${seg(app)}/${seg(flow)}/runs/${seg(runId)}/${seg(side)}/${seg(name)}${withInstance('')}`;
    },
    videoUrl(app, flow, name) {
      return `${basePath}/videos/${seg(app)}/${seg(flow)}/${seg(name)}${withInstance('')}`;
    },
    reportUrl(app, flow) {
      return `${basePath}/report/${seg(app)}/${seg(flow)}/${withInstance('')}`;
    },
    evidence(app, flow) {
      return request<Evidence>(`${basePath}/evidence/${seg(app)}/${seg(flow)}${withInstance('')}`);
    },
    pairs() {
      return request<PairsResponse>(`${basePath}/pairs${withInstance('')}`);
    },
    pair(appB, flowB, runB, pairId) {
      return request<ItemResponse>(`${basePath}/pairs/${seg(appB)}/${seg(flowB)}/${seg(runB)}/${seg(pairId)}${withInstance('')}`);
    },
    pairShotUrl(appB, flowB, runB, pairId, side, name) {
      if (name === '') {
        throw new Error(`no ${side}-side image for this checkpoint in ${appB}/${flowB}`);
      }
      return `${basePath}/pairs/${seg(appB)}/${seg(flowB)}/${seg(runB)}/${seg(pairId)}/shots/${seg(side)}/${seg(name)}${withInstance('')}`;
    },
    syncConfig() {
      return request<SyncConfigResponse>(`${basePath}/sync/config${withInstance('')}`);
    },
    syncCandidates(repo, filters = {}) {
      const { workflows, ...rest } = filters;
      const params = new URLSearchParams({
        ...(repo ? { repo } : {}),
        ...(workflows && workflows.length > 0 ? { workflows: workflows.join(',') } : {}),
        ...compact(rest),
      });
      const qs = params.toString();
      return request<SyncCandidatesResponse>(`${basePath}/sync/candidates${withInstance(qs ? `?${qs}` : '')}`);
    },
    syncBranches(repo, filters = {}) {
      const { workflows, ...rest } = filters;
      const params = new URLSearchParams({
        ...(repo ? { repo } : {}),
        ...(workflows && workflows.length > 0 ? { workflows: workflows.join(',') } : {}),
        ...compact(rest),
      });
      const qs = params.toString();
      return request<SyncBranchesResponse>(`${basePath}/sync/branches${withInstance(qs ? `?${qs}` : '')}`);
    },
    sync(repo, selections) {
      return request<SyncResult>(
        `${basePath}/sync${withInstance('')}`,
        jsonInit('POST', repo ? { repo, selections } : { selections }),
      );
    },
  };
}
