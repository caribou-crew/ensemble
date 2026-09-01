// Thin typed fetch wrappers over retrace/serve's REST surface. One function
// per endpoint this app calls; every non-2xx response throws ApiError. No
// caching and no retries — the three verbs are filesystem mutations and a
// retried accept is a second promotion.

import type {
  Evidence,
  FieldDiff,
  ItemResponse,
  QueueResponse,
  SyncCandidatesResponse,
  SyncResult,
  SyncSelection,
  Verdict,
} from './types';

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
 *
 * This half never changes: it is true of every wire rule ever written.
 */
export const RULE_BLAST_RADIUS_ALWAYS =
  'This rule applies to EVERY flow in this project and to BOTH the request and the response body — a wire rule is scoped by neither.';

/**
 * The rest of the sentence, RECOMPUTED from the live values of the method and
 * path boxes — N-2, and the reason it cannot be a constant.
 *
 * `rules.Rule.Path == ""` means EVERY PATH (`MatchPathGlob` returns true for
 * an empty glob: "an unscoped rule applies to every call"), and
 * `Rule.Method == ""` means every method. `handleRule` requires only `field`
 * and `matcher`, so nothing refuses an empty box — nor should it: a
 * project-wide rule is a legitimate thing the dialect exists to express, and
 * `retrace ref rule` shares that contract.
 *
 * The defect was that the screen went SILENT when the rule became wide. The
 * static half of this sentence tells the reviewer these two boxes are their
 * only protection, and the control it points at treats an empty value as "no
 * protection at all" — a zero value meaning "everything", on the one control
 * in this app that writes a persistent, committed, project-wide tolerance.
 * Clearing the path box to generalise a rule, or backspacing it by accident,
 * used to change nothing on screen.
 *
 * So the copy tells the truth continuously rather than at seed time. Blank
 * counts as empty: a box holding only spaces is not a narrowing either.
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

/**
 * The body of POST /api/queue/{app}/{flow}/redact.
 *
 * Unlike RuleRequest, there is no method/path to carry: config.RedactEntry
 * matches by FIELD NAME alone (config.go's RedactKeyRules), so the dialect
 * has nothing narrower to scope this to.
 */
export interface RedactRequest {
  field: string;
  mode: 'destroy' | 'encrypt' | 'display';
  why?: string;
}

/**
 * What writing this redaction rule will actually do — the same "say the
 * consequence before the confirm" principle ruleBlastRadius exists for, D3.
 *
 * A redact rule matches by field name ONLY (RedactKeyRules), so it is even
 * wider than a wire rule: no method/path box narrows it at all, in either
 * direction. And it changes what CAPTURE WRITES TO DISK from here forward —
 * a `destroy` or `encrypt` rule does not touch anything already recorded,
 * but every future capture, in every flow, for a field with this exact
 * name, is affected from the moment this rule is written.
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
 * config.RedactKeyRules matches by the BARE LEAF KEY (core/trace/redact.go's
 * redactValue walks a JSON tree and checks each map key on its own, never a
 * dotted path) — so a FieldDiff.path like `data.account.number` needs its
 * last segment, not the whole path, as the redaction rule's field name.
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
 * prose. `retrace ref accept` prints a warning for each on every promotion.
 * Typing the response as `{ ok: true }` threw both away, and the UI said
 * "accepted … as the new reference" with identical confidence whether or not
 * it had just promoted a broken capture, or a bundle whose redaction mask
 * silenced nothing.
 *
 * `unmatchedMasks` is the one that costs money. refs.go reports it rather
 * than refusing precisely because "a typo silently redacting nothing is the
 * one that ends with pixels in git" — and a reviewer accepting through the UI
 * got no signal at all.
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
   * never nil on the Go side, so this is `[]` and never absent. */
  unmatchedMasks: string[];
  /** What a FORCED accept pushed past — the accept-time secret scan's
   * findings, `[]` on every clean promotion (never absent, same contract as
   * unmatchedMasks). Non-empty means the committed bundle's manifest now
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
 * failure. The server marks the scan refusal — and ONLY that refusal — with
 * `forcible: true`; anything else (a fatal capture verdict without force, a
 * mask typo) must keep rendering as a plain error, because offering a
 * "force" button on a refusal the CLI would not override is two faces of one
 * verb disagreeing.
 */
export function secretFindingsOf(err: unknown): SecretFinding[] | null {
  if (!(err instanceof ApiError)) return null;
  const body = err.body;
  if (!body || typeof body !== 'object') return null;
  const b = body as { forcible?: unknown; secretFindings?: unknown };
  if (b.forcible !== true || !Array.isArray(b.secretFindings) || b.secretFindings.length === 0) return null;
  return b.secretFindings as SecretFinding[];
}

/** What POST .../reject answered. `warning` is D3: handleReject sets it when
 * the diff that would EXPLAIN the rejection could not be computed, so the
 * bundle has no summary.json. A reviewer who reads the unqualified "repro
 * bundle written to <dir>" believes they have a bundle explaining the
 * rejection; they have a directory. */
export interface RejectResult {
  ok: true;
  repro: { dir: string; files: string[]; runId: string };
  warning?: string;
}

export const api = {
  queue(): Promise<QueueResponse> {
    return request<QueueResponse>('/api/queue');
  },
  item(app: string, flow: string): Promise<ItemResponse> {
    return request<ItemResponse>(`/api/queue/${seg(app)}/${seg(flow)}`);
  },
  /** GET /api/queue/{app}/{flow}/runs/{runId} — the same detail document as
   * item(), pinned to one specific run rather than "latest". Powers the
   * sync panel's click-into-a-CI-run detail view, so a reviewer can inspect
   * any run in the candidate list, not only the newest one. */
  itemAtRun(app: string, flow: string, runId: string): Promise<ItemResponse> {
    return request<ItemResponse>(`/api/queue/${seg(app)}/${seg(flow)}/runs/${seg(runId)}`);
  },
  /** `force` mirrors `retrace ref accept --force`: it pushes past a failing
   * secret scan (recording acceptedWithSecrets in the bundle manifest) or a
   * fatal capture verdict. Omitted — the default — is the protective
   * reading, and the accept button only ever sends it after showing the
   * findings the server refused over. */
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
  /**
   * The URL of one comparison pane's PNG. The server already serves these as
   * image/png; a data URI would mean reading every shot into the JSON
   * document that lists them.
   *
   * An empty name is a throw, not a URL: it means the caller reached here
   * from a CheckpointVerdict whose images.<side> was "" — the side was never
   * written — and `/api/shots/app/flow/diff/` would 404 as a mystery.
   *
   * This throw is NOT a backstop, and an earlier note here claiming it was is
   * what made someone write the unsafe call on purpose. Every caller of this
   * function is a component building an `src` DURING RENDER. A throw in the
   * render phase is not a rejected promise: useAsync never sees it, there is
   * no error boundary anywhere under dashboard/, and React 19 unmounts the
   * root — the reviewer gets a WHITE PAGE, on exactly the checkpoint they
   * opened the flow to look at.
   *
   * So the obligation is entirely on the caller: check the field, and render
   * an explanation naming the checkpoint when it is empty (see ShotCompare,
   * which guards all four sides). This throw only refuses to manufacture a
   * URL that cannot resolve; it does not make the mistake survivable.
   */
  shotUrl(app: string, flow: string, side: 'a' | 'b' | 'diff' | 'overlay', name: string): string {
    if (name === '') {
      throw new Error(`no ${side}-side image for this checkpoint in ${app}/${flow}`);
    }
    return `/api/shots/${seg(app)}/${seg(flow)}/${seg(side)}/${seg(name)}`;
  },
  /** shotUrl's counterpart for a run-detail view pinned to one specific run
   * — GET /api/shots/{app}/{flow}/runs/{runId}/{side}/{name}. Its own
   * route, not a query param on shotUrl: the server caches "diff"/"overlay"
   * PNGs under a run-scoped directory precisely so a non-latest run's
   * generated images can never collide with (or be served instead of) the
   * "latest" queue's own cache for the same app/flow — see
   * retrace/serve/queue.go's diffDirForRun. */
  shotUrlAtRun(app: string, flow: string, runId: string, side: 'a' | 'b' | 'diff' | 'overlay', name: string): string {
    if (name === '') {
      throw new Error(`no ${side}-side image for this checkpoint in ${app}/${flow}/${runId}`);
    }
    return `/api/shots/${seg(app)}/${seg(flow)}/runs/${seg(runId)}/${seg(side)}/${seg(name)}`;
  },
  /** GET /api/sync/candidates?repo=... — discovers what's out there in
   * `repo` without downloading anything. `repo` is required: unlike
   * ensemble.yaml's `retrace:` block, retrace.yaml has no repo default. */
  syncCandidates(
    repo: string,
    filters: { branch?: string; actor?: string; event?: string; status?: string; since?: string } = {},
  ): Promise<SyncCandidatesResponse> {
    const params = new URLSearchParams({ repo, ...compact(filters) });
    return request<SyncCandidatesResponse>(`/api/sync/candidates?${params}`);
  },
  /** POST /api/sync — pulls exactly the given selections from `repo`. */
  sync(repo: string, selections: SyncSelection[]): Promise<SyncResult> {
    return request<SyncResult>('/api/sync', jsonInit('POST', { repo, selections }));
  },
  /** GET /api/evidence/{app}/{flow} — what video/report is attached to the
   * candidate run. Fetched independently of `item()`: evidence attaches
   * after a run finishes and is never part of Summary. */
  evidence(app: string, flow: string): Promise<Evidence> {
    return request<Evidence>(`/api/evidence/${seg(app)}/${seg(flow)}`);
  },
};

/** Builds `/api/videos/{app}/{flow}/{name}` — passed straight to a
 * <video> element's src. */
export function videoUrl(app: string, flow: string, name: string): string {
  return `/api/videos/${seg(app)}/${seg(flow)}/${seg(name)}`;
}

/** Builds `/api/report/{app}/{flow}/` — the candidate run's HTML report
 * root. Opened in a new tab rather than embedded: the report is a
 * self-routing SPA (Playwright's html reporter), and an iframe would fight
 * its own routing. */
export function reportUrl(app: string, flow: string): string {
  return `/api/report/${seg(app)}/${seg(flow)}/`;
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
