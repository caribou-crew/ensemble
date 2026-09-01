#!/usr/bin/env node
// Resolves and execs the prebuilt retrace binary for the current
// platform/arch, installed as one of the optionalDependencies below. No
// postinstall download — the binary ships inside that package's own
// published tarball (see scripts/prepare-npm-binary.mjs).
import { createRequire } from 'node:module';
import { spawnSync } from 'node:child_process';

const require = createRequire(import.meta.url);

const PLATFORM_PACKAGES = {
  'darwin-arm64': '@caribou-crew/retrace-darwin-arm64',
  'darwin-x64': '@caribou-crew/retrace-darwin-x64',
  'linux-arm64': '@caribou-crew/retrace-linux-arm64',
  'linux-x64': '@caribou-crew/retrace-linux-x64',
  'win32-arm64': '@caribou-crew/retrace-win32-arm64',
  'win32-x64': '@caribou-crew/retrace-win32-x64',
};

const key = `${process.platform}-${process.arch}`;
const pkg = PLATFORM_PACKAGES[key];
// The windows packages ship bin/retrace.exe — the suffix goreleaser gives
// the binary and the one Windows needs to exec it (see
// scripts/prepare-npm-binary.mjs).
const binFile = process.platform === 'win32' ? 'retrace.exe' : 'retrace';

if (!pkg) {
  console.error(
    `@caribou-crew/retrace has no prebuilt binary for ${key}.\n` +
      `Supported platforms: ${Object.keys(PLATFORM_PACKAGES).join(', ')}.\n` +
      `Build retrace from source instead: https://github.com/caribou-crew/ensemble/tree/main/retrace`,
  );
  process.exit(1);
}

let binPath;
try {
  binPath = require.resolve(`${pkg}/bin/${binFile}`);
} catch {
  console.error(
    `@caribou-crew/retrace: the optional dependency "${pkg}" is not installed.\n` +
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
