#!/usr/bin/env node
// Stages the retrace binary for each platform into its npm package's bin/
// dir and syncs every npm/retrace* package.json's version to the release
// tag, so `npm publish` ships binaries that exactly match the GitHub
// release. This also rewrites npm/retrace's optionalDependencies from
// "workspace:*" (how pnpm links them locally — see pnpm-workspace.yaml) to
// that same exact version: plain `npm publish` has no workspace protocol to
// resolve, unlike `pnpm publish`, so this script does that rewrite instead.
// Run from the repo root.
//
// Expects RELEASE_VERSION (the tag without its leading "v", e.g. "0.0.1")
// and one already-extracted retrace binary per platform at
// .npm-stage/<platform>/retrace — see .github/workflows/release.yml's
// npm-publish job for how those get there (downloading and untarring the
// goreleaser archives the same job just published).
import { copyFileSync, chmodSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const version = process.env.RELEASE_VERSION;
if (!version) {
  console.error('RELEASE_VERSION env var is required (e.g. "0.0.1")');
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
  const src = join(STAGE, platform, 'retrace');
  const dest = join(NPM_ROOT, `retrace-${platform}`, 'bin', 'retrace');
  copyFileSync(src, dest);
  chmodSync(dest, 0o755);
  bumpVersion(join(NPM_ROOT, `retrace-${platform}`, 'package.json'));
  console.log(`packed ${platform}: ${src} -> ${dest}`);
}

bumpVersion(join(NPM_ROOT, 'retrace', 'package.json'));
console.log(`synced npm/retrace and platform packages to version ${version}`);
