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
