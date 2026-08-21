// Thin typed fetch wrappers over retrace/serve's REST surface. One function
// per endpoint this app calls; every non-2xx response throws ApiError. No
// caching and no retries — the three verbs are filesystem mutations and a
// retried accept is a second promotion.

import type { FieldDiff, ItemResponse, QueueResponse } from './types';

export class ApiError extends Error {
  readonly status: number;
  /** The parsed JSON error body when the response had one. */
  readonly body: unknown;

  constructor(status: number, message: string, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

/** An ApiError's own message when there is one, else a caller-supplied
 * fallback for anything else (a network failure, a thrown non-ApiError). The
 * server's refusals are written to be read by a human — see routes.go — so
 * throwing that sentence away in favour of "something went wrong" would
 * discard the most useful part of the response. */
export function messageOf(err: unknown, fallback: string): string {
  if (err instanceof ApiError) return err.message;
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
    throw new ApiError(res.status, message, body);
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
 * The body of POST /api/queue/{app}/{flow}/rule — R-U.
 *
 * `scope` is NOT a member of this type, and neither is `flow`, and their
 * absence is the point rather than an omission. The wire-rule dialect can
 * express neither dimension: rules.Resolve keys on method plus normalized
 * path alone, so the request body and the response body consult the same
 * globs, and config.WireRules is a flat project-wide list with no per-flow
 * nesting. retrace/serve/routes.go REFUSES both fields with a 400 rather
 * than accepting and ignoring them, and `retrace ref rule` refuses
 * --scope/--flow identically.
 *
 * So a request type carrying `scope` is not merely a field the server drops:
 * every rule the UI writes would 400. The type is the place to fix that,
 * because the seed for this request is a FieldDiff, which HAS a `scope` —
 * making the unsendable field unspeakable is what stops it being passed
 * straight through.
 *
 * `flow` is a path parameter and never belongs in the body either.
 */
export interface RuleRequest {
  field: string;
  matcher: string;
  method?: string;
  path?: string;
}

/**
 * What writing this rule will actually do, in the picker's own words and
 * BEFORE the confirm.
 *
 * The user selects a response-body field and asks to silence it. What gets
 * written silences that field name in every flow in the project and in both
 * the request and the response body. The server says exactly this in its 400
 * — but a reviewer who receives it AFTER clicking has already formed the
 * belief that they scoped the rule, and nobody reads a REST call in a pull
 * request. So the sentence is said up front instead.
 */
export const RULE_BLAST_RADIUS =
  'This rule applies to EVERY flow in this project and to BOTH the request and the response body — a wire rule is scoped by neither. Narrow it with the method and path below; those are the only dimensions the rule dialect has.';

/**
 * The rule request for a selected field. This is the seam R-U turns on: the
 * seed is a FieldDiff, the FieldDiff carries `scope`, and this is where the
 * scope stops. It does not travel, and the returned object has no key for it
 * to travel in.
 */
export function ruleRequestFor(
  field: FieldDiff,
  matcher: string,
  entry: { method: string; normalizedPath: string },
): RuleRequest {
  return {
    field: field.path,
    matcher,
    method: entry.method,
    path: entry.normalizedPath,
  };
}

export const api = {
  queue(): Promise<QueueResponse> {
    return request<QueueResponse>('/api/queue');
  },
  item(app: string, flow: string): Promise<ItemResponse> {
    return request<ItemResponse>(`/api/queue/${seg(app)}/${seg(flow)}`);
  },
  accept(app: string, flow: string): Promise<{ ok: true }> {
    return request(`/api/queue/${seg(app)}/${seg(flow)}/accept`, jsonInit('POST'));
  },
  reject(app: string, flow: string): Promise<{ ok: true; repro: { dir: string } }> {
    return request(`/api/queue/${seg(app)}/${seg(flow)}/reject`, jsonInit('POST'));
  },
  rule(app: string, flow: string, r: RuleRequest): Promise<{ ok: true }> {
    return request(`/api/queue/${seg(app)}/${seg(flow)}/rule`, jsonInit('POST', r));
  },
  /**
   * The URL of one comparison pane's PNG. The server already serves these as
   * image/png; a data URI would mean reading every shot into the JSON
   * document that lists them.
   *
   * An empty name is a throw, not a URL: it means the caller reached here
   * from a CheckpointVerdict whose images.<side> was "" — the side was never
   * written — and `/api/shots/app/flow/diff/` would 404 as a mystery. Callers
   * that can see the empty field should render the explanation instead (see
   * ShotCompare); this is the backstop for the ones that cannot, and it lands
   * in useAsync's error state rather than taking the tree down (Task 14).
   */
  shotUrl(app: string, flow: string, side: 'a' | 'b' | 'diff' | 'overlay', name: string): string {
    if (name === '') {
      throw new Error(`no ${side}-side image for this checkpoint in ${app}/${flow}`);
    }
    return `/api/shots/${seg(app)}/${seg(flow)}/${seg(side)}/${seg(name)}`;
  },
};
