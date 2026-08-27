# Tasks: closed-loop-round-one

Ordered. Each ends in a tested, committed deliverable. Items marked **(shape
first)** should have their config/JSON shape agreed before the named Phase 4
task lands, even if the behaviour ships later. Oracles are artifacts, never a
green exit code.

**Sequencing ruling from the Phase 4 lead (2026-08-20):** Tasks 1-5 were
already committed when this change was written, so "shape first" is free only
for Tasks 6 and 10. The migration-sensitive set is **persisted shapes** —
`retrace.yaml` keys, `.retrace/wire-rules.json`, on-disk `manifest.json`. CLI
flags (`--no-fail`, `--allow-degraded`, `--flows`) and preflight/setup/
teardown *behaviour* carry zero migration cost and can land any time. The
lead is retrofitting the persisted shapes onto completed tasks as ONE
consolidated task with its own review: config keys `gates`, `failOn`, `why`
(mask rects / wireIgnore object form / wireRules / expectedStatuses /
deviations), `preflight`, `setup`, `teardown`, and manifest
`wire: {missing, reason}`. Everything else in items 1-2 lands in Tasks 6/10
via plan amendment.

## 1. Configurable failing gates (shape first — Task 10)
- [x] `gates:` in retrace.yaml: `pixel: { budgetPct: 1.5 }` per flow/checkpoint
      override; `failOn: [pixel, wire, hop, status, perf, spec]` (default all);
      `--no-fail` CLI flag.
- [x] Summary JSON carries `gates[]` with `{plane, threshold, observed, failed}`.
- [x] Oracle: a 0.8% pixel change under a 1.5% budget exits 0 and is still
      listed as changed; 2% exits 1; `--no-fail` exits 0 with `failed:true`
      still in JSON.

      Amended in implementation:

      1. **The YAML is `budget_pct`, not `budgetPct`**, and `fail_on`, not
         `failOn` — snake_case, like every other key in the file. A config
         format with two conventions in it is a config format where half the
         keys are typos.
      2. **`fail_on` takes the four measurable planes** — pixel, wire, hop,
         perf — not the six the brief lists. `status` and `spec` have no
         budget to be a percentage OF: an unexpected status is a violation and
         fails the run outright, and a spec finding is a conformance finding.
         Naming them here would imply a threshold nobody can set.
      3. **The Summary key is `budgets`, not `gates`.** `gates[]` was already
         taken, by the human-readable list of reasons a verdict is "failed",
         and two arrays named the same thing in one document is worse than
         either name being imperfect.
      4. **Per-flow overrides merge PER KEY, not wholesale**
         (`Config.ResolveGates`). Replacing the plane's entry means a flow that
         WIDENS its budget (`gates: {pixel: {budget_pct: 5}}`) silently
         discards the global per-checkpoint budgets underneath it — so
         loosening the flow would TIGHTEN the one screen already declared
         noisy, from 8% down to 5%. A knob must only change what it names.
      5. **A per-checkpoint budget reports the worst OVERAGE, not the worst
         diff.** Those pick different checkpoints: a cart screen allowed 8%
         and sitting at 7% has the largest diff in the run and is entirely
         within budget, while a login screen allowed 0% and sitting at 0.4%
         is the one that fails. Ranking by diff would print a PASSING row over
         a FAILING run — the worst outcome available to a CI gate. The row
         gains `checkpoint` naming which budget decided it, set ONLY for a
         per-checkpoint override, so a project with none produces
         byte-identical JSON.
      6. **`checkpoints:` on wire/hop/perf is a config ERROR**, not an ignored
         key. Those planes have no per-item unit for it to key on, so it would
         otherwise load clean, validate clean, and do nothing — the same
         silent-lie failure mode `validatePlanes` already exists to catch for
         a typo'd plane name. Per-flow gates get the identical two checks: a
         typo caught at the top level and waved through one level down is
         worse than one caught nowhere, because the correctly-spelled plane
         sits right above it in the same file.
      7. **`--no-fail` does not zero a quarantine** — see section 2.

      Not dogfooded in `sample/`, deliberately. The sample stack has no
      genuinely noisy screen, and adding an override to demonstrate the
      feature would model exactly what `retrace-iterate` tells a reader never
      to do: widen a budget to fit a result rather than to describe a screen
      that really does move on its own.

## 2. Quarantine non-ok captures (Tasks 6, 10)
- [x] `diff` refuses to compare a run whose `capture.status != ok` unless
      `--allow-degraded`; summary reports `quarantined` with the verdict reason.
- [x] Manifest gains `wire: { missing: bool, reason }` set from the verdict.
- [x] Oracle: a proxy-died run diffs to `quarantined`, not to "0 changes".

      Amended in implementation: the manifest field is
      `wire: { calls, recorded, reason }`, NOT `{ missing, reason }`. The
      spec'd encoding has its zero value backwards — `Missing:false` asserts
      "recorded and clean", so any code path that forgets to set `wire` claims
      a clean wire plane it never recorded, which is the permissive reading
      the project's zero-value constraint forbids. `Counts{}` is
      `Recorded:false` — "unknown, refuse" for free. `Recorded` is never
      `omitempty`, because a bool that vanishes when false is precisely how
      "absent" and "fine" become the same bytes on disk. `Calls` then carries
      what `missing` could not: `Recorded:true, Calls:0` is "recorded, and
      there were none", a real and clean fact that `missing:false` cannot
      distinguish from a plane nobody wrote.

      Two quarantine paths exist, not one, and only the first is what this
      section asked for: `quarantineCheck` (an untrusted capture verdict,
      which `--allow-degraded` overrides) and `incompleteCheck` (a truncated
      recording from a signal-killed test command, which it deliberately does
      NOT override — the flag lets a human accept a DEGRADED comparison, not
      an INCOMPLETE one; there is no complete data there to accept).
      `TestAllowDegradedDoesNotOverrideASignalKilledTestCommand` pins it.

      `--no-fail` does not zero a quarantine. It suppresses findings, and a
      quarantine is not a finding — nothing was compared. `retrace diff` exits
      3 on a quarantine regardless of the flag, and the usage text says so.
- Sequence **after** Task 6 closes, not beside it: quarantine is only as good
  as the trust verdict, and at time of writing Task 6's `RequestsSeen()==0`
  rule counted mux-rejected requests (incl. the preflight probe) and Task 4's
  proxy-death watcher was fabricating failures on most healthy runs (fix in
  flight). Quarantining on a lying verdict would quarantine good runs.

## 3. `why` on every tolerance (shape first — Task 3)
- [x] Optional `why` on mask rects, wireIgnore entries (object form), wireRules,
      expectedStatuses, deviations. Text summary prints `why` beside each
      tolerance that fired.

      Mask rects, wire_ignore and deviations already had it — deviations
      MANDATORY, for every project, with no opt-in. Added on `rules.Raw`
      (per-Raw: one Raw is one authored decision, however many headers or
      globs it names) and on `expected_statuses`.

      "Beside each tolerance that fired" is built on section 8's
      `suppressions[]`, which already knows exactly which tolerances fired:
      the rows gained `why`, and the text report prints it indented under its
      own row rather than inline, so a sentence cannot break the fixed-width
      table's alignment. The reason resolves in the SAME precedence as the
      matcher it explains — last-write-wins for `wire_rules`, first-match for
      the `wire_ignore` list, rules before the ignore list, the user's rule
      before a built-in — because a row attributed to one rule while quoting
      another's reason describes a rule that does not exist. Each of those
      four orderings has its own mutation.

      An absent why is reported absent: no "no reason given" placeholder, in
      the JSON or the text. `require_why` is opt-in, so unexplained tolerances
      are legal, and a placeholder would make one look documented in every
      consumer that prints the field.

      Built-ins carry their own why, so a `date builtin http-date ×29` row
      explains itself to a reader who never wrote it and cannot edit it.

      NOT covered, and deliberately: masks and expected_statuses accept a
      `why` but do not yet appear in `suppressions[]`, because neither plane
      records whether its tolerance actually silenced anything. An expected
      status is cheap to add (`isExcused` knows which rule excused each hop);
      a fired mask needs a per-rect pre-mask comparison in pixel.Compare.
      Listing either without that evidence would break the report's one
      invariant — that a row means the rule ACTUALLY fired.
- [x] `--require-why` / `gates.require_why` turns a missing `why` into a
      config error.

      Shipped as top-level `require_why: true`, NOT nested under `gates:`.
      `Config.Gates` is `map[string]Gate` keyed by plane, so a `require_why`
      key there decodes into a zero `Gate` and does nothing — parsed-but-unread,
      the defect the lead ruled on for `flows.<name>.command`. `--require-why`
      on both `run` and `diff` turns the check on for one invocation; neither
      flag can turn OFF a `require_why` the config sets, because a flag that
      could would hand a bypass to the person the setting exists to
      inconvenience. Enforced after the overlay merge, so machine-written
      rules from `ref rule` / the review queue are covered too — they are the
      least reviewable tolerances in the product. Every offender is reported,
      not the first. `sample/retrace.yaml` sets it and is guarded by a test.

## 4. Preflight + setup/teardown hooks (Task 4)
- [x] `preflight: [cmd…]` (global + per flow). Run in order before the proxy
      binds; non-zero → exit 2, stderr names the command and its exit code; no
      run dir is left behind.
- [x] Per-flow `setup:` / `teardown:` run inside the run env (RUN_DIR etc.);
      `teardown` always runs; setup failure is recorded as verdict `failed`.
- [x] Oracle: a preflight `false` produces no run dir; a failing `setup`
      produces a run dir whose manifest says why.

      Implemented in `retrace/cmd/retrace/hooks.go`, covered by
      `cmd_run_hooks_test.go`. The ordering the spec implies is load-bearing
      and is pinned: global `preflight` runs before a flow's own, and BOTH run
      before the proxy binds — a precondition check that ran against a stack
      the run had already started is not a precondition. `setup` and
      `teardown` sit OUTSIDE the recording window, so a seed step's own
      traffic is never captured and then diffed as though the app had made
      those calls. `teardown` runs on every exit path including a failed flow,
      which is when leftover state matters most: the next run inherits it.

## 5. Multi-flow runs (Task 4) — `flows.<name>.command` parsed-but-unread is a DEFECT
Lead's ruling (precedent: `Env.Retrace` had no writer): a config key that is
parsed and never read silently lies about what the file does; wire it up,
never delete it.
- [x] `retrace run --flows a,b` and bare `retrace run` (all configured flows)
      execute `flows.<name>.command` sequentially in one process, one run dir
      each, one summary line each, exit = worst.
- [x] `--flow x -- <cmd>` keeps working and overrides the configured command.

      Covered by `cmd_run_multiflow_test.go` (8 tests). "Exit = worst" is
      worst by the four-value exit ladder, not by numeric maximum of anything
      convenient: 0 pass, 1 changed, 2 failed, 3 quarantined — and 3 is the
      HIGHEST deliberately, because "nobody could evaluate this" outranks
      "this failed". A run where one flow failed and another was quarantined
      exits 3 in either order; `cmd_export_test.go` pins both orderings, since
      a fold that took the first non-zero would pass one and fail the other.

## 6. Screen-geometry guard (Tasks 1, 7, 17)
- [x] Adapters write `device.json` `{kind, id?, width, height, scale?}`
      (playwright: viewport; maestro: from the adapter's env; else from the
      first shot). Manifest carries it.
- [x] Flow `canonical: { width, height, strict: true }` → `run` refuses before
      the test starts when the reported geometry differs; `diff` refuses to
      compare mismatched geometry and reports both sizes.
- [x] Oracle: a 1206×2622 vs 1178×2556 pair reports `geometry-mismatch`, not
      a percentage.

Amendments:

- **The canonical check runs after the test, not before it.** A browser
  viewport does not exist until the browser opens, so there is nothing to
  check beforehand. A strict refusal still reports the manifest it recorded
  alongside the gate exit code, so an agent can tell a refusal from a crash.
- **`diff` refuses only a proven mismatch — both sides recorded a screen and
  they differ.** Refusing anything it could not vouch for was written first
  and caught by the suite: every recording predating `device.json` has none,
  and re-recording one side gives it one via the shot fallback, so that
  reading would have refused every comparison against a stored reference the
  moment anyone upgraded. `canonical` keeps the strict reading, because an
  opt-in declaration is a promise to record the screen.
- **A non-strict canonical mismatch writes to stderr only**, with no
  capture-trust note. A note makes the capture verdict `suspect`, and `diff`
  quarantines a suspect side — so non-strict would have refused every
  comparison while strict refuses only the recording, leaving the lenient
  setting strictly harsher than the strict one.
- **The geometry refusal is not `--allow-degraded`-able**, matching
  `incompleteCheck`. That flag accepts a capture you cannot fully trust, not
  a comparison between two things that were never comparable.
- **Maestro writes no `device.json` yet.** It talks to the marker door over
  HTTP and has no run directory, so it needs a new route rather than a file.
  Its runs take the shot fallback, which for a full-screen device recorder is
  the right answer; the wrong-answer case the adapter file exists to fix is
  playwright's selector-scoped checkpoint, where the shot is the size of an
  element.

## 7. Triage classification (Task 10)
- [x] Summary adds `triage: { label, rule }` from a table over
      `{pixel, wire, hop, spec, capture}` moved/same — defaults:
      pixel-only → `client-ui`; wire moved → `client-behavior`; hop-only →
      `stack`; spec fails with all else same → `contract-drift`; capture
      non-ok → `harness`. Overridable under `triage:` in config.

      Amended in implementation, five ways:

      0. **The wire plane is split by SCOPE, and this is the amendment that
         matters.** The brief's "wire moved → `client-behavior`" is wrong for
         the single most common real change there is. Found by running the
         real CLI against a stack whose RESPONSE body changed: it printed
         "TRIAGE: client-behavior — the client is making different calls"
         over a client that had sent the byte-identical request. Every test
         agreed with the code, because they shared its premise.
         `FieldDiff`/`HeaderDiff` already carry `scope: "req" | "resp"`, so
         the five signals are now CAUSES rather than planes: `wire` = the
         client sent something different (call missing/extra/reordered, or a
         changed request body or header); `hop` = the stack answered
         differently (changed status, response body or response header at the
         client edge, OR a moved cross-service chain). The response half
         matters most on a STANDALONE run, which records no hops.jsonl at all
         — without it a changed response has no bit to move and the only
         reading left is the client's fault. Tolerated and ignored
         differences attribute nothing, on either half. An unattributable
         changed entry falls back to `wire`, so a changed run can never report
         `unclassified`: an imprecise answer beats a vacuous one, and the
         client's own diff is the cheaper place to send a reader.

      1. `triage` also carries `signals` — the five booleans the label was
         derived from. Three of them are NOT re-derivable from the rest of
         the Summary without re-implementing `signalsOf` (`hop` folds new
         routes, gone routes and per-service count deviation into one bit;
         `spec` excludes `unchecked` findings; `capture` is true for a
         quarantine whose own capture verdict is `ok`). Shipping the label
         without its evidence would make it a claim no consumer can check,
         and re-derivation across four surfaces is the defect
         `unmeasuredGates` already exists to fix.
      2. The table is ORDERED and its rows are mutually exclusive: the
         first signal that moved, in the order capture → wire → hop → spec
         → pixel, names the label. That resolves every overlap the brief's
         five rows leave open (pixel AND hop moved with wire same, etc.)
         and makes the table total over all 32 vectors, so no run can fall
         through to an empty label. wire above hop because a client making
         different calls CAUSES the chain to differ; spec above pixel
         because with wire and hop unmoved the traffic is identical, so a
         conformance finding means the spec moved.
      3. TWO labels for "no signal moved", not one. `none` is a clean run;
         `unclassified` is a run that failed on something the five signals
         do not cover — a perf budget, an unexpected status, a hopRequire
         route, an unevaluated gate — and points the reader at `gates`.
         Collapsing them would put a reassuring label on the run that most
         needs reading. Perf was deliberately NOT added as a sixth signal:
         its cause is genuinely ambiguous between client and stack, and
         inventing a plane for it would be inventing a cause.
      4. Config rows are consulted BEFORE the built-in table rather than
         replacing it, so a project specialises without restating the
         defaults — a config that replaced the table wholesale would most
         often lose the `harness` row. The one exemption: a `quarantined`
         verdict is always `harness` and the table is not consulted at all,
         because Build returns before a single plane is computed, so the
         four traffic signals are false for want of DATA. A project rule
         matching `wire: same, pixel: same` would otherwise relabel a
         comparison that never happened.

      `retrace.yaml` gains `triage:` — a list of `{name?, label, why?, when}`
      where `when` names any subset of the five signals as `moved`/`same`.
      Validated at Load: a rule that constrains nothing is a config error
      (it matches every run and kills every rule below it), as is a missing
      label or an unknown signal name/value.

## 8. Fired-ignore report + default header rules (Tasks 2, 8)
- [x] Summary lists each wireIgnore / wireRule that suppressed a difference,
      with count.
- [x] Built-in header rules `date: http-date`, `etag: etag`,
      `content-length: integer`, overridable/disable-able in config.

      Shapes as implemented:

      1. `suppressions[]` on Summary — `{plane, target, source, matcher,
         count}`, sorted loudest-first with a total tie-break so two identical
         runs produce byte-identical JSON. `source` is `wire_rule` |
         `wire_ignore` | `builtin`, resolved in `resolveField`'s own
         precedence: a glob in both a rule and the ignore list is credited to
         the rule that actually won, and a user rule that overrides a built-in
         reattributes the row to them — otherwise the report sends a reader to
         the wrong file to change it. Text output prints `SUPPRESSED: N
         difference(s) across M rule(s)` before `VERDICT:`, and prints nothing
         at all when no rule fired.
      2. Only tolerances that ACTUALLY SILENCED something are listed. A rule
         covering a field whose two values already matched suppressed nothing
         and gets no row; the report answers "what did your rules hide", not
         "what rules do you have".
      3. `ignore` on a header previously left no trace anywhere — `DiffHeaders`
         dropped it with a bare `continue`. It now returns a second list, and
         `Entry` gained `headerIgnored[]`. Deliberately a separate array rather
         than a new `type` on `headerDiff`: `classify()` counts every
         non-`tolerated` `headerDiff` as a real change and triage reads the
         same list, so folding them in would have turned each ignored header
         into both a "changed" call and a triage signal — the opposite of what
         `ignore` means.
      4. The disable knob is `default_wire_rules: false` (per config, not per
         header); overriding one header is just naming it in `wire_rules`.
         Built-ins are normalized SEPARATELY from user rules and prepended, so
         a malformed matcher is still reported at the user's own rule index —
         concatenating first made `wireRules[0]` report as `wireRules[3]`.
      5. Built-ins apply even with no config on disk and under
         `--no-config`, and `suppressionsOf` attributes them as `builtin`
         there too: a nil config reporting `wire_rule` would point at a file
         that does not exist.
      6. The tell that these belong in the product rather than in each
         project's config: shipping them let us DELETE the `date` rule
         `sample/retrace.yaml` had to hand-write. Without it, two identical
         runs of the sample suite differed on 100% of paired calls and the
         wire gate failed every time — measured, twice.

## 9. Client identity header (core/trace, Task 5)
- [x] `client_identity_headers:` (default `[x-source-client, x-local-client]`);
      first present value validated `^[a-z0-9][a-z0-9:-]{0,31}$` → `hop.client`.
      Invalid → `client` with a one-time warning, never an error.
- [x] ensemble-ui traffic view shows `client` on entry hops.

      Amended in implementation, four ways:

      1. **FIRST PRESENT wins, not first valid.** A request carrying a
         malformed `x-source-client` and a well-formed `x-local-client`
         records the fallback. Falling through would silently repair a
         misconfigured app: the report looks clean while the value the team
         believes it sends is discarded, and nobody is ever told.
      2. **"A one-time warning" is once per DISTINCT (header, value), capped
         at 32.** Once per process is the literal reading and is worse than it
         sounds — `ensemble up` runs for hours, so the second app's mistake,
         or the same app after a failed fix, gets nothing and the silence
         reads as success. The cap is what keeps that from becoming a log
         flood and an unbounded map when the bad value varies per request (a
         request id in the wrong header, a hostile client). Past it the
         warnings stop; they are diagnostics and no gate depends on them.
      3. **The warning truncates the offending value at 32 bytes and strips
         control characters.** A header this is the wrong place for often
         holds a token, and echoing one in full moves a secret into a log
         file; 32 bytes is the length a VALID identity could have had, so
         nothing legitimate is cut. Control characters are stripped because
         the value is attacker-controlled and the sink is a terminal.
      4. **`""` and `"client"` are kept as different facts.** No header at
         all is the overwhelming majority of traffic; a malformed one is
         someone declaring an identity and getting it wrong. Collapsing them
         would hide a misconfigured app inside the population where it would
         never be found.

      Two things the brief did not name and that turned out to matter:

      - **`client` is NOT `from`, and the config docs now say so at both
         keys.** `source_header` already existed, already used
         `x-source-client` as its example, and already feeds an origin name
         onto the hop. They answer different questions — `from` is a position
         in the service graph and a FALLBACK for missing trace context;
         `client` is which front-end started the request, read
         unconditionally and validated as an identifier so it is safe to
         group by. `TestClientAndFromStayIndependent` pins that a hop can
         carry both.
      - **Startup and hot-reload now share one list**
         (`orchestrator.ApplyProxyGlobals`). Deleting the startup wiring
         compiled clean and no test failed — a config key that would have
         worked after `ensemble reload` and not on a cold start, which from
         the user's side is indistinguishable from a typo.
         `TestReconcileGlobalsCoversEveryProxyGlobal` guards the pair by
         behaviour rather than by reading source.

      Dogfooded with zero config: `sample/clients/web-app` sends
      `x-source-client: web`, which is one of the two default headers, so the
      sample stack shows a `client` badge in the traffic view without an
      `ensemble.yaml` key at all.

## 10. Pluggable hop source (Task 5)
- [ ] `hops.source: ensemble` (default, current behaviour) |
      `{ arm, disarm, export }` commands (disarm stdout = one JSON line
      `{windowId}`; export stdout = hops NDJSON in `core/trace` schema) |
      `{ file: path }`.
- [ ] Hops from any source go through the same Redactor before hitting disk;
      verdict gains `hop-source: <kind>`.
- [ ] Oracle: a fixture export script yields a run whose hops.jsonl is
      byte-equal to the ensemble-session path for the same traffic.

## 11. Cross-repo diff (Tasks 1, 10)
- [ ] `--root` repeatable; selectors `app@runId`, `app@<sha-prefix>`,
      `app@latest`; `retrace runs --root …` lists across roots.

## 12. Stack fingerprint (ensemble/server, Tasks 4/5)
- [x] ensemble.yaml `services.<name>.version:` (command) — default git sha of
      `dir` when it is a repo; `/api/status` returns `{service: version}` +
      `seed: {name, appliedAt}`.
- [x] retrace copies it into `manifest.stack`; diff reports
      `stack: { changed: [svc…] }` and triage can emit `stack`.

Amendments:

- **The git default is the commit plus a digest of anything uncommitted.** A
  bare sha reports two runs carrying two different uncommitted edits as the
  same stack — the false negative this feature exists to prevent, on the
  normal state of a developer's machine.
- **`/api/status` reports the version on each service's own state row**
  (`services[i].version`) rather than as a separate `{service: version}` map.
  Same information, already keyed by name, and it inherits the existing
  shape's absent-vs-empty handling.
- **Only a proven difference is a stack change.** A service fingerprinted on
  one side and not the other reports nothing, and a control plane with
  nothing to report yields no `manifest.stack` at all rather than an empty
  one — an empty stack compares equal to every other run that recorded
  nothing. Same rule as section 6's geometry guard.
- **`stack` is a sixth triage signal, below `capture` and above `wire`**, not
  merely a new way to reach the existing `hop-only` rule. A backend that
  moved can cause any plane to move, wire included, so a lower rank would
  keep reporting `client-behavior` against a redeployed stack.
- **Databases get no fingerprint.** They have no code of their own; what
  varies about a database is its seed, which is recorded separately.

## 13. Run supervision (Task 4)
- [x] `finalized` file written last; `retrace runs` flags dirs without it
      as `abandoned`; `retrace check --url <url>` asks the marker door
      `/identify` and reports owner pid + live run id.
      Amended in implementation: a `running.json` owner record (pid, proxy
      URL, marker URL) is written at run start and cleared by finalize, so
      abandonment is decided by whether the OWNER is still alive; the
      "older than N minutes" bound survives only as the fallback for runs
      that recorded no owner (`--abandoned-after`, default 15m). Flag is
      `--url`, not `--proxy`: `/identify` is on the marker door, and it
      cannot live on the proxy, whose handler forwards every path.

## 14. Agent recipe in-repo (docs)
- [x] `AGENTS.md` at root + `.claude/skills/retrace-iterate/SKILL.md`:
      capture → `diff --json` → read the verdict (never the exit code alone)
      → fix → recapture; lists the NEVERs that are the tool's (no
      `--allow-degraded` to get green, every tolerance needs a `why`).
      Amended: the recipe reads `verdict`/`gates`/`budgets`/`unmeasuredGates`/
      `quarantined`/`capture`. It does NOT name `triage` — that field is
      item 7 and is not implemented, so documenting it would have shipped a
      recipe describing an unshipped proposal. The "whose problem is it"
      step reads the planes directly (pixel-only → client UI; wire moved →
      client behaviour; hop-only → stack; conformance-only → contract
      drift; capture not ok → harness), which is exactly what item 7 will
      later collapse into one field. ~~**When item 7 lands, add `triage` to
      the skill's `<!-- retrace:fields -->` block and fold the plane table
      into it.**~~ **Done** — item 7 shipped and the recipe now reads
      `triage` first, with the plane table replaced by a label table and the
      signal vector documented as the evidence to check it against.
- [x] CI check: the skill's documented flag names appear in `retrace --help`.
      Implemented as `retrace/cmd/retrace/docs_contract_test.go`, so it runs
      in the existing CI job rather than as a separate step. Extended beyond
      the flag check to also assert every `--json` field the recipe names
      exists on `diff.Summary` (by reflection, not a second hand-kept list)
      and that every verdict value is explained. The field half is what
      would have caught the `triage` drift above without reading the source
      by hand. Extended again when item 7 landed: every label the built-in
      triage table can emit must be explained in the recipe, checked against
      `diff.TriageLabels()` — derived from the table, never a second list.

  NOT done, and not one of this item's checkboxes: the end-to-end walkthrough
  against `sample/`. There is no `retrace.yaml` anywhere in the repo yet, and
  `sample/clients/web-app`'s Playwright suite does not use the retrace
  adapter. That is its own piece of work — see the note in the ledger.
