// Hand-written types for retrace-maestro.mjs, which is deliberately plain,
// unbuilt JavaScript (see that file's header comment) — this is what lets
// ../src/index.ts import and re-export its `markerRequest` with full types,
// so the tested copy and the shipped copy are literally the same function.
export interface MarkerRequest {
  url: string;
  body: string;
}

export function markerRequest(argv: string[], env: NodeJS.ProcessEnv): MarkerRequest | null;
