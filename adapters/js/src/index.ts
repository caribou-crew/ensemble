import * as fs from 'node:fs/promises';
import * as path from 'node:path';
import { handshake, requireHandshake, MISSING_HANDSHAKE_MESSAGE } from './handshake.js';
import type { Handshake } from './handshake.js';

export type { Handshake };
export { handshake, requireHandshake, MISSING_HANDSHAKE_MESSAGE };

// VALID_NAME mirrors retrace/runs/paths.go:64's validComponent regex
// verbatim. R-AE (task-17-rulings.md): TypeScript cannot call the Go guard
// across the process boundary, so this is the deliberate second
// implementation of that one rule — checkpoint names (retrace-playwright)
// and group names (here, and retrace-maestro's CLI) both reach the
// filesystem or a JSON body from adapter code, where runs.ValidateComponents
// cannot reach. Keep this in sync with paths.go if that regex ever changes.
export const VALID_NAME = /^[A-Za-z0-9._-]+$/;

// validateName REJECTS by throwing, never by skipping. A skipped checkpoint
// or group name is a missing marker that looks exactly like nobody ever
// asked for one — the same silent-loss shape R-AC is about — so this throws
// unconditionally, in both strict and non-strict mode: strict mode governs
// whether there is a run to write to, not whether the name the caller typed
// is safe to write.
export function validateName(kind: 'checkpoint' | 'group', name: string): void {
  if (!VALID_NAME.test(name)) {
    throw new Error(
      `retrace: invalid ${kind} name ${JSON.stringify(name)} — must match ${VALID_NAME} ` +
        '(see retrace/runs/paths.go validComponent)',
    );
  }
}

// shotsDir is the directory @caribou-crew/retrace-playwright writes
// screenshots into. It is a plain derived path, not a handshake check: a
// caller that needs strict-mode enforcement calls requireHandshake() (or
// checks .runDir) itself first.
export function shotsDir(env: NodeJS.ProcessEnv = process.env): string | null {
  const h = handshake(env);
  return h.runDir ? path.join(h.runDir, 'shots') : null;
}

interface GroupRecord {
  phase: 'start' | 'end';
  name?: string;
  ts: string;
  quiet?: boolean;
}

// appendGroupRecord mirrors runs.AppendGroupRecord's on-disk shape exactly:
// one JSON object per line, appended to groups.jsonl in the run directory.
async function appendGroupRecord(runDir: string, record: GroupRecord): Promise<void> {
  await fs.mkdir(runDir, { recursive: true });
  await fs.appendFile(path.join(runDir, 'groups.jsonl'), JSON.stringify(record) + '\n', 'utf8');
}

// postMarker is the HTTP fallback (NewMarkerDoor in retrace/capture/markers.go).
// R-AF (task-17-rulings.md): `fetch` does not throw on a non-2xx response, so
// response.ok is checked explicitly — an adapter that ignored the status
// would report success for a marker the server rejected, which is exactly
// the silent-garbage condition the door's own 400s exist to prevent. A
// network failure (the door not listening) throws too, via fetch's own
// rejection; neither is softened by non-strict mode, because once
// RETRACE_MARKER_URL is set there IS a run, and a failure to record is a
// failure.
async function postMarker(markerUrl: string, urlPath: string, body: unknown): Promise<void> {
  const res = await fetch(markerUrl + urlPath, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '<unreadable body>');
    throw new Error(
      `retrace: marker POST ${urlPath} to ${markerUrl} failed with ${res.status} ${res.statusText}: ${text}`,
    );
  }
}

// group appends a "start" flow-part marker. R-AC (task-17-rulings.md): `ts`
// is an RFC3339 string with an explicit zone (`Date.prototype.toISOString`),
// never epoch millis — runs.ReadGroupRecords silently skips a line it cannot
// unmarshal, and time.Time unmarshals RFC3339 but not a bare number, so the
// wrong encoding here is a total, silent loss of every marker the adapter
// ever writes.
export async function group(name: string, options?: { quiet?: boolean }): Promise<void> {
  validateName('group', name);
  const h = requireHandshake();
  if (!h.runDir && !h.markerUrl) return; // no-op outside a run, strict is off
  const quiet = options?.quiet ?? false;
  if (h.runDir) {
    await appendGroupRecord(h.runDir, { phase: 'start', name, ts: new Date().toISOString(), quiet });
  } else if (h.markerUrl) {
    await postMarker(h.markerUrl, '/group', { name, quiet });
  }
}

// endGroup appends an "end" marker carrying NO name — the writer is
// stateless (a fresh process cannot know what is open; runs.DeriveGroups
// ignores a name on an "end" record anyway, so sending one would be a lie
// the reader cannot catch).
export async function endGroup(): Promise<void> {
  const h = requireHandshake();
  if (!h.runDir && !h.markerUrl) return; // no-op outside a run, strict is off
  if (h.runDir) {
    await appendGroupRecord(h.runDir, { phase: 'end', ts: new Date().toISOString() });
  } else if (h.markerUrl) {
    await postMarker(h.markerUrl, '/group/end', {});
  }
}
