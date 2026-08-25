// handshake.ts reads the env vars retrace/cmd/retrace/main.go documents
// (RETRACE_RUN_DIR, RETRACE_PROXY_URL, RETRACE_MARKER_URL, RETRACE_UPSTREAM_URL,
// RETRACE_STRICT) and turns them into a typed Handshake. This is the ONLY
// place RETRACE_STRICT is parsed, in any of the three adapter packages — see
// task-17-rulings.md R-AD.

export interface Handshake {
  runDir: string | null;
  proxyUrl: string | null;
  markerUrl: string | null;
  // upstreamUrl is conditional, unlike runDir/proxyUrl/markerUrl: it is only
  // set when the capture session has a real upstream to point at (see
  // design.md §6.1.2). A test fixture for an app with URL-bound auth (DPoP/
  // RFC 9449 and similar) points the app's HTTP transport at proxyUrl but
  // its auth/signing layer at upstreamUrl — a proof minted against the
  // proxy's own address fails validation upstream, and retrace has no
  // private key to re-sign one that was.
  upstreamUrl: string | null;
  strict: boolean;
}

// MISSING_HANDSHAKE_MESSAGE is the spec's "Missing handshake" scenario text
// (spec: "SHALL fail loudly ... with a message explaining how to invoke
// retrace"). It is exported from here ONLY — @caribou-crew/retrace-playwright
// and @caribou-crew/retrace-maestro both import this exact value rather than
// redefining the string, so "all three packages say the same thing" is an
// invariant a diff can enforce, not a convention that can quietly drift.
export const MISSING_HANDSHAKE_MESSAGE =
  'retrace: no active run. This fixture writes checkpoints and flow-part markers into the ' +
  'directory `retrace run` creates, and found neither RETRACE_RUN_DIR nor RETRACE_MARKER_URL ' +
  'in the environment.\n' +
  '  Run your tests through retrace:  retrace run --flow <name> -- <your test command>\n' +
  '  Or unset RETRACE_STRICT to let checkpoints be no-ops outside a run.';

// R-AD's accepted set, case-insensitive. Falling back to "not strict" for a
// value outside this set would mean RETRACE_STRICT=true (a spelling a
// careful user is MORE likely to type than the correct "1") silently turns
// the safety net off — a plausible value worse than an empty one. So
// anything outside both sets throws instead of defaulting either way.
const STRICT_TRUE = new Set(['1', 'true', 'yes', 'on']);
const STRICT_FALSE = new Set(['0', 'false', 'no', 'off', '']);

function parseStrict(raw: string | undefined): boolean {
  if (raw === undefined) return false; // unset = the spec'd default, no-op outside a run
  const v = raw.trim().toLowerCase();
  if (STRICT_TRUE.has(v)) return true;
  if (STRICT_FALSE.has(v)) return false;
  throw new Error(
    `retrace: RETRACE_STRICT=${JSON.stringify(raw)} is not a recognised value. ` +
      `Use one of ${[...STRICT_TRUE].join(', ')} for strict mode, or ` +
      `${[...STRICT_FALSE].filter((s) => s !== '').join(', ')} (or unset) for non-strict.`,
  );
}

// nonEmpty treats an explicitly-empty env var the same as an unset one: a
// path or URL configured to the empty string is not a value, it is the
// absence of one.
function nonEmpty(v: string | undefined): string | null {
  return v && v.length > 0 ? v : null;
}

export function handshake(env: NodeJS.ProcessEnv = process.env): Handshake {
  return {
    runDir: nonEmpty(env.RETRACE_RUN_DIR),
    proxyUrl: nonEmpty(env.RETRACE_PROXY_URL),
    markerUrl: nonEmpty(env.RETRACE_MARKER_URL),
    upstreamUrl: nonEmpty(env.RETRACE_UPSTREAM_URL),
    strict: parseStrict(env.RETRACE_STRICT),
  };
}

// requireHandshake throws MISSING_HANDSHAKE_MESSAGE when strict mode is on
// and NEITHER a file path nor an HTTP door is available to write to. It does
// NOT throw merely because one of the two is missing — group()/endGroup()
// treat runDir and markerUrl as alternatives, not requirements — callers
// that need one specifically (checkpoint(), which is file-only) must check
// that field themselves after calling this.
export function requireHandshake(env: NodeJS.ProcessEnv = process.env): Handshake {
  const h = handshake(env);
  if (h.strict && !h.runDir && !h.markerUrl) {
    throw new Error(MISSING_HANDSHAKE_MESSAGE);
  }
  return h;
}
