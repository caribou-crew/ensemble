## Context

Standalone capture (`retrace/capture.StartStandalone`) builds exactly one
`core/proxy.Proxy`, calls `ServeStoppable` on it exactly once with
`Target{Name: "client-edge", ...}`, and hands the test command exactly one
`RETRACE_PROXY_URL`. `retrace replay` mirrors this: one `net.Listen`, one
`replay.Server`, matching purely by wire-rule (method/path/body) with no
notion of "which upstream this call was meant for."

Two facts make this smaller than it looks:

- `core/proxy.Proxy` already supports N listeners per process — its own doc
  comment says so ("runs any number of intercept listeners inside one
  process") — and ensemble's orchestrator already calls `ServeStoppable`
  once per service on one shared `Proxy`. Standalone capture using it once
  is retrace's own restriction, not a `core/proxy` limitation.
- `trace.Hop.To` is already set from `Target.Name` and already lands in
  every recorded hop. Two listeners writing into the same `wire.jsonl` are
  already disambiguated at the wire-storage level; nothing about capture's
  storage format needs to change.

Ensemble-attached mode (`entry:`) is unaffected and out of scope: it already
gets the full multi-service hop chain from ensemble's own proxy mesh, which
is the thing this change is teaching *standalone* mode to approximate for
the case where there is no ensemble stack at all.

## Goals / Non-Goals

**Goals:**
- A standalone `retrace.yaml` can declare N listeners, each with its own
  name, upstream, and optional fixed host/port.
- Capturing through N listeners writes one run directory whose hops are
  already disambiguated by `Hop.To` — no new wire schema.
- Replaying that run serves each listener's own recorded calls on its own
  port; a client asking listener A's port never sees listener B's traffic
  and vice versa.
- Every existing standalone config (bare `upstream:`/`proxy_host:`/
  `proxy_port:`) captures and replays IDENTICALLY to today, including the
  exact `Hop.To` value already baked into every committed `.retrace-ref/`
  bundle in the wild.

**Non-Goals:**
- Ensemble-attached (`entry:`) mode. It has a different, already-solved
  version of this problem; teaching it to also understand `listeners:`
  would be solving nothing for a config that has ensemble's own mesh.
- Path-based single-port routing (the "route rules" alternative the
  feature request itself rejected, for the same collision reasons it
  named: two upstreams sharing `/health` or `/v1/status` cannot be
  disambiguated by path alone).
- Cross-listener request correlation (e.g. "the auth call this API call
  depended on"). Each listener's traffic is captured and replayed
  independently; nothing here builds a causal chain between them.

## Decisions

### D1: `Listeners []ListenerEntry`, with `Upstream`/`ProxyHost`/`ProxyPort` as sugar for one entry

```go
type ListenerEntry struct {
    Name     string `yaml:"name"`
    Upstream string `yaml:"upstream"`
    Host     string `yaml:"host"`
    Port     int    `yaml:"port"`
}
```

`Config.Listeners []ListenerEntry` is new. At load time (the same
`applyDefaults` pass that already fills `Thresholds`/`Gates`):

- `Listeners` empty AND `Upstream` set → synthesize
  `[]ListenerEntry{{Name: "client-edge", Upstream: Upstream, Host:
  ProxyHost, Port: ProxyPort}}`. **`"client-edge"` is deliberately the exact
  literal `core/proxy.Target{Name: "client-edge", ...}` already uses in
  `StartStandalone` today** — not a new default name — so a config that
  never touches `listeners:` produces byte-identical `Hop.To` values to
  every run and every committed reference bundle that exists today. A
  friendlier synthesized name (`"default"`, the config's own `App`) was
  considered and rejected for exactly this reason: it would make every
  existing project's next capture diff dirty against its own reference
  bundle for a rename nobody asked for.
- `Listeners` non-empty AND `Upstream`/`ProxyHost`/`ProxyPort` also
  non-zero → load error. One config, one form — mixing sugar and the
  spelled-out list is ambiguous about which wins, so it is refused rather
  than guessed.
- `Listeners` non-empty AND `Entry` set → load error. `entry:` already gets
  multi-service capture from ensemble; a config trying to use both is
  almost certainly a misunderstanding of which mode it's in, and the
  alternative (silently ignoring one) hides that.
- Each explicit entry needs a non-empty, unique `Name` — validated at load,
  same "loud failure at config time, not a mystery at capture time"
  standard `RedactEntry`'s unknown-mode check already set.

### D2: `capture.Options` gains `Listeners []config.ListenerEntry`; `StartAttached`'s singular fields are untouched

`capture` already imports `retrace/config` (`Options.Redact
[]config.RedactEntry` is the existing precedent for reusing the config
package's own type rather than a mirrored one) — `Listeners
[]config.ListenerEntry` follows the same pattern.

`Options.Upstream`/`Host`/`Port` stay exactly as they are: `StartAttached`
(ensemble-attached mode) uses them for a genuinely different purpose (the
ensemble-managed session's fixed bind address, and the optional
URL-bound-auth `RETRACE_UPSTREAM_URL`) that has nothing to do with this
change and is not being touched. `StartStandalone` branches on
`len(o.Listeners)`:

- `> 0` → the multi-listener path (D3): `o.Upstream`/`Host`/`Port` are
  ignored (config validation in D1 already prevents both being set through
  `cmd_run.go`'s normal path; a direct caller setting both gets whichever
  the switch checks first — documented, not silently merged).
- `== 0` → today's exact single-`ServeStoppable` path, byte-for-byte
  unchanged. This is what every existing `capture_test.go`/`ensemble_test.go`
  call site that constructs `Options{Upstream: ...}` directly keeps hitting
  with zero test changes.

`cmd_run.go` always sets `Listeners: p.cfg.Listeners` for the standalone
path (never `Upstream`/`Host`/`Port`) — `p.cfg.Listeners` is already
non-empty by the time `cmdRun` runs, because D1's sugar synthesis happened
inside `config.Discover`/`Load`.

### D3: One `Proxy`, N `ServeStoppable` calls, N session listeners

`StartStandalone`'s multi-listener branch loops `o.Listeners`, calling
`prox.ServeStoppable(proxy.Target{Name: l.Name, Listen: host+":"+port,
Upstream: l.Upstream, InjectBaggage: ...})` once per entry on the SAME
`*proxy.Proxy` (one `Recorder`, one redactor, one data key — a run has one
of each regardless of listener count). `Session` replaces its singular
`prox`/`stopProxy`/`ProxyURL` fields with a slice:

```go
type sessionListener struct {
    Name     string
    ProxyURL string
    stop     func()
}
```

`Session.ProxyURL` (the field, singular) is KEPT and set to the FIRST
listener's URL — every existing reader of it (the owner record, the
non-multi-listener manifest fields, `retrace serve`'s display) keeps
working for the single-listener case, which is unaffected either way. A
new `Session.Listeners() []sessionListener`-shaped accessor exists for
`Env()` (D4) and `Close()`'s teardown loop (`stop()` every listener, not
just one).

### D4: Per-listener env vars, first listener also gets the bare names

```
RETRACE_PROXY_URL=http://127.0.0.1:51000        # first listener — back-compat
RETRACE_PROXY_URL_EDGE=http://127.0.0.1:51000   # every listener, by NAME
RETRACE_PROXY_URL_AUTH=http://127.0.0.1:51001
```

Suffix is the listener's `Name`, upper-cased with non-alnum runs collapsed
to `_` (same transform shape as an env-var-safe identifier anywhere else in
Go tooling — no new dependency, a five-line function). A single-listener
config (sugar or explicit) exports `RETRACE_PROXY_URL` and
`RETRACE_PROXY_URL_CLIENT_EDGE` (or the explicit name) both pointing at the
same address — redundant for that case, but it means an adapter never has
to special-case "am I in multi-listener mode," it just always has the
per-name var available. `RETRACE_UPSTREAM_URL`/`RETRACE_MARKER_URL`/
`RETRACE_RUN_DIR` are unaffected — they are run-level, not per-listener.

### D5: Replay binds N ports, each scoped to its listener's own hops

`replay.Exchange` (lowered from `trace.Hop`) gains `Target string` — set
from `Hop.To`, mirroring `Key`'s other lowered fields. Every exchange in
every EXISTING bundle already has a `Hop.To` on disk (`"client-edge"`
today); nothing needs re-recording for this field to be populated on next
capture, and an old bundle replayed under old code paths is unaffected
since matching still ignores it unless a filter is requested.

`replay.NewServer` gains an optional target filter (a new field on its
existing options struct, empty string = no filter = today's exact
behavior): when set, the server answers only exchanges whose `Target`
matches, and reports a call to any OTHER listener's traffic as a normal
miss rather than a cross-listener answer.

`cmd_replay.go` reads `cfg.Listeners` (same config used at capture — D1's
sugar synthesis makes this non-empty for every existing config too) and,
per entry: binds one `net.Listen` at the listener's configured host/port
(or ephemeral, same `--listen`-flag-equivalent default as today), builds
one filtered `replay.Server` scoped to that listener's `Target`, and
serves it. `--listen` (today's single-address flag) keeps meaning "override
the address of the first/only listener" for the common single-listener
case; multiple explicit ports for a real multi-listener config come from
`retrace.yaml` itself, not new flags — a config already has to name each
upstream, so it is also where each listener's fixed port (if any) belongs.

Env vars mirror D4: `RETRACE_PROXY_URL` + one `RETRACE_PROXY_URL_<NAME>`
per listener, all pointing at replay's bound addresses instead of capture's.

### D6: Diff, serve, dashboard need no changes

`retrace diff`'s wire comparison already operates per-hop; two hops with
different `To` values were never compared against each other and still
aren't. `retrace/serve` and the dashboard render `Hop.To` today as
whichever value it happens to hold — a config with real listener names
just makes that column say `auth`/`edge` instead of always saying
`client-edge`. No code in either package assumes a single listener exists.

## Risks / Trade-offs

- **A listener with a genuinely empty/whitespace `Name` in `listeners:`
  would collide with the sugar-synthesized `"client-edge"` name if someone
  wrote `- { name: client-edge, upstream: ... }` deliberately alongside
  other entries.** Not forbidden — it is a legitimate way to keep one
  listener's hops attributed exactly the way a pre-multi-listener reference
  bundle expects while adding new named listeners alongside it. Config
  validation only requires names be non-empty and unique among themselves.
- **Fixed ports on more than one listener multiplies the "already in use"
  failure surface.** Unchanged behavior per listener (a configured port
  already held fails immediately, naming the port — `ProxyPort`'s existing
  contract) — just N chances for it instead of one. No mitigation beyond
  what already exists: retrace never silently falls back to a different
  port.
- **A test file written against a multi-listener config but only reading
  the bare `RETRACE_PROXY_URL`** will silently talk to just the first
  listener and never notice the second exists. This is a documentation
  problem, not a code one — task 10 covers it in the same doc that
  introduces `listeners:`.
