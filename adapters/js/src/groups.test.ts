import * as fs from 'node:fs/promises';
import * as http from 'node:http';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { endGroup, group } from './index.js';

const ENV_KEYS = ['RETRACE_RUN_DIR', 'RETRACE_PROXY_URL', 'RETRACE_MARKER_URL', 'RETRACE_STRICT'] as const;

function clearHandshakeEnv(): Record<string, string | undefined> {
  const saved: Record<string, string | undefined> = {};
  for (const k of ENV_KEYS) {
    saved[k] = process.env[k];
    delete process.env[k];
  }
  return saved;
}

function restoreEnv(saved: Record<string, string | undefined>): void {
  for (const k of ENV_KEYS) {
    if (saved[k] === undefined) delete process.env[k];
    else process.env[k] = saved[k];
  }
}

async function readLines(file: string): Promise<Record<string, unknown>[]> {
  const raw = await fs.readFile(file, 'utf8');
  return raw
    .split('\n')
    .filter((l) => l.length > 0)
    .map((l) => JSON.parse(l));
}

describe('group / endGroup — file path (RETRACE_RUN_DIR)', () => {
  let saved: Record<string, string | undefined>;
  let runDir: string;

  afterEach(async () => {
    restoreEnv(saved);
    await fs.rm(runDir, { recursive: true, force: true });
  });

  async function setup(): Promise<void> {
    saved = clearHandshakeEnv();
    runDir = await fs.mkdtemp(path.join(os.tmpdir(), 'retrace-js-groups-'));
    process.env.RETRACE_RUN_DIR = runDir;
  }

  it('appends a start record to groups.jsonl in RETRACE_RUN_DIR', async () => {
    await setup();
    const before = Date.now();
    await group('checkout', { quiet: true });
    const after = Date.now();

    const lines = await readLines(path.join(runDir, 'groups.jsonl'));
    expect(lines).toHaveLength(1);
    const rec = lines[0];
    expect(rec.phase).toBe('start');
    expect(rec.name).toBe('checkout');
    expect(rec.quiet).toBe(true);

    // R-AC: `ts` must be an RFC3339 string with an explicit zone
    // (Date.prototype.toISOString), not epoch millis — Go's time.Time
    // unmarshals the former and errors on the latter, and
    // runs.ReadGroupRecords silently SKIPS a line it cannot unmarshal. A
    // numeric `ts` would pass a looser "has a ts key" assertion; it fails
    // this one.
    expect(typeof rec.ts).toBe('string');
    expect(rec.ts as string).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    const parsed = Date.parse(rec.ts as string);
    expect(parsed).toBeGreaterThanOrEqual(before);
    expect(parsed).toBeLessThanOrEqual(after);
  });

  it('appends an end record carrying no name', async () => {
    await setup();
    const before = Date.now();
    await endGroup();
    const after = Date.now();

    const lines = await readLines(path.join(runDir, 'groups.jsonl'));
    expect(lines).toHaveLength(1);
    const rec = lines[0];
    expect(rec.phase).toBe('end');
    // The writer is stateless — a fresh process cannot know what is open —
    // so an end record must not carry a name at all, not merely a falsy
    // one: DeriveGroups ignores a name on "end" anyway, so emitting one
    // would be a lie the Go reader cannot catch.
    expect(rec).not.toHaveProperty('name');

    // F-3 (task-17-review.md): the start-record test pins `ts`'s shape;
    // this one did not, and a bad `end.ts` is the harder-to-see half of
    // R-AC — DeriveGroups' closeAt(finishedAt) fallback means the group
    // still closes at *some* time, so only checking bounds against the
    // RUN's window (as Step 5 must, since there is no other window to
    // check here) can miss it; checking the encoding itself cannot.
    expect(typeof rec.ts).toBe('string');
    expect(rec.ts as string).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    const parsed = Date.parse(rec.ts as string);
    expect(parsed).toBeGreaterThanOrEqual(before);
    expect(parsed).toBeLessThanOrEqual(after);
  });

  it('rejects a group name that is not a safe path component', async () => {
    await setup();
    await expect(group('cart/item')).rejects.toThrow(/invalid group name/);
    await expect(group('')).rejects.toThrow(/invalid group name/);
    await expect(fs.stat(path.join(runDir, 'groups.jsonl'))).rejects.toThrow();
  });

  // F-5 (task-17-review.md): these four all satisfy VALID_NAME's bare
  // character class — '.', '..', and '.hidden'/'...' contain nothing outside
  // [A-Za-z0-9._-] — so a validateName that only re-tested the regex (the
  // old, R-AE-described behavior) would accept every one of them. Go's
  // runs.ValidateComponents rejects all four via its leading-dot clause, and
  // this adapter now reproduces that clause, so it must too.
  it('rejects a leading-dot group name, matching runs.ValidateComponents', async () => {
    await setup();
    for (const name of ['.', '..', '.hidden', '...']) {
      await expect(group(name), `group(${JSON.stringify(name)})`).rejects.toThrow(/invalid group name/);
    }
    await expect(fs.stat(path.join(runDir, 'groups.jsonl'))).rejects.toThrow();
  });
});

describe('group / endGroup — HTTP fallback (RETRACE_MARKER_URL)', () => {
  let saved: Record<string, string | undefined>;

  afterEach(() => restoreEnv(saved));

  it('falls back to POSTing RETRACE_MARKER_URL when RETRACE_RUN_DIR is absent', async () => {
    const requests: { path: string; body: string }[] = [];
    const server = http.createServer((req, res) => {
      let body = '';
      req.on('data', (c) => (body += c));
      req.on('end', () => {
        requests.push({ path: req.url ?? '', body });
        res.writeHead(204);
        res.end();
      });
    });
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    const addr = server.address();
    if (addr === null || typeof addr === 'string') throw new Error('unexpected server address');

    saved = clearHandshakeEnv();
    process.env.RETRACE_MARKER_URL = `http://127.0.0.1:${addr.port}`;

    await group('checkout');
    await endGroup();
    server.close();

    expect(requests).toHaveLength(2);
    expect(requests[0].path).toBe('/group');
    expect(JSON.parse(requests[0].body)).toEqual({ name: 'checkout', quiet: false });
    expect(requests[1].path).toBe('/group/end');
    expect(JSON.parse(requests[1].body)).toEqual({});
  });

  // R-AF: `fetch` does not throw on a non-2xx response. An adapter that
  // ignores response.ok reports success for a marker the door rejected —
  // exactly the silent-garbage condition the door's 400s exist to prevent.
  it('throws, naming the status and body, when the marker door rejects the request', async () => {
    const server = http.createServer((req, res) => {
      res.writeHead(400, { 'content-type': 'application/json' });
      res.end('{"error":"group markers require a non-empty name"}');
    });
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    const addr = server.address();
    if (addr === null || typeof addr === 'string') throw new Error('unexpected server address');

    saved = clearHandshakeEnv();
    process.env.RETRACE_MARKER_URL = `http://127.0.0.1:${addr.port}`;

    await expect(group('checkout')).rejects.toThrow(/400/);
    await expect(group('checkout')).rejects.toThrow(/non-empty name/);
    server.close();
  });

  // F-8 (task-17-review.md): a trailing slash on RETRACE_MARKER_URL is a
  // realistic misconfiguration (many env-var conventions include one), and
  // naive `markerUrl + urlPath` concatenation turns it into `//group` —
  // markers.go registers bare paths, so that is not a path its mux matches.
  it('POSTs to the right path even when RETRACE_MARKER_URL has a trailing slash', async () => {
    const requests: { path: string; body: string }[] = [];
    const server = http.createServer((req, res) => {
      let body = '';
      req.on('data', (c) => (body += c));
      req.on('end', () => {
        requests.push({ path: req.url ?? '', body });
        res.writeHead(204);
        res.end();
      });
    });
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    const addr = server.address();
    if (addr === null || typeof addr === 'string') throw new Error('unexpected server address');

    saved = clearHandshakeEnv();
    process.env.RETRACE_MARKER_URL = `http://127.0.0.1:${addr.port}/`;

    await group('checkout');
    server.close();

    expect(requests).toHaveLength(1);
    expect(requests[0].path).toBe('/group');
  });

  it('throws on a network failure (the door is not listening)', async () => {
    saved = clearHandshakeEnv();
    // Port 0 is never a listener you can connect to; this stands in for
    // "the marker door process died mid-run".
    process.env.RETRACE_MARKER_URL = 'http://127.0.0.1:0';
    await expect(endGroup()).rejects.toThrow();
  });
});
