## 1. `ensemble/config` — schema, validation, mTLS resolution

- [ ] 1.1 Add `Upstreams []GatewayUpstream` to `Gateway`
  (`ensemble/config/config.go:697`), yaml-tagged `upstreams`, optional.
- [ ] 1.2 New `ensemble/config/gateway_upstream.go`: `GatewayUpstream`
  type (`Name`, `URL`, `AllowWrites`, `ClientCertFile`, `ClientKeyEnv`,
  `ClientKeyPassphraseEnv`, yaml-tagged `name`/`url`/`allow_writes`/
  `client_cert_file`/`client_key_env`/`client_key_passphrase_env`).
- [ ] 1.3 `validateGatewayUpstreams(gatewayName string, gw Gateway) []error`
  in the same file, mirroring `validatePassthrough`'s checks minus the
  `Proxy > 0` one (no local placement to cross-check): unique `Name` per
  gateway, non-empty valid http(s) `URL`, `ClientKeyEnv`/
  `ClientKeyPassphraseEnv` require `ClientCertFile` and vice versa, cert
  file resolved relative to `Config.Dir` and read, key resolved via
  `Config.LookupEnv`, `tls.X509KeyPair` built and cached.
- [ ] 1.4 New cache on `Config`: `gatewayClientCerts
  map[string]map[string]tls.Certificate` (gateway name → upstream name →
  cert), populated by 1.3 during `Validate`. New method
  `GatewayUpstreamClientCert(gateway, upstream string) (tls.Certificate,
  bool)`.
- [ ] 1.5 Wire `validateGatewayUpstreams` into `Config.Validate`'s
  existing per-gateway validation pass.
- [ ] 1.6 Config tests (`ensemble/config/gateway_upstream_test.go`): valid
  gateway with 0/1/N upstreams; duplicate upstream names within one
  gateway rejected; invalid URL; missing cert file; key env unset;
  cert/key mismatch; cert cached and retrievable via
  `GatewayUpstreamClientCert`; a gateway and a service sharing a name each
  resolve their own cert independently (no aliasing).

## 2. `ensemble/orchestrator` — gateway flip, re-wire

- [ ] 2.1 `wireOneGateway` (`ensemble/orchestrator/orchestrator.go:1361`)
  gains a `target string` parameter. `target == "local"` (or empty)
  builds today's routed `proxy.Target` unchanged. A non-local `target`
  looks up the matching `GatewayUpstream` by name, builds `proxy.Target{
  Name: name, Listen: ..., Upstream: gu.URL, Passthrough: true,
  AllowWrites: gu.AllowWrites, TLS: resolved cert if any, CORS: nil,
  Routes: nil}`. `wireGateways` and `Reconcile`'s gateway-rebind path pass
  `"local"` explicitly — no behavior change for existing callers.
- [ ] 2.2 New `gatewayActive map[string]string` field on `Orchestrator`
  (alongside `gatewayStop`), initialized empty (meaning `"local"`),
  recording each gateway's current flipped target.
- [ ] 2.3 New `FlipGateway(ctx context.Context, name, target string)
  error`: resolves `o.cfg.Gateways[name]` (error if unknown), validates
  `target` is `"local"` or a declared upstream name (error naming the
  gateway and target otherwise), locks via `o.lockService("gateway:" +
  name)`, calls `o.unwireGateway(name)` then `o.wireOneGateway(name,
  o.cfg.Gateways[name], target)` with the same `retryOnBindConflict`
  handling `wireProxy` uses for services, updates `gatewayActive[name]`
  on success.
- [ ] 2.4 Orchestrator tests
  (`ensemble/orchestrator/gateway_passthrough_test.go`): flip a gateway
  local→upstream→local, confirming the listener actually re-targets each
  time (not just reported state, matching how
  `TestReconcileUpstreamEditRewiresProxy` verifies the service case);
  flip to an undeclared upstream name errors; flip an unknown gateway
  errors; a passthrough-flipped gateway refuses a non-GET write unless
  `allow_writes: true`; mTLS dial uses the configured cert.

## 3. `ensemble/server` — REST/OpenAPI surface

- [ ] 3.1 `POST /api/gateways/{name}/flip` with body `{"target": "..."}`
  (required — unlike service flip, there's no legacy binary toggle to
  fall back to), routed alongside the existing `/api/services/{name}/flip`
  registration in `ensemble/server/routes.go:32`, calling
  `s.Orch.FlipGateway`.
- [ ] 3.2 Gateway entries in the status/topology response gain their
  active target and declared upstream names (mirroring how
  `TopologyNode.Placements` exposes every placement a service declares,
  not just the active one) — exact field naming resolved during
  implementation.
- [ ] 3.3 `ensemble/tui/client.go`'s API client gains a `FlipGateway`
  method mirroring `FlipTo`.
- [ ] 3.4 Server tests: flip via REST changes the gateway's reported
  active target; flip to an undeclared target 4xxs with a clear message;
  status/topology response includes declared upstream names for a
  gateway that has them, omits the field for one that doesn't.

## 4. `dashboard/ensemble-ui` and `ensemble/tui` — flip control

- [ ] 4.1 `api/types.ts`: gateway status type gains `activeTarget`/
  `upstreams` (final names matching 3.2's wire shape), consumed by the
  `"kind":"gateway"` rows already rendered in the Services view.
- [ ] 4.2 Generalize (or add a sibling of) the existing service
  `FlipControl` (`ServicesView.tsx`) for a gateway row: always a
  `<select>` (never just a button — a gateway always has `"local"` plus
  N declared upstreams, never exactly 2 choices the way a 2-placement
  service does), options being `local` + every declared upstream name.
  Renders nothing for a gateway with zero declared upstreams (same as a
  1-placement service showing no flip control today).
- [ ] 4.3 `ensemble/tui/services.go`: gateway rows' Status column shows
  the active target (already generic string rendering, likely no code
  change needed — confirm, same as passthrough-mode task 5.3's finding
  for services). A TUI flip *picker* for gateways is out of scope here,
  same reasoning `passthrough-mode` used to skip it for the 3-way service
  case (own keybinding/UX decision, dashboard is the primary control
  surface).
- [ ] 4.4 `ServicesView` tests covering the gateway flip select (correct
  options, correct API call) and the zero-upstream no-control case.

## 5. `retrace/runs` — reduced-scope capture honesty

- [ ] 5.1 Extend `Stack.Passthrough` population
  (`retrace/cmd/retrace/client.go`'s `Stack()`) to also include
  passthrough-flipped gateway names from the status response, alongside
  the existing per-service placement read — same field, additive source,
  no new `Stack` field needed since it's already `[]string` of "things
  that were passthrough during this run."
- [ ] 5.2 Tests confirming a run through a passthrough-flipped gateway
  marks that gateway's name in `Stack.Passthrough`, and that
  `retrace/diff`'s existing `ReducedScope` (already built off
  `Stack.Passthrough` in `passthrough-mode`) picks it up with no changes
  needed there.

## 6. Docs and sample stack

- [ ] 6.1 A gateway in `sample/ensemble.yaml` declares at least one
  `upstreams` entry (placeholder host, commented, matching how `ops`'s
  `upstream` field was introduced for the service case) so the shape is
  discoverable without a real QA env.
- [ ] 6.2 README/docs: a "Gateway passthrough" section (near the existing
  passthrough-mode section) covering config shape, full-bypass semantics
  (no local CORS/routing/fault-injection while flipped), the read-only
  safety rail, flipping via REST/dashboard, and the reduced-scope
  disclosure in `retrace diff`.

## 7. Verification

- [ ] 7.1 `go test ./core/... ./ensemble/... ./retrace/...` green,
  including a `-race` pass on `ensemble/orchestrator` and
  `ensemble/server`.
- [ ] 7.2 `go vet ./core/... ./ensemble/... ./retrace/...` clean.
- [ ] 7.3 `pnpm test` (ensemble-ui: `tsc --noEmit` + vitest) green.
- [ ] 7.4 Manual verification against `sample/`: flip a gateway to its
  placeholder upstream via the dashboard, confirm routing bypasses local
  routes/CORS, flip back to `local`, confirm normal behavior resumes.
- [ ] 7.5 `openspec validate gateway-passthrough --strict` if the
  `openspec` CLI is available in the environment this runs in — it was
  not available on this machine (same gap noted in `passthrough-mode`
  task 8.4); these three files were hand-authored matching
  `datadog-latency-import`'s structure.
