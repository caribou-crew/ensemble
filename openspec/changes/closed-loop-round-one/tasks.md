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
- [ ] `gates:` in retrace.yaml: `pixel: { budgetPct: 1.5 }` per flow/checkpoint
      override; `failOn: [pixel, wire, hop, status, perf, spec]` (default all);
      `--no-fail` CLI flag.
- [ ] Summary JSON carries `gates[]` with `{plane, threshold, observed, failed}`.
- [ ] Oracle: a 0.8% pixel change under a 1.5% budget exits 0 and is still
      listed as changed; 2% exits 1; `--no-fail` exits 0 with `failed:true`
      still in JSON.

## 2. Quarantine non-ok captures (Tasks 6, 10)
- [ ] `diff` refuses to compare a run whose `capture.status != ok` unless
      `--allow-degraded`; summary reports `quarantined` with the verdict reason.
- [ ] Manifest gains `wire: { missing: bool, reason }` set from the verdict.
- [ ] Oracle: a proxy-died run diffs to `quarantined`, not to "0 changes".
- Sequence **after** Task 6 closes, not beside it: quarantine is only as good
  as the trust verdict, and at time of writing Task 6's `RequestsSeen()==0`
  rule counted mux-rejected requests (incl. the preflight probe) and Task 4's
  proxy-death watcher was fabricating failures on most healthy runs (fix in
  flight). Quarantining on a lying verdict would quarantine good runs.

## 3. `why` on every tolerance (shape first — Task 3)
- [ ] Optional `why` on mask rects, wireIgnore entries (object form), wireRules,
      expectedStatuses, deviations. Text summary prints `why` beside each
      tolerance that fired.
- [ ] `--require-why` / `gates.requireWhy` turns a missing `why` into a
      config error.

## 4. Preflight + setup/teardown hooks (Task 4)
- [ ] `preflight: [cmd…]` (global + per flow). Run in order before the proxy
      binds; non-zero → exit 2, stderr names the command and its exit code; no
      run dir is left behind.
- [ ] Per-flow `setup:` / `teardown:` run inside the run env (RUN_DIR etc.);
      `teardown` always runs; setup failure is recorded as verdict `failed`.
- [ ] Oracle: a preflight `false` produces no run dir; a failing `setup`
      produces a run dir whose manifest says why.

## 5. Multi-flow runs (Task 4) — `flows.<name>.command` parsed-but-unread is a DEFECT
Lead's ruling (precedent: `Env.Retrace` had no writer): a config key that is
parsed and never read silently lies about what the file does; wire it up,
never delete it.
- [ ] `retrace run --flows a,b` and bare `retrace run` (all configured flows)
      execute `flows.<name>.command` sequentially in one process, one run dir
      each, one summary line each, exit = worst.
- [ ] `--flow x -- <cmd>` keeps working and overrides the configured command.

## 6. Screen-geometry guard (Tasks 1, 7, 17)
- [ ] Adapters write `device.json` `{kind, id?, width, height, scale?}`
      (playwright: viewport; maestro: from the adapter's env; else from the
      first shot). Manifest carries it.
- [ ] Flow `canonical: { width, height, strict: true }` → `run` refuses before
      the test starts when the reported geometry differs; `diff` refuses to
      compare mismatched geometry and reports both sizes.
- [ ] Oracle: a 1206×2622 vs 1178×2556 pair reports `geometry-mismatch`, not
      a percentage.

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
- [ ] Summary lists each wireIgnore / wireRule that suppressed a difference,
      with count.
- [ ] Built-in header rules `date: http-date`, `etag: etag`,
      `content-length: integer`, overridable/disable-able in config.

## 9. Client identity header (core/trace, Task 5)
- [ ] `client_identity_headers:` (default `[x-source-client, x-local-client]`);
      first present value validated `^[a-z0-9][a-z0-9:-]{0,31}$` → `hop.client`.
      Invalid → `client` with a one-time warning, never an error.
- [ ] ensemble-ui traffic view shows `client` on entry hops.

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
- [ ] ensemble.yaml `services.<name>.version:` (command) — default git sha of
      `dir` when it is a repo; `/api/status` returns `{service: version}` +
      `seed: {name, appliedAt}`.
- [ ] retrace copies it into `manifest.stack`; diff reports
      `stack: { changed: [svc…] }` and triage can emit `stack`.

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
