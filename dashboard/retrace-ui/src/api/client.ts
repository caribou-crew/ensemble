// Filesystem-mutating verbs — accept/reject/rule/redact — the part of
// retrace/serve's REST surface exclusive to this standalone dashboard.
// ensemble's aggregate Retrace tab exposes no mutation UI, so these never
// moved to @ensemble/design-system/retraceClient, which covers only the
// read+sync surface (queue/item/runs/shots/evidence/sync) both apps share.
// Every non-2xx response throws RetraceApiError. No caching and no
// retries — the four verbs here are filesystem mutations and a retried
// accept is a second promotion.

import { RetraceApiError, retraceMessageOf } from '@ensemble/design-system/retraceClient';
import type { FieldDiff, Verdict } from './types';

export { RetraceApiError as ApiError, retraceMessageOf as messageOf };

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
 * This half never changes: it is true of every wire rule ever written.
 */
export const RULE_BLAST_RADIUS_ALWAYS =
  'This rule applies to EVERY flow in this project and to BOTH the request and the response body — a wire rule is scoped by neither.';

/**
 * The rest of the sentence, RECOMPUTED from the live values of the method
 * and path boxes — N-2, and the reason it cannot be a constant.
 *
 * `rules.Rule.Path == ""` means EVERY PATH, and `Rule.Method == ""` means
 * every method — a zero value meaning "everything", on the one control in
 * this app that writes a persistent, committed, project-wide tolerance.
 */
export function ruleBlastRadius(method: string, path: string): string {
  const noMethod = method.trim() === '';
  const noPath = path.trim() === '';

  if (noMethod && noPath) {
    return `${RULE_BLAST_RADIUS_ALWAYS} Both boxes below are EMPTY, and empty does not mean "unset" — it means EVERY METHOD and EVERY PATH. This is the widest rule the dialect can write: it will silence this field name everywhere in the project.`;
  }
  if (noPath) {
    return `${RULE_BLAST_RADIUS_ALWAYS} The path box below is EMPTY, and empty does not mean "this path" — it means EVERY PATH. This rule will apply to every ${method.trim()} call in the project, not just this one.`;
  }
  if (noMethod) {
    return `${RULE_BLAST_RADIUS_ALWAYS} The method box below is EMPTY, and empty does not mean "this method" — it means EVERY METHOD. This rule will apply to ${path.trim()} whatever the verb.`;
  }
  return `${RULE_BLAST_RADIUS_ALWAYS} Within that, it is narrowed to ${method.trim()} ${path.trim()} — method and path are the only dimensions the rule dialect has, and clearing either box WIDENS the rule rather than unsetting it.`;
}

/**
 * The rule request for a selected field. This is the seam R-U turns on: the
 * seed is a FieldDiff, the FieldDiff carries `scope`, and this is where the
 * scope stops.
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

/**
 * The body of POST /api/queue/{app}/{flow}/redact.
 *
 * Unlike RuleRequest, there is no method/path to carry: config.RedactEntry
 * matches by FIELD NAME alone.
 */
export interface RedactRequest {
  field: string;
  mode: 'destroy' | 'encrypt' | 'display';
  why?: string;
}

/**
 * What writing this redaction rule will actually do — the same
 * say-the-consequence-before-the-confirm principle ruleBlastRadius exists
 * for, D3.
 */
export function redactBlastRadius(field: string, mode: RedactRequest['mode']): string {
  const consequence =
    mode === 'destroy'
      ? 'the value is IRREVERSIBLY overwritten at capture — not even the team key can bring it back'
      : mode === 'encrypt'
        ? 'the value is encrypted at capture — recoverable with the team key, but never written in the clear again'
        : "the value passes through in the clear, but the dashboard masks it behind reveal-on-click ('fine on disk, not fine on screen')";
  return `This applies to EVERY flow and EVERY app in this project, from the moment it is written: any field literally named "${field.trim() || '(unnamed)'}", wherever it appears, in every future capture. It does not touch anything already recorded. From here forward, ${consequence}.`;
}

/**
 * config.RedactKeyRules matches by the BARE LEAF KEY — so a FieldDiff.path
 * like `data.account.number` needs its last segment, not the whole path,
 * as the redaction rule's field name.
 */
export function leafFieldName(path: string): string {
  const last = path.split('.').pop() ?? '';
  return last.replace(/\[\d+\]$/, '');
}

/**
 * The redact request for a selected field — RulePicker's ruleRequestFor,
 * mirrored for the redact dialect.
 */
export function redactRequestFor(field: FieldDiff, mode: RedactRequest['mode'], why: string): RedactRequest {
  return { field: leafFieldName(field.path), mode, why: why.trim() || undefined };
}

/**
 * What POST .../accept actually answered — F1, and the most expensive
 * omission in this file.
 *
 * `refs.AcceptResult`'s own doc names this UI as the intended consumer:
 * captureStatus and unmatchedMasks "travel as VALUES, not as the stderr
 * sentences the CLI prints", so a caller can act on them without parsing
 * prose.
 *
 * `unmatchedMasks` is the one that costs money. refs.go reports it rather
 * than refusing precisely because "a typo silently redacting nothing is the
 * one that ends with pixels in git".
 */
export interface AcceptBundle {
  dir: string;
  /** bundle-relative, slash-separated */
  files: string[];
  bytes: number;
  runId: string;
  /** The PROMOTED run's own capture verdict. Anything but "ok" means every
   * future diff against this reference inherits that doubt. */
  captureStatus: Verdict;
  /** Project-wide mask entries that matched no checkpoint in this run —
   * never nil on the Go side. */
  unmatchedMasks: string[];
  /** What a FORCED accept pushed past — never absent, same contract as
   * unmatchedMasks. Non-empty means the committed bundle's manifest now
   * records acceptedWithSecrets: true. */
  secretFindings: SecretFinding[];
}

/** One likely credential refs.ScanForSecrets found in the staged bundle —
 * the typed refusal body of POST .../accept when the scan fails. */
export interface SecretFinding {
  /** which hop file ("wire.jsonl" | "hops.jsonl") */
  file: string;
  /** the offending hop's sequence number */
  seq: number;
  /** where the value sits, e.g. "resp.body.session_key" */
  path: string;
  /** which detector fired: "secret-key" | "jwt" | "aws-access-key-id" | "bearer-token" */
  kind: string;
  /** the command that fixes this for good, verbatim */
  suggestion: string;
}

/**
 * The secret-scan findings out of a refused accept, or null for every other
 * failure. The server marks the scan refusal — and ONLY that refusal —
 * with `forcible: true`.
 */
export function secretFindingsOf(err: unknown): SecretFinding[] | null {
  if (!(err instanceof RetraceApiError)) return null;
  const body = err.body;
  if (!body || typeof body !== 'object') return null;
  const b = body as { forcible?: unknown; secretFindings?: unknown };
  if (b.forcible !== true || !Array.isArray(b.secretFindings) || b.secretFindings.length === 0) return null;
  return b.secretFindings as SecretFinding[];
}

/** What POST .../reject answered. `warning` is D3: handleReject sets it
 * when the diff that would EXPLAIN the rejection could not be computed, so
 * the bundle has no summary.json. */
export interface RejectResult {
  ok: true;
  repro: { dir: string; files: string[]; runId: string };
  warning?: string;
}

export const api = {
  /** `force` mirrors `retrace ref accept --force`: it pushes past a failing
   * secret scan (recording acceptedWithSecrets in the bundle manifest) or a
   * fatal capture verdict. */
  accept(app: string, flow: string, force?: boolean): Promise<{ ok: true; bundle: AcceptBundle }> {
    return request(`/api/queue/${seg(app)}/${seg(flow)}/accept`, jsonInit('POST', force ? { force: true } : undefined));
  },
  reject(app: string, flow: string): Promise<RejectResult> {
    return request(`/api/queue/${seg(app)}/${seg(flow)}/reject`, jsonInit('POST'));
  },
  rule(app: string, flow: string, r: RuleRequest): Promise<{ ok: true }> {
    return request(`/api/queue/${seg(app)}/${seg(flow)}/rule`, jsonInit('POST', r));
  },
  redact(app: string, flow: string, r: RedactRequest): Promise<{ ok: true }> {
    return request(`/api/queue/${seg(app)}/${seg(flow)}/redact`, jsonInit('POST', r));
  },
};
