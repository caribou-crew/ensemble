## Why

Today ensemble's proxy only ever fronts a local process or container: every
`Service`'s upstream is derived as `http://127.0.0.1:<port>`, computed once
at `ensemble up`. A team validating a client against a local stack (via
ensemble) and then against a real remote environment (QA) has no way to put
the second half through the same capture/diff/retrace machinery as the
first — QA traffic today goes through a hand-rolled client with no capture,
no redaction, and no wire-diff against the local run. `docs/stretch-ideas.md`
§2 already sketches this as "passthrough mode"; this proposal scopes a
concrete v1 slice against a live use case (local-stack/ux-toolkit's
`card-ops-e2e.mjs MODE=local|qa` flow), including mTLS, which that sketch
had only flagged as an open question.

## What Changes

- `Service` gains passthrough fields: `upstream` (remote base URL),
  `passthrough` (environment label, e.g. `qa` — any non-empty value arms
  the safety rail below), `allow_writes` (opt out of read-only), and mTLS
  fields `client_cert_file` (path, relative to `Config.Dir`), `client_key_env`
  and `client_key_passphrase_env` (names of env vars holding the key/
  passphrase, never the values themselves — mirrors the existing Datadog
  API-key-name pattern).
- A service may declare **both** a local placement (`run`/`docker`) and a
  passthrough placement (`upstream`/`passthrough`) at once. `Flip` becomes
  3-way (`native` / `docker` / `passthrough`) instead of binary, so
  switching a service between local and QA is a live orchestrator action —
  exactly the `card-ops-e2e.mjs MODE=local|qa` switch, but now driven
  through ensemble (CLI, and the dashboard's Services tab) instead of two
  divergent client code paths.
- `core/proxy` gains: a per-target `http.Transport` override (used only
  when a target declares mTLS material) instead of the one shared
  no-TLS transport; and the ability for `wireProxy`/`Flip` to actually
  re-point an already-wired listener's `Target.Upstream` (today `wireProxy`
  no-ops on an already-wired service — see Impact).
- Read-only-by-default safety rail: any non-GET/HEAD request to a
  `passthrough`-labeled target is refused (502, recorded as a hop, not
  silently dropped) unless that service sets `allow_writes: true`.
- Fault/latency injection is skipped by default for a passthrough target
  (no config change needed to keep prod/QA safe — the absence of an
  explicit rule targeting it is already the safe state; this just needs a
  guard so a *stack-wide* rule doesn't accidentally reach a passthrough
  target).
- `retrace/runs.Stack` gains an additive `Passthrough []string` field
  naming which services in a run were passthrough targets, so a run mixing
  local services and one remote leaf can be marked "reduced scope past
  `edge`" precisely, instead of only having the existing whole-run
  `ModeStandalone`/nil-`Hops` encoding (which says "no chain recorded at
  all", not "recorded, but incomplete past one boundary").
- Dashboard/TUI: the existing service placement type (currently
  `'native'|'docker'`, `dashboard/ensemble-ui/src/api/types.ts:75`,
  rendered as a badge/flip control in `ServicesView.tsx:315,357`) gains a
  third value, `'passthrough'`; `ensemble/tui/services.go:123`'s Status
  column gets the same third value. No new view, no new config-mutation
  endpoint — confirmed there is none today (`ensemble/server/routes.go`
  is entirely runtime actions: restart/stop/flip/variant/latency/seed/
  profiles/reconcile), consistent with keeping this a `Flip`-style runtime
  action rather than a config-edit surface.

## Capabilities

### New Capabilities
- `passthrough-mode`: a `Service` can forward to a real remote HTTPS
  environment instead of a local process, with mTLS, a read-only-by-default
  safety rail, and reduced-scope capture honesty — CLI (`ensemble flip`),
  REST, and dashboard surfaces, all through the existing proxy/orchestrator/
  retrace machinery.

### Modified Capabilities
- `ensemble-gateway` (or wherever `core/proxy.Target`/`wireProxy` currently
  live as a spec) — `Target` gains an optional TLS transport override, and
  the "wire once at Up" invariant gets an explicit re-wire path for a
  service whose active placement changes at runtime.

## Impact

- `ensemble/config/config.go`: `Service` gains `Upstream`, `Passthrough`,
  `AllowWrites`, `ClientCertFile`, `ClientKeyEnv`,
  `ClientKeyPassphraseEnv`; `Validate` rejects `Upstream` combined with
  `Port`-only assumptions that don't hold for a remote target, and rejects
  `client_key_env`/`client_key_passphrase_env` without `client_cert_file`
  or vice versa.
- `core/proxy/proxy.go`: `Target` gains `TLS *TLSConfig{Cert, transport}`
  (resolved cert material, not raw config — resolution happens in
  `ensemble/config` or the orchestrator, not in `core/proxy`, which stays
  free of file/env I/O); `Proxy` builds a per-target transport when `TLS`
  is set. `Target` also gains `Passthrough bool`/`AllowWrites bool` so the
  read-only rail and latency-skip can be enforced in the request handler.
- `ensemble/orchestrator/orchestrator.go`: `wireProxy`'s `already :=
  o.wired[name]` fast path (line ~1203) currently no-ops all re-wiring;
  needs to detect "resolved upstream changed" and tear down + re-`Serve`
  that one listener instead of skipping.
- `ensemble/orchestrator/flip.go`: `Flip` currently requires exactly `Run
  != "" && Docker != nil` and toggles native/docker only; extend to a
  3-valued placement including `passthrough` when `Upstream != ""`, reusing
  `stopCurrent`/`startServiceAs`.
- `core/trace` / `retrace/runs/manifest.go`, `retrace/runs/stack.go`: new
  additive `Stack.Passthrough []string`; `retrace/diff` reads it to
  annotate a diff as reduced-scope past a named boundary instead of only
  recognizing whole-run `ModeStandalone`.
- `dashboard/ensemble-ui/src/views/ServicesView.tsx` and
  `ensemble/server/routes.go`/`openapi.go`: `placement` type/enum gains
  `'passthrough'`; the flip REST call accepts (or infers) the target
  placement rather than assuming binary toggle.
- No changes needed to `core/trace/redact.go` — `Authorization`/`dpop` are
  already in `defaultRedactHeaders` as always-destroy; this proposal adds
  tests confirming that holds for passthrough hops, not new redaction
  mechanism.
- Explicitly out of scope: fault injection *into* a passthrough target
  beyond the default skip (arm-gating is a follow-up), a named
  `environments:` config block (rejected in favor of inline service
  fields), full remote hop reconstruction (separate DD-trace-import idea),
  and `retrace revalidate` live-credential resolution against a passthrough
  target — `revalidate` already cannot re-send a redacted `Authorization`
  header today (`retrace/replay/revalidate.go`'s `sendableRequestHeader`
  drops any header whose recorded value is the redacted marker;
  `cmd_revalidate.go` renders the resulting drift) and this proposal does
  not change that; it is a known, documented limitation for a future
  change.
