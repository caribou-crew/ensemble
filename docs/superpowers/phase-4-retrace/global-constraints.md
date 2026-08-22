## Global Constraints

- Go 1.25 workspace (`go.work` uses `./core ./retrace ./ensemble`); module
  path `github.com/caribou-crew/ensemble/...`. **No new third-party Go
  dependency without a justification written into the task that introduces
  it.** This plan introduces exactly one: `gopkg.in/yaml.v3` in the `retrace`
  module (Task 3) — already used by `ensemble/config`, already in
  `go.work.sum`, and the alternative (a second config language for the
  second product) is worse than the dependency.
- **`net/http.ServeMux` traps, both already paid for once in this repo:**
  a METHOD-LESS pattern PANICS at registration when it conflicts with a
  `GET /` SPA fallback — always register per-method (see
  `ensemble/server/routes.go`'s entity-passthrough loop). Subtree-redirect
  301s DROP POST bodies, so register the bare path explicitly alongside any
  `{path...}` wildcard.
- **`ServeMux`'s path cleaning operates on the STILL-ESCAPED path**, so
  `%2e%2e%2f` traversal reaches handlers as literal `../`. Any path joining
  from a request value must root at `/` and `path.Clean` before use — see
  `ensemble/server/ui/ui.go` for the shape.
- **A function that JOINS a caller-supplied component into a filesystem
  path must validate that component; a function handed a fully-formed path
  the caller already resolved does not re-litigate it.** The guard lives at
  the construction seam and there is exactly one guard body. This cost three
  review rounds in Task 1: the first fix validated only `PathsFor`, leaving
  six sibling functions that joined `app`/`flow` straight into
  `filepath.Join` — measured, `ListFlows(root, "../../..")` enumerated
  directory names outside the runs root and `AppendGroupRecord` created
  directories there. `ReadManifest`/`ReadHops` deliberately keep bare
  `string` paths under the second half of this rule, because Task 10 reads
  hop files from repro bundles that are not run dirs; fabricating a `Paths`
  for them would produce a value that looks validated and is not.
  Task 4 paid for it a fourth time in `WatchProxy`.
- Every new HTTP route sits behind the existing Origin/Host guard
  (`ensemble/server/guard.go`, extracted to `core/httpguard` in **Task 4**,
  the task that first needs it). **No task may inline a second copy of any
  part of that guard** — not the `Sec-Fetch-Site` check, not the Host
  check, not the Origin check. Every loopback listener in this plan
  (Task 4's marker door, Task 12's replay server, Task 13's review server)
  wraps its mux in `httpguard.Handler`. Control planes bind loopback only
  (`127.0.0.1`).
- Dashboard toolchain: Vite 8 / TypeScript 7 (native `tsgo`) / vitest 4 /
  happy-dom. **No router, no data-fetching library, no component library.**
  URL-as-state via `history.replaceState`.
- **Every async load in a React view goes through `useAsync(fn, deps)`**
  (`@ensemble/design-system/useAsync`, built in Task 14). Hand-rolling
  `let cancelled = false; … .then(d => { if (cancelled) return; … })` is
  not permitted in any new view. Phase 3's whole-phase review found ten
  copies of that block across five files, and *every* Important finding in
  that review except one was a place where one copy had drifted from the
  others — including the phase's third instance of the same async-race bug
  class, after two had already been found and fixed. The hook is, in the
  reviewer's words, "the one piece of shared infrastructure this phase
  should have had and does not." Task 14 builds it; Task 15 is its first
  consumer; Task 18 migrates the Phase 3 views onto it.
- `pnpm -r build` overwrites the committed `go:embed` placeholder
  `ensemble/server/ui/dist/index.html`; it must never be committed. Task 15
  creates the same hazard at `retrace/serve/ui/dist/index.html` — the
  `.gitignore` stanza and the `git restore` habit apply identically.
- **TDD throughout, RED first.** This project has twice been bitten by async
  races that only a written-first regression test would catch (the
  Recorder's non-blocking fan-out drop; the dashboard's out-of-order
  `EntityDetail` load). Task 5's hop-drain race is the third candidate —
  its test is written before its fix.
- `go test -race ./core/... ./ensemble/... ./retrace/...` AND `pnpm -r test`
  green at every commit, run from the repo root (bare `./...` does not
  resolve there). `gofmt` and `go vet` clean.
- **Every exported Go field that crosses the wire carries an explicit
  `json:` tag, in camelCase.** This covers all of `retrace/runs`,
  `retrace/diff`, `retrace/serve`, `retrace/replay`, and `retrace/capture`.
  Untagged exported fields marshal as `"Method"`, `"NewRoutes"`,
  `"NormalizedPath"` — which would break, simultaneously and silently,
  every REST response, the `--json` CI contract, `summary.json`, the static
  export, and every TS mirror in Task 15. The tags are written into the type
  declarations in this plan; they are not an implementer's judgment call. A
  new exported field on a wire type without a tag is a review rejection.
- One hop schema. `core/trace.Hop` (`schema: "ensemble/1"`) is what
  `wire.jsonl` and `hops.jsonl` contain, verbatim. retrace never defines a
  parallel record type for captured traffic.
- Redaction happens at capture, never post-hoc: every write path in this
  plan pushes hops through a `*trace.Redactor` before they touch disk, so
  Phase 4b can swap in per-key modes at one seam.
- API-first parity: every verb the review UI offers is a REST call an agent
  can make identically, with the same effect.
- **A Go zero value must never mean "fine", and the rule must be pinned by
  a test.** This trap has now appeared five times in this plan:
  `CountTolerance` (0 meant "no tolerance" where unset was intended),
  `HopOptions.Collapse` (a bool documented "default true", which a bool
  cannot express), `CaptureTrust.Status` (an empty verdict ranks equal to
  `ok`, so an unassessed run gates as clean), the zero `runs.Paths`
  reaching `AppendGroupRecord` as an opaque directory string, and the zero
  `rules.Matcher` — "no rule applies" — whose correct `Changed` behavior no
  test required. The rule: for any field where absent and permissive are
  different meanings, make the zero value the SAFE one — invert the boolean
  so `false` means the protective behavior, or reject/normalize the empty
  value at the write seam. Never rely on a comment saying what the default
  "is".
  **The fifth instance is why the second clause exists.** There the code was
  already right and only the net was missing: mutating a zero `Matcher` to
  classify as `Ignored` — literally "no rule means fine" — left the whole
  suite green, and that mutation would make `retrace diff` exit 0 on a run
  where every field changed. Correct-but-unpinned is how this trap survives
  into the next task, so every task that has a zero value meaning "not set"
  owes a test that FAILS when that value is treated as permissive. Write it
  by mutating the behavior and watching the test fail, not by asserting
  against code you just wrote.
  **Third clause, from Tasks C and 6: a plausible-looking value is worse
  than an empty one.** `Counts{}` marshalled to "wire recorded, none seen" —
  a clean plane asserted by any path that forgot to set the field — and
  `Assess(AssessInput{})` returned `ok` / "capture looks complete" from an
  input carrying no evidence at all. In both cases the zero value was not
  merely unset, it was *affirmatively reassuring*. The reviewer's phrasing
  is the one to carry: **`ok` is worse than `""`, because `""` is rejected
  at the manifest seam while `ok` sails through both seams and past Task
  10's quarantine.** So "the returned value is non-empty" is never the
  property to argue; "the zero value is the refusing one" is. And defence
  by unreachability — "no caller can construct that" — is exactly what this
  constraint forbids: an exported function does not get to assume its
  callers.
- **A test whose input production can never construct is a test of a
  hypothetical.** Task 6 shipped attached-mode tests that passed the `-1`
  sentinel straight into `Assess` while the only production call site passed
  `0` — green, correct, and protecting nothing, on the exact surface the
  brief cared about most. This is the tests-that-cannot-fail pattern in its
  subtlest costume: the assertion is right, the wiring is missing, and no
  mutation of the code under test will reveal it. Whenever a test hand-builds
  an input struct, ask what production path builds that same value, and
  prefer a test that drives the real path end to end. Then mutate the WIRING
  — not just the logic — and watch it go red.
- **Mutation runs need `-count=1`, and the mutation needs proof it applied.**
  Two ways a mutation run lies about surviving, both of which nearly fooled
  the controller in one sitting: Go prints `ok ... (cached)` and never
  executes anything, so an identical mutation tried twice returns a stale
  pass; and a substitution whose pattern did not match leaves the file
  untouched while the run reports green. Before believing any survivor,
  confirm the edit landed (`git diff --stat`, or assert the pattern matched)
  and run with `-count=1`. A survivor is the strongest claim this process
  makes; it deserves two seconds of proof that it was ever tested.
- **Run mutations against the whole package, never a filtered `-run`.** A
  surviving mutation is the strongest claim this project's review process
  makes, and a `-run` filter can manufacture one out of nothing: the test
  that would have caught it simply never ran. I did this to myself checking
  Task 9's round 2 — `-run 'OpenAPI|Path'` reported a clean pass on a
  mutation that the full package caught immediately, because the killing
  test was named `...LeftmostLiteralSegmentWinsOverATemplateAtTheSamePosition`
  and matched neither term. Filter to reproduce a known failure quickly;
  never to establish that a mutation survived.
- **A fixture symmetric in the dimension under test cannot detect a defect in
  that dimension.** This is the most-repeated defect of this phase — nine
  instances across Tasks 7, 8 and 9 — and it survives instance-by-instance
  fixing, so sweep for it instead: after writing your fixtures, take each one
  and ask what it would still do if the thing it tests were swapped, dropped
  or inverted. It hides in three costumes:
  - **Value symmetry.** Both sides carry the same numbers, so swapping A for
    B changes nothing. `TrimA`/`TrimB`, `PosA`/`PosB`, `StatusChange` all
    shipped this way.
  - **Structural symmetry.** The fixture's *shape* is symmetric even when its
    values are not — no fixture in Task 9 put a relay on side A, so folding
    side A could be deleted entirely with a green suite. A sweep that checks
    only numbers misses this one.
  - **Mutation-set symmetry.** The blind spot applies to your *mutations*,
    not only your fixtures. Task 10's implementer reported 18 mutations, all
    killed, honestly; an independent 53-mutation sweep then found a 34% kill
    rate, because three of its 18 were true only in the shape it chose and
    the mirror survived — killed on A, alive on B. A perfect scorecard over
    an unrepresentative set proves nothing. Whenever you mutate one arm of
    anything two-sided (side A / side B, request / response, added /
    missing, one gate / another), **mutate the other arm in the same
    breath**, before writing the fix.
  - **Masking.** Two mechanisms that can independently produce the same
    verdict, exercised by one fixture in which both are true, pin neither —
    each hides the other's mutation. When you write a fixture for an
    outcome, ask what *else* in it could produce that same outcome on its
    own, and remove it. Task 10 shipped a live exit-0-on-a-failing-run bug
    behind exactly this.
  - **Rule symmetry.** The subtlest: the fixture is an input on which the
    correct rule and the incorrect rule return the *same answer*, so it pins
    neither. Task 9's tie-break came with a proposed fixture whose two
    candidate implementations agreed on it; it would have been added, gone
    green, and pinned nothing. Ask not only "would this detect a swap" but
    "would this detect the *wrong rule*" — and when the finding is that
    something is unpinned, the acceptance bar is that **deleting the fix must
    turn a test red.**
- **A golden regenerated from a struct production cannot construct is a
  golden of a hypothetical.** A golden file is supposed to be evidence of
  what the system emits; one built by hand-populating a struct and
  marshalling it is evidence only of what the test author typed. Task 10's
  `TestSummaryJsonShapeIsStable` would have regenerated a golden documenting
  `null`s for keys production never emits. Route golden fixtures through the
  same normalisation the real path uses, and prefer building them from the
  entry point over hand-populating them.
- **Assert over the output, not over your own inventory of the output.** A
  test that lists the field names it checks can only ever cover the fields
  its author remembered. Task 10's empty-`Summary` test walks the marshalled
  JSON instead, and immediately found two array fields the implementer would
  not have listed by hand — and it will cover fields added long after it was
  written, on the day they appear. Whenever a property should hold for *all*
  of something (every array field encodes as `[]`, every exported endpoint
  requires auth), enumerate that something mechanically inside the test.
- **Never assert a CLI exit code through `go run`.** `go run` treats a
  non-zero child as its own failure: it prints `exit status N` to stderr and
  itself exits **1**. Measured, not assumed. `retrace` defines a 0/1/2/3 CI
  contract (Task 10), so an assertion written against `go run` checks the
  wrong number in every case that matters — it passes only for 1. Build a
  binary (`go build -o <tmp> ./retrace/cmd/retrace`) and run that, or use
  `exec.Command` + `exec.ExitError.ExitCode()` inside a Go test. Found by
  Task 1's implementer.

---

## Fixture symmetry — sixth costume: the asymmetry asserted against an impossible value

Found in Task 15 (review F2). Worse than the unasserted asymmetry, because it
inverts the signal a reader uses to judge a test.

`Section.Name` is `string` in Go and is `""` for the unnamed section — never
`null`, which `order.go` cannot construct. `WireDiffTable.test.tsx` seeded its
fixture with `null`, asserted all three section names, and pinned the fallback
copy. It therefore reads as the most discriminating test in the file while
discriminating **nothing about production**: the branch it exercises is
unreachable, and the branch production always takes (`''`) renders a blank
header in every flow that has not adopted markers.

Check: for every fixture value in a test, ask **which line of production code
constructs this value.** If none does, the assertion around it is decoration.
This is only findable from the producer's side — reading the test, or the type,
will not reveal it.

## Mutations that die at the type-checker are not caught by the test

Twice in Task 15 (report R-U(a), review F4) the obvious mutation was killed by
`tsc --noUnusedLocals`/`TS2741` rather than by any assertion. That credits the
test with a catch the **type system** was making, and the test is still blind.

When a mutation dies at `tsc`, it does not count. **Sharpen it to one that
compiles** — the drift a real implementer would actually introduce — and
re-run before believing the test bites.

### Refinement to the sixth costume: a guard test vs. a vacuous one

Spot-checking Task 15's re-review found `CaptureBanner.test.tsx` rendering
`{status: '', summary: ''}` — a value production can no longer emit, which is
the sixth costume's exact signature. It is **not** an instance, and the
difference is worth stating because the two look identical from the fixture:

- **Vacuous** (F2): the test claims to describe **production** — *"names each
  section from summary.sections"* — while its fixture holds a value no
  producer constructs. It reads as discriminating and discriminates nothing.
- **Legitimate guard**: the test claims to pin a **defence** against a value
  production does not emit *today*, and its comment says exactly that,
  including why the defence must survive (here: `trace.Verdict`'s Go zero
  value is still `""`, and `Record<Verdict, BadgeTone>` totality is what makes
  the next construction path a compile error rather than a grey badge).

**The comment is the discriminator, and that makes it load-bearing rather than
decorative.** A guard test whose comment does not say "nothing emits this
today, and here is why the arm survives" is indistinguishable from a vacuous
one, and the next reader deletes it as dead weight — or worse, reads it as
evidence that production emits the value.

## The transcribing layer can ASSERT a distinction away, not just drop it

Found in Task 16 (review F-1). The phase's defect class had one direction
until now: a distinction the producer drew, dying in the layer that
transcribes it. This is the inversion, and it is worse.

`budgetsOf` deliberately emits **no Gate** for a plane that was unmeasurable,
by the same code path as a plane nobody configured, and its comment gives the
reason: otherwise a wire plane that paired nothing "would report a CLEAN gate
on the run with the least evidence in it". The producer refused to emit the
reassuring zero. The static report then printed, under the budgets table:

> *"A plane with no row here is not gated at all, which is different from
> being gated at a threshold of zero."*

— an affirmative claim the Summary never made, and false exactly when a
configured gate could not be evaluated.

Two things make this the quietest shape found so far:

1. **It is a claim about configuration**, and configuration is the one thing a
   reader of an artifact cannot check from the artifact.
2. **The prose was written to be helpful.** It exists because its author was
   thinking about the zero-value trap — and reached for a reassuring
   generalisation while doing it.

Check: for every explanatory sentence a rendering layer adds, ask **which
field of the producer's output makes this true**, and whether the producer can
emit a state where it is false. A sentence with no field behind it is an
assertion the layer is making on its own authority.

## An equivalent-mutant claim is discharged by pinning the PREMISE, not the arm

(Task 16 re-review, M4.) When a reviewer reports a surviving mutation and the
implementer answers "that arm is unreachable", mutating the arm's text again
proves nothing — it survives *because* the claim is true. The test to demand
is the one that pins **what keeps it unreachable**. In M4 that was
`applyDefaults`' pixel default; disabling it killed two independent tests, and
that is what made the equivalence claim safe rather than merely plausible.

Generalises the earlier rule about deleting currently-unreachable guards: an
unreachable arm is only safe while something keeps it unreachable, so the
premise is the thing that must be under test.

## When a finding rests on reading control flow, build the fixture instead

(Task 16 re-review, sweep b.) The re-reviewer misread the template's brace
nesting and was one step from filing a false finding. Rather than re-read it,
it **built a throwaway probe test** against a real quarantined flow in a
project gating all four planes — and the probe showed the block was correctly
nested. Re-reading the same code with the same eyes reproduces the same
misreading; a fixture that would exhibit the defect does not.

Ask for this explicitly when a finding's whole argument is "this branch is
reached when…". Cheaper than a fix round, and it fails honestly in both
directions.

## A TS layer that PRODUCES input for Go is a different risk than one that mirrors it

(Task 17 pre-dispatch scan.) Every mirror defect in this phase was "the two
sides disagree". A producer defect is "the Go reader silently tolerates what
the producer got wrong" — and this repo's readers are deliberately fail-open
in several places, for good reasons that all assume the *writer* was correct.

Before dispatching any task where non-Go code writes a file or request the Go
parses, read the reader and answer one question: **what happens on a
slightly-wrong input — an error, or silence?** Four of five answers were
silence. Silence at that boundary is unbounded: it makes a broken adapter look
exactly like a user who never called the API.

## An artifact file existing is NOT the artifact being finished

(Task 17, controller error.) I read `task-17-review.md` because it existed and
an idle notification had arrived, built a fix brief from it, and dispatched.
The reviewer was still writing: it went on to add two findings and renumber,
so the brief covered 9 of 11 and two real defects were never dispatched. The
implementer's "nothing left reported-not-fixed" was true against my brief and
false against the review.

The completion signal is **the agent's own return message**, never the
presence of the file it was told to write, and never an idle notification —
an agent can go idle mid-task and resume. This sits beside the existing rule
that report files are lagging indicators and the working tree is live; the
gap it closes is that a *partial* artifact reads exactly like a complete one.

Practical form: before building anything from a subagent's artifact, confirm
the agent has reported. If you must read early, re-read and diff before you
act on it.

## A guard that only stops the careful caller is not a guard

(Task 17, real F-6.) `retrace-maestro group add to cart` silently recorded the
part name `"add"`, while the honest `group "add to cart"` correctly threw —
because `parseArgv` truncated at index 1 and the *truncated* name passed the
validation the whole name failed. Maestro's documented invocation is the
whitespace-split form, so the guard fired on the input nobody sends and was
bypassed by the input the docs tell you to send.

When a validated value can reach the validator by more than one path, check
that **every** path presents the same value. Truncation, joining, splitting,
defaulting and normalisation all happen upstream of validation, and each is a
place the honest input and the real input diverge.

## An agent is alive until it reports or you stop it (R-AM, Task 18)

A subagent is presumed **ALIVE** until one of exactly two things happens: it
returns its report, or `TaskStop` returns success for it.

Never infer that an agent has finished or died from:
- its absence from an agent listing (listings may omit in-process subagents
  entirely — verify by looking for an agent you *know* is running);
- an idle notification;
- elapsed time;
- the presence, absence, or apparent completeness of its output file.

This is one constraint with two failure directions, and this phase paid for
both. Treating **a file's existence** as *finished* dispatched a fix round
against 9 of 11 findings and let two real defects through (Task 17). Treating
**an agent's absence from a listing** as *dead* put two agents on the same job,
each mutating what the other was measuring (Task 18).

If an agent appears stuck, `TaskStop` it deliberately and record that. Do not
spawn a replacement alongside it: that converts a possibly-slow agent into a
guaranteed contaminated tree, and forces you to choose which agent's evidence to
throw away.

**Corollary, for any agent working in a shared tree:** gate every measurement on
`git status --porcelain` and stop if it shows a change you did not make. That
habit is what caught this contamination — in minutes, and not by the controller.

## A subagent's plain text is invisible to the controller (Task 18)

A subagent's ordinary prose output does **not** reach the controller. Only an
explicit `SendMessage` (or the agent's final return value) is delivered.

Task 18's re-reviewer wrote two complete summaries as plain text and went idle
after each. From my seat that was indistinguishable from a stalled agent: two
idle notifications, no report. I diagnosed "stuck" and stopped it. It had in
fact finished, and its report arrived only when it later used `SendMessage`.

Two consequences, both binding:

1. **Every dispatch must state the return channel explicitly**, not just the
   return format: "report by sending me a message — plain text output does not
   reach me."
2. **Add it to the differential.** An agent that goes idle without reporting has
   three possible states, not two: still working, genuinely stuck, or *finished
   and reporting into a channel the controller cannot see*. The third is
   invisible and looks exactly like the second.

This compounds with R-AM rather than replacing it. R-AM says an agent is alive
until it reports or you stop it. This says the report may have been written and
lost. Together: when an agent goes quiet, **ask it directly on the channel you
can hear** before concluding anything about its state.
