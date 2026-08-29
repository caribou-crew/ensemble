## Context

`retrace replay --ref <flow> -- <cmd>` serves a test command's HTTP calls
from a committed reference bundle. `replay.Bundle.Match` (retrace/replay/match.go)
answers with the strict-fail behavior the package's doc comment calls out
("absence is never agreement") for one direction only: a call the bundle
cannot answer is a loud miss. In the other direction it is deliberately
loose — this is *by design*, not a bug:

- `bodyDiff` only reports keys the **recording** declared and the request
  didn't satisfy; a request carrying extra keys is accepted ("a client that
  sends more is not deviating from what was recorded" — match.go:297-299).
- Repeated identical calls are served from the same recorded exchange in
  recorded order (`Match`'s `chosen` selection, match.go:186-197), explicitly
  so a poll-until-ready loop doesn't hang. A flow that starts calling an
  endpoint 5x instead of 1x is indistinguishable, at match time, from a
  legitimate retry loop.
- `Key` (the match bucket) is method + path + query only; headers are never
  part of matching at all (match.go:28-32).

So `retrace replay`'s own report (`exchanges`, `served`, `unused`,
`missCount`, `misses`) can never surface: a surplus call on an
already-matched exchange, a new/changed request header, or a request body
that gained fields. `retrace diff`'s wire plane (retrace/diff/wire.go)
already computes exactly this comparison — but only between two **recorded**
runs; replay is the one backend-free mode, and today nothing pipes a
replay's observed traffic into that engine.

## Goals / Non-Goals

**Goals:**
- Let a CI job running `retrace replay --assert-requests` fail when the
  client's *requests* deviate from the reference — extra calls, changed/added
  request headers or body fields — with zero live backend.
- Reuse the wire plane's pairing/diff logic (`retrace/diff`) and its existing
  tolerances (`wire_rules`, `query_ignore`, `gates.wire.budget_pct`,
  `fail_on`) rather than inventing a second comparison engine.
- Leave `retrace replay` byte-for-byte unchanged when the flag is absent.

**Non-Goals:**
- Comparing the **response** side. A replay-served response is, by
  construction, either the recorded response verbatim (a hit) or a 501/500
  refusal (a miss, which already hard-gates the run today). There is no
  independent response to compare against — see Decision 3.
- Reusing `diff.Build` wholesale (Strawman B in the originating request).
  Rejected — see Decision 1.
- Persisting the observed requests to disk as a `wire.jsonl` a later
  `retrace diff` could read. `--assert-requests` computes and reports its
  verdict inline, in the same process, the same way the existing miss
  report does.
- Any change to `Bundle.Match`'s serving behavior (still loose, still
  poll-tolerant) — the new mode observes and reports *alongside* serving, it
  does not change what gets served.

## Decisions

### Decision 1: Compare in-process via `diff.DiffWire`, not `diff.Build` over a persisted replay run

The feature request's Strawman B — have replay persist a `wire.jsonl` and
run `retrace diff --a reference --b <replay-run>` — was evaluated against
the actual code and rejected:

- `cmd_diff.go`'s `resolveSide` only resolves `--a`/`--b` selectors
  (`reference`, `latest`, a run id, a git sha) inside `runs.RunsRoot`, via
  `runs.FindRun`. A replay run deliberately lives under a **separate** root
  (`replaysRoot`, cmd_replay.go:31-40) precisely so `retrace diff --b latest`
  and `refs.Resolve` never accidentally pick up a manifest-less replay
  directory. Making a replay diffable by selector would mean either
  reintroducing that exact hazard or adding a whole new "diff by explicit
  path" selector grammar — scope `diff.Build` was never asked to carry.
- `diff.Build` (retrace/diff/summary.go) is built for two **fully captured**
  runs: it quarantines on capture-trust verdict and on screen-geometry
  mismatch, diffs a `hops.jsonl` chain, diffs screenshots by checkpoint,
  checks OpenAPI conformance, and computes a capture banner. A replay run
  has none of that — no capture-trust verdict (nothing captured anything),
  no chain, typically no checkpoints. Satisfying `Build`'s invariants would
  mean synthesizing a fake manifest just to walk past checks that don't
  apply, which is more code and more risk than the feature needs.

Instead, `--assert-requests` calls `diff.DiffWire(referenceHops, observedHops, opts)`
directly — the same function `diff.Build` itself calls for the wire plane
(summary.go:684) — with `opts` assembled the same way `diff.OptionsFor`
assembles it for `retrace diff` (WireIgnore, Rules, Normalize; no groups,
since replay doesn't track flow-part groups). This is a lean, in-process
call: no second file format, no run-directory choreography, no new selector
grammar.

**Alternative considered:** build a second, replay-specific comparison
engine instead of reusing `diff.DiffWire`. Rejected: `DiffWire` already does
exactly the pairing (bucket by method+normalized-path+normalized-query,
align within a bucket, missing/extra outside any pair) and field/header
diffing (with `wire_rules` and `query_ignore` honored) this feature needs. A
second engine is a second place for that logic to drift.

### Decision 2: Reference hops come from the bundle's own `wire.jsonl`, already loaded

`replay.LoadBundle` already reads the bundle directory's `wire.jsonl` into
`[]trace.Hop` before lowering it into `[]Exchange` (bundle.go:170-186,
`runs.ReadHops`). `--assert-requests` reads that same file a second time (a
handful of exchanges; the cost is negligible and it keeps `Bundle`'s public
shape — `[]Exchange`, no `used`-mutation-safe way to get back the original
`trace.Hop` — unchanged) to get the reference side of `DiffWire` in the
exact shape `retrace diff` uses. When `Options.TargetFilter` is set
(multi-listener replay), the reference hops are filtered to `h.To ==
TargetFilter` first — same scoping `UnusedExchanges` already applies — so a
multi-listener replay's `--assert-requests` only ever compares a listener's
requests against that listener's own recorded traffic.

### Decision 3: Observed hops mirror the matched exchange's own recorded response; a miss contributes no observed hop

The interesting new signal is entirely on the **request** side (Decision
above, Goals/Non-Goals). To keep `DiffWire`'s response-side comparison from
manufacturing false positives out of replay's own, intentional response
rewrites (`writeHit` strips/rewrites CORS headers, `Location`, and the
`Set-Cookie` `Domain` attribute — server.go:257-279), an observed hop for a
**hit** carries:

- request side: the real incoming method/path/query, the request body
  (decoded + raw, same as `bundle.Exchange.ReqBody`/`ReqRaw`), and the real
  incoming request headers (`r.Header`) — not the recorded `ReqHeaders`.
  This is the whole point: the client's *actual* request.
- response side: a byte-for-byte copy of the **matched** `Exchange`'s own
  recorded `Status`/`Headers`/`Body` — the bytes replay is about to serve
  before `writeHit`'s rewrites, not what actually went out on the wire.

Response-side `DiffWire` output for every hit is therefore empty by
construction — there is nothing to say about a response that is definitionally
the recording. All signal comes from the request-side `BodyDiff`,
`BodyViolations`, `HeaderDiff`, and — because `Match`'s bucket key is
method+path+query only, exactly what `PairCalls` buckets on — from
`Wire.Extra` whenever more requests land in a bucket than the reference
recorded there (the call-count-drift case from the originating request).

A **miss** (`MissUnmatched` or `MissKeyUnavailable`) contributes no observed
hop to this comparison at all. A miss already exits 2 today
(cmd_replay.go:257-259, checked before `--assert-requests`'s own gate would
run) — reporting the same call again as a `DiffWire` "extra" would restate
one finding in two vocabularies. `--assert-requests`'s comparison only
changes the verdict for a run where **every** call matched *and* the match
still hides a deviation — which is exactly the gap this feature closes.

**Alternative considered:** record the response replay actually sent
(post-rewrite) as the observed hop's response, and let `DiffWire` diff it
against the recorded response. Rejected: CORS reflection, `Location`
rewriting, and cookie-domain stripping are unconditional, deliberate
behavior on every hit (server.go's own doc comments call these out as the
"whole of hermetic replay"'s enabling mechanics) — diffing them would fail
every replay that uses cookies or redirects, for a difference `--assert-requests`
did not introduce and a project has no tolerance knob for.

### Decision 4: gate on the same `gates.wire.budget_pct` / `fail_on` shape `retrace diff` uses, exit 2 on failure

`--assert-requests`'s verdict reuses `diff/summary.go`'s wire-budget formula
(`observedFor(s, "wire")`: changed paired entries ÷ total paired entries ×
100) computed over the `diff.Wire` result, plus `Wire.Extra` (untolerated)
count > 0 as an unconditional deviation (an extra call has no "percent
changed" reading — it either happened or it didn't, same as `retrace diff`
treats `Counts.WireExtra` inside its own `changed()` check). Both read
`cfg.ResolveGates(flow)` for `gates.wire.budget_pct`; a project with no
`gates:` configured gets `budget_pct == nil` → unmeasurable → any deviation
at all fails, matching every other zero-value-is-strictest default in this
package (`replay.Options`, `bundle.Match`'s "no candidates ⇒ miss").
`fail_on` is **not** consulted — `retrace replay --assert-requests` has no
other plane to weigh it against; asking for the flag at all is the opt-in
`fail_on` performs for `retrace diff`.

On failure, `cmdReplay` returns `exitGate` (2) — the same code an unmatched
miss already returns, and returned in the same place (after the existing
miss check, so a miss's message is never shadowed by a request-diff one).

### Decision 5: report shape — `extra` and `requestDiff` are additive, `--json` only under the flag

```json
{
  "...": "unchanged fields",
  "extra": [ { "method": "GET", "path": "/showpan", "query": "id=2" } ],
  "requestDiff": {
    "changed": 1,
    "paired": 4,
    "budgetPct": 25,
    "threshold": null,
    "entries": [ /* diff.Entry, request scope only populated */ ]
  }
}
```

`extra` is populated whenever `--assert-requests` is passed, even if empty
(so a consumer can tell "ran clean" from "flag absent"). `requestDiff` is
omitted entirely when the flag is absent — the same "omitted, not
zero-valued" contract `Summary.Budgets`/`UnmeasuredGates` already use for
"not configured" vs. "configured and clean" (summary.go:150-186).

## Risks / Trade-offs

- **[Risk]** Re-reading `wire.jsonl` a second time inside `cmdReplay`
  duplicates `LoadBundle`'s read. → **Mitigation**: a reference bundle is
  "a handful of exchanges" (bundle.go's own characterization); the cost is
  negligible and avoids adding an `used`-mutation-unsafe raw-hop accessor to
  `Bundle`'s public surface for a second caller.
- **[Risk]** A project with encrypted fields (`retrace-recording-encryption`)
  replays decrypted values into the served response; the observed hop's
  mirrored response (Decision 3) must use the **decrypted** exchange, or
  every encrypted field reads as a spurious `changed` the first time
  `--assert-requests` runs against such a bundle. → **Mitigation**:
  build the observed hop's response from the same `decryptExchange` output
  `writeHit` already computes for the hit, not the raw `Exchange`.
- **[Risk]** A project relying on the poll-until-ready pattern (repeated
  identical calls, deliberately) will newly see `--assert-requests` fail on
  the surplus calls that pattern produces. → **Mitigation**: this is the
  documented, intended behavior (call-count drift is exactly what this
  feature exists to catch) — the flag is opt-in per the backward-compat
  goal, and a genuine poll loop is expressible as `wire_rules`/deviations
  the same way any other tolerated repetition is today, or the flow simply
  doesn't pass `--assert-requests`.

### Decision 6: config-only budget, `Call`-shaped `extra`

No `--budget-pct` flag. `retrace diff` has no such flag either — it reads
`gates.wire.budget_pct` from `retrace.yaml` exclusively, and `--assert-requests`
follows the same precedent so a project has exactly one place to set the
tolerance for both commands.

`extra` in `--json` is `diff.Call`-shaped (method/path/seq/status), not the
leaner `replay.Key` shape `unused` uses — an agent reading both `retrace
replay --assert-requests --json` and `retrace diff --json` gets one
vocabulary for "a call with no counterpart on the other side" instead of two.

## Migration Plan

Additive and opt-in; no migration. Ships behind a new flag with no default
change to `retrace replay`'s existing behavior, report shape, or exit codes.

## Open Questions

None outstanding — see Decision 6 for the two questions raised during design
and their resolution.
