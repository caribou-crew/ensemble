#!/usr/bin/env node
// Syncs adapters/js and adapters/playwright's package.json versions to the
// release tag, and rewrites retrace-playwright's "workspace:*" dependency on
// retrace-js to that same exact version — the same rewrite
// prepare-npm-binary.mjs does for the binary packages' optionalDependencies,
// for the same reason: plain `npm publish` (unlike `pnpm publish`) has no
// workspace protocol to resolve. Run from the repo root, after `pnpm install`
// and before building either package.
//
// Expects RELEASE_VERSION (the tag without its leading "v", e.g. "0.0.4") —
// see .github/workflows/publish.yml for where that comes from.
import { readFileSync, writeFileSync } from 'node:fs';

const version = process.env.RELEASE_VERSION;
if (!version) {
  console.error('RELEASE_VERSION env var is required (e.g. "0.0.4")');
  process.exit(1);
}

function bumpVersion(pkgPath, { rewriteDeps = [] } = {}) {
  const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'));
  pkg.version = version;
  for (const dep of rewriteDeps) {
    if (pkg.dependencies?.[dep]) {
      pkg.dependencies[dep] = version;
    }
  }
  writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + '\n');
}

bumpVersion('adapters/js/package.json');
bumpVersion('adapters/playwright/package.json', { rewriteDeps: ['@caribou-crew/retrace-js'] });
console.log(`synced adapters/js and adapters/playwright to version ${version}`);
