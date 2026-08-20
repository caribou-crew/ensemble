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

**What part 2 (`docs/superpowers/plans/<date>-phase-4b-retrace-encryption.md`)
must cover — do not drop it:**

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
- Every new HTTP route sits behind the existing Origin/Host guard
  (`ensemble/server/guard.go`, extracted to `core/httpguard` in Task 13).
  Control planes bind loopback only (`127.0.0.1`).
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
- One hop schema. `core/trace.Hop` (`schema: "ensemble/1"`) is what
  `wire.jsonl` and `hops.jsonl` contain, verbatim. retrace never defines a
  parallel record type for captured traffic.
- Redaction happens at capture, never post-hoc: every write path in this
  plan pushes hops through a `*trace.Redactor` before they touch disk, so
  Phase 4b can swap in per-key modes at one seam.
- API-first parity: every verb the review UI offers is a REST call an agent
  can make identically, with the same effect.

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
  Note it in the openspec change when part 1 lands.
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
  report `truncated` and skip (Task 8, Step 6).
- flowlens had zero TODO/FIXME and comments that explain *why*. Carry the
  explanatory comments across when porting; they are the design record.

---

## File Structure

```
core/
  httpguard/guard.go          # Task 13: the Origin/Host + Sec-Fetch-Site guard,
  httpguard/guard_test.go     #   moved out of ensemble/server so both products share it
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
    summary.go summary_test.go        # T10
    deviations.go deviations_test.go  # T11 (in diff, not refs — one-way dependency)
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
  func PathsFor(root, app, flow, runID string) Paths
  func Create(root, app, flow, runID string) (Paths, error)
  func ListApps(root string) []string
  func ListFlows(root, app string) []string
  func ListRuns(root, app, flow string) []string         // lexical order == chronological
  func FindRun(root, app, flow, selector string) string  // "latest" | runId | short sha; "" = none
  func WriteManifest(p Paths, m Manifest) error
  func ReadManifest(path string) (Manifest, error)
  func ReadHops(path string) ([]trace.Hop, error)        // "" file → nil, nil
  func AppendGroupRecord(runDir string, r GroupRecord) error
  func ReadGroupRecords(runDir string) ([]GroupRecord, error)
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

func PathsFor(root, app, flow, runID string) Paths {
	dir := filepath.Join(root, app, flow, runID)
	return Paths{
		RunDir:       dir,
		ManifestPath: filepath.Join(dir, "manifest.json"),
		ShotsDir:     filepath.Join(dir, "shots"),
		WirePath:     filepath.Join(dir, "wire.jsonl"),
		HopsPath:     filepath.Join(dir, "hops.jsonl"),
		GroupsPath:   filepath.Join(dir, "groups.jsonl"),
		MissesPath:   filepath.Join(dir, "misses.jsonl"),
	}
}

func Create(root, app, flow, runID string) (Paths, error) {
	p := PathsFor(root, app, flow, runID)
	if err := os.MkdirAll(p.ShotsDir, 0o755); err != nil {
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

func ListApps(root string) []string              { return dirNames(root) }
func ListFlows(root, app string) []string         { return dirNames(filepath.Join(root, app)) }
func ListRuns(root, app, flow string) []string    { return dirNames(filepath.Join(root, app, flow)) }

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
	hops, err := ReadHops(path)
	if err != nil {
		t.Fatalf("ReadHops: %v", err)
	}
	if len(hops) != 2 || hops[1].To != "cart" {
		t.Fatalf("hops = %+v", hops)
	}
	missing, err := ReadHops(dir + "/nope.jsonl")
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
	Device      *Device      `json:"device,omitempty"`
	Groups      []Group      `json:"groups,omitempty"`
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
	Name   string `json:"name"`
	File   string `json:"file"` // run-dir-relative, e.g. "shots/cart.png"
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Device struct {
	Name   string `json:"name,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Source string `json:"source,omitempty"` // "playwright" | "maestro" | "manual"
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
	Retrace   string `json:"retrace"`
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

func WriteManifest(p Paths, m Manifest) error {
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
func ReadHops(path string) ([]trace.Hop, error) {
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
	dir := t.TempDir()
	if err := AppendGroupRecord(dir, GroupRecord{Phase: "start", Name: "a", TS: ts("2026-08-21T10:00:00Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	if err := appendRaw(dir, "{not json\n"); err != nil {
		t.Fatalf("appendRaw: %v", err)
	}
	if err := AppendGroupRecord(dir, GroupRecord{Phase: "end", TS: ts("2026-08-21T10:00:05Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	got, err := ReadGroupRecords(dir)
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

func AppendGroupRecord(runDir string, r GroupRecord) error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(runDir, "groups.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ReadGroupRecords tolerates corrupt lines: a half-written marker from a
// killed test process must not make the whole run unreadable.
func ReadGroupRecords(runDir string) ([]GroupRecord, error) {
	f, err := os.Open(filepath.Join(runDir, "groups.jsonl"))
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

Run: `go build ./retrace/... && go run ./retrace/cmd/retrace --version && go run ./retrace/cmd/retrace bogus; echo "exit=$?"`
Expected: prints `dev`, then `retrace: unknown command "bogus"` plus usage, `exit=3`.

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

Add `"net/http"` to the import block (used for `http.TimeFormat`).

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
      WireIgnore       []string
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
  type Rect struct { X, Y, Width, Height int }
  type Thresholds struct { Gate, Fine float64 }   // defaults 0.1 / 0.05
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

func TestAnInvalidMatcherFailsLoadNamingTheRule(t *testing.T)  // wire_rules[0].headers.date: "httpdate" → error mentions "wireRules[0].headers.date"
func TestMasksForFallsBackToTheWildcardCheckpoint(t *testing.T) // masks: {"*": [...]} applies to every checkpoint
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: Load`).

- [ ] **Step 3: Implement `retrace/config/config.go`**

Key body (the rest is straightforward struct tags):
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
	if c.Thresholds.Gate == 0 {
		c.Thresholds.Gate = 0.1
	}
	if c.Thresholds.Fine == 0 {
		c.Thresholds.Fine = 0.05
	}
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
	c := &Config{Dir: cwd, Thresholds: Thresholds{Gate: 0.1, Fine: 0.05}}
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

`MasksFor` resolves per-flow masks first, then top-level, then the `"*"`
wildcard key, returning the first non-empty list.

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
  func NewMarkerDoor(runDir string, now func() time.Time) http.Handler
  ```

**Design note — why a third env var.** The spec names `RETRACE_RUN_DIR` and
`RETRACE_PROXY_URL`. Those cover file-writing adapters (retrace-js,
retrace-playwright). Maestro cannot write files from a flow; flowlens solved
this with a marker door mounted ON the proxy (`POST /flowlens/group`). Here
the proxy in ensemble-attached mode is ensemble's own session edge listener,
which must stay a pure forwarder — so retrace always opens its own loopback
marker door and exports `RETRACE_MARKER_URL`. **Report this to the spec
owner as an additive extension**; the two spec'd variables remain
authoritative and sufficient for the file-writing adapters.

- [ ] **Step 1: Write the failing marker-door test**

`retrace/capture/markers_test.go`:
```go
func TestMarkerDoorAppendsStartAndEndRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(NewMarkerDoor(dir, func() time.Time { return now }))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/group", "application/json", strings.NewReader(`{"name":"checkout"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start marker: %v status=%v", err, resp.StatusCode)
	}
	resp, _ = http.Post(srv.URL+"/group/end", "application/json", strings.NewReader(`{}`))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("end marker status = %d", resp.StatusCode)
	}
	recs, _ := runs.ReadGroupRecords(dir)
	if len(recs) != 2 || recs[0].Name != "checkout" || recs[1].Phase != "end" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestMarkerDoorRejectsAnUnnamedStart(t *testing.T) {
	// 400 is the healthy answer and is what a preflight probe keys on: the
	// door exists and refused a nameless marker. Anything else means some
	// OTHER server holds the port.
}

func TestMarkerDoorRejectsCrossSiteBrowserRequests(t *testing.T) {
	// Sec-Fetch-Site: cross-site → 403. The door is loopback-bound but a
	// page the developer has open can still reach 127.0.0.1.
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: NewMarkerDoor`).

- [ ] **Step 3: Implement `retrace/capture/markers.go`**

```go
package capture

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// NewMarkerDoor is the HTTP face of flow-part markers, for runners that
// cannot write files (Maestro). Two routes, registered per-method: a
// method-less pattern would panic at registration against any "GET /"
// sibling, and the bare paths are registered explicitly so a POST is never
// answered with a subtree-redirect 301, which drops the body.
func NewMarkerDoor(runDir string, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /group", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name  string `json:"name"`
			Quiet bool   `json:"quiet"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Name) == "" {
			http.Error(w, `{"error":"group markers require a non-empty name"}`, http.StatusBadRequest)
			return
		}
		if err := runs.AppendGroupRecord(runDir, runs.GroupRecord{
			Phase: "start", Name: body.Name, TS: now(), Quiet: body.Quiet,
		}); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /group/end", func(w http.ResponseWriter, r *http.Request) {
		if err := runs.AppendGroupRecord(runDir, runs.GroupRecord{Phase: "end", TS: now()}); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// Same reasoning as ensemble/server/guard.go: loopback keeps the network
	// out but not a browser tab. Sec-Fetch-Site is set by the browser and
	// cannot be forged by page script.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
			http.Error(w, `{"error":"cross-site browser requests are not permitted"}`, http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Run — expect PASS.**

- [ ] **Step 5: Write the failing standalone-capture test**

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

	hops, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil || len(hops) != 1 { t.Fatalf("wire.jsonl hops = %v (%v)", hops, err) }
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
	// RETRACE_RUN_DIR, RETRACE_PROXY_URL, RETRACE_MARKER_URL all present and
	// non-empty; RETRACE_RUN_DIR == s.Paths.RunDir.
}

func TestCheckpointsReadsShotGeometryFromPngHeaders(t *testing.T) {
	// write a 40x40 PNG into s.Paths.ShotsDir/cart.png → Checkpoints()
	// returns {Name:"cart", File:"shots/cart.png", Width:40, Height:40}.
}
```

- [ ] **Step 6: Run — expect FAIL** (`undefined: StartStandalone`).

- [ ] **Step 7: Implement `retrace/capture/capture.go`**

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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

	rec        *proxy.Recorder
	prox       *proxy.Proxy
	stopProxy  func()
	markerSrv  *http.Server
	wireFile   *os.File
	requests   atomic.Int64
	closed     bool
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
	door := NewMarkerDoor(s.Paths.RunDir, now)
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

func (s *Session) Hops() []trace.Hop { return s.rec.Snapshot() }

// RequestsSeen counts everything that reached retrace at all — proxied calls
// plus markers. Zero of these is proof the app never routed through us,
// which is a different (and much worse) fact than "the flow made no calls".
func (s *Session) RequestsSeen() int { return len(s.rec.Snapshot()) + int(s.requests.Load()) }

func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.stopProxy()
	if s.markerSrv != nil {
		s.markerSrv.Close()
	}
	return s.wireFile.Close()
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
		out = append(out, runs.Checkpoint{
			Name:   strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())),
			File:   filepath.ToSlash(filepath.Join("shots", e.Name())),
			Width:  cfg.Width,
			Height: cfg.Height,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// gitSHA shells out to git for the run id's provenance suffix. A repo-less
// directory is normal (a user trying retrace in /tmp), so failure is "" and
// NewRunID falls back to "nogit" — never an error that blocks a recording.
func gitSHA(dir string) string { /* exec.Command("git","-C",dir,"rev-parse","HEAD"); trim; "" on error */ }
```

Also add `gitInfo(dir string) runs.Git` (sha, branch, `git status --porcelain`
non-empty → dirty) in the same file — Task 6's manifest needs it and Task 11's
reference eligibility rules read `Git.Dirty`.

- [ ] **Step 8: Run — expect PASS** (`go test -race ./retrace/capture/ -v`).

- [ ] **Step 9: `retrace run` command, standalone path**

`retrace/cmd/retrace/cmd_run.go` — flag set `--flow` (required), `--app`
(default: the config's `app`, else the cwd base name), `--upstream`,
`--ensemble` (default `$ENSEMBLE_API` or `http://127.0.0.1:4700`),
`--no-ensemble`, `--json`; everything after `--` is the test command.

```go
func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		flow     = fs.String("flow", "", "flow name to record (required)")
		app      = fs.String("app", "", "app name (default: config app, else the directory name)")
		upstream = fs.String("upstream", "", "standalone: base URL clients would call")
		ensembleURL = fs.String("ensemble", envOr("ENSEMBLE_API", "http://127.0.0.1:4700"), "ensemble control-plane URL")
		noEnsemble  = fs.Bool("no-ensemble", false, "force standalone capture even if ensemble is up")
		asJSON      = fs.Bool("json", false, "emit the manifest as JSON on stdout")
	)
	cmdArgs, err := splitDoubleDash(args)   // returns (flagArgs, testCmd []string)
	...
	// Attach decision, in one place so the manifest's Mode is never a guess:
	//   --no-ensemble          → standalone
	//   ensemble health check OK and config.Entry set → ensemble (Task 5)
	//   otherwise              → standalone, with a one-line note on stderr
}
```

The run body: start the session → `exec.CommandContext` with
`os.Environ()` + `session.Env()`, stdout/stderr piped through → wait →
capture exit code and duration → `session.Close()` → assess trust (Task 6)
→ write manifest → print summary (or `--json`).

- [ ] **Step 10: CLI test**

`retrace/cmd/retrace/cmd_run_test.go`:
`TestRunStandaloneRecordsAndWritesAManifest` — spins an httptest upstream,
runs `run --flow checkout --app web --no-ensemble --upstream <url> -- <a
tiny "go run"-free command>`. Use `sh -c 'curl -s "$RETRACE_PROXY_URL/cart" >
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

- [ ] **Step 11: Full suites + commit**

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
- Modify: `retrace/cmd/retrace/cmd_run.go` (attach path)

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
  type EndReport struct { Hops int; Verdict trace.Verdict; Reasons []string }
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

`retrace/capture/ensemble_test.go`:
```go
// fakeEnsemble serves hops on a delay so the test reproduces the real
// timing: a downstream call that completes AFTER the test command exits.
type fakeEnsemble struct {
	mu       sync.Mutex
	hops     []trace.Hop
	ended    bool
	endCount int
}

func (f *fakeEnsemble) Health(context.Context) error { return nil }
func (f *fakeEnsemble) StartSession(_ context.Context, id, entry string) (string, error) { return "127.0.0.1:0", nil }
func (f *fakeEnsemble) SessionHops(context.Context) ... // returns the current slice
func (f *fakeEnsemble) EndSession(...) (EndReport, error) { f.ended = true; return EndReport{Hops: len(f.hops), Verdict: trace.VerdictOK}, nil }

func TestDrainWaitsForLateHopsBeforeEndingTheSession(t *testing.T) {
	f := &fakeEnsemble{}
	f.push(hop(1, "edge"))
	go func() {                       // a downstream call finishing 150ms late
		time.Sleep(150 * time.Millisecond)
		f.push(hop(2, "catalog"))
	}()

	s := attachedSessionFor(t, f)
	if err := s.Drain(context.Background()); err != nil { t.Fatalf("Drain: %v", err) }
	if err := s.Close(); err != nil { t.Fatalf("Close: %v", err) }

	hops, _ := runs.ReadHops(s.Paths.HopsPath)
	if len(hops) != 2 {
		t.Fatalf("Drain must not end the session before late hops land: got %d, want 2", len(hops))
	}
}

func TestHopsArrivingAfterTheDrainWindowDegradeTheVerdict(t *testing.T) {
	// EndSession reports 3 hops but only 2 were written → session's
	// TrustNote records "1 hop(s) arrived after the drain window" and the
	// capture status is at least suspect.
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
Update `Session.Hops()` to return `s.hops` when attached and
`s.rec.Snapshot()` when standalone.

- [ ] **Step 4: Run — expect PASS** (`go test -race ./retrace/capture/ -run Drain -v`).

- [ ] **Step 5: Implement `retrace/cmd/retrace/client.go`**

A `Client` with `Health`, `StartSession`, `SessionHops`, `EndSession`.
`SessionHops` must read **NDJSON**, not a JSON array — use
`trace.NewReader(resp.Body)` and loop to `trace.ErrEOF`. Errors follow the
server's `{"error":"..."}` convention: non-2xx → `fmt.Errorf("%s %s: %s",
method, path, body.Error)`.

- [ ] **Step 6: Wire the attach path into `cmdRun`**

```go
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

- [ ] **Step 7: Integration test against a REAL ensemble server**

`retrace/cmd/retrace/cmd_run_ensemble_test.go`:
`TestRunAttachedToALiveEnsembleRecordsTheFullChain` — build a live
`ensemble/server.New(Deps{...})` in-process over a two-service fake stack
(an "edge" proxy target whose upstream calls a "catalog" target), point
`retrace run` at it, assert `hops.jsonl` has both hops sharing one traceId
and `wire.jsonl` has only the edge hop. This is the "one schema, two
consumers" scenario from the core-trace-model spec, executed end to end.

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
  const GapThreshold = 60 * time.Second
  type ProxyFailure struct { Phase string; Message string } // Phase: "bind" | "running"
  type AssessInput struct {
      ProxyConfigured     bool
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
		{"clean run", AssessInput{ProxyConfigured: true, Hops: hops(3), Checkpoints: 2, RequestsSeen: 3}, trace.VerdictOK, ""},
		{"failed test outranks everything", AssessInput{TestExitCode: 1, ProxyConfigured: true, Hops: hops(3), RequestsSeen: 3}, trace.VerdictFailed, "test-failed"},
		{"proxy never bound", AssessInput{ProxyConfigured: true, ProxyFailure: &ProxyFailure{Phase: "bind", Message: "address in use"}, RequestsSeen: 0}, trace.VerdictBroken, "proxy-never-started"},
		{"proxy died mid-run", AssessInput{ProxyConfigured: true, ProxyFailure: &ProxyFailure{Phase: "running", Message: "closed"}, Hops: hops(1), RequestsSeen: 1}, trace.VerdictBroken, "proxy-died"},
		{"zero calls AND zero requests", AssessInput{ProxyConfigured: true, RequestsSeen: 0}, trace.VerdictBroken, "proxy-never-reached"},
		{"zero calls but requests seen", AssessInput{ProxyConfigured: true, RequestsSeen: 4}, trace.VerdictDegraded, "no-calls"},
		{"zero calls, reachability unknown", AssessInput{ProxyConfigured: true, RequestsSeen: -1}, trace.VerdictDegraded, "no-calls"},
		{"screenshots vanished", AssessInput{ProxyConfigured: true, Hops: hops(2), RequestsSeen: 2, Checkpoints: 0, ExpectedCheckpoints: 5}, trace.VerdictDegraded, "no-screenshots"},
		{"ensemble reported a propagation gap", AssessInput{ProxyConfigured: true, Hops: hops(2), RequestsSeen: 2, SessionVerdict: trace.VerdictDegraded, SessionReasons: []string{"propagation gap at bff: traceparent forwarded but baggage dropped before catalog"}}, trace.VerdictDegraded, "propagation-gap"},
		{"drain shortfall", AssessInput{ProxyConfigured: true, Hops: hops(2), RequestsSeen: 2, Notes: []string{"1 hop(s) arrived after the drain window"}}, trace.VerdictSuspect, "capture-note"},
	}
	// each: Assess(in).Status == want, and want=="" or a reason with that Code exists
}

func TestFindGapsSubtractsDeclaredQuietIntervals(t *testing.T) {
	// two hops 120s apart, with a quiet group covering 90s of it → no gap
	// (a flow that declared "waiting for a push notification" explained its
	// own silence); without the quiet group → one 120s gap, status suspect.
}

func TestAWireOnlyFlowIsNeverNaggedAboutScreenshots(t *testing.T) {
	// ExpectedCheckpoints == 0 and Checkpoints == 0 → ok.
}

func TestNoScreenshotReasonIsSuppressedWhenTheTestFailed(t *testing.T) {
	// blaming the capture for a test that fell over early points at the
	// wrong thing — status is failed, and only test-failed is reported.
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: Assess`).

- [ ] **Step 3: Implement `retrace/capture/trust.go`** — port
  `assessCapture` reason-for-reason. Key structure:

```go
// GapThreshold is how long a stretch with no captured call has to be before
// it counts as evidence. Measured BETWEEN consecutive calls only, never
// against the run's own start/end: the app launching before its first call,
// and teardown after the last, are normal and would fire on every run.
const GapThreshold = 60 * time.Second

func Assess(in AssessInput) runs.CaptureTrust {
	threshold := in.GapThreshold
	if threshold <= 0 {
		threshold = GapThreshold
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

	switch {
	case in.ProxyFailure != nil && in.ProxyFailure.Phase == "bind":
		add("proxy-never-started", trace.VerdictBroken,
			"the capture listener could not bind: "+in.ProxyFailure.Message,
			"free the port (lsof -ti tcp:<port>) and re-run — this capture recorded no calls")
	case in.ProxyFailure != nil:
		add("proxy-died", trace.VerdictBroken,
			"the capture listener stopped during the run: "+in.ProxyFailure.Message,
			"re-run — calls made after it stopped were never recorded")
	}

	// Zero calls is ambiguous on its own: "genuinely quiet" and "the app
	// never routed through us" look identical. RequestsSeen (markers
	// included, a strictly broader count) tells them apart; -1 means we
	// could not verify, which must say so rather than read as either.
	if in.ProxyConfigured && in.ProxyFailure == nil && len(in.Hops) == 0 && in.TestExitCode == 0 {
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

`FindGaps` sorts `hop.T.Start` values, walks consecutive pairs, subtracts
overlap with any `Quiet` group, and emits a `runs.Gap` when the remainder
reaches the threshold.

- [ ] **Step 4: Run — expect PASS** (`go test -race ./retrace/capture/ -run 'Assess|Gaps|Screenshot' -v`).

- [ ] **Step 5: Banner it in `cmdRun`**

After `session.Close()`: compute `ExpectedCheckpoints` from the previous run
of the same app/flow (`runs.ListRuns` → second-to-last → its manifest's
`len(Checkpoints)`; `-1` when there is no history), call `Assess`, store it
in `Manifest.Capture`, and print — **to stderr, before the summary, always**:

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
- Consumes: stdlib `image`, `image/png`, `image/draw` only.
- Produces (used by Tasks 10, 11, 13, 16):
  ```go
  package pixel
  type Rect struct { X, Y, Width, Height int }
  type Options struct { Masks []Rect; GateThreshold, FineThreshold float64; WantDiff, WantOverlay bool }
  type Overlap struct { Width, Height int; DiffPct, DiffPctFine float64; NumDiff int; PaddingPct float64 }
  type Result struct {
      Width, Height  int
      DiffPct        float64
      DiffPctFine    float64
      NumDiff        int
      Mismatch       bool
      PaddedForDiff  bool
      WidthA, HeightA, WidthB, HeightB int
      Overlap        *Overlap
  }
  type Images struct { Diff, Overlay *image.RGBA } // nil when not requested or NumDiff == 0
  func Compare(aPNG, bPNG []byte, o Options) (Result, Images, error)
  func Match(a, b, out *image.RGBA, threshold float64, diffMask bool) int
  func ApplyMasks(img *image.RGBA, rects []Rect)
  func TrimUniformBorder(pngBytes []byte) ([]byte, error)
  func Encode(img *image.RGBA) ([]byte, error)
  func Decode(pngBytes []byte) (*image.RGBA, error)
  ```
  Defaults when zero: `GateThreshold = 0.1`, `FineThreshold = 0.05`.

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
1. decode both; record `WidthA/HeightA/WidthB/HeightB`.
2. if sizes differ → compute `Overlap` on the **cropped intersection** first
   (that is the only number that means "the content changed"), then pad both
   onto the union canvas and set `PaddedForDiff`.
3. `ApplyMasks` on both.
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
pixel, **refusing** to trim when the result would be <2px in either
dimension or when nothing would change — a fully uniform shot means nothing
rendered, and trimming it to a sliver destroys the evidence.
Tests: `TestTrimsAUniformBorder`, `TestRefusesToTrimAFullyUniformImage`,
`TestLeavesAnAlreadyTightImageByteIdentical`.

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

**Interfaces:**
- Consumes: `core/trace.Hop`, `retrace/rules`, `retrace/config`, `retrace/runs`.
- Produces (used by Tasks 10, 12, 13, 16):
  ```go
  package diff
  type Options struct {
      WireIgnore []string
      Rules      []rules.Rule
      Normalize  func(path string) string        // config.NormalizePath
      GroupsA    []runs.Group
      GroupsB    []runs.Group
      Deviations []Deviation                     // defined in diff/deviations.go (Task 11); nil until then
  }
  type Pair struct { Method, NormalizedPath string; A, B trace.Hop }
  func PairCalls(a, b []trace.Hop, normalize func(string) string) (pairs []Pair, missing, extra []trace.Hop)
  func CallSimilarity(a, b trace.Hop) float64
  func NormalizeQuery(rawQuery string) string
  func SplitPath(hopPath string) (path, rawQuery string)

  type FieldDiff struct { Scope, Path, Type string; A, B any; Matcher, Glob string }
  type HeaderDiff struct { Scope, Name, Type string; A, B string; Matcher string }
  type StatusChange struct { A, B int }
  type Entry struct {
      Method, NormalizedPath string
      SeqA, SeqB             uint64
      PosA, PosB             int
      GroupA, GroupB         string
      Moved                  bool
      Truncated              bool
      Classes                []string
      StatusChange           *StatusChange
      BodyDiff               []FieldDiff
      BodyTolerated          []FieldDiff
      BodyViolations         []FieldDiff
      BodyIgnored            []FieldDiff
      OrderingChanges        []FieldDiff
      HeaderDiff             []HeaderDiff
  }
  type Wire struct { Paired []Entry; Missing, Extra []Call; Groups *GroupNames }
  type Call struct { Method, Path string; Seq uint64; Status int; Group string; Tolerated *ToleratedNote }
  type GroupNames struct { A, B []string `json:"a","b"` }
  func DiffWire(a, b []trace.Hop, o Options) Wire
  func DiffHeaders(a, b map[string]string, res rules.Resolved, scope string) []HeaderDiff

  type Section struct { Name string; Entries []Entry; Counts map[string]int }
  func BuildSections(entries []Entry, groups *GroupNames) []Section
  func LISIndices(seq []int) []int
  ```

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
  values compare equal regardless of key order (`json.Marshal` on a
  `map[string]any` already sorts, but arrays of maps need the recursive
  form — write it explicitly, don't rely on the marshaller).
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
  // status.go
  type StatusFinding struct { Seq uint64; Method, Path string; Status int }
  func MatchURLGlob(pattern, urlPath string) bool     // query stripped before matching
  func FindUnexpectedStatuses(hops []trace.Hop, expected []config.StatusRule) []StatusFinding

  // hop.go
  const DefaultCountTolerance = 0.5
  type ServiceCount struct { Service string; A, B int; Deviates bool }
  type Route struct { To, Method, Path string }
  type RouteFailure struct { Method, Path string; ExpectedStatus int; ActualStatus int; Reason string } // "missing" | "wrong-status"
  type HopDiff struct {
      ServiceCounts         []ServiceCount
      NewErrors, GoneErrors []StatusFinding
      NewRoutes, GoneRoutes []Route
      RequiredRouteFailures []RouteFailure
      HopRequireConfigured  bool
  }
  type HopOptions struct { Normalize func(string) string; Expected []config.StatusRule; Require []config.RequiredRoute; CountTolerance float64 }
  func DiffHops(a, b []trace.Hop, o HopOptions) HopDiff
  func RequiredRouteFailures(hops []trace.Hop, require []config.RequiredRoute) []RouteFailure

  // perf.go
  type PerfResult struct { Status string; MeasuredMs, BudgetMs float64 } // "ok" | "over" | "unset"
  type PerfBudget struct { BudgetMs float64; SampleCount int; MeasuredMaxMs, MeasuredMedianMs, MarginFactor float64 }
  func TotalCallDurationMs(hops []trace.Hop) float64
  func DerivePerfBudget(samples []float64, marginFactor float64) (PerfBudget, error)
  func CheckPerfBudget(hops []trace.Hop, budgetMs float64) PerfResult

  // openapi.go
  type ConformanceFinding struct { Method, Path string; Status int; Kind, Detail string } // Kind: "unknown-path"|"unknown-method"|"undocumented-status"|"missing-required-field"
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

- [ ] **Step 2: hop.go, test first**

```go
func TestAnAddedDownstreamCallIsListedAsANewRoute(t *testing.T)  // the spec's headline scenario
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
`TotalCallDurationMs` sums `hop.T.DoneMs`. `DerivePerfBudget` defaults
`marginFactor` to 1.5 when zero.

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
- Consumes: everything from Tasks 7–9, `runs.Manifest`, `capture.Fatal`.
- Produces (used by Tasks 11, 13, 15, 16):
  ```go
  package diff
  const SummarySchema = "retrace-diff/1"
  type RunRef struct { RunID, Kind, Dir string; Manifest runs.Manifest } // Kind: "run" | "reference"
  type CheckpointVerdict struct {
      Name       string
      Verdict    string          // "ok" | "changed" | "missing" | "added" | "unreadable"
      DiffPct    float64
      DiffPctFine float64
      NumDiff    int
      Mismatch   bool
      Overlap    *pixel.Overlap
      Images     CheckpointImages // run-dir-relative paths, "" when not written
  }
  type CheckpointImages struct { A, B, Diff, Overlay string }
  type Counts struct { Checkpoints, PixelChanged, WirePaired, WireChanged, WireMoved, WireMissing, WireExtra, Violations, HopNew, HopGone, UnexpectedStatuses, Conformance int }
  type Summary struct {
      Schema      string
      App, Flow   string
      A, B        RunRef
      Verdict     string        // "pass" | "changed" | "failed"
      Checkpoints []CheckpointVerdict
      Wire        Wire
      Sections    []Section
      Hops        HopDiff
      UnexpectedStatuses []StatusFinding
      Perf        PerfResult
      Conformance []ConformanceFinding
      Capture     CaptureBanner
      Counts      Counts
      Gates       []string      // human-readable reasons the verdict is "failed"
  }
  type CaptureBanner struct { A, B runs.CaptureTrust }
  type BuildInput struct {
      App, Flow string
      A, B      RunRef
      Cfg       *config.Config
      Options   Options
      WantImages bool
      OutDir    string          // where diff/overlay PNGs are written (usually B's run dir)
  }
  func Build(in BuildInput) (Summary, error)
  func ExitCode(s Summary) int          // 0 pass, 1 changed, 2 failed
  func RenderText(w io.Writer, s Summary)
  ```

**Verdict rules (the CI contract — assert each one in a test):**
- `failed` (exit 2) if ANY of: a rule `Violation` exists;
  `RequiredRouteFailures` is non-empty; `UnexpectedStatuses` is non-empty;
  `Perf.Status == "over"`; `capture.Fatal` is true for either side.
  Unexpected ≥400 fails the run **regardless of pixel/wire results** — the
  spec's explicit scenario.
- `changed` (exit 1) if not failed and any of: a checkpoint verdict is not
  `ok`; `Wire` has changed/moved/missing/extra entries; `HopDiff` has new or
  gone routes or a deviating service count; `Conformance` is non-empty.
- `pass` (exit 0) otherwise.

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
func TestACaptureTrustBannerRidesAlongInJsonAndText(t *testing.T)
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

	// --- pixel, per checkpoint, by name union so a checkpoint that
	// appeared or vanished is its own verdict rather than a silent skip.
	for _, name := range checkpointUnion(in.A.Manifest, in.B.Manifest) { ... }

	// --- wire, from each side's client-edge hops
	hopsA, err := runs.ReadHops(filepath.Join(in.A.Dir, "wire.jsonl"))
	...
	s.Wire = DiffWire(hopsA, hopsB, in.Options)
	s.Sections = BuildSections(s.Wire.Paired, s.Wire.Groups)

	// --- hops, from the full chain; absent on a standalone run, and that
	// is reported as "not captured", never as "no differences".
	chainA, _ := runs.ReadHops(filepath.Join(in.A.Dir, "hops.jsonl"))
	chainB, _ := runs.ReadHops(filepath.Join(in.B.Dir, "hops.jsonl"))
	if chainA != nil || chainB != nil {
		s.Hops = DiffHops(chainA, chainB, HopOptions{...})
	}

	// --- auxiliary checks always run against side B (the candidate).
	s.UnexpectedStatuses = FindUnexpectedStatuses(append(hopsB, chainB...), in.Cfg.ExpectedStatuses)
	s.Perf = CheckPerfBudget(hopsB, in.Cfg.Flows[in.Flow].PerfBudgetMs)
	if in.Cfg.OpenAPI != "" {
		s.Conformance, err = CheckOpenAPI(hopsB, filepath.Join(in.Cfg.Dir, in.Cfg.OpenAPI))
	}

	s.Counts = countOf(s)
	s.Gates = gatesOf(s)
	switch {
	case len(s.Gates) > 0:
		s.Verdict = "failed"
	case changed(s):
		s.Verdict = "changed"
	default:
		s.Verdict = "pass"
	}
	return s, nil
}

func ExitCode(s Summary) int {
	switch s.Verdict {
	case "failed":
		return 2
	case "changed":
		return 1
	}
	return 0
}
```

`RenderText` prints, in this order: the capture-trust banner for either side
when non-ok; a per-checkpoint line (`✓ cart   0.00%` / `✗ receipt  2.14%
(fine 3.02%)  shots/diff/receipt.png`); a wire section per flow part with
worst-first entries; the hop deltas; then a `GATE:` line per entry in
`Gates`. Wide values are never truncated — a report an agent must read is
not a dashboard.

- [ ] **Step 4: Run — expect PASS.**

- [ ] **Step 5: `retrace diff` CLI**

`cmd_diff.go` flags: `--flow` (required), `--app`, `--a` (default
`reference`, else `previous`), `--b` (default `latest`), `--json`,
`--images` (default true), `--out`. Selector resolution: `reference` →
`refs.Resolve` (Task 11; until then, `--a` accepts only run selectors and
`reference` errors with "run `retrace ref accept` first"), otherwise
`runs.FindRun`.

Tests: `TestDiffExitsZeroOnIdenticalRuns`,
`TestDiffExitsOneWhenAFieldChanged`,
`TestDiffExitsTwoOnAnUnexpected500`,
`TestDiffJsonIsParseableAndCarriesTheVerdict`,
`TestDiffNamesTheMissingRunInsteadOfPanicking`.

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
- Create: `retrace/diff/deviations.go`, `retrace/diff/deviations_test.go`
- Create: `retrace/cmd/retrace/cmd_ref.go`, `retrace/cmd/retrace/cmd_ref_test.go`
- Modify: `retrace/cmd/retrace/main.go` (dispatch `ref`), `.gitignore`

**Interfaces:**
- Produces (used by Tasks 12, 13, 16):
  ```go
  package refs
  const MaxBundleBytes = 8 << 20   // 8 MiB per bundle, enforced at accept
  func BundleDir(cwd, app, flow string) string   // <cwd>/.retrace-ref/<app>/<flow>/reference
  type Candidate struct { RunID string; Eligible bool; Reason, Detail string }
  type Reference struct { Kind string; Dir, RunID string; Manifest runs.Manifest; Reason string; History []Candidate }
  func Resolve(cwd, runsRoot, app, flow string) Reference   // Kind: "bundle" | "run" | "none"
  type AcceptOptions struct { Cwd, RunsRoot, App, Flow, RunID string; Masks []pixel.Rect; MasksFor func(checkpoint string) []pixel.Rect; Force bool }
  type AcceptResult struct { Dir string; Files []string; RunID string; Bytes int64; CaptureStatus trace.Verdict }
  func Accept(o AcceptOptions) (AcceptResult, error)
  type RejectOptions struct { Cwd, RunsRoot, App, Flow, RunID, OutDir string; Summary *diff.Summary }
  type RejectResult struct { Dir string; Files []string }
  func Reject(o RejectOptions) (RejectResult, error)
  ```
  ```go
  package diff   // deviations.go — in diff, NOT refs: refs.RejectOptions carries a
                 // *diff.Summary, so refs → diff is the only direction available.
  type Deviation struct { ID, Status string; Apps [2]string; Method, Path, Reason string }
  type ToleratedNote struct { ID, Reason string }
  func LoadDeviations(file string) ([]Deviation, error)
  func ResolveDeviations(ds []Deviation, appA, appB string) []Deviation
  func FindDeviation(ds []Deviation, method, path string) *Deviation
  ```

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

- [ ] **Step 5: Implement `Accept`** — `os.RemoveAll` then copy; shots go
  through `pixel.Decode` → `pixel.ApplyMasks` → `pixel.Encode` when the
  checkpoint has masks, plain copy otherwise; sum bytes as it goes and fail
  with `fmt.Errorf("reference bundle for %s/%s would be %s, over the %s
  budget — the largest file is %s (%s); add a mask, trim the flow, or raise
  MaxBundleBytes deliberately", ...)`.

- [ ] **Step 6: `Reject` + deviations**

`Reject` copies the failing run's manifest, wire/hops, shots, plus the
`diff.Summary` as `summary.json`, into `<OutDir>/<app>__<flow>__<runId>/` —
a repro bundle someone can attach to a bug. Test:
`TestRejectEmitsASelfContainedReproBundle`.

`retrace/diff/deviations.go` ports `src/deviations.mjs` verbatim in spirit: an agent can
append `status: "proposed"` (visible, git-diffable, inert); only a human
flipping it to `approved` makes retrace honor it. Tests:
`TestOnlyApprovedDeviationsApply`, `TestAppPairMatchingIsOrderIndependent`,
`TestAMalformedEntryIsAnErrorNamingItsIndex`.

Wire `Deviations` into `diff.Options`: a matched deviation **annotates**
`Call.Tolerated` rather than removing the entry, so it stays visible in
every consumer's output and only stops counting as a finding.
Test in `retrace/diff`: `TestASanctionedDeviationAnnotatesButDoesNotHide`.

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
  type Key struct { Method, Path, Query string }
  type Exchange struct {
      Key      Key
      ReqBody  any
      Status   int
      Headers  map[string]string
      Body     string
      Seq      uint64
      used     int
  }
  type Bundle struct { Dir string; Manifest runs.Manifest; Exchanges []Exchange }
  func LoadBundle(dir string) (*Bundle, error)   // reads wire.jsonl through runs.ReadHops
  type MissField struct { Field, Expected, Actual string }
  type Result struct { Hit *Exchange; Miss bool; Nearest *Exchange; Diff []MissField }
  type Request struct { Method, Path, Query string; Body any }
  type Options struct {
      Rules       []rules.Rule
      Normalize   func(string) string
      QueryIgnore []string
      MissPath    string    // misses.jsonl
  }
  func (b *Bundle) Match(r Request, o Options) Result
  type Server struct { http.Handler }
  func NewServer(b *Bundle, o Options) *Server
  func (s *Server) Misses() []Miss
  func (s *Server) MissCount() int
  type Miss struct { TS time.Time; Kind string; Method, Path, Query string; Diff []MissField; Nearest *Key }
  type Drift struct { Method, Path string; Status StatusDrift; Fields []diff.FieldDiff }
  type RevalReport struct { Flow string; Checked int; Drifts []Drift; Verdict string }
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

- [ ] **Step 5: Implement `server.go`.** Routes register per-method for every
  verb over both `/{path...}` and the bare `/` (the ServeMux traps in Global
  Constraints), or — simpler and what this task does — a single
  `http.HandlerFunc` with no mux at all, since a mock server matches on its
  own table rather than on route patterns.

- [ ] **Step 6: `retrace replay` CLI**

```
retrace replay --ref FLOW [--app N] [--listen 127.0.0.1:0] [--json] -- <test command>
```
Resolves the reference via `refs.Resolve` (a `Kind == "none"` is exit 3 with
the candidate history printed), starts the server, exports
`RETRACE_PROXY_URL` + `RETRACE_RUN_DIR` + `RETRACE_MARKER_URL` exactly as
`retrace run` does, runs the command, then: **exit 2 if `MissCount() > 0`**,
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
- Create: `core/httpguard/guard.go`, `core/httpguard/guard_test.go`
- Modify: `ensemble/server/guard.go`, `ensemble/server/server.go:89`
- Create: `retrace/serve/queue.go`, `retrace/serve/queue_test.go`
- Create: `retrace/serve/server.go`, `retrace/serve/routes.go`, `retrace/serve/routes_test.go`
- Create: `retrace/cmd/retrace/cmd_serve.go`
- Modify: `retrace/cmd/retrace/main.go`

**Interfaces:**
- Produces:
  ```go
  package httpguard
  // Handler wraps h with the DNS-rebinding and cross-origin protections a
  // loopback control plane needs. allowedHosts extends the always-allowed
  // loopback literals and "localhost"; the single entry "*" disables
  // host/origin matching (Sec-Fetch-Site is still enforced).
  func Handler(allowedHosts []string, h http.Handler) http.Handler
  ```
  ```go
  package serve
  type Item struct {
      App, Flow string
      Verdict   string          // diff.Summary.Verdict
      Score     float64         // worst-first sort key
      RunID     string
      RefRunID  string
      Counts    diff.Counts
      Capture   diff.CaptureBanner
      Gates     []string
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
  ```

**Routes** (JSON; errors `{"error":"..."}`; every one behind
`httpguard.Handler`):
```
GET  /api/health                              → {ok, version}
GET  /api/queue                               → {items: Item[]}          worst first
GET  /api/queue/{app}/{flow}                  → {summary: diff.Summary}
POST /api/queue/{app}/{flow}/accept           → {ok, bundle: {dir, files, bytes}}
POST /api/queue/{app}/{flow}/reject           → {ok, repro: {dir, files}}
POST /api/queue/{app}/{flow}/rule             → {ok, rule, rules: RawRule[]}
GET  /api/shots/{app}/{flow}/{side}/{name}    → image/png   side: a|b|diff|overlay
GET  /                                        → embedded retrace-ui (Task 15)
```
`POST .../rule` body: `{"scope":"resp","field":"cart.updatedAt","matcher":"iso8601","method":"GET","path":"/cart"}`.

**Worst-first ordering.** `Score` = `1000` if `Verdict == "failed"`, plus
`100 * len(Gates)`, plus `10 * (HopNew + HopGone)`, plus `PixelChanged`,
plus `WireChanged + WireMissing + WireExtra`. Passing flows score 0 and the
UI collapses them. The formula lives in one exported function,
`ScoreOf(s diff.Summary) float64`, so the UI never re-derives it.

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
// core/httpguard so retrace's review server gets the identical treatment —
// the rationale (CSRF against an unauthenticated loopback control plane;
// DNS rebinding) applies to both products, and a second copy would drift.
func guard(allowedHosts []string, h http.Handler) http.Handler {
	return httpguard.Handler(allowedHosts, h)
}
```
and `server.go:89` becomes `return guard(d.AllowedHosts, mux)`.

Move `ensemble/server/guard_test.go`'s cases into
`core/httpguard/guard_test.go` (they currently exercise the guard through
`server.New`; rewrite them against `httpguard.Handler` directly), and leave
ONE integration test in `ensemble/server` —
`TestServerRejectsCrossOriginRequests` — proving the wiring survived.

Run: `go test -race ./core/httpguard/ ./ensemble/server/ -v` → PASS.
Commit this step on its own: `refactor(core): extract the Origin/Host guard
into core/httpguard for both products`.

- [ ] **Step 2: Write the failing queue test**

```go
func TestQueueIsWorstFirstWithPassingFlowsLast(t *testing.T)
func TestAFlowWithNoReferenceAppearsWithAReasonNotSilentlyMissing(t *testing.T)
func TestQueueSurvivesAnUnreadableRunDirectory(t *testing.T) {
	// one broken flow must not take the whole queue down — it becomes an
	// item whose Verdict is "failed" and whose Gates name the read error.
}
```

- [ ] **Step 3: Implement `BuildQueue`** — for each app/flow under the runs
  root: resolve the reference (A) and the latest run (B), `diff.Build`,
  `ScoreOf`, sort descending, stable by `app/flow` for ties.

- [ ] **Step 4: Write the failing routes test**

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

- [ ] **Step 5: Implement `routes.go`**

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

- [ ] **Step 6: `retrace serve` CLI** — `--addr 127.0.0.1:4800` (loopback
  only; a non-loopback bind requires an explicit `--allow-host` and prints a
  warning), `--open`. Reuses `server.Serve`-style graceful shutdown:
  `BaseContext` ties every connection to the CLI's context so Ctrl-C is
  immediate.

- [ ] **Step 7: Full suites + commit**

```bash
go test -race ./core/... ./ensemble/... ./retrace/...
git add core/httpguard ensemble/server retrace/serve retrace/cmd/retrace
git commit -m "feat(retrace): review queue and retrace serve REST verbs behind the shared guard"
```

---

### Task 15: `dashboard/retrace-ui` — the keyboard-driven review screen

**Files:**
- Create: `dashboard/retrace-ui/{package.json,vite.config.ts,tsconfig.json,index.html}`
- Create: `dashboard/retrace-ui/src/{main.tsx,App.tsx,App.css}`
- Create: `dashboard/retrace-ui/src/api/{types.ts,client.ts}`
- Create: `dashboard/retrace-ui/src/{urlState.ts,keys.ts,keys.test.ts,score.ts,score.test.ts}`
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
  export interface CheckpointVerdict { name: string; verdict: 'ok'|'changed'|'missing'|'added'|'unreadable';
    diffPct: number; diffPctFine: number; numDiff: number; mismatch: boolean; images: { a: string; b: string; diff: string; overlay: string } }
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
  export interface Summary { schema: string; app: string; flow: string; verdict: 'pass'|'changed'|'failed';
    checkpoints: CheckpointVerdict[]; wire: { paired: Entry[]; missing: Call[]; extra: Call[] };
    sections: Section[]; hops: HopDiff; unexpectedStatuses: StatusFinding[]; perf: PerfResult;
    conformance: ConformanceFinding[]; capture: { a: CaptureTrust; b: CaptureTrust }; counts: Counts; gates: string[] }
  ```
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
  // score.ts (mirrors serve.ScoreOf — tested against a fixture the Go side also uses)
  export function verdictTone(v: Item['verdict']): 'green' | 'amber' | 'red';
  ```

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

`QueueList.tsx`: worst-first rows (`Badge` tone from `verdictTone`), each
showing app/flow, verdict, the gate count, and a one-line counts strip
(`3 shots · 12 wire · +1 hop`). Passing flows render collapsed under a
"N passing" disclosure — the spec's "passing collapsed". Selection is
keyboard-driven and mirrored into the URL (`?app=&flow=`) via `urlState.ts`
(copy the shipped `ensemble-ui/src/urlState.ts` — same file, same tests).

- [ ] **Step 4: Item screen — shots, wire, hops**

`ShotCompare.tsx`: A/B/overlay with a drag-or-arrow-key slider, and a
`diff` tab. Images come from `api.shotUrl(...)` — never a data URI, the
server already serves PNGs.
Test (`ShotCompare.test.tsx`, happy-dom):
`TestSliderPositionIsClampedToZeroThroughOneHundred`,
`TestOverlayToggleSwapsTheRenderedImageSource`.

`WireDiffTable.tsx`: one collapsible section per flow part (from
`summary.sections`), rows carrying `classes` as CSS class names
(`changed|moved|new|missing|identical`), field rows beneath an expanded row.
A `truncated` entry renders "body was size-capped at capture — not field
diffed" instead of an empty diff. Redaction markers (`[redacted]`, and
`$enc:v1:` once Phase 4b lands) render with the `.redacted` class and a
title tooltip; **no reveal control in this task**.
Test: `TestAToleratedFieldRendersItsMatcherInsteadOfCountingAsAChange`,
`TestATruncatedBodyExplainsItselfRatherThanRenderingEmpty`.

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
pnpm -r test && go test -race ./core/... ./ensemble/... ./retrace/...
git add dashboard/retrace-ui retrace/serve/ui .gitignore
git commit -m "feat(dashboard): retrace review UI — worst-first queue, keyboard item screen, three verbs"
```

---

### Task 16: `retrace export` — the static report

**Files:**
- Create: `retrace/serve/export.go`, `retrace/serve/export_test.go`
- Create: `retrace/cmd/retrace/cmd_export.go`
- Modify: `retrace/cmd/retrace/main.go`

**Interfaces:**
- Produces:
  ```go
  package serve
  type ExportOptions struct { Deps Deps; OutDir string; App, Flow string } // empty App/Flow = everything
  type ExportResult struct { Dir string; Files []string; Items int }
  func Export(o ExportOptions) (ExportResult, error)
  ```

**Shape of the output** — a directory that opens with `file://` and needs no
server, because the person receiving it is reading a CI artifact:
```
<out>/index.html                 queue overview, worst first
<out>/<app>__<flow>/index.html   the item screen, static
<out>/<app>__<flow>/shots/{a,b,diff,overlay}/<checkpoint>.png
<out>/<app>__<flow>/summary.json the exact diff.Summary the UI consumed
```

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
  template, and a tiny inlined `<style>` block. No JavaScript at all except
  a ~15-line inline overlay toggle, written as a `<details>`/`<input
  type=range>` pair where possible so it degrades without script.

- [ ] **Step 4: `retrace export` CLI** — `--out` (required), `--app`,
  `--flow`, `--json` (prints the `ExportResult`). Exit code mirrors the
  worst verdict exported, so `retrace export` can be the only step in a CI
  job.

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
- Modify: nothing in Go. **All heavy lifting stays in the binary** — the
  adapters only mark flow parts and capture screenshots.

**Interfaces:**
- Consumes: the env handshake from Task 4 — `RETRACE_RUN_DIR`,
  `RETRACE_PROXY_URL`, `RETRACE_MARKER_URL`, `RETRACE_STRICT`.
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
it('records the viewport as device.json so the run knows what it captured on', async () => {});
it('is a no-op outside a run when strict is off', async () => {});
it('throws the handshake message in strict mode', async () => {});
```
`@playwright/test` is a **peerDependency**, not a dependency — the consumer
owns their Playwright version.

Note: `trim` (uniform-border cropping) is **NOT** implemented in the
adapter, unlike flowlens. The Go binary owns pixel work
(`pixel.TrimUniformBorder`), and duplicating it in TS would be a second
implementation to keep in sync. The fixture's `trim: true` writes a
`<name>.trim` marker file next to the shot; `capture.Checkpoints()` reads it
and trims on the Go side. Add the matching Go test in Task 4's file:
`TestACheckpointMarkedTrimIsCroppedOnRead`. *(Implementers: this is a
deliberate deviation from the ported file — do not "restore" the JS trim.)*

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

- [ ] **Step 4: Workspace wiring**

`pnpm-workspace.yaml` already globs `adapters/*` — no change needed. Verify:
`pnpm install && pnpm -r test` picks up all three packages.

- [ ] **Step 5: End-to-end proof**

`retrace/cmd/retrace/cmd_run_adapters_test.go`:
`TestARunWithMarkersAndAScreenshotProducesGroupsAndCheckpoints` — the test
command is a small Node script (skipped with `t.Skip` when `node` is not on
PATH) that imports the built `adapters/js` and calls `group`, `endGroup`,
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

## Wrap-up: update the roadmap

- [ ] Tick boxes 4.1–4.7 in
  `openspec/changes/init-ensemble-retrace/tasks.md`, leaving 4.8 unticked
  with a pointer to the part-2 plan.
- [ ] Add a one-line note under 4.4 recording that **a11y-tree diff is
  deferred** (spec keeps it "flagged experimental until device-verified";
  no task here implements it).
- [ ] Record the `RETRACE_MARKER_URL` handshake extension in the
  `adapters` spec, or raise it with the spec owner if the two-variable list
  is meant to be exhaustive.

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
| diff-review · Machine-readable everything (`--json`, stable shapes, exit codes) | 10 |
| diff-review · Review queue (worst-first, keyboard, three verbs, same over REST) | 13, 14 |
| diff-review · Auxiliary checks (≥400 + expectedStatuses, OpenAPI, perf) | 9 |
| diff-review · Auxiliary checks (a11y-tree diff) | **Deferred** — see wrap-up; spec itself flags it experimental |
| adapters · Thin in-test adapters (playwright, maestro, js) | 16 |
| adapters · Env-based handshake + loud strict failure | 4, 16 |
| core-trace-model · Unified hop schema used identically by both | 1, 4, 5 (`wire.jsonl`/`hops.jsonl` ARE `trace.Hop`) |
| core-trace-model · W3C propagation, baggage join key | reused from shipped `core/proxy` + `core/trace`; asserted end-to-end in 5 |
| core-trace-model · Relay collapse | already shipped (`core/trace/collapse.go`) |
| core-trace-model · Redaction at capture | 4, 5 (every disk write goes through `trace.Redactor`) |
| core-trace-model · Per-key redaction modes | **Part 2** |
| core-trace-model · Export formats (HAR/curl/raw) | already shipped (`core/trace/export.go`); the review UI links to ensemble's export endpoints rather than duplicating them |

Two gaps found and fixed inline while reviewing: (a) the a11y requirement
had no task and was silently missing — it now has an explicit deferral in
the wrap-up and in the porting inventory's "NOT ported" list; (b) the
"banner non-ok verdicts on all report surfaces" requirement was only in
Task 6 — assertions were added to Tasks 10, 13 and 16.

**2. Placeholder scan.** Searched for TBD / TODO / "implement later" /
"add appropriate error handling" / "similar to Task N" / "write tests for
the above": none present. Three places name a file to port rather than
inlining 200 lines of it (`wire.mjs`'s walker, `capture-health.mjs`'s reason
list, `reference.mjs`'s eligibility ladder) — each names the exact source
path, the exact function, and the exact behaviours its tests must pin, which
is the porting equivalent of showing the code. The one genuinely open
question — whether `RETRACE_MARKER_URL` is acceptable — is called out as a
question for the spec owner rather than left as a silent choice.

**3. Type consistency.** Checked every name used across task boundaries:
`runs.Paths`, `runs.Manifest`, `runs.CaptureTrust`, `runs.Group`,
`runs.ReadHops`, `capture.Session`, `capture.Options`, `capture.Assess`,
`capture.Fatal`, `rules.Matcher`, `rules.Resolved.ForField`,
`config.Config.NormalizePath`, `config.AppendWireRule`, `pixel.Compare`,
`pixel.ApplyMasks`, `pixel.Rect`, `diff.Options`, `diff.Entry`,
`diff.Summary`, `diff.ExitCode`, `refs.Resolve`, `refs.Accept`,
`replay.Bundle.Match`, `serve.Item`, `serve.ScoreOf`, `httpguard.Handler`.
Three inconsistencies were found and fixed inline:
- `config.Rect` vs `pixel.Rect` — the config package's `Rect` is the YAML
  shape; Task 11 and Task 10 both convert explicitly (`AcceptOptions.Masks
  []pixel.Rect`), and `config.MasksFor` returns `[]Rect` that callers map.
  Both are declared, so no task references an undefined type.
- `diff.Counts` was referenced by `serve.Item` before being defined —
  it is defined in Task 10, which precedes Task 13. Correct order.
- **An import cycle.** The first draft put `Deviation` in `refs` while
  `diff.Options` consumed it and `refs.RejectOptions` carried a
  `*diff.Summary` — `diff → refs → diff`. Fixed by moving the deviations
  ledger into `retrace/diff/deviations.go`: `refs → diff` is now the only
  direction, and Task 11 creates the file there. Task 8's Interfaces block
  declares `Deviations []Deviation` as inert until Task 11 turns it on, so
  Task 8 still compiles standalone.

Dependency direction after the fix, verified acyclic:
`runs → trace` · `rules → ∅` · `config → rules` · `capture → runs, proxy,
trace` · `pixel → ∅` · `diff → runs, rules, config, pixel, trace` ·
`refs → diff, runs, pixel` · `replay → runs, rules, diff, trace` ·
`serve → diff, refs, runs, config, httpguard` · `cmd/retrace → all`.
