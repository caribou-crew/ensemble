# Syncing CI retrace results locally

`retrace sync` pulls the run directories a CI workflow recorded (see
`retrace-ci-example.yml` in this directory) into your local
`.retrace/runs/` tree, next to whatever you've recorded locally. Once
synced, they're indistinguishable from a local run to every existing tool
— `retrace serve`, `retrace diff`, `retrace export`, and the ensemble
dashboard's Retrace tab all just see another run to diff against your
accepted reference.

## Usage

```sh
retrace sync --from github --repo <org>/<repo> [--workflow <name>] [--since 7d]
```

- `--repo` is required: the GitHub repository the workflow runs live in.
- `--workflow` narrows sync to one workflow name; omit it to pull every
  workflow's artifacts.
- `--since` bounds how far back to look (default `7d` — GitHub's default
  artifact retention is 90 days, but a short-lived `retention-days: 7` on
  the upload step, as in the example workflow, means anything older is
  already gone).
- Add `--json` for the machine-readable `{"synced": [...], "skipped":
  [...]}` result.

Auth is whatever the `gh` CLI itself resolves — `gh auth login`, or the
`GH_TOKEN`/`GITHUB_TOKEN` environment variables `gh` reads directly. This
command never handles a token itself; `gh` needs to be installed and
authenticated on the machine running `retrace sync`.

Re-running `retrace sync` is safe and cheap: a run already merged onto
local disk (by run-id directory) is left untouched, not re-downloaded on
top of.

A run with no downloadable artifacts (an unrelated workflow, an older run
whose retention already expired, a run GitHub reports "no valid artifacts
found to download" for) is **skipped with a reason, not fatal** — it shows
up in `Skipped`/`--json`'s `"skipped"` array and sync continues to every
other matching run. Nothing about `--since`, `--workflow`, or any other
filter changes this: a bulk sync across a repo with many unrelated or
artifact-less runs completes and reports what it could and couldn't pull,
rather than aborting on the first one that has nothing to offer.

## Video and test-report evidence

A run directory may also carry a `videos/` subdirectory (one file per
test/checkpoint, `.webm` or `.mp4`) and a `report/` subdirectory (the test
runner's own HTML report — Playwright's `html` reporter output, for
example). `retrace sync` needs no flag for this: it already copies the
*entire* directory a `manifest.json` sits in (see `copyOneRun` in
`retrace/sync/github.go`), so anything placed alongside `manifest.json`,
`wire.jsonl`, and `shots/` rides along automatically.

- **Playwright** projects get this for free by registering
  `@caribou-crew/retrace-playwright/reporter` in `playwright.config.ts` —
  no CI script changes. See `docs/retrace-ci-example.yml`'s `retrace-web`
  job.
- **Maestro** projects add one explicit CI step after `maestro test`
  finishes, using the same `retrace-maestro` bin the `group` marker command
  already uses: `retrace-maestro attach video <path>` / `retrace-maestro
  attach report <path>`. See `docs/retrace-ci-example.yml`'s
  `retrace-ios-maestro` job.

The ensemble dashboard's Retrace tab shows the candidate run's video
(playable inline) and a link to the full report, when present, in the
flow detail pane's "evidence" section.

## The CLI-first agent recipe

The retrace CLI already emits structured JSON an agent can read directly
— there is no separate MCP server in this change, and none is needed for
the common loop:

```sh
retrace diff --flow checkout --app ios --json
```

returns the same `diff.Summary` the review UI renders: verdict, pixel/wire/
hop counts, gates, and (for a wire diff) the exact fields that changed.
An agent walks this loop:

1. `retrace sync --from github --repo <org>/<repo>` (or read the ensemble
   dashboard's Retrace tab) to see what CI flagged.
2. `retrace diff --flow <flow> --app <app> --json` to read the structured
   verdict for the flagged flow.
3. Correlate a wire diff's new/changed field with `ensemble traffic` (or
   the ensemble Traffic tab) to find which upstream service introduced it.
4. Propose either a wire rule (`retrace ref rule --field ... --matcher
   ...`, if the change is intentional and should be tolerated) or a code
   fix (if the app should account for the new behavior).
5. Re-run locally, `retrace diff` again to confirm the verdict, then push.

Nothing above requires a new tool surface — it's the same CLI a human
runs, read as JSON instead of a table.

## Multiple apps, one dashboard

By default the ensemble dashboard diffs every synced app against ONE
retrace.yaml — the one next to `.retrace/` (`retrace.dir` in
`ensemble.yaml`, or the same directory as `ensemble.yaml` itself). That is
correct when every app's runs were recorded against that same file.

When an app's runs come from a DIFFERENT repository's CI (or a monorepo
client living in its own subdirectory with its own retrace.yaml), point
the dashboard at it explicitly:

```yaml
retrace:
  dir: .retrace
  apps:
    mobile-ios: ../mobile-app
    mobile-android: ../mobile-app
```

`apps` maps an app key (the same name under `.retrace/runs/<app>/`) to the
directory holding that app's own retrace.yaml. An app with no entry keeps
using the dashboard-wide default, so this is opt-in per app. Two apps may
point at the same directory — `--app` at record time is what tells two
clients apart, not the config file.

## Resolution-independent masks

`masks:` rects are normally absolute pixels. Add `pct: true` to make
x/y/width/height fractions (0–1) of the checkpoint's own dimensions
instead — the same entry then covers, say, "the top 6% of the screen" on
every device it's applied to, at whatever resolution that device really
shot:

```yaml
flows:
  card-views:
    masks:
      "*":
        - { x: 0, y: 0, width: 1, height: 0.06, pct: true, why: "status bar (clock/battery/wifi)" }
```

This is what makes ONE flow-keyed masks: entry work for two apps recorded
at very different resolutions sharing the same flow name and the same
`apps:` directory above.
