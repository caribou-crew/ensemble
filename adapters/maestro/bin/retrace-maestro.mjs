#!/usr/bin/env node
// retrace-maestro — the executable a Maestro `runScript` step calls, e.g.:
//
//   - runScript:
//       file: node_modules/@caribou-crew/retrace-maestro/bin/retrace-maestro.mjs
//       env: { ARGS: "group checkout" }
//
// This IS the single implementation of markerRequest — ../src/index.ts
// re-exports it (see that file) so the copy the unit tests exercise is the
// copy Maestro actually runs, rather than a hand-synced duplicate. It stays
// plain, unbuilt JavaScript (no tsc pass over THIS file) so a Maestro flow
// never depends on `pnpm build` having run for this package; a hand-written
// bin/retrace-maestro.d.mts gives ../src/index.ts (and anything else that
// imports this file) its types.
import { realpathSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { MISSING_HANDSHAKE_MESSAGE, handshake, validateName } from '@caribou-crew/retrace-js';

function parseArgv(argv) {
  if (argv[0] !== 'group') {
    throw new Error(`retrace-maestro: unknown command ${JSON.stringify(argv[0] ?? '')}; expected "group"`);
  }
  if (argv[1] === '--end') return { end: true };
  const name = argv[1];
  if (!name) {
    throw new Error('retrace-maestro: "group" requires a name (e.g. `group checkout`), or `--end`');
  }
  return { end: false, name };
}

// joinMarkerPath avoids the `//group` a trailing slash on RETRACE_MARKER_URL
// would otherwise produce (F-8): markers.go registers bare paths so a
// subtree redirect never silently drops the POST body, and `//group` is not
// a bare path the mux matches.
function joinMarkerPath(markerUrl, path) {
  return markerUrl.replace(/\/+$/, '') + path;
}

export function markerRequest(argv, env) {
  const parsed = parseArgv(argv);
  if (!parsed.end) validateName('group', parsed.name);

  const h = handshake(env);
  if (!h.markerUrl) {
    if (h.strict) throw new Error(MISSING_HANDSHAKE_MESSAGE);
    return null;
  }

  return parsed.end
    ? { url: joinMarkerPath(h.markerUrl, '/group/end'), body: '{}' }
    : { url: joinMarkerPath(h.markerUrl, '/group'), body: JSON.stringify({ name: parsed.name }) };
}

async function main() {
  // Maestro's runScript passes a single env map, not real argv, hence
  // ARGS — but process.argv is honoured too, for `node bin/retrace-maestro.mjs
  // group checkout` direct invocation (and for testing this file itself).
  const direct = process.argv.slice(2);
  const argv = direct.length > 0 ? direct : (process.env.ARGS ?? '').trim().split(/\s+/).filter(Boolean);

  const req = markerRequest(argv, process.env);
  if (!req) return; // no active run, RETRACE_STRICT is not set

  const res = await fetch(req.url, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: req.body,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '<unreadable body>');
    throw new Error(`retrace-maestro: marker POST ${req.url} failed with ${res.status} ${res.statusText}: ${text}`);
  }
}

// Only run when executed directly, never when merely imported (which is
// exactly what ../src/index.ts's re-export does, and what
// src/index.test.ts's `markerRequest` tests rely on — importing this module
// must not itself POST anything).
//
// The comparison is realpath-to-realpath on both sides, not a raw string
// compare: Node resolves import.meta.url to the entry file's REAL path
// (dereferencing symlinks), but leaves process.argv[1] exactly as the
// caller typed it. npm's own "bin" mechanism (this package's package.json)
// materialises a SYMLINK at node_modules/.bin/retrace-maestro — the path
// every real installation invokes this file through — so a naive
// `import.meta.url === pathToFileURL(process.argv[1]).href` still fails on
// that symlink (verified: it also fails on a plain, non-symlinked /tmp path
// on macOS, where /tmp itself is a symlink to /private/tmp). realpathSync on
// process.argv[1] is what makes both sides agree; see Node's own "is this
// the entry module" ESM recipe. bin.test.ts pins the symlinked case.
if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  main().catch((err) => {
    console.error(err instanceof Error ? err.message : String(err));
    process.exitCode = 1;
  });
}
