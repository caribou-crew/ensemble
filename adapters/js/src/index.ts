import * as fs from 'node:fs/promises';
import * as path from 'node:path';
import { handshake, requireHandshake, MISSING_HANDSHAKE_MESSAGE } from './handshake.js';
import type { Handshake } from './handshake.js';

export type { Handshake };
export { handshake, requireHandshake, MISSING_HANDSHAKE_MESSAGE };

// VALID_NAME mirrors the *character-class* half of
// retrace/runs.ValidateComponents' guard. F-5 (task-17-review.md; corrects
// R-AE, which cited only the bare regex): the Go guard is the four-part
// disjunction in ValidateComponents — empty, a leading dot, a "/" or "\\"
// separator, or a regex mismatch — not the regex alone, and a leading dot
// ("." | ".." | ".hidden" | "...") passes this regex while
// ValidateComponents rejects all of it (dotfile/relative-path components are
// exactly what it exists to keep off the filesystem, since "." and ".."
// aren't components a real one can equal either). validateName below
// reproduces the whole guard, not just this piece.
export const VALID_NAME = /^[A-Za-z0-9._-]+$/;

// validateName REJECTS by throwing, never by skipping. A skipped checkpoint
// or group name is a missing marker that looks exactly like nobody ever
// asked for one — the same silent-loss shape R-AC is about — so this throws
// unconditionally, in both strict and non-strict mode: strict mode governs
// whether there is a run to write to, not whether the name the caller typed
// is safe to write.
//
// The condition below is the TypeScript reimplementation of
// retrace/runs.ValidateComponents' full guard (R-AE/F-5: TypeScript cannot
// call the Go guard across the process boundary, so this is the deliberate
// second implementation of that one rule — checkpoint names
// (retrace-playwright) and group names (here, and retrace-maestro's CLI)
// both reach the filesystem or a JSON body from adapter code, where
// ValidateComponents cannot reach). Keep all four clauses in sync with
// ValidateComponents if it ever changes, not just the regex.
export function validateName(kind: 'checkpoint' | 'group', name: string): void {
  if (name === '' || name.startsWith('.') || /[/\\]/.test(name) || !VALID_NAME.test(name)) {
    throw new Error(
      `retrace: invalid ${kind} name ${JSON.stringify(name)} — must be non-empty, not start with ` +
        `"." and match ${VALID_NAME} (see retrace/runs.ValidateComponents)`,
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

// DeviceRecord mirrors retrace/runs.Device, the manifest's record of the
// screen a run was captured on. `kind` says where the numbers came from, and
// the diff message quotes it: two "browser" runs at different sizes is a
// viewport someone changed, a "browser" against a "device" is two adapters
// that were never comparable.
//
// "shot" is deliberately absent from this union. It is the Go side's own
// label for the fallback it derives from the first screenshot when no adapter
// wrote a device — an adapter claiming it would be claiming not to know its
// own geometry, which is the one thing it always does know.
export interface DeviceRecord {
  kind: 'browser' | 'device';
  id?: string;
  width: number;
  height: number;
  scale?: number;
}

// DEVICE_FILE is retrace/capture.DeviceFile. It sits in the run directory,
// beside shots/ rather than inside it.
export const DEVICE_FILE = 'device.json';

// recordDevice writes the screen this run is capturing on, which the Go side
// prefers over the geometry it would otherwise infer from the first
// screenshot. That preference matters most when a checkpoint is scoped to a
// selector: the shot is then the size of the ELEMENT, and a run whose first
// checkpoint is `checkpoint('cart', { selector: '#cart' })` would otherwise
// report a 300x120 "screen" and disagree with the identical run whose first
// checkpoint happens to be full-page.
//
// Zero dimensions throw rather than write. runs.validateDevice rejects them
// too, but it does so at manifest-write time — at the END of a run, naming
// neither the adapter nor the test that produced them. Failing here costs the
// same run and says where to look.
//
// FIRST WRITE WINS, and a later, conflicting geometry warns on stderr instead
// of overwriting. The manifest carries one screen; if the suite genuinely
// spans two, no single value describes it, and letting the last writer win
// would make the recorded geometry depend on which parallel worker finished
// last — producing phantom geometry mismatches between two runs of the same
// unchanged suite. Such a suite has a larger problem anyway: its checkpoints
// collide by filename in shots/ long before their sizes disagree.
export async function recordDevice(
  device: DeviceRecord,
  env: NodeJS.ProcessEnv = process.env,
): Promise<void> {
  if (!Number.isInteger(device.width) || !Number.isInteger(device.height) || device.width <= 0 || device.height <= 0) {
    throw new Error(
      `retrace: refusing to record a ${device.width}x${device.height} screen — ` +
        `width and height must be positive integers (see retrace/runs.validateDevice)`,
    );
  }

  const h = handshake(env);
  if (!h.runDir) {
    // File-only, like checkpoint(): a markerUrl-only handshake has no run
    // directory to write into. Unlike checkpoint(), this is called BY the
    // adapter rather than by the person writing the test, so it stays silent
    // even in strict mode — strict promises that the caller's own markers are
    // recorded, and failing a run over bookkeeping the caller never asked for
    // would be a worse bargain than the geometry is worth.
    return;
  }

  const file = path.join(h.runDir, DEVICE_FILE);
  await fs.mkdir(h.runDir, { recursive: true });
  try {
    await fs.writeFile(file, JSON.stringify(device) + '\n', { encoding: 'utf8', flag: 'wx' });
  } catch (err: unknown) {
    if ((err as NodeJS.ErrnoException)?.code !== 'EEXIST') throw err;
    const existing = JSON.parse(await fs.readFile(file, 'utf8')) as DeviceRecord;
    if (existing.width !== device.width || existing.height !== device.height) {
      process.stderr.write(
        `retrace: this run captured on more than one screen — keeping ${existing.width}x${existing.height} ` +
          `and ignoring ${device.width}x${device.height}. Screen comparisons for this run describe the first ` +
          `only.\n`,
      );
    }
  }
}

export type EvidenceKind = 'video' | 'report';

// copyDirContents copies every entry of src directly INTO dest (dest
// becomes what src's contents were), not as a nested dest/<basename(src)>
// subdirectory — WriteReport (retrace/serve) serves report/index.html, so
// index.html must land at report/index.html, not report/<name>/index.html.
async function copyDirContents(src: string, dest: string): Promise<void> {
  await fs.mkdir(dest, { recursive: true });
  const entries = await fs.readdir(src, { withFileTypes: true });
  await Promise.all(
    entries.map((e) => fs.cp(path.join(src, e.name), path.join(dest, e.name), { recursive: true })),
  );
}

// attachEvidence copies a finished test-runner artifact (a video file, or a
// whole HTML report directory) into the active run's videos/ or report/
// subdirectory. Unlike checkpoint() and recordDevice(), a Manifest field
// never carries this: the artifact is not ready until AFTER `retrace run`
// exits (a video isn't flushed until test teardown; an HTML report isn't
// written until the test command's own reporter finishes), so nothing here
// can be part of the ONE write WriteManifest already owns. Discovery is by
// directory listing instead — see the design doc's D1.
//
// File-only and silent outside a run, same rule as checkpoint(): there is
// no HTTP marker equivalent for "attach this file" (retrace/capture/markers.go
// has no such door), so a markerUrl-only handshake cannot satisfy this
// either, and strict mode must still fail loudly when NEITHER is set.
export async function attachEvidence(
  kind: EvidenceKind,
  srcPath: string,
  name?: string,
  env: NodeJS.ProcessEnv = process.env,
): Promise<void> {
  const h = requireHandshake(env);
  if (!h.runDir) {
    if (h.strict) throw new Error(MISSING_HANDSHAKE_MESSAGE);
    return; // no-op outside a run
  }

  if (kind === 'video') {
    const dir = path.join(h.runDir, 'videos');
    await fs.mkdir(dir, { recursive: true });
    await fs.copyFile(srcPath, path.join(dir, name ?? path.basename(srcPath)));
    return;
  }

  await copyDirContents(srcPath, path.join(h.runDir, 'report'));
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

// joinMarkerPath avoids the `//group` a trailing slash on RETRACE_MARKER_URL
// would otherwise produce (F-8, task-17-review.md): markers.go registers
// bare paths, so a subtree redirect never silently drops the POST body, and
// `//group` is not a bare path the mux matches. Mirrors
// retrace-maestro/bin/retrace-maestro.mjs's joinMarkerPath — keep the two in
// sync if this ever changes.
function joinMarkerPath(markerUrl: string, urlPath: string): string {
  return markerUrl.replace(/\/+$/, '') + urlPath;
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
  const res = await fetch(joinMarkerPath(markerUrl, urlPath), {
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
