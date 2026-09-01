## Context

`core/proxy.Target.Upstream` is already "the real backend a listener
forwards to" — today it's always derived as `http://127.0.0.1:<port>` by
`wireProxy` (`ensemble/orchestrator/orchestrator.go:1198`), computed once
per service at `ensemble up` and never touched again; `wireProxy` even
short-circuits (`already := o.wired[name]`) if called a second time for the
same service. `Proxy` (`core/proxy/proxy.go:145`) holds one shared
`http.Transport` for every target, explicitly commented "Local dev: no
proxies, no TLS between local services" — there is no per-target transport
and no TLS/mTLS configuration anywhere in `core/proxy` today.

`Flip` (`ensemble/orchestrator/flip.go`) already switches a running
service between two declared placements (`native`/`docker`) live, via
`stopCurrent` + `startServiceAs`, without touching the proxy listener —
that's safe today only because both placements share the same `Port`, so
the derived upstream address never changes. `Reconcile`
(`ensemble/orchestrator/reconcile.go:227`) independently already stops and
restarts a service whose config changed and re-calls `wireProxy`, which is
the existing "config change restarts the one service" path.

`retrace`'s capture already has a concept of reduced visibility:
`runs.Manifest.Mode` (`ModeEnsemble`/`ModeStandalone`/`ModePixel`) and a
nilable `Hops *Counts` distinguish "full chain recorded", "no chain
recorded" (standalone), and "pixel-only, hops N/A" at the whole-run level
(`retrace/runs/manifest.go:15-71`). `Stack.Services`
(`retrace/runs/stack.go:21-27`) already fingerprints each service's backing
version so two runs' backends are comparable — the same distinguishing
purpose passthrough needs for "which service was pointed at QA", just for
a different reason (dev/backend drift vs. local/remote comparison).

Auth redaction for a passthrough target's real DPoP credential is already
solved: `defaultRedactHeaders` in `core/trace/redact.go` hardcodes
`Authorization`/`dpop` as always-destroy-mode, and redaction runs at
capture time regardless of what kind of target produced the hop.

## Goals / Non-Goals

**Goals:**
- A `Service` can declare a remote HTTPS `upstream` and be flipped into and
  out of that placement live (GUI, CLI, or REST), exactly like flipping
  native/docker today, including when the remote requires a client
  certificate (mTLS).
- Read-only-by-default: a passthrough target refuses non-GET/HEAD unless
  explicitly opted in, no exceptions by label.
- A run that includes a passthrough leaf is diffable and its reduced scope
  is visible in the recording itself, not just inferred by a human who
  happens to know which service was remote.
- The dashboard's existing Services tab is the only UI surface touched —
  no new view.

**Non-Goals (this proposal):**
- No fault/latency injection *into* a passthrough target beyond "skip by
  default" — arm-gating a deliberate injection later is a follow-up noted
  in `docs/stretch-ideas.md` §2, not built here.
- No named `environments:` config block — passthrough fields live inline on
  the service that uses them (decided over a shared/reusable block; nothing
  here forecloses adding one later if multiple services need to share one
  QA definition).
- No live-credential resolution for `retrace revalidate` against a
  passthrough target. `revalidate` replays recorded request headers
  verbatim except any header whose recorded value is the literal redacted
  marker, which `sendableRequestHeader` drops rather than sends
  (`retrace/replay/revalidate.go:160-194`) — a deliberate choice documented
  there to avoid a false-positive 401 the recording didn't cause;
  `cmd_revalidate.go:115-116` renders the resulting drift with a pointer at
  `redact` in retrace.yaml. So revalidate against an auth-gated passthrough
  target will report the auth failure as "drift," which is misleading but
  pre-existing behavior, not a regression this proposal introduces. Solving
  it (live creds from env, a Britive-style secrets hook) is future work.
- No full remote-hop reconstruction (the separate DD-trace-import idea).
- No changes to how `Variants`/`SetVariant` work — passthrough is a third
  `Flip` placement, not a `Variant`, because a `Variant` shares `Port` with
  its siblings by design (`hasBackingFields`/`ResolveService`) and a remote
  URL has no `Port` to share.

## Decisions

**`startServiceAs`/`defaultPlacement` need a third branch, confirmed by
code, not assumed.** `defaultPlacement` (`orchestrator.go:1064-1074`)
resolves native-if-`Run`-else-docker, and `startServiceAs`
(`orchestrator.go:1080-1160`) hard-fails ("no native run command
configured" / "no docker placement configured") if neither is set for the
placement it's asked to start — there is no existing "start nothing"
branch anywhere in the orchestrator today, and `core/stub` is not a fit
(it answers canned routes in-process; it has no upstream to forward to at
all, per `proxy.go:61-65`'s own comment). Entering `passthrough` placement
must bypass `startServiceAs` entirely rather than call it with a new
placement value that then has to short-circuit inside — the function's
whole shape is "start a real backing," which passthrough has none of.

**Passthrough is a third `Flip` placement, not a new lifecycle concept.**
`Flip` already models "one logical service, multiple backings, live
switch, brief downtime acceptable during the switch" — exactly the
`card-ops-e2e.mjs MODE=local|qa` shape. Extending it beats inventing
parallel machinery: `Flip` gains a target-placement parameter (`native` /
`docker` / `passthrough`) instead of inferring "the other one" from a
binary state; entering `passthrough` calls `stopCurrent` (killing the local
process, if any, exactly as today) and skips `startServiceAs` entirely;
leaving it calls `startServiceAs` normally. `Flip` requires the service to
declare **both** a local placement (`run` or `docker`) and `upstream` —
same "no silent no-op" rule it already enforces for native/docker.

**`wireProxy`'s re-wire gap is closed generally, not just for
passthrough.** Rather than special-casing "if switching into/out of
passthrough, tear down and re-`Serve`," `wireProxy` gains a real
"resolved upstream (and TLS config) changed since last wire" check that
tears down and re-binds the one listener whenever it fires — Flip driving
that check for placement changes is then just one caller, and any future
reason a resolved upstream changes gets the same correct behavior for
free. The rebind briefly drops the listener socket; acceptable because the
service being flipped is, by construction, not currently serving traffic
mid-flip (its local process was just stopped, or is about to be started).

**mTLS material resolves in `ensemble/config`, not `core/proxy`.**
`core/proxy` stays free of file/env I/O (matching its existing shape — it
takes a `Target`, never a filesystem path). `ensemble/config.Validate`
reads `client_cert_file` (relative to `Config.Dir`, same convention as
`readiness:`'s `file:`) and resolves `client_key_env`/
`client_key_passphrase_env` through `Config.LookupEnv` (the same env/`.env`
precedence `datadog-latency-import` already added), producing a resolved
`tls.Certificate` that `wireProxy` attaches to the `Target`. A target with
`TLS` set gets its own `http.Transport{TLSClientConfig: &tls.Config{
Certificates: []tls.Certificate{cert}}}`; every other target keeps sharing
`Proxy`'s existing no-TLS transport untouched.

**The read-only rail is enforced once, in the request handler, keyed only
on `Target.Passthrough != "" && !Target.AllowWrites`.** No label matching
(rejected in brainstorming: guarding only `prod` was weaker than needed and
this v1 is GET-only anyway). A refused write is recorded as a hop with
`Status 502` and an explicit error string, never silently dropped — matches
the "unsupported protocol" handling already in `proxy.go:495-502`, which is
the existing precedent for "refuse and still record."

**Reduced-scope capture gets a new additive field, not a Mode override.**
`Manifest.Mode`/`Hops` stay exactly as they mean today — a run with any
local services attached is still `ModeEnsemble` with real `Hops`, because
those local hops genuinely were recorded. `runs.Stack` gains `Passthrough
[]string` (service names), populated by whatever assembles `Stack` today
from the live orchestrator's config (mirrors how `Stack.Services` is
already populated per-service). `retrace/diff`/`retrace/serve` read it to
annotate a diff or verdict as reduced past those specific boundaries,
rather than only being able to say "reduced" or "not" for an entire run.

## Risks / Trade-offs

- **[Risk] A rebind gap during Flip races an in-flight request from a
  client that doesn't know the flip happened.** → Mitigation: this is the
  same risk `Flip` already accepts for native/docker (the local process is
  killed mid-request today too); documented as existing, accepted
  behavior, not something this proposal makes worse.
- **[Risk] A committed `ensemble.yaml` accidentally ships a real QA URL
  with `allow_writes: true`.** → Mitigation: `allow_writes` defaults to
  `false`; `Config.Validate` could additionally warn (not block) when
  `allow_writes: true` and `passthrough` are both set, so it's visible at
  `ensemble up` time, not just in a diff review.
- **[Risk] `client_key_env` names a var that isn't set, or the PEM is
  malformed.** → Mitigation: `Config.Validate` fails `ensemble up` at load
  time with the offending service and env var named, same failure timing
  as every other misconfiguration this codebase already treats as
  load-time, not request-time.
- **[Trade-off] Extending `Flip` to 3 placements instead of adding a
  separate "SetPassthrough" action is a slightly larger diff in
  `flip.go`/`ServicesView.tsx` than a bolt-on endpoint would be.** Accepted
  — a second, parallel "how do I change what backs this service" action
  would be confusing next to the existing Flip control for no real benefit.

## Open Questions

- Should `wireProxy`'s generalized re-wire check also fire for a plain
  `Reconcile`-driven config edit (not just a `Flip` RPC) so editing
  `upstream:` in `ensemble.yaml` and re-running `ensemble up` "just works"
  without requiring the service to also declare a local placement? The
  proposal's v1 assumes the common case (both placements declared, flipped
  via RPC) but the underlying `wireProxy` fix should make this fall out for
  free — worth confirming with a test during implementation rather than
  assuming.
- `retrace revalidate`'s auth gap (see Non-Goals) will surface the first
  time someone points it at a passthrough target expecting it to "just
  work" — worth a clear error message pointing at this limitation rather
  than a confusing drift report, even though solving it is out of scope.
