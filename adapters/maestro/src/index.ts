// markerRequest's real implementation lives in ../bin/retrace-maestro.mjs —
// the file Maestro's `runScript` actually invokes — and is re-exported here
// rather than duplicated, so this package's own tests exercise the exact
// code that ships, not a hand-synced copy that can drift from it (see
// ../bin/retrace-maestro.mjs's header comment and ../bin/retrace-maestro.d.mts
// for its types).
export { markerRequest } from '../bin/retrace-maestro.mjs';
export type { MarkerRequest } from '../bin/retrace-maestro.mjs';
