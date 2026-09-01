## Context

The 2026-08-31 audit (four parallel deep-dives over `core/`, `retrace/`,
`ensemble/`+dashboards, and adapters/CI/docs) found no rot — zero
TODO/FIXME in core, every package tested — but a consistent theme: the
system fails *silently* at its edges. Secrets slip into committed bundles,
mis-wired env makes hops invisible, unsupported protocols die quietly,
crashed services stay green, and several fail-closed mechanisms are so
coarse (whole-bundle refusal) or so sharp (panic on the record path) that
they punish the user for the tool's own limits. This change is a hardening
pass: make every known edge either work, or fail loudly with a name.

Six capabilities, one theme. Groups are independent by design so team
agents can implement them in parallel; the only shared surface is additive
`trace.Hop`/`Payload` fields, defined once up front (task group 1).

## Goals / Non-Goals

**Goals:**
- No secret reaches a committed reference bundle by default.
- No captured-or-not ambiguity: traffic that bypasses or exceeds the
  proxy's abilities is detected and named, never silently absent/mangled.
- No silent lifecycle state: crashed services, dropped hops, and write
  errors all surface in status/verdicts.
- Bounded memory everywhere bodies are held.
- Additive schema only — old recordings/bundles keep loading.

**Non-Goals:**
- Implementing the missing protocols (WebSocket/gRPC/h2c/TLS tunneling).
  Detection + refusal only; each is its own future change.
- Latency replay, stateful scenarios, fault injection, a11y diff.
- Auto-restart of crashed services — restart stays a deliberate action
  (same philosophy as freshness never pulling).

## Decisions

### 1. Shared schema additions land first, everything else parallelizes

`Payload` gains `BodyB64 string` (base64 of a non-UTF-8-safe body; mutually
exclusive with `Body`) and `SetCookies []string` (ordered, populated only
when >1 `Set-Cookie` or always-when-present — always, for simplicity;
`Headers["set-cookie"]` keeps the joined form for back-compat readers).
`Hop` gains `Streaming bool` and `Unsupported string` (`"websocket"`,
`"grpc"`, empty otherwise), and the session/manifest side gains
`droppedHops int`. All `omitempty`, schema string stays `ensemble/1` —
readers ignore unknown fields (verify with a fixture test against a
pre-change recording).

### 2. Body redaction: same key list, applied to JSON bodies, recursively

`NewRedactor` today seeds only header keys (`core/trace/redact.go:106`);
`defaultRedactQuery` (`redact.go:34`) already names the secret keys
(`access_token`, `password`, `client_secret`, …). Reuse that exact list as
`defaultRedactBodyKeys`. When a payload body parses as JSON, walk it and
redact any object key matching the list (case-insensitive), at any depth,
in both req and resp. Non-JSON bodies are left alone (the accept-time scan
is the net for those). User `redact:` rules continue to layer on top;
an explicit `redact: { body_defaults: off }` opt-out exists for stacks that
legitimately record fixture credentials.

### 3. Redaction-aware matching: redacted fields are wildcards

`rules.Classify` gains a built-in `redacted` matcher: a *recorded* value
equal to the destroy-mode sentinel (`[redacted]`) matches any live value,
in body-subset matching and in `SignificantQuery` comparison. This is the
same trust model as the existing `uuid`/`iso8601` matchers — the recorded
value asserts shape ("something secret was here"), not content. Encrypt
mode is unaffected (values decrypt before compare, as today).

### 4. Accept-time secret scan, fail closed, `--force` to override

`refs.Accept` (`retrace/refs/refs.go:395`) already refuses undecodable
shots and fatal verdicts; add one more staging check: scan every wire
exchange (headers, query, body — post-redaction) for (a) object keys in the
secret list carrying non-redacted values, (b) JWT-shaped strings
(`eyJ`-prefixed three-dot-segments), (c) known credential shapes (AWS
`AKIA[0-9A-Z]{16}`, `Bearer ` + long token in any header). Findings refuse
the accept with field paths and the matching rule-suggestion (`retrace ref
rule --field ... --matcher redacted`); `--force` records an
`acceptedWithSecrets: true` note in the reference manifest. The serve
review UI surfaces the same scan result on the accept button.

### 5. Wiring validation is a warning, not an error

At `Up`, for each service/stub env var whose value contains
`127.0.0.1:<p>`, `localhost:<p>`, or `host.docker.internal:<p>`: if `<p>`
is another node's real `port:` and that node declares a `proxy:` port, emit
a `wiring` warning naming both ("edge's CATALOG_URL points at catalog's
real port 8081; hops will bypass capture — use proxy port 9081"). Warning,
not error: calling a real port directly is legal (that's what the proxy
itself does). Surfaced in `up` output, `ensemble status` (a `warnings`
field), and a dashboard Services-tab badge. Validation lives in
`ensemble/config/validate.go` beside the port-collision check, evaluated
against resolved variants.

### 6. Streaming: flush-through, record at headers, finalize at close

Wrap the response writer so every upstream write is followed by
`http.Flusher.Flush` when the response is identified as streaming
(`Content-Type: text/event-stream`, or chunked with no `Content-Length`).
Streaming hops are `Record`ed when response headers arrive with
`Streaming: true` and `DoneMs`/body finalized on close via an in-place
update (the recorder keeps the ring slot; subscribers get a second
`hop.updated` event on the SSE stream — the dashboard already re-keys hops
by `seq`). Non-streaming behavior unchanged.

### 7. Unsupported protocols: refuse loudly, record the refusal

A request with `Upgrade: websocket` (or `Connection: Upgrade`) gets a `501`
with a JSON body naming the limitation, and a recorded hop with
`Unsupported: "websocket"` — same for `Content-Type: application/grpc`
(`Unsupported: "grpc"`). The dashboard renders these hops with a distinct
badge; a session containing any produces a `degraded` note in the capture
verdict. This turns "mysteriously broken" into "told you at the first
request".

### 8. Binary bodies: capture losslessly, replay refuses unless excluded

If a captured body is not valid UTF-8 (or content-type is a known-binary
family: `image/*`, `application/octet-stream`, `application/pdf`,
`application/protobuf`…), store `BodyB64` instead of `Body`, same 256 KB
cap. Diff treats `BodyB64` payloads as opaque (equal/not-equal by hash);
replay serves the decoded bytes verbatim — which is now correct, since
capture was lossless. `LoadBundle`'s refusal list drops its implicit
"binary would corrupt" hole entirely.

### 9. Truncation refusal becomes per-exchange and rule-excludable

`LoadBundle` refusing the whole bundle for one truncated body
(`replay/bundle.go:239`) stays fail-closed but gains an escape hatch: a
wire rule `exclude: true` (with mandatory `why`, same convention as every
other rule) drops that exchange from the bundle at load. A truncated
exchange with no excluding rule still refuses the bundle, but the error
now lists the exact rule command to add. Same mechanism covers
`Content-Encoding` and 206 refusals.

### 10. Recorder: writes off the lock, rings budgeted by bytes

`Recorder.Record` currently does NDJSON write + fan-out under the one
mutex (`core/proxy/recorder.go:79-104`). Split: under the lock, append to
ring + enqueue to a bounded write queue (ordered, per-recorder goroutine);
the writer drains to disk and increments a visible `writeErrors` /
`droppedWrites` counter instead of swallowing (`recorder.go:88`). Ring
eviction adds a byte budget (default 256 MB, configurable) alongside the
count cap — evict oldest until under both. Retrace's 8192-hop ring
(`retrace/capture/capture.go:212`) inherits the budget.

### 11. Panics degrade

`trace/redact.go:141,273,277` run inside `Record` on the live path.
Replace with error returns: a hop whose redaction fails is recorded with
`Err` set and its payloads dropped (`body: ""`, note in `Err`) — the
request itself is never killed. `ctx.go:145` (crypto/rand) stays a panic.

### 12. Supervision: exit is a state, not a mystery

`startNativeProcess`'s reaper goroutine (`orchestrator/proc.go:93`)
records `exitCode`, `signal`, and `exitedAt` into `ServiceState`, moves
status to `exited` (zero exit) or `crashed` (non-zero/signal), tails the
last KBs of the service log into `lastErr`, and emits an SSE status event.
Docker placement gets the equivalent via the existing `docker ps` health
path noticing the container gone. No auto-restart. Dashboard/TUI render
the state distinctly; `ensemble ready` treats `crashed` as failure.

### 13. Logs and history are read-only GETs over files that already exist

- `GET /api/services/{name}/logs?tail=N` (default 200 lines, cap 5000) and
  `GET /api/services/{name}/logs/stream` (SSE follow) over
  `.ensemble/run/<name>.log`. Dashboard: log pane on the service panel;
  TUI: `l` opens a log tail on the selected service.
- `GET /api/traffic/history?before=<seq>&limit=&…same filters as /api/traffic`
  reads `.ensemble/hops.jsonl` backwards (the NDJSON reader already
  exists, 16 MB line cap); the Traffic view gains "load earlier".
- `GET /api/sessions/{id}/export?format=har` reuses `core/trace/export.go`
  over all hops with that session id — whole-session HAR closes the
  per-trace-only gap.

### 14. Small sharp edges, fixed directly

Session `Start` binds its edge listener before the duplicate-id check
(`core/proxy/session.go:234-252`) — reorder, check under lock first.
Post-`End` routed hops (`session.go:157`) increment `droppedHops` on the
session result instead of vanishing (closes F.3: zero means zero).
`core/stub/stub.go:114` caps `io.ReadAll` with `http.MaxBytesReader` at
the body cap, returning 413 beyond it; the stub listener gains the same
loopback enforcement as the proxy (`stub.go:76`) and both servers get
`ReadHeaderTimeout` (`proxy.go:307`, `stub.go:80`).

## Risks / Trade-offs

- **Body redaction could redact fixture data a diff legitimately needs.**
  Mitigation: key-list is narrow and shared with query redaction (already
  accepted behavior), opt-out exists, and the `redacted` matcher keeps
  replay/diff green across redaction.
- **`hop.updated` events are a new SSE contract** — dashboard and TUI must
  upsert by `seq` rather than append-only. Audit says the dashboard
  already keys by seq; verify TUI.
- **Ordered write queue adds a failure mode (queue overflow).** Bounded +
  counted + surfaced; strictly better than blocking every request on disk.
- **Accept-time scan false positives** (a JWT-shaped test fixture).
  `--force` plus rule suggestions keep the workflow unblocked; scan is
  accept-time only, never on record/diff.

## Open Questions

- None blocking. Byte-budget default (256 MB) and log tail caps are
  config-tunable guesses; adjust from usage.
