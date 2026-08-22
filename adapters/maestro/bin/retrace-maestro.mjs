#!/usr/bin/env node
// retrace-maestro — the executable a Maestro `runScript` step calls, e.g.:
//
//   - runScript:
//       file: node_modules/@caribou-crew/retrace-maestro/bin/retrace-maestro.mjs
//       env: { ARGS: "group checkout" }
//
// Deliberately plain, unbuilt JavaScript (no tsc pass over THIS file) so a
// Maestro flow never depends on `pnpm build` having run for this package.
// Its argv/env → HTTP-POST logic mirrors ../src/index.ts's markerRequest —
// duplicated by hand, not imported, and kept in sync deliberately; see that
// file's comment for why.
//
// @caribou-crew/retrace-js IS imported here (unlike the markerRequest
// logic): it is a separate, already-built dependency by the time this
// package is installed from a registry (its dist/ ships with the published
// package), so importing it does not reintroduce the "depends on a build
// having run" problem this file exists to avoid.
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

export function markerRequest(argv, env) {
  const parsed = parseArgv(argv);
  if (!parsed.end) validateName('group', parsed.name);

  const h = handshake(env);
  if (!h.markerUrl) {
    if (h.strict) throw new Error(MISSING_HANDSHAKE_MESSAGE);
    return null;
  }

  return parsed.end
    ? { url: h.markerUrl + '/group/end', body: '{}' }
    : { url: h.markerUrl + '/group', body: JSON.stringify({ name: parsed.name }) };
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

// Only run when executed directly (Maestro's runScript, or a manual `node
// bin/retrace-maestro.mjs`) — never when imported, e.g. by a test that wants
// markerRequest without the network call.
if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((err) => {
    console.error(err instanceof Error ? err.message : String(err));
    process.exitCode = 1;
  });
}
