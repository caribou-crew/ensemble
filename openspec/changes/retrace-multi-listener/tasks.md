## 1. `retrace/config`: `ListenerEntry` and `Listeners`

- [x] 1.1 Add to `retrace/config/config.go`: `type ListenerEntry struct { Name
      string; Upstream string; Host string; Port int }` (yaml tags
      `name`/`upstream`/`host`/`port`), and `Config.Listeners
      []ListenerEntry` (`yaml:"listeners"`).
- [x] 1.2 Add `(l ListenerEntry) EnvSuffix() string` — upper-cases `Name`
      and collapses runs of non-`[A-Za-z0-9]` to a single `_`, trimming
      leading/trailing `_` (e.g. `"card-api"` → `"CARD_API"`). This is the
      ONE place the env-var-name transform lives; both `retrace/capture`'s
      `Env()` (task 2) and `cmd_replay.go` (task 6) call it — no second
      implementation of the transform.
- [x] 1.3 In `applyDefaults` (or wherever `Discover`/`Load` finalizes a
      `Config` today): if `Listeners` is empty and `Upstream != ""`,
      synthesize `Listeners = []ListenerEntry{{Name: "client-edge",
      Upstream: Upstream, Host: ProxyHost, Port: ProxyPort}}` — the literal
      string `"client-edge"` matches `core/proxy.Target{Name:
      "client-edge"}` already hardcoded in `StartStandalone` today, so a
      config that never touches `listeners:` produces byte-identical
      `Hop.To` values before and after this change.
- [x] 1.4 Add load-time validation (alongside wherever `RedactEntry`'s
      unknown-mode check lives, same "loud failure at load" standard):
      (a) non-empty `Listeners` AND non-zero `Upstream`/`ProxyHost`/
      `ProxyPort` together is an error naming both forms; (b) non-empty
      `Listeners` AND non-empty `Entry` together is an error naming both
      keys; (c) any entry with an empty `Name`, or two entries sharing a
      `Name`, is an error naming the conflict.
- [x] 1.5 Tests: bare `upstream:` config synthesizes exactly one
      `client-edge`-named listener; an explicit `listeners:` list parses
      each field correctly; `upstream:` + `listeners:` together errors;
      `entry:` + `listeners:` together errors; empty name errors; duplicate
      names error; `EnvSuffix()` on `"edge"`, `"card-api"`, `"Auth Service"`
      produces `"EDGE"`, `"CARD_API"`, `"AUTH_SERVICE"`.

## 2. `retrace/capture`: multi-listener standalone capture

- [x] 2.1 Add `Options.Listeners []config.ListenerEntry` (reusing
      `retrace/config`'s own type — the same pattern `Options.Redact
      []config.RedactEntry` already established; `capture` already imports
      `retrace/config`, no new import cycle). `Options.Upstream`/`Host`/
      `Port` are UNCHANGED and keep serving `StartAttached` exactly as
      today — this task does not touch ensemble-attached mode.
- [x] 2.2 In `StartStandalone`: when `len(o.Listeners) > 0`, loop it instead
      of the current single `prox.ServeStoppable(proxy.Target{Name:
      "client-edge", ...})` call — one `ServeStoppable` per entry, on the
      SAME `*proxy.Proxy`/`*proxy.Recorder`/redactor/data-key (one run has
      one of each regardless of listener count), each `Target{Name:
      l.Name, Listen: defaultHost(l.Host)+":"+listenPort(l.Port), Upstream:
      strings.TrimRight(l.Upstream, "/"), InjectBaggage: {session id}}`.
      When `len(o.Listeners) == 0`, keep today's exact single-listener path
      using `o.Upstream`/`Host`/`Port` untouched — every existing
      `capture_test.go`/`hopsource_test.go` call site keeps working with
      zero changes.
- [x] 2.3 Replace `Session`'s singular `prox`/`stopProxy`/`ProxyURL` proxy
      state with a slice (`type sessionListener struct { Name, ProxyURL
      string; stop func() }`), keeping the exported `Session.ProxyURL`
      field set to the FIRST listener's URL for every existing reader
      (owner record, `retrace serve` display) that only knows about one.
      Add an unexported accessor for the full slice.
- [x] 2.4 `Session.Close()`'s teardown calls `stop()` for every listener,
      not just one; a failure stopping one listener does not prevent
      stopping the rest (log and continue, matching this codebase's
      "cleanup that only ran on the happy path is how state leaks"
      standard already documented elsewhere in this file).
- [x] 2.5 `Session.Env()` exports `RETRACE_PROXY_URL_<EnvSuffix>` for every
      listener plus keeps `RETRACE_PROXY_URL` pointing at the first
      listener's URL (so it equals that listener's own
      `RETRACE_PROXY_URL_<NAME>` var too — a single-listener config, sugar
      or explicit, gets both names pointing at the same address).
- [x] 2.6 Tests: two-listener `StartStandalone` call proxies both upstreams
      correctly (each captured hop's `To` matches its listener's name);
      `Env()` on a two-listener session has all three expected vars with
      correct values; `Close()` stops both listeners (a second request to
      either address after `Close()` fails to connect); a single-listener
      call via `o.Listeners` (length 1) behaves identically to the
      existing `o.Upstream` path for the same upstream (hop tag, env vars).

## 3. `retrace/cmd/retrace`: wire `cmd_run.go` to `cfg.Listeners`

- [x] 3.1 In `cmdRun`'s `capture.Options{...}` construction (the standalone
      path only — `StartAttached`'s call is untouched): replace
      `Upstream: p.upstream, Host: p.host, Port: p.port` with `Listeners:
      p.cfg.Listeners` — non-empty by construction, since task 1.3's sugar
      synthesis already ran inside `config.Discover`/`Load` before
      `cmdRun` sees `p.cfg`.
- [x] 3.2 Test: `retrace run` against a two-listener standalone config (no
      ensemble) records both upstreams into one run directory; `retrace
      run --json`'s manifest/env reporting (whatever surfaces env vars
      today, if anything does) is unaffected for a single-listener config.

## 4. `retrace/replay`: per-listener exchange filtering

- [x] 4.1 Add `Target string` to `replay.Exchange`, populated from
      `Hop.To` in `lower()` (the function that turns a `trace.Hop` into an
      `Exchange` — mirrors how `Key`'s other fields are already lowered
      from the hop).
- [x] 4.2 Add an optional target filter to `replay.NewServer`'s options
      (empty string = no filter = today's exact behavior, matching every
      exchange regardless of `Target`): when set, the server only matches
      exchanges whose `Target` equals the filter, and reports a request
      that would have matched a DIFFERENT target's exchange as a normal
      miss (not a match) — the whole point being no cross-listener leakage.
- [x] 4.3 Tests: a bundle with exchanges from two targets, served through a
      server filtered to one target, only answers that target's requests
      and misses the other's; an unfiltered server (empty target) matches
      both, preserving today's behavior for every existing bundle and
      caller.

## 5. `retrace/cmd/retrace`: `cmd_replay.go` binds N ports

- [x] 5.1 Replace the single `net.Listen(*listen)` + single
      `replay.NewServer(bundle, opts, missesPath)` with a loop over
      `cfg.Listeners` (same config `retrace diff`/`retrace run` already
      load): for the FIRST listener, honor `--listen` as its bind address
      (today's exact behavior for the common single-listener case); every
      other listener binds at its own configured `host:port` (or
      ephemeral, same convention as `listenPort`/`defaultHost` in
      `retrace/capture`). Each gets its own filtered `replay.Server`
      (task 4.2) scoped to that listener's name.
- [x] 5.2 Env handed to the test command gets `RETRACE_PROXY_URL_<NAME>`
      per listener plus `RETRACE_PROXY_URL` for the first, mirroring
      `Session.Env()` (task 2.5) — same `ListenerEntry.EnvSuffix()` helper,
      no second implementation of the name transform.
- [x] 5.3 Served/unused/miss counts and the `--json` report aggregate
      across all listeners' servers (sum served, concatenate
      unused/misses) — a miss on ANY listener still fails the whole replay
      (`exitGate`), matching today's "any miss fails the run" rule.
- [x] 5.4 Shutdown (the existing `defer` block) closes every listener's
      `http.Server`, not just one.
- [x] 5.5 Tests: replaying a two-listener reference bundle serves each
      listener's own recorded calls on its own port and misses a call sent
      to the wrong listener's port; a single-listener config (sugar or
      explicit) replays identically to today, including `--listen`
      overriding its one address.

## 6. Docs and end-to-end verification

- [x] 6.1 Add a `listeners:` section to whichever doc already covers
      `upstream:`/`entry:` config (the `retrace-iterate` skill or
      `docs/`), with the strawman-shaped example from the feature request
      (OAuth + Card API) and an explicit note that `listeners:` is
      standalone-only (mutually exclusive with `entry:`, which already
      gets multi-service capture from ensemble's own proxy mesh).
- [ ] 6.2 End-to-end verification against two of the sample stack's own
      stub services (e.g. `payments`/`analytics`, both already bare HTTP
      stubs with no ensemble dependency) run via `retrace run
      --no-ensemble` against a SCRATCH two-listener config — not committed
      to `sample/retrace.yaml`, since the sample's own `entry:`-based
      config already demonstrates multi-service capture through ensemble
      and mixing the two forms is exactly what task 1.4 makes a load
      error. Confirm: both stub responses captured with correct `Hop.To`
      tags, `retrace ref accept`, then `retrace replay` against the
      accepted bundle correctly separates the two listeners' traffic.
- [ ] 6.3 `go test -race ./core/... ./retrace/... ./ensemble/...` and
      `pnpm -r --if-present test` both green.
