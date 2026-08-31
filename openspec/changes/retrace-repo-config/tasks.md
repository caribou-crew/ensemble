## 1. `retrace.repo.yaml` type, loader, and upward discovery

- [x] 1.1 Add a new package `retrace/repoconfig` (mirrors `retrace/config`'s
      layout): `type Config struct { Repo string; Apps map[string]AppEntry;
      Sync SyncDefaults }`, `type AppEntry struct { Root string }`, `type
      SyncDefaults struct { Workflows []string; Branch, Actor, Event,
      Status, Since string }`. Verify with a unit test that a minimal YAML
      (one `repo:`, one `apps:` entry) round-trips through `yaml.Decode`
      with `KnownFields(true)`, matching `retrace/config.Load`'s own
      strictness (a typo'd key is an error, not silently dropped).
- [x] 1.2 Add `Load(path string) (*Config, error)`: resolves every
      `apps.<key>.root` relative to `filepath.Dir(path)` (absolute roots
      pass through unchanged), and fails if `apps:` is empty/absent or if
      any resolved root does not exist as a directory — naming the
      offending app key and root in the error. Verify with tests for: a
      valid multi-app, multi-root file; an empty `apps:` map (error); a
      root that doesn't exist on disk (error naming the app key).
- [x] 1.3 Add `Discover(startDir string) (*Config, string, error)`
      (returns the loaded config and the directory containing it, or
      `(nil, "", nil)` when none is found — the same "absent is not an
      error" convention `config.Discover` uses for a missing
      `retrace.yaml`): search `startDir`, then each parent, stopping at
      the first `retrace.repo.yaml` found or at a directory containing
      `.git` (inclusive) or the filesystem root, whichever comes first.
      Verify with tests: found from the directory containing it; found
      from a nested subdirectory several levels down; not found above a
      directory with no `retrace.repo.yaml` anywhere upward; a `.git`
      directory or filesystem root stops the search without erroring.
- [x] 1.4 Add a `Roots() []string` and `RootFor(app string) (string,
      bool)` convenience method on `*Config` (sorted, de-duplicated root
      list; single-root lookup by app key) for the aggregation code in
      Task 2 to consume without re-deriving the map. Verify with a unit
      test against the two-root, seven-app-key example from design.md.

## 2. `retrace/serve` multi-root aggregation

- [x] 2.1 Add a `Deps` builder helper, `NewDepsForRoot(root string) (Deps,
      error)`, factoring out the `config.Discover(cwd)` + `Deps{Cwd, Cfg,
      ...}` construction `cmd_serve.go` already does inline, so Task 4 can
      build one `Deps` per repo-config root without duplicating it.
      Verify: existing `retrace serve` behavior (single root, no repo
      config) is unchanged — run the existing `cmd_serve_test.go` suite
      and confirm it still passes unmodified.
- [x] 2.2 Add a `Sources` type wrapping one `Deps` per distinct root plus
      the app→root map from `repoconfig.Config`, with `BuildQueue(Sources)
      ([]Item, error)` that calls the existing (unmodified) per-root
      `BuildQueue` once per source, concatenates the results, and re-sorts
      with the existing `ScoreOf`-based comparator (extract the
      comparator from `BuildQueue`'s `sort.SliceStable` call into a shared
      function both call, rather than duplicating the sort). Verify with a
      test: two roots, one app each, that an item from each root appears
      in one combined worst-first list, ties broken by app/flow name
      exactly as single-root `BuildQueue` already does.
- [x] 2.3 Add `Sources.DepsFor(app string) (Deps, bool)` resolving an app
      key to its owning root's `Deps`. Update `server` (`server.go`) to
      hold an optional `Sources` alongside its existing single `Deps`; when
      `Sources` is set, `flowFrom` (routes.go) and `handleQueue` resolve
      through it instead of `s.deps()`; when unset, every handler behaves
      exactly as today (this is the compatibility path for "no repo
      config found" and for every existing single-root caller, including
      `ensemble/server`). Verify with a test hitting
      `GET /api/queue/{app}/{flow}` for an app mapped to a NON-default
      root and confirming the response reflects that root's own
      `retrace.yaml`/runs, not the root the server happened to start in.
- [x] 2.4 Update `POST .../rule` and `POST .../redact` (`handleRule`,
      `handleRedact`) and `reloadConfig` to write through and reload the
      resolved app's own root's config when `Sources` is active, not the
      server's default `Deps.Cwd`. Verify with a test: appending a rule
      for an app in a non-default root updates that root's own
      `.retrace/wire-rules.json`, and a subsequent queue read for that
      app reflects the new rule.
- [x] 2.5 Update `GET /api/queue` (`handleQueue`/`WriteQueue`) to build
      from `Sources` when active. Verify with an integration test
      matching design.md's running example: a repo config mapping one app
      to root `.` and several apps to a second root, each root with its
      own recorded runs, `GET /api/queue` from a server started at either
      root returns items for every app across both roots.

## 3. `retrace/sync` app allowlist

- [x] 3.1 Add `Options.Apps []string` (empty means no filter — the
      existing behavior for every current caller). In `github.go`'s merge
      step, skip (and record in `Result.Skipped`, with a reason distinct
      from the existing malformed-artifact reason, e.g. `"<app>: not in
      this sync's app allowlist"`) any `<app>/<flow>/<run-id>/` directory
      whose `<app>` is not in a non-empty `Options.Apps`. Verify with
      tests: an allowlist admitting one of two apps in a downloaded
      artifact merges only that app and reports the other as skipped; no
      allowlist set merges both, byte-identical to the existing
      `retrace-ci-sync` test suite's assertions.
- [x] 3.2 Run the existing `retrace/sync` test suite unmodified and
      confirm it still passes — `Options.Apps`'s zero value must not
      change any existing test's outcome.

## 4. `retrace serve --watch` and repo-config wiring in the CLI

- [x] 4.1 In `cmd_serve.go`, after resolving `cwd`, call
      `repoconfig.Discover(cwd)`. When a config is found, build one
      `serve.Deps` per root (Task 2.1's helper) and a `serve.Sources`
      (Task 2.2/2.3), and pass it to a new `serve.NewWithSources(Sources)`
      (or extend `serve.New`'s `Deps` parameter to accept an optional
      `Sources` — pick the shape that keeps `serve.New`'s existing
      single-`Deps` signature working for every other caller, including
      `ensemble/server`). When no repo config is found, behavior is
      byte-identical to today. Verify with a CLI-level test: `retrace
      serve` started inside a directory tree with a `retrace.repo.yaml`
      serves all mapped apps; started in a tree with none, serves only the
      one cwd's apps as before.
- [x] 4.2 Add `--watch` (bool) and `--interval` (duration string, default
      `5m`) flags to `cmdServe`. When `--watch` is set, after the listener
      binds (and before blocking on `ctx.Done()`), start a
      `time.Ticker(interval)` goroutine, tied to the same `ctx` the HTTP
      server uses so Ctrl-C stops both. On start and on every tick, call
      `sync.Run` once per Task 2's root (or once for the single cwd root
      when no repo config is active), passing that root's mapped app keys
      as `Options.Apps` (Task 3.1) and the repo config's `Sync` block (or
      CLI-level `--repo`/`--branch`/`--since`/etc. flags, which take
      precedence when both are set) as the other `Options` fields. Verify
      with a test using a fake `gh` on `PATH` (matching
      `retrace-ci-sync`'s existing fixture): starting `retrace serve
      --watch --interval 50ms` against a two-root repo config merges each
      root's own apps only, within one interval, with no manual `sync`
      call.
- [x] 4.3 A sync error on any tick (fake `gh` exits non-zero, or a
      malformed candidate) is written to stderr and does not stop the
      server or the ticker. Verify with a test: one tick's sync fails,
      the HTTP server still answers `GET /api/health` afterward, and a
      later tick with a working `gh` still syncs successfully.
- [x] 4.4 Update `cmdServe`'s usage text (and `--help` output) to document
      `--watch`/`--interval` and repo-config discovery. Verify by reading
      the printed usage in a test (matching `docs_contract_test.go`'s
      existing pattern of asserting on CLI help text, if one exists for
      `serve`).

## 5. Docs

- [x] 5.1 Add `docs/retrace-repo-config.md`: the `retrace.repo.yaml`
      schema (mirroring design.md's example), upward-discovery behavior,
      `retrace serve --watch` usage, and how this relates to (and does
      not replace) ensemble's global `retrace:`/`Apps` aggregator in
      `docs/retrace-ci-sync.md`. Cross-link both docs. Verify by reading
      the rendered file for accuracy against the shipped flag names/
      defaults from Task 4.

## 6. End-to-end verification

- [ ] 6.1 Run the full `retrace` test suite (`go test ./retrace/...`) and
      confirm everything passes, including every existing single-root
      `retrace serve`/`retrace sync` test unmodified by this change.
- [ ] 6.2 Manually verify against a throwaway multi-root fixture (e.g. two
      directories under a scratch repo, each with its own `retrace.yaml`
      and a couple of recorded runs, tied together by a
      `retrace.repo.yaml`): `retrace serve` from either directory shows
      every app in one dashboard, and `retrace serve --watch` picks up a
      new locally-faked "synced" run without a manual `retrace sync`
      call.
