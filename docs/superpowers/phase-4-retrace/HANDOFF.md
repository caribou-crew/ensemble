# Hand-off: roadmap ticks and spec notes Phase 4 earned

**Why this is a hand-off and not a commit.** Task 18's wrap-up asks for edits
to `openspec/changes/init-ensemble-retrace/tasks.md` and to three spec
documents. `openspec/` is owned by a different session that has been actively
committing in it. Two sessions writing one roadmap is how a tick lands on a box
somebody else just re-scoped, so I ruled these **reported, not written**.

Everything below is verified against the tree, not against the plan's own
claims. Apply it yourself, or tell me to and I will — but I will not race
another session for that file.

## 1. Roadmap boxes — `openspec/changes/init-ensemble-retrace/tasks.md`

**Tick 4.1 through 4.7. Leave 4.8 and 4.9 unticked.**

| Box | What it asked for | Delivered by | Where it lives now |
| --- | --- | --- | --- |
| 4.1 | `retrace run --flow -- <cmd>`, session registration | Tasks 4, 5 | `retrace/capture`, `retrace/cmd/retrace/cmd_run.go` |
| 4.2 | wire-rules matchers (uuid/iso8601/http-date/etag/…) | Tasks 2–3 | `retrace/rules` |
| 4.3 | strict mock server from a reference bundle | Task 12 | `retrace/replay` |
| 4.4 | pixel (thresholds, masks, border trim), wire, hop diff | Tasks 7–10 | `retrace/diff`, `retrace/diff/pixel` |
| 4.5 | compact reference bundles, accept/reject/rule | Task 11 | `retrace/refs` |
| 4.6 | review queue (worst-first), REST + UI | Tasks 13, 15 | `retrace/serve`, `dashboard/retrace-ui` |
| 4.7 | retrace-js, retrace-playwright, retrace-maestro | Task 17 | `adapters/{js,playwright,maestro}` |
| 4.8 | redaction modes + recording encryption | **not started** | Phase 4b |
| 4.9 | a11y-tree diff | **not started** | Phase 4b |

**Add a pointer under 4.8 and 4.9** to the part-2 plan, so the two unticked
boxes read as deferred-with-a-home rather than as forgotten. The spec itself
already flags 4.9 experimental "until device-verified", which is the reason it
was never scheduled here.

Two additions that are **not** roadmap boxes and should not get one: the
`useAsync` hook (Task 14) and the migration of Phase 3's twelve hand-rolled
fetch effects onto it (Task 18). Both came out of the Phase 3 whole-phase
review, not from a spec. Recording them in the Phase 4 completion note keeps
the decision traceable without inventing roadmap scope.

## 2. Spec notes — three, each preventing a specific future mistake

**a. `adapters` spec — the env handshake has four variables, not two.**
`RETRACE_MARKER_URL` and `RETRACE_STRICT` are both plan-exceeds-spec. The spec
requires adapters to "fail loudly if invoked without [the handshake] when
strict mode is on" but never names the switch that turns strict mode on;
`RETRACE_STRICT` is that switch, and leaving it unset preserves the spec'd
default of no-op-outside-a-run. `RETRACE_MARKER_URL` is the HTTP door for
runners that cannot write files (Maestro).

As shipped, `RETRACE_STRICT` accepts `1`/`true`/`yes`/`on` (and the negatives),
**and throws on any other value** — an unrecognised value means someone tried
to turn the safety net on and failed, which must not collapse into "off".
`retrace/cmd/retrace/main.go:41-44` documents all four.

**If the two-variable list is meant to be exhaustive, this is the decision to
make, and it should be made deliberately rather than discovered later.**

**b. `ensemble-api-dashboard` spec — the `logical` half of
`GET /api/traces/{id}` now has a product consumer.** Task 9 makes relay-folded
hops the basis of route and service-count diffing, pinned by
`TestATransparentRelayHopIsFoldedAndNotCountedAsANewRoute`, which fails if the
folding is switched off. Worth writing down because the next reviewer who
greps `ensemble-ui` for `trace.CollapseRelays`, finds nothing, and proposes
deleting it would be deleting live code — the consumer is in `retrace`, not in
the dashboard.

**c. `capture-replay` spec — encryption (4.8) and per-key redaction modes are
Phase 4b, declared not dropped.** Both are named in the part-2 scope
enumeration so the SHALLs have a home.

## 3. The follow-up register, for whenever you want it

**Superseded by section 5**, which has the current count and the current batch.
Kept here only for the one item that has since changed state:

- **F.19** — `LatencyView`'s seeded-state exception needed a counter-assertion
  so its "deps stay `[]`" premise could not rot silently. **This is now CLOSED**
  (Task 18, fix round 2). It took two attempts: round 1's version caught the two
  handlers its test happened to click, and a re-review proved the premise still
  died silently under an actual refresh button. The shipped version asserts the
  property itself rather than sampling handlers, and all six named ways to break
  it now fail.

## 3b. Three things found after this document was first written

**a. A peer session committed our uncommitted working tree.** `9479180
"dashboard updates"` contains only our Task 18 files, under a message describing
none of them, landing 34 seconds after that session's own unrelated Go commit
`d5f8f7e`. The mechanism is a peer running `git add -A` or `git commit -a`.

Nothing broke — the swept state happened to be coherent — but that was luck. A
broad commit from another session can capture files **mid-edit, before any test
has run on them**, and it costs the real author attribution. Our own agents are
under a standing never-`git add -A` rule; that rule cannot bind other sessions.
**Only you can set this convention across sessions:** stage by explicit
pathspec. I did not rewrite the history — amending another session's pushed
commit is a worse hazard than an ugly message, since there is no way to know
whether that session has built on it.

**b. F.22 — a skip-guard that hangs CI for ten minutes, then panics.**
`ensemble/orchestrator/docker_integration_test.go:38`:

```go
if err := exec.Command("docker", "info").Run(); err != nil {
    t.Skip("docker daemon not reachable (docker info failed); skipping docker-gated test")
```

No timeout, no context. Your docker daemon is wedged right now, so `docker info`
blocks forever and the package dies on Go's 10-minute panic. **The guard whose
whole purpose is to skip gracefully is itself the hang.** The code underneath is
fine: `go test -race -count=1 -short ./ensemble/orchestrator/...` passes in ~7s.

The fix is two lines — `exec.CommandContext` with a short timeout. **I did not
make it**, because it is outside Phase 4 and a peer session is actively working
in that package (it committed `d5f8f7e` touching `ensemble/config` and
`ensemble/orchestrator` today). Editing a file another session has open is how
two sessions produce one broken merge.

**c. F.21 — a failed trace load renders its error nowhere.** `useTracePoll`'s
`failed to load trace <id>` (`TopologyView.tsx:244`) sits in JSX **below**
`if (!layout) return <loading/>`. In trace mode `layout` is null whenever
`traceHops` is null, and `useAsync` sets `data` **or** `error` and never both —
so when the error exists the view has already returned the spinner. A failing
trace load shows a **permanent "loading trace …" spinner**. Proven by writing
the test and watching it receive `'loading trace abc123…'`. Fixing the
reachability is also what makes the last unpinned `messageOf` site testable, so
this is one item, not two.

## 4. One thing that needs you specifically

`npm publish` stays inert, as instructed, and is now gated **twice**: the
`npm-publish` job in `.github/workflows/release.yml` is `if: false`, and all
three adapter packages carry `"private": true`.

Enabling publication needs all of it: flip `if: false`, clear `private` in
`adapters/js/package.json`, `adapters/playwright/package.json` and
`adapters/maestro/package.json`, add a real publish step (the current run is a
placeholder), and pass `--access public` since `@caribou-crew` packages are
private by default. Each half now names the other in a comment, so neither can
be flipped alone by accident.

I have not touched any of it, and will not without you saying so.

## 5. Status at the hold

**Phase 4 is complete: all eighteen tasks.** Head `c8647f4`. The final
whole-phase review is the last gate before the push.

Per your instruction — *"after phase 4 wraps up lets commit/merge/push and hold
as sample/dashboard is sufficient for now"* — **Phase 5 (the sample app) is off
the board.** I have not inventoried `sample/` and will not plan it. If it comes
back it starts fresh.

Two mechanical notes on the push, checked rather than assumed:

- **"Merge" is a no-op.** There is no feature branch; all of this landed
  directly on `main` alongside your other sessions, which is the workflow you
  set up. The operation is commit + push to `origin/main`
  (`github.com/caribou-crew/ensemble`).
- **Nothing of anyone else's is riding along.** Only our commits are unpushed —
  your peer sessions have been pushing regularly, so we are not carrying a
  backlog of theirs onto the remote.

### The one I would look at first, if you only look at one

**F.23 — two verdict routes, one of them scoped.** A gate *you wrote* that could
not be measured, sitting outside `fail_on`, exits `pass`/0 — while the **same
gate** measured and breached exits `changed`/1. A less-informed state scores
better than a more-informed one.

Budgets reach the verdict by two paths: `failingBudget` (scoped to `fail_on`)
and `anyFailed` (unscoped). Phase 4's final fix corrected the scoped one.

**Do not "fix" this by widening `anyFailed`.** `applyDefaults` inserts a pixel
gate into *every* project and `config.Gate` is only `{BudgetPct *float64}`, so
nothing downstream can distinguish a gate you wrote from one the tool inserted.
Widening it unscoped turns every screenshot-less build red for a gate nobody
asked to enforce. The real fix is provenance on `Gate` first, then reconciling
the two routes — a data-model change, which is why it is parked rather than
rushed in before a hold.

The behaviour is currently pinned by a test whose comment now says plainly that
this is a **known limitation, not the intended end state**.

### The follow-up register: 23 items (F.1–F.23)

All parked deliberately, each with its reasoning in `progress.md`. They are
deferrals, not loose ends. The five with a decided design and no open question —
the batch that could land in one sitting:

- **F.17** — `PerfResult` needs an additive `Measured bool`, mirroring
  `OpenAPIConfigured`. **Not** a new `Status` value: `"unset"` already means
  "no budget configured" and reusing it gives one state two spellings.
- **F.14** — `rules.Rule.Path == ""` currently means *every path*, a permissive
  zero value shared by REST and CLI. F.7 must not land without deciding it.
- **F.20** — `useAsync` needs a keep-previous-data option. Three bespoke
  last-good-value wrappers in `ensemble-ui` exist only because the hook clears
  `data` on every deps change; one option deletes all three, and it is also the
  right fix for N8.
- **F.22** — the docker skip-guard timeout above. Two lines.
- **F.21** — the trace-error rendering above; also unblocks F5's last site.

F.23 is deliberately **not** in that batch: it needs a data-model decision
first, not an afternoon.

### One pattern worth carrying into Phase 4b

Four independent times this phase, **the mechanism that existed to prevent a
failure was itself the failure**: a socket guard that threw where nothing could
observe it (F4); its fix's `beforeEach` reset, which wiped the evidence the
assertion depended on (R-AL); a probe suite that asserted a child process failed
but never checked *why*, so an unrelated crash read as a successful detection
(N6); and the docker skip-guard that hangs (F.22). Three of those are in one
task, each introduced by the fix for the one before it.

The rule we started the phase with — *a guard that cannot be shown to fail is
not a guard* — turned out to be only half of it. The half that kept catching
things is: **shown to fail for the RIGHT REASON.** Worth writing into the
Phase 4b plan's Global Constraints rather than rediscovering it a fifth time.
