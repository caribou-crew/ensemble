## Why

Retrace's standalone mode (a bare `upstream:` in `retrace.yaml`, no `entry:`
attaching to an ensemble stack) binds exactly one proxy listener to exactly
one upstream. An app that makes independent call sequences to more than one
backend during a flow — the common case being an OAuth/token service plus the
API it authorizes calls to — can only have one of those services proxied by
retrace at all. Today the integrator hand-builds and maintains a second mock
server for every extra upstream, keeps it in sync with the real service by
hand, and documents a second port convention nobody else's retrace project
needs. This was hit concretely by the UX Toolkit RN sample app (OAuth on
`:4050`, Card API on `:4000`) and will recur for any standalone-mode app
shaped like BFF+CDN, GraphQL gateway+REST sidecar, or auth+API.

Ensemble-attached mode (`entry:`) does not have this problem — it already
delegates to ensemble's own proxy mesh, which captures the full multi-service
hop chain from one entry point. This change is scoped to standalone mode,
where the gap actually is.

## What Changes

- Add a `listeners:` list to `retrace.yaml` standalone config — each entry
  names a listener (`name`, `upstream`, optional `host`/`port`) — so one
  `retrace run` binds N proxy listeners, each recording against its own
  upstream, in one process.
- A bare `upstream:`/`proxy_host:`/`proxy_port:` config (today's only form)
  keeps working completely unchanged: it becomes sugar for a single
  `listeners:` entry synthesized at config-load time, and its captured hops
  keep today's `Hop.To` value (`client-edge`) so no existing recording or
  committed `.retrace-ref/` bundle changes shape.
- `retrace run`'s environment handshake exposes one proxy URL per listener
  (`RETRACE_PROXY_URL_<NAME>`) plus keeps the bare `RETRACE_PROXY_URL`
  pointing at the first-declared listener, so an existing single-upstream
  test file needs zero changes and a multi-listener test file can address
  each backend by name.
- `retrace replay` binds one listening port per configured listener (reusing
  the same config the capture used) and serves each listener only the
  recorded hops that belong to it — no cross-listener leakage, no path-based
  routing guesswork.
- `listeners:` and `entry:` are mutually exclusive in one config — configured
  together is a load-time error, since `entry:` mode already gets
  multi-service capture from ensemble's proxy mesh and doesn't need this.

## Capabilities

### New Capabilities
- `retrace-multi-listener`: N independently-configured proxy listeners (name,
  upstream, host/port) in one standalone retrace run, with per-listener
  env vars and per-listener replay routing.

### Modified Capabilities
- `retrace-capture-replay`: standalone capture gains multiple listeners
  (was: exactly one `client-edge` listener per run); replay gains
  per-listener port binding and hop routing (was: one listening port,
  routing by wire-rule match only).

## Impact

- `retrace/config`: new `Listener`/`ListenerEntry` type and `Listeners
  []ListenerEntry` field; `Upstream`/`ProxyHost`/`ProxyPort` synthesize a
  single default-named listener at load time when `Listeners` is empty;
  `entry:` + `listeners:` together is a load error.
- `retrace/capture`: `StartStandalone` calls `core/proxy.Proxy.ServeStoppable`
  once per configured listener instead of exactly once (the underlying
  `core/proxy.Proxy` already supports this — "any number of intercept
  listeners inside one process" — so this is wiring, not a new proxy
  primitive); `Session.Env()` emits the per-listener and default env vars.
- `retrace/cmd/retrace`: `cmd_replay.go` binds one `net.Listen` per listener
  instead of exactly one, and scopes each to that listener's recorded hops.
- `retrace/replay`: gains a per-listener hop filter so a bound port only ever
  serves the hops recorded through that listener.
- No change to `core/proxy` itself, `core/trace.Hop` (its existing `To` field
  already disambiguates by listener/target name), `retrace/diff`,
  `retrace/serve`, `retrace/runs`, the dashboard, or `entry:`-mode
  (ensemble-attached) capture.
- Non-breaking: a config with bare `upstream:`/`proxy_port:` and no
  `listeners:` behaves exactly as it does today, byte-for-byte, including
  every existing committed reference bundle's `Hop.To` value.
