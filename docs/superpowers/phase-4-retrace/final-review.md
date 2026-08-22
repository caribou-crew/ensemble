# Phase 4 (retrace) — final whole-phase review

Reviewed at head `c8647f4`, eighteen tasks complete. Worked from the current
tree plus the decision ledger, not from the 182-commit diff.

**Verdict: the phase is coherent and shippable.** The wire contracts agree,
the dependency direction is clean, the Go↔TS mirror is exact, and the
zero-value constraint holds at every seam I probed but two. Six findings
follow, ranked. Three are CONFIRMED by fixture against the built binary; two
of those are the phase's signature defect recurring a fifth and sixth time,
exactly where the addendum said to look.

Method note: every claim below that says CONFIRMED was produced by building
`retrace` to `/tmp` and driving a real project through `run` / `ref accept` /
`diff` / `export` / `serve` / `revalidate`, or by a standalone Go program
against the real exported functions. Nothing in the repo tree was modified;
`git status --porcelain` is empty (pasted at the end).

Suites: `go test -race -count=1 -short ./core/... ./ensemble/... ./retrace/...`
→ **exit 0, every package ok** (F.22's `-short` workaround as briefed; not
re-reported).

---

## The cross-cutting pattern: a fifth and sixth instance

The addendum names four independent cases where *the mechanism that existed to
prevent a failure was itself the failure* (F4, R-AL, N6, F.22) and asks for a
fifth. There are two, both in `retrace/` production code rather than test
infrastructure, and both are the same shape as the four: the protective
mechanism produced a state that reads as a pass.

F-1 is the sharper one, because the mechanism is the phase's own zero-value
discipline: `budgetsOf` was taught to *refuse* to emit a reassuring gate row,
and the refusal is then read as "no gate failed" by the exit-code seam.
F-2 is the classic form: a feature built so a declared pause is not misread as
a dead proxy becomes, on a zero value, a blanket disabling of the dead-proxy
detector.

---

## Findings, most severe first

### F-1 — CONFIRMED — Important
**A configured gate that could not be evaluated exits 0, and only one of the
four faces of the report says so.**

`diff.budgetsOf` deliberately emits **no `Gate` row** for a plane that is
configured but unmeasurable (`summary.go:658-676`, `observedFor`), precisely
so a plane with no evidence cannot report CLEAN. Task 16 F-1 then noticed that
the *static export* was papering over the distinction and fixed it there, by
re-deriving the fact from `cfg` in `serve/export.go:631 unmeasuredGates`.

That fix landed in exactly one consumer. The other three read
`Summary.Budgets` alone — and `export.go:207-209`'s own comment states that
`Summary.Budgets` alone *cannot* tell "not gated" from "gated, and not
evaluated":

| consumer | says the gate was not evaluated? |
|---|---|
| `retrace export`'s HTML report | **yes** — "Gated by this project's config and NOT evaluated on this run: `perf` `pixel` … That is not a gate that passed." |
| `retrace diff` (text) | no — prints `VERDICT: pass` |
| `retrace diff --json` (the agent contract) | no — `"budgets": []`, `"verdict":"pass"` |
| review UI (`retrace-ui/src/App.tsx:251`) | no — `summary.budgets.length > 0` renders nothing at all |

`failingBudget(s.Budgets, cfg.FailOn)` iterates rows that do not exist, so an
unevaluated gate is indistinguishable from a passing one at the seam that
decides the exit code. The absence of a row is read as permissive — the
zero-value constraint's clause 1, at the gate itself.

**Reproduced (`/tmp/p4probe/proj2`, real binary):**

```yaml
# retrace.yaml
app: web
wire_rules: [{headers: {date: http-date}}]
gates: {perf: {budget_pct: 10}}
fail_on: [perf]
```
Record → `ref accept` → record → `retrace diff`:
```
-- (unnamed) --
  GET /cart [identical]
VERDICT: pass
DIFF EXIT=0
$ retrace diff --json | …
verdict pass
budgets []
perf {'status': 'unset', 'measuredMs': 0.63, 'budgetMs': 0}
```
`retrace export` on the *same* two runs, same config:
> Gated by this project's config and NOT evaluated on this run: `perf`
> `pixel` — this run carried no evidence to measure those planes against, so
> no budget was computed for them. **That is not a gate that passed.**

**Why this is not a corner case.** `config.applyDefaults` (`config.go:462`)
auto-inserts a `pixel` gate into *every* project, so every flow that captures
no screenshots has a silently-unevaluated pixel gate. The perf half is a
two-key misconfiguration a user will actually make: `gates.perf.budget_pct`
without `flows.<flow>.perf_budget_ms` gates nothing, forever, with no warning
on any CLI surface.

**Concrete failure.** An operator writes `fail_on: [perf]`, ships it, and
believes perf regressions break the build. They never can: `s.Perf.BudgetMs ==
0` makes the plane unmeasurable, no `Gate` is emitted, `failingBudget` returns
false, verdict `pass`, exit 0, on every run forever. The only artifact that
would have told them is one they have to generate with a different subcommand.

**Shape of the fix (not applied).** The fact belongs on `diff.Summary`, not
re-derived per consumer — `unmeasuredGates` is already written and already
takes `(cfg, summary)`. Ruling `retrace diff` to exit 3 on an unevaluated
`fail_on` plane is a second, separable decision; even without it, the CLI and
the UI need the sentence the HTML already has.

---

### F-2 — CONFIRMED — Important
**One `groups.jsonl` line with no `ts` disables gap detection for the whole
run, and the run still reports `capture ok`.**

`runs.GroupRecord.TS` is a bare `time.Time` with a bare `json:"ts"` tag
(`runs/groups.go:19`). A record that omits `ts` unmarshals cleanly — Go's zero
time. `DeriveGroups` sorts by `TS`, so that record sorts first and opens a
group at `0001-01-01`, which `closeAt(finishedAt)` closes at the run's finish.
With `quiet: true` the result is a *declared-silent interval covering all of
history up to the end of the run*, and `capture.FindGaps` subtracts it from
every inter-call gap.

**Reproduced through the real CLI** (`/tmp/p4probe/proj3`, a test command that
appends one line and makes two calls):
```
$ cat .retrace/runs/web/checkout/…/groups.jsonl
{"phase":"start","name":"warmup","quiet":true}
$ manifest.json
groups:  [{"name":"warmup","startedAt":"0001-01-01T00:00:00Z",
           "endedAt":"2026-08-22T00:33:00-05:00","quiet":true}]
capture: {"status":"ok","summary":"capture looks complete"}
```
**Consequence proved against the real `capture` package** (standalone program,
two hops ten minutes apart, `DefaultGapThreshold`):
```
gaps with no quiet group:        1  [600s]
gaps with the zero-TS quiet group: 0  []
Assess, no quiet group -> status="suspect"  "1 stretch(es) of 60s+ … longest 600s"
Assess, zero-TS quiet  -> status="ok"       "capture looks complete"
```
A proxy that died for ten minutes mid-run goes from `suspect` to `ok`, and
`ok` is the one verdict `diff`'s quarantine (`Status != VerdictOK`) and
`capture.Fatal` both let through.

**Reachability.** The shipped JS adapter always writes `toISOString()` and the
marker door stamps `now()`, so nothing in the tree emits this today. That is
exactly the defence the global constraints forbid relying on:
`runs.AppendGroupRecord` is **exported** and takes a `GroupRecord` literal —
`runs.AppendGroupRecord(p, runs.GroupRecord{Phase: "start", Name: "warmup",
Quiet: true})` is one omitted field, in Go, in-repo — and `groups.jsonl` is a
documented file-drop protocol (`adapters/js/README.md:47`) that a third-party
runner can write. `ReadGroupRecords` is deliberately fail-open, so a
half-correct record is *worse* than a corrupt one: a corrupt line is skipped,
this one is honoured.

Nothing pins it. I grepped `retrace/runs/groups_test.go` and
`retrace/capture/trust_test.go` for a zero-`TS` case: there is none, in either
direction.

**Shape of the fix (not applied).** Reject or ignore a `GroupRecord` with a
zero `TS` at the write seam (`AppendGroupRecord`) and at the read seam
(`DeriveGroups`), the same way `WriteManifest` rejects an empty
`Capture.Status`. Making the zero value the refusing one here means "a marker
with no timestamp is not a marker", not "a marker at the beginning of time".

---

### F-3 — CONFIRMED — Moderate
**`retrace serve` on a Go-only build serves a blank 200 page; `ensemble` in
the identical situation explains itself, and the README's recovery
instruction is keyed on the sentence retrace's placeholder does not
contain.**

The global constraint says Task 15 "creates the same hazard at
`retrace/serve/ui/dist/index.html` — the `.gitignore` stanza and the `git
restore` habit apply identically." The `.gitignore` half was honoured
(verified: both stanzas present, both placeholders tracked, neither dirty).
The *content* half was not:

- `ensemble/server/ui/dist/index.html`: `<p>UI not built. Run <code>pnpm -r
  build</code> from the repo root, then rebuild the ensemble binary.</p>`
- `retrace/serve/ui/dist/index.html`: `<div id="root"></div>` and nothing
  else — the dev `index.html` with the `<script>` tag stripped.

**Reproduced:** built `retrace` from the current tree with no `pnpm -r build`,
`retrace serve --addr 127.0.0.1:47811`:
```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
… <body><div id="root"></div></body>
$ curl …/api/queue
{"empty":"all-clear","items":[{"app":"web","flow":"checkout","verdict":"pass",…
```
Blank white page, HTTP 200, no console output, no stderr hint (`retrace serve:
listening on http://127.0.0.1:47811` is all the log says) — while the API
underneath is fully healthy. A reviewer reads a blank review queue as "nothing
to review" or "the tool is broken".

**What makes it more than cosmetic:** `README.md:69` documents the recovery as
*"If you ever see 'UI not built' in the dashboard, it means the Go binary was
built without a preceding `pnpm -r build`"*. That string exists only in
`ensemble`'s placeholder. The user of the product that ships the blank page
has no symptom to match against the documented cause. This is the
plausible-value clause exactly: `ensemble`'s placeholder refuses, retrace's
looks like a working app shell that rendered nothing.

Neither `retrace/serve/ui/ui_test.go` nor `ensemble/server/ui/ui_test.go`
asserts anything about placeholder content, so nothing would go red if
ensemble's drifted the same way.

---

### F-4 — PLAUSIBLE — Moderate (answer to question 6)
**F.4 and F.5 became load-bearing after Task 10, and neither register entry
records the constraint their fix must now respect.**

Both were parked as isolated representation nits. Task 10 then built
`diff.incompleteCheck` — the quarantine `--allow-degraded` explicitly cannot
override — entirely on the `-1` sentinel they are about
(`summary.go:250-285`), justified by "Go's `ExitCode()` never returns any
other negative number".

I confirmed the live chain end to end (`/tmp/p4probe/proj4`, a test command
that `kill -TERM $$`s itself):
```
killed run exit=255                       ← F.4's symptom
manifest test: {'command':'./kill.sh','exitCode': -1, 'durationMs': 205}
manifest capture status: failed
$ retrace diff
QUARANTINED: … side b: the test command did not complete (signal-killed, raw
exit code -1) — the recording is truncated, not a comparable run
DIFF EXIT=3
```
So: **the raw `-1` on disk is now the sole input to the strongest gate in the
product**, and three consumers read it — `capture.Assess`'s `test-failed`
(`!= 0`), `diff.incompleteCheck` (`< 0`), and the process exit status (F.4's
255).

- F.4's entry offers "either document 255 as intended … or map it." Mapping
  `-1 → 255` **at the manifest** silently retires `incompleteCheck`: `255 < 0`
  is false, the quarantine stops firing, and a truncated recording becomes a
  comparable run that reports fabricated "gone route"/"missing call" findings.
  F.4's entry does not mention `incompleteCheck` at all.
- F.5's entry wants `*int` or a `test.ran` bool and calls the interaction
  "harmless in Task 10". It is no longer harmless in the direction that
  matters: a `*int` with `omitempty` makes `code < 0` a nil-deref or a
  silently-false comparison at the same site.

**They are one item, not two, and the acceptance bar for either is that
deleting `incompleteCheck` must still turn a test red.** Nothing else in the
tree detects a signal-killed capture. This is the only F-register pair I found
that got *more* dangerous rather than merely more urgent; the rest (F.9/F.11
as one wire decision, F.7 blocked on F.14, F.15/F.16/F.17 in the queue row)
are already recorded as coupled in the ledger and I have nothing to add.

---

### F-5 — CONFIRMED — Minor
**`hostIP(t)`'s `t.Skip` is evaluated in the parent test's range expression,
so one absent interface silently skips the entire loopback-refusal test —
including the two cases that do not need an interface, and the over-refusal
mirror.**

`cmd_replay_test.go:499`:
```go
for _, addr := range []string{"0.0.0.0:" + port, ":" + port, hostIP(t) + ":" + port} {
```
The slice literal is evaluated in the parent goroutine before the loop, so
`hostIP`'s `t.Skip` (`:579`) `Goexit`s the *parent*. Proved with a standalone
reproduction:
```
=== RUN   TestParentRangeExpr
    s_test.go:8: no interface
--- SKIP: TestParentRangeExpr (0.00s)
PASS
```
Not one subtest ran — including a mirror subtest containing a bare `t.Fatal`,
and the run still reported `PASS`. In the real test that means
`TestListenRefusesANonLoopbackAddressBeforeItBinds` loses its `0.0.0.0` case,
its `:port` case, and the "still binds 127.0.0.1/localhost/[::1]" mirror the
test itself calls "not optional" — on a hermetic CI container with no
non-loopback IPv4, the loopback-bind constraint would be entirely unenforced
and the suite green.

Not reachable on GitHub's `ubuntu-latest` (it has a private IPv4), which is
why this is Minor rather than Important. The two-line fix is to resolve
`hostIP` inside its own `t.Run`.

---

### F-6 — CONFIRMED — Minor (comment, not code)
**`NewMarkerDoorCounted`'s doc asserts a guarantee the code does not make,
and `capture.Assess`'s doc says the opposite.**

`markers.go:27-33` says the `onAdmitted` hook fires "never for a request the
guard rejected. A cross-site POST, **a nameless-marker 400**, or a stray port
probe must never count as 'traffic that reached retrace'". The hook is
installed *outside* the mux (`markers.go:80-84`), so it fires before dispatch
— a 400 for an empty name, a 400 for malformed JSON and a 404 for a stray path
all increment the counter.

The code is right and the comment is wrong: `capture.Assess`'s own doc
(`trust.go:126-136`) states plainly that "the mux counts the plan's own
preflight probe, and a 405/404/malformed-body 400", and neutralises it by
construction (`RequestsSeen` can only demote `broken` → `degraded`, never
promote to `ok`, pinned by `TestInflatedRequestsSeenNeverReadsAsClean`). The
defect is that the two docs disagree about the same fact, and the one an
implementer reads at the counting site is the false one — the next person to
key a decision off `RequestsSeen > 0` will believe it means real traffic.
Fix the sentence in `markers.go`, not the hook placement.

---

## The six questions

**1. Was a Global Constraint honoured in one task and quietly dropped in
another?** Yes, twice, and both are above.

- **Zero-Value Constraint.** Held everywhere I probed except **F-2**
  (`GroupRecord.TS`: the zero value is the permissive one, at a fail-open
  reader). The *third* clause — a plausible value is worse than an empty one —
  lapsed in **F-3** (retrace's placeholder is a plausible app shell where
  ensemble's is an honest refusal). **F-1** is the constraint's first clause
  applied one layer too shallowly: the producer correctly refuses to emit a
  reassuring value, and the consumer reads the absence as permissive. Where
  the constraint held, it held well and it held *deliberately*: `Assess`'s
  removed `ProxyConfigured` bool, `capture.Fatal`'s by-construction rejection
  of `""`, `replay.ExitCode`'s default-3, `loopbackAddr`'s empty-host → not
  loopback, `EmptyReasonFor`'s refusal to re-derive "all-clear" from
  `len(rows)`, and `brokenItem`'s `quarantined`-not-`pass`. `Export` even
  turns an empty row set into exit 3 rather than 0. That is a high bar met
  fifteen tasks in a row.
- **Loopback-only bind.** Honoured, and honoured *thoughtfully* rather than
  uniformly: `replay --listen` refuses non-loopback outright before binding;
  `serve --addr` allows a wide bind only when paired with a non-star
  `--allow-host`, refusing every star-shaped spelling on the pair; both
  consult one `loopbackAddr`. The marker door, replay server and review server
  all wrap `httpguard.Handler` and there is **no inlined second copy of any
  part of the guard anywhere in the tree** (grepped). The only weakness is
  F-5, which is about the test, not the bind.
- **Never-committed recording key.** Honoured. `.gitignore`'s `.retrace/*`
  form (not `.retrace/`) is correct and the reason is written down;
  `!.retrace/wire-rules.json` re-includes only the reviewed overlay; the
  reference-bundle root deliberately lives outside `.retrace/` so the blanket
  rule cannot swallow it. `git ls-files` shows no key material.
- **`if: false` npm gate.** Honoured, and consistent with its mirror: the job
  is gated, and all three adapter `package.json`s still carry `"private":
  true`, which is the second, independent lock the release comment claims.
  Verified all three.

**2. Do the wire contracts agree across subsystems?** Yes. I built the
tag→Go-type table across the whole tree and inspected every tag carrying more
than one Go type: all are distinct documents (`a`/`b` as sides, `status` as
HTTP int vs `trace.Verdict` vs plane string, etc.), none is a shared contract
read with two meanings. Four separate vocabularies share the tag `verdict`
(capture `ok|suspect|degraded|broken|failed`, diff `pass|changed|failed|
quarantined`, checkpoint `ok|changed|missing|added|unreadable`, revalidate
`clean|drift|failed`) but they never occupy the same field, and the two that
share the token `failed` are never compared. `core/trace.Hop` is the single
hop schema and retrace defines no parallel record type for captured traffic.

The Go↔TS mirror is **exact**. I compared every `export interface` in
`dashboard/retrace-ui/src/api/types.ts` against the `json:` tags of the Go
struct of the same name across `retrace/diff`, `retrace/runs`, `retrace/serve`,
`retrace/refs`, `retrace/replay` and `core/trace`: zero drift in either
direction, including the two name collisions (`diff.Counts` → `Counts`,
`runs.Counts` → `RunCounts`) and the three response wrappers. `diff.Summary`'s
golden and the TS `Summary` agree field-for-field.

**3. Dead code, derived.** Four exported symbols have no production consumer.
None causes a failure, so none is a finding; listed so they are not
rediscovered:
- `core/trace.NewCtx` — **zero references anywhere, tests included.**
- `core/trace.MergeForDetail` — tests only (its intended consumer, an
  ensemble-ui detail pane, does not call it; two comments cite it).
- `retrace/diff.CollapsedRoutes` — tests only; `hop_test.go:473` notes
  production goes through `DiffHops`, "a different entry point".
- `retrace/runs.ListApps` — tests only; `paths.go:214` says the `…Err`
  variants are what production calls and these "stay for callers that only
  ever" need the plain list. Deliberate.

Checked and **not** dead: `trace.CollapseRelays` (as briefed),
`reportRow.CaptureSides` (called from `report.tmpl.html:54`, invisible to a Go
grep), `diff.CallSimilarity` (a deliberate reference implementation for the
inlined `pairSimilarity`, with `TestPairSimilarityMatchesCallSimilarity`
pinning the equivalence — the premise, not the arm).

**4. Dependency direction.** Clean, and structurally enforced rather than
documented. `core/go.mod` declares **no requires at all** — it cannot reach up
into a product even by accident. `retrace/go.mod` requires `core` and
`gopkg.in/yaml.v3` and **does not require the `ensemble` module**, so
`retrace` importing an `ensemble` internal would not compile. Zero
`caribou-crew/ensemble/{ensemble,retrace}` imports anywhere under `core/`.

**5. Adapters as producers.** The four values the adapters emit into Go are
`groups.jsonl` records, marker-door POST bodies, checkpoint PNG filenames, and
the `.trim` sentinel. Three are safe: the marker door 400s a malformed body
and a nameless marker rather than swallowing them (`markers.go:47-56`, and
`postMarker`/`retrace-maestro` both check `res.ok`, so a rejected marker is a
thrown error rather than a silent no-op); `validateName` reproduces all four
clauses of `runs.ValidateComponents`, not just the regex; and Task 17's F-6
truncation bug is genuinely fixed — `parseArgv` joins all remaining argv, so
the documented whitespace-split invocation and the quoted one produce the same
name and the same validation outcome. The fourth is **F-2**, and the risk is
exactly the shape the constraint predicts: the Go reader is fail-open by
design, so it does not reject the bad value, it *honours* it.

One capability gap, not a defect: `retrace-maestro`'s `group` has no `--quiet`,
so a Maestro flow cannot declare a quiet interval at all. Worth a line in the
README rather than a follow-up.

**6. The follow-up register.** Only **F.4 + F.5** changed status; see F-4
above. The rest are correctly parked and correctly coupled where they interact
(F.9+F.11 as one `trace.Payload` schema decision, F.10 subsumed by F.9, F.7
blocked on F.14's ruling, F.15/F.16/F.17 all in the queue-row shape). F.12
(unauthenticated control plane) deserves one note it does not currently carry:
Task 13 shipped the *legitimate* wide-bind path for `serve`, so F.12's blast
radius is now "unauthenticated review server including the accept verb,
reachable on a build-box network" rather than "loopback only". The bind
guard's refusals are good and the pairing rule is right; F.12 is simply worth
more than it was when it was filed.

---

## What I checked and found sound

Recorded because a negative result is evidence, and because re-checking these
is wasted effort:

- **`revalidate` on an empty bundle.** The obvious "nothing checked reads as
  clean" hole (`Revalidate` returns `VerdictClean` for `Checked == 0`) is
  closed upstream: bundle load refuses a zero-exchange bundle, so `revalidate`,
  `replay` and the CLI all exit 3 with the same sentence. Confirmed by
  `--force`-accepting a zero-call run as a reference and running all three.
- **`ref accept` on a broken capture.** Refuses with exit 3 and names the
  reason; `--force` warns loudly and stamps `capture broken` into the bundle.
  Confirmed.
- **`replay` when the suite calls nothing.** Exits 3, not 0, and
  `renderReplay` refuses to print "every call matched" for a run that compared
  nothing. Confirmed by reading and by the `served == 0` branch.
- **`export` over an empty item set.** Exits 3 through the one `exitCodeFor`
  seam rather than 0. Read.
- **`core/httpguard`.** One body, no inlined partial copies, wildcard held as
  a separate field so `*:8080` cannot degrade to `*`, `Origin: null` rejected,
  `Sec-Fetch-Site` enforced even in wildcard mode, `[::1]` handled.
- **`overlaylock`'s multi-process test.** The obvious `-test.run`-matched-
  nothing trap (a child that never runs exits 0) cannot pass it: the assertion
  is `len(got) == procs*per`, so a child that did nothing produces a shortfall.
- **`skipOrFatal`.** Correct, and CI actually installs the toolchain it
  demands (`ci.yml` sets up pnpm+node in the **go** job, with a comment saying
  why). `pnpm -r --if-present test` is safe here because all six workspace
  packages have a `test` script and all six run `tsc --noEmit` before vitest.
- **`incompleteCheck` / quarantine / `--allow-degraded` layering.** Verified
  live end to end (see F-4's transcript).

---

## Tree state

Nothing in the repo was modified. All probes ran from `/tmp/p4probe`,
`/tmp/p4gaps`, `/tmp/p4skip` against a binary built to `/tmp/retracebin`; no
backup restore was needed because no in-tree mutation was made. Probe
processes killed.

```
$ git status --porcelain
(empty)
```
