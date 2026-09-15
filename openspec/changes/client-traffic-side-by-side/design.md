## Context

`hop.client` already exists and is already trustworthy in the specific way
that matters: `core/proxy.clientIdentity` reads the first configured
client-identity header present, validates it against `proxy.ValidClient`
(`^[a-z0-9][a-z0-9:-]{0,31}$`), and replaces anything failing validation
with `proxy.FallbackClient` ("client") rather than storing it — so no
caller-supplied string reaches a group-by key unexamined. `Hop.Client`'s
own doc comment says the constraint exists "precisely so it can be grouped
and filtered on". This change is the grouping and filtering that field was
built for.

Two facts about the data shape drive every decision below.

**One: client identity lands on the entry hop, not the chain.** The header
arrives at the client edge. An internal service-to-service call carries it
only if that service chose to forward it, which in a stack under
development it generally has not. A naive `h.Client == want` filter
therefore returns one hop per user action and hides the fan-out — and the
fan-out is the entire point. Routing divergence (client A reaches a service
directly, client B goes through an extra edge hop and fails there) lives in
hops that carry no client identity of their own.

**Two: `hop.session` is already the retrace run id.** `capture.StartAttached`
calls `c.StartSession(ctx, SessionRequest{ID: runID, …})`, so ensemble's
session id and retrace's run id are the same string, and `core/trace`'s
`BaggageSession` ("retrace-run") carries it through the stack. The "make a
run and its hops the same object" half of this request is mostly already
true on the wire; what is missing is that nothing records *which control
plane*, so neither dashboard can link to the other.

A related correction worth stating once: the baggage `correlationId`
(`trace.BaggageCorrelationID`) is a **per-request** join key, minted by
`hopCtx.EnsureCorrelationID()` for one request chain. It is not run
identity and cannot be used as it — two calls in the same run have
different correlation ids. Run identity is `hop.session`.

## Goals / Non-Goals

**Goals**
- Server-side client filtering on all three traffic reads, so `ensemble
  traffic --follow --client app-legacy` streams one client's hops with no
  client-side post-processing.
- A client filter and a two-pane side-by-side mode in the Traffic view,
  ergonomically identical to the session filter already there.
- One resolution rule for "which client does this hop belong to", shared by
  Go and TypeScript, with an explicit unattributed outcome.
- A manifest record linking an attached run to its ensemble control plane,
  and navigation both ways.

**Non-Goals**
- Changing the `Hop` schema, bumping `trace.SchemaVersion`, or writing any
  *derived* attribution to disk. (Propagating an identity through baggage,
  per D1a, records an observed fact on the hop that saw it — that is
  capture, not inference.)
- Cross-run diff mode (see Deferred).
- Making side-by-side handle more than two panes. Two is the workflow;
  N panes is a different layout problem and no one has asked for it.
- Forwarding client-identity headers through internal services so the chain
  carries them natively. That is a change to the services under test, not
  to ensemble, and the transitive rule below removes the need for it.

## Decisions

### D1: Client attribution is resolved transitively through `traceId`, at read time, in `core/trace`

A hop's client is: its own `hop.client` when set; otherwise the `client` of
the hop that has one and shares its `traceId`; otherwise **unattributed**.
Resolution is computed over a window of hops (a ring snapshot, a history
page, a run's hops) by building a `traceId → client` index first, then
resolving each hop against it.

The index is built from hops that carry a client identity of their own. If
two hops in one trace carry *different* client identities — which should not
happen, but a stack that forwards headers inconsistently can produce it —
the trace resolves to **unattributed**, not to whichever hop was seen first.
A conflicting trace is a fact about the stack the developer should see, and
silently picking one client would put a hop in a pane where it does not
belong, which is the one failure mode this whole view cannot survive.

A hop with no `traceId` at all resolves to its own `hop.client` or to
unattributed — it never joins a trace group, matching `HopTable`'s existing
grouping rule that a hop with no `traceId` groups with nothing.

This lives in `core/trace` (house rule: one hop schema, no product-local
copies) as something like `ClientIndex(hops) map[string]string` plus
`ResolveClient(h, idx) (string, bool)`. `ensemble/server` uses it for the
`client=` param; `retrace` gets it for free for the run-scoped case.

**Rejected: stamping the resolved client onto the hop in API responses.**
It would save a second implementation in TypeScript. It also puts a derived
value into the field whose whole documented contract is "the client
application self-declared this", where a reader — or a recording written by
a future code path that trusts the API shape — could not tell an observed
identity from an inferred one. `core/trace.Hop.Client`'s doc comment draws
exactly this line against `Hop.From`, and `Hop.Attribution` exists because
the codebase already decided that non-observed attribution must be labeled.
Deriving at read time on both sides, pinned by one shared fixture (D2),
keeps the persisted hop honest.

### D1a: Client identity also propagates in baggage, so most chains carry it natively

*Added during implementation. The read-time rule of D1 survives unchanged as
the fallback; this is what it falls back FROM.*

`core/proxy` puts a resolved client identity into trace baggage
(`trace.BaggageClient`) and reads an inherited one back on the next hop, so
a chain that forwards trace context records the client on every hop rather
than only at the edge. This is not a new mechanism: `BaggageSession`
("retrace-run") already propagates run identity exactly this way, which is
precisely why `session:` filtering already works on a whole chain today
while `client:` would not have.

Three things forced this beyond "it is tidier":

- **Hops are recorded inner-first.** `core/proxy` records a gateway's own
  "client → gateway" hop only after the "gateway → bff" hop it was waiting
  on completes — `HopTable`'s `buildRows` comment documents this. So on a
  live SSE stream the client-carrying entry hop arrives LAST, and a
  server-side `client=` filter driven purely by a read-time index would
  drop every inner hop before its entry hop could attribute it, with no
  second chance. That is the CLI's headline use case (`--follow --client`,
  no `jq`) failing on exactly the gateway topology that motivated the
  feature. With the identity on the hop, the stream filter is a field
  comparison and the ordering problem does not arise.
- **The dashboard never had that problem** — it re-resolves over its
  accumulated window on every render, so a late entry hop retroactively
  attributes its chain. The asymmetry was the tell that the stream was
  being asked to infer something the capture plane should have recorded.
- **It removes the "entry hop outside the window" caveat** for any chain
  that propagates baggage, including history pages.

Precedence: an inherited identity outranks a client-identity header on a
later hop. The field answers "which front-end STARTED this chain"; an
internal service re-declaring a different one is a bug or a forgery, not a
new origin.

Inherited values are revalidated against `ValidClient`, and a malformed one
is **dropped, not bucketed** — unlike a malformed header, which records
`FallbackClient` so a developer is told their app is misconfigured. Baggage
arriving malformed is corruption or injection rather than a misconfigured
app the developer owns, and answering it with a real-looking bucket would
let anything that can reach the proxy park traffic under an identity. The
existing loopback-only rule (`hostAddrs`, which refuses a non-loopback
listener *because* the proxy injects baggage) is what bounds who can reach
it at all; this change adds a fact to a channel that threat model already
covers rather than opening a new one.

Safe for existing recordings: `retrace diff` does not compare `hop.client`
on any path, so chain hops beginning to carry an identity changes no diff
outcome, and `trace.SchemaVersion` is untouched — the field already existed.

### D2: The Go and TypeScript implementations are pinned to one shared fixture

The dashboard partitions its panes in the browser (D5), so the rule exists
twice: Go in `core/trace`, TypeScript in `dashboard/ensemble-ui`. Two
implementations of one rule drift.

Mitigation: a single `core/trace/testdata/client_attribution.json` — a hop
window plus the expected resolution for every hop in it, including the
no-traceId, conflicting-trace, entry-hop-outside-the-window and
fallback-identity cases — asserted by both a Go test and a Vitest test. A
change to the rule that updates only one side fails the other suite.

### D3: The unattributed bucket is explicit, selectable, and never silently hidden

`client=` absent means no filtering (today's behavior). To ask for hops that
resolve to no client, the param value is the sentinel `(none)`.

The sentinel is deliberately unrepresentable as a client identity:
`proxy.ValidClient` requires a leading `[a-z0-9]`, so no real client can
ever be called `(none)` and collide with it. A friendlier spelling like
`none` would collide with a client legitimately named `none`.

In the side-by-side view, hops that resolve to neither pane's client are
not simply dropped: the view reports how many are hidden and offers to show
them, because "the call I am looking for went missing" and "the call I am
looking for is unattributed" are opposite diagnoses and a silent filter
gives the wrong one. This is the same fail-closed reading `runs.Counts{}`
and the unassessed capture verdict take — an absence is reported as an
absence, never as a clean result.

Note that `proxy.FallbackClient` ("client") is a *real* bucket, not the
unattributed one: it means a client-identity header arrived and was
malformed. It is selectable as `client=client` and the dashboard labels it
as the malformed bucket, so a developer whose header is wrong sees that it
is wrong rather than seeing an empty pane.

### D4: Both panes are fed by the one existing stream, partitioned in the browser

The dashboard keeps its single `GET /api/traffic/stream` connection and
splits hops into panes client-side.

The alternative — one filtered SSE connection per pane — doubles the
server's stream fan-out, and worse, makes the two panes independently
reconnectable: after a blip, one pane can be caught up and the other behind,
and a side-by-side view whose two sides are at different points in time is
actively misleading about divergence. One stream cannot desynchronize from
itself.

Server-side `client=` filtering still ships, for the CLI (whose entire ask
is "no `jq`") and for the house rule that anything a UI can do must be a
JSON call first. The dashboard simply does not need it.

### D5: The shared axis is interleaved row slots ordered by `(t.start, seq)`, not proportional time

Both panes' hops are merged into one ordered sequence; each merged position
is one row slot; each hop renders in its own pane's column at its slot, with
the opposite column empty. Ordering is by `t.start`, tie-broken by `seq`
(monotonic at the proxy), so the order is total and stable.

An empty slot opposite a row therefore means exactly one thing: **that call
happened on one client and not the other, at that point in the sequence.**
That is the readable claim the feature is for.

**Rejected: a proportional wall-clock axis.** It is more honest about
duration and bursts, and unusable for this: a developer who taps one app,
thinks for thirty seconds, then taps the other gets thirty seconds of blank
pixels between the two things being compared. Per-hop timing is already one
click away in `HopDetail`, and `TraceWaterfall` already exists for the
proportional view of a single trace.

### D6: The manifest records the ensemble link as an optional pointer

`runs.Manifest` gains:

```go
// Ensemble is the control plane this run was captured against, recorded
// at session start. Nil in standalone mode and in every manifest written
// before this field existed — a zero value would claim an attachment that
// never happened.
Ensemble *EnsembleLink `json:"ensemble,omitempty"`

type EnsembleLink struct {
    API     string `json:"api"`     // control-plane base URL
    Session string `json:"session"` // session id registered with ensemble
}
```

A pointer, absent-is-not-evidence, matching `Stack`, `Device` and
`Fixtures` — the established encoding for "this run may have nothing to say
about this". `Session` is recorded even though it currently always equals
`RunID`: the link should say what was actually registered rather than make
every future reader re-derive an equality that only holds by today's
implementation choice.

The API URL is a local development address (`http://127.0.0.1:…`); it is
written to a manifest that can be committed as a reference bundle. It is not
a secret, it is already recorded in kind by `Manifest.Stack`, and a stale
one degrades to a dead link rather than to a wrong answer.

### D7: Navigation is URL state on both sides, not a new API

The ensemble Traffic view already keeps filter state in the URL
(`urlState.ts`). Deep-linking one run's hops is `?session=<runId>` — no new
route. Retrace's link out is that URL built from `Ensemble.API` +
`Ensemble.Session`. Ensemble's link into retrace reuses the existing
RetraceView. Neither direction needs a new endpoint, and a run whose
manifest has no `Ensemble` simply shows no link.

## Risks / Trade-offs

- **Attribution is only as good as the trace context.** A service that
  forwards neither `baggage` nor `traceparent` breaks both the propagated
  identity (D1a) and the read-time fallback (D1), and its downstream hops go
  unattributed. This is visible (D3's hidden-count) rather than silent, and
  `ensemble doctor` already diagnoses dropped trace context — the link
  between the two is worth a line in the docs.
- **Entry hop outside the window.** A history page or a ring that has aged
  past a trace's entry hop cannot resolve that trace's later hops *by the
  fallback rule*. Since D1a, this only bites chains that dropped baggage;
  those hops go unattributed and the count tells the user why. Accepted: the
  alternative is a second scan of `hops.jsonl` per page, for the oldest edge
  of a window a developer is rarely reading.
- **Two implementations of one rule.** Mitigated by D2's shared fixture,
  not eliminated.
- **Side-by-side is a third Traffic layout to keep working** (single,
  grouped/collapsed, split). The panes reuse `HopTable`'s row rendering
  rather than forking it, so status/size/preflight semantics cannot drift.

## Migration Plan

Additive throughout; no migration, no schema version bump.
`trace.SchemaVersion` is untouched — nothing about the hop record changes.
Manifests written before this change decode with `Ensemble == nil` and show
no link, which is the correct statement about them.

## Deferred: cross-run diff mode

Recorded here so the shape is not lost, and deliberately not specified in
this change.

Once `retrace-run-traffic-link` lands, "legacy run 3 vs. next run 7" is
already a selectable pair of runs whose hops are addressable. The diff
belongs in `retrace diff`'s existing wire machinery, not in a fourth
Traffic-view mode: `retrace diff` already compares two runs' hops, already
has the `comparison-sides` spec's vocabulary for which side is which, and
already distinguishes deltas a human accepted from deltas that should fail
a run — which is precisely the "intentional vs. actionable" labeling asked
for. The dashboard-native version is then a view over that existing diff
output, scoped to two runs from two clients, and the only new question is
how a delta gets labeled intentional (an accepted reference? an explicit
annotation?) — which is a real design question and the reason this is not
being specified alongside a live-view feature.
