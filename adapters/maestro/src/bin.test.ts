// bin.test.ts is the ONE test in this package that actually executes
// bin/retrace-maestro.mjs as a process — a unit test importing markerRequest
// cannot catch F-1, because the defect is in whether main() runs at all.
// npm's own "bin" mechanism materialises a SYMLINK at
// node_modules/.bin/retrace-maestro (this package's package.json declares
// "bin"), and that symlink is how every real installation invokes this
// file — so the symlinked case is not a hypothetical, it is the normal path.
import * as fs from 'node:fs/promises';
import * as http from 'node:http';
import * as os from 'node:os';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { afterEach, describe, expect, it } from 'vitest';

const BIN_PATH = fileURLToPath(new URL('../bin/retrace-maestro.mjs', import.meta.url));

interface DoorHit {
  method: string | undefined;
  url: string | undefined;
  body: string;
}

async function startFakeDoor(): Promise<{ url: string; hits: DoorHit[]; close(): Promise<void> }> {
  const hits: DoorHit[] = [];
  const server = http.createServer((req, res) => {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      hits.push({ method: req.method, url: req.url, body });
      res.writeHead(204);
      res.end();
    });
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const addr = server.address();
  if (addr === null || typeof addr === 'string') throw new Error('unexpected server address');
  return {
    url: `http://127.0.0.1:${addr.port}`,
    hits,
    close: () => new Promise((resolve) => server.close(() => resolve())),
  };
}

function run(execPath: string, args: string[], env: NodeJS.ProcessEnv): Promise<{ code: number | null; out: string; err: string }> {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [execPath, ...args], {
      env: { ...process.env, ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let out = '';
    let err = '';
    child.stdout.on('data', (c) => (out += c));
    child.stderr.on('data', (c) => (err += c));
    child.on('close', (code) => resolve({ code, out, err }));
  });
}

describe('bin/retrace-maestro.mjs as an executed process', () => {
  let tmpDir: string;

  afterEach(async () => {
    if (tmpDir) await fs.rm(tmpDir, { recursive: true, force: true });
  });

  it('runs main() and posts a marker when invoked directly', async () => {
    const door = await startFakeDoor();
    try {
      const res = await run(BIN_PATH, ['group', 'checkout'], { RETRACE_MARKER_URL: door.url });
      expect(res.code, `stderr: ${res.err}`).toBe(0);
      expect(door.hits).toEqual([{ method: 'POST', url: '/group', body: JSON.stringify({ name: 'checkout' }) }]);
    } finally {
      await door.close();
    }
  });

  // This is F-1's pinning test: node_modules/.bin/<name> is a SYMLINK to the
  // package's declared "bin" file, and Node resolves import.meta.url to the
  // REAL path of that file while leaving process.argv[1] as the symlink path
  // the caller typed — the exact mismatch that made the old guard silently
  // no-op main() on the normal install path (exit 0, zero POSTs).
  it('runs main() and posts a marker when invoked through a symlink (the node_modules/.bin path every real install uses)', async () => {
    const door = await startFakeDoor();
    try {
      tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), 'retrace-maestro-bin-'));
      const symlinkPath = path.join(tmpDir, 'retrace-maestro'); // no .mjs extension, like a real npm bin shim
      await fs.symlink(BIN_PATH, symlinkPath);

      const res = await run(symlinkPath, ['group', 'checkout'], { RETRACE_MARKER_URL: door.url });
      expect(res.code, `stderr: ${res.err}`).toBe(0);
      expect(door.hits).toEqual([{ method: 'POST', url: '/group', body: JSON.stringify({ name: 'checkout' }) }]);
    } finally {
      await door.close();
    }
  });

  // markerRequest's own unit tests (index.test.ts) cannot see this: the
  // response.ok check lives in main(), which only a spawned process
  // executes. Without it, a rejected marker looks like a successful one —
  // exactly the silent-garbage condition R-AF exists to prevent, and one of
  // the three defects M19 bundled together in the review.
  it('exits non-zero and reports the door status when the marker POST is rejected', async () => {
    const server = http.createServer((_req, res) => {
      res.writeHead(400, { 'content-type': 'application/json' });
      res.end('{"error":"group markers require a non-empty name"}');
    });
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    const addr = server.address();
    if (addr === null || typeof addr === 'string') throw new Error('unexpected server address');
    try {
      const res = await run(BIN_PATH, ['group', 'checkout'], { RETRACE_MARKER_URL: `http://127.0.0.1:${addr.port}` });
      expect(res.code).toBe(1);
      expect(res.err).toMatch(/400/);
      expect(res.err).toMatch(/non-empty name/);
    } finally {
      await new Promise((resolve) => server.close(resolve));
    }
  });
});
