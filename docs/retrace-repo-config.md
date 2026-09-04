# Standalone repo dashboard: `retrace.repo.yaml` + `retrace serve --watch`

`retrace serve` already aggregates every app it finds under one
`.retrace/runs/` tree into one review queue. `retrace.repo.yaml` extends
that to a repo whose apps' runs live under more than one directory — a web
app at the repo root and several mobile apps under a shared subdirectory,
say — so `retrace serve`, run from anywhere inside the repo, shows every
app in one dashboard. `--watch` then keeps that dashboard's GitHub Actions
data current on its own, with no manual `retrace sync` call.

Both are opt-in and purely additive: a repo that never adds a
`retrace.repo.yaml` sees `retrace serve` behave exactly as it always has —
one dashboard for the current directory's own `.retrace/runs/`.

## The file

`retrace.repo.yaml`, committed at (or near) the repo root:

```yaml
repo: acme/sample-app             # optional; org/repo for --watch
apps:
  web:               { root: . }
  mobile-ios:        { root: apps/mobile }
  mobile-android:    { root: apps/mobile }
sync:                              # optional; defaults for --watch
  workflows: ["Retrace *"]
  branch: main
  branches: ["main", "e2e/*"]     # optional; dashboard's branch-picker allowlist
  since: 24h
```

- `apps` maps an app key — the same name recorded under
  `.retrace/runs/<app>/` and `.retrace-ref/<app>/` — to the directory
  holding that app's own `retrace.yaml` and `.retrace/` tree. `root` is
  relative to `retrace.repo.yaml`'s own location (or absolute); no run
  data moves anywhere. Two or more app keys naming the same root is the
  common case (apps already colocated under one `.retrace/runs/` tree, as
  `mobile-ios`/`mobile-android` are above), not a special case.
- `repo` is optional: only `--watch` needs it, and `--repo ORG/REPO` on
  the command line overrides it.
- `sync` supplies `--watch`'s defaults — `workflows`, `branch`, `actor`,
  `event`, `status`, `since` — the same filters `retrace sync` itself
  takes as flags. A CLI flag always wins over the matching `sync:` key
  when both are set.
- `sync.branches` is separate from `sync.branch`: `branch` is the single
  exact-match default `--watch` and the dashboard's plain "pull latest"
  button use; `branches` is a glob allowlist (`path.Match` — an exact name
  with no glob metacharacter matches only itself, e.g. `"main"`, or a
  pattern like `"e2e/*"`) naming which branches the dashboard's "Choose
  source" picker offers as alternatives. Omitted or empty: the picker
  offers every branch it discovers, unfiltered.

A root that does not exist as a directory, or an empty `apps:`, is
refused at startup, naming the offending app key — the same "fail at
load, not at first use" posture `retrace.yaml` itself already has.

## Discovery

`retrace serve` looks for `retrace.repo.yaml` in the current directory,
then each parent in turn, stopping at the first `retrace.repo.yaml`
found, or at a directory containing `.git` (inclusive), or the filesystem
root — whichever comes first. This is the same rule a developer already
expects from `.git` discovery itself, so `retrace serve` run from any
subdirectory of the repo finds the same file and aggregates the same
apps.

No `retrace.repo.yaml` found anywhere above the current directory:
`retrace serve` behaves exactly as it always has, serving only the
current directory's own `.retrace/runs/`.

## `retrace serve --watch`

```sh
retrace serve --watch [--interval 5m] [--repo ORG/REPO] \
  [--workflow NAME] [--workflows PATTERN,PATTERN] \
  [--branch NAME] [--actor USER] [--event EVENT] [--status STATUS] \
  [--since 7d]
```

Runs one `retrace sync`-equivalent pass immediately (so a fresh `retrace
serve --watch` shows CI data without waiting a full interval), then again
every `--interval` (default `5m`), until the server stops. With a
`retrace.repo.yaml` present, this runs once per mapped root, each pass
scoped to only that root's own app keys — a `web` app's run is never
merged into the `mobile-ios`/`mobile-android` root's `.retrace/runs/`
tree, even though both roots may sync from the same repository. With no
`retrace.repo.yaml`, it runs once, for the current directory, exactly
like a manual `retrace sync` would.

`--watch` requires a repo: either `retrace.repo.yaml`'s own `repo:` key,
or `--repo ORG/REPO` on the command line. Auth is whatever the `gh` CLI
itself resolves — see `docs/retrace-ci-sync.md` for the same auth story
`retrace sync` already has.

A sync failure on any one tick (a `gh` auth error, a rate limit, a
malformed run) is written to stderr and never stops the server or the
ticker — the dashboard a developer is actively looking at keeps serving
whatever it already has, and the next tick tries again.

## Relation to ensemble's `retrace:` aggregator

Ensemble's `retrace:`/`Apps` block (`docs/retrace-ci-sync.md`'s "Multiple
apps, one dashboard" section) is a **machine-global, cross-repo**
aggregator: one `ensemble.yaml`, one app-key namespace, spanning however
many client repos and stacks a machine runs. `retrace.repo.yaml` is a
**repo-local, single-repo** view: each repo owns its own app map, so two
repos that happen to share an app key never collide, and viewing one
repo's dashboard never depends on ensemble running at all.

The two are independent and can both apply to the same repo — a
`retrace.repo.yaml` for `retrace serve --watch` run standalone inside the
repo, and an `Apps` entry in some other machine's `ensemble.yaml` for the
"every stack at once" view. Neither replaces the other.
