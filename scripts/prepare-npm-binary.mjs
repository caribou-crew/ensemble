#!/usr/bin/env node
// Stages a release binary (ensemble or retrace) for each platform into its
// npm package's bin/ dir and syncs that binary's npm/<name>* package.json
// versions to the release tag, so `npm publish` ships binaries that exactly
// match the GitHub release. This also rewrites npm/<name>'s
// optionalDependencies from "workspace:*" (how pnpm links them locally —
// see pnpm-workspace.yaml) to that same exact version: plain `npm publish`
// has no workspace protocol to resolve, unlike `pnpm publish`, so this
// script does that rewrite instead. Run from the repo root.
//
// One shared script for both binaries — the retrace/ensemble split is data
// (a name, a set of npm/<name>* directories), not logic, so a second
// near-identical script per binary would just be the same bug fixed once
// and not the other.
//
// Expects BINARY ("ensemble" or "retrace"), RELEASE_VERSION (the tag
// without its leading "v", e.g. "0.0.4"), and one already-extracted binary
// per platform at .npm-stage/<platform>/<binary> — see
// .github/workflows/publish.yml for how those get there (downloading and
// untarring the goreleaser archives the tagged release already carries).
import { copyFileSync, chmodSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const binary = process.env.BINARY;
if (binary !== 'ensemble' && binary !== 'retrace') {
  console.error('BINARY env var must be "ensemble" or "retrace"');
  process.exit(1);
}
const version = process.env.RELEASE_VERSION;
if (!version) {
  console.error('RELEASE_VERSION env var is required (e.g. "0.0.4")');
  process.exit(1);
}

const STAGE = '.npm-stage';
const NPM_ROOT = 'npm';
const PLATFORMS = ['darwin-arm64', 'darwin-x64', 'linux-arm64', 'linux-x64'];

function bumpVersion(pkgPath) {
  const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'));
  pkg.version = version;
  if (pkg.optionalDependencies) {
    for (const dep of Object.keys(pkg.optionalDependencies)) {
      pkg.optionalDependencies[dep] = version;
    }
  }
  writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + '\n');
}

for (const platform of PLATFORMS) {
  const src = join(STAGE, platform, binary);
  const dest = join(NPM_ROOT, `${binary}-${platform}`, 'bin', binary);
  copyFileSync(src, dest);
  chmodSync(dest, 0o755);
  bumpVersion(join(NPM_ROOT, `${binary}-${platform}`, 'package.json'));
  console.log(`packed ${binary} ${platform}: ${src} -> ${dest}`);
}

bumpVersion(join(NPM_ROOT, binary, 'package.json'));
console.log(`synced npm/${binary} and platform packages to version ${version}`);
