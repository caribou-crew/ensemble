## 1. Config

- [x] 1.1 Add `FreshnessConfig` (`DefaultBranch`, `PollIntervalS`) to
      `ensemble/config/config.go`, optional/pointer on `Config`, with
      `default_branch` defaulting to `"main"` and `poll_interval_s`
      defaulting to `300` when the block is present but a field is omitted.
- [x] 1.2 Add validation in `ensemble/config/validate.go` for the freshness
      block (e.g. `poll_interval_s` must be positive).
- [x] 1.3 Add config tests: block absent, block present with defaults,
      block present with explicit overrides, invalid `poll_interval_s`.

## 2. Orchestrator: eligibility and git primitives

- [x] 2.1 In new `ensemble/orchestrator/freshness.go`, add repo-toplevel
      helpers built on the existing `gitOutput` from `version.go`:
      `isGitRepo(dir)` and `repoToplevel(dir)`.
- [x] 2.2 Add `eligibleForFreshness(serviceDir, configDir string) bool`
      comparing `repoToplevel(serviceDir)` against
      `repoToplevel(configDir)`, per the eligibility requirement.
- [x] 2.3 Add `currentBranch(dir string) string` via
      `git symbolic-ref --short HEAD`, and `behindCount(dir, ref string) int`
      via `git rev-list --count HEAD..<ref>`, both using `gitOutput`'s
      existing timeout/error conventions (empty/zero on failure).
- [x] 2.4 Unit tests for 2.1–2.3 against real temp git repos (mirroring
      `version_test.go`'s approach): eligible vs. same-toplevel-as-config,
      non-repo dir, branch detection, behind-count with 0/N commits.

## 3. Orchestrator: background polling

- [x] 3.1 Define `FreshnessState` struct
      (`Branch`, `BehindBranch`, `BehindDefault`, `DefaultBranch`,
      `CheckedAt`, `Error`, matching the JSON tags in the spec) and add
      `Freshness *FreshnessState` to `ServiceState`.
- [x] 3.2 Implement `checkServiceFreshness(ctx, dir, defaultBranch string) FreshnessState`:
      `git fetch origin` (bounded timeout), then branch + behind-counts on
      success; on fetch failure, return a state carrying only `Error` for
      the caller to merge over the previous state (per the
      "fetch failure preserves last-known state" requirement — merging
      happens in the caller, not this function).
- [x] 3.3 Implement `runFreshnessPoll(ctx)` that fetches all eligible
      services concurrently (semaphore-bounded, default 4) and merges
      results into orchestrator state under its existing lock.
- [x] 3.4 Wire a background goroutine into `Orchestrator.Up`/`Down`
      lifecycle: ticks every `poll_interval_s`, calls `runFreshnessPoll`,
      exits cleanly on `Down`. No-op entirely when `Freshness` config is
      absent.
- [x] 3.5 Add `TriggerFreshnessCheck(ctx)` on `Orchestrator` that runs one
      poll pass immediately (used by the on-demand endpoint), reusing
      `runFreshnessPoll`.
- [x] 3.6 Tests: poll runs on schedule, does not block `Up`/`Down`, stops
      on `Down`, merges failure over prior success without clobbering
      `behindBranch`/`behindDefault`/`checkedAt`, skips ineligible
      services and omits their `Freshness`.

## 4. Server API

- [x] 4.1 Include `Freshness` in the `GET /api/status` service payload
      (already flows through if `ServiceState` marshals it with
      `omitempty`; verify no stub/placeholder is emitted when nil).
- [x] 4.2 Add `POST /api/freshness/check` route in
      `ensemble/server/routes.go` calling
      `Orchestrator.TriggerFreshnessCheck` and returning once complete.
- [x] 4.3 Server tests: status payload includes/omits `freshness`
      correctly; check endpoint triggers an immediate poll and the
      subsequent status reflects it.

## 5. CLI

- [x] 5.1 Confirm `ensemble status --json` already surfaces `freshness`
      via the shared `ServiceState` type (likely no code change beyond a
      test).
- [x] 5.2 Add `ensemble freshness` command (in
      `ensemble/cmd/ensemble/`) printing the table described in the spec,
      reading current orchestrator state via the existing status client
      path — no new fetch triggered.
- [x] 5.3 CLI tests covering table output shape and the
      no-eligible-services case.

## 6. Dashboard

- [x] 6.1 Add `freshness?` field to `ServiceState` in
      `dashboard/ensemble-ui/src/api/types.ts` matching the server shape.
- [x] 6.2 Render freshness badges in
      `dashboard/ensemble-ui/src/views/ServicesView.tsx` per the spec's
      three states (clean / amber behind-badge(s) / grey-unknown), plus a
      tooltip/popover with branch, exact counts, and `checkedAt`.
- [x] 6.3 Add a "check now" control that calls
      `POST /api/freshness/check` and refreshes the view.
- [x] 6.4 Covered by `ServicesView.freshness.test.ts` (component-level,
      mocked API) exercising all three render states plus the "check now"
      control, instead of a manual browser pass — no running stack was
      driven live end-to-end in this session; flagging in case a real
      browser/retrace-iterate check is still wanted before shipping.

## 7. Docs

- [x] 7.1 Document the `freshness:` config block (fields, defaults,
      eligibility rule) in the ensemble config reference.
- [x] 7.2 Document `ensemble freshness` and
      `POST /api/freshness/check` alongside the existing status
      docs.
