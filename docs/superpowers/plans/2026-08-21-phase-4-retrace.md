# Phase 4: retrace — Capture / Replay / Diff / Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

## Scope ruling: Phase 4 is split in two. This plan is part 1 (boxes 4.1–4.7).

**Recommendation: split box 4.8 out into its own plan.** Reasons, in order of
weight:

1. **4.8 mutates a shared, already-shipped contract.** It rewrites
   `core/trace.Redactor` (used today by `core/proxy.Recorder` on every hop
   ensemble captures) into per-key modes, and adds `$enc:v1:` /
   `red-<hash8>` markers that the *already-built* Phase 3 traffic UI must
   learn to render and reveal. That is a change to ensemble's live path
   dressed as a retrace feature; it deserves its own RED-first regression
   suite and its own review, not a 16th task at the bottom of a capture
   plan.
2. **It is the only cryptographic surface in the product.** AES-256-GCM,
   envelope-wrapped data keys, HMAC-keyed destroy placeholders, and
   `retrace rekey` rotation are one coherent security review. Burying them
   behind fifteen unrelated tasks means the reviewer who reaches them is
   out of budget.
3. **It depends on part 1 existing.** "Replay decrypts at serve time" has no
   meaning until the replay server (Task 12 here) exists; "reveal-eyeball
   and add-rule in the review UI" has no meaning until the review UI
   (Task 15 here) exists. Sequencing it after part 1 is not a deferral, it
   is the only order in which it can be tested end to end.

**What part 2
(`docs/superpowers/plans/<date>-phase-4b-retrace-encryption-and-a11y.md`)
must cover — do not drop it.** Part 2 is box 4.8 (encryption) **plus the
a11y-tree diff**, which is a spec SHALL that part 1 does not implement and
which would otherwise have no plan anywhere:

- `core/trace`: per-key redaction modes `display` / `encrypt` / `destroy`,
  defaulting auth-bearing headers (authorization, cookie, set-cookie, dpop)
  to `destroy` and user-listed body fields to `encrypt`; the shared
  `redaction:` config block read by BOTH `ensemble.yaml` and `retrace.yaml`.
- Field-level AES-256-GCM at capture, stored as `$enc:v1:<base64(nonce||ct)>`;
  deterministic `red-<hash8>` destroy placeholders (HMAC keyed with a
  per-recording key that is generated, used, and discarded) so the same
  original value maps to the same placeholder within one recording and
  replay pairing still works.
- Envelope key wrapping: a random per-recording data key wrapped by the team
  key; team key sourced **only** from `RETRACE_RECORDING_KEY` or the
  gitignored `.retrace/recording.key` (both already honored by
  `.gitignore`) — never a committed file, never in config.
- `retrace rekey`: re-wrap data keys under a new team key without
  re-recording.
- Replay-time decryption (Task 12's `replay.Server` grows a decrypting body
  writer); key-missing must fail loudly with a named error, never serve a
  marker as if it were a value.
- `recordings: encrypt-all` opt-in whole-body mode.
- Reveal-eyeball + "add redaction rule" actions in the ensemble traffic UI
  (ties into shipped 3.3) and the retrace review UI (Task 15 here). Task 15
  in THIS plan ships the masked rendering and the `.redacted` styling hook
  but no reveal.
- **Non-retroactivity, stated as a testable guarantee.** Changing a
  redaction mode affects **future captures only**; it never re-processes,
  re-encrypts, or re-reveals a recording already on disk. A `destroy` is
  **irreversible** — the original never existed in any artifact, so no key,
  no rekey, and no mode change can recover it. Part 2 must carry a
  RED-first test for each half (`TestChangingAModeDoesNotRewriteExistingRuns`,
  `TestRekeyCannotRecoverADestroyedValue`). This is precisely the clause
  that gets implemented backwards when it is left unwritten: the obvious
  reading of "switch this field to display" is "and show me the ones I
  already recorded."
- **A11y-tree diff** (`retrace-diff-review` SHALL, flagged experimental
  until device-verified). Part 1 explicitly does not port it — see
  "Explicitly NOT ported" in the flowlens inventory below — and the
  wrap-up records that deferral against the openspec change. Part 2 owns
  it: capture the a11y tree alongside each checkpoint shot, diff it as a
  tree (not as text), and surface it as a fourth section beside
  pixels/wire/hops. It is listed here so the SHALL has a home; it is
  independent of the encryption work and may be sequenced either side of
  it.

Everything below is part 1.

**Goal:** Ship `retrace` — record a flow through the stack, replay it as
strict mocks in CI, diff two runs on pixels/wire/hops, and review the
differences with three verbs — as a single static Go binary plus three thin
npm adapters.

**Architecture:** `retrace run` mints a runId, registers a session with a live
`ensemble` (or opens its own client-edge listener from the same
`core/proxy`), and writes a per-runId directory of `trace.Hop` NDJSON plus
screenshots and flow-part markers. Diff engines are pure Go functions over
`trace.Hop` and PNG bytes. `retrace serve` exposes one review queue as REST
behind the shared Origin/Host guard, with an embedded React app as just
another client of it. Every recorded artifact uses the existing
`ensemble/1` hop schema — retrace adds no second wire format.

**Tech Stack:** Go 1.25 stdlib (`net/http`, `image/png`, `embed`,
`crypto/rand`) + `gopkg.in/yaml.v3` for `retrace.yaml`; React 19 / Vite 8 /
TypeScript 7 / vitest 4 / happy-dom for `dashboard/retrace-ui`; the existing
`@ensemble/design-system` (exports exactly `Badge`, `Tabs`, `Spinner`).
Adapters are plain TS with zero runtime dependencies.

**Spec:**
- `openspec/changes/init-ensemble-retrace/specs/retrace-capture-replay/spec.md`
- `openspec/changes/init-ensemble-retrace/specs/retrace-diff-review/spec.md`
- `openspec/changes/init-ensemble-retrace/specs/adapters/spec.md`
- `openspec/changes/init-ensemble-retrace/specs/core-trace-model/spec.md`
- `openspec/changes/init-ensemble-retrace/design.md` §6 and §7
- Roadmap boxes 4.1–4.7 in `openspec/changes/init-ensemble-retrace/tasks.md`

**Where this plan exceeds the spec.** Three additions are not in any spec.
Each is argued at its task and each must be reported to the spec owner as an
additive extension; none of them narrows or reinterprets a spec'd behaviour:

1. **`RETRACE_MARKER_URL`** (Task 4) — a third handshake variable beside the
   spec'd `RETRACE_RUN_DIR` / `RETRACE_PROXY_URL`, because Maestro cannot
   write files from a flow. The two spec'd variables remain authoritative
   and sufficient for the file-writing adapters.
2. **`RETRACE_STRICT`** (Task 17) — an additive env var that makes adapters
   fail loudly when the handshake env is absent. The spec says adapters
   "SHALL fail loudly if invoked without them when strict mode is on" but
   never names the switch that turns strict mode on; this is that switch.
   Unset, checkpoints stay no-ops outside a run, which is the spec'd
   default behaviour.
3. **The `core/httpguard` extraction** (Task 4) — moving
   `ensemble/server/guard.go` into `core` so retrace's marker door and
   review server sit behind the *same* guard rather than a copy. No spec
   asks for the refactor; the alternative is three drifting copies of a
   security check, which is worse. It is a pure move plus a rename: the
   ensemble behaviour must not change, and Task 4 proves that by keeping
   ensemble's existing guard tests green unmodified.

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
- **Never assert a CLI exit code through `go run`.** `go run` treats a
  non-zero child as its own failure: it prints `exit status N` to stderr and
  itself exits **1**. Measured, not assumed. This plan defines a 0/1/2/3 CI
  contract (Task 10), so an assertion written against `go run` checks the
  wrong number in every case that matters — it passes only for 1. Build a
  binary and run that, or use `exec.Command` + `exec.ExitError.ExitCode()`
  inside a Go test.

---

## Prior art: flowlens porting inventory

The old prototype at `/Users/steven/dev/oss/flowlens` is a week-old Node
implementation being rewritten in Go, not ported line-for-line. Its
**algorithms, fixtures and scenarios** port; its CLI, board UI and bless flow
do not. Same format as `docs/phase-3-porting-inventory.md`.

### Port as goldens (pure, tested, cited per task)

| flowlens file | lines | ports to | task |
|---|---|---|---|
| `src/matchers.mjs` | 70 | `retrace/rules/matchers.go` — NAMED_MATCHERS regexes verbatim (uuid/etag/integer/semver/iso8601/http-date), `classifyDifference` → `Classify` | 2 |
| `src/wire-rules.mjs` | 91 | `retrace/rules/rules.go` — `matchesPathGlob` (`/`-segmented, `*`/`**`), `matchesFieldGlob` (`.`-segmented), `resolveRules` last-write-wins, `matcherForField` last-glob-wins | 2 |
| `test/wire-rules.test.mjs` | 18 cases | `retrace/rules/rules_test.go` — port ALL 18 case names, listed in Task 2 | 2 |
| `src/pixel-diff.mjs` | 216 | `retrace/diff/pixel/pixel.go` — masks, gate+fine thresholds, pad/crop on size mismatch, overlap metrics, dilate+density overlay | 7 |
| `src/pixel-trim.mjs` | 43 | `retrace/diff/pixel/trim.go` — `trimUniformBorder`, incl. the 1px-sliver refusal | 7 |
| `test/fixtures/generate-fixtures.mjs` + `{identical,diff,mask}.{a,b}.png` | — | `retrace/diff/pixel/testdata/*.png` — regenerate byte-identically from the same 40×40 recipe (Task 7 Step 1) | 7 |
| `test/pixel-diff.test.mjs` | 9 cases | `retrace/diff/pixel/pixel_test.go` — all 9, named in Task 7 | 7 |
| `src/wire-diff.mjs` | 451 | `retrace/diff/wire.go` — Needleman-Wunsch `alignBySimilarity`, `callSimilarity` (0.3 status / 0.5 resp / 0.2 req), `walk`, `diffArrays` multiset-reorder, `blankTolerated` | 8 |
| `src/wire-order.mjs` | 101 | `retrace/diff/order.go` — `lisIndices` (patience sort), `annotate`, `buildSections` | 8 |
| `src/hop-diff.mjs` | 115 | `retrace/diff/hop.go` — service counts w/ 0.5 tolerance, error signatures, routes appeared/vanished, `requiredRouteFailures` | 9 |
| `src/unexpected-status.mjs` | 21 | `retrace/diff/status.go` — `matchUrlGlob` (query stripped before matching), `findUnexpectedStatuses` | 9 |
| `src/perf-check.mjs` | 37 | `retrace/diff/perf.go` — sum-not-median total, max×margin budget derivation | 9 |
| `src/capture-health.mjs` | 201 | `retrace/capture/trust.go` — the RANK table, `findGaps` w/ quiet-interval subtraction, every reason code | 6 |
| `src/groups.mjs` | 77 | `retrace/runs/groups.go` — stateless writer, `deriveGroups`, half-open `groupAt` | 1 |
| `src/manifest.mjs` | 175 | `retrace/runs/manifest.go` + `paths.go` — run-dir layout, `listRuns/Flows/Apps` | 1 |
| `src/reference.mjs` + `src/ref-save.mjs` | 87+101 | `retrace/refs/refs.go` — bundle resolution w/ eligibility history, mask-on-promote | 11 |
| `src/deviations.mjs` | 49 | `retrace/diff/deviations.go` — proposed/approved ledger (lives in `diff`, not `refs`, to keep the dependency one-way — see Task 11) | 11 |
| `src/contract-match.mjs` `matchRequest` | 184 | `retrace/replay/match.go` — method+path → query → body-subset, nearest-miss diff | 12 |
| `src/playwright-fixture.mjs` | 31 | `adapters/playwright/src/fixture.ts` | 16 |

### Explicitly NOT ported

- `src/board.mjs`, `src/board-serve.mjs`, `src/board-ui.html`,
  `src/bless-queue.mjs`, `src/ref-bless.mjs` — the board + bless-token flow
  is **replaced** by the review queue (design §6.4). Read them for the
  failure modes they document, port nothing.
- `src/metro-*.mjs`, `src/device.mjs`, `src/sdk-stamp.mjs`,
  `src/optimize-mine.mjs`, `src/bug-mine.mjs`, `src/corpus-query.mjs`,
  `src/parity-board.mjs`, `src/checkpoint-map.mjs` — React-Native-shop and
  corpus-mining specifics outside this product's scope.
- `src/a11y-diff.mjs` — spec keeps a11y-tree diff "flagged experimental
  until device-verified". Deferred out of part 1; not in any task here.
  **It is not dropped: part 2 owns it** (see the part-2 enumeration in the
  scope ruling above). Note the deferral and its part-2 home in the
  openspec change when part 1 lands.
- `src/redact.mjs` — superseded by the shipped `core/trace.Redactor`.

### Architecture facts worth knowing before porting

- flowlens `wire.jsonl` records are `{seq, ts, method, path, query, status,
  durationMs, reqHeaders, reqBody, respHeaders, respBody}`. The Go
  equivalent is `trace.Hop`: `ts`→`T.Start`, `durationMs`→`T.DoneMs`,
  `reqBody`→`Req.Body` (a **string**, so every diff engine must
  `json.Unmarshal` it itself), `path`+`query`→`Path` (already
  `RequestURI()`, query included).
- flowlens bodies were parsed objects; `trace.Payload.Body` is raw text and
  may be `Truncated`. A truncated body must never be diffed field-wise —
  report `truncated` and skip (Task 8, Step 6). **Clarified after Task 8's
  review: truncation gates PER PAYLOAD, not per entry.** An `Entry` has four
  payloads (request and response, on each side); a truncation flag suppresses
  field-diffing of *that body only*, and the other three are diffed normally.
  `Entry.Truncated` reports that at least one was truncated. The earlier text
  was ambiguous between the two readings — Step 6's `parseBody(p
  trace.Payload)` signature says per-payload, Step 5's test description read
  as per-entry — and the implementation took the per-entry reading, so a
  truncated REQUEST body silently suppressed the RESPONSE diff and the entry
  reported `identical`. A false "ok" produced by ambiguity in this plan, not
  by the implementer.
- flowlens had zero TODO/FIXME and comments that explain *why*. Carry the
  explanatory comments across when porting; they are the design record.

---

## File Structure

```
core/
  httpguard/guard.go          # T4  the Origin/Host + Sec-Fetch-Site guard, moved out of
  httpguard/guard_test.go     #     ensemble/server so both products share ONE copy. Extracted
                              #     in T4 because T4's marker door is its first consumer;
                              #     T12's replay server and T13's review server also wrap it.
retrace/
  cmd/retrace/
    main.go                   # T1  dispatch + usage (mirrors ensemble/cmd/ensemble/main.go)
    output.go                 # T1  text/json writers, exit-code constants
    cmd_run.go                # T4/T5
    cmd_diff.go               # T10
    cmd_ref.go                # T11 (accept/reject/rule/list)
    cmd_replay.go             # T12
    cmd_revalidate.go         # T12
    cmd_serve.go              # T13
    cmd_export.go             # T16
    client.go                 # T5  thin REST client for ensemble's /api
  runs/
    paths.go paths_test.go            # T1 run-dir layout, listing
    manifest.go manifest_test.go      # T1 manifest schema retrace/1
    groups.go groups_test.go          # T1 flow-part markers
  config/
    config.go config_test.go          # T3 retrace.yaml + .retrace/wire-rules.json overlay
  rules/
    matchers.go matchers_test.go      # T2
    rules.go rules_test.go            # T2
  capture/
    capture.go capture_test.go        # T4 standalone client-edge capture
    markers.go markers_test.go        # T4 loopback marker door
    ensemble.go ensemble_test.go      # T5 session-attached capture
    trust.go trust_test.go            # T6 capture-trust verdict
  diff/
    pixel/pixel.go pixel_test.go      # T7 pixelmatch port
    pixel/trim.go trim_test.go        # T7
    pixel/testdata/*.png              # T7 golden images
    wire.go wire_test.go              # T8 pairing + field diff
    order.go order_test.go            # T8 LIS reorder + sections
    hop.go hop_test.go                # T9
    status.go status_test.go          # T9
    perf.go perf_test.go              # T9
    openapi.go openapi_test.go        # T9
    summary.go summary_test.go        # T10 Summary, Build, OptionsFor, ExitCode
    deviations.go                     # T8  Deviation/ToleratedNote TYPES (diff's own structs
                                      #     reference them, so they must compile with T8)
                                      # T11 appends the ledger: Load/Resolve/Find
    deviations_test.go                # T11
  refs/
    refs.go refs_test.go              # T11 bundles, resolve, accept
  replay/
    bundle.go match.go server.go      # T12
    revalidate.go                     # T12
    *_test.go
  serve/
    queue.go queue_test.go            # T13 worst-first review queue
    routes.go routes_test.go          # T13 REST verbs
    server.go                         # T13
    export.go export_test.go          # T16 static report
    report.tmpl.html                  # T16 the go:embed target (must exist to compile)
    ui/ui.go ui_test.go               # T15 go:embed of retrace-ui
    ui/dist/index.html                # T15 committed placeholder ONLY
dashboard/design-system/
  useAsync.ts useAsync.test.ts          # T14 the shared async-load hook (both UIs)
dashboard/retrace-ui/                  # T15 review app (Vite → retrace/serve/ui/dist)
adapters/
  js/                                 # T17 @caribou-crew/retrace-js
  playwright/                         # T17 @caribou-crew/retrace-playwright
  maestro/                            # T17 @caribou-crew/retrace-maestro
```

Rationale for the boundaries: `runs` is pure filesystem layout with no
knowledge of proxies, so capture, diff, refs and serve can all depend on it
without cycles. `rules` is pure and depends on nothing, so both `diff` and
`replay` use one matcher implementation. **`runs` owns the capture-trust
*types* while `capture` owns the *assessor*** — the manifest must carry a
verdict, and the assessor must read flow-part groups, so putting both in one
package would be an import cycle. For the same reason the deviations ledger
lives in `diff` rather than `refs`: `refs.Reject` needs a `diff.Summary`, so
`refs → diff` is the only direction that can exist.

---

### Task 1: Run-dir model — paths, manifest, flow-part groups, CLI skeleton

**Files:**
- Create: `retrace/runs/paths.go`, `retrace/runs/paths_test.go`
- Create: `retrace/runs/manifest.go`, `retrace/runs/manifest_test.go`
- Create: `retrace/runs/groups.go`, `retrace/runs/groups_test.go`
- Create: `retrace/cmd/retrace/output.go`
- Modify: `retrace/cmd/retrace/main.go` (replace the 8-line stub)

**Interfaces:**
- Consumes: `github.com/caribou-crew/ensemble/core/trace` — `trace.Verdict`,
  `trace.VerdictOK`, `(trace.Verdict).Worse(trace.Verdict) trace.Verdict`.
- Produces (used by Tasks 4, 5, 6, 10, 11, 12, 13, 16):
  ```go
  package runs
  const Schema  = "retrace/1"
  const RefRunID = "reference"
  func RunsRoot(cwd string) string                       // <cwd>/.retrace/runs
  func RefsRoot(cwd string) string                       // <cwd>/.retrace-ref
  func NewRunID(now time.Time, sha string) string        // 20260821T101500Z-ab12cd3
  func PathsFor(root, app, flow, runID string) (Paths, error)  // validates each component
  func Create(root, app, flow, runID string) (Paths, error)
  func ListApps(root string) []string
  func ListFlows(root, app string) []string
  func ListRuns(root, app, flow string) []string         // lexical order == chronological
  func FindRun(root, app, flow, selector string) string  // "latest" | runId | short sha; "" = none
  func WriteManifest(p Paths, m *Manifest) error         // stamps Schema on the caller's copy; errors on empty Capture.Status
  func ReadManifest(path string) (Manifest, error)
  func ReadHops(path string) (hops []trace.Hop, skipped int, err error)  // missing file → nil,0,nil; corrupt lines skipped and counted
  func AppendGroupRecord(p Paths, r GroupRecord) error

**Five signatures moved across Task 1's fix rounds (commits
`b2366e9..35f7555`); the committed code is authoritative and this block
matches it.** `PathsFor` returns an error because it now validates
`app`/`flow`/`runID` — the original joined caller-supplied names straight
into `filepath.Join`, which *resolves* `..` rather than rejecting it, so
`Create(root, "web", "checkout", "../../../../escaped")` returned a nil
error while making a directory outside the runs root, invisible to every
listing function. Validating once here is deliberate: Tasks 4, 12 and 13
all route request-derived values into these functions. `WriteManifest`
takes a pointer so the schema stamp and defaults land on the caller's
copy, and it now REJECTS an empty `Capture.Status` (an empty verdict ranks
equal to `ok`, so an unassessed run would have gated as clean). `ReadHops`
returns a skipped count so a partially corrupt hop log degrades visibly
instead of either sinking a whole diff or vanishing silently.
`AppendGroupRecord` and `ReadGroupRecords` moved from a bare `runDir
string` to a `p Paths`: the precondition that a run directory came from
`PathsFor`/`Create` — and was therefore validated — used to live only in a
doc comment, so a Task 4 implementer wiring a request field or
`RETRACE_RUN_DIR` straight into a marker write got no guard from the
signature itself; now the precondition is structural, and writing
something that skips it looks visibly wrong. Call sites throughout this
plan are written against these shapes.
  func ReadGroupRecords(p Paths) ([]GroupRecord, error)
  func DeriveGroups(records []GroupRecord, finishedAt time.Time) []Group
  func GroupAt(groups []Group, ts time.Time) string
  func GroupNames(groups []Group) []string
  ```
- Produces (CLI, used by every later cmd_*.go):
  ```go
  package main
  const (
      exitOK      = 0 // no differences, nothing to review
      exitDiff    = 1 // differences found — review required
      exitGate    = 2 // hard gate failed (rule violation, hopRequire, >=400, perf, capture)
      exitUsage   = 3 // bad flags, unreadable config, I/O failure
  )
  func writeJSON(w io.Writer, v any) error
  func fail(stderr io.Writer, format string, args ...any) int // prints "retrace: ..." → exitUsage
  ```

- [ ] **Step 1: Write the failing paths + listing test**

`retrace/runs/paths_test.go`:
```go
package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewRunIDIsLexicallyChronological(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 10, 15, 0, 0, time.UTC)
	a := NewRunID(t0, "ab12cd3ef")
	b := NewRunID(t0.Add(time.Second), "ab12cd3ef")
	if a != "20260821T101500Z-ab12cd3" {
		t.Fatalf("run id = %q, want 20260821T101500Z-ab12cd3", a)
	}
	if !(a < b) {
		t.Fatalf("run ids must sort chronologically: %q !< %q", a, b)
	}
}

func TestNewRunIDWithoutShaStillUnique(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 10, 15, 0, 0, time.UTC)
	if got := NewRunID(t0, ""); got != "20260821T101500Z-nogit" {
		t.Fatalf("run id = %q, want 20260821T101500Z-nogit", got)
	}
}

func TestCreateMakesShotsDirAndListingsRoundTrip(t *testing.T) {
	root := RunsRoot(t.TempDir())
	p, err := Create(root, "web", "checkout", "20260821T101500Z-ab12cd3")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if st, err := os.Stat(p.ShotsDir); err != nil || !st.IsDir() {
		t.Fatalf("shots dir not created: %v", err)
	}
	if p.WirePath != filepath.Join(p.RunDir, "wire.jsonl") {
		t.Fatalf("WirePath = %q", p.WirePath)
	}
	if got := ListApps(root); len(got) != 1 || got[0] != "web" {
		t.Fatalf("ListApps = %v", got)
	}
	if got := ListFlows(root, "web"); len(got) != 1 || got[0] != "checkout" {
		t.Fatalf("ListFlows = %v", got)
	}
	if got := ListRuns(root, "web", "checkout"); len(got) != 1 {
		t.Fatalf("ListRuns = %v", got)
	}
}

func TestFindRunSelectors(t *testing.T) {
	root := RunsRoot(t.TempDir())
	for _, id := range []string{"20260821T100000Z-aaa1111", "20260821T110000Z-bbb2222"} {
		if _, err := Create(root, "web", "checkout", id); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if got := FindRun(root, "web", "checkout", "latest"); got != "20260821T110000Z-bbb2222" {
		t.Fatalf("latest = %q", got)
	}
	if got := FindRun(root, "web", "checkout", "aaa1111"); got != "20260821T100000Z-aaa1111" {
		t.Fatalf("by sha = %q", got)
	}
	if got := FindRun(root, "web", "checkout", "nope"); got != "" {
		t.Fatalf("unknown selector = %q, want empty", got)
	}
}

func TestListingsOfMissingRootAreEmptyNotPanic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-created")
	if got := ListApps(root); len(got) != 0 {
		t.Fatalf("ListApps = %v", got)
	}
	if got := ListRuns(root, "web", "checkout"); len(got) != 0 {
		t.Fatalf("ListRuns = %v", got)
	}
}
```

- [ ] **Step 2: Run it — expect FAIL**

Run: `go test ./retrace/runs/ -run TestNewRunID -v`
Expected: FAIL, `undefined: NewRunID` (package has no non-test files yet).

- [ ] **Step 3: Implement `retrace/runs/paths.go`**

```go
// Package runs owns the on-disk shape of a retrace recording: where a run
// directory lives, what its manifest says, and how flow-part markers are
// written and folded into intervals. It knows nothing about proxies or
// diffing, so capture, diff, refs and serve can all depend on it.
package runs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Schema versions the manifest, independently of core/trace's hop schema:
// a manifest gains fields far more often than a hop record does.
const Schema = "retrace/1"

// RefRunID is the fixed run-id level inside a reference bundle. Literal,
// never the source runId: a churning directory name makes git show each
// promotion as a delete + add instead of a screenshot modification.
const RefRunID = "reference"

func RunsRoot(cwd string) string { return filepath.Join(cwd, ".retrace", "runs") }
func RefsRoot(cwd string) string { return filepath.Join(cwd, ".retrace-ref") }

// NewRunID is timestamp-first so lexical directory order is chronological
// order — every listing in this package relies on that and never stats.
func NewRunID(now time.Time, sha string) string {
	stamp := now.UTC().Format("20060102T150405Z")
	short := "nogit"
	if len(sha) >= 7 {
		short = sha[:7]
	} else if sha != "" {
		short = sha
	}
	return stamp + "-" + short
}

// Paths is every file a run directory can hold. Members are absolute once
// PathsFor is given an absolute root; existence is never implied.
type Paths struct {
	RunDir       string
	ManifestPath string
	ShotsDir     string
	WirePath     string // client-edge hops, NDJSON of trace.Hop
	HopsPath     string // full provider chain, NDJSON of trace.Hop
	GroupsPath   string
	MissesPath   string // replay only
}

// PathsFor computes the paths a run directory would have, without
// touching disk. It validates app/flow/runID (see validateComponents) so
// every caller — Create, and every later task that resolves an existing
// run from a selector — gets the same traversal guard from one place.
func PathsFor(root, app, flow, runID string) (Paths, error) {
	if err := validateComponents(app, flow, runID); err != nil {
		return Paths{}, err
	}
	dir := filepath.Join(root, app, flow, runID)
	return Paths{
		RunDir:       dir,
		ManifestPath: filepath.Join(dir, "manifest.json"),
		ShotsDir:     filepath.Join(dir, "shots"),
		WirePath:     filepath.Join(dir, "wire.jsonl"),
		HopsPath:     filepath.Join(dir, "hops.jsonl"),
		GroupsPath:   filepath.Join(dir, "groups.jsonl"),
		MissesPath:   filepath.Join(dir, "misses.jsonl"),
	}, nil
}

// Create makes a fresh run directory and its shots subdirectory. It fails
// if the run directory already exists — two runs must never silently share
// one directory.
func Create(root, app, flow, runID string) (Paths, error) {
	p, err := PathsFor(root, app, flow, runID)
	if err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p.RunDir), 0o755); err != nil {
		return Paths{}, err
	}
	if err := os.Mkdir(p.RunDir, 0o755); err != nil {
		return Paths{}, err
	}
	if err := os.Mkdir(p.ShotsDir, 0o755); err != nil {
		return Paths{}, err
	}
	return p, nil
}

func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // a root that was never written is empty, not an error
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func ListApps(root string) []string            { return dirNames(root) }
func ListFlows(root, app string) []string      { return dirNames(filepath.Join(root, app)) }
func ListRuns(root, app, flow string) []string { return dirNames(filepath.Join(root, app, flow)) }

// FindRun resolves a user-facing selector: "latest", an exact run id, or a
// git sha (full or short) whose run id ends in its 7-char prefix. Returns
// "" when nothing matches — callers report that, never guess.
func FindRun(root, app, flow, selector string) string {
	ids := ListRuns(root, app, flow)
	if len(ids) == 0 {
		return ""
	}
	if selector == "" || selector == "latest" {
		return ids[len(ids)-1]
	}
	for _, id := range ids {
		if id == selector {
			return id
		}
	}
	short := selector
	if len(short) > 7 {
		short = short[:7]
	}
	for i := len(ids) - 1; i >= 0; i-- {
		if strings.HasSuffix(ids[i], "-"+short) {
			return ids[i]
		}
	}
	return ""
}
```

- [ ] **Step 4: Run the paths tests — expect PASS**

Run: `go test ./retrace/runs/ -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Write the failing manifest test**

`retrace/runs/manifest_test.go`:
```go
package runs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestManifestRoundTripsAndStampsSchema(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := Manifest{
		App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble,
		StartedAt: time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		Checkpoints: []Checkpoint{{Name: "cart", File: "shots/cart.png", Width: 390, Height: 844}},
		Capture: CaptureTrust{Status: trace.VerdictDegraded, Summary: "propagation gap at bff"},
	}
	if err := WriteManifest(p, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Schema != Schema {
		t.Fatalf("Schema = %q, want %q", got.Schema, Schema)
	}
	if got.Capture.Status != trace.VerdictDegraded || len(got.Checkpoints) != 1 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	raw, _ := os.ReadFile(p.ManifestPath)
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("manifest must end with a newline (it is committed in reference bundles)")
	}
	var any map[string]json.RawMessage
	if err := json.Unmarshal(raw, &any); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if _, ok := any["capture"]; !ok {
		t.Fatal(`capture must always be present — "unknown trust" and "not written yet" must not look alike`)
	}
}

func TestReadHopsSkipsBlankLinesAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/hops.jsonl"
	body := "{\"schema\":\"ensemble/1\",\"seq\":1,\"to\":\"bff\"}\n\n{\"schema\":\"ensemble/1\",\"seq\":2,\"to\":\"cart\"}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hops, skipped, err := ReadHops(path)
	if err != nil || skipped != 0 {
		t.Fatalf("ReadHops: %v (skipped %d)", err, skipped)
	}
	if len(hops) != 2 || hops[1].To != "cart" {
		t.Fatalf("hops = %+v", hops)
	}
	missing, _, err := ReadHops(dir + "/nope.jsonl")
	if err != nil || missing != nil {
		t.Fatalf("missing file must be (nil, nil), got (%v, %v)", missing, err)
	}
}
```

- [ ] **Step 6: Run it — expect FAIL**

Run: `go test ./retrace/runs/ -run TestManifest -v`
Expected: FAIL, `undefined: Manifest`.

- [ ] **Step 7: Implement `retrace/runs/manifest.go`**

```go
package runs

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// Capture modes. A reader must be able to tell a reduced client-edge-only
// capture from a full-chain one WITHOUT inferring it from an empty
// hops.jsonl — an empty chain and an unrecorded chain are different facts.
const (
	ModeEnsemble   = "ensemble"
	ModeStandalone = "standalone"
)

// Manifest is the versioned index of one run directory.
type Manifest struct {
	Schema      string       `json:"schema"`
	App         string       `json:"app"`
	Flow        string       `json:"flow"`
	RunID       string       `json:"runId"`
	Mode        string       `json:"mode"`
	Git         Git          `json:"git"`
	StartedAt   time.Time    `json:"startedAt"`
	FinishedAt  time.Time    `json:"finishedAt"`
	Checkpoints []Checkpoint `json:"checkpoints"`
	// Groups is the flow-part timeline, folded from groups.jsonl by
	// Task 4's run body at manifest time. Task 10 loads it from BOTH
	// manifests and feeds diff.Options.GroupsA/GroupsB, which is what
	// gives the wire diff its named sections.
	Groups []Group `json:"groups,omitempty"`
	// Capture is never omitted: "no verdict recorded" and "verdict ok" must
	// not serialize the same way, or a broken capture reads as a clean one.
	Capture CaptureTrust `json:"capture"`
	Wire    Counts       `json:"wire"`
	// Hops is nil in standalone mode — see ModeStandalone. Present-but-zero
	// means the chain was recorded and was empty.
	Hops *Counts `json:"hops,omitempty"`
	Test Test    `json:"test"`
	Env  Env     `json:"env"`
}

type Git struct {
	SHA    string `json:"sha"`
	Branch string `json:"branch"`
	Dirty  bool   `json:"dirty"`
}

type Checkpoint struct {
	Name string `json:"name"`
	File string `json:"file"` // run-dir-relative, e.g. "shots/cart.png"
	// Width and Height are the shot's REAL geometry, always pre-trim. A
	// checkpoint that asked for border trimming still reports what was
	// captured; trimming is a compare-time decision (Tasks 7 and 10) and
	// the rect actually used is reported there, per checkpoint pair.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Trim records that a `<name>.trim` marker sat beside the shot — the
	// adapter asked for uniform-border trimming at compare time. Reading
	// the marker here, rather than in the pixel engine, is what keeps
	// `capture` from importing `pixel`: capture records a fact, compare
	// acts on it.
	Trim bool `json:"trim,omitempty"`
}

type Counts struct {
	Calls int `json:"calls"`
}

type Test struct {
	Command    string  `json:"command"`
	ExitCode   int     `json:"exitCode"`
	DurationMs float64 `json:"durationMs"`
}

type Env struct {
	Go       string `json:"go"`
	Platform string `json:"platform"`
	Retrace  string `json:"retrace"`
}

// CaptureTrust is the capture-trust verdict every report surface banners.
// The types live here (not in retrace/capture) because the manifest carries
// them and the assessor reads Group — the other direction would be a cycle.
type CaptureTrust struct {
	Status  trace.Verdict `json:"status"`
	Reasons []TrustReason `json:"reasons,omitempty"`
	Gaps    []Gap         `json:"gaps,omitempty"`
	Summary string        `json:"summary"`
	Hint    string        `json:"hint,omitempty"`
}

type TrustReason struct {
	Code   string        `json:"code"`
	Status trace.Verdict `json:"status"`
	Detail string        `json:"detail"`
	Hint   string        `json:"hint,omitempty"`
}

type Gap struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Seconds int       `json:"seconds"`
}

func WriteManifest(p Paths, m *Manifest) error {  // see fix-round note below
	m.Schema = Schema
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.ManifestPath, append(b, '\n'), 0o644)
}

func ReadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ReadHops loads an NDJSON hop file through core/trace's reader, so retrace
// never re-implements the schema's parsing. A missing file is (nil, nil):
// a standalone run legitimately has no hops.jsonl.
func ReadHops(path string) (hops []trace.Hop, skipped int, err error) {  // see fix-round note below
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := trace.NewReader(f)
	var out []trace.Hop
	for {
		h, err := r.Next()
		if errors.Is(err, trace.ErrEOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
}
```

- [ ] **Step 8: Run the manifest tests — expect PASS**

Run: `go test ./retrace/runs/ -run 'TestManifest|TestReadHops' -v`
Expected: PASS.

- [ ] **Step 9: Write the failing groups test**

`retrace/runs/groups_test.go`:
```go
package runs

import (
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestDeriveGroupsClosesOnNextStartAndAtFinish(t *testing.T) {
	records := []GroupRecord{
		{Phase: "start", Name: "browse", TS: ts("2026-08-21T10:00:00Z")},
		{Phase: "start", Name: "checkout", TS: ts("2026-08-21T10:00:10Z")},
		{Phase: "end", TS: ts("2026-08-21T10:00:20Z")},
		{Phase: "start", Name: "receipt", TS: ts("2026-08-21T10:00:30Z")},
	}
	got := DeriveGroups(records, ts("2026-08-21T10:00:40Z"))
	if len(got) != 3 {
		t.Fatalf("want 3 intervals, got %d: %+v", len(got), got)
	}
	if got[0].Name != "browse" || !got[0].EndedAt.Equal(ts("2026-08-21T10:00:10Z")) {
		t.Fatalf("an unclosed group must end when the next one starts: %+v", got[0])
	}
	if !got[2].EndedAt.Equal(ts("2026-08-21T10:00:40Z")) {
		t.Fatalf("the last open group must close at finishedAt: %+v", got[2])
	}
}

func TestDeriveGroupsSortsOutOfOrderRecords(t *testing.T) {
	records := []GroupRecord{
		{Phase: "start", Name: "second", TS: ts("2026-08-21T10:00:10Z")},
		{Phase: "start", Name: "first", TS: ts("2026-08-21T10:00:00Z")},
	}
	got := DeriveGroups(records, ts("2026-08-21T10:00:20Z"))
	if got[0].Name != "first" {
		t.Fatalf("records must be sorted by ts, got %+v", got)
	}
}

func TestGroupAtIsHalfOpen(t *testing.T) {
	groups := DeriveGroups([]GroupRecord{
		{Phase: "start", Name: "a", TS: ts("2026-08-21T10:00:00Z")},
		{Phase: "start", Name: "b", TS: ts("2026-08-21T10:00:10Z")},
	}, ts("2026-08-21T10:00:20Z"))

	if got := GroupAt(groups, ts("2026-08-21T10:00:10Z")); got != "b" {
		t.Fatalf("a boundary call joins the group that just opened, got %q", got)
	}
	if got := GroupAt(groups, ts("2026-08-21T09:59:59Z")); got != "" {
		t.Fatalf("before any group must be empty, got %q", got)
	}
}

func TestAppendAndReadGroupRecordsSkipsCorruptLines(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := AppendGroupRecord(p, GroupRecord{Phase: "start", Name: "a", TS: ts("2026-08-21T10:00:00Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	if err := appendRaw(p.RunDir, "{not json\n"); err != nil {
		t.Fatalf("appendRaw: %v", err)
	}
	if err := AppendGroupRecord(p, GroupRecord{Phase: "end", TS: ts("2026-08-21T10:00:05Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	got, err := ReadGroupRecords(p)
	if err != nil {
		t.Fatalf("ReadGroupRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a corrupt marker line must be dropped, not fatal: %+v", got)
	}
}
```

Add the tiny test helper at the bottom of `groups_test.go`:
```go
func appendRaw(runDir, line string) error {
	f, err := os.OpenFile(filepath.Join(runDir, "groups.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}
```
(with `"os"` and `"path/filepath"` added to the test imports).

- [ ] **Step 10: Run it — expect FAIL**

Run: `go test ./retrace/runs/ -run Group -v`
Expected: FAIL, `undefined: GroupRecord`.

- [ ] **Step 11: Implement `retrace/runs/groups.go`**

```go
package runs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// GroupRecord is one flow-part marker as an adapter writes it. The writer is
// deliberately stateless — an `end` record carries no name, because a CLI or
// HTTP marker door is a fresh caller that cannot know what is open. Every
// sequencing rule lives in DeriveGroups instead.
type GroupRecord struct {
	Phase string    `json:"phase"` // "start" | "end"
	Name  string    `json:"name,omitempty"`
	TS    time.Time `json:"ts"`
	Quiet bool      `json:"quiet,omitempty"` // declared silence: suppresses gap suspicion
}

// Group is a derived half-open interval [StartedAt, EndedAt).
type Group struct {
	Name      string    `json:"name"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Quiet     bool      `json:"quiet,omitempty"`
}

// AppendGroupRecord and ReadGroupRecords take a Paths, not a bare runDir
// string, so the traversal guard is structural rather than documented: a
// Paths is only obtainable from PathsFor/Create, both of which validate
// app/flow/runID (review finding 2, re-review section 2 — the write
// side). A Paths{RunDir: ...} literal is technically still forgeable in
// Go; that is an accepted, documented residual — the goal here is
// removing the accidental door, not making Paths unforgeable.
func AppendGroupRecord(p Paths, r GroupRecord) error {
	if err := os.MkdirAll(p.RunDir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(p.RunDir, "groups.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ReadGroupRecords tolerates corrupt lines: a half-written marker from a
// killed test process must not make the whole run unreadable. Unlike
// ReadHops this function does not count drops (review finding 12, parked
// as an acceptable Minor for this task).
func ReadGroupRecords(p Paths) ([]GroupRecord, error) {
	f, err := os.Open(filepath.Join(p.RunDir, "groups.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []GroupRecord
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var r GroupRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, s.Err()
}

// DeriveGroups folds markers into intervals in start order. A name may
// repeat. An unclosed group closes when the next one opens, or at
// finishedAt — a marker placed after the traffic it meant to bracket then
// shows as an empty part, which is exactly the symptom worth seeing.
func DeriveGroups(records []GroupRecord, finishedAt time.Time) []Group {
	sorted := append([]GroupRecord(nil), records...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })

	var out []Group
	var open *Group
	closeAt := func(ts time.Time) {
		if open != nil {
			open.EndedAt = ts
			out = append(out, *open)
			open = nil
		}
	}
	for _, r := range sorted {
		switch r.Phase {
		case "start":
			closeAt(r.TS)
			open = &Group{Name: r.Name, StartedAt: r.TS, Quiet: r.Quiet}
		case "end":
			closeAt(r.TS)
		}
	}
	closeAt(finishedAt)
	return out
}

// GroupAt returns the part a timestamp falls in, "" for none. Half-open, so
// a call made at the instant a part opens belongs to that part.
func GroupAt(groups []Group, ts time.Time) string {
	for _, g := range groups {
		if !ts.Before(g.StartedAt) && ts.Before(g.EndedAt) {
			return g.Name
		}
	}
	return ""
}

// GroupNames lists distinct part names in first-seen order.
func GroupNames(groups []Group) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		if !seen[g.Name] {
			seen[g.Name] = true
			out = append(out, g.Name)
		}
	}
	return out
}
```

- [ ] **Step 12: Run the groups tests — expect PASS**

Run: `go test ./retrace/runs/ -v`
Expected: PASS (all 10 tests).

- [ ] **Step 13: CLI skeleton**

`retrace/cmd/retrace/output.go`:
```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// Exit codes are the CI contract. Every command returns one of these and
// nothing else, so a pipeline can branch on "needs review" vs "is broken"
// without parsing output.
const (
	exitOK    = 0 // no differences, nothing to review
	exitDiff  = 1 // differences found — review required
	exitGate  = 2 // a hard gate failed (rule violation, hopRequire, >=400, perf, capture)
	exitUsage = 3 // bad flags, unreadable config, I/O failure
)

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func fail(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "retrace: "+format+"\n", args...)
	return exitUsage
}
```

`retrace/cmd/retrace/main.go` (replacing the stub):
```go
// Command retrace records a flow through a local stack, replays it as strict
// mocks in CI, diffs two runs on pixels/wire/hops, and serves a review
// queue. It is a thin dispatcher: every subcommand lives in its own
// cmd_*.go and returns a process exit code (see output.go).
package main

import (
	"fmt"
	"io"
	"os"
)

// version is stamped by goreleaser via -X main.version at release time.
var version = "dev"

const usage = `retrace — record / replay / diff / review flows

Usage:
  retrace run --flow NAME [--app NAME] [--ensemble URL] [--upstream URL] -- <test command>
  retrace diff --flow NAME [--app NAME] [--a SELECTOR] [--b SELECTOR] [--json]
  retrace replay --ref FLOW [--app NAME] [--listen 127.0.0.1:0] -- <test command>
  retrace revalidate --ref FLOW [--app NAME] --upstream URL [--json]
  retrace ref accept|reject|list --flow NAME [--app NAME] [--run SELECTOR]
  retrace serve [--addr 127.0.0.1:4800] [--open]
  retrace export --out DIR [--flow NAME] [--app NAME]
  retrace --version

Exit codes:
  0 no differences   1 differences to review   2 hard gate failed   3 usage/IO error

Env:
  RETRACE_RUN_DIR     set by ` + "`retrace run`" + ` for adapters (checkpoints, markers)
  RETRACE_PROXY_URL   set by ` + "`retrace run`" + `; point the app under test at it
  RETRACE_MARKER_URL  set by ` + "`retrace run`" + `; HTTP-only runners post markers here
  RETRACE_STRICT      1 = adapters fail loudly when the handshake env is absent
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the testable entrypoint: it writes to the supplied writers rather
// than the process' own, so CLI tests capture output in-process instead of
// exec'ing a subprocess.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "--version", "-version":
		fmt.Fprintln(stdout, version)
		return exitOK
	default:
		return fail(stderr, "unknown command %q\n\n%s", args[0], usage)
	}
}
```

- [ ] **Step 14: Verify the CLI builds and prints usage**

Run: `go build -o /tmp/retrace-check ./retrace/cmd/retrace && /tmp/retrace-check --version && /tmp/retrace-check bogus; echo "exit=$?"`
Expected: prints `dev`, then `retrace: unknown command "bogus"` plus usage, `exit=3`.

**Build a binary; do not use `go run` to check an exit code.** `go run`
reports a non-zero child as its own failure: it prints `exit status 3` to
stderr and itself exits **1**. Verified. Any exit-code assertion written
against `go run` silently checks the wrong number — it passes for 1 and
fails for every code the CLI actually defines.

- [ ] **Step 15: Full suites + commit**

```bash
gofmt -l ./core ./retrace ./ensemble && go vet ./retrace/...
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/runs retrace/cmd/retrace
git commit -m "feat(retrace): run-dir model, manifest schema retrace/1, flow-part groups, CLI skeleton"
```

---

### Task 2: Wire-rules — value matchers and rule resolution (port from flowlens)

**Files:**
- Create: `retrace/rules/matchers.go`, `retrace/rules/matchers_test.go`
- Create: `retrace/rules/rules.go`, `retrace/rules/rules_test.go`

**Interfaces:**
- Consumes: nothing (pure package, stdlib only — `regexp`, `strings`).
- Produces (used by Tasks 3, 8, 12, 13):
  ```go
  package rules
  type Kind string
  const (KindExact Kind = "exact"; KindIgnore Kind = "ignore"; KindNamed Kind = "named"; KindPattern Kind = "pattern")
  type Matcher struct { Kind Kind; Name string; Pattern string; re *regexp.Regexp }
  func ParseMatcher(spec any, where string) (Matcher, error)
  func (m Matcher) Label() string          // "uuid" | "/^v\\d+$/" | "exact" | "ignore"
  func (m Matcher) Zero() bool             // true for the zero Matcher (no rule applies)
  type Outcome string
  const (Ignored Outcome = "ignored"; Changed Outcome = "changed"; Tolerated Outcome = "tolerated"; Violation Outcome = "violation")
  func Classify(m Matcher, a, b any, bothPresent bool) Outcome
  func Names() []string                    // MATCHER_NAMES, for error text

  type Raw struct {
      Method  string         `json:"method,omitempty"  yaml:"method"`
      Path    string         `json:"path,omitempty"    yaml:"path"`
      Headers map[string]any `json:"headers,omitempty" yaml:"headers"`
      Body    map[string]any `json:"body,omitempty"    yaml:"body"`
  }
  type BodyRule struct { Glob string; Matcher Matcher }
  type Rule struct { Method, Path string; Headers map[string]Matcher; Body []BodyRule }
  func Normalize(raw []Raw) ([]Rule, error)
  type Resolved struct { Headers map[string]Matcher; Body []BodyRule }
  func Resolve(rs []Rule, method, normalizedPath string) Resolved
  func (r Resolved) ForHeader(name string) Matcher   // lowercased lookup; Zero() if none
  func (r Resolved) ForField(fieldPath string) Matcher
  func MatchPathGlob(glob, path string) bool   // '/'-segmented, '*' within a segment, '**' spans
  func MatchFieldGlob(glob, fieldPath string) bool // '.'-segmented, same semantics
  ```

- [ ] **Step 1: Write the failing matcher test**

`retrace/rules/matchers_test.go` — port every case from flowlens
`test/wire-rules.test.mjs` lines 20–54 as Go subtests:

```go
package rules

import "testing"

func TestNamedMatchersAcceptTheirFormatAndRejectOthers(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"uuid", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"uuid", "not-a-uuid", false},
		{"iso8601", "2026-08-21T10:15:00.123Z", true},
		{"iso8601", "Wed", false}, // stricter than time.Parse-anything on purpose
		{"http-date", "Wed, 21 Aug 2026 10:15:00 GMT", true},
		{"http-date", "2026-08-21T10:15:00Z", false},
		{"etag", `W/"abc"`, true},
		{"etag", "abc", false},
		{"integer", 1760.0, true},  // a JSON number decodes as float64
		{"integer", "1760", true},  // a header carries the string form
		{"integer", 17.6, false},
		{"semver", "1.2.3-rc.1", true},
		{"semver", "1.2", false},
	}
	for _, c := range cases {
		m, err := ParseMatcher(c.name, "test")
		if err != nil {
			t.Fatalf("ParseMatcher(%q): %v", c.name, err)
		}
		got := Classify(m, c.value, c.value, true) == Tolerated
		if got != c.want {
			t.Errorf("%s(%v) satisfied = %v, want %v", c.name, c.value, got, c.want)
		}
	}
}

func TestMatcherToleratesValueChangeButCatchesShapeChange(t *testing.T) {
	m, _ := ParseMatcher("uuid", "test")
	if got := Classify(m, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "6ec0bd7f-11c0-43da-975e-2a8ad9ebae0b", true); got != Tolerated {
		t.Errorf("two uuids = %v, want tolerated", got)
	}
	if got := Classify(m, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", 42.0, true); got != Violation {
		t.Errorf("uuid vs number = %v, want violation", got)
	}
}

func TestUnknownMatcherNameIsAnErrorNotSilentTolerance(t *testing.T) {
	if _, err := ParseMatcher("uuidv4", "wireRules[0].body.id"); err == nil {
		t.Fatal("want an error naming the location and the valid names")
	}
}

func TestCustomPatternRequiresBothSidesToMatch(t *testing.T) {
	m, err := ParseMatcher(map[string]any{"pattern": `^v\d+$`}, "test")
	if err != nil {
		t.Fatalf("ParseMatcher: %v", err)
	}
	if got := Classify(m, "v1", "v2", true); got != Tolerated {
		t.Errorf("both match = %v, want tolerated", got)
	}
	if got := Classify(m, "v1", "x", true); got != Violation {
		t.Errorf("one side fails = %v, want violation", got)
	}
	if m.Label() != `/^v\d+$/` {
		t.Errorf("Label = %q", m.Label())
	}
}

func TestAValueMatcherNeverExcusesAnAppearingOrDisappearingField(t *testing.T) {
	uuid, _ := ParseMatcher("uuid", "test")
	if got := Classify(uuid, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", nil, false); got != Changed {
		t.Errorf("one-sided value under a matcher = %v, want changed", got)
	}
	ign, _ := ParseMatcher("ignore", "test")
	if got := Classify(ign, "x", nil, false); got != Ignored {
		t.Errorf("ignore must silence a one-sided value, got %v", got)
	}
}
```

- [ ] **Step 2: Run it — expect FAIL** (`go test ./retrace/rules/ -v` → `undefined: ParseMatcher`).

- [ ] **Step 3: Implement `retrace/rules/matchers.go`**

```go
// Package rules answers one question: "these two values differ — is that
// difference tolerable?" A matcher tolerates a value change while still
// catching a SHAPE change, which is what makes it stronger than an ignore.
// Ported from the flowlens prototype's src/matchers.mjs + src/wire-rules.mjs;
// the regexes are deliberately byte-identical so its fixtures stay valid.
package rules

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	etagRe    = regexp.MustCompile(`^(W/)?"[^"]*"$`)
	integerRe = regexp.MustCompile(`^-?\d+$`)
	semverRe  = regexp.MustCompile(`^\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)*$`)
	// Deliberately stricter than a permissive date parser, which accepts
	// "Wed" and other junk.
	isoRe      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?$`)
	httpDateRe = regexp.MustCompile(`^[A-Z][a-z]{2}, \d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} GMT$`)
)

type Kind string

const (
	KindExact   Kind = "exact"
	KindIgnore  Kind = "ignore"
	KindNamed   Kind = "named"
	KindPattern Kind = "pattern"
)

type Matcher struct {
	Kind    Kind
	Name    string
	Pattern string
	re      *regexp.Regexp
}

func (m Matcher) Zero() bool { return m.Kind == "" }

func (m Matcher) Label() string {
	switch m.Kind {
	case KindNamed:
		return m.Name
	case KindPattern:
		return "/" + m.Pattern + "/"
	default:
		return string(m.Kind)
	}
}

var named = map[string]func(any) bool{
	"uuid":      func(v any) bool { s, ok := v.(string); return ok && uuidRe.MatchString(s) },
	"etag":      func(v any) bool { s, ok := v.(string); return ok && etagRe.MatchString(s) },
	"semver":    func(v any) bool { s, ok := v.(string); return ok && semverRe.MatchString(s) },
	"iso8601":   func(v any) bool { s, ok := v.(string); return ok && isoRe.MatchString(s) && parses(s, time.RFC3339) },
	"http-date": func(v any) bool { s, ok := v.(string); return ok && httpDateRe.MatchString(s) && parses(s, http.TimeFormat) },
	// Accepts a JSON number too — a body field carries 1760, a header "1760".
	"integer": func(v any) bool {
		switch t := v.(type) {
		case float64:
			return t == float64(int64(t))
		case json.Number:
			_, err := t.Int64()
			return err == nil
		case string:
			return integerRe.MatchString(t)
		}
		return false
	},
}

func parses(s, layout string) bool {
	if _, err := time.Parse(layout, s); err == nil {
		return true
	}
	// RFC3339 without a zone, and the "YYYY-MM-DD HH:MM:SS" variant the
	// regex already blessed, still parse — the regex is the gate, this is
	// only the "is it a real date" backstop.
	for _, alt := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04:05.999999999Z07:00"} {
		if _, err := time.Parse(alt, s); err == nil {
			return true
		}
	}
	return false
}

// Names lists every accepted matcher name, for error messages.
func Names() []string {
	out := []string{string(KindExact), string(KindIgnore)}
	for k := range named {
		out = append(out, k)
	}
	sort.Strings(out[2:])
	return out
}

// ParseMatcher accepts a name string, a {"pattern": "..."} map (either
// map[string]any from JSON/YAML or map[string]string), or nil for exact.
// An unknown name is an error rather than silently tolerating nothing.
func ParseMatcher(spec any, where string) (Matcher, error) {
	at := ""
	if where != "" {
		at = " at " + where
	}
	switch t := spec.(type) {
	case nil:
		return Matcher{Kind: KindExact}, nil
	case string:
		if t == string(KindExact) || t == string(KindIgnore) {
			return Matcher{Kind: Kind(t)}, nil
		}
		if _, ok := named[t]; ok {
			return Matcher{Kind: KindNamed, Name: t}, nil
		}
		return Matcher{}, fmt.Errorf("unknown matcher %q%s — expected one of: %s, or {pattern: ...}",
			t, at, strings.Join(Names(), ", "))
	case map[string]any:
		p, _ := t["pattern"].(string)
		return compilePattern(p, at)
	case map[string]string:
		return compilePattern(t["pattern"], at)
	}
	return Matcher{}, fmt.Errorf("invalid matcher%s — expected a name or {pattern: ...}", at)
}

func compilePattern(p, at string) (Matcher, error) {
	if p == "" {
		return Matcher{}, fmt.Errorf("invalid matcher%s — expected a name or {pattern: ...}", at)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return Matcher{}, fmt.Errorf("invalid matcher pattern %q%s: %w", p, at, err)
	}
	return Matcher{Kind: KindPattern, Pattern: p, re: re}, nil
}

func (m Matcher) satisfies(v any) bool {
	switch m.Kind {
	case KindNamed:
		return named[m.Name](v)
	case KindPattern:
		s, ok := v.(string)
		return ok && m.re.MatchString(s)
	}
	return false
}

// Classify decides what a difference between two values means under a
// matcher. bothPresent is false when one side has no value at all: a value
// matcher cannot speak to a value that does not exist, so an appearing or
// disappearing field stays a real change — only ignore silences it.
func Classify(m Matcher, a, b any, bothPresent bool) Outcome {
	switch m.Kind {
	case KindIgnore:
		return Ignored
	case "", KindExact:
		return Changed
	}
	if !bothPresent {
		return Changed
	}
	if m.satisfies(a) && m.satisfies(b) {
		return Tolerated
	}
	return Violation
}

type Outcome string

const (
	Ignored   Outcome = "ignored"
	Changed   Outcome = "changed"
	Tolerated Outcome = "tolerated"
	Violation Outcome = "violation"
)
```

- [ ] **Step 4: Run — expect PASS** (`go test ./retrace/rules/ -v`).

- [ ] **Step 5: Write the failing rule-resolution test**

`retrace/rules/rules_test.go` — port flowlens `test/wire-rules.test.mjs`
cases 55–90 plus the glob semantics:
`TestPathGlobsScopeARuleStarOneSegmentDoubleStarAnySpan`,
`TestALaterMoreSpecificRuleOverridesAnEarlierGlobalOnePerKey`,
`TestMethodScopesARuleAndIsCaseInsensitive`,
`TestTheLastMatchingBodyGlobWins`,
`TestHeaderLookupIsCaseInsensitiveOnTheName`,
`TestNormalizeRejectsAnUnknownMatcherNamingTheRuleIndex`.

```go
func TestPathGlobsScopeARuleStarOneSegmentDoubleStarAnySpan(t *testing.T) {
	cases := []struct{ glob, path string; want bool }{
		{"/experience/*", "/experience/home", true},
		{"/experience/*", "/experience/home/v2", false},
		{"/experience/*.json", "/experience/home.json", true},
		{"/api/**", "/api/v1/cart/items", true},
		{"/api/**/items", "/api/v1/cart/items", true},
		{"", "/anything", true}, // an unset path scopes to everything
	}
	for _, c := range cases {
		if got := MatchPathGlob(c.glob, c.path); got != c.want {
			t.Errorf("MatchPathGlob(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestALaterMoreSpecificRuleOverridesAnEarlierGlobalOnePerKey(t *testing.T) {
	rs, err := Normalize([]Raw{
		{Headers: map[string]any{"x-request-id": "uuid", "date": "http-date"}},
		{Path: "/cart", Headers: map[string]any{"x-request-id": "ignore"}},
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	res := Resolve(rs, "GET", "/cart")
	if got := res.ForHeader("X-Request-Id").Kind; got != KindIgnore {
		t.Errorf("specific rule must win for its key: %v", got)
	}
	if got := res.ForHeader("date").Name; got != "http-date" {
		t.Errorf("untouched keys keep the global rule: %v", got)
	}
}

func TestTheLastMatchingBodyGlobWins(t *testing.T) {
	rs, _ := Normalize([]Raw{{Body: map[string]any{"**.id": "uuid"}}, {Body: map[string]any{"order.id": "integer"}}})
	res := Resolve(rs, "POST", "/orders")
	if got := res.ForField("order.id").Name; got != "integer" {
		t.Errorf("ForField = %q, want integer", got)
	}
	if got := res.ForField("user.id").Name; got != "uuid" {
		t.Errorf("ForField = %q, want uuid", got)
	}
}
```

- [ ] **Step 6: Run — expect FAIL** (`undefined: Normalize`).

- [ ] **Step 7: Implement `retrace/rules/rules.go`**

```go
package rules

import (
	"fmt"
	"sort"
	"strings"
)

type Raw struct {
	Method  string         `json:"method,omitempty"  yaml:"method"`
	Path    string         `json:"path,omitempty"    yaml:"path"`
	Headers map[string]any `json:"headers,omitempty" yaml:"headers"`
	Body    map[string]any `json:"body,omitempty"    yaml:"body"`
}

type BodyRule struct {
	Glob    string
	Matcher Matcher
}

type Rule struct {
	Method  string // "" = any; stored upper-cased
	Path    string // "" = any
	Headers map[string]Matcher
	Body    []BodyRule
}

// Normalize validates and lowers a config's raw rules. Map iteration order
// is random in Go, so header and body entries within ONE raw rule are
// sorted by key — otherwise "last one wins" would be nondeterministic
// between runs of the same config.
func Normalize(raw []Raw) ([]Rule, error) {
	out := make([]Rule, 0, len(raw))
	for i, r := range raw {
		where := fmt.Sprintf("wireRules[%d]", i)
		rule := Rule{Method: strings.ToUpper(r.Method), Path: r.Path, Headers: map[string]Matcher{}}
		for _, name := range sortedKeys(r.Headers) {
			m, err := ParseMatcher(r.Headers[name], where+".headers."+name)
			if err != nil {
				return nil, err
			}
			rule.Headers[strings.ToLower(name)] = m
		}
		for _, glob := range sortedKeys(r.Body) {
			m, err := ParseMatcher(r.Body[glob], where+".body."+glob)
			if err != nil {
				return nil, err
			}
			rule.Body = append(rule.Body, BodyRule{Glob: glob, Matcher: m})
		}
		out = append(out, rule)
	}
	return out, nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type Resolved struct {
	Headers map[string]Matcher
	Body    []BodyRule
}

// Resolve collapses every rule that applies to this call into one lookup.
// Later rules overwrite earlier ones per key, so a specific rule placed
// after a global one wins for that key alone.
func Resolve(rs []Rule, method, normalizedPath string) Resolved {
	res := Resolved{Headers: map[string]Matcher{}}
	for _, r := range rs {
		if r.Method != "" && r.Method != strings.ToUpper(method) {
			continue
		}
		if !MatchPathGlob(r.Path, normalizedPath) {
			continue
		}
		for k, m := range r.Headers {
			res.Headers[k] = m
		}
		res.Body = append(res.Body, r.Body...)
	}
	return res
}

func (r Resolved) ForHeader(name string) Matcher { return r.Headers[strings.ToLower(name)] }

// ForField mirrors the header map's last-write-wins: the last matching body
// glob decides.
func (r Resolved) ForField(fieldPath string) Matcher {
	for i := len(r.Body) - 1; i >= 0; i-- {
		if MatchFieldGlob(r.Body[i].Glob, fieldPath) {
			return r.Body[i].Matcher
		}
	}
	return Matcher{}
}

// MatchPathGlob matches a '/'-segmented URL path. '*' matches within one
// segment (so both "/experience/*" and "/experience/*.json" work); '**'
// spans any number of segments, including none. An empty glob matches
// everything — an unscoped rule applies to every call.
func MatchPathGlob(glob, path string) bool {
	if glob == "" {
		return true
	}
	return matchSegs(split(glob, "/"), split(path, "/"))
}

// MatchFieldGlob is the same algorithm over '.'-segmented JSON field paths.
// Array indices are part of their segment ("items[0]"), so "items[*]" is
// spelled "items.*" only when the walker emits index segments — see
// retrace/diff/wire.go, which emits "items[0].sku".
func MatchFieldGlob(glob, fieldPath string) bool {
	return matchSegs(split(glob, "."), split(fieldPath, "."))
}

func split(s, sep string) []string {
	out := []string{}
	for _, p := range strings.Split(s, sep) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func matchSegs(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	head := pattern[0]
	if head == "**" {
		for i := 0; i <= len(path); i++ {
			if matchSegs(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if !segMatches(head, path[0]) {
		return false
	}
	return matchSegs(pattern[1:], path[1:])
}

// segMatches handles '*' inside one segment without regexp compilation per
// call: the pattern is split on '*' and matched as an ordered set of
// literal chunks.
func segMatches(pattern, seg string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == seg
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(seg, parts[0]) {
		return false
	}
	rest := seg[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(rest, parts[i])
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(parts[i]):]
	}
	last := parts[len(parts)-1]
	return strings.HasSuffix(rest, last) && len(rest) >= len(last)
}
```

- [ ] **Step 8: Run — expect PASS** (`go test -race ./retrace/rules/ -v`, all cases green).

- [ ] **Step 9: Commit**

```bash
gofmt -l ./retrace && go vet ./retrace/...
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/rules
git commit -m "feat(retrace): wire-rules matchers and rule resolution, ported from flowlens goldens"
```

---

### Task 3: `retrace.yaml` config + machine-owned wire-rule overlay

**Files:**
- Create: `retrace/config/config.go`, `retrace/config/config_test.go`
- Modify: `retrace/go.mod` (add `gopkg.in/yaml.v3`)

**Dependency justification (required by Global Constraints):** `retrace.yaml`
must be the same shape of file as `ensemble.yaml` — one product, one config
language, and `ensemble/config` already parses YAML with `gopkg.in/yaml.v3
v3.0.1`. The module is already in `go.work.sum`, so this adds no new
supply-chain surface; the alternative (JSON config for the second product)
splits the user-facing story for no gain.

**Interfaces:**
- Consumes: `retrace/rules` — `rules.Raw`, `rules.Normalize`.
- Produces (used by Tasks 4, 5, 8, 9, 10, 11, 12, 13, 16):
  ```go
  package config
  type Config struct {
      App              string
      Flows            map[string]Flow
      Entry            string          // ensemble service name clients call
      Upstream         string          // standalone-mode upstream base URL
      WireIgnore       []WireIgnoreEntry   // {Path, Why}; see WireIgnorePaths()
      WireRules        []rules.Raw
      PathNormalize    []Normalize
      ExpectedStatuses []StatusRule
      HopRequire       []RequiredRoute
      Masks            map[string][]Rect  // checkpoint name (or "*") -> rects
      Thresholds       Thresholds
      OpenAPI          string
      Redact           []string
      Deviations       string
      Dir              string             // dir containing retrace.yaml
  }
  type Flow struct { Command string; PerfBudgetMs float64; Masks map[string][]Rect }
  type Normalize struct { Pattern, Replacement string; re *regexp.Regexp }
  func (n Normalize) Apply(path string) string
  type StatusRule struct { Path string; Status int }
  type RequiredRoute struct { Method, Path string; Status int }
  type Rect struct { X, Y, Width, Height int; Why string }  // json+yaml tags, see Step 3
  type Thresholds struct { Gate, Fine float64 }   // defaults DefaultGate / DefaultFine
  // EVERY type in this package needs explicit `yaml:` tags — Load uses
  // dec.KnownFields(true) and yaml.v3 matches lower-cased field names, so
  // an untagged `WireIgnore` rejects `wire_ignore` outright. Step 3 writes
  // them all out.
  func Load(path string) (*Config, error)
  func Discover(cwd string) (*Config, error)      // retrace.yaml then .retrace/wire-rules.json overlay
  func (c *Config) Rules() ([]rules.Rule, error)
  func (c *Config) MasksFor(flow, checkpoint string) []Rect
  func (c *Config) NormalizePath(path string) string  // applies PathNormalize in order
  func AppendWireRule(dir string, r rules.Raw) error  // writes .retrace/wire-rules.json
  ```

- [ ] **Step 1: Write the failing config test**

`retrace/config/config_test.go` — key cases:
```go
func TestLoadParsesFlowsRulesAndDefaults(t *testing.T) {
	dir := t.TempDir()
	yaml := `
app: web
entry: edge-gw
flows:
  checkout:
    command: npx playwright test checkout.spec.ts
    perf_budget_ms: 4200
wire_ignore:
  - "**.requestId"
wire_rules:
  - headers: { x-request-id: uuid }
  - path: /cart
    body: { updatedAt: iso8601 }
path_normalize:
  - { pattern: "/users/[0-9]+", replacement: "/users/:id" }
expected_statuses:
  - { path: /api/flaky, status: 503 }
hop_require:
  - { method: POST, path: /payments/**, status: 201 }
masks:
  cart: [{ x: 10, y: 20, width: 100, height: 40 }]
`
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte(yaml), 0o644)
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil { t.Fatalf("Load: %v", err) }
	if cfg.Thresholds.Gate != 0.1 || cfg.Thresholds.Fine != 0.05 {
		t.Fatalf("thresholds must default to 0.1/0.05, got %+v", cfg.Thresholds)
	}
	if got := cfg.NormalizePath("/users/42/cart"); got != "/users/:id/cart" {
		t.Fatalf("NormalizePath = %q", got)
	}
	if _, err := cfg.Rules(); err != nil { t.Fatalf("Rules: %v", err) }
	if cfg.Dir != dir { t.Fatalf("Dir = %q, want %q", cfg.Dir, dir) }
}

func TestAppendWireRuleWritesTheOverlayAndDiscoverMergesItAfterYaml(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	if err := AppendWireRule(dir, rules.Raw{Path: "/cart", Body: map[string]any{"total": "integer"}}); err != nil {
		t.Fatalf("AppendWireRule: %v", err)
	}
	cfg, err := Discover(dir)
	if err != nil { t.Fatalf("Discover: %v", err) }
	if len(cfg.WireRules) != 1 || cfg.WireRules[0].Path != "/cart" {
		t.Fatalf("overlay rule not merged: %+v", cfg.WireRules)
	}
	// Appending twice must not duplicate an identical rule — the review
	// queue's `rule` verb is idempotent by design.
	AppendWireRule(dir, rules.Raw{Path: "/cart", Body: map[string]any{"total": "integer"}})
	cfg, _ = Discover(dir)
	if len(cfg.WireRules) != 1 { t.Fatalf("duplicate rule appended: %+v", cfg.WireRules) }
}

// An unknown matcher must fail Load and NAME the offending rule. A config
// error that says only "invalid matcher" costs an editing round-trip on a
// file that may hold fifty rules.
func TestAnInvalidMatcherFailsLoadNamingTheRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("wire_rules:\n  - path: /cart\n    headers:\n      date: httpdate\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("an unknown matcher must fail Load")
	}
	if !strings.Contains(err.Error(), "wireRules[0].headers.date") {
		t.Fatalf("error must name the rule, got: %v", err)
	}
	if !strings.Contains(err.Error(), "http-date") {
		t.Fatalf("error should suggest the real matcher name, got: %v", err)
	}
}

func TestMasksForFallsBackToTheWildcardCheckpoint(t *testing.T) {
	c := &Config{Masks: map[string][]Rect{"*": {{X: 0, Y: 0, Width: 10, Height: 10}}}}
	if got := c.MasksFor("checkout", "cart"); len(got) != 1 || got[0].Width != 10 {
		t.Fatalf(`the "*" key must apply to every checkpoint, got %+v`, got)
	}
	c.Masks["cart"] = []Rect{{X: 1, Y: 1, Width: 20, Height: 20}}
	if got := c.MasksFor("checkout", "cart"); len(got) != 1 || got[0].Width != 20 {
		t.Fatalf("a named checkpoint must win over the wildcard, got %+v", got)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: Load`).

- [ ] **Step 3: Implement `retrace/config/config.go`**

**Write the `yaml:` tags out — they are not optional decoration here.**
`Load` sets `dec.KnownFields(true)`, and yaml.v3's default field matching is
*lower-cased field name*, not snake_case: `WireIgnore` matches `wireignore`,
**never** `wire_ignore`. So a missing tag is not a silently-ignored key, it
is a hard `Load` error — `field wire_ignore not found in type config.Config`
— on the very fixture above. Every multi-word key needs its tag:

```go
type Config struct {
	App              string                 `yaml:"app"`
	Flows            map[string]Flow        `yaml:"flows"`
	Entry            string                 `yaml:"entry"`
	Upstream         string                 `yaml:"upstream"`
	WireIgnore       []WireIgnoreEntry      `yaml:"wire_ignore"`
	WireRules        []rules.Raw            `yaml:"wire_rules"`
	PathNormalize    []Normalize            `yaml:"path_normalize"`
	ExpectedStatuses []StatusRule           `yaml:"expected_statuses"`
	HopRequire       []RequiredRoute        `yaml:"hop_require"`
	Masks            map[string][]Rect      `yaml:"masks"`
	Thresholds       Thresholds             `yaml:"thresholds"`
	OpenAPI          string                 `yaml:"openapi"`
	Redact           []string               `yaml:"redact"`
	Deviations       string                 `yaml:"deviations"`
	// --- added after this task shipped, by the consolidated shapes task ---
	// Gates is the per-plane CI budget. Gate.BudgetPct is *float64, NOT
	// float64: that pointer is what keeps "absent" and "explicitly 0"
	// distinguishable, and an explicit `budget_pct: 0` legitimately means
	// "must be pixel-identical". applyDefaults fills Gates["pixel"] from
	// Thresholds.Gate when absent, so pixel is NEVER missing; wire, hop and
	// perf have no default and stay ungated when absent. Task 10 consumes.
	Gates  map[string]Gate `yaml:"gates"`
	FailOn []string        `yaml:"fail_on"`
	// Preflight runs once before any flow; Flow.Preflight then runs before
	// that specific flow. Shape only — nothing executes these yet.
	Preflight []string `yaml:"preflight"`
	// Dir is set by Load from the file's own location and is NOT a YAML
	// key. It must be tagged `yaml:"-"`, or KnownFields(true) will happily
	// accept a `dir:` key in the file and then Load will overwrite it —
	// a setting that appears to work and silently does nothing.
	Dir string `yaml:"-"`
	// Loaded reports whether a real retrace.yaml was found and read. Zero
	// value false is deliberately the unsafe-to-proceed one: no config means
	// no redaction rules, so a consumer that captures traffic must REFUSE
	// rather than write unredacted bodies to disk.
	Loaded bool `yaml:"-"`
}

type Gate struct {
	BudgetPct *float64 `yaml:"budget_pct"`
}

type Flow struct {
	Command      string            `yaml:"command"`
	PerfBudgetMs float64           `yaml:"perf_budget_ms"`
	Masks        map[string][]Rect `yaml:"masks"`
	// Shape only, as above: parsed and tagged, never executed here.
	Preflight []string `yaml:"preflight"`
	Setup     []string `yaml:"setup"`
	Teardown  []string `yaml:"teardown"`
}
type Normalize struct {
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
	re          *regexp.Regexp
}
type StatusRule struct {
	Path   string `yaml:"path"`
	Status int    `yaml:"status"`
}
type RequiredRoute struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
	Status int    `yaml:"status"`
}
type Thresholds struct {
	Gate float64 `yaml:"gate"`
	Fine float64 `yaml:"fine"`
}
```

`rules.Raw` already carries `json:` tags (Task 2) and needs matching
`yaml:` ones for the same reason — it is decoded from both
`retrace.yaml`'s `wire_rules` and `.retrace/wire-rules.json`.
`TestAYamlKeyTypoIsAnErrorNamingTheKey` pins the `KnownFields` behaviour so
the tags cannot quietly rot.

Key body:
```go
// Package config parses retrace.yaml — the flows to record, the rules that
// decide what counts as a difference, and the thresholds that gate CI.
//
// Rules come from TWO places on purpose. retrace.yaml is human-owned and
// full of explanatory comments; the review queue's `rule` verb appends
// machine-written rules to .retrace/wire-rules.json instead, because
// re-emitting YAML would silently delete the human's comments. The overlay
// is loaded AFTER the yaml rules, so a hand-written rule can be overridden
// by a later reviewed one but never clobbered on disk.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

const OverlayPath = ".retrace/wire-rules.json"

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a typo'd key is an error, not a silently ignored setting
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Dir = filepath.Dir(path)
	applyDefaults(&c)
	for i := range c.PathNormalize {
		re, err := regexp.Compile(c.PathNormalize[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: path_normalize[%d]: %w", path, i, err)
		}
		c.PathNormalize[i].re = re
	}
	// Fail at load, not at first diff: an unknown matcher name in config is
	// a typo the user wants to hear about now.
	if _, err := c.Rules(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Discover loads <cwd>/retrace.yaml plus the machine-owned overlay. A
// missing retrace.yaml is not an error — an app with no config still records
// and diffs, it just has no rules.
func Discover(cwd string) (*Config, error) {
	// Defaults have ONE source: applyDefaults, which Load also calls. The
	// earlier draft re-hardcoded 0.1/0.05 here, which is two places to
	// change a number that appears in every pixel verdict.
	c := &Config{Dir: cwd}
	applyDefaults(c)
	if _, err := os.Stat(filepath.Join(cwd, "retrace.yaml")); err == nil {
		c, err = Load(filepath.Join(cwd, "retrace.yaml"))
		if err != nil {
			return nil, err
		}
	}
	overlay, err := readOverlay(filepath.Join(cwd, OverlayPath))
	if err != nil {
		return nil, err
	}
	c.WireRules = append(c.WireRules, overlay...)
	if _, err := c.Rules(); err != nil {
		return nil, fmt.Errorf("%s: %w", OverlayPath, err)
	}
	return c, nil
}

func (c *Config) NormalizePath(path string) string {
	for _, n := range c.PathNormalize {
		if n.re != nil {
			path = n.re.ReplaceAllString(path, n.Replacement)
		}
	}
	return path
}

// AppendWireRule adds one rule to the overlay, idempotently: the review
// queue's `rule` verb can be pressed twice on the same field (or by a human
// and an agent at once) and must not grow the file each time.
func AppendWireRule(dir string, r rules.Raw) error {
	path := filepath.Join(dir, OverlayPath)
	existing, err := readOverlay(path)
	if err != nil {
		return err
	}
	want, _ := json.Marshal(r)
	for _, e := range existing {
		if got, _ := json.Marshal(e); string(got) == string(want) {
			return nil
		}
	}
	existing = append(existing, r)
	b, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
```

`applyDefaults` is the single home for every zero-value fallback in this
package — `Load` calls it after decoding, `Discover` calls it for the
no-config case, and nothing else hardcodes a default:

```go
// DefaultGate and DefaultFine are the pixel thresholds every verdict is
// measured against. They are named constants because they appear in
// report copy, in `retrace diff --help`, and in the docs.
const (
	DefaultGate = 0.1
	DefaultFine = 0.05
)

func applyDefaults(c *Config) {
	if c.Thresholds.Gate == 0 {
		c.Thresholds.Gate = DefaultGate
	}
	if c.Thresholds.Fine == 0 {
		c.Thresholds.Fine = DefaultFine
	}
}
```

`MasksFor` is real code, not a description — Task 10 calls it per
checkpoint and converts the result into `pixel.Rect`:

```go
// Rect is the config-facing rectangle. It is deliberately NOT pixel.Rect:
// retrace/config must not import retrace/pixel (config is the leaf every
// package reads). Task 10 and Task 11 convert at the call site; the
// conversion is one function, pixel.RectsFrom, and it lives in the pixel
// package for the same reason.
type Rect struct {
	X      int `json:"x" yaml:"x"`
	Y      int `json:"y" yaml:"y"`
	Width  int `json:"width" yaml:"width"`
	Height int `json:"height" yaml:"height"`
	// Why records the reason this mask exists. A mask hides pixels from the
	// diff, so an unexplained one is indistinguishable from a mask added to
	// silence a real regression — and masks outlive whoever added them.
	// Optional: existing configs without it must keep loading.
	Why string `json:"why,omitempty" yaml:"why"`
}

// MasksFor resolves the masks for one checkpoint: the flow's own map wins,
// then the top-level map, and within each the named checkpoint wins over
// the "*" wildcard. First non-empty list wins; an explicit empty list at a
// more specific level does NOT mask anything (use it to opt a checkpoint
// out of a wildcard).
func (c *Config) MasksFor(flow, checkpoint string) []Rect {
	for _, m := range []map[string][]Rect{c.Flows[flow].Masks, c.Masks} {
		if m == nil {
			continue
		}
		if r, ok := m[checkpoint]; ok {
			return r
		}
		if r, ok := m["*"]; ok {
			return r
		}
	}
	return nil
}
```

- [ ] **Step 4: Run — expect PASS** (`go test ./retrace/config/ -v`).

- [ ] **Step 5: Wire the dependency and commit**

```bash
cd retrace && go get gopkg.in/yaml.v3@v3.0.1 && go mod tidy && cd ..
gofmt -l ./retrace && go vet ./retrace/...
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/config retrace/go.mod retrace/go.sum go.work.sum
git commit -m "feat(retrace): retrace.yaml config with machine-owned wire-rule overlay"
```

---

### Task 4: Standalone capture — `retrace run` without ensemble, marker door, env handshake

**Files:**
- Create: `core/httpguard/guard.go`, `core/httpguard/guard_test.go`
- Modify: `ensemble/server/guard.go` (becomes a 4-line delegator), `ensemble/server/server.go:89`
- Create: `retrace/capture/capture.go`, `retrace/capture/capture_test.go`
- Create: `retrace/capture/markers.go`, `retrace/capture/markers_test.go`
- Create: `retrace/cmd/retrace/cmd_run.go`, `retrace/cmd/retrace/cmd_run_test.go`
- Modify: `retrace/cmd/retrace/main.go` (dispatch `run`)

**Interfaces:**
- Consumes: `core/proxy` — `proxy.NewRecorder(proxy.RecorderOpts{Ring int; Redactor *trace.Redactor; Writer *trace.Writer})`,
  `proxy.New(*proxy.Recorder) *proxy.Proxy`,
  `(*proxy.Proxy).ServeStoppable(proxy.Target{Name, Listen, Upstream string, InjectBaggage map[string]string}) (addr string, stop func(), err error)`,
  `(*proxy.Recorder).Snapshot() []trace.Hop`, `(*proxy.Recorder).Subscribe(cursor uint64) (<-chan trace.Hop, func() uint64, func())`;
  `core/trace` — `trace.NewRedactor(userKeys []string, maxBody int) *trace.Redactor`, `trace.NewWriter(io.Writer) *trace.Writer`;
  `retrace/runs`, `retrace/config`.
- Produces (used by Tasks 12, 13 — every loopback listener in this plan):
  ```go
  package httpguard
  // Handler wraps h with the DNS-rebinding and cross-origin protections a
  // loopback control plane needs. allowedHosts extends the always-allowed
  // loopback literals and "localhost"; the single entry "*" disables
  // host/origin matching (Sec-Fetch-Site is still enforced). Pass nil for
  // a listener that should answer only as loopback.
  func Handler(allowedHosts []string, h http.Handler) http.Handler
  ```
- Produces (used by Tasks 5, 6):
  ```go
  package capture
  type Options struct {
      Cwd       string
      App, Flow string
      Upstream  string        // standalone: base URL of the entry service
      Redact    []string
      MaxBody   int           // 0 → proxy.CaptureLimit
      Now       func() time.Time
  }
  type Session struct {
      Paths     runs.Paths
      RunID     string
      ProxyURL  string    // RETRACE_PROXY_URL
      MarkerURL string    // RETRACE_MARKER_URL
      Mode      string    // runs.ModeStandalone | runs.ModeEnsemble
  }
  func StartStandalone(o Options) (*Session, error)
  func (s *Session) Env() []string          // RETRACE_RUN_DIR/PROXY_URL/MARKER_URL, appendable to exec.Cmd.Env
  func (s *Session) Hops() []trace.Hop      // everything captured so far, oldest first
  func (s *Session) RequestsSeen() int      // every request that reached the proxy or marker door
  func (s *Session) Close() error           // stops listeners, flushes wire.jsonl/hops.jsonl
  func (s *Session) Checkpoints() ([]runs.Checkpoint, error) // reads shots/*.png, decodes geometry
  func NewMarkerDoor(p runs.Paths, now func() time.Time) http.Handler
  ```

**`NewMarkerDoor` takes a `runs.Paths`, not a bare run-dir string — this is
load-bearing, not incidental.** The marker door is exactly the case that
motivated moving `AppendGroupRecord`/`ReadGroupRecords` off a bare `runDir
string` in Task 1's fix round: an HTTP handler that will get
`RETRACE_RUN_DIR` or a request-derived value wired into it by whoever
implements this task next. Typing this parameter as a plain `string` would
reopen exactly the door Task 1 closed, one task later — a caller could
construct the handler with any string, validated or not. Its one caller
(`startMarkerDoor` below) already holds a `runs.Paths` on `Session`, so
there is no loss of information in requiring it here too.

**Design note — why a third env var.** The spec names `RETRACE_RUN_DIR` and
`RETRACE_PROXY_URL`. Those cover file-writing adapters (retrace-js,
retrace-playwright). Maestro cannot write files from a flow; flowlens solved
this with a marker door mounted ON the proxy (`POST /flowlens/group`). Here
the proxy in ensemble-attached mode is ensemble's own session edge listener,
which must stay a pure forwarder — so retrace always opens its own loopback
marker door and exports `RETRACE_MARKER_URL`. **Report this to the spec
owner as an additive extension**; the two spec'd variables remain
authoritative and sufficient for the file-writing adapters.

- [ ] **Step 1: Extract the guard**

Move `ensemble/server/guard.go`'s body into `core/httpguard/guard.go`
verbatim — **including every explanatory comment**, they are the security
rationale — exporting `Handler(allowedHosts []string, h http.Handler)` and
keeping `hostSet`/`newHostSet`/`originAllowed`/`hostOnly` unexported there.
It needs its own error writer (`writeErr`) since it can no longer use the
server package's.

Replace `ensemble/server/guard.go` with:
```go
package server

import (
	"net/http"

	"github.com/caribou-crew/ensemble/core/httpguard"
)

// guard is the shared browser-facing protection, now living in
// core/httpguard so retrace's marker door (Task 4), replay server
// (Task 12) and review server (Task 13) get the identical treatment — the
// rationale (CSRF against an unauthenticated loopback control plane; DNS
// rebinding) applies to every one of them, and a second copy would drift.
func guard(allowedHosts []string, h http.Handler) http.Handler {
	return httpguard.Handler(allowedHosts, h)
}
```
and `server.go:89` becomes `return guard(d.AllowedHosts, mux)`.

Move `ensemble/server/guard_test.go`'s six cases —
`TestGuardRejectsForeignHost`, `TestGuardAllowsLoopbackHostAndOrigin`,
`TestGuardRejectsCrossOriginMutation`, `TestGuardRejectsNullOrigin`,
`TestGuardRejectsCrossSiteFetchMetadata`,
`TestGuardAllowsConfiguredNonLoopbackHost` (verified: those are exactly the
six in the file today) — into `core/httpguard/guard_test.go`, rewritten
against `httpguard.Handler` directly rather than through `server.New`.
Leave ONE integration test behind in `ensemble/server` —
`TestServerRejectsCrossOriginRequests`, a new thin test through
`server.New` — proving the wiring survived the move. **Do not weaken or
re-derive any assertion while moving them:** this step must be a pure
refactor, and the six tests passing unchanged in their new home is the only
evidence of that.

Run: `go test -race ./core/httpguard/ ./ensemble/server/ -v` → PASS.
Commit this step on its own: `refactor(core): extract the Origin/Host guard
into core/httpguard for both products`.

**Why this lives in Task 4 and not later.** The marker door two steps down
is the first loopback listener retrace opens, and the version of this plan
that reviewed clean had it inlining its own copy of the `Sec-Fetch-Site`
check with the comment "same reasoning as `ensemble/server/guard.go`" — a
copy that was **strictly weaker than the original**: no Host check (so DNS
rebinding worked against it) and no Origin check. Extracting first and
consuming immediately means the duplicate is never written. This is a
plan-exceeds-spec refactor (see "Where this plan exceeds the spec"): it is
a pure move, ensemble's behaviour must not change, and the proof is that
ensemble's own guard tests stay green.

- [ ] **Step 2: Write the failing marker-door test**

`retrace/capture/markers_test.go`:
```go
func TestMarkerDoorAppendsStartAndEndRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(NewMarkerDoor(runs.Paths{RunDir: dir}, func() time.Time { return now }))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/group", "application/json", strings.NewReader(`{"name":"checkout"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start marker: %v status=%v", err, resp.StatusCode)
	}
	resp, _ = http.Post(srv.URL+"/group/end", "application/json", strings.NewReader(`{}`))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("end marker status = %d", resp.StatusCode)
	}
	recs, _ := runs.ReadGroupRecords(runs.Paths{RunDir: dir})
	if len(recs) != 2 || recs[0].Name != "checkout" || recs[1].Phase != "end" {
		t.Fatalf("records = %+v", recs)
	}
}

// 400 is the healthy answer here and is what a preflight probe keys on: the
// door exists and refused a nameless marker. Anything else means some OTHER
// server holds the port.
func TestMarkerDoorRejectsAnUnnamedStart(t *testing.T) {
	srv := httptest.NewServer(NewMarkerDoor(runs.Paths{RunDir: t.TempDir()}, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/group", "application/json", strings.NewReader(`{"name":"  "}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// A malformed body must not read as an empty one — otherwise an adapter
// posting garbage looks exactly like an adapter posting nothing.
func TestMarkerDoorRejectsAMalformedBody(t *testing.T) {
	srv := httptest.NewServer(NewMarkerDoor(runs.Paths{RunDir: t.TempDir()}, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/group", "application/json", strings.NewReader(`{"name":`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// The door is loopback-bound, but a page the developer has open can still
// reach 127.0.0.1. This asserts the door is behind httpguard.Handler and
// not a hand-rolled subset of it: cross-site is refused, and so is a Host
// header naming somebody else's domain (the DNS-rebinding case the old
// inlined copy did not cover).
func TestMarkerDoorRejectsCrossSiteAndReboundHosts(t *testing.T) {
	dir := t.TempDir()
	h := NewMarkerDoor(runs.Paths{RunDir: dir}, nil)

	crossSite := httptest.NewRequest("POST", "http://127.0.0.1/group", strings.NewReader(`{"name":"checkout"}`))
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, crossSite)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d, want 403", rec.Code)
	}

	rebound := httptest.NewRequest("POST", "http://127.0.0.1/group", strings.NewReader(`{"name":"checkout"}`))
	rebound.Host = "attacker.example"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rebound)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebound-Host status = %d, want 403", rec.Code)
	}

	if recs, _ := runs.ReadGroupRecords(runs.Paths{RunDir: dir}); len(recs) != 0 {
		t.Fatalf("a rejected request wrote %d marker records", len(recs))
	}
}
```

- [ ] **Step 3: Run — expect FAIL** (`undefined: NewMarkerDoor`).

- [ ] **Step 4: Implement `retrace/capture/markers.go`**

```go
package capture

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/core/httpguard"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// NewMarkerDoor is the HTTP face of flow-part markers, for runners that
// cannot write files (Maestro). Two routes, registered per-method: a
// method-less pattern would panic at registration against any "GET /"
// sibling, and the bare paths are registered explicitly so a POST is never
// answered with a subtree-redirect 301, which drops the body.
func NewMarkerDoor(p runs.Paths, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /group", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name  string `json:"name"`
			Quiet bool   `json:"quiet"`
		}
		// A decode error is reported, not swallowed: a malformed marker body
		// and an empty one are different mistakes, and an adapter that is
		// silently posting garbage would otherwise look like an adapter that
		// is working.
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"marker body is not valid JSON: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			http.Error(w, `{"error":"group markers require a non-empty name"}`, http.StatusBadRequest)
			return
		}
		if err := runs.AppendGroupRecord(p, runs.GroupRecord{
			Phase: "start", Name: body.Name, TS: now(), Quiet: body.Quiet,
		}); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /group/end", func(w http.ResponseWriter, r *http.Request) {
		if err := runs.AppendGroupRecord(p, runs.GroupRecord{Phase: "end", TS: now()}); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// The SAME guard ensemble's control plane uses — not a copy of its
	// Sec-Fetch-Site check. Loopback keeps the network out but not a browser
	// tab, and the door is a state-changing POST endpoint on a predictable
	// port, so it needs the Host (DNS-rebinding) and Origin checks too, not
	// just the cross-site one. nil allowed-hosts = answer only as loopback.
	return httpguard.Handler(nil, mux)
}
```

- [ ] **Step 5: Run — expect PASS.**

- [ ] **Step 6: Write the failing standalone-capture test**

`retrace/capture/capture_test.go`:
```go
func TestStandaloneCaptureRecordsClientEdgeHopsAndWritesWireJsonl(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"token":"secret-value"}`))
	}))
	defer upstream.Close()

	cwd := t.TempDir()
	s, err := StartStandalone(Options{
		Cwd: cwd, App: "web", Flow: "checkout", Upstream: upstream.URL,
		Redact: []string{"token"},
		Now:    func() time.Time { return time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil { t.Fatalf("StartStandalone: %v", err) }

	resp, err := http.Get(s.ProxyURL + "/cart")
	if err != nil { t.Fatalf("through proxy: %v", err) }
	resp.Body.Close()
	if err := s.Close(); err != nil { t.Fatalf("Close: %v", err) }

	hops, skipped, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil || skipped != 0 || len(hops) != 1 { t.Fatalf("wire.jsonl hops = %v (%v)", hops, err) }
	if hops[0].Path != "/cart" || hops[0].Status != 200 {
		t.Fatalf("hop = %+v", hops[0])
	}
	if strings.Contains(hops[0].Resp.Body, "secret-value") {
		t.Fatal("redaction must happen at capture: the plaintext must never reach disk")
	}
	if _, err := os.Stat(s.Paths.HopsPath); !os.IsNotExist(err) {
		t.Fatal("standalone mode must NOT write hops.jsonl — an absent chain and an empty chain are different facts")
	}
}

func TestSessionEnvCarriesTheFullHandshake(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	s, err := StartStandalone(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	env := map[string]string{}
	for _, kv := range s.Env() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, k := range []string{"RETRACE_RUN_DIR", "RETRACE_PROXY_URL", "RETRACE_MARKER_URL"} {
		if env[k] == "" {
			t.Errorf("%s is missing or empty; the handshake is all-or-nothing", k)
		}
	}
	if env["RETRACE_RUN_DIR"] != s.Paths.RunDir {
		t.Errorf("RETRACE_RUN_DIR = %q, want %q", env["RETRACE_RUN_DIR"], s.Paths.RunDir)
	}
}

// Geometry comes from the PNG header, and it is the shot's REAL geometry —
// pre-trim. Trimming is a compare-time decision (Task 7/Task 10); the
// manifest records what was actually captured.
func TestCheckpointsReadsShotGeometryFromPngHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	s, err := StartStandalone(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 40, 40))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Paths.ShotsDir, "cart.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sibling marker file must not be mistaken for a checkpoint.
	if err := os.WriteFile(filepath.Join(s.Paths.ShotsDir, "cart.trim"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cps, err := s.Checkpoints()
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}
	want := []runs.Checkpoint{{Name: "cart", File: "shots/cart.png", Width: 40, Height: 40, Trim: true}}
	if !reflect.DeepEqual(cps, want) {
		t.Fatalf("checkpoints = %+v, want %+v", cps, want)
	}
}
```

- [ ] **Step 7: Run — expect FAIL** (`undefined: StartStandalone`).

- [ ] **Step 8: Implement `retrace/capture/capture.go`**

```go
// Package capture owns the recording half of retrace: it opens (or borrows)
// a listener, points the test command at it through env, and writes the run
// directory. It deliberately does NOT contain a proxy — core/proxy is the
// one interceptor in this repo and this package reuses it, so a hop retrace
// records is byte-identical to a hop ensemble streams.
package capture

import (
	"fmt"
	"image"
	_ "image/png"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

type Options struct {
	Cwd      string
	App      string
	Flow     string
	Upstream string
	Redact   []string
	MaxBody  int
	Now      func() time.Time
}

type Session struct {
	Paths     runs.Paths
	RunID     string
	ProxyURL  string
	MarkerURL string
	Mode      string
	StartedAt time.Time

	rec       *proxy.Recorder
	prox      *proxy.Proxy
	stopProxy func()
	markerSrv *http.Server
	wireFile  *os.File
	requests  atomic.Int64
	closed    bool

	mu           sync.Mutex
	proxyFailure *ProxyFailure
}

// ProxyFailure is the one structured "the interceptor itself misbehaved"
// fact a run can record. Phase is always "running": see Session.ProxyDied.
// Task 6's Assess consumes this type; it is declared HERE, in the task that
// produces it, so package capture has exactly one declaration of it.
type ProxyFailure struct {
	Phase   string `json:"phase"` // always "running" — see ProxyDied
	Message string `json:"message"`
}

func StartStandalone(o Options) (*Session, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	if o.Upstream == "" {
		return nil, fmt.Errorf("standalone capture needs --upstream (the base URL clients would call)")
	}
	runID := runs.NewRunID(now(), gitSHA(o.Cwd))
	p, err := runs.Create(runs.RunsRoot(o.Cwd), o.App, o.Flow, runID)
	if err != nil {
		return nil, err
	}
	wire, err := os.Create(p.WirePath)
	if err != nil {
		return nil, err
	}
	maxBody := o.MaxBody
	if maxBody <= 0 {
		maxBody = proxy.CaptureLimit
	}
	// Redaction at capture: the Recorder scrubs before the ring, before the
	// writer, before anything is streamed. Phase 4b swaps per-key modes in
	// at exactly this seam.
	rec := proxy.NewRecorder(proxy.RecorderOpts{
		Ring:     8192,
		Redactor: trace.NewRedactor(o.Redact, maxBody),
		Writer:   trace.NewWriter(wire),
	})
	prox := proxy.New(rec)
	addr, stop, err := prox.ServeStoppable(proxy.Target{
		Name:          "client-edge",
		Listen:        "127.0.0.1:0",
		Upstream:      strings.TrimRight(o.Upstream, "/"),
		InjectBaggage: map[string]string{trace.BaggageSession: runID},
	})
	if err != nil {
		wire.Close()
		return nil, err
	}

	s := &Session{
		Paths: p, RunID: runID, Mode: runs.ModeStandalone,
		StartedAt: now(), rec: rec, prox: prox, stopProxy: stop, wireFile: wire,
		ProxyURL: "http://" + addr,
	}
	if err := s.startMarkerDoor(now); err != nil {
		stop()
		wire.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) startMarkerDoor(now func() time.Time) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	door := NewMarkerDoor(s.Paths, now)
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		door.ServeHTTP(w, r)
	})
	s.markerSrv = &http.Server{Handler: counted}
	go s.markerSrv.Serve(ln)
	s.MarkerURL = "http://" + ln.Addr().String()
	return nil
}

func (s *Session) Env() []string {
	return []string{
		"RETRACE_RUN_DIR=" + s.Paths.RunDir,
		"RETRACE_PROXY_URL=" + s.ProxyURL,
		"RETRACE_MARKER_URL=" + s.MarkerURL,
	}
}

func (s *Session) Hops() []trace.Hop {
	if s.rec == nil {
		return nil
	}
	return s.rec.Snapshot()
}

// RequestsSeen counts everything that reached retrace at all — proxied calls
// plus markers. Zero of these is proof the app never routed through us,
// which is a different (and much worse) fact than "the flow made no calls".
//
// The nil check is load-bearing, not defensive style. In ensemble-attached
// mode (Task 5) there is no local Recorder at all — ensemble owns the
// listener and retrace drains hops over REST — so s.rec is nil, and
// (*proxy.Recorder).Snapshot takes r.mu.Lock() on its receiver, which
// panics on nil. Task 6 feeds RequestsSeen into Assess on EVERY run, so
// without this guard every ensemble-attached `retrace run` crashes at
// manifest time. Task 5 counts attached traffic into s.requests instead.
func (s *Session) RequestsSeen() int {
	n := int(s.requests.Load())
	if s.rec != nil {
		n += len(s.rec.Snapshot())
	}
	return n
}

func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.stopProxy != nil {
		s.stopProxy()
	}
	if s.markerSrv != nil {
		s.markerSrv.Close()
	}
	return s.wireFile.Close()
}

// ProxyDied records that the client-edge listener stopped answering while
// the test command was still running. This is the ONLY producer of a
// capture.ProxyFailure (Task 6 ranks it `broken/proxy-died`), and it is
// what makes that reason code reachable outside its unit test: a bind
// failure aborts StartStandalone before a manifest exists, so there is no
// such thing as a recorded `proxy-never-started`.
func (s *Session) ProxyDied(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxyFailure == nil {
		s.proxyFailure = &ProxyFailure{Phase: "running", Message: err.Error()}
	}
}

// ProxyFailure returns the recorded running-phase failure, or nil. Task 4's
// run body calls this after the test command exits and passes the result
// into Task 6's Assess.
func (s *Session) ProxyFailure() *ProxyFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxyFailure
}

// WatchProxy polls the client-edge listener while the test command runs. A
// listener that stops accepting is the difference between "the flow made no
// calls" and "the flow's calls went nowhere" — Task 6 must be able to tell
// those apart, and only this loop can tell it.
func (s *Session) WatchProxy(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c, err := net.DialTimeout("tcp", strings.TrimPrefix(s.ProxyURL, "http://"), 200*time.Millisecond)
			if err != nil {
				s.ProxyDied(err)
				return
			}
			c.Close()
		}
	}
}

// Checkpoints reads shots/*.png and decodes each header for geometry. PNG
// dimensions are decoded via image.DecodeConfig, so a 4MB screenshot costs
// a 33-byte read rather than a full decode.
func (s *Session) Checkpoints() ([]runs.Checkpoint, error) {
	entries, err := os.ReadDir(s.Paths.ShotsDir)
	if err != nil {
		return nil, err
	}
	var out []runs.Checkpoint
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			continue
		}
		f, err := os.Open(filepath.Join(s.Paths.ShotsDir, e.Name()))
		if err != nil {
			return nil, err
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("checkpoint %s is not a readable PNG: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		// A `<name>.trim` marker beside the shot means the adapter asked
		// for uniform-border trimming. Record the request; do NOT trim
		// here. Trimming needs retrace/pixel, and capture importing pixel
		// would put a capture → pixel edge in a dependency graph that is
		// deliberately capture → runs, proxy, trace. Width/Height stay
		// pre-trim for the same reason: the manifest reports what was
		// captured, and the compare step reports what it used.
		_, trimErr := os.Stat(filepath.Join(s.Paths.ShotsDir, name+".trim"))
		out = append(out, runs.Checkpoint{
			Name:   name,
			File:   filepath.ToSlash(filepath.Join("shots", e.Name())),
			Width:  cfg.Width,
			Height: cfg.Height,
			Trim:   trimErr == nil,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// gitSHA shells out to git for the run id's provenance suffix. A repo-less
// directory is normal (a user trying retrace in /tmp), so failure is "" and
// NewRunID falls back to "nogit" — never an error that blocks a recording.
func gitSHA(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GitInfo is the manifest's provenance block. Same rule as gitSHA: a
// missing repo is a zero value, never an error — the manifest needs it and
// Task 11's reference-eligibility rules read Git.Dirty.
//
// EXPORTED, unlike gitSHA. gitSHA is called only from inside this package
// (both constructors), but GitInfo is called from runFlow in
// `package main` — same reasoning as WatchProxy below. An unexported
// identifier used across a package boundary is `undefined:` at build time,
// on the only path `retrace run` has.
func GitInfo(dir string) runs.Git {
	g := runs.Git{SHA: gitSHA(dir)}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		g.Branch = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err == nil {
		g.Dirty = strings.TrimSpace(string(out)) != ""
	}
	return g
}
```

Add `os/exec` to the import block above (`gitSHA`/`GitInfo` are its only
users). `GitInfo` gets one test —
`TestGitInfoIsAZeroValueOutsideARepository` — asserting that
`GitInfo(t.TempDir())` returns `runs.Git{}` and does not error: a user
trying retrace in `/tmp` must still get a recording.

- [ ] **Step 9: Run — expect PASS** (`go test -race ./retrace/capture/ -v`).

- [ ] **Step 10: `retrace run` command, standalone path**

`retrace/cmd/retrace/cmd_run.go` — flag set `--flow` (required), `--app`
(default: the config's `app`, else the cwd base name), `--upstream`,
`--json`; everything after `--` is the test command.

```go
func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		flow     = fs.String("flow", "", "flow name to record (required)")
		app      = fs.String("app", "", "app name (default: config app, else the directory name)")
		upstream = fs.String("upstream", "", "standalone: base URL clients would call")
		asJSON   = fs.Bool("json", false, "emit the manifest as JSON on stdout")
	)
	cmdArgs, err := splitDoubleDash(args)   // returns (flagArgs, testCmd []string)
	...
	// This task records the client edge only, so Mode is always standalone
	// and there is no attach decision to make yet.
	sess, err := capture.StartStandalone(capture.Options{ /* … */ })
}
```

**`retrace run` REFUSES to capture when no `retrace.yaml` was found.**
Task 3's `config.Discover` deliberately does not walk up the directory tree
— its inability to reach a parent directory or `~` is a security property,
not a limitation — so running from a subdirectory of a monorepo finds no
config at all. `Discover` then returns a defaulted `Config` whose `Redact`
list is EMPTY, and capture writes **unredacted** hops to disk. Absent
config and permissive config are different meanings, and this is the one
place in the plan where confusing them leaks secrets to a file rather than
mis-gating a diff.

So `cmdRun` checks the flag Task 3 sets on the loaded config (`false` = no
`retrace.yaml` was found, these are synthesized defaults — the zero value
is the unsafe-to-proceed one on purpose) and, when it is false, exits
**2** with a message naming the absolute path it looked for and the
`--no-config` flag that overrides. `--no-config` is declared in this task
and read on this line, so it compiles here. Capturing unredacted traffic
to disk is not a degraded mode that warrants a warning; it is a refusal
with an explicit opt-out.

The test asserts the exit code through a BUILT binary, never `go run` —
see the Global Constraint; `go run` collapses 2 to 1 and the assertion
would pass for the wrong reason.

**`--ensemble` and `--no-ensemble` are deliberately NOT declared here.**
They belong to the attach decision, and every line that could read them —
the health check, `NewClient`, the fallback note — is Task 5's code. A
`flag` result assigned to a local and never read is a compile error in Go
(`declared and not used`), so declaring them one task early would break
this task's own build gate. **Task 5 Step 6 adds both flags and their
readers in the same edit**, which is the only edit in which they can both
exist and be used.

**Step 10b: the run body and manifest assembly, written out.** The earlier
draft of this plan ended this sketch in `...`, and the result was that
`Manifest.Groups` had no writer anywhere in the plan while `diff.Options`
had no source for it — a feature that records markers all the way to disk
and then silently drops them at the last hop, with every unit test still
green. Every manifest field is assigned here, in one place:

```go
// runOptions is what cmdRun has already resolved from flags and config by
// the time it gets here.
type runOptions struct {
	Cwd       string
	App, Flow string
	TestCmd   []string        // everything after "--"
	Stdout    io.Writer
	Stderr    io.Writer
	Now       func() time.Time
}

// runFlow executes the test command against an already-started session and
// returns the assembled manifest. Every Manifest field is set here; if a
// field has no assignment in this function it has no writer at all.
func runFlow(s *capture.Session, o runOptions) (runs.Manifest, error) {
	ctx, cancel := context.WithCancel(context.Background())
	go s.WatchProxy(ctx)          // R12: the only producer of a ProxyFailure

	started := o.Now()
	cmd := exec.CommandContext(ctx, o.TestCmd[0], o.TestCmd[1:]...)
	cmd.Env = append(os.Environ(), s.Env()...)
	cmd.Stdout, cmd.Stderr, cmd.Dir = o.Stdout, o.Stderr, o.Cwd
	runErr := cmd.Run()
	cancel()
	elapsed := o.Now().Sub(started)

	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return runs.Manifest{}, fmt.Errorf("could not run the test command: %w", runErr)
		}
	}

	// Close BEFORE reading anything off disk: it flushes wire.jsonl (and,
	// in attached mode, drains and writes hops.jsonl — see Task 5).
	if err := s.Close(); err != nil {
		return runs.Manifest{}, err
	}

	checkpoints, err := s.Checkpoints()
	if err != nil && !os.IsNotExist(err) {
		return runs.Manifest{}, err
	}

	// Flow-part groups: markers were appended to groups.jsonl by the marker
	// door and by file-writing adapters. THIS is where they stop being a
	// log and become part of the run. Without these three lines the wire
	// diff has no sections and nothing anywhere reports that.
	records, err := runs.ReadGroupRecords(s.Paths)
	if err != nil {
		return runs.Manifest{}, err
	}
	groups := runs.DeriveGroups(records, o.Now())

	// Read back what actually reached disk rather than what the ring
	// happened to hold — Close() has already flushed.
	wireHops, _, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil {
		return runs.Manifest{}, err
	}

	hops := s.Hops()
	trust := assessTrust(s, hops, checkpoints, groups, exitCode)

	m := runs.Manifest{
		Schema:      runs.Schema,
		App:         o.App,
		Flow:        o.Flow,
		RunID:       s.RunID,
		Mode:        s.Mode,
		Git:         capture.GitInfo(o.Cwd),
		StartedAt:   started,
		FinishedAt:  o.Now(),
		Checkpoints: checkpoints,
		Groups:      groups,
		Capture:     trust,
		Wire:        runs.Counts{Calls: len(wireHops), Recorded: true},
		Test:        runs.Test{Command: strings.Join(o.TestCmd, " "), ExitCode: exitCode, DurationMs: float64(elapsed.Milliseconds())},
		// Retrace is the recording binary's own version, from main.version
		// (the `var version = "dev"` in main.go, stamped by -ldflags at
		// release). It is replay-compatibility provenance: "which retrace
		// wrote this bundle" is the first question asked when a reference
		// recorded months ago stops replaying, and it cannot be
		// reconstructed after the fact.
		Env: runs.Env{
			Go:       runtime.Version(),
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Retrace:  version,
		},
	}
	// Hops is a *Counts on purpose: nil means "standalone, no chain was
	// recorded", present-and-zero means "the chain was recorded and was
	// empty". Task 5 sets it; standalone leaves it nil.
	if s.Mode == runs.ModeEnsemble {
		m.Hops = &runs.Counts{Calls: len(hops), Recorded: true}
	}
	return m, runs.WriteManifest(s.Paths, &m)
}
```

`WatchProxy` is the exported name of the proxy-liveness loop from Step 8 —
the run body lives in `package main`, so declare it exported there rather
than as `watchProxy`.

`assessTrust` is the **seam Task 6 fills**. Capture-trust rules are Task 6's
subject, and `capture.Assess` does not exist yet, so this task ships the
seam and a deliberately honest placeholder — not an `ok`, which would be a
manifest claiming a clean capture nobody checked:

```go
// TODO(task-6): replace this body with capture.Assess. Task 6 owns the
// rules; this task owns only the call site and the manifest field.
func assessTrust(s *capture.Session, hops []trace.Hop, cps []runs.Checkpoint,
	groups []runs.Group, exitCode int) runs.CaptureTrust {
	return runs.CaptureTrust{
		Status:  trace.VerdictSuspect,
		Summary: "capture-trust not assessed yet — see Task 6",
	}
}
```

Task 6 Step 5 replaces the body with the real `capture.Assess` call,
passing `s.ProxyFailure()`, `s.RequestsSeen()`, `len(cps)`, the previous
run's checkpoint count, and `quietOnly(groups)` — a three-line filter on
`g.Quiet`. Keeping the signature stable means Task 6 changes one function
body and nothing else.

Then: print the summary, or `--json` the manifest, and exit with the test
command's code.

- [ ] **Step 11: CLI test**

`retrace/cmd/retrace/cmd_run_test.go`:
`TestRunStandaloneRecordsAndWritesAManifest` — spins an httptest upstream,
runs `run --flow checkout --app web --upstream <url> -- <a tiny "go
run"-free command>` (no `--no-ensemble`: that flag arrives with the attach
decision in Task 5, and standalone is this task's only mode). Use `sh -c 'curl -s "$RETRACE_PROXY_URL/cart" >
/dev/null'`… **no**: `curl` may be absent. Instead the test command is the
test binary re-invoked with a helper env var (`os.Args[0] -test.run
TestHelperFetchesThroughProxy`), the standard Go idiom, so the test has no
external tool dependency.
Assert: exit code 0, `manifest.json` exists, `mode == "standalone"`,
`test.exitCode == 0`, `wire.calls == 1`.
`TestRunRequiresFlow` — no `--flow` → exit 3 with a message naming the flag.
`TestRunPropagatesTheTestCommandsExitCode` — helper exits 7 → `retrace run`
exits 7 (a failing test must fail the pipeline; retrace's own exit codes only
apply when the command itself succeeded).
`TestRunFoldsMarkersIntoManifestGroups` — the helper posts two `/group`
markers to `$RETRACE_MARKER_URL` and one `/group/end`; assert
`manifest.Groups` has both names, in start order, with the first closed at
the second's start. **This is the test that keeps flow-part groups from
being a write-only feature**: markers reached `groups.jsonl` in the earlier
draft and stopped there, so the wire diff's sections silently collapsed to
one unnamed section on every real run while every unit test stayed green.
Task 10 has the matching assertion at the reading end.

- [ ] **Step 12: Full suites + commit**

```bash
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/capture retrace/cmd/retrace
git commit -m "feat(retrace): standalone capture, marker door, env handshake, retrace run"
```

---

### Task 5: Ensemble-attached capture — session registration, wire/hops split, drain race

**Files:**
- Create: `retrace/cmd/retrace/client.go`
- Create: `retrace/capture/ensemble.go`, `retrace/capture/ensemble_test.go`
- Create: `retrace/cmd/retrace/cmd_run_ensemble_test.go`
- Modify: `retrace/cmd/retrace/cmd_run.go` (attach path)
- **Not modified: `retrace/go.mod`.** This task adds no module dependency —
  see Step 7.

**Interfaces:**
- Consumes: ensemble's control plane, exactly as `ensemble/server/routes.go`
  serves it:
  `GET /api/health` → `{"ok":true,"version":"..."}`;
  `POST /api/sessions` body `{"id":"<runId>","entry":"<service>"}` →
  `{"id":"...","edgeAddr":"127.0.0.1:PORT"}` (409 when the id is active,
  404 unknown entry, 400 entry has no proxy port);
  `GET /api/sessions/{id}/hops` → **NDJSON of `trace.Hop`**, not JSON;
  `DELETE /api/sessions/{id}` → `{"id","hops":N,"verdict","reasons":[...]}`.
- Produces (used by Task 6 and cmd_run):
  ```go
  package capture
  type EnsembleClient interface {
      Health(ctx context.Context) error
      StartSession(ctx context.Context, id, entry string) (edgeAddr string, err error)
      SessionHops(ctx context.Context, id string) ([]trace.Hop, error)
      EndSession(ctx context.Context, id string) (EndReport, error)
  }
  // EndReport is DECODED from ensemble's `DELETE /api/sessions/{id}`
  // response, so it is a wire type too — inbound rather than outbound.
  // It happens to work untagged because encoding/json matches field names
  // case-insensitively, which makes this the most dangerous kind of
  // missing tag: correct today, and silently wrong the day ensemble
  // renames a field to something that no longer case-folds onto ours.
  type EndReport struct {
      Hops    int           `json:"hops"`
      Verdict trace.Verdict `json:"verdict"`
      Reasons []string      `json:"reasons"`
  }
  func StartAttached(o Options, c EnsembleClient, entry string) (*Session, error)
  func (s *Session) Drain(ctx context.Context) error  // attached only; no-op standalone
  ```
  `retrace/cmd/retrace/client.go` provides `type Client struct{ BaseURL string; HTTP *http.Client }`
  implementing `capture.EnsembleClient`, modeled on
  `ensemble/cmd/ensemble/client.go` (same "the CLI is just another API
  consumer" discipline).

**The race this task exists to prevent (write the test FIRST).** Hops are
recorded at *completion*, and `SessionManager.route` drops a hop whose
session has already ended ("session already ended; late hop is dropped").
So the naive order — command exits, `DELETE /api/sessions/{id}`, then read
hops — silently loses every in-flight downstream call. The correct order is:
command exits → **drain** (poll `GET /api/sessions/{id}/hops` until the
count is stable across two consecutive polls, ~100ms apart, capped at 2s) →
write `hops.jsonl` → `DELETE` → compare `EndReport.Hops` with what was
written and, if the server counted more, degrade the capture-trust verdict
to `suspect` with reason `"N hop(s) arrived after the drain window"`.

- [ ] **Step 1: Write the failing drain test**

**No sleeps in this test.** This is the plan's flagship "write the race test
first" task, and the first draft of it synchronised a goroutine with
`time.Sleep(150ms)` against a 100 ms poll and a 2 s cap — a timing-dependent
test for a timing bug, which flakes on a loaded CI box in *both* directions
(false green and false red). The fake injects the late hop **on the first
poll, from inside the poll**, which is the same causal ordering as reality
(a downstream call completing after the command exited) with none of the
timing. Every shared field is guarded by the same mutex: the task's own
commit gate is `go test -race`.

`retrace/capture/ensemble_test.go`:
```go
// fakeEnsemble stands in for ensemble's control plane. It reproduces the
// ordering this task exists to defend against — a hop that lands after the
// test command exits — deterministically, by appending `late` during the
// first SessionHops call rather than after a wall-clock delay.
type fakeEnsemble struct {
	mu        sync.Mutex
	hops      []trace.Hop
	late      *trace.Hop // injected on the first poll, then cleared
	polls     int
	endCalled bool
	hopsAtEnd int // len(hops) when EndSession was called — the ordering assertion
}

func (f *fakeEnsemble) push(h trace.Hop) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hops = append(f.hops, h)
}

func (f *fakeEnsemble) Health(context.Context) error { return nil }

func (f *fakeEnsemble) StartSession(_ context.Context, id, entry string) (string, error) {
	return "127.0.0.1:0", nil
}

// SessionHops takes the id the interface declares, and returns a COPY: the
// caller keeps the slice past the lock, so handing out the backing array
// would be a data race the -race gate catches on a good day and misses on
// a bad one.
func (f *fakeEnsemble) SessionHops(_ context.Context, id string) ([]trace.Hop, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	if f.polls == 1 && f.late != nil {
		f.hops = append(f.hops, *f.late)
		f.late = nil
	}
	return append([]trace.Hop(nil), f.hops...), nil
}

// EndSession records how many hops existed at teardown. Everything it
// touches is under f.mu — the earlier draft wrote f.ended lock-free while
// a goroutine appended to f.hops under the lock, which is a data race in
// the one task gated on -race.
func (f *fakeEnsemble) EndSession(_ context.Context, id string) (EndReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endCalled, f.hopsAtEnd = true, len(f.hops)
	return EndReport{Hops: len(f.hops), Verdict: trace.VerdictOK}, nil
}

func TestDrainWaitsForLateHopsBeforeEndingTheSession(t *testing.T) {
	late := hop(2, "catalog")
	f := &fakeEnsemble{late: &late}
	f.push(hop(1, "edge"))

	s := attachedSessionFor(t, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	hops, _, _ := runs.ReadHops(s.Paths.HopsPath)
	if len(hops) != 2 {
		t.Fatalf("Drain must not end the session before late hops land: got %d, want 2", len(hops))
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// The ordering, asserted directly rather than inferred from the count:
	// ensemble's SessionManager drops hops for a session it has already
	// ended, so EndSession must observe the fully drained state.
	if !f.endCalled || f.hopsAtEnd != 2 {
		t.Fatalf("EndSession saw %d hop(s) (called=%v); it must run AFTER the drain", f.hopsAtEnd, f.endCalled)
	}
	// Stability needs two agreeing polls; one poll would mean the loop
	// stopped at the first answer it got.
	if f.polls < 2 {
		t.Fatalf("polls = %d; the drain must confirm stability across two polls", f.polls)
	}
}

func TestHopsArrivingAfterTheDrainWindowDegradeTheVerdict(t *testing.T) {
	// A fake whose EndReport.Hops is 3 while only 2 were ever served →
	// Session.trustNotes records "1 hop(s) arrived after the drain window
	// and are missing from this recording" and the capture status is at
	// least suspect. No sleeps: the shortfall is a value the fake returns.
}

func TestWireJsonlIsTheClientEdgeSubsetOfHopsJsonl(t *testing.T) {
	// hops.jsonl: [{From:"", To:"edge"}, {From:"edge", To:"bff"}]
	// wire.jsonl: only the From=="" hop — a client-edge call is one whose
	// caller is not a service we proxy.
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: StartAttached`).

- [ ] **Step 3: Implement `retrace/capture/ensemble.go`**

```go
package capture

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// drainPoll/drainWindow bound the wait for hops that complete after the
// test command exits. Nested calls are RECORDED inner-first but they all
// have to finish before the outermost one does, so "the count stopped
// changing" is a sound stop condition — and the cap keeps a wedged upstream
// from hanging CI.
const (
	drainPoll   = 100 * time.Millisecond
	drainWindow = 2 * time.Second
)

func StartAttached(o Options, c EnsembleClient, entry string) (*Session, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	runID := runs.NewRunID(now(), gitSHA(o.Cwd))
	p, err := runs.Create(runs.RunsRoot(o.Cwd), o.App, o.Flow, runID)
	if err != nil {
		return nil, err
	}
	edge, err := c.StartSession(context.Background(), runID, entry)
	if err != nil {
		return nil, fmt.Errorf("register session with ensemble: %w", err)
	}
	s := &Session{
		Paths: p, RunID: runID, Mode: runs.ModeEnsemble, StartedAt: now(),
		ProxyURL: "http://" + edge, ens: c,
	}
	if err := s.startMarkerDoor(now); err != nil {
		_, _ = c.EndSession(context.Background(), runID)
		return nil, err
	}
	return s, nil
}

// Drain polls until the hop count is stable across two consecutive polls or
// the window expires, then snapshots. It must run BEFORE EndSession:
// ensemble's SessionManager drops hops for a session it no longer knows.
func (s *Session) Drain(ctx context.Context) error {
	if s.ens == nil {
		return nil // standalone: our own recorder already has everything
	}
	deadline := time.Now().Add(drainWindow)
	last := -1
	for {
		hops, err := s.ens.SessionHops(ctx, s.RunID)
		if err != nil {
			return err
		}
		if len(hops) == last {
			s.hops = hops
			return nil
		}
		last = len(hops)
		s.hops = hops
		if time.Now().After(deadline) {
			s.trustNotes = append(s.trustNotes,
				fmt.Sprintf("hops were still arriving when the %s drain window expired — the recording may be truncated", drainWindow))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(drainPoll):
		}
	}
}

// Close writes hops.jsonl and wire.jsonl, then ends the session and
// reconciles the counts. Everything on disk goes through a Redactor first:
// ensemble redacted on capture, but a recording is committed and shared, so
// retrace re-applies its OWN configured key list rather than trusting the
// producer's.
func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.markerSrv != nil {
		s.markerSrv.Close()
	}
	if s.ens == nil {
		s.stopProxy()
		return s.wireFile.Close()
	}

	red := trace.NewRedactor(s.redact, s.maxBody)
	written := 0
	if err := writeHops(s.Paths.HopsPath, s.hops, red, func(trace.Hop) bool { return true }, &written); err != nil {
		return err
	}
	wire := 0
	if err := writeHops(s.Paths.WirePath, s.hops, red, isClientEdge, &wire); err != nil {
		return err
	}

	rep, err := s.ens.EndSession(context.Background(), s.RunID)
	if err != nil {
		s.trustNotes = append(s.trustNotes, "ending the ensemble session failed: "+err.Error())
		return nil // the recording is already on disk; never lose it over a teardown error
	}
	s.endReport = rep
	if rep.Hops > written {
		s.trustNotes = append(s.trustNotes,
			fmt.Sprintf("%d hop(s) arrived after the drain window and are missing from this recording", rep.Hops-written))
	}
	return nil
}

// isClientEdge selects the hops wire.jsonl holds: those whose caller is not
// a service ensemble proxies. core/proxy fills Hop.From from the recorder's
// span-owner map, so From == "" means "a client, or an unproxied caller" —
// exactly the client edge.
func isClientEdge(h trace.Hop) bool { return h.From == "" }

func writeHops(path string, hops []trace.Hop, red *trace.Redactor, keep func(trace.Hop) bool, n *int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := trace.NewWriter(f)
	for _, h := range hops {
		if !keep(h) {
			continue
		}
		if err := w.Write(red.Hop(h)); err != nil {
			return err
		}
		*n++
	}
	return nil
}
```

Add the fields this uses to `Session`: `ens EnsembleClient`, `hops
[]trace.Hop`, `endReport EndReport`, `trustNotes []string`, `redact
[]string`, `maxBody int`. Set `redact`/`maxBody` in BOTH constructors.

**Methods this task changes, all of them.** `StartAttached` leaves
`s.rec` and `s.stopProxy` nil — ensemble owns the listener — so every
method that touches them must tolerate an attached session. Miss one and
it panics on every attached run, not on an edge case:

| Method | Attached behaviour |
|---|---|
| `Hops()` | return `s.hops` (the drained slice), not `s.rec.Snapshot()` |
| `RequestsSeen()` | the atomic counter alone — see the nil guard in Task 4 |
| `Close()` | nil-check `s.stopProxy` before calling it |
| `ProxyFailure()` | always nil: retrace does not own the listener, so it cannot witness it dying |

Also add three accessors over the fields above, which Task 6's
`assessTrust` reads and nothing else may reach into directly:
`EndVerdict() trace.Verdict` and `EndReasons() []string` (from
`s.endReport`, zero values before `Close`), and `TrustNotes() []string`
(a copy of `s.trustNotes`).

`RequestsSeen` in attached mode counts marker-door hits only, and that is
the honest number: proxied requests reach *ensemble's* edge listener and
never touch retrace, so retrace has nothing else it could truthfully
count. It still separates the two cases Task 6 needs separated — a flow
that posted markers but recorded no calls is a different (and less
alarming) fact than a flow that produced neither.

- [ ] **Step 4: Run — expect PASS** (`go test -race ./retrace/capture/ -run Drain -v`).

- [ ] **Step 5: Implement `retrace/cmd/retrace/client.go`**

A `Client` with `Health`, `StartSession`, `SessionHops`, `EndSession`.
`SessionHops` must read **NDJSON**, not a JSON array — use
`trace.NewReader(resp.Body)` and loop to `trace.ErrEOF`. Errors follow the
server's `{"error":"..."}` convention: non-2xx → `fmt.Errorf("%s %s: %s",
method, path, body.Error)`.

- [ ] **Step 6: Wire the attach path into `cmdRun` — flags and readers in
  one edit**

Add BOTH flags here, not in Task 4. Task 4 could not declare them: a
`flag` result bound to a local and never read is `declared and not used`,
a compile error, and every possible reader is in the block below.

```go
// New in this task, alongside the code that reads them:
ensembleURL := fs.String("ensemble", envOr("ENSEMBLE_API", "http://127.0.0.1:4700"), "ensemble control-plane URL")
noEnsemble  := fs.Bool("no-ensemble", false, "force standalone capture even if ensemble is up")

// Attach when ensemble answers AND the config names an entry service.
// Anything else falls back to standalone with an explicit stderr note —
// silently recording less than the user asked for is how a "the app made
// no calls" report gets believed.
mode := runs.ModeStandalone
if !*noEnsemble && cfg.Entry != "" {
	c := NewClient(*ensembleURL)
	if err := c.Health(ctx); err == nil {
		sess, err = capture.StartAttached(opts, c, cfg.Entry)
		mode = runs.ModeEnsemble
	} else {
		fmt.Fprintf(stderr, "retrace: ensemble at %s is not answering (%v) — recording the client edge only\n", *ensembleURL, err)
	}
}
```

- [ ] **Step 7: Integration test over real HTTP — against a fake control
  plane, NOT the ensemble module**

**Ruling, and it is a hard constraint: `retrace` must not import
`ensemble`.** Not in production code and not in a test. Design §1 argues
that a team can adopt retrace in CI without ever running ensemble, and a
test-only import is still a `require` + `replace` in `retrace/go.mod` —
which would also make Task 3's `go mod tidy` step try to fetch an
unpublished module. The dependency direction stays
`retrace → core`, `ensemble → core`, and the two products never see each
other.

What this test must still prove is that `client.go` speaks ensemble's wire
contract correctly — which is an HTTP-level property, so an HTTP-level
fake proves it. `retrace/cmd/retrace/cmd_run_ensemble_test.go`:

```go
// ensembleAPI is an httptest server that answers exactly the four routes
// retrace uses, in exactly the shapes ensemble/server/routes.go serves
// them. The shapes are pinned in this plan's Task 5 Interfaces block and
// were verified against routes.go; if ensemble ever changes them, THIS is
// the test that must be updated, deliberately, rather than a compile error
// telling us after we have already coupled the modules.
func ensembleAPI(t *testing.T, hops []trace.Hop) *httptest.Server
```
It serves `GET /api/health` → `{"ok":true,"version":"test"}`;
`POST /api/sessions` → `{"id":...,"edgeAddr":<a real httptest edge addr>}`;
`GET /api/sessions/{id}/hops` → **NDJSON** written with `trace.NewWriter`
(the encoder ensemble itself uses, so the bytes are not hand-rolled);
`DELETE /api/sessions/{id}` → `{"id":...,"hops":N,"verdict":"ok","reasons":[]}`.

`TestRunAttachedRecordsTheFullChainAndSplitsWireFromHops` — two hops
sharing one traceId, one with `From: ""` (the client edge) and one with
`From: "edge"`; point `retrace run` at the fake; assert `hops.jsonl` has
both, `wire.jsonl` has only the `From == ""` hop, and `manifest.mode` is
`"ensemble"`. This is the core-trace-model spec's "one schema, two
consumers" scenario, executed end to end over real HTTP.

`TestSessionHopsParsesNdjsonNotAJsonArray` — the single most likely
integration bug, and the one a hand-written fake could paper over: feed
three `trace.Hop` records written by `trace.NewWriter` (blank line
included, which `trace.NewReader` skips) and assert the client returns
three hops. A client written against `json.Unmarshal` of an array fails
here, which is the point.

**Verify before committing:** `retrace/go.mod` still requires only `core`.
If a future step genuinely cannot be tested without the real
`ensemble/server`, stop and raise it — adding the module edge is the
controller's decision, not the implementer's.

- [ ] **Step 8: Full suites + commit**

```bash
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/capture retrace/cmd/retrace
git commit -m "feat(retrace): ensemble-attached capture with drain-before-end hop reconciliation"
```

---

### Task 6: Capture-trust verdict

**Files:**
- Create: `retrace/capture/trust.go`, `retrace/capture/trust_test.go`
- Modify: `retrace/cmd/retrace/cmd_run.go` (assess + banner + manifest)

**Interfaces:**
- Consumes: `runs.CaptureTrust`, `runs.TrustReason`, `runs.Gap`, `runs.Group`;
  `trace.Verdict` + `(trace.Verdict).Worse`.
- Produces (used by Tasks 10, 11, 13, 16):
  ```go
  package capture
  const DefaultGapThreshold = 60 * time.Second   // 60s; Step 3 argues the value
  // ProxyFailure is DECLARED IN TASK 4 (retrace/capture/capture.go), the
  // only thing that produces one. Task 6 consumes it and must not
  // re-declare it — same package, one declaration.
  //   type ProxyFailure struct { Phase, Message string }  // Phase: "running"
  type AssessInput struct {
      // NO ProxyConfigured field. An earlier draft had one; its zero value
      // (false) skipped the whole reachability branch, so Assess(AssessInput{})
      // returned "ok"/"capture looks complete" — the zero value reading as
      // fine, and worse than the empty Status, because "ok" sails through both
      // manifest seams AND past Task 10's quarantine while "" is rejected.
      // Its only call site hardcoded true, so it carried no information and
      // only risk. Do not reintroduce it. If a mode ever genuinely has no
      // proxy, add ProxyNotConfigured so the zero value stays protective.
      ProxyFailure        *ProxyFailure
      Hops                []trace.Hop
      Checkpoints         int
      ExpectedCheckpoints int   // -1 = no history to compare against
      TestExitCode        int
      RequestsSeen        int   // -1 = unknown
      Quiet               []runs.Group
      GapThreshold        time.Duration
      SessionVerdict      trace.Verdict
      SessionReasons      []string
      Notes               []string  // Session.trustNotes (drain shortfall, teardown failure)
  }
  func Assess(in AssessInput) runs.CaptureTrust
  func Fatal(c runs.CaptureTrust) bool   // broken | degraded | failed
  func FindGaps(hops []trace.Hop, threshold time.Duration, quiet []runs.Group) []runs.Gap
  ```

**`RequestsSeen` is inflated, and this task owns the rule that reads it.**
Task 4's re-review measured what the counter actually counts: `onAdmitted`
fires for everything the *mux* rejects after the guard admits it — the
nameless-marker 400 that this plan's own preflight probe sends, a
malformed-body 400, `GET /` → 404, a 405, a 301. It correctly excludes
everything the **guard** rejects (verified across an 18-case matrix: never
fires on cross-site, rebinding-Host, cross-Origin or null-Origin 403s), so
the security-relevant half is right. But `markers.go`'s doc comment claims
mux-rejected requests "must never count", and that claim is false.

The consequence lands here, not there: `RequestsSeen() == 0` is the rule
this task uses to decide that nothing reached retrace at all, and a run
where the preflight probe was the only traffic reports `1`. So **do not
treat `RequestsSeen() > 0` as proof that real traffic flowed.** Either
have Task 6 discount the known-probe requests, or move the counter to the
handler bodies so it counts only requests that did something — this task
is where that choice belongs, because this task is the first consumer and
the shape of `AssessInput` is its call. Task 4 deliberately left the seam
alone rather than pre-empting this decision.

Pin whichever you choose with a test that fails if the discount is
removed. This interacts with `ProxyFailure` deliberately: they were the
two halves of a single guard against "the proxy died and nobody noticed",
and the re-review found that leaning on either alone fails on the same
input.

**In attached mode `RequestsSeen` is legitimately zero, and reading that as
zero would be a false accusation.** Task 5 shipped ensemble-attached
capture, where proxied requests reach *ensemble's* edge listener and never
touch retrace at all — so retrace can only ever count marker-door hits.
A perfectly healthy attached run therefore reports 0, and the table above
maps 0 to `VerdictBroken` / `proxy-never-reached`. That verdict would be
wrong, loudly, on every attached run whose flow happened to record no
calls.

The `-1 = unknown` sentinel exists for exactly this, and it is why the
field is not a plain count: **attached mode must pass `-1`, never `0`.**
Zero means "we counted, and nothing arrived"; `-1` means "this mode does
not count". Collapsing them is the zero-value trap in its purest form — the
absent measurement reading as a damning one — and note it fails in the
opposite direction from the inflation problem above: one makes a dead proxy
look alive, this one makes a live proxy look dead. A single field carries
both hazards, which is why the rule for setting it belongs in one place.

Two agents reached this independently — Task 5's implementer while building
attached capture, and Task 5's reviewer while re-deriving the drain race —
before this task was dispatched. Treat it as established, not speculative.
Add a case to the table: attached mode, zero calls, `RequestsSeen: -1`,
which must NOT produce `proxy-never-reached`.

**This task's verdict is what Task 10's quarantine reads.** Comparing two
captures when one of them is already known-broken produces confident
nonsense — a diff against a `broken` reference does not mean "identical",
it means "nobody checked". Task 10 quarantines a side from `retrace diff`
by default whenever `runs.CaptureTrust.Status != trace.VerdictOK` for that
side, and reports which side and why. That check is **broader than
`Fatal`**: `Fatal` exists to answer a different question ("should this
stop a promotion or a CI build outright") and deliberately excludes
`suspect` so a heuristic gap-detector does not flood false alarms — but a
`suspect` run is still not a run Task 10 should silently diff as if it
were clean, so quarantine keys on the raw `Status`, not on `Fatal(c)`.
Nothing changes here: `Assess` already produces `Status` and a human
`Summary` string for every case above, and that is all Task 10 needs.

- [ ] **Step 1: Write the failing trust test** — one subtest per reason code,
  ported from flowlens `src/capture-health.mjs`:

```go
func TestAssessRanksTheWorstEvidence(t *testing.T) {
	cases := []struct {
		name string
		in   AssessInput
		want trace.Verdict
		code string
	}{
		{"clean run", AssessInput{Hops: hops(3), Checkpoints: 2, RequestsSeen: 3}, trace.VerdictOK, ""},
		{"failed test outranks everything", AssessInput{TestExitCode: 1, Hops: hops(3), RequestsSeen: 3}, trace.VerdictFailed, "test-failed"},
		{"proxy died mid-run", AssessInput{ProxyFailure: &ProxyFailure{Phase: "running", Message: "closed"}, Hops: hops(1), RequestsSeen: 1}, trace.VerdictBroken, "proxy-died"},
		{"zero calls AND zero requests", AssessInput{RequestsSeen: 0}, trace.VerdictBroken, "proxy-never-reached"},
		{"zero calls but requests seen", AssessInput{RequestsSeen: 4}, trace.VerdictDegraded, "no-calls"},
		{"zero calls, reachability unknown", AssessInput{RequestsSeen: -1}, trace.VerdictDegraded, "no-calls"},
		{"screenshots vanished", AssessInput{Hops: hops(2), RequestsSeen: 2, Checkpoints: 0, ExpectedCheckpoints: 5}, trace.VerdictDegraded, "no-screenshots"},
		{"ensemble reported a propagation gap", AssessInput{Hops: hops(2), RequestsSeen: 2, SessionVerdict: trace.VerdictDegraded, SessionReasons: []string{"propagation gap at bff: traceparent forwarded but baggage dropped before catalog"}}, trace.VerdictDegraded, "propagation-gap"},
		{"drain shortfall", AssessInput{Hops: hops(2), RequestsSeen: 2, Notes: []string{"1 hop(s) arrived after the drain window"}}, trace.VerdictSuspect, "capture-note"},
	}
	// each: Assess(in).Status == want, and want=="" or a reason with that Code exists
}

// A flow that declared "I am waiting for a push notification" explained its
// own silence; an undeclared 120s hole did not.
func TestFindGapsSubtractsDeclaredQuietIntervals(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	hops := []trace.Hop{
		{T: trace.Timings{Start: t0}},
		{T: trace.Timings{Start: t0.Add(120 * time.Second)}},
	}
	quiet := []runs.Group{{
		Name: "await-push", Quiet: true,
		StartedAt: t0.Add(10 * time.Second), EndedAt: t0.Add(100 * time.Second),
	}}

	if gaps := FindGaps(hops, 60*time.Second, quiet); len(gaps) != 0 {
		t.Fatalf("a declared quiet interval must not read as a gap: %+v", gaps)
	}
	gaps := FindGaps(hops, 60*time.Second, nil)
	if len(gaps) != 1 || gaps[0].Seconds != 120 {
		t.Fatalf("gaps = %+v, want one 120s gap", gaps)
	}
}

// A wire-only flow captures no screenshots by design. Nagging about it
// every run is how a real warning gets tuned out.
func TestAWireOnlyFlowIsNeverNaggedAboutScreenshots(t *testing.T) {
	got := Assess(AssessInput{
		Hops: hops(1), RequestsSeen: 1,
		Checkpoints: 0, ExpectedCheckpoints: 0,
	})
	if got.Status != trace.VerdictOK {
		t.Fatalf("status = %s (%s), want ok", got.Status, got.Summary)
	}
}

// Blaming the capture for a test that fell over early points at the wrong
// thing: the screenshots are missing BECAUSE the test died.
func TestNoScreenshotReasonIsSuppressedWhenTheTestFailed(t *testing.T) {
	got := Assess(AssessInput{
		Hops: hops(1), RequestsSeen: 1,
		Checkpoints: 0, ExpectedCheckpoints: 3, TestExitCode: 1,
	})
	if got.Status != trace.VerdictFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	for _, r := range got.Reasons {
		if r.Code == "no-screenshots" {
			t.Fatalf("no-screenshots must be suppressed when the test failed: %+v", got.Reasons)
		}
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: Assess`).

- [ ] **Step 3: Implement `retrace/capture/trust.go`** — port
  `assessCapture` reason-for-reason. Key structure:

```go
// DefaultGapThreshold is how long a stretch with no captured call has to be
// before it counts as evidence, when AssessInput.GapThreshold is unset.
//
// Sixty seconds, and the value is a judgement, so here is the judgement: a
// human-driven mobile or web flow that goes a full minute without a single
// call has almost always stopped routing through the proxy — real think
// time, animations, retries and polling all land far under it — while the
// legitimate long pauses (waiting on a push notification, an OTP, a
// third-party redirect) are exactly the ones a flow can declare with a
// `quiet` group and have subtracted. Lower and every deliberate wait
// becomes a false `suspect`; higher and a proxy that died mid-run reads as
// clean.
//
// Measured BETWEEN consecutive calls only, never against the run's own
// start/end: the app launching before its first call, and teardown after
// the last, are normal and would fire on every run.
//
// Named `Default…` to match the other zero-value fallbacks in this plan
// (`DefaultCountTolerance`, `config.DefaultGate`, `config.DefaultFine`),
// and there is exactly ONE declaration of this number.
const DefaultGapThreshold = 60 * time.Second

func Assess(in AssessInput) runs.CaptureTrust {
	threshold := in.GapThreshold
	if threshold <= 0 {
		threshold = DefaultGapThreshold
	}
	gaps := FindGaps(in.Hops, threshold, in.Quiet)
	var reasons []runs.TrustReason
	add := func(code string, st trace.Verdict, detail, hint string) {
		reasons = append(reasons, runs.TrustReason{Code: code, Status: st, Detail: detail, Hint: hint})
	}

	// The P0 flowlens shipped with: a failed test used to leave the status
	// at its 'ok' default, so a run that verified nothing read as clean.
	// `failed` outranks broken/degraded deliberately — those mean the
	// capture machinery misbehaved though the test may have passed; a failed
	// test means nothing was verified at all.
	if in.TestExitCode != 0 {
		add("test-failed", trace.VerdictFailed,
			"capture not verified — the test failed, so nothing here was checked",
			"fix the failing test and re-run; a failed test proves nothing about the capture")
	}

	// There is deliberately no `proxy-never-started` reason. A bind failure
	// aborts StartStandalone before a run directory or a manifest exists,
	// so a recording can never carry it — a reason code only its own unit
	// test can reach is a reason code that lies about coverage. The one
	// producer of a ProxyFailure is Session.ProxyDied, and it always sets
	// Phase "running".
	if in.ProxyFailure != nil {
		add("proxy-died", trace.VerdictBroken,
			"the capture listener stopped during the run: "+in.ProxyFailure.Message,
			"re-run — calls made after it stopped were never recorded")
	}

	// Zero calls is ambiguous on its own: "genuinely quiet" and "the app
	// never routed through us" look identical. RequestsSeen (markers
	// included, a strictly broader count) tells them apart; -1 means we
	// could not verify, which must say so rather than read as either.
	if in.ProxyFailure == nil && len(in.Hops) == 0 && in.TestExitCode == 0 {
		if in.RequestsSeen == 0 {
			add("proxy-never-reached", trace.VerdictBroken,
				"zero calls AND zero requests of any kind reached retrace — the app almost certainly never routed through it",
				"confirm the app's base URL uses $RETRACE_PROXY_URL before trusting anything else here")
		} else if in.RequestsSeen > 0 {
			add("no-calls", trace.VerdictDegraded,
				fmt.Sprintf("the test passed and %d request(s) reached retrace, but zero calls were recorded", in.RequestsSeen),
				"check the app's base URL actually points at $RETRACE_PROXY_URL")
		} else {
			add("no-calls", trace.VerdictDegraded,
				"the test passed but zero calls were recorded, and whether retrace was reached at all could not be verified — treat this zero as unknown, not confirmed clean",
				"check the app's base URL actually points at $RETRACE_PROXY_URL")
		}
	}

	if in.ExpectedCheckpoints > 0 && in.Checkpoints == 0 && in.TestExitCode == 0 {
		add("no-screenshots", trace.VerdictDegraded,
			fmt.Sprintf("the test passed but captured no screenshots — the last good run took %d", in.ExpectedCheckpoints),
			"check the test still writes shots into $RETRACE_RUN_DIR/shots")
	}

	// ensemble already proved this one at the source (SessionManager names
	// the service that dropped baggage). Carry its reasons verbatim rather
	// than re-deriving a weaker version here.
	for _, r := range in.SessionReasons {
		add("propagation-gap", in.SessionVerdict, r,
			"make the named service forward the `baggage` header alongside `traceparent`")
	}
	for _, n := range in.Notes {
		add("capture-note", trace.VerdictSuspect, n, "re-run if the recording matters; the artifact may be incomplete")
	}

	if len(gaps) > 0 {
		longest := gaps[0]
		for _, g := range gaps {
			if g.Seconds > longest.Seconds {
				longest = g
			}
		}
		// Evidence, not a verdict: a gap cannot tell a dead proxy from an
		// idle test.
		add("quiet-stretch", trace.VerdictSuspect,
			fmt.Sprintf("%d stretch(es) of %ds+ with no calls captured — longest %ds", len(gaps), int(threshold.Seconds()), longest.Seconds),
			"if the capture was restarted mid-run, calls in that window are missing, not absent")
	}

	status := trace.VerdictOK
	for _, r := range reasons {
		status = status.Worse(r.Status)
	}
	out := runs.CaptureTrust{Status: status, Reasons: reasons, Gaps: gaps, Summary: "capture looks complete"}
	for _, r := range reasons {
		if r.Status == status {
			out.Summary, out.Hint = r.Detail, r.Hint
			break
		}
	}
	return out
}

// Fatal reports whether a verdict should stop a recording from being
// promoted or trusted. `suspect` is a heuristic that fires on plenty of
// legitimate runs, so failing on it would flood false alarms and get the
// check switched off; broken/degraded/failed mean the capture did not
// happen as intended.
func Fatal(c runs.CaptureTrust) bool {
	switch c.Status {
	case trace.VerdictBroken, trace.VerdictDegraded, trace.VerdictFailed:
		return true
	}
	return false
}
```

`FindGaps` is real code, not a description — it is the only producer of
`CaptureTrust.Gaps` and its quiet-subtraction is what the test above pins:

```go
// FindGaps reports stretches where nothing was recorded for longer than
// threshold, MINUS any interval a flow explicitly declared quiet. A flow
// that said "I am waiting on a push notification here" has explained its
// own silence; an undeclared hole of the same length has not, and is
// usually the app having stopped routing through us mid-run.
func FindGaps(hops []trace.Hop, threshold time.Duration, quiet []runs.Group) []runs.Gap {
	if len(hops) < 2 || threshold <= 0 {
		return nil
	}
	starts := make([]time.Time, len(hops))
	for i, h := range hops {
		starts[i] = h.T.Start
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	var out []runs.Gap
	for i := 1; i < len(starts); i++ {
		from, to := starts[i-1], starts[i]
		d := to.Sub(from)
		for _, g := range quiet {
			if !g.Quiet {
				continue
			}
			d -= overlap(from, to, g.StartedAt, g.EndedAt)
		}
		if d >= threshold {
			out = append(out, runs.Gap{From: from, To: to, Seconds: int(d.Seconds())})
		}
	}
	return out
}

// overlap is the intersection of two half-open intervals, or zero.
func overlap(aStart, aEnd, bStart, bEnd time.Time) time.Duration {
	start, end := aStart, aEnd
	if bStart.After(start) {
		start = bStart
	}
	if bEnd.Before(end) {
		end = bEnd
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start)
}
```

Note that `Gap.Seconds` reports the **unexplained** remainder, not the
wall-clock span — the number in the report is the number the reader has to
account for.

- [ ] **Step 4: Run — expect PASS** (`go test -race ./retrace/capture/ -run 'Assess|Gaps|Screenshot' -v`).

- [ ] **Step 5: Fill Task 4's seam and banner it in `cmdRun`**

Task 4 shipped `assessTrust(s, hops, cps, groups, exitCode) runs.CaptureTrust`
in `cmd_run.go` with a `TODO(task-6)` placeholder body that returns
`suspect / "not assessed yet"`. **Replace that body — do not add a second
call site**; `runFlow` already stores the result in `Manifest.Capture`:

```go
func assessTrust(s *capture.Session, hops []trace.Hop, cps []runs.Checkpoint,
	groups []runs.Group, exitCode int) runs.CaptureTrust {
	return capture.Assess(capture.AssessInput{
		ProxyFailure:        s.ProxyFailure(),
		Hops:                hops,
		Checkpoints:         len(cps),
		ExpectedCheckpoints: expectedCheckpoints(s.Paths),
		RequestsSeen:        s.RequestsSeen(),
		TestExitCode:        exitCode,
		// Quiet intervals come from the SAME derived groups the manifest
		// stores, so "the report says this stretch was deliberately quiet"
		// and "the verdict forgave this stretch" can never disagree.
		Quiet:          quietOnly(groups),
		// The same constant Assess falls back to, passed explicitly so the
		// number has a visible name at the call site. There is one
		// declaration of it, in capture/trust.go.
		GapThreshold:   capture.DefaultGapThreshold,
		SessionVerdict: s.EndVerdict(),
		SessionReasons: s.EndReasons(),
		Notes:          s.TrustNotes(),
	})
}
```

`expectedCheckpoints` reads the previous run of the same app/flow
(`runs.ListRuns` → second-to-last → its manifest's `len(Checkpoints)`) and
returns `-1` when there is no history. `EndVerdict`/`EndReasons`/`TrustNotes`
are accessors over the `endReport`/`trustNotes` fields Task 5 added.

Then print — **to stderr, before the summary, always**:

```go
if trust.Status != trace.VerdictOK {
	fmt.Fprintf(stderr, "\n  ⚠ capture-trust: %s — %s\n", trust.Status, trust.Summary)
	if trust.Hint != "" {
		fmt.Fprintf(stderr, "    %s\n", trust.Hint)
	}
}
```

- [ ] **Step 6: Regression test for the banner**

`TestRunBannersANonOkVerdict` — a `run` whose command makes no calls prints
a line containing `capture-trust:` and `broken` on stderr, and the manifest
records the same status. (Spec: "all report surfaces SHALL banner non-ok
verdicts" — Tasks 10, 13 and 16 assert the same on their surfaces.)

- [ ] **Step 7: Full suites + commit**

```bash
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/capture retrace/cmd/retrace
git commit -m "feat(retrace): capture-trust verdict with ranked evidence and report banners"
```

---

### Task 7: Pixel diff — pixelmatch port, masks, border trim, A/B/overlay/diff images

**Files:**
- Create: `retrace/diff/pixel/pixel.go`, `retrace/diff/pixel/pixel_test.go`
- Create: `retrace/diff/pixel/trim.go`, `retrace/diff/pixel/trim_test.go`
- Create: `retrace/diff/pixel/testdata/{identical,diff,mask}.{a,b}.png`
- Create: `retrace/diff/pixel/gen_testdata_test.go` (regenerates the goldens)

**Interfaces:**
- Consumes: stdlib `image`, `image/png`, `image/draw`, plus `retrace/config`
  for `RectsFrom` (config is a leaf: `pixel → config` is safe, the reverse
  is not).
- Produces (used by Tasks 10, 11, 13, 16):
  ```go
  package pixel
  type Rect struct {
      X      int `json:"x"`
      Y      int `json:"y"`
      Width  int `json:"width"`
      Height int `json:"height"`
  }
  type Options struct {
      Masks         []Rect
      GateThreshold float64
      FineThreshold float64
      WantDiff      bool
      WantOverlay   bool
      // Trim crops a uniform border off BOTH shots before comparing,
      // when the checkpoint asked for it (runs.Checkpoint.Trim, set at
      // capture from a `<name>.trim` marker). Trimming is a COMPARE-time
      // decision on purpose: it must never alter what was captured, and
      // putting it here is what keeps retrace/capture from importing
      // retrace/diff/pixel.
      Trim bool
  }
  // Overlap is embedded in diff.CheckpointVerdict and therefore reaches
  // summary.json, `retrace diff --json`, the REST item response and the
  // static export. It is nil on the equal-size path, which is what almost
  // every unit fixture uses — so an untagged version of this type would
  // have shipped `{"Width":390,…}` to a UI reading `overlap.width`, and
  // the plan's own golden could not have caught it (the golden is written
  // from these same Go types, so a wrong-cased key gets baked in rather
  // than flagged).
  type Overlap struct {
      Width       int     `json:"width"`
      Height      int     `json:"height"`
      DiffPct     float64 `json:"diffPct"`
      DiffPctFine float64 `json:"diffPctFine"`
      NumDiff     int     `json:"numDiff"`
      PaddingPct  float64 `json:"paddingPct"`
  }
  type Result struct {
      Width, Height  int
      DiffPct        float64
      DiffPctFine    float64
      NumDiff        int
      Mismatch       bool
      PaddedForDiff  bool
      WidthA, HeightA, WidthB, HeightB int
      Overlap        *Overlap
      // TrimA/TrimB are the rects Compare actually kept when Options.Trim
      // was set, in the ORIGINAL images' coordinates; nil when trimming
      // was not requested or was refused. WidthA/HeightA/WidthB/HeightB
      // remain the shots' real, pre-trim geometry — the report says what
      // was captured AND what was compared, never one standing in for the
      // other.
      TrimA, TrimB *Rect
  }
  type Images struct { Diff, Overlay *image.RGBA } // nil when not requested or NumDiff == 0
  func Compare(aPNG, bPNG []byte, o Options) (Result, Images, error)
  func Match(a, b, out *image.RGBA, threshold float64, diffMask bool) int
  func ApplyMasks(img *image.RGBA, rects []Rect)
  // TrimUniformBorder returns the tight rect and the cropped image, or
  // ok=false when it refuses (see Step 6). Compare is its only caller.
  func TrimUniformBorder(img *image.RGBA) (cropped *image.RGBA, kept Rect, ok bool)
  func Encode(img *image.RGBA) ([]byte, error)
  func Decode(pngBytes []byte) (*image.RGBA, error)
  // RectsFrom converts the config package's YAML rectangles into pixel
  // rectangles. It lives HERE, not in config, because config is the leaf
  // package everything reads and must not import an engine. Tasks 10 and
  // 11 both call it — it is the ONLY conversion between the two Rect
  // types, so there is no second one to drift.
  func RectsFrom(rs []config.Rect) []Rect
  ```
  Defaults when zero: `GateThreshold = config.DefaultGate` (0.1),
  `FineThreshold = config.DefaultFine` (0.05).

- [ ] **Step 1: Generate the golden fixtures**

`retrace/diff/pixel/gen_testdata_test.go` — a `TestGenerateTestdata` guarded
by `if os.Getenv("REGEN") == ""  { t.Skip("set REGEN=1 to rewrite goldens") }`,
reproducing flowlens `test/fixtures/generate-fixtures.mjs` exactly: 40×40,
base `RGB(10,20,30)` opaque; `diff.b` adds a `RGB(250,0,0)` rect at
(5,5)-(15,15); `mask.b` adds a `RGB(0,250,0)` rect at (20,20)-(30,30);
`identical.a`/`identical.b` are the same solid image. Run it once with
`REGEN=1 go test ./retrace/diff/pixel/ -run TestGenerateTestdata` and commit
the six PNGs. Keeping the generator in-tree is why these are goldens and not
mystery bytes.

- [ ] **Step 2: Write the failing pixel test** — all 9 flowlens cases:

```go
func TestIdenticalImagesDiffToZero(t *testing.T)
func TestAChangedRectProducesANonzeroDiffPct(t *testing.T)
func TestMaskingTheChangedRectSuppressesTheDiff(t *testing.T)      // mask.a/mask.b + Rect{20,20,10,10} → DiffPct == 0
func TestAnUnmaskedRectStillReportsADiff(t *testing.T)             // same pair, mask elsewhere → DiffPct > 0
func TestDimensionMismatchIsReportedNotThrown(t *testing.T)        // 40x40 vs 40x60 → Mismatch, PaddedForDiff, no error
func TestDifferentSizesStillProduceAnOverlayAndADiff(t *testing.T) // Images.Diff != nil && Images.Overlay != nil
func TestIdenticallySizedScreenshotsAreNotFlaggedAsPadded(t *testing.T)
func TestASizeMismatchReportsOverlapMeasuredWithoutThePadding(t *testing.T) // Overlap.PaddingPct > 0, Overlap.DiffPct != Result.DiffPct
func TestMatchingSizesReportNoOverlapBlockAtAll(t *testing.T)      // Result.Overlap == nil
```

Plus two Go-specific cases the JS version could not have:
```go
func TestCompareRejectsNonPngInputWithANamedError(t *testing.T)
func TestDiffImageIsOnlyProducedWhenPixelsActuallyDiffer(t *testing.T) // NumDiff==0 → Images.Diff == nil
```

- [ ] **Step 3: Run — expect FAIL** (`undefined: Compare`).

- [ ] **Step 4: Implement `retrace/diff/pixel/pixel.go`** — the pixelmatch
  algorithm, ported. Constants are load-bearing; do not "improve" them.

```go
// Package pixel is a Go port of the pixelmatch algorithm (YIQ perceptual
// colour delta with antialiasing rejection), plus the screenshot-diff
// policy flowlens layered on top: mask rectangles, a coarse gate threshold
// and a fine reporting threshold, union-canvas padding when two shots have
// different geometry, and a magenta density overlay.
//
// It uses image/png from the standard library — the whole reason the diff
// engines are Go is that this needs no dependency.
package pixel

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
)

// maxYIQDelta is pixelmatch's 35215: the maximum possible YIQ difference
// between two colours, so `threshold` reads as a fraction of "as different
// as two colours can be".
const maxYIQDelta = 35215.0

// Overlay tuning, carried from the prototype: magenta at 45–70% alpha,
// dilated 3px so a one-pixel change is visible at a glance, with alpha
// scaled by how dense the changes are within a 10px radius.
var overlayColor = [3]float64{255, 0, 170}

const (
	overlayAlphaMin    = 0.45
	overlayAlphaMax    = 0.70
	overlayDilatePx    = 3
	overlayDensityPx   = 10
)

func Decode(b []byte) (*image.RGBA, error) {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("not a decodable PNG: %w", err)
	}
	if rgba, ok := img.(*image.RGBA); ok && rgba.Rect.Min == (image.Point{}) {
		return rgba, nil
	}
	out := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out, nil
}

func Encode(img *image.RGBA) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// colorDelta is pixelmatch's perceptual difference. yOnly returns the
// brightness delta alone, which is what antialias detection compares.
// Semi-transparent pixels are blended against white first, so an alpha
// change is not silently free.
func colorDelta(a, b []uint8, k, m int, yOnly bool) float64 {
	r1, g1, b1, a1 := float64(a[k]), float64(a[k+1]), float64(a[k+2]), float64(a[k+3])
	r2, g2, b2, a2 := float64(b[m]), float64(b[m+1]), float64(b[m+2]), float64(b[m+3])
	if a1 == a2 && r1 == r2 && g1 == g2 && b1 == b2 {
		return 0
	}
	if a1 < 255 {
		f := a1 / 255
		r1, g1, b1 = blend(r1, f), blend(g1, f), blend(b1, f)
	}
	if a2 < 255 {
		f := a2 / 255
		r2, g2, b2 = blend(r2, f), blend(g2, f), blend(b2, f)
	}
	y1, y2 := rgb2y(r1, g1, b1), rgb2y(r2, g2, b2)
	y := y1 - y2
	if yOnly {
		return y
	}
	i := rgb2i(r1, g1, b1) - rgb2i(r2, g2, b2)
	q := rgb2q(r1, g1, b1) - rgb2q(r2, g2, b2)
	delta := 0.5053*y*y + 0.299*i*i + 0.1957*q*q
	if y1 > y2 {
		return -delta
	}
	return delta
}

func blend(c, a float64) float64 { return 255 + (c-255)*a }
func rgb2y(r, g, b float64) float64 { return r*0.29889531 + g*0.58662247 + b*0.11448223 }
func rgb2i(r, g, b float64) float64 { return r*0.59597799 - g*0.27417610 - b*0.32180189 }
func rgb2q(r, g, b float64) float64 { return r*0.21147017 - g*0.52261711 + b*0.31114694 }

// Match counts differing pixels between two same-sized images. Antialiased
// pixels are detected and NOT counted — text rendering differs by a
// subpixel between machines, and counting that would make every CI run a
// diff. When out is non-nil it receives either the classic red-on-grey diff
// or, with diffMask, an alpha-only mask used to build the overlay.
func Match(a, b, out *image.RGBA, threshold float64, diffMask bool) int {
	w, h := a.Rect.Dx(), a.Rect.Dy()
	maxDelta := maxYIQDelta * threshold * threshold
	diff := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pos := y*a.Stride + x*4
			delta := colorDelta(a.Pix, b.Pix, pos, pos, false)
			if math.Abs(delta) > maxDelta {
				if antialiased(a, x, y, b) || antialiased(b, x, y, a) {
					if out != nil && !diffMask {
						setPix(out, pos, 255, 255, 0, 255) // yellow: antialiasing, not a change
					}
					continue
				}
				if out != nil {
					setPix(out, pos, 255, 0, 0, 255)
				}
				diff++
			} else if out != nil && !diffMask {
				// Unchanged pixels fade to grey so the red stands out.
				v := uint8(255 - (255-rgb2y(float64(a.Pix[pos]), float64(a.Pix[pos+1]), float64(a.Pix[pos+2])))*0.1)
				setPix(out, pos, v, v, v, 255)
			}
		}
	}
	return diff
}

// antialiased reports whether the pixel at (x1,y1) in img looks like an
// antialiasing artefact: its brightness is an extreme among its neighbours,
// and the extreme neighbour has many identical siblings in BOTH images —
// i.e. it sits on an edge between two solid regions.
func antialiased(img *image.RGBA, x1, y1 int, other *image.RGBA) bool {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	x0, y0 := max(x1-1, 0), max(y1-1, 0)
	x2, y2 := min(x1+1, w-1), min(y1+1, h-1)
	pos := y1*img.Stride + x1*4
	zeroes := 0
	if x1 == x0 || x1 == x2 || y1 == y0 || y1 == y2 {
		zeroes = 1
	}
	minDelta, maxD := 0.0, 0.0
	minX, minY, maxX, maxY := -1, -1, -1, -1
	for x := x0; x <= x2; x++ {
		for y := y0; y <= y2; y++ {
			if x == x1 && y == y1 {
				continue
			}
			d := colorDelta(img.Pix, img.Pix, pos, y*img.Stride+x*4, true)
			switch {
			case d == 0:
				zeroes++
				if zeroes > 2 {
					return false // a flat neighbourhood is not an edge
				}
			case d < minDelta:
				minDelta, minX, minY = d, x, y
			case d > maxD:
				maxD, maxX, maxY = d, x, y
			}
		}
	}
	if minDelta == 0 || maxD == 0 {
		return false
	}
	return (hasManySiblings(img, minX, minY) && hasManySiblings(other, minX, minY)) ||
		(hasManySiblings(img, maxX, maxY) && hasManySiblings(other, maxX, maxY))
}

// hasManySiblings reports whether a pixel has 3+ identical neighbours,
// counting the image border as one — the signature of a solid region.
func hasManySiblings(img *image.RGBA, x1, y1 int) bool {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	x0, y0 := max(x1-1, 0), max(y1-1, 0)
	x2, y2 := min(x1+1, w-1), min(y1+1, h-1)
	pos := y1*img.Stride + x1*4
	zeroes := 0
	if x1 == x0 || x1 == x2 || y1 == y0 || y1 == y2 {
		zeroes = 1
	}
	for x := x0; x <= x2; x++ {
		for y := y0; y <= y2; y++ {
			if x == x1 && y == y1 {
				continue
			}
			p := y*img.Stride + x*4
			if img.Pix[pos] == img.Pix[p] && img.Pix[pos+1] == img.Pix[p+1] &&
				img.Pix[pos+2] == img.Pix[p+2] && img.Pix[pos+3] == img.Pix[p+3] {
				zeroes++
				if zeroes > 2 {
					return true
				}
			}
		}
	}
	return false
}

func setPix(img *image.RGBA, pos int, r, g, b, a uint8) {
	img.Pix[pos], img.Pix[pos+1], img.Pix[pos+2], img.Pix[pos+3] = r, g, b, a
}

// ApplyMasks paints rectangles opaque black in BOTH images before
// comparison, so a clock widget or an avatar cannot fail a checkpoint. The
// rects are clamped to the image, so a mask authored for a taller device
// degrades to a partial mask instead of panicking.
func ApplyMasks(img *image.RGBA, rects []Rect) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	for _, r := range rects {
		for y := max(r.Y, 0); y < min(r.Y+r.Height, h); y++ {
			for x := max(r.X, 0); x < min(r.X+r.Width, w); x++ {
				setPix(img, y*img.Stride+x*4, 0, 0, 0, 255)
			}
		}
	}
}
```

`Compare` orchestrates, exactly as flowlens did:
1. decode both; record `WidthA/HeightA/WidthB/HeightB` — these are the
   shots' real geometry and are never overwritten by anything below.
1a. **`ApplyMasks` on both, HERE — before trim, before the size branch.**
   Mask rects come from `cfg.MasksFor(...)` and are authored in the
   ORIGINAL screenshot's coordinate space, so this is the only frame in
   which a mask rect means what its author meant. **Amended after Task 7's
   review; the original plan ran this at step 3 and that was wrong twice
   over.** With masks applied post-trim, a mask at (12,12) under
   `TrimA={10,10,20,20}` covers the content that was at (22,22) — and since
   A and B trim independently, it covers *different content on each side*.
   Measured on the shipped code: a masked widget gave `NumDiff=0` with
   `Trim:false` and `NumDiff=16` with `Trim:true`, and the plan's only
   `pixel.Compare` call site (line ~5518) passes `Masks` and `Trim` in the
   same `Options`. Running masks post-`Overlap` was the second error: it
   left masked regions counted in the `Overlap` block, the one number this
   plan calls "the only number that means the content changed", which
   reaches `summary.json`, `--json`, the REST item response and the static
   export. Do NOT "fix" this by translating mask rects per side into
   post-trim coordinates — that answers the coordinate-space question twice
   instead of removing it.
1b. if `o.Trim` → `TrimUniformBorder` each image independently; on `ok`,
   replace the working image and record the kept rect in `Result.TrimA` /
   `Result.TrimB`. A refusal (see Step 6) is not an error: the working
   image stays whole and the corresponding `Trim*` stays nil, so the
   report can say "trim was requested and declined" rather than silently
   comparing something other than what it claims. **This is
   `TrimUniformBorder`'s only caller** — an earlier draft implemented it
   and never called it from anywhere.
2. if sizes differ → compute `Overlap` on the **cropped intersection** first
   (that is the only number that means "the content changed"), then pad both
   onto the union canvas and set `PaddedForDiff`.
3. ~~`ApplyMasks` on both.~~ **TOMBSTONE — moved to step 1a by Task 7's
   review. Do not restore it here.** Masking at this point applies
   original-coordinate rects to trimmed images and leaves masked pixels
   counted in `Overlap`; both were measured as live defects, not
   hypotheticals. See 1a for the evidence.
4. `Match` twice: at `GateThreshold` (the verdict number) and at
   `FineThreshold` (the reporting number).
5. if `WantDiff && NumDiff > 0` → keep the gate pass's output image.
6. if `WantOverlay && NumDiff > 0` → `Match(..., diffMask: true)` into a mask
   image, build `rawMask []uint8` from its alpha, `dilate` at 3px, compute a
   `density` box-filter at 10px, and composite magenta over a copy of B.
7. box filter is the two-pass `boxSum1D` from the prototype — O(w·h), not
   O(w·h·r²); port it as `boxFilter2D(mask []float32, w, h, r int) []float32`.

- [ ] **Step 5: Run — expect PASS** (`go test -race ./retrace/diff/pixel/ -v`).

- [ ] **Step 6: Port `trim.go` + its test**

`TrimUniformBorder` crops a uniform border matched against the top-left
pixel, **refusing** to trim (returning `ok == false`) when the result would
be <2px in either dimension — a fully uniform shot means nothing rendered,
and trimming it to a sliver destroys the evidence.

**Amended after Task 7's review, two corrections.** First, the "<2px" clause
is the ONLY refusal test; an earlier draft listed a separate "fully uniform"
refusal, but the scan invariant makes those identical — `top` runs to
`b.Max.Y` on a uniform image, so `kh == 0` exactly, a strict subset of <2px.
Verified exhaustively for every uniform `w,h` in 1..6 plus the degenerate
set (0x0, 5x0, 1x1, fully transparent, 10x1, 1x10, non-zero-`Rect.Min`
sub-images). A separate check there is unreachable code.
Second, ~~"or when nothing would change"~~ is a **TOMBSTONE — an
already-tight image returns `ok=true` with the full-bounds rect, NOT a
refusal.** Refusing would set `Result.TrimA` to nil, which that field's own
doc reads as "trim was requested and declined", making "already tight"
indistinguishable from "declined" — one zero value carrying two meanings,
which the Global Constraint forbids. It works on a decoded `*image.RGBA`, not on PNG bytes: `Compare`
has already decoded, and a re-encode round trip inside the compare path
would be pure waste.
Tests: `TestTrimsAUniformBorder`, `TestRefusesToTrimAFullyUniformImage`,
`TestRefusesToTrimBelowTwoPixels`,
`TestLeavesAnAlreadyTightImageUntouched`.
Plus the wiring test, which is the one that would have caught the dead
function: `TestCompareTrimsBothSidesWhenTrimIsRequested` — a 40×40 pair
whose only difference sits inside a 10px uniform border; with
`Options{Trim: false}` the diff is non-zero, with `Options{Trim: true}` the
borders come off, `Result.TrimA`/`TrimB` are non-nil, and `WidthA`/`WidthB`
still report 40.

- [ ] **Step 7: Commit**

```bash
go test -race ./retrace/...
git add retrace/diff/pixel
git commit -m "feat(retrace): pixelmatch port with masks, thresholds, border trim, overlay images"
```

---

### Task 8: Wire diff — pairing, field-level diff, LIS reorder, sections

**Files:**
- Create: `retrace/diff/wire.go`, `retrace/diff/wire_test.go`
- Create: `retrace/diff/order.go`, `retrace/diff/order_test.go`
- Create: `retrace/diff/deviations.go` — **types only in this task**
  (`Deviation`, `ToleratedNote`); Task 11 adds the ledger logic to the same
  file. Without this file the package does not compile, because `Options`
  and `Call` below reference both types.

**Interfaces:**
- Consumes: `core/trace.Hop`, `retrace/rules`, `retrace/runs`. (**Not**
  `retrace/config` — an earlier draft listed it and nothing in this task's
  code touches it. `Options.Normalize` is a `func(string) string` precisely
  so the engine takes a behaviour, not a config object.)
- Produces (used by Tasks 10, 12, 13, 16):

  **Every one of these types is serialized** — into `summary.json`, into
  `retrace diff --json`, into the REST responses Task 13 serves, and into
  the TypeScript mirrors Task 15 declares. The `json:` tags below are part
  of the contract, not decoration: without them `encoding/json` emits
  `"Method"`, `"NormalizedPath"`, `"NewRoutes"`, and every consumer breaks
  at once, silently, with the Go tests still green. Copy them exactly.

  ```go
  package diff
  type Options struct {
      WireIgnore []string
      Rules      []rules.Rule
      Normalize  func(path string) string        // config.NormalizePath
      GroupsA    []runs.Group
      GroupsB    []runs.Group
      Deviations []Deviation                     // see deviations.go, below
  }
  type Pair struct { Method, NormalizedPath string; A, B trace.Hop }
  func PairCalls(a, b []trace.Hop, normalize func(string) string) (pairs []Pair, missing, extra []trace.Hop)
  func CallSimilarity(a, b trace.Hop) float64
  func NormalizeQuery(rawQuery string) string
  func SplitPath(hopPath string) (path, rawQuery string)

  type FieldDiff struct {
      Scope   string `json:"scope"`   // "req" | "resp"
      Path    string `json:"path"`    // dotted field path
      Type    string `json:"type"`    // "changed" | "added" | "removed"
      A       any    `json:"a"`
      B       any    `json:"b"`
      Matcher string `json:"matcher,omitempty"`
      Glob    string `json:"glob,omitempty"`
  }
  type HeaderDiff struct {
      Scope string `json:"scope"`
      Name  string `json:"name"`
      // Type is the OUTCOME, not merely the shape of the change:
      // "changed" | "added" | "removed" | "tolerated" | "violation".
      // Clarified after Task 8's review, which found the implementation
      // emitting only "changed" — so a rule violation and a tolerated
      // change produced structurally identical rows and Task 10's
      // "exit 2 if a rule Violation exists" bullet became inexpressible
      // for headers. Task 15's TS mirror already declares this exact
      // five-value union, so the field always meant this; nothing about
      // the wire shape changes. `classify` keys on Type: "violation" and
      // "changed" count as changed, "tolerated" and "ignored" do NOT —
      // mirroring BodyTolerated, which deliberately does not.
      Type    string `json:"type"`
      A       string `json:"a"`
      B       string `json:"b"`
      Matcher string `json:"matcher,omitempty"`
  }
  type StatusChange struct {
      A int `json:"a"`
      B int `json:"b"`
  }
  type Entry struct {
      Method          string        `json:"method"`
      NormalizedPath  string        `json:"normalizedPath"`
      SeqA            uint64        `json:"seqA"`
      SeqB            uint64        `json:"seqB"`
      PosA            int           `json:"posA"`
      PosB            int           `json:"posB"`
      GroupA          string        `json:"groupA,omitempty"`
      GroupB          string        `json:"groupB,omitempty"`
      Moved           bool          `json:"moved,omitempty"`
      Truncated       bool          `json:"truncated,omitempty"`
      Classes         []string      `json:"classes,omitempty"`
      StatusChange    *StatusChange `json:"statusChange,omitempty"`
      BodyDiff        []FieldDiff   `json:"bodyDiff,omitempty"`
      BodyTolerated   []FieldDiff   `json:"bodyTolerated,omitempty"`
      BodyViolations  []FieldDiff   `json:"bodyViolations,omitempty"`
      BodyIgnored     []FieldDiff   `json:"bodyIgnored,omitempty"`
      OrderingChanges []FieldDiff   `json:"orderingChanges,omitempty"`
      HeaderDiff      []HeaderDiff  `json:"headerDiff,omitempty"`
  }
  type Wire struct {
      Paired  []Entry     `json:"paired"`
      Missing []Call      `json:"missing"`
      Extra   []Call      `json:"extra"`
      Groups  *GroupNames `json:"groups,omitempty"`
  }
  type Call struct {
      Method    string         `json:"method"`
      Path      string         `json:"path"`
      Seq       uint64         `json:"seq"`
      Status    int            `json:"status"`
      Group     string         `json:"group,omitempty"`
      Tolerated *ToleratedNote `json:"tolerated,omitempty"`
  }
  // One tag string per field, and a multi-name field cannot carry two of
  // them — `A, B []string `json:"a","b"`` is not valid Go, it is a struct
  // tag containing a comma. Declare the fields separately.
  type GroupNames struct {
      A []string `json:"a"`
      B []string `json:"b"`
  }
  func DiffWire(a, b []trace.Hop, o Options) Wire
  func DiffHeaders(a, b map[string]string, res rules.Resolved, scope string) []HeaderDiff

  type Section struct {
      Name    string         `json:"name"`
      Entries []Entry        `json:"entries"`
      Counts  map[string]int `json:"counts"`
  }
  func BuildSections(entries []Entry, groups *GroupNames) []Section
  func LISIndices(seq []int) []int
  ```

  **These two types are declared HERE, in `retrace/diff/deviations.go`,
  even though nothing populates them until Task 11.** A struct field whose
  type is undeclared is a *compile error* in Go, not an inert field —
  `Options.Deviations []Deviation` and `Call.Tolerated *ToleratedNote`
  would fail `go test ./retrace/diff/`, which is this task's own commit
  gate. Task 11 fills in the ledger logic (`LoadDeviations`, `Applies`,
  expiry) in the same file; this task creates the file with the types and
  a doc comment:

  ```go
  // deviations.go — a deviation is a recorded human decision to tolerate a
  // specific difference between two apps' runs: "these two are expected to
  // differ here, and here is why". Task 11 owns the ledger that loads,
  // resolves and matches them. The TYPES live here because this package's
  // own structs reference them, and a struct field whose type is undeclared
  // does not compile.
  package diff

  // Deviation is one entry in the ledger file named by config.Deviations.
  // Status is "proposed" | "approved": teams that want the ceremony gate on
  // approved; teams that do not, approve on write.
  type Deviation struct {
      ID     string    `json:"id"`
      Status string    `json:"status"`
      Apps   [2]string `json:"apps"`
      Method string    `json:"method"`
      Path   string    `json:"path"`
      Reason string    `json:"reason"`
  }

  // ToleratedNote is what a consumer sees on a difference a Deviation
  // covered: the difference still happened and is still reported, it just
  // does not count against the verdict. Never drop the difference itself —
  // "tolerated" and "absent" must never look the same to a reviewer.
  type ToleratedNote struct {
      ID     string `json:"id"`
      Reason string `json:"reason"`
  }
  ```

  Task 11 adds `LoadDeviations`, `ResolveDeviations` and `FindDeviation` to
  this same file. This task adds nothing else to it and does not populate
  `Options.Deviations` — a run before Task 11 simply passes nil, which is a
  no-op, and `TestNilDeviationsToleratesNothing` pins that.

- [ ] **Step 1: Write the failing pairing test**

```go
func TestPairsOnMethodAndNormalizedPathAndQuery(t *testing.T)
func TestQueryParamOrderDoesNotAffectPairing(t *testing.T)   // ?b=2&a=1 pairs with ?a=1&b=2
func TestTheBestMatchWinsNotWhicheverSharedAnIndex(t *testing.T) {
	// three POSTs to /cart with bodies X, Y, Z on side A and Y, Z on side B.
	// A positional zip would pair X↔Y and Y↔Z and report two changes plus a
	// missing; alignment must pair Y↔Y, Z↔Z and report X missing.
}
func TestStatusCarriesWeightInSimilarity(t *testing.T) {
	// a 304 with no body must not pair with a 200 with a body when a
	// better 200 candidate exists.
}
func TestUnmatchedCallsFallThroughToMissingAndExtra(t *testing.T)
func TestIdenticalRunsPairOneToOneInOrder(t *testing.T)      // ties resolve toward the diagonal
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement pairing in `retrace/diff/wire.go`**

```go
// callSimilarity weights: status carries real weight because a 304 cache
// hit and a 200 with a body are not the same event — which is exactly the
// pair a positional zip invents.
func CallSimilarity(a, b trace.Hop) float64 {
	s := 0.0
	if a.Status == b.Status {
		s += 0.3
	}
	return s + 0.5*bodySimilarity(a.Resp.Body, b.Resp.Body) + 0.2*bodySimilarity(a.Req.Body, b.Req.Body)
}

// align is Needleman-Wunsch with a zero gap score: a pair is made whenever
// it beats leaving both sides unmatched, and the BEST available match wins.
// Order-preserving, so it never pairs across a reorder — that is the
// reorder detector's job (order.go), not the aligner's.
func align(as, bs []trace.Hop) (pairs [][2]trace.Hop, aOnly, bOnly []trace.Hop) {
	n, m := len(as), len(bs)
	if n == 0 || m == 0 {
		return nil, as, bs
	}
	score := make([][]float64, n+1)
	for i := range score {
		score[i] = make([]float64, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			diag := CallSimilarity(as[i], bs[j]) + score[i+1][j+1]
			score[i][j] = math.Max(diag, math.Max(score[i+1][j], score[i][j+1]))
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		diag := CallSimilarity(as[i], bs[j]) + score[i+1][j+1]
		switch {
		case diag >= score[i+1][j] && diag >= score[i][j+1]:
			pairs = append(pairs, [2]trace.Hop{as[i], bs[j]})
			i, j = i+1, j+1
		case score[i+1][j] >= score[i][j+1]:
			aOnly = append(aOnly, as[i])
			i++
		default:
			bOnly = append(bOnly, bs[j])
			j++
		}
	}
	aOnly = append(aOnly, as[i:]...)
	bOnly = append(bOnly, bs[j:]...)
	return pairs, aOnly, bOnly
}
```

`PairCalls` buckets by `method + " " + normalize(path) + "?" + NormalizeQuery(query)`
(bucket keys iterated in **first-seen order**, not map order, so output is
deterministic), aligns each bucket, and concatenates.

`SplitPath` splits `trace.Hop.Path` (which is `RequestURI()`, so query
included) on the first `?`. `NormalizeQuery` sorts `k=v` pairs.

- [ ] **Step 4: Run — expect PASS.**

- [ ] **Step 5: Write the failing field-diff test** — port flowlens
`test/wire-diff.test.mjs` + `test/wire-rules.test.mjs` cases 90–176:

```go
func TestAToleratedBodyFieldIsReportedSeparatelyNotAsAChange(t *testing.T)
func TestAViolatingBodyFieldIsRecordedAsAViolationNotAPlainChange(t *testing.T)
func TestBodyDiffKeepsItsMeaningWhenNoRulesAreConfigured(t *testing.T)
func TestAnIgnoreIsOnlyRecordedWhenItActuallySuppressedADifference(t *testing.T)
func TestAddedAndRemovedKeysAreReportedAtTheChildPath(t *testing.T)
func TestSameMultisetDifferentOrderIsAnOrderingChangeNotEightFieldChanges(t *testing.T)
func TestAReorderIsDetectedEvenThoughAToleratedFieldDiffersInEveryElement(t *testing.T)
func TestWithoutARuleTheSameReorderStillReportsPositionalChanges(t *testing.T)
func TestHeadersAreComparedCaseInsensitivelyAndEqualOnesOmitted(t *testing.T)
func TestAHeaderOnOneSideOnlyIsNeverToleratedByAValueMatcher(t *testing.T)
func TestIgnoreDoesSilenceAnAppearingHeader(t *testing.T)
func TestATruncatedBodyIsFlaggedAndNotFieldDiffed(t *testing.T) {
	// trace.Payload.Truncated is a Go-side fact flowlens never had: a
	// size-capped body would otherwise report every field past the cap as
	// "removed". Entry.Truncated = true and BodyDiff is empty.
}
```

- [ ] **Step 6: Implement the walker**

Port `walk` / `diffArrays` / `blankTolerated` / `canonicalJSON` from
`src/wire-diff.mjs`. Go specifics:
- Bodies are strings: `parseBody(p trace.Payload) (any, bool)` returns
  `(nil, false)` when `p.Truncated` or the body is not JSON. When either
  side is unparseable, compare the raw strings and emit a single
  `FieldDiff{Path: "", Type: "changed"}` — never a field tree over
  half-parsed data.
- `canonicalJSON(v any) string` sorts object keys so structurally identical
  values compare equal regardless of key order. **Corrected after Task 8's
  review: the parenthetical this line used to carry — that `json.Marshal`
  sorts only top-level `map[string]any` keys and "arrays of maps need the
  recursive form" — is factually false.** `encoding/json` sorts map keys at
  every nesting level, maps inside slices included; measured, the two forms
  are equivalent on any value decoded from JSON. Keep the explicit function
  (one pass, no intermediate `[]byte`, and it makes the ordering guarantee
  local rather than inherited), but do NOT repeat the false justification in
  its doc comment. A mutation replacing its body with `json.Marshal` is an
  equivalent mutant, not a coverage gap — do not chase it.
- Array index segments are emitted as `items[0].sku`, so a rule glob
  `items.*.sku` will NOT match. Document this in `MatchFieldGlob`'s doc
  comment and cover it: `TestFieldGlobsAddressArrayElementsWithBracketIndices`.

- [ ] **Step 7: Implement `order.go`** — `LISIndices` (patience sort, O(n log n)),
`annotate` (per-side ordinals `PosA`/`PosB`, `Moved` = outside the LIS of B
positions read in A order, `Classes` from
`missing|new|changed|moved|identical`), and `BuildSections` (bucket by flow
part, seeding declared-but-empty parts — a part with zero calls is the exact
symptom of a marker placed after the traffic it meant to bracket).

Tests: `TestLISPicksTheMinimalMovedSet` (A: 1 2 3 → B: 3 1 2 is **one** call
moving, not three), `TestSectionsSeedDeclaredButEmptyParts`,
`TestUngroupedRunsRenderAsOneFlatSection`.

- [ ] **Step 8: Run — expect PASS** (`go test -race ./retrace/diff/ -v`).

- [ ] **Step 9: Commit**

```bash
git add retrace/diff/wire.go retrace/diff/wire_test.go retrace/diff/order.go retrace/diff/order_test.go
git commit -m "feat(retrace): wire diff with similarity pairing, field-level rules, LIS reorder detection"
```

---

### Task 9: Hop diff, unexpected statuses, perf budgets, OpenAPI conformance

**Files:**
- Create: `retrace/diff/hop.go`, `retrace/diff/hop_test.go`
- Create: `retrace/diff/status.go`, `retrace/diff/status_test.go`
- Create: `retrace/diff/perf.go`, `retrace/diff/perf_test.go`
- Create: `retrace/diff/openapi.go`, `retrace/diff/openapi_test.go`, `retrace/diff/testdata/openapi.json`

**Interfaces:**
- Produces (used by Tasks 10, 13, 16):
  ```go
  package diff
  // Every type here is embedded in diff.Summary and therefore serialized.
  // Tags are mandatory (Global Constraints) and are what Task 15's TS
  // mirrors transcribe.

  // status.go
  type StatusFinding struct {
      Seq    uint64 `json:"seq"`
      Method string `json:"method"`
      Path   string `json:"path"`
      Status int    `json:"status"`
  }
  func MatchURLGlob(pattern, urlPath string) bool     // query stripped before matching
  func FindUnexpectedStatuses(hops []trace.Hop, expected []config.StatusRule) []StatusFinding

  // hop.go
  const DefaultCountTolerance = 0.5
  type ServiceCount struct {
      Service  string `json:"service"`
      A        int    `json:"a"`
      B        int    `json:"b"`
      Deviates bool   `json:"deviates"`
  }
  type Route struct {
      To     string   `json:"to"`
      Method string   `json:"method"`
      Path   string   `json:"path"`
      Via    []string `json:"via,omitempty"` // relays folded into this route
  }
  type RouteFailure struct {
      Method         string `json:"method"`
      Path           string `json:"path"`
      ExpectedStatus int    `json:"expectedStatus"`
      ActualStatus   int    `json:"actualStatus"`
      Reason         string `json:"reason"` // "missing" | "wrong-status"
  }
  type HopDiff struct {
      ServiceCounts         []ServiceCount  `json:"serviceCounts"`
      NewErrors             []StatusFinding `json:"newErrors,omitempty"`
      GoneErrors            []StatusFinding `json:"goneErrors,omitempty"`
      NewRoutes             []Route         `json:"newRoutes"`
      GoneRoutes            []Route         `json:"goneRoutes"`
      RequiredRouteFailures []RouteFailure  `json:"requiredFailures,omitempty"`
      HopRequireConfigured  bool            `json:"hopRequireConfigured"`
  }
  type HopOptions struct {
      Normalize func(string) string
      Expected  []config.StatusRule
      Require   []config.RequiredRoute
      // CountTolerance zero means "unset" and falls back to
      // DefaultCountTolerance — a caller that wants NO tolerance passes a
      // negative value. Stated because a zero-value field that silently
      // means something other than zero is exactly how a "0% tolerance"
      // intent becomes 50%.
      CountTolerance float64
      // NoCollapse turns relay folding OFF. The field is negative on
      // purpose: folding is the wanted behaviour on every real run, and a
      // `Collapse bool` documented as "default true" is a documentation
      // claim a bool cannot keep — its zero value is false, so every
      // caller that built HopOptions without naming the field would get
      // folding OFF and every relay topology change would read as a new
      // API call. That is precisely the false positive this task exists to
      // prevent, and its own test would still have passed, because the
      // test sets the field explicitly.
      //
      // No pointer, no sentinel: the zero value IS the default, which is
      // the only shape that cannot be got wrong by omission.
      NoCollapse bool
  }
  func DiffHops(a, b []trace.Hop, o HopOptions) HopDiff
  func CollapsedRoutes(hops []trace.Hop, normalize func(string) string) []Route  // relay-folded, with Via
  // RequiredRouteFailures takes ONE hop slice, and the caller contract is
  // that it is side B, RAW (uncollapsed) — a hard gate must be evaluated
  // against what actually happened on the candidate, not against a folded
  // view of it and not against the reference. DiffHops calls it with b.
  func RequiredRouteFailures(hopsB []trace.Hop, require []config.RequiredRoute) []RouteFailure

  // perf.go
  type PerfResult struct {
      Status     string  `json:"status"` // "ok" | "over" | "unset"
      MeasuredMs float64 `json:"measuredMs"`
      BudgetMs   float64 `json:"budgetMs"`
  }
  type PerfBudget struct {
      BudgetMs         float64 `json:"budgetMs"`
      SampleCount      int     `json:"sampleCount"`
      MeasuredMaxMs    float64 `json:"measuredMaxMs"`
      MeasuredMedianMs float64 `json:"measuredMedianMs"`
      MarginFactor     float64 `json:"marginFactor"`
  }
  func TotalCallDurationMs(hops []trace.Hop) float64
  func DerivePerfBudget(samples []float64, marginFactor float64) (PerfBudget, error)
  func CheckPerfBudget(hops []trace.Hop, budgetMs float64) PerfResult

  // openapi.go
  type ConformanceFinding struct {
      Method string `json:"method"`
      Path   string `json:"path"`
      Status int    `json:"status"`
      // Kind: "unknown-path" | "unknown-method" | "undocumented-status" |
      //       "missing-required-field" | "unchecked"
      //
      // "unchecked" was added after Task 9's review and is load-bearing: it
      // is how the checker says "I could not verify this", which must never
      // serialize identically to "this passed". Emit it for a `$ref` that
      // cannot be resolved (the doc comment always promised
      // checked-what-we-could, never a silent pass), for a response body
      // that fails json.Unmarshal, and for a TRUNCATED body — trace.Redactor
      // caps bodies at maxBody and sets Payload.Truncated, so every
      // redaction-truncated response would otherwise report as fully
      // conformant. Detail carries what could not be checked and why.
      //
      // Task 10: "unchecked" is reported and NEVER satisfies conformance.
      // It must not count toward a pass in the exit-code contract.
      Kind   string `json:"kind"`
      Detail string `json:"detail"`
  }
  func CheckOpenAPI(hops []trace.Hop, specPath string) ([]ConformanceFinding, error)
  ```

- [ ] **Step 1: status.go, test first**

```go
func TestUnexpectedStatusesIgnoreTheQueryStringWhenMatchingGlobs(t *testing.T) {
	// expected: {path: "/api/cards/*/eligibility", status: 404}
	// hop path:  "/api/cards/42/eligibility?fresh=1"  → excused.
	// Matching raw would silently un-excuse it and report it every run.
}
func TestAnyUnallowlisted4xxOr5xxIsAFinding(t *testing.T)
func TestADoubleStarGlobSpansSegments(t *testing.T)
func TestAHopWithNoStatusIsNotAFinding(t *testing.T)  // a transport error carries Err, not a status
```
Implement: strip `?`/`#` before splitting on `/`; `**` backtracks over any
span including zero.

**Ruling: this task is where `trace.CollapseRelays` gets its consumer.**
`GET /api/traces/{id}` has returned a `logical` half since Phase 2, and
nothing has ever read it — `TopologyView` uses only `r.hops`, and task 3.3's
review accepted that. So `trace.CollapseRelays` and `trace.LogicalHop` are
tested, shipped, and unused in the product. **Do not delete them: hop
diffing is the consumer they were waiting for.** A transparent relay hop
(an edge gateway that forwards unchanged) is not a downstream call anyone
made a decision about, so counting it as one means every relay topology
change reads as "this flow grew an extra API call" — the exact false
positive that gets a regression signal switched off.

`DiffHops` therefore folds both sides with
`trace.CollapseRelays(hops, !o.NoCollapse)` before deriving service counts
and routes, and each `Route` carries the folded relays in `Via` so the
report can say *how* the call got there. `RequiredRouteFailures` and error
signatures run against the **raw** hops — a hopRequire assertion names a
real route and a 500 is a 500 no matter who relayed it. Task 15's
`HopDeltaList` renders `Via` as a chain of small badges, which is the first
time a user sees relay collapse at all.

**The lowering, written out — `CollapseRelays` does not return hops.** It
returns `[]trace.LogicalHop`, whose `Hop` and `Origin` are `*trace.Hop`
pointing into the input slice, and the two are different legs:

| field | for `client → edge → bff` | meaning |
|---|---|---|
| `LogicalHop.Hop.To` | `"edge"` | the FIRST leg — the relay |
| `LogicalHop.Origin.To` | `"bff"` | the LAST leg — the real destination |
| `LogicalHop.Via` | `["edge"]` | the relays folded out of the middle |

So `Route.To` must come from `Origin`, and only the method/path identity
comes from `Hop`. Taking `To` from `Hop` would name every folded route
after the relay it just folded out — the collapse would run, the counts
would look right, and every route would be attributed to the wrong service:

```go
// CollapsedRoutes lowers folded hops into the route identities the diff
// compares. Origin carries the outcome (destination, status); Hop carries
// the request identity (method, path). Mixing them up is silent — the
// route set stays the same size and every name is wrong.
func CollapsedRoutes(hops []trace.Hop, normalize func(string) string) []Route {
	seen := map[string]bool{}
	var out []Route
	for _, lh := range trace.CollapseRelays(hops, true) {
		path := lh.Hop.Path
		if normalize != nil {
			path = normalize(path)
		}
		r := Route{
			To:     lh.Origin.To, // NOT lh.Hop.To — see the table above
			Method: lh.Hop.Method,
			Path:   path,
			Via:    lh.Via,
		}
		key := r.To + " " + r.Method + " " + r.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}
```

`ServiceCount` is derived from the same `[]LogicalHop`, counting
`Origin.To` for the same reason. Note `CollapseRelays` never folds a pair
whose legs disagree on status (`LogicalHop.StatusMismatch`) — a relay that
changed the outcome is exactly the case worth seeing, and it arrives here
unfolded with an empty `Via`, which needs no special handling.

- [ ] **Step 2: hop.go, test first**

```go
func TestAnAddedDownstreamCallIsListedAsANewRoute(t *testing.T)  // the spec's headline scenario
func TestATransparentRelayHopIsFoldedAndNotCountedAsANewRoute(t *testing.T) {
	// run A: client -> bff.            run B: client -> edge -> bff, where
	// edge forwards unchanged. Collapsed, both runs made ONE logical call:
	// NewRoutes is empty and the folded route's Via is ["edge"].
	// With NoCollapse:true the same input DOES report a new route — that
	// is what proves the folding is doing the work.
}
func TestAFoldedRouteIsNamedAfterItsOriginNotItsRelay(t *testing.T) {
	// client -> edge -> bff, folded: the single Route must be To:"bff",
	// Via:["edge"]. Taking To from LogicalHop.Hop instead of .Origin gives
	// To:"edge" — same route count, same Via, wrong service, and no other
	// assertion in this file notices.
}
func TestCollapseIsAppliedToServiceCountsToo(t *testing.T)
func TestHopRequireAndErrorSignaturesRunAgainstRawHops(t *testing.T) {
	// a 500 returned BY a relay is still an error signature, and a
	// hopRequire route satisfied only on the raw leg still passes.
}
func TestServiceCountDriftUnderToleranceIsNotFlagged(t *testing.T) // 4 vs 5 calls → not deviating
func TestServiceCountDriftOverToleranceIsFlagged(t *testing.T)     // 2 vs 8 → deviating
func TestErrorSignaturesAreDedupedToOnePerRouteAndStatus(t *testing.T)
func TestHopRequireMissingRouteIsAFailure(t *testing.T)
func TestHopRequireWrongStatusReportsTheActualStatus(t *testing.T)
func TestHopRequireConfiguredDistinguishesNoAssertionsFromAllPassing(t *testing.T)
```
Implement per flowlens `src/hop-diff.mjs`, with the coarseness comment
carried over: a run's own retry/poll cadence is not reproducible, which is
why hops are never paired call-for-call the way wire.jsonl is.
`RequiredRouteFailures` runs against **side B only** — it asserts the latest
run made the call and got the right status, so an old reference predating
the requirement is never itself a failure.

- [ ] **Step 3: perf.go, test first**

```go
func TestTotalIsASumNotAMedian(t *testing.T) {
	// a run with more calls genuinely did more backend work end to end.
}
func TestDeriveBudgetUsesObservedMaxTimesMargin(t *testing.T) {
	// max, not mean+stddev: dev-machine timings are fat-tailed and a budget
	// that is too tight gets the whole plane switched off.
}
func TestDeriveBudgetRejectsAnEmptySample(t *testing.T)
func TestAnUnsetBudgetReportsUnsetNotOk(t *testing.T)
```
`TotalCallDurationMs` sums `hop.T.DoneMs` **over folded logical hops —
`trace.CollapseRelays(hops, true)`, taking `LogicalHop.Hop.T.DoneMs`, the
outer leg.** `DerivePerfBudget` defaults `marginFactor` to 1.5 when zero.

**Corrected after Task 9's review; the earlier text said to sum raw legs and
that was wrong.** `core/trace`'s collapse code documents the nesting —
`out.T.DoneMs = l.Hop.T.DoneMs // the outer leg's wall clock contains the
inner` — so on a `client → edge → bff` topology the client→edge leg already
contains the edge→bff leg. Measured: the same one logical call totals 100
direct and **205 relayed**. Putting a transparent relay in front of a
service roughly doubles the measured total and trips the budget, which is
exactly the false-positive class Step 1's relay-folding ruling exists to
eliminate — it folded routes and service counts and then left the perf plane
summing raw legs.

**Always fold; do NOT add a `collapse bool` here.** `CollapseRelays` is the
identity on a run with no relays, so folding is universally safe, and there
is no legitimate reason to sum nested legs — double-counting is a bug, not a
preference. An option whose wrong setting silently produces false budget
failures should not exist when one answer is always right.

- [ ] **Step 4: openapi.go, test first**

Scope ruling, stated in the file's doc comment: **conformance checking, not
validation.** No JSON-Schema dependency. `CheckOpenAPI` parses the spec with
`encoding/json` and answers four questions per recorded call:
1. does a `paths` entry match (literal, else template — `/users/{id}` matched
   segment-wise, `{...}` matching any one segment)? → `unknown-path`
2. is the method documented on it? → `unknown-method`
3. is the observed status documented under `responses` (exact, else the
   `2XX`/`4XX` range form, else `default`)? → `undocumented-status`
4. for a JSON response with an inline
   `content["application/json"].schema.required` list, is every required
   top-level property present? → `missing-required-field`
`$ref`s are followed one level into `#/components/schemas/...`; anything
deeper is reported as checked-what-we-could, never as a pass.

```go
func TestAnUndocumentedPathIsAFinding(t *testing.T)
func TestATemplatedPathMatchesASegment(t *testing.T)
func TestAnUndocumentedStatusIsAFindingAndARangeCounts(t *testing.T)  // 201 documented as "2XX" → ok
func TestAMissingRequiredFieldIsAFinding(t *testing.T)
func TestARefIsFollowedOneLevel(t *testing.T)
func TestAMissingSpecFileIsAnErrorNotSilentSuccess(t *testing.T)
```
Fixture: a hand-written `retrace/diff/testdata/openapi.json` with `/cart`
(GET 200 + required `items`), `/users/{id}` (GET "2XX"), and a
`#/components/schemas/Cart` ref.

- [ ] **Step 5: Run all four suites — expect PASS** (`go test -race ./retrace/diff/ -v`).

- [ ] **Step 6: Commit**

```bash
git add retrace/diff
git commit -m "feat(retrace): hop diff, unexpected-status detection, perf budgets, OpenAPI conformance"
```

---

### Task 10: Unified summary + `retrace diff` with `--json` and CI exit codes

**Files:**
- Create: `retrace/diff/summary.go`, `retrace/diff/summary_test.go`
- Create: `retrace/cmd/retrace/cmd_diff.go`, `retrace/cmd/retrace/cmd_diff_test.go`
- Modify: `retrace/cmd/retrace/main.go` (dispatch `diff`)

**Interfaces:**
- Consumes: everything from Tasks 7–9, `runs.Manifest`, `capture.Fatal`,
  `runs.CaptureTrust.Status` (Task 6). Also consumes, but does not define,
  two `retrace.yaml` keys Task 3's config package owns: `gates:` (per-plane
  budgets, e.g. `gates: {pixel: {budget_pct}}`) and `fail_on:` (the list of
  plane names allowed to fail the build) — named exactly as `cfg.Gates` and
  `cfg.FailOn` below; their yaml shape has exactly one definition, in
  Task 3.
- Produces (used by Tasks 11, 13, 15, 16):
  ```go
  package diff
  const SummarySchema = "retrace-diff/1"

  // Kind uses ONE vocabulary across this package and refs: "bundle" (an
  // accepted reference bundle), "run" (a run directory), "none" (no side
  // resolved). refs.Resolve returns the same three strings — an earlier
  // draft had this type saying "reference" where refs said "bundle", with
  // no mapping anywhere, so the two never agreed on the default path.
  type RunRef struct {
      RunID    string        `json:"runId"`
      Kind     string        `json:"kind"` // "bundle" | "run" | "none"
      Dir      string        `json:"dir"`
      Manifest runs.Manifest `json:"manifest"`
  }
  type CheckpointVerdict struct {
      Name        string           `json:"name"`
      Verdict     string           `json:"verdict"` // "ok" | "changed" | "missing" | "added" | "unreadable"
      DiffPct     float64          `json:"diffPct"`
      DiffPctFine float64          `json:"diffPctFine"`
      NumDiff     int              `json:"numDiff"`
      // Mismatch reports that the two SHOTS differed in size — copied from
      // pixel.Result.Mismatch, which Task 7's review pinned to the real
      // pre-trim geometry. Overlap is non-nil whenever the COMPARED images
      // differed in size, which independently trimmed same-size shots also
      // trigger. **So overlap != nil does NOT imply mismatch == true, and
      // code must not treat them as one signal.** When they disagree,
      // DiffPct is inflated by padding and overlap.paddingPct is how much;
      // overlap.diffPct is the honest content number.
      Mismatch    bool             `json:"mismatch,omitempty"`
      Overlap     *pixel.Overlap   `json:"overlap,omitempty"`
      // Trimmed reports the rects Compare actually used when the
      // checkpoint asked for border trimming, in the originals'
      // coordinates. nil = no trim requested, or trim refused.
      Trimmed *TrimRects       `json:"trimmed,omitempty"`
      Images  CheckpointImages `json:"images"` // "" for any side not written
  }
  type TrimRects struct {
      A *pixel.Rect `json:"a,omitempty"`
      B *pixel.Rect `json:"b,omitempty"`
  }
  // CheckpointImages carries the four sides of a checkpoint comparison.
  // A and B are relative to Summary.A.Dir / Summary.B.Dir and are the
  // capture's own path ("shots/receipt.png"); Diff and Overlay are
  // relative to BuildInput.OutDir and are written by Build
  // ("diff/shots/receipt.png"). Every one of the four resolves as
  // <dir>/shots/<name>.png — see the layout contract in Step 3.
  type CheckpointImages struct {
      A       string `json:"a,omitempty"`
      B       string `json:"b,omitempty"`
      Diff    string `json:"diff,omitempty"`
      Overlay string `json:"overlay,omitempty"`
  }
  type Counts struct {
      Checkpoints        int `json:"checkpoints"`
      PixelChanged       int `json:"pixelChanged"`
      WirePaired         int `json:"wirePaired"`
      WireChanged        int `json:"wireChanged"`
      WireMoved          int `json:"wireMoved"`
      WireMissing        int `json:"wireMissing"`
      WireExtra          int `json:"wireExtra"`
      // Violations counts rule violations from BOTH planes: every
      // Entry.BodyViolations element AND every Entry.HeaderDiff element
      // whose Type == "violation". Task 8's review found headers flattening
      // violations into "changed", which made the exit-2 bullet below
      // inexpressible for headers — a header rule violation gated at exit 1.
      // HeaderDiff.Type carries the outcome
      // ("changed"|"added"|"removed"|"tolerated"|"violation"); counting only
      // BodyViolations here reintroduces that defect at the consumer.
      // Tolerated entries on either plane are NOT violations.
      Violations         int `json:"violations"`
      HopNew             int `json:"hopNew"`
      HopGone            int `json:"hopGone"`
      UnexpectedStatuses int `json:"unexpectedStatuses"`
      Conformance        int `json:"conformance"`
  }
  type Summary struct {
      Schema             string              `json:"schema"`
      App                string              `json:"app"`
      Flow               string              `json:"flow"`
      A                  RunRef              `json:"a"`
      B                  RunRef              `json:"b"`
      Verdict            string              `json:"verdict"` // "pass" | "changed" | "failed" | "quarantined"
      Checkpoints        []CheckpointVerdict `json:"checkpoints"`
      Wire               Wire                `json:"wire"`
      Sections           []Section           `json:"sections"`
      Hops               HopDiff             `json:"hops"`
      UnexpectedStatuses []StatusFinding     `json:"unexpectedStatuses"`
      Perf               PerfResult          `json:"perf"`
      Conformance        []ConformanceFinding `json:"conformance"`
      Capture            CaptureBanner       `json:"capture"`
      Counts             Counts              `json:"counts"`
      Gates              []string            `json:"gates"` // human-readable reasons the verdict is "failed"
      // Budgets is the configurable CI-gate wire contract
      // (closed-loop-round-one item 1): one entry per PLANE that
      // retrace.yaml's `gates:` key names, never one per plane that merely
      // exists. A plane `gates:` does not mention gets no entry at all —
      // "not gated" and "gated at a threshold of zero" are different
      // configurations (a real `budget_pct: 0` legitimately means "must be
      // pixel-identical"), and a Go zero value cannot carry both meanings,
      // so the zero-value Gate is simply never constructed.
      //
      // Pixel is the exception, and NOT one this task implements: Task 3's
      // `applyDefaults` fills `gates.pixel` from `thresholds.gate` when the
      // key is absent, so by the time Build sees a Config the pixel plane is
      // never missing. "Absent means not gated" is correct for wire, hop and
      // perf, which have no default; applying it to pixel would silently
      // ungate the one plane that IS gated today, at 0.1. Do not add a
      // pixel special case here — read what config hands you.
      //
      // `fail_on` (also
      // consumed here, not defined here — Task 3's config package owns the
      // yaml shape for both keys) says which of these plane names can turn
      // Verdict to "failed"; a plane can be measured and reported without
      // being allowed to fail the build. Named `Budgets`, not `Gates`,
      // because `Gates []string` above already answers to that json key —
      // Task 13's worst-first score is `100 * len(Gates)` against exactly
      // that field, and a second field claiming the same wire key would
      // silently shadow it. `Gate.Failed` is computed from `Observed`
      // against `Threshold` once, in `Build`, per the Global Constraint on
      // zero values: this is the constraint's sixth instance in this plan,
      // and it needs a test that FAILS if an unconfigured plane is ever
      // treated as passing, not just code that happens to get it right.
      Budgets            []Gate              `json:"budgets"`
      // Quarantined lists the sides excluded from this comparison because
      // their own capture-trust verdict was not "ok" (Task 6's `Assess`
      // produces that verdict; see the cross-reference there). Empty unless
      // `--allow-degraded` was NOT passed and at least one side warranted it.
      Quarantined        []Quarantine        `json:"quarantined,omitempty"`
  }
  // Gate is one configured CI budget for one diff plane, read from
  // retrace.yaml's `gates:` map (e.g. `gates: {pixel: {budget_pct: 2}}`).
  type Gate struct {
      Plane     string  `json:"plane"`      // "pixel" | "wire" | "hop" | "perf"
      Threshold float64 `json:"threshold"`
      Observed  float64 `json:"observed"`
      Failed    bool    `json:"failed"`
  }
  // Quarantine records why one side of a comparison was refused instead of
  // diffed. Task 6 owns the verdict this keys on and Task 10 (here) is
  // where "not ok" becomes a refusal instead of a diff result.
  type Quarantine struct {
      Side   string `json:"side"`   // "a" | "b"
      Reason string `json:"reason"` // the runs.CaptureTrust.Summary that triggered it
  }
  type CaptureBanner struct {
      A runs.CaptureTrust `json:"a"`
      B runs.CaptureTrust `json:"b"`
  }
  type BuildInput struct {
      App, Flow     string
      A, B          RunRef
      Cfg           *config.Config
      Options       Options
      WantImages    bool
      OutDir        string       // where diff/overlay PNGs are written (usually B's run dir)
      // AllowDegraded disables the default quarantine of a non-ok side
      // (--allow-degraded). false is the safe default: a run that never
      // sets it still refuses to diff a broken capture.
      AllowDegraded bool
  }
  func Build(in BuildInput) (Summary, error)
  // OptionsFor is the ONE place BuildInput.Options is assembled from a
  // config plus two manifests. cmd_diff, serve and export all call it —
  // an earlier draft left Options "caller-supplied" with no step telling
  // any caller to populate it, and the result was that wire sections and
  // deviations were silently empty on every real run while the unit tests,
  // which pass Options directly, stayed green.
  func OptionsFor(cfg *config.Config, a, b runs.Manifest) (Options, error)
  func ExitCode(s Summary) int          // 0 pass, 1 changed, 2 failed, 3 quarantined/could-not-evaluate
  func RenderText(w io.Writer, s Summary)
  ```

**Verdict rules (the CI contract — assert each one in a test):**
- `quarantined` (exit 3) takes priority over everything below: if either
  resolved side's `runs.CaptureTrust.Status != trace.VerdictOK` and
  `--allow-degraded` was not passed, `Build` sets `Quarantined` and returns
  immediately — it does not compute checkpoints, wire, hops, gates or any
  other field, because a comparison against a side we already believe is
  broken is not evidence of anything. This is deliberately **wider** than
  the `capture.Fatal` check the `failed` bullet below still uses on its own
  terms: quarantine also catches `suspect`, which `Fatal` does not (see
  Task 6). `--allow-degraded` disables only this early return; a fatal
  side that slips through it still lands the run on `failed` via
  `capture.Fatal`, unchanged from today.
- `failed` (exit 2) if not quarantined and ANY of: a rule `Violation`
  exists **on either plane — `Entry.BodyViolations`, or an
  `Entry.HeaderDiff` element with `Type == "violation"`** (see `Counts`
  above; checking only the body plane gates a header violation at exit 1,
  which is the defect Task 8's review found); `RequiredRouteFailures` is non-empty; `UnexpectedStatuses` is
  non-empty; `Perf.Status == "over"`; `capture.Fatal` is true for either
  side; a `Budgets` entry has `Failed == true` for a plane named in
  `fail_on`. Unexpected ≥400 fails the run **regardless of pixel/wire
  results** — the spec's explicit scenario.
- `changed` (exit 1) if not failed and any of: a checkpoint verdict is not
  `ok`; `Wire` has changed/moved/missing/extra entries; `HopDiff` has new or
  gone routes or a deviating service count; **`Conformance` contains any
  finding whose `Kind` is NOT `"unchecked"`**; a
  `Budgets` entry has `Failed == true` for a plane NOT named in `fail_on`
  (measured and reported, but not allowed to fail the build).
- `pass` (exit 0) otherwise.

**The `"unchecked"` conformance kind, and why it sits on neither side of
that line.** Added by Task 9's review: it means "the checker could not
verify this" — an unresolvable `$ref`, an unparseable body, or a body
`trace.Redactor` truncated at `maxBody`. It must never be silently treated
as a pass, which is why it is a finding at all rather than an empty list.
But it must not fail or change a run either: redaction truncation is
routine, so gating on it would mark nearly every run `changed` and the gate
would be turned off within a week — the same fate as any noisy guard.

So `unchecked` is **reported and verdict-neutral**. `retrace diff --json`
and `summary.json` must surface these findings plainly enough that a reader
who sees `"verdict": "pass"` can still tell that part of the response was
never checked; a `pass` next to a silent `unchecked` list is the reassuring
zero value this plan keeps having to dig out. Pin BOTH directions: a
conformance list containing only `unchecked` entries verdicts `pass`, and
one containing a single non-`unchecked` finding verdicts `changed`.

**Gate budgets are computed once, per plane, not per call site.** For each
plane `retrace.yaml`'s `gates:` map configures (`cfg.Gates`, Task 3's
shape — Task 10 reads it, does not redefine it), `Build` derives `Observed`
from that plane's own data, sets
`Failed = Observed > Threshold`, and appends one `Gate` to `Budgets`. A
plane `gates:` never mentions gets no `Gate` at all — not a `Gate` with
`Threshold: 0` — so "unconfigured" and "configured to zero tolerance" stay
distinguishable, per the Global Constraint on zero values. `cfg.FailOn`
(also read, not redefined) names which plane budgets can move `Verdict`;
the rest surface in `Budgets`/`--json` for a reader to see but cannot fail
the build on their own.

**`Observed` is a percentage on every plane, because `Threshold` always is.**
`Threshold` comes from `budget_pct`, so a plane whose `Observed` is a raw
count makes `Failed = Observed > Threshold` compare a count against a
percentage — under that reading three changed wire entries out of a thousand
fail a `budget_pct: 2` gate. An earlier draft of this task specified exactly
that for `"wire"`; Task 10's implementer caught it. The four planes:

- `"pixel"` → the worst per-checkpoint `DiffPct`.
- `"wire"` → changed entries / total entries × 100.
- `"hop"` → `ServiceCounts` entries with `Deviates == true`, as a percentage
  of all entries.
- `"perf"` → percent **over** budget, `(MeasuredMs - BudgetMs) / BudgetMs *
  100`, so `0` means "exactly at budget" and `budget_pct: 10` means "10%
  over is allowed". Not `Measured/Budget*100`, which would put "at budget" at
  100 and force every threshold on this one plane to be written around 100
  while the other three are written near 0.
- `BudgetMs` absent or zero emits **no perf gate at all** — never a `Gate`
  carrying `Inf`, `NaN`, or a `0` that reads as clean.

Pin the unit with a fixture on which the count reading and the percentage
reading disagree: **3 changed wire entries out of 1000 against
`budget_pct: 2` must PASS**. A 3-of-4 fixture fails under both readings and
therefore pins neither.

**No evidence, no gate — on all four planes.** Returning `0` when there was
nothing to measure reports a **clean gate** for a plane that captured
nothing: a run whose B side paired no wire entries at all passes its wire
budget. "No data" is not "0% changed", and a reassuring number derived from
an absence of evidence is what the Global Constraint on zero values forbids.
So `WirePaired == 0`, `len(ServiceCounts) == 0` and `len(Checkpoints) == 0`
all behave exactly as `BudgetMs == 0` already does on perf: `budgetsOf` emits
no `Gate` for that plane, the same rule as a plane `gates:` never mentions.
Pin all four the same way.

An earlier wording scoped this to "the three planes that divide", which was
a description of the mechanism rather than the rule. Pixel does not divide —
it takes a max over `Checkpoints` — but a `0` from an empty max asserts
exactly the same false thing as a `0` from an empty denominator, and
`applyDefaults` fills `gates.pixel` from `thresholds.gate` whenever the key
is absent, so the pixel gate is essentially always emitted. `BUDGET: pixel
0.10% → 0.00% ok` on a run that captured no screenshots is the defect this
rule exists to remove.

**This does not hide a broken capture, because that case never reaches
`budgetsOf`.** `capture/trust.go` raises `no-screenshots` at
`VerdictDegraded` when `ExpectedCheckpoints > 0 && Checkpoints == 0 &&
TestExitCode == 0`, so a run whose checkpoints went missing is already
non-`ok` and `quarantineCheck` refuses it first. Suppressing the gate can
therefore only drop a plane that genuinely had no subject — an API-only flow
with no checkpoints, which is a correct configuration, not a failure. Under
`--allow-degraded` a degraded side does reach `budgetsOf` and loses its pixel
gate; that is intended, and the `no-screenshots` reason still prints in the
capture-trust banner, which is a truer statement than a gate reading
`0.00% ok`.

**Array-valued fields on the wire types marshal as arrays, never `null` and
never absent.** Initialise the slices; carry no `omitempty` on an array
field. `null`, absent and `[]` are three encodings of one meaning here —
"no entries" — and the distinction a consumer would null-guard for is not
actually carried: `budgetsOf` returns nil both when no gates are configured
and when gates are configured but none are measurable. The cost of the three
encodings is a branch in every consumer, and the consumer that forgets it
crashes rather than misbehaving quietly — `summary.budgets.map(...)` throws.
Since "no evidence, no gate" applies to all four planes, an ordinary API-only
flow now reaches that case. Pin it with a test that marshals a **fully
empty** `Summary` and asserts every array field encodes as `[]`; a golden
built from hand-populated slices cannot reach it.

`--no-fail` computes and reports every gate exactly as above but forces
`ExitCode` to `0` for the verdicts `changed` and `failed` — for a reporting
run that must not break the build.

**`--no-fail` suppresses findings, not inability to run.** A `quarantined`
verdict still exits **3** under `--no-fail`, alongside config and I/O
failure. Otherwise a report-only CI job reports *success* for a run that was
never compared — and a config error, which is the same class of "could not
evaluate", already exits 3 regardless of the flag, so zeroing quarantine is
also internally inconsistent. A flag meaning "do not break the build on
differences" must not also mean "call it clean when nothing was evaluated".

Pin the zero-value rule with a test that mutates the "no entry when
unconfigured" behavior and watches it fail — e.g. a `Cfg.Gates` with no
`"wire"` key must produce zero `Budgets` entries for `"wire"` even when the
wire diff changed heavily; if a stray `Gate{Plane: "wire"}` ever starts
appearing with `Threshold: 0` (and therefore `Failed: true` on anything
nonzero), that test must be the one that catches it.

**This task owns the exit contract, so it owns the codes it does not
produce as well.** Task 4's review found that a **signal-killed child**
surfaces as `-1` (or `255` once the shell has it), which is outside the
0/1/2/3 contract entirely — so a test run killed by CI's timeout or by
Ctrl-C is indistinguishable from garbage rather than reporting a defined
status.

**The signal reaches this task as data, not as a process status.**
`retrace diff` execs nothing, so there is no child here to kill — an earlier
draft asked for "a test that kills a child", which in this task's scope
would only kill the `retrace` binary itself and prove nothing. What actually
happens is that `cmd_run.go` sets `exitCode = ee.ExitCode()`, which is `-1`
for a signal-killed test command, and writes it straight into the manifest
as `runs.Test{ExitCode: ...}`. A run killed by CI's timeout or by Ctrl-C
therefore sits on disk as a manifest with a **negative `Test.ExitCode`**,
and this task reads those manifests.

Such a run's hop stream is truncated at the moment of the kill, so diffing
it against a complete reference reports every un-run hop as a "gone" hop — a
screenful of fabricated regressions from a run that never finished. That is
the false-positive class that teaches a team to ignore the plane.

Ruling: a manifest carrying a negative `Test.ExitCode` did not complete. It
takes the **quarantine / could-not-evaluate** path, never the normal diff
path, and `retrace diff` exits **3** on it — "could not evaluate", alongside
config and I/O failure, never `1`, which would assert that differences were
found. Pin it with a **fixture manifest** carrying `ExitCode: -1`; no child
process is involved. `retrace run`'s own pass-through of the test command's
code is Task 4's deliberate decision and stays as it is — see the follow-up
below.

Two constraints on that test, both already paid for once in this project:
assert through a **BUILT binary** (`go run` collapses every non-zero status
to 1, so an assertion through it passes only for the one case that does not
matter), and pin the **literal numbers** rather than the named constants —
Task 1 shipped `exitDiff`/`exitGate` pinned only by constant assertion
because no subcommand emitted them yet, and this is the task where they
finally do.

- [ ] **Step 1: Write the failing summary test**

```go
func TestUnexpected500FailsTheRunEvenWhenPixelsAndWireAreClean(t *testing.T)
func TestARuleViolationExitsNonZero(t *testing.T)
func TestAVolatileFieldUnderAnIso8601RuleProducesNoDiffEntry(t *testing.T) // the spec's wire scenario
func TestAMaskedRegionDoesNotAffectTheCheckpointVerdict(t *testing.T)      // the spec's pixel scenario
func TestAnAddedDownstreamCallMarksTheFlowChanged(t *testing.T)            // the spec's hop scenario
func TestOneJsonDocumentCarriesCheckpointsWirePairsAndHopDeltas(t *testing.T) {
	// the "Agent gate" scenario: marshal the Summary, unmarshal into
	// map[string]any, assert the presence and shape of "checkpoints",
	// "wire".paired, "hops".newRoutes, "verdict", "counts" — an LLM must
	// judge a change from ONE document without parsing human output.
}
func TestRelayFoldingIsOnByDefaultInBuild(t *testing.T) {
	// Two runs whose chains differ ONLY by a transparent relay
	// (client->bff vs client->edge->bff), built through Build with a
	// config that says nothing about collapsing — the verdict must be
	// "pass" and Hops.NewRoutes must be empty.
	//
	// This asserts the DEFAULT, not the feature: Task 9's own folding test
	// sets the option explicitly and therefore passes either way. This is
	// the test that fails if the field is ever flipped back to a
	// positive `Collapse bool`, which no caller here names.
}
func TestACaptureTrustBannerRidesAlongInJsonAndText(t *testing.T)
func TestANonOkSideIsQuarantinedByDefault(t *testing.T) {
	// B's manifest carries CaptureTrust{Status: broken}. Build must set
	// Quarantined (naming side "b" and Fatal's own reason), Verdict must be
	// "quarantined", and NONE of Checkpoints/Wire/Hops/Budgets may be
	// populated — a quarantined Build is not a partial diff.
}
func TestAllowDegradedOverridesQuarantine(t *testing.T) {
	// same input as above, BuildInput.AllowDegraded set: Build
	// proceeds to a real comparison, and the fatal side still lands the run
	// on "failed" via capture.Fatal, not "quarantined".
}
func TestAnUnconfiguredPlaneGetsNoGateEntry(t *testing.T) {
	// cfg.Gates has no "wire" key; the wire diff changes heavily. Budgets
	// must contain no Gate{Plane:"wire"} at all — the regression this test
	// exists for is a stray zero-Threshold Gate silently appearing and
	// reading as "passed".
}
func TestAZeroBudgetGatesOnAnyDifference(t *testing.T) {
	// Gate.BudgetPct is *float64, NOT float64 — that is how "absent" and
	// "explicitly 0" stay distinguishable, since a bare float cannot carry
	// both. So the configured-zero case is `BudgetPct != nil &&
	// *BudgetPct == 0`, never `BudgetPct == 0` (which does not compile and
	// would mean the wrong thing if it did).
	//
	// With budget_pct explicitly 0, any nonzero DiffPct must set
	// Gate.Failed true: zero-but-configured means "must be pixel-identical",
	// and is not the same as absent.
}
func TestFailOnDeterminesWhichBudgetCanFailTheBuild(t *testing.T) {
	// a "pixel" Gate.Failed with fail_on:["wire"] only  → Verdict "changed",
	// not "failed"; the same Gate with fail_on:["pixel"] → Verdict "failed".
}
func TestNoFailForcesExitZeroButStillReportsGates(t *testing.T) {
	// --no-fail on a run with a failing Gate: ExitCode is 0, but
	// Summary.Budgets still names the failed plane — a reporting run must
	// not also blind the reader.
}
func TestSummaryJsonShapeIsStable(t *testing.T) {
	// golden: marshal a fixed Summary and compare against
	// testdata/summary.golden.json. Field names are an API — a rename is a
	// breaking change for every agent consuming it.
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement `Build`**

```go
// Build produces the one document every consumer reads: the CLI's text
// report, `--json`, the review queue, the static export, and any agent.
// There is no second aggregation path — if a surface shows something, it
// came from here.
func Build(in BuildInput) (Summary, error) {
	s := Summary{Schema: SummarySchema, App: in.App, Flow: in.Flow, A: in.A, B: in.B}
	s.Capture = CaptureBanner{A: in.A.Manifest.Capture, B: in.B.Manifest.Capture}

	// --- quarantine, checked before anything else is compared. A capture
	// whose own trust verdict is not "ok" (Task 6's Assess) makes every
	// downstream comparison confident nonsense, so this returns immediately
	// rather than computing a partial Summary. --allow-degraded is the only
	// way past it; it lives on BuildInput alongside WantImages because
	// Build, not cmd_diff alone, is the one place every caller (CLI, serve,
	// export) goes through.
	if !in.AllowDegraded {
		if q := quarantineCheck(in.A, in.B); len(q) > 0 {
			s.Quarantined = q
			s.Verdict = "quarantined"
			return s, nil
		}
	}

	// --- pixel, per checkpoint, by name union so a checkpoint that
	// appeared or vanished is its own verdict rather than a silent skip.
	// This loop is written out rather than elided because it is where the
	// spec's two pixel scenarios actually live: masks must be converted
	// and passed (an earlier draft asserted
	// TestAMaskedRegionDoesNotAffectTheCheckpointVerdict against a loop
	// that never mentioned masks), and a trim request recorded at capture
	// must be honoured here, at compare time.
	for _, name := range checkpointUnion(in.A.Manifest, in.B.Manifest) {
		cpA, okA := findCheckpoint(in.A.Manifest, name)
		cpB, okB := findCheckpoint(in.B.Manifest, name)
		switch {
		case !okA:
			s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{Name: name, Verdict: "added"})
			continue
		case !okB:
			s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{Name: name, Verdict: "missing"})
			continue
		}
		aPNG, errA := os.ReadFile(filepath.Join(in.A.Dir, cpA.File))
		bPNG, errB := os.ReadFile(filepath.Join(in.B.Dir, cpB.File))
		if errA != nil || errB != nil {
			// Unreadable is its own verdict. "I could not compare this"
			// must never render as "this did not change".
			s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{Name: name, Verdict: "unreadable"})
			continue
		}
		// config.Rect → pixel.Rect, via the single conversion function.
		// MasksFor resolves flow-specific masks, then top-level, then the
		// "*" wildcard (Task 3).
		masks := pixel.RectsFrom(in.Cfg.MasksFor(in.Flow, name))
		res, imgs, err := pixel.Compare(aPNG, bPNG, pixel.Options{
			Masks:         masks,
			GateThreshold: in.Cfg.Thresholds.Gate,
			FineThreshold: in.Cfg.Thresholds.Fine,
			WantDiff:      in.WantImages,
			WantOverlay:   in.WantImages,
			// Either side asking for a trim trims both — comparing a
			// trimmed shot against an untrimmed one would be a geometry
			// mismatch invented by the tool.
			Trim: cpA.Trim || cpB.Trim,
		})
		if err != nil {
			s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{Name: name, Verdict: "unreadable"})
			continue
		}
		v := CheckpointVerdict{
			Name: name, DiffPct: res.DiffPct, DiffPctFine: res.DiffPctFine,
			NumDiff: res.NumDiff, Mismatch: res.Mismatch, Overlap: res.Overlap,
			Verdict: "ok",
		}
		if res.TrimA != nil || res.TrimB != nil {
			v.Trimmed = &TrimRects{A: res.TrimA, B: res.TrimB}
		}
		if res.DiffPct > in.Cfg.Thresholds.Gate || res.Mismatch {
			v.Verdict = "changed"
		}
		if in.WantImages {
			v.Images = writeCheckpointImages(in.OutDir, name, cpA, cpB, imgs) // "" for any it did not write
		}
		s.Checkpoints = append(s.Checkpoints, v)
	}
	// LAYOUT CONTRACT for writeCheckpointImages. Three tasks read these
	// paths (T12 RenderText, T13 serve, T16 export) and only this one
	// writes them, so the layout is pinned here rather than inferred.
	// Under OutDir:
	//
	//   diff/shots/<name>.png      overlay/shots/<name>.png
	//
	// The `shots/` level is the SECOND component, not the first. That is
	// not aesthetic: Task 13's safeShotPath is `filepath.Join(dir, "shots",
	// base+".png")` and rejects any name containing a separator, so every
	// side it serves must be a directory with a `shots/` child — which is
	// exactly what a run directory already is. Putting the side first
	// (`shots/diff/<name>.png`) would make the diff sides the only ones
	// safeShotPath could not address, and Task 13 would need a second
	// path builder for them.
	//
	// CheckpointImages.Diff/.Overlay are those two strings, OutDir-relative
	// ("diff/shots/receipt.png"). CheckpointImages.A/.B are NOT written
	// here — the A- and B-side PNGs already exist in their own run
	// directories, so A/B carry that run's own run-dir-relative path
	// ("shots/receipt.png", the same string as runs.Checkpoint.File) and
	// are resolved against Summary.A.Dir / Summary.B.Dir. Copying them
	// would double every artifact for no reader; Task 16's export is the
	// one place that copies, because a shipped tree must be self-contained.

	// --- wire, from each side's client-edge hops
	hopsA, _, err := runs.ReadHops(filepath.Join(in.A.Dir, "wire.jsonl"))
	...
	s.Wire = DiffWire(hopsA, hopsB, in.Options)
	s.Sections = BuildSections(s.Wire.Paired, s.Wire.Groups)

	// --- hops, from the full chain; absent on a standalone run, and that
	// is reported as "not captured", never as "no differences".
	chainA, _, _ := runs.ReadHops(filepath.Join(in.A.Dir, "hops.jsonl"))
	chainB, _, _ := runs.ReadHops(filepath.Join(in.B.Dir, "hops.jsonl"))
	if chainA != nil || chainB != nil {
		// NoCollapse is deliberately not set: folding is on by default,
		// and the default is what every real run gets. Task 9's
		// NoCollapse field is inverted precisely so that this call site
		// — which cannot name every field — cannot silently turn folding
		// off by omitting it.
		s.Hops = DiffHops(chainA, chainB, HopOptions{
			Normalize: in.Cfg.NormalizePath,
			Expected:  in.Cfg.ExpectedStatuses,
			Require:   in.Cfg.HopRequire,
			// CountTolerance is left at zero → DefaultCountTolerance.
			// `config.Config` has no CountTolerance field and this plan
			// does not add one: the fallback is the only value part 1
			// ships, so there is nothing here to read.
		})
	}

	// --- auxiliary checks always run against side B (the candidate).
	// Status checks run over the WIDEST record of side B that exists, but
	// each call exactly once: hops.jsonl is a superset of wire.jsonl (the
	// client edge is part of the chain), so `append(hopsB, chainB...)`
	// would report every client-edge 500 twice.
	statusHops := chainB
	if statusHops == nil {
		statusHops = hopsB
	}
	s.UnexpectedStatuses = FindUnexpectedStatuses(statusHops, in.Cfg.ExpectedStatuses)
	s.Perf = CheckPerfBudget(hopsB, in.Cfg.Flows[in.Flow].PerfBudgetMs)
	if in.Cfg.OpenAPI != "" {
		s.Conformance, err = CheckOpenAPI(hopsB, filepath.Join(in.Cfg.Dir, in.Cfg.OpenAPI))
	}

	s.Counts = countOf(s)
	s.Gates = gatesOf(s)
	// budgetsOf builds one Gate per plane cfg.Gates configures — NEVER one
	// per plane that merely exists. See the Budgets field doc: a plane
	// absent from cfg.Gates gets no entry, not a zero-Threshold one.
	s.Budgets = budgetsOf(s, in.Cfg)
	switch {
	case len(s.Gates) > 0 || failingBudget(s.Budgets, in.Cfg.FailOn):
		s.Verdict = "failed"
	case changed(s) || len(s.Budgets) > 0 && anyFailed(s.Budgets):
		s.Verdict = "changed"
	default:
		s.Verdict = "pass"
	}
	return s, nil
}

func ExitCode(s Summary) int {
	switch s.Verdict {
	case "quarantined":
		return 3
	case "failed":
		return 2
	case "changed":
		return 1
	}
	return 0
}

// OptionsFor assembles the diff Options from the config and the two
// manifests. EVERY caller of Build uses it — cmd_diff, serve, export — so
// there is exactly one place where "what the engine was told" is decided.
//
// The Groups lines are the whole reason this function exists. Markers are
// written by the marker door, folded into Manifest.Groups by `retrace run`
// (Task 4), and read HERE. Miss this hop and BuildSections falls back to a
// single unnamed section on every real run: the wire diff still renders,
// nothing errors, no test fails, and the flow-part feature is silently
// dead end to end. That is exactly what happened in the earlier draft.
func OptionsFor(cfg *config.Config, a, b runs.Manifest) (Options, error) {
	rs, err := cfg.Rules()
	if err != nil {
		return Options{}, err
	}
	o := Options{
		// cfg.WireIgnore is []config.WireIgnoreEntry ({Path, Why}), not
		// []string: an un-explained ignore is indistinguishable from one
		// added to silence a real regression, so the config carries the
		// reason. The diff engine has no use for Why — it is documentation
		// for the human reading the config later — so config owns the
		// conversion and hands down plain paths, the same way pixel.RectsFrom
		// converts config.Rect at one seam instead of at every call site.
		WireIgnore: cfg.WireIgnorePaths(),
		Rules:      rs,
		Normalize:  cfg.NormalizePath,
		GroupsA:    a.Groups,
		GroupsB:    b.Groups,
	}
	// TODO(task-11): load the deviations ledger here —
	//   if cfg.Deviations != "" {
	//       ds, err := LoadDeviations(filepath.Join(cfg.Dir, cfg.Deviations))
	//       o.Deviations = ResolveDeviations(ds, a.App, b.App)
	//   }
	// LoadDeviations/ResolveDeviations are Task 11's; referencing them now
	// would not compile. o.Deviations stays nil until then, which is a
	// no-op (TestNilDeviationsToleratesNothing, Task 8).
	return o, nil
}
```

`RenderText` prints, in this order: when `Verdict == "quarantined"`, the
`Quarantined` reasons and nothing else — no checkpoint/wire/hop sections
exist to print; otherwise the capture-trust banner for either side when
non-ok; a per-checkpoint line (`✓ cart   0.00%` / `✗ receipt  2.14%
(fine 3.02%)  diff/shots/receipt.png`, the OutDir-relative path
`CheckpointImages.Diff` carries); a wire section per flow part with
worst-first entries; the hop deltas; a `GATE:` line per entry in `Gates`;
then a `BUDGET:` line per `Budgets` entry (`BUDGET: pixel 2.00% → 3.50%
FAILED`), so a `--no-fail` reporting run still shows every configured
budget even though nothing in `Gates` names it; and a **conformance
section**. Wide values are never truncated — a report an agent must read is
not a dashboard.

**The conformance section is not optional, and `unchecked` gets its own
line.** Task 9 added the fifth `ConformanceFinding.Kind`, `"unchecked"`, for
exactly one reason: an unresolvable `$ref`, an unparseable body or a
redaction-truncated body must never read as a verified pass. If `RenderText`
omits conformance, then in the default human-facing view — everything that is
not `--json` — an `unchecked` finding is **invisible**, which restores the
silent pass at the presentation layer with the producer correctly fixed and
simply unused. Print `unchecked` findings distinguishably from both a pass
and a violation, so "we could not check this" cannot be read as "we checked
it and it was fine".

- [ ] **Step 4: Run — expect PASS.**

- [ ] **Step 5: `retrace diff` CLI**

`cmd_diff.go` flags: `--flow` (required), `--app`, `--a` (default
`reference`, else `previous`), `--b` (default `latest`), `--json`,
`--images` (default true), `--out`, `--allow-degraded` (disable the
default quarantine of a non-ok side — see the cross-reference to Task 6
above), `--no-fail` (compute and print every `Budgets` entry as usual, but
exit `0` for a `changed` or `failed` verdict — for a reporting run that must
not break the build; a `quarantined` verdict still exits 3, see the ruling
above). `--no-fail` is applied last, at the CLI's own exit-code call:
it overrides the code `ExitCode(s)` returns, it does not change `s` or
`ExitCode` itself, so `--json` output is identical with or without it.
Selector resolution: `reference` → `refs.Resolve` (Task 11), otherwise
`runs.FindRun`.

**The stub, and who removes it.** `refs` does not exist yet, so
`resolveSide("reference")` returns a `RunRef{Kind: "none"}` and
`cmd_diff` exits 3 with "no reference bundle: run `retrace ref accept`
first". `--a reference` is the DEFAULT, so this stub is on the main path
and must not survive: **Task 11 lists `cmd_diff.go` as a file it modifies
and removing this stub is one of its steps.** Mark it in the source so it
cannot be missed:

```go
// TODO(task-11): replace with refs.Resolve. Until then the default
// selector errors — see Task 11, which owns this line.
```

`cmd_diff` builds its input in exactly this order — resolve both sides,
read both manifests, then `OptionsFor`. There is no path that constructs
`BuildInput` without it:

```go
a, b := resolveSide(*aSel), resolveSide(*bSel)   // RunRef, Kind bundle|run|none
opts, err := diff.OptionsFor(cfg, a.Manifest, b.Manifest)
if err != nil {
	return usageErr(stderr, err)                 // exit 3: config/IO, not a diff result
}
s, err := diff.Build(diff.BuildInput{
	App: app, Flow: *flow, A: a, B: b, Cfg: cfg,
	Options: opts, WantImages: *images, OutDir: outDir,
	AllowDegraded: *allowDegraded,
})
...
code := diff.ExitCode(s)
if *noFail {
	code = 0   // report every gate, but never break the build
}
```

Tests: `TestDiffExitsZeroOnIdenticalRuns`,
`TestDiffExitsOneWhenAFieldChanged`,
`TestDiffExitsTwoOnAnUnexpected500`,
`TestDiffExitsThreeOnAQuarantinedSide`,
`TestDiffJsonIsParseableAndCarriesTheVerdict`,
`TestDiffNamesTheMissingRunInsteadOfPanicking`,
`TestSectionsComeFromTheManifestsGroups` — **the reading half of Task 4's
`TestRunFoldsMarkersIntoManifestGroups`.** Two run dirs whose manifests
carry groups `["login","checkout"]`; assert the summary's `Sections` are
named after them and that every paired entry lands in the section its
timestamp falls in. Without this test the whole marker → group → section
chain can break at its last link and every other test still passes.

- [ ] **Step 6: Full suites + commit**

```bash
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/diff retrace/cmd/retrace
git commit -m "feat(retrace): unified diff summary with --json and CI exit codes"
```

---

### Task 11: Reference bundles — resolve, accept, reject, rule, deviations ledger

**Files:**
- Create: `retrace/refs/refs.go`, `retrace/refs/refs_test.go`
- **Modify** (not create): `retrace/diff/deviations.go` — Task 8 created it
  with the two type declarations; this task appends the ledger functions.
- Create: `retrace/diff/deviations_test.go`
- **Modify: `retrace/cmd/retrace/cmd_diff.go`** — Task 10 shipped it with a
  deliberate stub that errors on `--a reference`; this task removes the stub
  and routes it through `refs.Resolve`. Without this edit the default diff
  path stays broken.
- Create: `retrace/cmd/retrace/cmd_ref.go`, `retrace/cmd/retrace/cmd_ref_test.go`
- Modify: `retrace/cmd/retrace/main.go` (dispatch `ref`), `.gitignore`

**Interfaces:**
- Produces (used by Tasks 12, 13, 16):
  ```go
  package refs
  const MaxBundleBytes = 8 << 20   // 8 MiB per bundle, enforced at accept
  func BundleDir(cwd, app, flow string) (string, error)   // <cwd>/.retrace-ref/<app>/<flow>/reference; errors on an invalid app/flow
  type Candidate struct {
      RunID    string `json:"runId"`
      Eligible bool   `json:"eligible"`
      Reason   string `json:"reason"`
      Detail   string `json:"detail,omitempty"`
  }
  type Reference struct {
      // Kind is the SAME vocabulary as diff.RunRef.Kind: "bundle" | "run"
      // | "none". Task 10 maps a Reference onto a RunRef by copying this
      // field through unchanged — there is no translation table, because
      // there are no two vocabularies to translate between.
      Kind     string        `json:"kind"`
      Dir      string        `json:"dir"`
      RunID    string        `json:"runId"`
      Manifest runs.Manifest `json:"manifest"`
      Reason   string        `json:"reason,omitempty"`
      History  []Candidate   `json:"history,omitempty"`
  }
  func Resolve(cwd, runsRoot, app, flow string) Reference   // Kind: "bundle" | "run" | "none"
  type AcceptOptions struct {
      Cwd, RunsRoot, App, Flow, RunID string
      // MasksFor is the ONLY mask input. The earlier draft carried a flat
      // `Masks []pixel.Rect` alongside it with no precedence rule, which
      // is a coin-flip waiting to be resolved differently by two readers.
      // Masks are per-checkpoint by nature — the whole point is "ignore
      // the clock in the header of THIS screen" — so the function form is
      // the one that can express the requirement. Callers wrap
      // config.MasksFor + pixel.RectsFrom; see Step 3.
      MasksFor func(checkpoint string) []pixel.Rect
      Force    bool
  }
  type AcceptResult struct {
      Dir           string        `json:"dir"`
      Files         []string      `json:"files"`
      RunID         string        `json:"runId"`
      Bytes         int64         `json:"bytes"`
      CaptureStatus trace.Verdict `json:"captureStatus"`
  }
  func Accept(o AcceptOptions) (AcceptResult, error)
  type RejectOptions struct { Cwd, RunsRoot, App, Flow, RunID, OutDir string; Summary *diff.Summary }
  type RejectResult struct {
      Dir   string   `json:"dir"`
      Files []string `json:"files"`
  }
  func Reject(o RejectOptions) (RejectResult, error)
  ```

  The caller-side wiring, written out once so both call sites match — the
  `config.Rect` → `pixel.Rect` conversion happens here, at the boundary,
  through `pixel.RectsFrom` and nowhere else:

  ```go
  o := refs.AcceptOptions{
      Cwd: cwd, RunsRoot: runsRoot, App: app, Flow: flow, RunID: runID,
      MasksFor: func(checkpoint string) []pixel.Rect {
          return pixel.RectsFrom(cfg.MasksFor(flow, checkpoint))
      },
  }
  ```
  ```go
  package diff   // deviations.go — in diff, NOT refs: refs.RejectOptions carries a
                 // *diff.Summary, so refs → diff is the only direction available.
  //
  // `Deviation` and `ToleratedNote` are ALREADY DECLARED, by Task 8, in
  // this same file — Task 8's Options.Deviations and Call.Tolerated
  // reference them, and an undeclared type there is a compile error, not a
  // dormant field. This task adds the ledger functions BELOW those
  // declarations and must not restate them.
  //   type Deviation struct { ID, Status string; Apps [2]string; Method, Path, Reason string }
  //   type ToleratedNote struct { ID, Reason string }
  func LoadDeviations(file string) ([]Deviation, error)
  func ResolveDeviations(ds []Deviation, appA, appB string) []Deviation
  func FindDeviation(ds []Deviation, method, path string) *Deviation
  ```

**`BundleDir` must guard the `app`/`flow` join.** `BundleDir` performs the
same `filepath.Join(root, app, flow, ...)` shape that `retrace/runs`'
`PathsFor` validates, and that Task 1's re-review named as the predicted
next place this exact bug reappears once a second package starts building
this kind of path. Task 11 must validate `app` and `flow` before joining
them here, and must not grow a second copy of the rule: `runs` keeps the
one validation rule (`validateComponent`/`validateComponents`), currently
unexported. Task 11 therefore adds `func ValidateComponents(names
...string) error` to `retrace/runs`, delegating to the existing unexported
`validateComponents` rather than copying its body — one guard body is the
whole rule — and calls that from `BundleDir`, rather than duplicating the
character-class/traversal check inside `refs`. (This wrapper is not added
now; it would sit exported with no caller for nine tasks.)

**`BundleDir` returns `(string, error)`, not a bare `string`.** A *lister*
(`ListFlows`, `FindRun`) fails closed to an empty result because "nothing
found" is a natural and safe answer for it. A path *constructor* has no
natural empty — returning `""` from one invites a caller to
`filepath.Join("", ...)` and land on a relative path rooted at the
process CWD. `BundleDir` is a constructor, the same shape as `PathsFor`,
and `PathsFor` returns an error; so does `BundleDir`:
`func BundleDir(cwd, app, flow string) (string, error)`. Every caller in
this task (`Resolve`, `Accept`, `Reject`) propagates that error the same
way it already propagates everything else.

**Ruling on the bless flow (design §6.4).** flowlens wrote proposals to a
separate `.flowlens-ref-proposed` tree that a human promoted with `ref
bless`. The redesign says: *"No separate bless mode, no bless tokens"* —
`accept` writes the active bundle directly. The safety that the proposed
tree provided is preserved differently: the bundle is a **git-committed
artifact**, so an agent accepting something wrong shows up as a reviewable
diff in the PR rather than as an invisible state change. The deviations
ledger keeps its proposed/approved ceremony for teams that want it, opt-in
per `config.Deviations`.

- [ ] **Step 1: Write the failing resolve test**

```go
func TestResolvePrefersTheCommittedBundle(t *testing.T)
func TestResolveFallsBackToTheNewestEligibleRun(t *testing.T)
func TestARunWithANonOkCaptureIsIneligibleAndSaysWhy(t *testing.T) {
	// "unknown capture is not ok: a run predating the verdict cannot vouch
	// for itself" — a manifest with no capture block is ineligible too.
}
func TestADirtyTreeRunIsIneligible(t *testing.T)
func TestNoEligibleRunReportsTheCandidatesItTried(t *testing.T) {
	// Reference.History must name the runs and the reason each was
	// rejected — an empty state that says only "no reference" is useless.
}
```

- [ ] **Step 2: Run — expect FAIL. Step 3: implement `Resolve`** per
  flowlens `src/reference.mjs`, minus the git-ancestor check (that needed a
  configured trunk name; here `Git.Dirty == false` plus a non-fatal capture
  verdict is the bar, and the reason strings say so).

- [ ] **Step 4: Write the failing accept test**

```go
func TestAcceptWritesACompactCommittableBundle(t *testing.T) {
	// bundle dir contains manifest.json, wire.jsonl, hops.jsonl, shots/;
	// misses.jsonl and any logs are NOT carried — they are not reference
	// material. RunID in the manifest keeps the provenance of the run it
	// was promoted from, while the directory is the literal "reference".
}
func TestAcceptRedactsMaskedRegionsIntoTheStoredShots(t *testing.T) {
	// masks previously only gated comparison; a blessed shot once reached a
	// reference bundle with legible card data. Accept is the ONLY place
	// this can be fixed, so it re-encodes each masked shot.
}
func TestAcceptRefusesAnUnreadableShotRatherThanPromotingItUnredacted(t *testing.T)
func TestAcceptReplacesRatherThanMergesTheBundle(t *testing.T) {
	// a screen deleted from the flow must not linger in the reference.
}
func TestAcceptRefusesToExceedTheSizeBudgetNamingTheOffender(t *testing.T)
func TestAcceptWarnsButProceedsOnANonOkCapture(t *testing.T) {
	// promotion is explicit, so an untrustworthy capture is warned about,
	// never promoted silently — that is how a proxy-down run becomes the
	// source of truth.
}
```

- [ ] **Step 5: Implement `Accept`** — `os.RemoveAll` then copy; for each
  shot call `o.MasksFor(checkpointName)` and, when it returns a non-empty
  list, run `pixel.Decode` → `pixel.ApplyMasks` → `pixel.Encode`; plain
  copy otherwise. `MasksFor` is already `[]pixel.Rect` — the
  `config.Rect` → `pixel.Rect` conversion happened once, in the caller's
  closure above, via `pixel.RectsFrom`. Do not convert again here and do
  not reach for `config` from this package. Sum bytes as it goes and fail
  with `fmt.Errorf("reference bundle for %s/%s would be %s, over the %s
  budget — the largest file is %s (%s); add a mask, trim the flow, or raise
  MaxBundleBytes deliberately", ...)`.

- [ ] **Step 6: `Reject` + deviations**

`Reject` copies the failing run's manifest, wire/hops, shots, plus the
`diff.Summary` as `summary.json`, into `<OutDir>/<app>__<flow>__<runId>/` —
a repro bundle someone can attach to a bug. Test:
`TestRejectEmitsASelfContainedReproBundle`.

`retrace/diff/deviations.go` already exists — Task 8 created it with the
`Deviation` and `ToleratedNote` declarations, because its own structs
reference them. **Append to it; do not re-declare those two types.** The
ledger logic ports `src/deviations.mjs` verbatim in spirit: an agent can
append `status: "proposed"` (visible, git-diffable, inert); only a human
flipping it to `approved` makes retrace honor it. Tests:
`TestOnlyApprovedDeviationsApply`, `TestAppPairMatchingIsOrderIndependent`,
`TestAMalformedEntryIsAnErrorNamingItsIndex`.

Wire `Deviations` into `diff.Options` by filling **Task 10's
`TODO(task-11)` in `OptionsFor`** — that is the one assembly point, so
there is no second place for a caller to forget. A matched deviation
**annotates** `Call.Tolerated` rather than removing the entry, so it stays
visible in every consumer's output and only stops counting as a finding.
Tests in `retrace/diff`: `TestASanctionedDeviationAnnotatesButDoesNotHide`,
and `TestOptionsForLoadsTheLedgerNamedByConfig` — the seam itself, which
is what would otherwise be silently skipped.

- [ ] **Step 7: `retrace ref` CLI**

```
retrace ref list   [--app N] [--flow N] [--json]     → what each flow resolves to, and why
retrace ref accept --flow N [--app N] [--run SELECTOR]
retrace ref reject --flow N [--app N] [--run SELECTOR] [--out DIR]
retrace ref rule   --flow N --scope req|resp --field PATH --matcher NAME [--method M] [--path GLOB]
```
`ref rule` calls `config.AppendWireRule` — the same code path the review
queue's `rule` verb uses, so the CLI and the UI cannot drift.
Tests: `TestRefAcceptThenDiffAgainstReferenceExitsZero` (the round trip that
proves the whole chain), `TestRefRuleAppendsToTheOverlayAndSilencesTheDiff`.

**This task introduces the SECOND process on the overlay, and owes it a
cross-process lock.** Task 3 made `AppendWireRule` safe within one process
(a mutex plus a same-directory temp-file/rename) and its doc scopes the
no-loss guarantee to exactly that. Readers are already safe across
processes — 4000 concurrent `Discover` calls against 6 writer processes
produced zero errors, because the rename is atomic. **Writers are not:**
measured, 3 processes × 12 appends landed 12, 12 and 14 of 36, and every
lost call returned a nil error. Silent loss with a nil error is the same
failure shape the atomicity fix was written to eliminate; it simply moves
up a level when a second process appears.

`ref rule` is that second process, and it is the normal case rather than
an exotic one: a developer runs `retrace ref rule` in a terminal while the
review server (Task 13) is open in a browser, or while a capture run is
in flight. So this task adds a lock around the read-modify-write — an
`O_EXCL` lockfile beside the overlay, or `flock` on a sidecar — held
across the read, the merge and the rename, with a bounded wait and a clear
error rather than an unbounded block. Whichever you choose, state why in
the code, and widen `AppendWireRule`'s doc clause to match the guarantee
it can then actually make. The test is the one Task 3 could not write:
N separate processes appending concurrently must land N rules.

**Step 7b: remove Task 10's stub.** `cmd_diff.go` currently carries a
`TODO(task-11)` where `--a reference` — the DEFAULT selector — errors out.
Replace it with `refs.Resolve`, mapping the returned `Reference` onto a
`diff.RunRef` by copying `Kind` straight through (both use
`"bundle" | "run" | "none"`; there is nothing to translate). The
round-trip test above is what proves the stub is gone: it exercises the
default selector, so it cannot pass while the stub stands.
`TestDiffAgainstAMissingReferenceExplainsHowToCreateOne` keeps the good
half of the stub's behaviour — `Kind == "none"` still exits 3 naming
`retrace ref accept`, rather than diffing against nothing.

- [ ] **Step 8: `.gitignore`**

Add, with a comment matching the existing house style:
```gitignore
# Reference bundles ARE committed — they are the blessed artifact a diff
# runs against. The repro bundles `retrace ref reject` emits are not.
.retrace/repro/
```
(`.retrace/runs/` is already ignored; `.retrace-ref/` must NOT be ignored.)

- [ ] **Step 9: Full suites + commit**

```bash
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/refs retrace/cmd/retrace .gitignore
git commit -m "feat(retrace): reference bundles with accept/reject/rule verbs and deviations ledger"
```

---

### Task 12: Strict replay + `retrace revalidate`

**Files:**
- Create: `retrace/replay/bundle.go`, `retrace/replay/match.go`, `retrace/replay/server.go`, `retrace/replay/revalidate.go`
- Create: `retrace/replay/match_test.go`, `retrace/replay/server_test.go`, `retrace/replay/revalidate_test.go`
- Create: `retrace/cmd/retrace/cmd_replay.go`, `retrace/cmd/retrace/cmd_revalidate.go`
- Modify: `retrace/cmd/retrace/main.go`

**Interfaces:**
- Produces:
  ```go
  package replay
  type Key struct {
      Method string `json:"method"`
      Path   string `json:"path"`
      Query  string `json:"query,omitempty"`
  }
  type Exchange struct {
      Key     Key               `json:"key"`
      ReqBody any               `json:"reqBody,omitempty"`
      Status  int               `json:"status"`
      Headers map[string]string `json:"headers,omitempty"`
      Body    string            `json:"body"`
      Seq     uint64            `json:"seq"`
      // used counts how many times this exchange has already been served.
      // It is unexported because it is replay state, not bundle content:
      // marshalling it would put a runtime counter into an artifact. It is
      // what makes repeated identical calls come back in recorded order
      // (see Step 3) — a poll-until-ready flow that got the same first
      // response forever would hang.
      used int
  }
  type Bundle struct { Dir string; Manifest runs.Manifest; Exchanges []Exchange }
  func LoadBundle(dir string) (*Bundle, error)   // reads wire.jsonl through runs.ReadHops
  type MissField struct {
      Field    string `json:"field"`
      Expected string `json:"expected"`
      Actual   string `json:"actual"`
  }
  type Result struct { Hit *Exchange; Miss bool; Nearest *Exchange; Diff []MissField }
  type Request struct { Method, Path, Query string; Body any }
  type Options struct {
      Rules       []rules.Rule
      Normalize   func(string) string
      QueryIgnore []string
      // No MissPath here. The misses file has ONE name and ONE owner:
      // runs.Paths.MissesPath. A second field naming the same file is a
      // second thing to keep in sync, and the loser of that race writes
      // misses nobody reads. NewServer takes the path explicitly.
  }
  func (b *Bundle) Match(r Request, o Options) Result
  type Server struct { http.Handler }
  // NewServer takes the misses path directly — from runs.Paths.MissesPath
  // at the call site. An empty string means "record misses in memory only",
  // which is what the unit tests use.
  func NewServer(b *Bundle, o Options, missesPath string) *Server
  func (s *Server) Misses() []Miss
  func (s *Server) MissCount() int
  type Miss struct {
      TS     time.Time   `json:"ts"`
      Kind   string      `json:"kind"`
      Method string      `json:"method"`
      Path   string      `json:"path"`
      Query  string      `json:"query,omitempty"`
      Diff   []MissField `json:"diff,omitempty"`
      Nearest *Key       `json:"nearest,omitempty"`
  }
  // StatusDrift is nil when the status did not change. Declared here, in
  // the only package that uses it — an earlier draft referenced it from
  // Drift without declaring it anywhere at all.
  type StatusDrift struct {
      Recorded int `json:"recorded"`
      Live     int `json:"live"`
  }
  type Drift struct {
      Method string           `json:"method"`
      Path   string           `json:"path"`
      Status *StatusDrift     `json:"status,omitempty"`
      Fields []diff.FieldDiff `json:"fields,omitempty"`
  }
  type RevalReport struct {
      Flow    string  `json:"flow"`
      Checked int     `json:"checked"`
      Drifts  []Drift `json:"drifts"`
      Verdict string  `json:"verdict"`
  }
  func Revalidate(ctx context.Context, b *Bundle, upstream string, o Options) (RevalReport, error)
  ```

**The central invariant, stated in `server.go`'s doc comment:** *absence is
never agreement.* An unmatched request must never fall through to a
passthrough, a 200, or an empty body — it is a **501** carrying a
field-level explanation, a `misses.jsonl` line, and a non-zero final exit.
That is the entire point of "client deviated" detection.

- [ ] **Step 1: Write the failing match test**

```go
func TestMatchesOnMethodPathThenQueryThenBodySubset(t *testing.T)
func TestAnExtraApiCallNotInTheReferenceIsAMiss(t *testing.T) {
	// the spec's "client deviation caught in CI" scenario.
}
func TestAMissNamesTheNearestExchangeAndTheFieldsThatDiffered(t *testing.T) {
	// no fixture for the path at all → diff names method+path.
	// path matches, query differs → diff names query, expected vs actual.
	// query matches, body differs → field-level diff of the request body.
}
func TestQueryIgnoreMakesAVolatileParamIrrelevant(t *testing.T)
func TestWireRulesDecideEquivalenceNotByteEquality(t *testing.T) {
	// a request body carrying a fresh uuid under a uuid rule still matches.
}
func TestRepeatedIdenticalCallsAreServedInRecordedOrder(t *testing.T) {
	// two recorded GET /cart responses (empty, then one item): the first
	// request gets the first, the second gets the second. Serving the same
	// one twice would make a poll-until-ready flow hang forever.
}
func TestWhenRepeatsAreExhaustedTheLastRecordedResponseRepeats(t *testing.T) {
	// ...but a third call is served the last response rather than missing:
	// a retry loop's extra attempt is not a client deviation.
}
```

- [ ] **Step 2: Run — expect FAIL. Step 3: implement `bundle.go` + `match.go`.**

`LoadBundle` reads the bundle's `wire.jsonl` via `runs.ReadHops` and lowers
each hop into an `Exchange` (`SplitPath` for path/query; `Resp.Body` kept as
the raw recorded string so a replayed body is byte-identical to what was
recorded). `Match` is flowlens `matchRequest` plus two additions the Go side
needs: rule-aware body-subset equivalence, and the `used` counter that gives
repeated identical calls their recorded order.

- [ ] **Step 4: Write the failing server test**

```go
func TestAHitReplaysTheRecordedStatusHeadersAndBody(t *testing.T)
func TestAMissIs501WithAnExplanatoryJsonBodyAndAMissesJsonlLine(t *testing.T)
func TestAMissIsNeverForwardedUpstream(t *testing.T)   // no upstream is even configured
func TestCorsHeadersAreReflectedOnHitsAndMisses(t *testing.T) {
	// a browser consumer blocked by CORS never sees the loud 501 body —
	// it sees a network error, which reads as an app bug. Reflect the
	// request's Origin, never a bare "*".
}
func TestOptionsPreflightIsAnsweredNotMissed(t *testing.T)
```

- [ ] **Step 5: Implement `server.go`.** **One implementation, not a
  choice:** a single `http.HandlerFunc` with no `ServeMux` at all. A mock
  server matches against its own exchange table, so route patterns would
  add nothing but the ServeMux traps in Global Constraints. Concretely
  that means the handler answers *every* method and path itself, including
  the two cases a mux would otherwise have handled for free:

  - `OPTIONS` is answered before the table lookup — a preflight is not a
    recorded exchange and must never be a miss. Reflect the request's
    `Origin`, `Access-Control-Allow-Methods: *`, and echo
    `Access-Control-Request-Headers`.
  - Every other verb falls through to `Match`; a miss is a 501 with the
    explanatory body, never a fallthrough.

  CORS headers are reflected on hits AND misses: a browser blocked by CORS
  never sees the loud 501 body, only a network error, which reads to a
  developer as an app bug rather than as a replay miss. Reflect the actual
  `Origin`, never a bare `*` — with `*` a credentialed request fails.

  The whole handler sits behind `httpguard.Handler(nil, ...)` (extracted in
  Task 4), same as every other loopback listener in this plan.

- [ ] **Step 6: `retrace replay` CLI**

```
retrace replay --ref FLOW [--app N] [--listen 127.0.0.1:0] [--json] -- <test command>
```
Resolves the reference via `refs.Resolve` (a `Kind == "none"` is exit 3 with
the candidate history printed), starts the server, exports
`RETRACE_PROXY_URL` + `RETRACE_RUN_DIR` + `RETRACE_MARKER_URL` exactly as
`retrace run` does, passes `runs.Paths.MissesPath` to `NewServer` as the
misses path (one name for that file, set at the one call site that knows
the run directory), runs the command, then: **exit 2 if `MissCount() > 0`**,
else the command's own exit code. Prints a miss report — one line per miss
with its nearest exchange and field diff.

Tests: `TestReplayExitsTwoAndReportsTheUnmatchedRequest` (the spec scenario,
end to end through a real HTTP client), `TestReplayExitsZeroWhenEveryCallMatched`,
`TestReplayWithoutAReferenceExplainsHowToCreateOne`.

- [ ] **Step 7: `retrace revalidate`**

Re-issues every recorded request against a live `--upstream` and diffs the
responses **with the same rules the wire diff uses**, so a rule-matched
volatile field is not drift. Reports per-field drift; exit 1 when drift
exists, 0 when clean, 2 on a hard gate (an unexpected ≥400 from the live
stack).

Tests: `TestRevalidateReportsAChangedResponseShapePerField` (the spec's
"stale recording detected" scenario), `TestRevalidateDoesNotFlagRuleMatchedVolatileFields`,
`TestRevalidateSendsTheRecordedRequestBodyAndHeaders`.

- [ ] **Step 8: Full suites + commit**

```bash
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/replay retrace/cmd/retrace
git commit -m "feat(retrace): strict replay with miss reporting, and revalidate against a live stack"
```

---

### Task 13: Review queue + `retrace serve` REST (and the shared Origin/Host guard)

**Files:**
- Create: `retrace/serve/queue.go`, `retrace/serve/queue_test.go`
- Create: `retrace/serve/server.go`, `retrace/serve/routes.go`, `retrace/serve/routes_test.go`
- Create: `retrace/cmd/retrace/cmd_serve.go`
- Modify: `retrace/cmd/retrace/main.go`

**Interfaces:**
- Consumes: `core/httpguard` — `httpguard.Handler(allowedHosts []string, h http.Handler) http.Handler`,
  **extracted in Task 4**, which is also its first consumer (the marker
  door). This task adds no second copy and no new guard code; it wraps its
  mux exactly as Task 4's door does.

  **This task owns the one open guard defect, because it is the first task
  to pass a non-nil `allowedHosts`.** Task 4's review probed the guard
  across 27 Host forms, 16 Origin forms and 4 raw-socket shapes and found
  the `nil` and `"*"` paths correct — `nil` is loopback-only, `"*"` still
  enforces `Sec-Fetch-Site`. It also found a **pre-existing** bypass in the
  configured-hosts path involving a `"*:8080"`-shaped entry, which predates
  the extraction and was deliberately NOT fixed in Task 4: changing host
  matching as a ride-along in a fix round is how a security regression gets
  in unexamined. Fix it here, where configured hosts are the subject rather
  than a passenger, with its own test — and pin the two properties Task 4
  established so a later change cannot quietly undo them: `nil` must stay
  loopback-only rather than wide open, and `"*"` must keep enforcing
  `Sec-Fetch-Site`. Both are zero-value traps on a security boundary, and
  the Global Constraint on zero values requires each to be pinned by a test
  that FAILS when the property is violated.
- Produces:
  ```go
  package serve
  type Item struct {
      App      string             `json:"app"`
      Flow     string             `json:"flow"`
      Verdict  string             `json:"verdict"` // diff.Summary.Verdict
      Score    float64            `json:"score"`   // worst-first sort key
      RunID    string             `json:"runId"`
      RefRunID string             `json:"refRunId,omitempty"`
      Counts   diff.Counts        `json:"counts"`
      Capture  diff.CaptureBanner `json:"capture"`
      Gates    []string           `json:"gates,omitempty"`
  }
  type Deps struct {
      Cwd          string
      Cfg          *config.Config
      AllowedHosts []string
      Version      string
      Now          func() time.Time
  }
  func BuildQueue(d Deps) ([]Item, error)
  func SummaryFor(d Deps, app, flow string) (diff.Summary, error)
  func New(d Deps) http.Handler
  // ScoreOf is the ONE definition of the worst-first sort key. It is
  // exported because it is tested directly and because the ordering is
  // part of the REST contract; it is NOT re-derived anywhere else, in Go
  // or in TypeScript — the UI reads Item.score.
  func ScoreOf(s diff.Summary) float64
  ```

**Routes** (JSON; errors `{"error":"..."}`; every one behind
`httpguard.Handler`):
```
GET  /api/health                              → {ok, version}
GET  /api/queue                               → {items: Item[]}          worst first
GET  /api/queue/{app}/{flow}                  → {summary: diff.Summary}
POST /api/queue/{app}/{flow}/accept           → {ok, bundle: {dir, files, bytes}}
POST /api/queue/{app}/{flow}/reject           → {ok, repro: {dir, files}}
POST /api/queue/{app}/{flow}/rule             → {ok, rule, rules: rules.Raw[]}
GET  /api/shots/{app}/{flow}/{side}/{name}    → image/png   side: a|b|diff|overlay
GET  /                                        → embedded retrace-ui (Task 15)
```
`POST .../rule` body: `{"scope":"resp","field":"cart.updatedAt","matcher":"iso8601","method":"GET","path":"/cart"}`.

**Worst-first ordering.** `Score` = `1000` if `Verdict == "failed"`, plus
`100 * len(Gates)`, plus `10 * (HopNew + HopGone)`, plus `PixelChanged`,
plus `WireChanged + WireMissing + WireExtra`. Passing flows score 0 and the
UI collapses them. The formula lives in one exported function,
`ScoreOf(s diff.Summary) float64`, so the UI never re-derives it.

**And it genuinely never does.** The server is authoritative: `Item.score`
is on the wire, the UI sorts by it and renders it, and there is no
TypeScript copy of this formula for the two to drift apart. Task 15's
`src/score.ts` is tone-only (`verdictTone`) for exactly this reason — see
the note there.

- [ ] **Step 1: Write the failing queue test**

```go
func TestQueueIsWorstFirstWithPassingFlowsLast(t *testing.T)
func TestAFlowWithNoReferenceAppearsWithAReasonNotSilentlyMissing(t *testing.T)
func TestQueueSurvivesAnUnreadableRunDirectory(t *testing.T) {
	// one broken flow must not take the whole queue down — it becomes an
	// item whose Verdict is "failed" and whose Gates name the read error.
}
```

- [ ] **Step 2: Implement `BuildQueue`** — for each app/flow under the runs
  root: resolve the reference (A) and the latest run (B), `diff.Build`,
  `ScoreOf`, sort descending, stable by `app/flow` for ties.

- [ ] **Step 3: Write the failing routes test**

```go
func TestGetQueueReturnsItemsWorstFirst(t *testing.T)
func TestPostAcceptUpdatesTheReferenceExactlyAsTheUiWould(t *testing.T) {
	// the spec's "LLM walks the queue" scenario: after POST accept, a
	// fresh GET /api/queue/{app}/{flow} verdict is "pass".
}
func TestPostRuleAppendsAWireRuleAndTheQueueReEvaluatesWithoutThatNoise(t *testing.T) {
	// the spec's "Rule from the UI" scenario, asserted through REST only.
}
func TestPostRejectEmitsAReproBundle(t *testing.T)
func TestEveryRouteIsRegisteredPerMethodAndRejectsCrossOrigin(t *testing.T)
func TestShotPathsCannotEscapeTheRunDirectory(t *testing.T) {
	// GET /api/shots/web/checkout/a/%2e%2e%2f%2e%2e%2fetc%2fpasswd
	// ServeMux's cleaning runs on the STILL-ESCAPED path, so this arrives
	// as literal "../../etc/passwd". Must 400, not read the file.
}
func TestAnUnknownAppOrFlowIs404NotAPanic(t *testing.T)
```

- [ ] **Step 4: Implement `routes.go`**

```go
func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/queue", s.handleQueue)
	mux.HandleFunc("GET /api/queue/{app}/{flow}", s.handleItem)
	mux.HandleFunc("POST /api/queue/{app}/{flow}/accept", s.handleAccept)
	mux.HandleFunc("POST /api/queue/{app}/{flow}/reject", s.handleReject)
	mux.HandleFunc("POST /api/queue/{app}/{flow}/rule", s.handleRule)
	mux.HandleFunc("GET /api/shots/{app}/{flow}/{side}/{name}", s.handleShot)
}

// safeShotPath resolves a checkpoint name to a file inside the run
// directory and nowhere else. ServeMux's path cleaning operates on the
// still-escaped path, so "%2e%2e%2f" reaches us as literal "../" — rooting
// at "/" before Clean, then rejecting any remaining separator, is what
// makes that harmless.
func safeShotPath(runDir, name string) (string, error) {
	clean := path.Clean("/" + name)
	base := strings.TrimPrefix(clean, "/")
	if base == "" || strings.ContainsAny(base, `/\`) {
		return "", fmt.Errorf("invalid checkpoint name %q", name)
	}
	return filepath.Join(runDir, "shots", base+".png"), nil
}
```

**Where the four shot sides come from.** `handleShot` serves
`side ∈ {a, b, diff, overlay}`, and only two of those are files somebody
else already wrote. Resolve them in one place, `shotDirFor(side)`:

`shotDirFor` returns the directory `safeShotPath` joins onto, so each of
the four must be a directory with a `shots/` child — the diff layout
contract in Task 10 Step 3 exists to make that true for all four:

```go
func (s *Server) shotDirFor(sum *diff.Summary, outDir, side string) (string, error) {
	switch side {
	case "a":
		return sum.A.Dir, nil
	case "b":
		return sum.B.Dir, nil
	case "diff", "overlay":
		return filepath.Join(outDir, side), nil // outDir/diff/shots/<name>.png
	}
	return "", fmt.Errorf("unknown side %q", side)
}
```

- `a` → the A-side run (or bundle) directory, `Summary.A.Dir`; its own
  `shots/` is the capture's.
- `b` → `Summary.B.Dir`, likewise.
- `diff` / `overlay` → **generated by `diff.Build`**, into
  `BuildInput.OutDir`. `SummaryFor` therefore calls `Build` with
  `WantImages: true` and `OutDir` set to a per-flow directory under
  `<cwd>/.retrace/diffs/<app>/<flow>/`, and the returned
  `CheckpointVerdict.Images.Diff/.Overlay` are relative to it
  (`diff/shots/<name>.png`), which is the same string this function's
  `filepath.Join(outDir, side)` plus `safeShotPath` rebuilds. Serving a
  `diff`/`overlay` side for a checkpoint whose `Images.Diff` is `""` is a
  404 with `{"error":"no diff image: this checkpoint did not change"}` —
  not an empty 200, which would render as a blank comparison pane and read
  as "identical".

`.retrace/` is already gitignored, so generated diff images never reach a
commit. `safeShotPath` guards the name in every case.

- [ ] **Step 5: `retrace serve` CLI** — `--addr 127.0.0.1:4800` (loopback
  only; a non-loopback bind requires an explicit `--allow-host` and prints a
  warning), `--open`. Reuses `server.Serve`-style graceful shutdown:
  `BaseContext` ties every connection to the CLI's context so Ctrl-C is
  immediate.

- [ ] **Step 6: Full suites + commit**

```bash
go test -race ./core/... ./ensemble/... ./retrace/...
git add retrace/serve retrace/cmd/retrace
git commit -m "feat(retrace): review queue and retrace serve REST verbs behind the shared guard"
```

---

### Task 14: `useAsync` — the shared async-load hook both dashboards use

**Files:**
- Create: `dashboard/design-system/useAsync.ts`, `dashboard/design-system/useAsync.test.ts`
- Create: `dashboard/design-system/vite.config.ts` (vitest config only — happy-dom env)
- Modify: `dashboard/design-system/package.json` (add the `./useAsync` export, a `test` script, and dev deps)

**Why this is a task and not a footnote.** Phase 3's whole-phase review swept
all five shipped views and found the same fetch-on-mount block copied ten
times. Every Important finding in that review except one was a place where
one copy had drifted from the other nine — including the phase's **third**
instance of the same async-race bug class, after two had already been found
and fixed individually. Task 15 builds a *second* dashboard that would
otherwise repeat the pattern from scratch. A ~15-line hook makes the whole
bug class structurally impossible, so it lands before the first view that
would need it.

**Where it lives.** `@ensemble/design-system`. The package is already the
shared dependency of every dashboard app, it is already source-exported
(`main: ./primitives.tsx`, consumed by Vite, not pre-built), and a hook is
exactly the kind of cross-app convention it exists to hold. It ships as a
separate entry point (`@ensemble/design-system/useAsync`) rather than from
`primitives.tsx`, because that file is the visual primitives and importing
it should not pull React state machinery into a component that only wants a
`Badge`.

**Interfaces:**
- Consumes: `react` (peer dependency, already declared).
- Produces (used by Tasks 15 and 18):
  ```ts
  export interface AsyncState<T> {
    data: T | null;
    error: Error | null;
    loading: boolean;
  }
  export function useAsync<T>(fn: () => Promise<T>, deps: readonly unknown[]): AsyncState<T>;
  ```
  Contract, in words, because every consumer depends on all four clauses:
  1. On mount and on every `deps` change it calls `fn()` and reports
     `{data: null, error: null, loading: true}` **synchronously**, before the
     new promise settles. Stale data from the previous deps is never
     rendered against the new deps.
  2. Exactly one resolution wins: the most recent load. Any earlier load
     that settles later is discarded.
  3. A rejection produces `{data: null, error, loading: false}`. A non-Error
     rejection is wrapped in an `Error` so consumers can always read
     `.message`.
  4. After unmount, nothing is set.

- [ ] **Step 1: Write the failing test**

`dashboard/design-system/useAsync.test.ts` — React 19 exports `act` from the
`react` package itself, so this needs no testing-library dependency:

```ts
import { act } from 'react';
import { createElement, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { useAsync, type AsyncState } from './useAsync';

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

/** Renders useAsync and exposes every state it has been through. */
function renderHook<T>(fn: () => Promise<T>, deps: readonly unknown[]) {
  const states: AsyncState<T>[] = [];
  function Probe({ f, d }: { f: () => Promise<T>; d: readonly unknown[] }): ReactNode {
    states.push(useAsync(f, d));
    return null;
  }
  const render = (f: () => Promise<T>, d: readonly unknown[]) =>
    act(() => root.render(createElement(Probe, { f, d })));
  render(fn, deps);
  return { states, render, last: () => states[states.length - 1] };
}

/** A promise whose settlement this test controls. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe('useAsync', () => {
  it('reports loading synchronously, then the resolved data', async () => {
    const d = deferred<string>();
    const h = renderHook(() => d.promise, ['a']);
    expect(h.last()).toEqual({ data: null, error: null, loading: true });
    await act(async () => { d.resolve('hello'); });
    expect(h.last()).toEqual({ data: 'hello', error: null, loading: false });
  });

  it('reports a rejection as an Error and never as data', async () => {
    const d = deferred<string>();
    const h = renderHook(() => d.promise, ['a']);
    await act(async () => { d.reject(new Error('boom')); });
    expect(h.last().data).toBeNull();
    expect(h.last().error?.message).toBe('boom');
    expect(h.last().loading).toBe(false);
  });

  it('wraps a non-Error rejection so consumers can always read .message', async () => {
    const d = deferred<string>();
    const h = renderHook(() => d.promise, ['a']);
    await act(async () => { d.reject('just a string'); });
    expect(h.last().error).toBeInstanceOf(Error);
    expect(h.last().error?.message).toBe('just a string');
  });

  it('clears stale data the instant deps change, before the new load settles', async () => {
    const first = deferred<string>();
    const h = renderHook(() => first.promise, ['a']);
    await act(async () => { first.resolve('page A'); });
    expect(h.last().data).toBe('page A');

    const second = deferred<string>();
    h.render(() => second.promise, ['b']);
    // This is the EntityDetail bug: the previous entity's body rendered
    // under the new entity's heading until the new fetch landed.
    expect(h.last()).toEqual({ data: null, error: null, loading: true });
    await act(async () => { second.resolve('page B'); });
    expect(h.last().data).toBe('page B');
  });

  it('discards a slow earlier load that settles after a newer one', async () => {
    const slow = deferred<string>();
    const h = renderHook(() => slow.promise, ['a']);
    const fast = deferred<string>();
    h.render(() => fast.promise, ['b']);

    await act(async () => { fast.resolve('newest'); });
    await act(async () => { slow.resolve('stale'); });   // arrives last, must lose
    expect(h.last().data).toBe('newest');
  });

  it('sets nothing after unmount', async () => {
    const d = deferred<string>();
    const h = renderHook(() => d.promise, ['a']);
    const before = h.states.length;
    act(() => root.unmount());
    await act(async () => { d.resolve('too late'); });
    expect(h.states.length).toBe(before);
  });
});
```

- [ ] **Step 2: Run it — expect FAIL**

Run: `pnpm --filter @ensemble/design-system test`
Expected: FAIL — `Cannot find module './useAsync'`.

- [ ] **Step 3: Implement `dashboard/design-system/useAsync.ts`**

```ts
import { useEffect, useRef, useState } from 'react';

export interface AsyncState<T> {
  data: T | null;
  error: Error | null;
  loading: boolean;
}

/**
 * Loads `fn()` on mount and whenever `deps` change, reporting
 * `{data, error, loading}`.
 *
 * This exists because the hand-rolled version of it —
 * `let cancelled = false; … .then(d => { if (cancelled) return; … })` —
 * was written ten times across Phase 3's five views and drifted nine ways.
 * Three separate race bugs came out of that drift.
 *
 * Two details are load-bearing:
 *
 *   - **The guard is a generation counter, not a boolean.** A boolean is
 *     scoped to the effect closure that created it, so it can only guard
 *     that one effect's resolution. The counter lives on the hook, so every
 *     path that can ever start a load — a deps change, StrictMode's
 *     deliberate double-invoke in development, and any refetch a future
 *     caller adds by bumping a dep — is guarded by construction rather than
 *     by each call site remembering to.
 *   - **State is cleared synchronously when deps change.** Leaving the
 *     previous deps' data on screen while the new load is in flight is not
 *     a cosmetic nicety: it renders one record's body under another
 *     record's heading, which is exactly the bug the Phase 3 review found
 *     in EntityDetail.
 *
 * `fn` is intentionally NOT in the dependency list: callers pass an inline
 * arrow, which is a new function identity every render, and depending on it
 * would re-fetch forever. `deps` is the caller's explicit statement of what
 * the load actually depends on.
 */
export function useAsync<T>(fn: () => Promise<T>, deps: readonly unknown[]): AsyncState<T> {
  const [state, setState] = useState<AsyncState<T>>({ data: null, error: null, loading: true });
  const generation = useRef(0);

  useEffect(() => {
    const mine = ++generation.current;
    setState({ data: null, error: null, loading: true });
    fn().then(
      (data) => {
        if (generation.current === mine) setState({ data, error: null, loading: false });
      },
      (cause: unknown) => {
        if (generation.current !== mine) return;
        setState({
          data: null,
          error: cause instanceof Error ? cause : new Error(String(cause)),
          loading: false,
        });
      },
    );
    // Bumping the generation on cleanup is what makes clause 4 hold: after
    // unmount (or before the next load starts) no in-flight promise can
    // still match `mine`.
    return () => { generation.current++; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- deps is the caller's contract; see the doc comment
  }, deps);

  return state;
}
```

- [ ] **Step 4: Wire the package**

`dashboard/design-system/package.json` gains:
```json
{
  "scripts": { "test": "vitest run" },
  "exports": {
    ".": "./primitives.tsx",
    "./tokens.css": "./tokens.css",
    "./useAsync": "./useAsync.ts"
  },
  "devDependencies": {
    "@types/react": "^19.2.18",
    "@types/react-dom": "^19.2.4",
    "happy-dom": "^20.11.6",
    "react": "^19.2.8",
    "react-dom": "^19.2.8",
    "vitest": "^4.1.11"
  }
}
```
and `vite.config.ts`:
```ts
/// <reference types="vitest/config" />
import { defineConfig } from 'vite';

// The design system ships source, not a build — this config exists only so
// vitest has a happy-dom environment for the hook test.
export default defineConfig({ test: { environment: 'happy-dom' } });
```
No new third-party surface: `vitest`, `happy-dom`, `react` and `react-dom`
are already in the lockfile from `ensemble-ui`.

- [ ] **Step 5: Run — expect PASS**

Run: `pnpm install && pnpm --filter @ensemble/design-system test`
Expected: PASS (6 tests).

- [ ] **Step 6: Commit**

```bash
pnpm -r test
git add dashboard/design-system
git commit -m "feat(design-system): useAsync — one guarded async-load hook for both dashboards"
```

---

### Task 15: `dashboard/retrace-ui` — the keyboard-driven review screen

**Files:**
- Create: `dashboard/retrace-ui/{package.json,vite.config.ts,tsconfig.json,index.html}`
- Create: `dashboard/retrace-ui/src/{main.tsx,App.tsx,App.css}`
- Create: `dashboard/retrace-ui/src/api/{types.ts,client.ts}`
- Create: `dashboard/retrace-ui/src/{urlState.ts,keys.ts,keys.test.ts,tone.ts}`
  — note there is **no** `score.ts`/`score.test.ts`: see the ruling below.
- Create: `dashboard/retrace-ui/src/components/{QueueList,ShotCompare,WireDiffTable,HopDeltaList,CaptureBanner}.tsx` (+ `.css` each)
- Create: `dashboard/retrace-ui/src/components/{ShotCompare,WireDiffTable}.test.tsx`
- Create: `retrace/serve/ui/ui.go`, `retrace/serve/ui/ui_test.go`, `retrace/serve/ui/dist/index.html` (placeholder)
- Modify: `retrace/serve/server.go` (mount `GET /`), `.gitignore`

**Interfaces:**
- Consumes: the REST surface from Task 13, verbatim. TS mirrors of the Go
  JSON — write these EXACTLY, they are the contract:
  ```ts
  export interface Timings { start: string; firstByteMs?: number; doneMs?: number }
  export interface Payload { headers?: Record<string, string>; body?: string; truncated?: boolean }
  export interface Hop { schema: string; seq: number; traceId?: string; spanId?: string; parentSpanId?: string;
    correlationId?: string; session?: string; from?: string; to: string; method?: string; path?: string;
    status?: number; t: Timings; req?: Payload; resp?: Payload; injectedDelayMs?: number; err?: string }
  export type Verdict = 'ok' | 'suspect' | 'degraded' | 'broken' | 'failed';
  export interface TrustReason { code: string; status: Verdict; detail: string; hint?: string }
  export interface CaptureTrust { status: Verdict; reasons?: TrustReason[]; gaps?: Gap[]; summary: string; hint?: string }
  export interface Gap { from: string; to: string; seconds: number }
  export interface Rect { x: number; y: number; width: number; height: number }
  export interface CheckpointVerdict { name: string; verdict: 'ok'|'changed'|'missing'|'added'|'unreadable';
    diffPct: number; diffPctFine: number; numDiff: number; mismatch?: boolean;
    overlap?: { width: number; height: number; diffPct: number; diffPctFine: number; numDiff: number; paddingPct: number };
    trimmed?: { a?: Rect; b?: Rect };
    images: { a?: string; b?: string; diff?: string; overlay?: string } }
  export interface FieldDiff { scope: string; path: string; type: 'changed'|'added'|'removed'; a?: unknown; b?: unknown; matcher?: string; glob?: string }
  export interface HeaderDiff { scope: string; name: string; type: 'changed'|'added'|'removed'|'tolerated'|'violation'; a?: string; b?: string; matcher?: string }
  export interface Entry { method: string; normalizedPath: string; seqA: number; seqB: number; posA: number; posB: number;
    groupA?: string; groupB?: string; moved: boolean; truncated: boolean; classes: string[];
    statusChange?: { a: number; b: number }; bodyDiff: FieldDiff[]; bodyTolerated: FieldDiff[];
    bodyViolations: FieldDiff[]; bodyIgnored: FieldDiff[]; orderingChanges: FieldDiff[]; headerDiff: HeaderDiff[] }
  export interface Section { name: string | null; entries: Entry[]; counts: Record<string, number> }
  export interface Counts { checkpoints: number; pixelChanged: number; wirePaired: number; wireChanged: number;
    wireMoved: number; wireMissing: number; wireExtra: number; violations: number; hopNew: number; hopGone: number;
    unexpectedStatuses: number; conformance: number }
  export interface Item { app: string; flow: string; verdict: 'pass'|'changed'|'failed'; score: number;
    runId: string; refRunId: string; counts: Counts; capture: { a: CaptureTrust; b: CaptureTrust }; gates: string[] }
  export interface Call { method: string; path: string; seq: number; status: number;
    group?: string; tolerated?: { id: string; reason: string } }
  export interface GroupNames { a: string[]; b: string[] }
  export interface Manifest { schema: string; app: string; flow: string; runId: string; mode: 'ensemble'|'standalone';
    git: { sha: string; branch: string; dirty: boolean }; startedAt: string; finishedAt: string;
    checkpoints: { name: string; file: string; width: number; height: number; trim?: boolean }[];
    groups?: { name: string; startedAt: string; endedAt: string; quiet?: boolean }[];
    capture: CaptureTrust; wire: { calls: number }; hops?: { calls: number };
    test: { command: string; exitCode: number; durationMs: number }; env: { go: string; platform: string; retrace: string } }
  export interface RunRef { runId: string; kind: 'bundle'|'run'|'none'; dir: string; manifest: Manifest }
  export interface Route { to: string; method: string; path: string; via?: string[] }
  export interface ServiceCount { service: string; a: number; b: number; deviates: boolean }
  export interface RouteFailure { method: string; path: string; expectedStatus: number;
    actualStatus: number; reason: 'missing'|'wrong-status' }
  export interface StatusFinding { seq: number; method: string; path: string; status: number }
  export interface HopDiff { serviceCounts: ServiceCount[];
    newErrors?: StatusFinding[]; goneErrors?: StatusFinding[];
    newRoutes: Route[]; goneRoutes: Route[];
    requiredFailures?: RouteFailure[]; hopRequireConfigured: boolean }
  export interface PerfResult { status: 'ok'|'over'|'unset'; measuredMs: number; budgetMs: number }
  export interface ConformanceFinding { method: string; path: string; status: number;
    kind: 'unknown-path'|'unknown-method'|'undocumented-status'|'missing-required-field'|'unchecked'; detail: string }
  export interface Gate { plane: 'pixel'|'wire'|'hop'|'perf'; threshold: number; observed: number; failed: boolean }
  export interface Quarantine { side: 'a'|'b'; reason: string }
  export interface Summary { schema: string; app: string; flow: string;
    verdict: 'pass'|'changed'|'failed'|'quarantined';
    a: RunRef; b: RunRef;
    quarantined: Quarantine[];
    checkpoints: CheckpointVerdict[];
    wire: { paired: Entry[]; missing: Call[]; extra: Call[]; groups?: GroupNames };
    sections: Section[]; hops: HopDiff; unexpectedStatuses: StatusFinding[]; perf: PerfResult;
    conformance: ConformanceFinding[]; capture: { a: CaptureTrust; b: CaptureTrust }; counts: Counts;
    gates: string[]; budgets: Gate[] }
  ```

  **Three of those were missing and are corrections, not additions.** An
  earlier draft of this interface omitted `budgets` and `quarantined`
  entirely, and typed `verdict` as a three-value union. Task 10 emits four
  verdicts: `quarantined` is the "could not evaluate" state that a signal-
  killed or untrustworthy run takes, and it is the one an exhaustive switch
  in this UI most needs to handle, because it is the case where every other
  field is empty *on purpose*. `Gate` had no mirror at all despite
  `Summary.Budgets` being part of the wire since Task 10.

  **Array-valued fields are always arrays — never `null`, never absent — so
  none of them is typed optional or nullable here.** Task 10 initialises
  every slice and carries no `omitempty` on an array field, so
  `summary.budgets.map(...)` is safe on an API-only flow that produced no
  gates at all. This is a real case, not a theoretical one: since "no
  evidence, no gate" applies to all four planes, a flow with no checkpoints
  and no paired wire entries emits an empty `budgets`.

  **These names are not a guess — they are the `json:` tags from Tasks 8
  and 10, transcribed.** If a Go type here gains a field, its tag is the
  TS property name; if a tag is missing on the Go side, this whole block is
  wrong and so is every REST response. Tasks 8, 10, 12 and 13 carry the
  tags explicitly for exactly this reason. `TestSummaryJsonShapeIsStable`
  (Task 10) is the golden that catches a drift here from the Go side; the
  `.test.tsx` files below catch it from this side.
- Consumes: `useAsync(fn, deps)` from `@ensemble/design-system/useAsync`
  (Task 14). **Every fetch in this app goes through it** — the queue load,
  the item load, and the post-mutation refetch (which is a `useAsync` whose
  deps include a `version` counter the three verbs bump). No view in this
  task may contain the string `let cancelled`; a grep for it is part of
  Step 7.
- Produces: `retrace/serve/ui.Handler() http.Handler` — the embedded SPA,
  identical in shape to `ensemble/server/ui.Handler()` (real files served as
  themselves; `/assets/*` misses 404; everything else falls back to
  index.html).
  ```ts
  // client.ts
  export const api: {
    queue(): Promise<{ items: Item[] }>;
    item(app: string, flow: string): Promise<{ summary: Summary }>;
    accept(app: string, flow: string): Promise<{ ok: true }>;
    reject(app: string, flow: string): Promise<{ ok: true; repro: { dir: string } }>;
    rule(app: string, flow: string, r: { scope: string; field: string; matcher: string; method?: string; path?: string }): Promise<{ ok: true }>;
    shotUrl(app: string, flow: string, side: 'a'|'b'|'diff'|'overlay', name: string): string;
  };
  export class ApiError extends Error { constructor(public status: number, message: string) }
  // keys.ts
  export type Action = 'next'|'prev'|'accept'|'reject'|'rule'|'toggleOverlay'|'scrubLeft'|'scrubRight'|'help'|'back';
  export function actionFor(e: { key: string; ctrlKey: boolean; metaKey: boolean; altKey: boolean; target: EventTarget | null }): Action | null;
  // tone.ts — presentation only. There is deliberately NO TypeScript copy
  // of serve.ScoreOf: the server computes the score, puts it on the wire
  // as Item.score, and this app sorts and renders that number. A mirrored
  // formula is two implementations of one rule, and the day they disagree
  // the queue silently orders itself differently from the CI report.
  export function verdictTone(v: Item['verdict']): 'green' | 'amber' | 'red';
  ```

  **Ruling (scoring has one home).** An earlier draft had `score.ts`
  "mirroring `serve.ScoreOf`, tested against a fixture the Go side also
  uses" while Task 13 argued `ScoreOf` exists "so the UI never re-derives
  it" — the plan mandated both. The server wins: it is the surface an
  agent hits over REST, and `Item.score` is already in the payload. The
  only thing this file ever held that the UI genuinely needs is
  `verdictTone`, which is the one function its own interface block
  declared, so it becomes `tone.ts` and the mirror disappears with it.

**Keyboard map** (the "keyboard-driven item screen" the spec requires):
`j`/`↓` next · `k`/`↑` prev · `enter` open · `esc` back to queue ·
`a` accept · `r` reject · `u` rule (on the selected field) · `o` toggle
overlay · `←`/`→` scrub the A/B slider · `?` help. Modifier-held and
in-input keystrokes return `null` so browser shortcuts and typing survive —
that is what `keys.test.ts` asserts first.

- [ ] **Step 1: Scaffold + the failing keymap test**

`dashboard/retrace-ui/package.json` mirrors `ensemble-ui`'s dependency set
exactly (react 19, `@ensemble/design-system` workspace dep, vite 8,
typescript 7, vitest 4, happy-dom, `@fontsource/ibm-plex-{sans,mono}`), with
`"build": "tsc --noEmit -p tsconfig.json && vite build"`.
`vite.config.ts`: `build.outDir: '../../retrace/serve/ui/dist'`,
`emptyOutDir: true`, dev `server.proxy: {'/api': 'http://127.0.0.1:4800'}`.

`src/keys.test.ts` (RED first):
```ts
import { describe, expect, it } from 'vitest';
import { actionFor } from './keys';

const ev = (key: string, over: Partial<Parameters<typeof actionFor>[0]> = {}) =>
  ({ key, ctrlKey: false, metaKey: false, altKey: false, target: null, ...over });

describe('actionFor', () => {
  it('maps the three verbs', () => {
    expect(actionFor(ev('a'))).toBe('accept');
    expect(actionFor(ev('r'))).toBe('reject');
    expect(actionFor(ev('u'))).toBe('rule');
  });
  it('maps navigation both by letter and by arrow', () => {
    expect(actionFor(ev('j'))).toBe('next');
    expect(actionFor(ev('ArrowDown'))).toBe('next');
    expect(actionFor(ev('k'))).toBe('prev');
  });
  it('ignores keystrokes with a modifier so browser shortcuts survive', () => {
    expect(actionFor(ev('a', { metaKey: true }))).toBeNull();
  });
  it('ignores keystrokes while typing in a field', () => {
    const input = document.createElement('input');
    expect(actionFor(ev('a', { target: input }))).toBeNull();
  });
  it('returns null for an unmapped key rather than guessing', () => {
    expect(actionFor(ev('z'))).toBeNull();
  });
});
```

Run: `pnpm --filter retrace-ui test` → FAIL (`Cannot find module './keys'`).
Implement `keys.ts` → GREEN.

- [ ] **Step 2: Go embed, test first**

`retrace/serve/ui/ui_test.go`: `TestUIServesIndexAndSPAFallback` and
`TestAssetsMissIs404` — same two assertions as `ensemble/server/ui/ui_test.go`.
Implement `ui.go` as the direct analogue (`//go:embed all:dist`), commit the
placeholder `dist/index.html`, and add the `.gitignore` stanza:

```gitignore
# Same hazard as ensemble/server/ui/dist: `pnpm -r build` overwrites the
# committed placeholder with the real bundle, which must never land in a
# commit — `git restore retrace/serve/ui/dist/index.html` after building.
!retrace/serve/ui/dist/
!retrace/serve/ui/dist/index.html
retrace/serve/ui/dist/assets/
```

Mount in `retrace/serve/server.go`: `mux.Handle("GET /", ui.Handler())` —
after the `/api/...` patterns, which ServeMux prefers automatically.

- [ ] **Step 3: Queue screen**

`QueueList.tsx`: worst-first rows sorted by the server's `item.score` —
never recomputed here (`Badge` tone from `verdictTone` in `tone.ts`), each
showing app/flow, verdict, the gate count, and a one-line counts strip
(`3 shots · 12 wire · +1 hop`). Passing flows render collapsed under a
"N passing" disclosure — the spec's "passing collapsed". Selection is
keyboard-driven and mirrored into the URL (`?app=&flow=`) via `urlState.ts`
(copy the shipped `ensemble-ui/src/urlState.ts` — same file, same tests).

- [ ] **Step 4: Item screen — shots, wire, hops**

`ShotCompare.tsx`: A/B/overlay with a drag-or-arrow-key slider, and a
`diff` tab. Images come from `api.shotUrl(...)` — never a data URI, the
server already serves PNGs.
Test (`ShotCompare.test.tsx`, happy-dom) — vitest `it(...)` names, not Go
`TestXxx` identifiers; these are `.tsx` files:
`it('clamps the slider position to 0–100')`,
`it('swaps the rendered image source when the overlay toggles')`,
`it('renders the no-diff-image case as an explanation, not a blank pane')`.

`WireDiffTable.tsx`: one collapsible section per flow part (from
`summary.sections`), rows carrying `classes` as CSS class names
(`changed|moved|new|missing|identical`), field rows beneath an expanded row.
A `truncated` entry renders "body was size-capped at capture — not field
diffed" instead of an empty diff. Redaction markers (`[redacted]`, and
`$enc:v1:` once Phase 4b lands) render with the `.redacted` class and a
title tooltip; **no reveal control in this task**.
Test (`WireDiffTable.test.tsx`):
`it('renders a tolerated field with its matcher instead of counting it as a change')`,
`it('explains a truncated body rather than rendering an empty diff')`,
`it('names each section from summary.sections')` — the UI end of the
marker → group → section chain.

`HopDeltaList.tsx`: new/gone routes and deviating service counts.
`CaptureBanner.tsx`: renders a non-ok `CaptureTrust` at the top of BOTH the
queue row and the item screen — the spec requires every report surface to
banner it.

- [ ] **Step 5: The three verbs**

Each verb is one `api` call followed by a queue refetch. `rule` opens a
matcher picker seeded from the selected field
(`{scope, field: entry-relative path, matcher, method, path: normalizedPath}`)
and, on success, refetches — the diff comes back without that noise, which
is the "drains volatile-field noise permanently" payoff.
Failures surface the `ApiError` message inline; nothing is optimistic —
accepting a reference is a filesystem mutation, and a UI that lies about it
is worse than a slow one.

- [ ] **Step 6: Build round-trip**

```bash
pnpm install && pnpm -r build
go build ./retrace/... && go test -race ./retrace/serve/...
go run ./retrace/cmd/retrace serve --addr 127.0.0.1:4800   # confirm the queue loads
git restore retrace/serve/ui/dist/index.html               # NEVER commit the built shell
```

- [ ] **Step 7: Full suites + commit**

```bash
# The constraint, enforced rather than remembered:
! grep -rn "let cancelled" dashboard/retrace-ui/src
pnpm -r test && go test -race ./core/... ./ensemble/... ./retrace/...
git add dashboard/retrace-ui retrace/serve/ui .gitignore
git commit -m "feat(dashboard): retrace review UI — worst-first queue, keyboard item screen, three verbs"
```

---

### Task 16: `retrace export` — the static report

**Files:**
- Create: `retrace/serve/export.go`, `retrace/serve/export_test.go`
- Create: `retrace/serve/report.tmpl.html` — **the embedded template.**
  `//go:embed` fails to compile when its target is missing, so this file is
  created in Step 3 along with the code that embeds it, not left implied.
- Create: `retrace/cmd/retrace/cmd_export.go`
- Modify: `retrace/cmd/retrace/main.go`

**Interfaces:**
- Produces:
  ```go
  package serve
  type ExportOptions struct { Deps Deps; OutDir string; App, Flow string } // empty App/Flow = everything
  // ExportResult is printed by `retrace export --json`, so it is a wire
  // type and carries tags. ExportOptions is an input and does not.
  type ExportResult struct {
      Dir   string   `json:"dir"`
      Files []string `json:"files"`
      Items int      `json:"items"`
  }
  func Export(o ExportOptions) (ExportResult, error)
  ```

**Shape of the output** — a directory that opens with `file://` and needs no
server, because the person receiving it is reading a CI artifact:
```
<out>/index.html                 queue overview, worst first
<out>/<app>__<flow>/index.html   the item screen, static
<out>/<app>__<flow>/{a,b,diff,overlay}/shots/<checkpoint>.png
<out>/<app>__<flow>/summary.json the exact diff.Summary the UI consumed
```

The four shot sides keep the `<side>/shots/<name>.png` shape they have on
disk (Task 10 Step 3's layout contract) rather than being re-laid-out on
copy, so `summary.json`'s `images` paths stay valid inside the export and
an agent can join them without a second rule. Export **copies** the `a`
and `b` PNGs in — a CI artifact has no access to the run directories they
normally resolve against.

**Ruling: the static report is server-rendered Go `html/template`, not the
React app.** The React app is a live client of a REST API; making it work
from `file://` means a build variant, a fetch shim, and inlined JSON — three
things that can drift from the real UI silently. A separate, simpler,
**always-honest** HTML rendering is the smaller lie. `summary.json` ships
alongside so an agent reads the same document the UI reads.

- [ ] **Step 1: Write the failing export test**

```go
func TestExportWritesASelfContainedTreeThatNeedsNoServer(t *testing.T) {
	// no "http://", "fetch(", or "/api/" substring anywhere in the HTML —
	// a report that silently needs a running server is not an artifact.
}
func TestExportCopiesEveryReferencedShot(t *testing.T) {
	// every src= in the HTML resolves to a file that exists on disk.
}
func TestExportEscapesUntrustedStringsIntoHtml(t *testing.T) {
	// a recorded response body containing "<script>alert(1)</script>"
	// appears escaped. Bodies are attacker-influenced data in a real stack.
}
func TestExportBannersANonOkCaptureVerdict(t *testing.T)
func TestExportOfAnUnknownFlowIsAnErrorNamingIt(t *testing.T)
```

- [ ] **Step 2: Run — expect FAIL. Step 3: implement** with
  `html/template` (which escapes by default — never `template.HTML` on
  anything derived from a recording), one `//go:embed report.tmpl.html`
  template — **write `retrace/serve/report.tmpl.html` in this step**; an
  embed directive pointing at a file that does not exist is a compile
  error, not a runtime one — and a tiny inlined `<style>` block. No JavaScript at all except
  a ~15-line inline overlay toggle, written as a `<details>`/`<input
  type=range>` pair where possible so it degrades without script.

- [ ] **Step 4: `retrace export` CLI** — `--out` (required), `--app`,
  `--flow`, `--json` (prints the `ExportResult`). Exit code mirrors the
  worst verdict exported, so `retrace export` can be the only step in a CI
  job: `max(diff.ExitCode(s))` across everything exported — 0 all pass,
  1 any changed, 2 any failed. Assert it, or it is a claim rather than a
  behaviour: `TestExportExitsWithTheWorstVerdictItExported` — two flows,
  one `pass` and one `failed`, exits 2; the same pair with `pass` and
  `changed`, exits 1; two passes, exits 0.

- [ ] **Step 5: Commit**

```bash
go test -race ./retrace/...
git add retrace/serve retrace/cmd/retrace
git commit -m "feat(retrace): retrace export static report"
```

---

### Task 17: Adapters — retrace-js, retrace-playwright, retrace-maestro

**Files:**
- Create: `adapters/js/{package.json,tsconfig.json,vite.config.ts,src/index.ts,src/handshake.ts,src/handshake.test.ts,src/groups.test.ts,README.md}`
- Create: `adapters/playwright/{package.json,tsconfig.json,vite.config.ts,src/index.ts,src/fixture.ts,src/fixture.test.ts,README.md}`
- Create: `adapters/maestro/{package.json,bin/retrace-maestro.mjs,src/index.ts,src/index.test.ts,README.md}`
- Modify: nothing in Go, and that is now literally true. **All heavy
  lifting stays in the binary** — the adapters only mark flow parts and
  capture screenshots. (An earlier draft said this while Step 2 also
  mandated a `capture.Checkpoints()` change; the trim marker is now read by
  Task 4 and acted on by Tasks 7/10, so this task really is Go-free.)

**Interfaces:**
- Consumes: the env handshake from Task 4 — `RETRACE_RUN_DIR`,
  `RETRACE_PROXY_URL`, `RETRACE_MARKER_URL`, `RETRACE_STRICT`.
  **`RETRACE_STRICT` is plan-exceeds-spec** (see "Where this plan exceeds
  the spec" in the header): the spec requires adapters to "fail loudly if
  invoked without [the handshake] when strict mode is on" but never names
  the switch that turns strict mode on. This is that switch, and unset it
  preserves the spec'd default of no-op-outside-a-run. Report it to the
  spec owner alongside `RETRACE_MARKER_URL`.
- Produces:
  ```ts
  // @caribou-crew/retrace-js
  export interface Handshake { runDir: string | null; proxyUrl: string | null; markerUrl: string | null; strict: boolean }
  export function handshake(env?: NodeJS.ProcessEnv): Handshake;
  export function requireHandshake(env?: NodeJS.ProcessEnv): Handshake;  // throws in strict mode
  export function group(name: string, options?: { quiet?: boolean }): Promise<void>;
  export function endGroup(): Promise<void>;
  export function shotsDir(): string | null;
  export const MISSING_HANDSHAKE_MESSAGE: string;

  // @caribou-crew/retrace-playwright
  export const test: TestType<{ retrace: RetraceFixture }, {}>;   // test.extend of @playwright/test
  export interface RetraceFixture {
    checkpoint(name: string, options?: { selector?: string | Locator; trim?: boolean }): Promise<void>;
    group(name: string): Promise<void>;
    endGroup(): Promise<void>;
  }

  // @caribou-crew/retrace-maestro  (bin: retrace-maestro)
  //   retrace-maestro group <name>     → POST $RETRACE_MARKER_URL/group      {"name": "<name>"}
  //   retrace-maestro group --end      → POST $RETRACE_MARKER_URL/group/end  {}
  export function markerRequest(argv: string[], env: NodeJS.ProcessEnv): { url: string; body: string } | null;
  ```

**The strict-mode message** (spec: "SHALL fail loudly … with a message
explaining how to invoke retrace") — one exported constant so all three
packages say the same thing:
```ts
export const MISSING_HANDSHAKE_MESSAGE =
  'retrace: no active run. This fixture writes checkpoints and flow-part markers into the ' +
  'directory `retrace run` creates, and found neither RETRACE_RUN_DIR nor RETRACE_MARKER_URL ' +
  'in the environment.\n' +
  '  Run your tests through retrace:  retrace run --flow <name> -- <your test command>\n' +
  '  Or unset RETRACE_STRICT to let checkpoints be no-ops outside a run.';
```

- [ ] **Step 1: `adapters/js`, handshake test first**

```ts
describe('handshake', () => {
  it('reads all three variables', () => { /* env with all three → non-null fields */ });
  it('reports strict from RETRACE_STRICT=1', () => {});
  it('is a no-op outside a run when strict is off', async () => {
    // group('x') with an empty env resolves without throwing and writes nothing.
    // This is the contract that lets a test suite run normally outside retrace.
  });
  it('throws MISSING_HANDSHAKE_MESSAGE in strict mode', () => {
    // the spec's "Missing handshake" scenario.
  });
});
describe('group', () => {
  it('appends a start record to groups.jsonl in RETRACE_RUN_DIR', async () => {});
  it('appends an end record carrying no name', async () => {
    // the writer is stateless — a fresh process cannot know what is open.
  });
  it('falls back to POSTing RETRACE_MARKER_URL when RETRACE_RUN_DIR is absent', async () => {});
});
```
Run `pnpm --filter @caribou-crew/retrace-js test` → RED → implement → GREEN.
Zero runtime dependencies: `node:fs`, `node:path`, and global `fetch`.

- [ ] **Step 2: `adapters/playwright`, fixture test first**

The fixture is tested against a **fake page object** (`{ screenshot: async
() => Buffer, viewportSize: () => ({width, height}), locator: (s) => ({
screenshot }) }`), so the package's tests need no browser:
```ts
it('writes <name>.png into the run dir shots directory', async () => {});
it('scopes the shot to a selector when given one', async () => {});
it('accepts an already-scoped Locator (for cross-origin frames)', async () => {});
it('is a no-op outside a run when strict is off', async () => {});
it('throws the handshake message in strict mode', async () => {});
```
`@playwright/test` is a **peerDependency**, not a dependency — the consumer
owns their Playwright version.

Note: `trim` (uniform-border cropping) is **NOT** implemented in the
adapter, unlike flowlens. The Go binary owns pixel work, and duplicating it
in TS would be a second implementation to keep in sync.

**The whole trim path, so no task has to guess at its half:**
`trim: true` writes an empty `<name>.trim` marker file beside the shot —
that is this task's entire contribution, and it is a file write, not a Go
change. **Task 4** notices the marker and records `Checkpoint.Trim` in the
manifest (a fact about the capture; `Width`/`Height` stay the shot's real
pre-trim geometry). **Tasks 7 and 10** do the cropping, at compare time,
via `pixel.Options.Trim` → `pixel.TrimUniformBorder`, and report the rect
actually used in `CheckpointVerdict.Trimmed`. Trimming at compare time
rather than capture time is what keeps `retrace/capture` from importing
`retrace/diff/pixel`, and it means the recorded artifact is never altered
by a display preference.
Test here: `it('writes a .trim marker beside the shot when trim is true')`.
*(Implementers: this is a deliberate deviation from the ported file — do
not "restore" the JS trim, and do not crop in the adapter.)*

- [ ] **Step 3: `adapters/maestro`**

Maestro flows call out via `runScript`, so the package ships an executable
that turns argv + env into one HTTP POST. `markerRequest` is pure and
therefore unit-testable without a network:
```ts
it('builds a start marker request', () => {});
it('builds an end marker request', () => {});
it('returns null (a silent no-op) outside a run when strict is off', () => {});
it('throws the handshake message in strict mode', () => {});
```
`README.md` shows the Maestro snippet:
```yaml
- runScript:
    file: node_modules/@caribou-crew/retrace-maestro/bin/retrace-maestro.mjs
    env: { ARGS: "group checkout" }
```

- [ ] **Step 4: Workspace wiring and package entry points**

`pnpm-workspace.yaml` already globs `adapters/*` — no change needed. Verify:
`pnpm install && pnpm -r test` picks up all three packages.

Each `package.json` must declare what Step 5 consumes, or "imports the
built `adapters/js`" has nothing to import:

```jsonc
{
  "name": "@caribou-crew/retrace-js",
  "type": "module",
  "main": "./dist/index.js",
  "types": "./dist/index.d.ts",
  "exports": { ".": { "types": "./dist/index.d.ts", "default": "./dist/index.js" } },
  "files": ["dist", "README.md"],
  "scripts": {
    "build": "tsc -p tsconfig.json",
    "test": "vitest run"
  }
}
```
`retrace-playwright` is the same shape plus `"peerDependencies": {
"@playwright/test": ">=1.40" }`. `retrace-maestro` additionally declares
`"bin": { "retrace-maestro": "./bin/retrace-maestro.mjs" }`, and that file
is plain `.mjs` (not built) so the Maestro `runScript` path never depends
on a build having run.

- [ ] **Step 5: End-to-end proof**

`retrace/cmd/retrace/cmd_run_adapters_test.go`:
`TestARunWithMarkersAndAScreenshotProducesGroupsAndCheckpoints` — the test
command is a small Node script (skipped with `t.Skip` when `node` is not on
PATH, and with `pnpm --filter @caribou-crew/retrace-js build` run first —
the script imports `dist/`, so the build is a precondition of the test, not
an assumption) that imports the built `adapters/js` and calls `group`,
`endGroup`,
and writes a PNG into `$RETRACE_RUN_DIR/shots`. Asserts the manifest ends up
with one group interval and one checkpoint. This is the spec's "Playwright
fixture" scenario reduced to its testable core without requiring browsers in
CI.

- [ ] **Step 6: Full suites + commit**

```bash
pnpm -r test && go test -race ./core/... ./ensemble/... ./retrace/...
git add adapters retrace
git commit -m "feat(adapters): retrace-js, retrace-playwright and retrace-maestro with a strict env handshake"
```

---

### Task 18: Migrate the Phase 3 ensemble-ui views onto `useAsync`

**Recommendation, stated so nobody has to re-decide it: migrate them, here,
after Task 15 — not "left as-is", and not earlier.**

- *Why migrate at all:* leaving eleven hand-rolled copies in `ensemble-ui`
  while `retrace-ui` uses the hook means the bug class is only half
  eliminated, and the next reviewer finds the fourth instance in the half
  that was skipped. Three race bugs have already come out of this pattern.
- *Why not in Phase 3's closing fix round:* refactoring five shipped views is
  riskier than four localized fixes, and that round was for landing fixes,
  not restructuring.
- *Why after Task 15 and not before:* Task 15 is the hook's first real
  consumer across a queue view, a detail view, and a post-mutation refetch.
  If the API is wrong, that is where it shows — and changing a hook with one
  consumer is cheap while changing it with eleven is not. Migrating first
  would be committing to an unexercised interface.

**Files:**
- Modify: `dashboard/ensemble-ui/src/App.tsx`
- Modify: `dashboard/ensemble-ui/src/views/TopologyView.tsx`
- Modify: `dashboard/ensemble-ui/src/views/TrafficView.tsx`
- Modify: `dashboard/ensemble-ui/src/views/InspectorView.tsx`
- Modify: `dashboard/ensemble-ui/src/views/EntityView.tsx`
- Modify: `dashboard/ensemble-ui/src/views/LatencyView.tsx` — **in scope.**
  See Step 1: it is the eleventh site, and excluding it would make Step 4's
  gate unpassable.
- Modify: `dashboard/ensemble-ui/package.json` if the design-system export
  needs no change — it does not; `@ensemble/design-system` is already a
  `workspace:*` dependency, so only the import line changes.
- Do NOT modify: any `*.test.ts` under `dashboard/ensemble-ui/src/views/`.
  The existing race regression tests (`TopologyView.poll-race.test.ts`,
  `TopologyView.trace-race.test.ts`, `EntityView.detail-race.test.ts`,
  `InspectorView.stale-rows.test.ts`, `EntityView.url-clear.test.ts`,
  `InspectorView.url-clear.test.ts`, `EntityView.empty-body.test.ts`) are
  the regression net for this refactor. **If a migration makes one of them
  fail, the hook or the migration is wrong — never the test.**

**Interfaces:**
- Consumes: `useAsync(fn: () => Promise<T>, deps: readonly unknown[]): AsyncState<T>`
  from `@ensemble/design-system/useAsync` (Task 14).
- Produces: no new exports. This task's deliverable is a deletion.

- [ ] **Step 1: Get the authoritative list, don't trust a count**

Run: `grep -rn "let cancelled" dashboard/ensemble-ui/src --include='*.tsx'`
Expected today: **eleven** sites — `App.tsx` ×1, `TopologyView.tsx` ×2,
`TrafficView.tsx` ×1, `InspectorView.tsx` ×3, `EntityView.tsx` ×3,
`LatencyView.tsx` ×1. Write the list down; it is this task's checklist and
its definition of done. Re-run the grep rather than trusting this number —
it has already moved once (`26a27cb` added LatencyView's copy after an
earlier count said ten).

**`LatencyView.tsx` is in scope**, and the earlier draft's carve-out was
the problem, not the file. Step 4's gate is an unscoped `! grep`, so a
deliberately-excluded eleventh site makes it fail forever; the alternatives
were to filter the gate or to migrate the file, and **an honest gate beats
a gate with a carve-out** — a `| grep -v LatencyView` is a permanent
invitation to add a twelfth exclusion. The file's own comment says its copy
exists "to follow the shape every other fetch-on-mount in the dashboard
uses", so when that shape becomes `useAsync`, it follows.

- [ ] **Step 2: Confirm the net is green before touching anything**

Run: `pnpm --filter ensemble-ui test`
Expected: PASS. A refactor that starts from an unknown baseline cannot prove
it preserved anything.

- [ ] **Step 3: Migrate one file, run the suite, commit. Repeat.**

One file per commit, in this order — cheapest first, so the pattern is
established before the hard ones:
`App.tsx` → `TrafficView.tsx` → `TopologyView.tsx` → `InspectorView.tsx` →
`EntityView.tsx` → `LatencyView.tsx`.

`LatencyView` is last despite having the simplest *load*, because it is the
only file whose state is also written by mutations — see watch-out 4.

The mechanical shape of each replacement:

```tsx
// BEFORE — one of eleven near-identical copies
const [topology, setTopology] = useState<Topology | null>(null);
const [error, setError] = useState<string | null>(null);
useEffect(() => {
  let cancelled = false;
  api.topology().then(
    (t) => { if (!cancelled) setTopology(t); },
    (e) => { if (!cancelled) setError(String(e)); },
  );
  return () => { cancelled = true; };
}, []);

// AFTER
const { data: topology, error, loading } = useAsync(() => api.topology(), []);
```

Three things to watch, because they are where a mechanical rewrite goes
wrong:
1. **A polling view is not a one-shot load.** `TopologyView`'s 5s status
   poll keeps its `setInterval`; only the load it triggers moves into
   `useAsync`, with a `tick` counter in `deps`. Do not turn a poll into a
   mount-only fetch.
2. **`error` changes type.** It was `string | null` in most views and is now
   `Error | null`. Render `error.message`, and update the local prop types —
   do not stringify the Error back into the old shape just to avoid touching
   a component signature.
3. **Some copies swallowed their rejection** (a bare `.then(d => ...)` with
   no second argument). After migration those views suddenly *have* an
   error state. Render it; an error that was previously invisible is a bug
   the migration exposes, not one it introduces. Add a test in the affected
   view's existing spec file if the exposed path has no coverage.
4. **`LatencyView`'s state has seven writers, not one.** Its load is the
   simplest of the eleven, but `setRules(result)` is also called by six
   mutation handlers (`upsert`, `toggle`, `delete`, `armAll` ×2, `reset`),
   each of which gets the new list back in the response. `useAsync` owns
   its `data` and hands back no setter, so a straight swap would strand
   those six writes. Do **not** reintroduce a local `rules` state seeded
   from `data` by an effect — that is the same two-sources-of-truth race in
   a new costume. Use the version-counter refetch, which is the pattern
   Task 15 already specifies for its three verbs:

   ```tsx
   const [version, setVersion] = useState(0);
   const { data: rules, error } = useAsync(() => api.latencyList(), [version]);

   // each mutation handler, after its await:
   //   setRules(result)   ->   setVersion((v) => v + 1);
   ```

   The cost is one extra `GET /api/latency` per mutation against a loopback
   dev server, which is not a cost. The gain is that the list has exactly
   one writer, and the "which response won" question stops existing. The
   mutation's own returned list is discarded deliberately — keep the
   `setError` / `setFormError` handling exactly as it is.

Per file:
```bash
pnpm --filter ensemble-ui test
git add dashboard/ensemble-ui/src/<file>
git commit -m "refactor(dashboard): migrate <file> onto useAsync"
```

- [ ] **Step 4: Verify the pattern is gone**

```bash
! grep -rn "let cancelled" dashboard/ensemble-ui/src --include='*.tsx'
pnpm -r test && go test -race ./core/... ./ensemble/... ./retrace/...
```
Expected: the grep finds nothing in `.tsx` sources (the `*.test.ts` files may
still mention it in a comment describing the old bug — that is fine and
worth keeping as history), and both suites are green.

The gate is deliberately **unscoped** — no `| grep -v <file>`. All eleven
sites are in scope precisely so this line can stay honest: a gate with an
exclusion list stops being a gate the moment someone adds the next
exclusion instead of migrating the next file.

- [ ] **Step 5: Close the loop in the docs**

Add one line to `docs/phase-3-porting-inventory.md` under its "Rewrite
decisions" section recording that the hand-rolled fetch effect was replaced
by `@ensemble/design-system/useAsync` in Phase 4, so a future reader of that
inventory does not reintroduce the pattern from the old prototype.

- [ ] **Step 6: Commit**

```bash
git add docs/phase-3-porting-inventory.md
git commit -m "docs: record the useAsync migration in the phase-3 inventory"
```

---

## Wrap-up: update the roadmap

- [ ] Tick boxes 4.1–4.7 in
  `openspec/changes/init-ensemble-retrace/tasks.md`, leaving 4.8 unticked
  with a pointer to the part-2 plan.
- [ ] Add a one-line note under 4.4 recording that **a11y-tree diff is
  deferred to part 2** (spec keeps it "flagged experimental until
  device-verified"; no task here implements it, and the part-2 plan's scope
  enumeration lists it alongside encryption so the SHALL has a home).
- [ ] Record the `RETRACE_MARKER_URL` handshake extension in the
  `adapters` spec, or raise it with the spec owner if the two-variable list
  is meant to be exhaustive.
- [ ] Note in the `ensemble-api-dashboard` spec that the `logical` half of
  `GET /api/traces/{id}` now has a product consumer (Task 9's relay-folded
  hop diff), so the next reviewer who finds it unused in `ensemble-ui`
  does not propose deleting `trace.CollapseRelays`.
- [ ] Tasks 14 and 18 are dashboard infrastructure, not roadmap boxes —
  they have no `tasks.md` line to tick. Record them in the Phase 4
  completion note so the `useAsync` decision is traceable to the Phase 3
  review that motivated it.

---

## Self-review

Run against the four specs with fresh eyes. Findings and their resolutions:

**1. Spec coverage.**

| Spec requirement | Task |
|---|---|
| capture-replay · Runner-agnostic capture (full-chain + standalone scenarios) | 4, 5 |
| capture-replay · Capture-trust verdict (banner on all surfaces) | 6 (+ banners asserted in 10, 13, 15) |
| capture-replay · Strict replay (unmatched fails, miss report) | 12 |
| capture-replay · Reference bundles (compact, size-bounded, redacted) | 11 |
| capture-replay · Encrypted recordings replay with fidelity; `retrace rekey` | **Part 2** — declared at the top, not dropped |
| capture-replay · Revalidation | 12 |
| diff-review · Pixel diff (thresholds, masks, border trim, A/B/overlay/diff) | 7 |
| diff-review · Wire diff (pairing, field+header, LIS reorder, rules exit non-zero) | 8, 10 |
| diff-review · Hop diff (added/removed, hopRequire hard gates) | 9 |
| diff-review · Machine-readable everything (`--json`, stable shapes, exit codes) | 10 (+ 16, the static export) |
| diff-review · Review queue (worst-first, keyboard, three verbs, same over REST) | 13 (REST), 15 (UI) |
| diff-review · Auxiliary checks (≥400 + expectedStatuses, OpenAPI, perf) | 9 |
| diff-review · Auxiliary checks (a11y-tree diff) | **Deferred to part 2** — enumerated in the scope ruling; spec itself flags it experimental |
| adapters · Thin in-test adapters (playwright, maestro, js) | 17 |
| adapters · Env-based handshake + loud strict failure | 4 (server side), 17 (adapter side) |
| core-trace-model · Unified hop schema used identically by both | 1, 4, 5 (`wire.jsonl`/`hops.jsonl` ARE `trace.Hop`) |
| core-trace-model · W3C propagation, baggage join key | reused from shipped `core/proxy` + `core/trace`; asserted end-to-end in 5 |
| core-trace-model · Relay collapse | already shipped (`core/trace/collapse.go`) — **and given its first product consumer in Task 9** |
| core-trace-model · Redaction at capture | 4, 5 (every disk write goes through `trace.Redactor`) |
| core-trace-model · Per-key redaction modes | **Part 2** |
| core-trace-model · Export formats (HAR/curl/raw) | already shipped (`core/trace/export.go`); the review UI links to ensemble's export endpoints rather than duplicating them |

Two additions came from the Phase 3 whole-phase review rather than from a
spec, and are tracked here so they are not read as scope creep: **Task 14**
(`useAsync`, the shared async-load hook, plus a Global Constraint binding
every new view to it) and **Task 18** (migrating Phase 3's eleven hand-rolled
fetch effects onto it, recommended explicitly rather than left open). Both
are infrastructure the Phase 3 review identified as "the one piece of shared
infrastructure this phase should have had and does not"; neither changes
what Phase 4 delivers.

The same review's second input — the unconsumed `logical` half of
`GET /api/traces/{id}` — is **decided, not deferred**: Task 9 makes
relay-folded hops the basis of route and service-count diffing, with a test
(`TestATransparentRelayHopIsFoldedAndNotCountedAsANewRoute`) that fails if
the folding is switched off. `trace.CollapseRelays` and `trace.LogicalHop`
stay, with a consumer.

Two gaps found and fixed inline while reviewing: (a) the a11y requirement
had no task and was silently missing — it now has an explicit deferral in
the wrap-up and in the porting inventory's "NOT ported" list; (b) the
"banner non-ok verdicts on all report surfaces" requirement was only in
Task 6 — assertions were added to Tasks 10, 13 and 16.

**2. Placeholder scan.** Searched for TBD / "implement later" /
"add appropriate error handling" / "similar to Task N" / "write tests for
the above": none present. Three places name a file to port rather than
inlining 200 lines of it (`wire.mjs`'s walker, `capture-health.mjs`'s reason
list, `reference.mjs`'s eligibility ladder) — each names the exact source
path, the exact function, and the exact behaviours its tests must pin, which
is the porting equivalent of showing the code.

Three `TODO(task-N)` markers survive **deliberately**, and each is a seam a
later task is contractually required to fill, named in that task's Files
block and covered by a test that cannot pass while the seam is open:

| Marker | Written by | Filled by | Test that proves it was filled |
|---|---|---|---|
| `TODO(task-6)` in `assessTrust` | 4 | 6 | `TestRunBannersANonOkVerdict` |
| `TODO(task-11)` in `cmd_diff.go`'s `reference` selector | 10 | 11 | `TestRefAcceptThenDiffAgainstReferenceExitsZero` |
| `TODO(task-11)` in `OptionsFor`'s deviations branch | 10 | 11 | `TestOptionsForLoadsTheLedgerNamedByConfig` |

A seam with a named owner and a failing test is not a placeholder; a seam
without one is, which is why the earlier draft's "assess trust (Task 6)"
inside an elided `...` was the more dangerous form.

Two open questions are called out for the spec owner rather than left as
silent choices: `RETRACE_MARKER_URL` and `RETRACE_STRICT`, both listed under
"Where this plan exceeds the spec".

**3. Type consistency.** Checked every name used across task boundaries:
`runs.Paths`, `runs.Manifest`, `runs.CaptureTrust`, `runs.Group`,
`runs.Checkpoint.Trim`, `runs.ReadHops`, `capture.Session`,
`capture.Options`, `capture.ProxyFailure`, `capture.Assess`,
`capture.Fatal`, `rules.Matcher`, `rules.Raw`, `rules.Resolved.ForField`,
`config.Config.NormalizePath`, `config.MasksFor`, `config.AppendWireRule`,
`config.DefaultGate`/`DefaultFine`, `pixel.Compare`, `pixel.ApplyMasks`,
`pixel.Rect`, `pixel.RectsFrom`, `pixel.TrimUniformBorder`, `diff.Options`,
`diff.Entry`, `diff.Deviation`, `diff.ToleratedNote`, `diff.Summary`,
`diff.OptionsFor`, `diff.ExitCode`, `refs.Resolve`, `refs.Accept`,
`replay.Bundle.Match`, `replay.StatusDrift`, `serve.Item`, `serve.ScoreOf`,
`httpguard.Handler`, `useAsync`/`AsyncState<T>` (Task 14, consumed unchanged
by Tasks 15 and 18), `HopOptions.NoCollapse` and `Route.Via` (Task 9, rendered
by Task 15's `HopDeltaList`).

Resolved during the first review:
- `diff.Counts` is referenced by `serve.Item` and defined in Task 10, which
  precedes Task 13. Correct order.
- **An import cycle.** The first draft put `Deviation` in `refs` while
  `diff.Options` consumed it and `refs.RejectOptions` carried a
  `*diff.Summary` — `diff → refs → diff`. Fixed by moving the deviations
  ledger into `retrace/diff/deviations.go`: `refs → diff` is now the only
  direction.

Resolved during the pre-flight revision round — **including two claims this
self-review previously made that were wrong**, recorded here rather than
quietly overwritten:

- **"Task 8 still compiles standalone."** It did not. The earlier text said
  `Options.Deviations []Deviation` was "inert until Task 11 turns it on";
  in Go a struct field whose type is undeclared is a compile error, and
  Task 8's own commit gate runs `go test ./retrace/diff/`. **Fixed:** the
  `Deviation` and `ToleratedNote` *declarations* now live in Task 8, which
  creates `deviations.go`; Task 11 appends the ledger functions to the same
  file and is told not to re-declare them.
- **"Task 11 and Task 10 both convert explicitly" between `config.Rect` and
  `pixel.Rect`.** Neither task's steps showed the conversion, and Task 10's
  `Build` sketch elided the entire pixel loop — so the spec scenario Task 10
  asserts (`TestAMaskedRegionDoesNotAffectTheCheckpointVerdict`) tested code
  no task wrote. **Fixed:** the conversion is one exported function,
  `pixel.RectsFrom`, called at exactly two call sites, both now written out
  — Task 10's checkpoint loop and Task 11's `AcceptOptions.MasksFor`
  closure.
- **JSON tags.** Every wire-crossing type in `diff`, `serve`, `replay`,
  `capture` and `runs` now carries explicit camelCase `json:` tags, and
  Task 15's TS mirrors are transcriptions of those tags rather than
  independent guesses. Untagged, `encoding/json` would have emitted
  `"Method"`/`"NewRoutes"` and broken every REST response, the `--json`
  contract, `summary.json` and the whole UI at once — with all Go tests
  green.
- **Dead ends closed.** `Manifest.Device` (no writer, no reader) is gone
  along with the adapter fixture that fed it; `Manifest.Groups` now has a
  writer (Task 4's `runFlow`) and a reader (Task 10's `OptionsFor`), each
  with its own test; `ProxyFailure` has a producer (`Session.ProxyDied`) and
  lost its unreachable `proxy-never-started` case;
  `pixel.TrimUniformBorder` has a caller (`Compare`, under `Options.Trim`);
  `replay.Options.MissPath` and `refs.AcceptOptions.Masks` are gone as
  duplicate inputs.
- **The guard is extracted once, in Task 4, and consumed everywhere.** The
  earlier draft had Task 4 inlining a weaker copy (no Host check, no Origin
  check) and Task 13 extracting the canonical one nine tasks later, on the
  stated grounds that a second copy would drift. The duplicate is now never
  written.
- **`retrace` does not import `ensemble`, in any build.** Task 5's
  integration test runs against an `httptest` fake that speaks ensemble's
  four routes, so `retrace/go.mod` still requires only `core` and design
  §1's "adopt retrace in CI without ever running ensemble" survives.

Dependency direction after the revision, verified acyclic:
`runs → trace` · `rules → ∅` · `config → rules` · `httpguard → ∅` ·
`capture → runs, proxy, trace, httpguard` · `pixel → config` ·
`diff → runs, rules, config, pixel, trace` · `refs → diff, runs, pixel` ·
`replay → runs, rules, diff, trace, httpguard` ·
`serve → diff, refs, runs, config, httpguard` · `cmd/retrace → all`.
Note `pixel → config` (for `RectsFrom`) and **not** the reverse: `config` is
the leaf every engine reads, so it may not import an engine.
On the TS side: `design-system → react` only, and both
`ensemble-ui → design-system` and `retrace-ui → design-system` — the hook
introduces no edge between the two apps, which is why it lives in the
design system rather than in either one of them.

**4. Cross-reference check (run after the revision round).** The plan refers
to tasks by number in prose throughout, and an earlier insertion renumbered
five of them, so every reference was re-resolved against the current
numbering rather than assumed: **190 task references checked** (`Task N`,
`Tasks N, M`, `# TN` File-Structure markers, and `TODO(task-N)`), across all
18 tasks plus the header, File Structure, porting inventory, wrap-up and
this self-review.

Four were stale and are fixed:

| Where | Said | Now |
|---|---|---|
| File Structure, `core/httpguard` | Task 13 | **T4** (R7 moved the extraction) |
| Self-review coverage table, adapters rows (×2) | 16 | **17** |
| Self-review coverage table, review-queue row | 13, 14 | **13 (REST), 15 (UI)** |
| File Structure, `diff/deviations.go` | T11 | **T8 types, T11 ledger** (R2) |

The rest resolve correctly. Two conventions make this checkable rather than
a matter of trust: every `Produces` block names the task numbers that
consume it, and every task that fills another's seam names the seam's
owner in its own Files block.
