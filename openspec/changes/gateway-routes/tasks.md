# Tasks: gateway-routes

Ordered; each ends in passing tests. TDD per task: write the failing test
first, then the code.

## 1. core/proxy — per-request upstream resolution

- [x] 1.1 Add `Route{Prefix, Upstream, StripPrefix}` and `Target.Routes`; add
      `resolve(path) (upstream, forwardPath string, ok bool)` with longest
      segment-aware prefix match and strip semantics (table-driven test:
      longest wins, `/cart` vs `/cartoon`, `/` catch-all, strip to `/`,
      strip keeps remainder).
- [x] 1.2 Handler uses `resolve` when `Routes` is non-empty; unmatched →
      404 hop with `Err`; matched → forward to `upstream + forwardPath +
      query`. Test: two httptest upstreams behind one gateway target, assert
      each receives the right path/query and the gateway hop records
      `To: <gateway>`; chain a second `Target` behind it and assert its hop's
      `From` is the gateway name.

## 2. ensemble/config — schema and validation

- [x] 2.1 Add `Gateway{Port, Routes []GatewayRoute}` and
      `GatewayRoute{Prefix, Service, StripPrefix}` to `Config.Gateways`;
      `RoutablePort(name) (port int, kind string, ok bool)`.
- [x] 2.2 `Validate`: port > 0 and in `usedPorts`; name not colliding with
      service/database/stub; routes non-empty; prefix `/`-rooted and
      normalised (trailing `/` dropped except bare `/`); target resolvable;
      duplicate prefix. Tests for each rejection plus a clean config and an
      absent block.

## 3. ensemble/orchestrator — wiring

- [x] 3.1 `wireGateways()` at the top of `Up`: one `proxy.Target` per gateway
      with `Routes` built via `RoutablePort` (`http://127.0.0.1:<port>`);
      bind failure returns an error naming the gateway. Test with `:0`-free
      ports via a real `proxy.Proxy` and httptest upstreams, or assert the
      built `Target` through a seam consistent with existing orchestrator
      tests.

## 4. ensemble/server + cmd — surfaces

- [x] 4.1 Topology: gateway nodes (`category: "gateway"`, `status: "static"`,
      `entry: true`) and deduped gateway → target edges. Test on
      `buildTopology`.
- [x] 4.2 Sessions: `handleSessionStart` accepts a gateway name as `entry`.
      Test: start a session with a gateway entry, assert 200 + `edgeAddr`;
      unknown name still 404.
- [x] 4.3 `cmd_up`: gateway names appended to the session manager `entries`.

## 5. Docs

- [x] 5.1 README: `gateways:` block in the config example + a short
      "Gateways" subsection (resolution rule, `strip_prefix`, latency target,
      unproxied-target caveat).
- [x] 5.2 `go vet ./...` and full `go test ./...` across the workspace green.
