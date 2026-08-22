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
    await endGroup();

    const lines = await readLines(path.join(runDir, 'groups.jsonl'));
    expect(lines).toHaveLength(1);
    expect(lines[0].phase).toBe('end');
    // The writer is stateless — a fresh process cannot know what is open —
    // so an end record must not carry a name at all, not merely a falsy
    // one: DeriveGroups ignores a name on "end" anyway, so emitting one
    // would be a lie the Go reader cannot catch.
    expect(lines[0]).not.toHaveProperty('name');
  });

  it('rejects a group name that is not a safe path component', async () => {
    await setup();
    await expect(group('cart/item')).rejects.toThrow(/invalid group name/);
    await expect(group('')).rejects.toThrow(/invalid group name/);
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

  it('throws on a network failure (the door is not listening)', async () => {
    saved = clearHandshakeEnv();
    // Port 0 is never a listener you can connect to; this stands in for
    // "the marker door process died mid-run".
    process.env.RETRACE_MARKER_URL = 'http://127.0.0.1:0';
    await expect(endGroup()).rejects.toThrow();
  });
});
