// TS mirrors of ensemble/server's JSON types — do not drift from the Go
// shapes without updating both.

export interface Timings {
  start: string;
  firstByteMs?: number;
  doneMs?: number;
}

export interface Payload {
  headers?: Record<string, string>;
  body?: string;
  /** Base64 of a binary body (invalid UTF-8 or a known-binary content
   * type), captured losslessly — mutually exclusive with `body`. */
  bodyB64?: string;
  /** Every Set-Cookie response header value, in order — the joined
   * `headers` form is lossy for multiple cookies. */
  setCookies?: string[];
  truncated?: boolean;
}

export interface Hop {
  schema: string;
  seq: number;
  traceId?: string;
  spanId?: string;
  parentSpanId?: string;
  correlationId?: string;
  session?: string;
  from?: string;
  /** How `from` was resolved when it isn't a real trace-context fact (core/proxy's SpanOwner
   * had nothing to look up): "declared" means the caller self-asserted its name via the
   * X-Ensemble-Caller header (a caller ensemble doesn't manage); "inferred" means it came
   * from a config-declared called_by hint instead, a static guess rather than a live
   * assertion. Absent means `from`, if set, is a trace-derived fact. */
  attribution?: 'inferred' | 'declared';
  to: string;
  method?: string;
  path?: string;
  status?: number;
  t: Timings;
  req?: Payload;
  resp?: Payload;
  injectedDelayMs?: number;
  err?: string;
  /** True when ensemble answered a CORS preflight itself rather than
   * forwarding it to an upstream — never set on real proxied traffic. */
  preflight?: boolean;
  /** The client APPLICATION that sent this request — "web", "ios", "admin" —
   * read from the first configured client-identity header present
   * (`client_identity_headers:`, default x-source-client then x-local-client).
   * Populated wherever that header arrives, which in practice is the entry
   * hop: an internal call carries it only if that service forwards it.
   *
   * Not the same thing as `from`, which is a position in the service graph
   * and a fallback for missing trace context. `client` is validated to
   * `^[a-z0-9][a-z0-9:-]{0,31}$` at capture, so unlike `from` it is safe to
   * group and filter on; a value that failed validation arrives as the
   * literal "client" and the original is never stored. */
  client?: string;
  /** True for a hop recorded at response-headers time for a streaming
   * response (SSE, or chunked with no Content-Length). Finalized in place
   * — same seq, via the `hop.updated` SSE event — when the stream closes;
   * until then `t.doneMs` is absent. */
  streaming?: boolean;
  /** A protocol the proxy detected and refused rather than silently
   * breaking ("websocket" | "grpc") — the hop's status is the 501 it was
   * answered with; nothing was forwarded upstream. */
  unsupported?: string;
}

export interface ServiceState {
  name: string;
  status: string;
  placement: "native" | "docker" | "passthrough";
  pid?: number;
  proxyPort?: number;
  port?: number;
  startedAt?: string;
  lastErr?: string;
  /** How this service's process last ended on its own — set alongside status "exited"
   * (clean zero exit) or "crashed" (non-zero exit or signal), cleared on the next start.
   * `signal` is set instead of `exitCode` when the process died to a signal. */
  exitCode?: number;
  signal?: string;
  exitedAt?: string;
  /** Current `variants:` choice, for a service that declares any. */
  variant?: string;
  /** Sampled RSS in KB. Only populated when status was fetched with `?mem=1`. */
  rssKB?: number;
  /** Free-form label from config.Service/Variant `kind:`, e.g. "stub", "mock". Empty means
   * "service" (a real, unlabeled backing). Named kind, not type, to avoid colliding with
   * config.Database's `type:` (a validated engine enum — postgres, redis, etc). */
  kind?: string;
  /** How far this service's checkout is behind its own remote branch and behind the
   * configured default branch — absent unless `freshness:` is configured AND the service is
   * eligible (its own git repo, distinct from the one containing ensemble.yaml). Populated by
   * a background poll, not computed at read time — see FreshnessState. */
  freshness?: FreshnessState;
}

/** One service's git-freshness snapshot — mirrors
 * ensemble/orchestrator.FreshnessState's JSON shape exactly. */
export interface FreshnessState {
  branch: string;
  behindBranch: number;
  behindDefault: number;
  defaultBranch: string;
  /** RFC3339 timestamp of the last SUCCESSFUL check. Empty means this service has never been
   * successfully checked — render as "unknown", never as "up to date". */
  checkedAt?: string;
  /** Set when the most recent check attempt failed (fetch failure, or the branch/rev-list
   * comparison couldn't be resolved). May appear alongside a populated branch/behind* from an
   * earlier successful check — that's "this is what we last knew, and the most recent
   * recheck failed", not a fresh empty answer. */
  error?: string;
}

/** One env: value that references another service's REAL port when that service also
 * declares a `proxy:` port — mirrors config.WiringWarning's JSON shape exactly. Advisory:
 * the referencing stack still starts, the hop is just uncaptured. See GET /api/status's
 * `warnings` field and the proxy-wiring-validation spec. */
export interface WiringWarning {
  service: string;
  variant?: string;
  env: string;
  target: string;
  port: number;
  proxyPort: number;
  message: string;
}

export interface TopologyNode {
  name: string;
  category: "service" | "database" | "stub" | "gateway";
  status: string;
  entry?: boolean;
  /** Set only for a service declaring `variants:` — the current choice and every option. */
  variant?: string;
  variants?: string[];
  /** Every placement a "service" category node declares ("native", "docker", "passthrough",
   * in that order) — never empty for a service. What decides which Flip targets to offer,
   * since ServiceState.placement alone only says which one is CURRENTLY active. */
  placements?: Array<"native" | "docker" | "passthrough">;
  /** "gateway" nodes only — mirrors config.Gateway.ExposeInTraffic. When false (the default),
   * the Traffic tab collapses this gateway's own hop into its target's. */
  exposeInTraffic?: boolean;
  /** Every upstream a "gateway" category node declares (by name), in declaration order —
   * mirrors config.Gateway.Upstreams' Name field. What decides which FlipGateway targets to
   * offer, since GatewayStatus.activeTarget alone only says which one is CURRENTLY active.
   * Unset for every other category and for a gateway with none declared. */
  upstreams?: string[];
}

/** One gateway's current flip target — mirrors ensemble/orchestrator.GatewayStatus's JSON
 * shape exactly. "local" is the default (routed) mode; any other value names one of the
 * gateway's declared TopologyNode.upstreams. */
export interface GatewayStatus {
  name: string;
  activeTarget: string;
}

export interface TopologyEdge {
  from: string;
  to: string;
}

export interface Topology {
  nodes: TopologyNode[];
  edges: TopologyEdge[];
}

export interface LatencyRule {
  target: string;
  path: string;
  fixedMs?: number;
  p50?: number;
  p95?: number;
  p99?: number;
  enabled: boolean;
}

export interface LogicalHop {
  hop: Hop;
  origin: Hop | null;
  via?: string[];
  index: number;
  statusMismatch?: boolean;
}

// Task 3.4 additions:
export interface Column {
  name: string;
  type: string;
  nullable: boolean;
}

export interface Table {
  name: string;
  columns: Column[];
}

export interface ChangeEvent {
  db: string;
  table: string;
  at: string;
}

// Task 3.5 additions:

export interface DatabaseInfo {
  name: string;
  type: string;
}

// One configured "open in host app" button for an entity's rows — mirrors
// ensemble/server's entityLink. Template is a plain string with
// {{column}} placeholders resolved client-side against each row's own
// fields — see format.ts's resolveLinkTemplate.
//
// `kind` is absent (or "url") for the original behavior: resolve and
// navigate/open directly. `kind: "exec"` instead means `steps` is the
// target command's steps, each an argv template (exactly one element
// across all of them is the literal sentinel "{{url}}") — the button
// builds and copies a local CLI command to the clipboard, one step per
// `&&`-joined shell command, rather than navigating. Any `adb reverse`
// steps from the config's `reverse:` list are already resolved into
// `steps` by the server — the client never sees `reverse:` or a port
// number to resolve itself. See format.ts's buildExecCommand.
export interface EntityLink {
  label: string;
  template: string;
  kind?: 'exec';
  steps?: string[][];
}

// One entry in GET /api/entities' discovery list. `id` is the CONFIGURED
// ROW-ID FIELD NAME for this entity (ensemble.yaml's entities.<name>.id,
// e.g. "id" or "uuid") — not any particular row's id value. A row's own
// identifier is read off `row[info.id]` at render time.
export interface EntityInfo {
  name: string;
  id: string;
  links?: EntityLink[];
}

// Not in the plan's contract block, but required to type POST
// /api/seed/{name}'s response — mirrors
// ensemble/orchestrator.SeedStepResult's JSON tags exactly
// (ensemble/orchestrator/seed.go).
export interface SeedStepResult {
  kind: string;
  ref: string;
  ok: boolean;
  err?: string;
  durationMs: number;
}

/** GET /api/profiles — every configured profile with its members and live state. */
export interface ProfileInfo {
  name: string;
  services: string[];
  active: boolean;
}

export interface ProfilesState {
  active: string[];
  profiles: ProfileInfo[];
}

