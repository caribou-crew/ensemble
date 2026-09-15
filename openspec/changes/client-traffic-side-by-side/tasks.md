## 1. Shared client-attribution rule in `core/trace`

- [x] 1.1 Add `core/trace/client.go` with `ClientIndex(hops []Hop)
      map[string]string` (traceId → client, built only from hops carrying a
      non-empty `Client`) and `ResolveClient(h Hop, idx map[string]string)
      (string, bool)` returning the resolved identity and whether it
      resolved at all. Nothing writes to `Hop`. Verify with unit tests for
      each branch: own client wins; downstream hop inherits via `traceId`;
      no client anywhere → not resolved; empty `traceId` → own field only,
      never a trace lookup.
- [x] 1.2 Make a trace carrying two different client identities resolve to
      unattributed for every hop in it, rather than first-wins. Verify with
      a test whose two conflicting hops are ordered BOTH ways in the input
      slice, so an implementation that happens to keep the first (or the
      last) fails one of the two orderings — a single-ordering test cannot
      fail for the right reason here.
- [x] 1.3 Add `core/trace/testdata/client_attribution.json`: one hop window
      plus the expected resolution for every hop, covering own-client,
      inherited, conflicting-trace, no-traceId, entry-hop-outside-the-window
      and `proxy.FallbackClient` ("client") cases. Add a Go test that reads
      it and asserts every expectation. This file is the contract task 4.1
      pins the TypeScript implementation against.
- [x] 1.4 Document on `Hop.Client` that resolution is a read-time derivation
      living in this package and is never persisted, with a pointer to why
      (the field's existing "self-declared, not trace-derived" framing must
      not be muddied by an inferred value).

## 1b. Propagate client identity through trace context (added during implementation — see design.md D1a)

- [x] 1b.1 Add `trace.BaggageClient` and a `Ctx.Client()` accessor beside
      the existing `BaggageSession`/`Session()` pair, documenting that the
      value is untrusted and validated by its consumer.
- [x] 1b.2 In `core/proxy`, resolve a hop's client as: the validated
      inherited baggage identity, else the client-identity header; write the
      resolved value back into `hopCtx.Baggage` so it reaches the next hop.
      Verify with a two-hop chain test that BOTH hops record the identity
      though only the first saw the header.
- [x] 1b.3 Make an inherited identity outrank a header on a later hop.
      Verify with a chain whose middle service sets a DIFFERENT
      `x-source-client` — an implementation that prefers the local header
      relabels the inner hop and fails.
- [x] 1b.4 Validate inherited identities and discard (never bucket as
      `FallbackClient`) a malformed one. Verify with a unit test on both
      outcomes, plus a test that `ValidClient` rejects
      `trace.UnattributedClient` so the filter sentinel can never collide
      with a real identity.
- [x] 1b.5 Verify the read-time fallback still earns its place: a chain
      whose middle service forwards `traceparent` but drops `baggage`
      records no client on the inner hop, and `trace.ResolveClient` recovers
      it.

## 2. Traffic API: `client=` on all three reads

- [x] 2.1 Add a `client` query parameter to `handleTraffic`
      (`ensemble/server/routes.go`), resolved with `trace.ClientIndex`/
      `ResolveClient` over the ring snapshot, composed with the existing
      `since`/`limit`/`errorsOnly`/`session` filters. Verify with handler
      tests: a filtered request returns the entry hop AND its downstream
      hops sharing a `traceId`; another client's hops are excluded;
      `client=` absent returns byte-identical output to before.
- [x] 2.2 Define the unattributed sentinel as `(none)` in one exported
      place, with a comment stating it is unrepresentable under
      `proxy.ValidClient`. Verify with a test asserting
      `proxy.ValidClient.MatchString(sentinel)` is false — so a future
      loosening of the identity charset that would let a real client collide
      with the sentinel fails the build.
- [x] 2.3 Honor `client` in `handleTrafficHistory`
      (`ensemble/server/traffic_history.go`). The index must be built from
      the same page-scan the handler already performs — do not add a second
      pass over `hops.jsonl`. Verify with tests: a page filtered to one
      client; a hop whose trace's entry hop fell outside the page resolves
      unattributed and is excluded from a named-client filter but included
      by `client=(none)`.
- [x] 2.4 Honor `client` in `handleTrafficStream` (`ensemble/server/sse.go`),
      maintaining the traceId→client index incrementally as hops arrive so a
      downstream hop arriving after its entry hop is delivered. Verify with a
      streaming test that an entry hop followed by a chain hop with no client
      both reach a `client=`-filtered subscriber, and that a chain hop whose
      trace never had a client does not.
- [x] 2.5 Update the three route summaries in `ensemble/server/openapi.go`
      to name the `client` filter. Verify with the existing openapi test that
      the documented surface matches the routes.

## 3. `ensemble traffic --client`

- [x] 3.1 Add `--client` to `cmd_traffic.go` and thread it through
      `client.go`'s `TrafficFiltered` and `TrafficStream` as a query
      parameter — never filtered locally, so the CLI and the API cannot
      disagree. Verify with a CLI test against a stub server asserting the
      request URL carries `client=`, and a test that `--follow --client`
      prints only what the stream delivered.
- [x] 3.2 Make `--client` compose with `--session`, `--since` and
      `--errors-only`. Verify with a test that `--session x --client y`
      sends both parameters on one request. `--export` is REFUSED with
      `--client` rather than composed: it renders a whole session, and
      returning every client's hops to a command line that asked for one
      client's would be a wrong answer dressed as a filtered one. Verify
      with a test that the combination exits 2, names the flag, and never
      calls the API.
- [x] 3.3 Update `README.md`'s traffic section and `docs/getting-started.md`
      with the two-clients-on-one-stack recipe, using neutral client names
      (`app-legacy` / `app-next`). Show `ensemble traffic --follow --client
      app-legacy` as the replacement for a hand-written `jq` filter, and
      name `client_identity_headers` as what makes it work.

## 4. Dashboard: client filter

- [x] 4.1 Port the resolution rule to `dashboard/ensemble-ui/src/` (a
      `clientAttribution.ts` beside `attribution.ts`) and assert it against
      `core/trace/testdata/client_attribution.json` in a Vitest test that
      reads the same file the Go test reads. Verify by mutating the rule in
      one language and confirming that language's suite fails.
- [x] 4.2 Add `client` to `COLON_FIELDS` in `trafficFilter.ts` with a
      `matchesToken` branch resolving against the current window, not
      `hop.client` alone. Verify with tests: `client:app-next` matches a
      downstream hop that carries no client of its own; it does not match
      another client's hop.
- [x] 4.3 Add the client selector to `TrafficView.tsx`, mirroring the
      session selector: distinct clients computed from the window, an
      explicit unattributed option, and the same stale-selection fallback to
      "all" that `sessionFilter` already performs. Verify with a test that a
      selected client whose hops age out of the ring falls back to "all"
      rather than showing an empty table under a stale selection.
- [x] 4.4 Label the `proxy.FallbackClient` ("client") bucket in the selector
      as the malformed-header bucket, reusing/extending
      `components/attribution.ts`'s `CLIENT_IDENTITY_TITLE` so the wording
      stays one source of truth. Verify with a test asserting the label
      renders for that identity and not for an ordinary one.
- [x] 4.5 Reflect the client selection in `urlState.ts`. Verify with a
      round-trip test alongside the existing url-state tests.

## 5. Dashboard: two-pane side-by-side mode

- [x] 5.1 Add a pure `splitPanes(hops, left, right)` module returning the
      merged row slots — each slot carrying an optional left hop and an
      optional right hop — ordered by `(t.start, seq)`, plus the count of
      hops belonging to neither pane. Verify with unit tests: interleaved
      order across panes; a left-only call produces a slot with an empty
      right; equal start times tie-break by `seq` (assert with a case whose
      `seq` order contradicts input order, so a stable-sort-only
      implementation fails).
- [x] 5.2 Render the mode in `TrafficView.tsx` sharing `HopTable`'s hop
      presentation so status/size/preflight semantics cannot drift between
      the merged table and the panes. NOTE: reusing HopTable's row
      *component* proved impossible — its row is a twelve-column `<tr>`, and
      half the width cannot carry seq/trace/route/size/delay and still leave
      the method and path readable, which is what a divergence looks like.
      The anti-drift intent is met by extracting the presentation helpers
      (`statusClass`, `statusIcon`, `payloadSize`, `formatTimestamp`,
      `sessionLabel`) into `components/hopFormat.ts` and having BOTH shapes
      import them; the pane renders a compact cell over those helpers. Full
      detail stays one click away in `HopDetail`.
- [x] 5.3 Show the withheld-hop count with a control to reveal those hops,
      and never drop them silently. Verify with a test: a window containing
      a third client's hops and an unattributed hop reports the correct
      count while two other clients are selected, and the unattributed hop
      appears in neither pane.
- [x] 5.4 Assert that entering side-by-side opens no additional traffic
      stream — both panes partition the single existing subscription. Verify
      with a test counting stream subscriptions across a mode switch; this
      is the assertion that stops a later refactor from giving each pane its
      own connection and letting the two sides desynchronize.
- [x] 5.5 Put the mode and both pane selections in `urlState.ts` and restore
      them on load; leaving the mode restores the merged table's own
      filters. Verify with a round-trip test and a test that exiting the
      mode does not clear the single-view filter state.

## 6. Run identity: manifest link and navigation

- [x] 6.1 Add `Ensemble *EnsembleLink` (`API`, `Session`) to
      `runs.Manifest` — a pointer, absent in standalone mode, documented
      with the same absence-is-not-evidence reasoning as `Stack`/`Device`/
      `Fixtures`. Verify with tests: an attached manifest round-trips the
      link; a standalone manifest emits no `ensemble` key at all (assert on
      the JSON, not on the decoded struct); a manifest written without the
      field decodes to nil.
- [x] 6.2 Populate it in `capture.StartAttached` from the control-plane URL
      (a new `Options.EnsembleAPI`, rather than widening the
      `EnsembleClient` interface and making every test fake implement a URL
      getter it has no use for) and the session id actually registered by
      `StartSession`. NOTE: the planned verification — a fake whose
      registered id differs from the run id — is not constructible:
      `StartSession` returns an edge address, not an id, so the registered
      id IS `SessionRequest.ID` by construction. The equivalent assertion,
      which holds even if the registered id later stops being the run id, is
      that the fake RECORDS what it was asked to register and the link is
      compared against that, never against `RunID`. An unnamed control plane
      records no link at all, and standalone mode records none regardless of
      the option.
- [x] 6.3 Add the link-out on the retrace run/item screen for a run whose
      manifest carries the link, building the ensemble Traffic URL from
      `API` + `Session`. Verify with tests: the link renders with the
      expected href for a linked run; nothing renders for a standalone run.
- [x] 6.4 Accept a session identifier in the ensemble Traffic view's URL
      state and open scoped to it, including when that session has no hops
      in the window (show the empty scope, never fall back to unfiltered).
      Verify with a test for each of those two cases.

## 7. Documentation and closing verification

- [x] 7.1 Document the attribution rule where a user meets it: what
      `client_identity_headers` must be set to, that downstream hops inherit
      through `traceId`, that a service dropping `traceparent` leaves its
      hops unattributed, and that `ensemble doctor` is what diagnoses that.
      Cross-reference from the Traffic view's client-selector tooltip.
- [x] 7.2 Document the side-by-side workflow with neutral client names.
      NOTE: retargeted from `docs/retrace-dashboard-one-pager.md`, which is a
      dated findings/cleanup write-up about a specific review round rather
      than a living dashboard reference — a new feature section there would
      read as part of that round's findings. It went where a user actually
      meets the dashboard instead: the Traffic bullet of
      `docs/getting-started.md`'s tour, plus the "Two client apps on one
      stack" section in `README.md` beside `client_identity_headers`.
- [x] 7.3 Run both suites green: `go test -race ./core/... ./ensemble/...
      ./retrace/...` and `pnpm -r --if-present test`. Budget ~4 minutes for
      `retrace/cmd/retrace` alone; redirect output to a file rather than
      piping through `grep`.
