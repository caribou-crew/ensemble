---
name: release
description: Use when the user asks to cut/ship a release of ensemble or retrace — commits pending work, pushes main, tags the next version, waits for goreleaser to build and draft the GitHub release, then dispatches the npm publish workflow. Covers both binaries at once; there is one shared version number across the monorepo.
---

# Cutting a release

This repo's versioning is **git-tag-driven only**. No `package.json` in the
repo carries a real version to bump by hand:

- Root `package.json` is `"private": true` and stays `0.0.0` forever.
- Every `npm/<pkg>/package.json` version is rewritten transiently, in CI, by
  `scripts/prepare-npm-binary.mjs` from the tag's version — and never
  committed back. Check `git log -- npm/*/package.json`: across 8+ releases
  none of them ever changed a version field by commit.

So there is nothing to bump. If you're tempted to edit a `package.json`
version as part of a release, don't — it's a no-op that CI overwrites, and
doing it would be the first time this repo's history has ever done that.

A single tag versions **both** `ensemble` and `retrace` binaries together —
goreleaser builds both from one `v*` tag. There's no way to release just one.

## The flow

```
commit → push main → tag vX.Y.Z → push tag (triggers `release` workflow)
  → wait for goreleaser to build + draft the GitHub release
  → dispatch `publish` workflow with that version
  → wait for it to publish the GitHub release + all 10 npm packages
```

### 1. Confirm there's something to release, and commit it

```sh
git status --porcelain
```

If there are uncommitted changes the user wants in this release, commit them
(only if the user has asked you to — see the repo-wide "only commit when
explicitly asked" rule). Otherwise skip straight to step 2 using whatever is
already on `main`.

### 2. Pick the version

```sh
git tag --sort=-v:refname | head -1
```

The next version is that tag's patch bumped by one, unless the user named a
specific version. State the version you're about to tag before tagging it —
pushing a tag triggers a real CI build and (after step 5) a real npm publish
that cannot be unpublished from most registries within the window, so this is
not a step to guess silently through.

### 3. Push main, then tag and push the tag

```sh
git push origin main
git tag vX.Y.Z
git push origin vX.Y.Z
```

Pushing the tag triggers `.github/workflows/release.yml` — goreleaser builds
`ensemble` and `retrace` for darwin/linux/windows × amd64/arm64, and drafts
(not publishes) a GitHub release. Nothing public happens yet.

### 4. Wait for the `release` workflow to finish

```sh
gh run list --workflow=release.yml --limit 3
```

Poll (or use `gh run watch <run-id>`) until the run for your tag shows
`completed success`. It normally takes ~2-3 minutes. Do not proceed to step 5
on a `failure` or `in_progress` run — diagnose first (`gh run view <id> --log-failed`).

### 5. Dispatch the `publish` workflow

This is the step that makes anything public — it un-drafts the GitHub release
and publishes all 10 npm packages (`@caribou-crew/ensemble` +
`@caribou-crew/retrace`, each with 4 platform packages) via npm trusted
publishing (no stored token; OIDC-based).

```sh
gh workflow run publish.yml -f version=X.Y.Z
```

(No `v` prefix on the input — it's added internally as `v${{ inputs.version }}`.)

### 6. Wait for `publish` to finish, then verify

```sh
gh run list --workflow=publish.yml --limit 3
npm view @caribou-crew/ensemble version
npm view @caribou-crew/retrace version
```

Both `npm view` calls should print the version you just released. The publish
job's npm-publish loop is idempotent (skips a package/version already on the
registry), so re-running it after a partial failure is safe.

## If something fails partway

- `release` workflow failed: nothing was published (draft release, if any,
  is not public). Fix the issue, delete the tag (`git push --delete origin
  vX.Y.Z && git tag -d vX.Y.Z`) if goreleaser partially created release
  assets you need to redo cleanly, then retry from step 3. Confirm with the
  user before deleting a pushed tag — that's a shared-state, hard-to-reverse
  action.
- `publish` workflow failed partway through the npm loop: safe to just
  re-dispatch it (step 5) — already-published packages are skipped, not
  re-published.
- A brand-new npm package (one that's never been published before) needs a
  **one-time manual first publish + trusted-publisher setup** before this
  workflow can touch it — see the comment block above the "Pack npm
  packages" step in `.github/workflows/publish.yml`. This only applies the
  first time a new package name is introduced, not to routine releases.
