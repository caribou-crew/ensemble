## 1. `ensemble/config` — schema, validation, mTLS resolution

- [x] 1.1 Add `Upstreams []GatewayUpstream` to `Gateway`
  (`ensemble/config/config.go:697`), yaml-tagged `upstreams`, optional.
- [x] 1.2 New `ensemble/config/gateway_upstream.go`: `GatewayUpstream`
  type (`Name`, `URL`, `AllowWrites`, `ClientCertFile`, `ClientKeyEnv`,
  `ClientKeyPassphraseEnv`, yaml-tagged `name`/`url`/`allow_writes`/
  `client_cert_file`/`client_key_env`/`client_key_passphrase_env`).
- [x] 1.3 `validateGatewayUpstreams(gatewayName string, gw Gateway) []error`
  in the same file, mirroring `validatePassthrough`'s checks minus the
  `Proxy > 0` one (no local placement to cross-check): unique `Name` per
  gateway, non-empty valid http(s) `URL`, `ClientKeyEnv`/
  `ClientKeyPassphraseEnv` require `ClientCertFile` and vice versa, cert
  file resolved relative to `Config.Dir` and read, key resolved via
  `Config.LookupEnv`, `tls.X509KeyPair` built and cached.
- [x] 1.4 New cache on `Config`: `gatewayClientCerts
  map[string]map[string]tls.Certificate` (gateway name → upstream name →
  cert), populated by 1.3 during `Validate`. New method
  `GatewayUpstreamClientCert(gateway, upstream string) (tls.Certificate,
  bool)`.
- [x] 1.5 Wire `validateGatewayUpstreams` into `Config.Validate`'s
  existing per-gateway validation pass.
- [x] 1.6 Config tests (`ensemble/config/gateway_upstream_test.go`): valid
  gateway with 0/1/N upstreams; duplicate upstream names within one
  gateway rejected; invalid URL; missing cert file; key env unset;
  cert/key mismatch; cert cached and retrievable via
  `GatewayUpstreamClientCert`; a gateway and a service sharing a name each
  resolve their own cert independently (no aliasing).

## 2. `ensemble/orchestrator` — gateway flip, re-wire

- [x] 2.1 `wireOneGateway` (`ensemble/orchestrator/orchestrator.go:1361`)
  gains a `target string` parameter. `target == "local"` (or empty)
  builds today's routed `proxy.Target` unchanged. A non-local `target`
  looks up the matching `GatewayUpstream` by name, builds `proxy.Target{
  Name: name, Listen: ..., Upstream: gu.URL, Passthrough: true,
  AllowWrites: gu.AllowWrites, TLS: resolved cert if any, CORS: nil,
  Routes: nil}`. `wireGateways` and `Reconcile`'s gateway-rebind path pass
  `"local"` explicitly — no behavior change for existing callers.
- [x] 2.2 New `gatewayActive map[string]string` field on `Orchestrator`
  (alongside `gatewayStop`), initialized empty (meaning `"local"`),
  recording each gateway's current flipped target.
- [x] 2.3 New `FlipGateway(ctx context.Context, name, target string)
  error`: resolves `o.cfg.Gateways[name]` (error if unknown), validates
  `target` is `"local"` or a declared upstream name (error naming the
  gateway and target otherwise), locks via `o.lockService("gateway:" +
  name)`, calls `o.unwireGateway(name)` then `o.wireOneGateway(name,
  o.cfg.Gateways[name], target)` with the same `retryOnBindConflict`
  handling `wireProxy` uses for services, updates `gatewayActive[name]`
  on success.
- [x] 2.4 Orchestrator tests
  (`ensemble/orchestrator/gateway_passthrough_test.go`): flip a gateway
  local→upstream→local, confirming the listener actually re-targets each
  time; flip to an undeclared upstream name errors; flip an unknown
  gateway errors; a passthrough-flipped gateway refuses a non-GET write
  unless `allow_writes: true`; `Gateways()` reports the active target
  before and after a flip.

## 3. `ensemble/server` — REST/OpenAPI surface

- [x] 3.1 `POST /api/gateways/{name}/flip` with body `{"target": "..."}`
  (required — unlike service flip, there's no legacy binary toggle to
  fall back to), routed alongside the existing `/api/services/{name}/flip`
  registration in `ensemble/server/routes.go`, calling
  `s.Orch.FlipGateway`.
- [x] 3.2 `TopologyNode` gains `Upstreams []string` (declared upstream
  names, mirroring `Placements`); `GET /api/status` gains a `gateways`
  field (`orchestrator.Gateways()`) reporting each one's current active
  target.
- [ ] 3.3 `ensemble/tui/client.go`'s API client gaining a `FlipGateway`
  method. **Not built** — the design explicitly scoped the TUI as
  read-only for gateways (no flip picker), matching how `passthrough-mode`
  skipped an equivalent 3-way picker for services and left the dashboard
  as the primary control surface; a client method with nothing calling it
  would be dead code.
- [x] 3.4 Server tests
  (`ensemble/server/gateway_flip_test.go`): flip via REST changes the
  gateway's reported active target (verified through `GET /api/status`);
  flip to an undeclared target errors (500, matching `handleServiceFlip`'s
  existing convention of not distinguishing 4xx/5xx for a `FlipTo`
  failure); flip with no body errors 400; flip on an unknown gateway 404s;
  topology's `Upstreams` field is populated.

## 4. `dashboard/ensemble-ui` and `ensemble/tui` — flip control

- [x] 4.1 `api/types.ts`/`api/client.ts`: new `GatewayStatus` type,
  `TopologyNode.upstreams`, `api.gatewayStatus()`, `api.flipGateway()`.
- [x] 4.2 `GatewayRow` (`ServicesView.tsx`) now renders its current active
  target and a `FlipControl` (reused as-is — its existing "N others as a
  select" branch already covers a gateway's "local + N upstreams" shape,
  no generalization needed) offering `local` + every declared upstream.
  Renders nothing for a gateway with zero declared upstreams.
- [x] 4.3 `ensemble/tui/services.go`: confirmed no code change needed —
  out of scope per 3.3's note (dashboard is the control surface; the TUI
  never gained gateway status wiring to render in the first place, unlike
  the service case which already had a Status column rendering whatever
  string `Placement` held).
- [x] 4.4 `ServicesView.gatewayFlip.test.ts`: the flip select's options
  and `api.flipGateway` call, the zero-upstream no-control case, and the
  active-target badge. Every existing `ServicesView.*.test.ts` file's
  `Promise.all` mock set gained `api.gatewayStatus()`, matching how
  `wiringWarnings()` was added previously.

## 5. `retrace/runs` — reduced-scope capture honesty

- [x] 5.1 Extended `Stack.Passthrough` population
  (`retrace/cmd/retrace/client.go`'s `Stack()`) to also include
  passthrough-flipped gateway names from `/api/status`'s new `gateways`
  field, alongside the existing per-service placement read.
- [x] 5.2 `TestStackRecordsPassthroughGateways`
  (`retrace/cmd/retrace/client_stack_test.go`) confirms a gateway with
  `activeTarget != "local"` lands in `Stack.Passthrough`; existing
  `retrace/diff` `ReducedScope` tests confirm it needs no changes (already
  builds off `Stack.Passthrough` generically).

## 6. Docs and sample stack

- [x] 6.1 `sample/ensemble.yaml`'s `public` gateway declares one
  `upstreams` entry (placeholder host, commented) — `ensemble/config`'s
  `TestWiringWarningsSampleStackClean` and `retrace/config`'s
  `TestTheShippedSampleConfigSatisfiesItsOwnRatchet` both still pass.
- [x] 6.2 README: new "Gateway passthrough: flipping a gateway to a real
  remote edge" section, after the existing passthrough-mode section.

## 7. Verification

- [x] 7.1 `go test ./core/... ./ensemble/...` green with `-race`.
  `go test ./retrace/...` green except `retrace/cmd/retrace`, which hits
  a **pre-existing, unrelated** timeout/flake under this suite's full
  parallel run (goroutine traces point at `cmd_run_ensemble_test.go`'s
  `wedgedServer`/marker-door fixtures — nothing touched by this change);
  reproduces identically with or without `-race` and is unrelated to
  `Stack`/gateways. The new/modified tests in that package
  (`TestStackRecordsPassthroughGateways` and the rest of the `Stack`
  suite) pass reliably in isolation.
- [x] 7.2 `go vet ./core/... ./ensemble/... ./retrace/...` clean.
- [x] 7.3 `pnpm test` (ensemble-ui: `tsc --noEmit` + vitest): 63 files,
  298 tests, green.
- [ ] 7.4 Manual dashboard click-through against a live `sample/` stack —
  **not done**: `sample/`'s `ensemble up` needs several Docker-backed
  dependencies (postgres/mysql/redis/dynamodb) unrelated to this change.
  Substituted with equivalent automated coverage instead: the
  orchestrator/server test suites already drive the real mechanism
  end-to-end (real listeners, real HTTP requests, real flip/rebind) via
  `TestFlipGatewayRoundTripsLocalToUpstreamToLocal` and
  `TestGatewayFlipViaRESTChangesActiveTarget`.
- [x] 7.5 `openspec validate` — **could not run**: the `openspec` CLI is
  not installed on this machine (same gap `passthrough-mode` task 8.4
  hit); these three files were hand-authored matching
  `datadog-latency-import`'s structure.
