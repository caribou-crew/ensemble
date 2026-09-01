## 1. `ensemble/config` — schema, validation, mTLS resolution

- [x] 1.1 Add `Upstream`, `Passthrough`, `AllowWrites`, `ClientCertFile`,
  `ClientKeyEnv`, `ClientKeyPassphraseEnv` fields to `Service`
  (`ensemble/config/config.go`), yaml-tagged (`upstream`, `passthrough`,
  `allow_writes`, `client_cert_file`, `client_key_env`,
  `client_key_passphrase_env`), all optional.
- [x] 1.2 `Validate`: `Upstream` is mutually exclusive with `Port`-derived
  assumptions where they don't apply; a pure passthrough service (no
  `run`/`docker`) is valid, it just can't `Flip`.
- [x] 1.3 `Validate`: `ClientKeyEnv`/`ClientKeyPassphraseEnv` require
  `ClientCertFile` and vice versa; `ClientCertFile` resolves relative to
  `Config.Dir` and must exist.
- [ ] 1.4 `Validate`: warn (not fail) when `AllowWrites: true` is set
  alongside `Passthrough`. **Skipped** — there is no generic non-fatal
  config-warning mechanism in this codebase to hang it on (`WiringWarning`
  is a different, runtime-computed concept with its own shape); building
  one for this alone was cut as scope not requested as a must-have. The
  info is still visible via `ensemble status`/the dashboard's placement +
  a service with `allow_writes: true` set.
- [x] 1.5 `validatePassthrough`/`ClientCert` in
  `ensemble/config/passthrough.go`: resolves the cert file + `LookupEnv`
  key into a cached `tls.Certificate`, keeping `core/proxy` free of file/
  env I/O.
- [x] 1.6 Config tests (`ensemble/config/passthrough_test.go`): valid
  passthrough-alone and flippable (run+upstream) services; missing proxy
  port; invalid URL; missing cert file; key env unset; cert/key mismatch.

## 2. `core/proxy` — TLS transport, target fields, safety rail

- [x] 2.1 Added `Passthrough bool`, `AllowWrites bool`, `TLS
  *tls.Certificate` to `Target` (`core/proxy/proxy.go`).
- [x] 2.2 `Proxy.transportFor` builds a per-target `*http.Transport`
  (cloning the base transport, preserving any existing `TLSClientConfig`)
  when `Target.TLS` is set; every other target keeps sharing the one
  transport.
- [x] 2.3 Read-only rail added right after the unsupported-protocol
  refusal: non-GET/HEAD to a `Passthrough && !AllowWrites` target is
  refused with 502 and recorded as a hop.
- [x] 2.4 `LatencyStore` gained `DelayForExact` (no `"*"` wildcard
  fallback); the proxy handler uses it for a `Passthrough` target instead
  of `DelayFor`.
- [x] 2.5 Proxy tests (`core/proxy/passthrough_test.go`,
  `core/proxy/latency_test.go`): read-only refusal + `allow_writes`
  override + non-passthrough unaffected; mTLS dial succeeds with the
  configured cert and fails (real TLS handshake failure, not a test
  artifact) with none; `DelayForExact` skips the wildcard.

## 3. `ensemble/orchestrator` — 3-way Flip, wireProxy re-wire

- [x] 3.1 Generalized `wireProxy`: tracks `wiredUpstream`/`wiredStop` per
  service, tears down and re-`ServeStoppable`s when the resolved upstream
  changes; unchanged (no-op) otherwise. Rebind wrapped in
  `retryOnBindConflict` (5 attempts, 20ms apart) to absorb the brief
  OS-level window between closing and rebinding the same address.
- [x] 3.2 `defaultPlacement` gained a `passthrough` branch (no `run`, no
  `docker`); `startServiceAs` bypasses build/spawn/native/docker entirely
  for that placement rather than extending the docker/native branches.
- [x] 3.3 `Flip` (binary, unchanged public behavior) and new `FlipTo`
  (explicit `native`/`docker`/`passthrough` target) both funnel through a
  shared `flipTo` that stops the current placement, starts the requested
  one, and re-wires the proxy.
- [ ] 3.4 Reconcile picking up a plain `upstream:` config-file edit (no
  `Flip`/`FlipTo` call) through the same generalized `wireProxy` — the
  code path is shared so this should already work, but it's **not
  covered by a dedicated test**; worth confirming before relying on it.
- [x] 3.5 Orchestrator tests (`ensemble/orchestrator/passthrough_test.go`):
  pure-upstream service skips process spawn and proxies to the real
  upstream; flippable service round-trips native→passthrough→native with
  the proxy actually re-targeting each time (not just the reported
  state); FlipTo passthrough without `upstream` errors.

## 4. `ensemble/server` — REST/OpenAPI surface

- [x] 4.1 `POST /api/services/{name}/flip` now accepts an optional
  `{"target": "..."}` body — present routes to `FlipTo`, absent keeps the
  legacy binary `Flip` (fully backward compatible; the TUI/older clients
  that send no body are unaffected).
- [x] 4.2 New `TopologyNode.Placements []string` (every placement a
  service *declares*, not just its current one) — what the dashboard needs
  to decide which Flip targets to offer, since `ServiceState.Placement`
  alone only reports the active one.
- [x] 4.3 Server tests: `TestServiceFlipToPassthroughViaTargetBody`,
  `TestTopologyPlacementsReflectDeclaredModes`.
- [x] (unplanned, added during implementation) `ensemble/tui`'s
  `Client.FlipTo` mirrors the new REST capability for CLI/TUI callers.

## 5. `dashboard/ensemble-ui` and `ensemble/tui` — service status legibility

- [x] 5.1 `placement` type extended to include `'passthrough'`
  (`api/types.ts`); new `TopologyNode.placements` field.
- [x] 5.2 `ServicesView.tsx`'s flip control extracted into `FlipControl`:
  a single button when exactly 2 placements are declared (unchanged
  behavior, just generalized off real declared placements instead of a
  hardcoded native/docker assumption), a target-picking `<select>` once a
  service declares 3, nothing when only 1.
- [ ] 5.3 `ensemble/tui/services.go`'s Status column already renders
  whatever string `Placement` is — `"passthrough"` displays correctly
  with **no code change needed**. A dedicated interactive 3-way flip
  *picker* for the TUI (today's `f` key stays a binary toggle) was
  **not built** — it needs its own keybinding/UX decision this session
  didn't make, and the web dashboard was the explicitly requested control
  surface.
- [x] 5.4 `ServicesView.flip.test.ts`: 2-placement button, 3-placement
  select (correct options, correct `api.flip` call), no-op for a
  single-placement service.
- [ ] 5.5 `retrace-iterate` verification of the Services tab flip
  interaction — **not run this session**; the dev server wasn't started
  and clicked through. `tsc --noEmit` + the full vitest suite are green,
  which verifies correctness but not "looks right."

## 6. `retrace/runs` — reduced-scope capture honesty

- [x] 6.1 `Stack.Passthrough []string` added (`retrace/runs/stack.go`),
  additive, validated (no empty names). Populated in
  `retrace/cmd/retrace/client.go`'s `Stack()` from `GET /api/status`'s
  existing per-service `placement` field (no new ensemble endpoint
  needed).
- [x] 6.2 `retrace/diff`: new `Summary.ReducedScope []string` — the union
  of both sides' `Stack.Passthrough`, independent of `StackDiff` (it's a
  scope-honesty disclosure, not a diff between the sides — non-nil even
  when both sides agree).
- [ ] 6.3 `retrace/serve` human-readable report line for `ReducedScope`.
  **Not built** — `Summary.Stack` itself has no dedicated human-text
  rendering anywhere in the codebase today (only feeds the `Triage`
  signal), so there was no existing line to extend; `ReducedScope` is
  available via `retrace diff --json` today, consistent with how `Stack`
  itself is surfaced. A CLI/report text line is a reasonable fast-follow.
- [x] 6.4 Tests: `client_stack_test.go` (Stack() populates Passthrough),
  `stack_test.go` (validation), `diff/stack_test.go` (ReducedScope merges/
  dedupes both sides, nil when neither has one).

## 7. Docs and sample stack

- [ ] 7.1 A passthrough-configured service in `sample/`. **Not built.**
- [ ] 7.2 README/docs section on passthrough mode. **Not built** — this
  proposal + design.md document the shape and the known `retrace
  revalidate` limitation, but nothing user-facing in `docs/`/`README.md`
  yet.

## 8. Verification

- [x] 8.1 `go test ./core/... ./ensemble/... ./retrace/...` green (run
  repeatedly, including the flip-flake fix, and once with `-race` on
  `core/proxy`, `ensemble/orchestrator`, `ensemble/server`,
  `ensemble/tui`).
- [x] 8.2 `go vet ./core/... ./ensemble/... ./retrace/...` clean.
- [x] 8.3 `pnpm test` (ensemble-ui: `tsc --noEmit` + vitest) green, 294
  tests.
- [ ] 8.4 `openspec validate passthrough-mode --strict` — **could not
  run**: the `openspec` CLI is not installed anywhere on this machine
  (checked PATH, Homebrew, npm/npx, common bin dirs). These three files
  were hand-authored matching the exact structure of
  `datadog-latency-import`; run validate wherever the CLI is actually
  available before archiving this change.
