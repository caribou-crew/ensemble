#!/usr/bin/env node
// Resolves and execs the prebuilt ensemble binary for the current
// platform/arch, installed as one of the optionalDependencies below. No
// postinstall download — the binary ships inside that package's own
// published tarball (see scripts/prepare-npm-binary.mjs). Mirrors
// @caribou-crew/retrace's bin/retrace.mjs exactly: each published package
// must be a self-contained tarball, so this can't import a shared module
// from a sibling package — the platform-name table is the only part that
// differs between the two.
import { createRequire } from 'node:module';
import { spawnSync } from 'node:child_process';

const require = createRequire(import.meta.url);

const PLATFORM_PACKAGES = {
  'darwin-arm64': '@caribou-crew/ensemble-darwin-arm64',
  'darwin-x64': '@caribou-crew/ensemble-darwin-x64',
  'linux-arm64': '@caribou-crew/ensemble-linux-arm64',
  'linux-x64': '@caribou-crew/ensemble-linux-x64',
};

const key = `${process.platform}-${process.arch}`;
const pkg = PLATFORM_PACKAGES[key];

if (!pkg) {
  console.error(
    `@caribou-crew/ensemble has no prebuilt binary for ${key}.\n` +
      `Supported platforms: ${Object.keys(PLATFORM_PACKAGES).join(', ')}.\n` +
      `Build ensemble from source instead: https://github.com/caribou-crew/ensemble/tree/main/ensemble`,
  );
  process.exit(1);
}

let binPath;
try {
  binPath = require.resolve(`${pkg}/bin/ensemble`);
} catch {
  console.error(
    `@caribou-crew/ensemble: the optional dependency "${pkg}" is not installed.\n` +
      `This usually means npm skipped optional deps (--no-optional / --omit=optional), ` +
      `or your platform wasn't in that package's "os"/"cpu" fields.`,
  );
  process.exit(1);
}

const result = spawnSync(binPath, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
