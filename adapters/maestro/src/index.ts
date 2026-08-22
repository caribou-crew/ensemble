import { MISSING_HANDSHAKE_MESSAGE, handshake, validateName } from '@caribou-crew/retrace-js';

export interface MarkerRequest {
  url: string;
  body: string;
}

interface ParsedArgs {
  end: boolean;
  name?: string;
}

// parseArgv understands exactly the two Maestro forms this package
// documents:
//   retrace-maestro group <name>   → a "start" marker
//   retrace-maestro group --end    → an "end" marker
function parseArgv(argv: string[]): ParsedArgs {
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

// markerRequest turns Maestro's argv + env into the single HTTP POST that
// records a flow-part marker — POST $RETRACE_MARKER_URL/group {"name":...}
// for a start, POST $RETRACE_MARKER_URL/group/end {} for an end. Pure and
// side-effect free, so it is unit-testable without a network.
//
// It is deliberately duplicated (not imported) by bin/retrace-maestro.mjs:
// that file is plain, unbuilt JavaScript on purpose (Task 17 Step 3) so a
// Maestro `runScript` never depends on this package's own `tsc` build
// having run. Keep the two in sync if this logic changes.
//
// Uses handshake() (not requireHandshake()): this adapter has exactly one
// resource it can use — RETRACE_MARKER_URL, there is no file-writing path
// for Maestro — so it checks that field specifically rather than the
// combined runDir-or-markerUrl check requireHandshake() makes, the same way
// @caribou-crew/retrace-playwright's checkpoint() does for RETRACE_RUN_DIR.
export function markerRequest(argv: string[], env: NodeJS.ProcessEnv): MarkerRequest | null {
  const parsed = parseArgv(argv);
  if (!parsed.end) {
    // R-AE: the same rule applies to a group name arriving via the CLI as
    // to one arriving over HTTP or into groups.jsonl — throw, don't let a
    // bad name become a request nobody asked for.
    validateName('group', parsed.name!);
  }

  const h = handshake(env);
  if (!h.markerUrl) {
    if (h.strict) throw new Error(MISSING_HANDSHAKE_MESSAGE);
    return null; // silent no-op outside a run, strict is off
  }

  return parsed.end
    ? { url: h.markerUrl + '/group/end', body: '{}' }
    : { url: h.markerUrl + '/group', body: JSON.stringify({ name: parsed.name }) };
}
