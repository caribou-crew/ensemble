import * as fs from 'node:fs/promises';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { performCheckpoint } from './fixture.js';
import type { PageLike, ShotTaker } from './fixture.js';

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

const PNG_BYTES = Buffer.from('fake-full-page-png');
const SELECTOR_PNG_BYTES = Buffer.from('fake-selector-png');

// fakePage is exactly the shape Task 17's Step 2 specifies:
// `{ screenshot: async () => Buffer, viewportSize: () => ({width, height}),
// locator: (s) => ({ screenshot }) }` — no browser involved.
function fakePage(opts?: { locatorScreenshot?: () => Promise<Buffer> }): PageLike & { viewportSize(): { width: number; height: number } } {
  return {
    screenshot: async () => PNG_BYTES,
    viewportSize: () => ({ width: 800, height: 600 }),
    locator: (_selector: string): ShotTaker => ({
      screenshot: opts?.locatorScreenshot ?? (async () => SELECTOR_PNG_BYTES),
    }),
  };
}

describe('checkpoint', () => {
  let saved: Record<string, string | undefined>;
  let runDir: string;

  afterEach(async () => {
    restoreEnv(saved);
    if (runDir) await fs.rm(runDir, { recursive: true, force: true });
  });

  async function withRun(): Promise<string> {
    saved = clearHandshakeEnv();
    runDir = await fs.mkdtemp(path.join(os.tmpdir(), 'retrace-pw-'));
    process.env.RETRACE_RUN_DIR = runDir;
    return runDir;
  }

  it('writes <name>.png into the run dir shots directory', async () => {
    await withRun();
    await performCheckpoint(fakePage(), 'cart');
    const written = await fs.readFile(path.join(runDir, 'shots', 'cart.png'));
    expect(written).toEqual(PNG_BYTES);
  });

  it('scopes the shot to a selector when given one', async () => {
    await withRun();
    await performCheckpoint(fakePage(), 'cart', { selector: '#cart' });
    const written = await fs.readFile(path.join(runDir, 'shots', 'cart.png'));
    expect(written).toEqual(SELECTOR_PNG_BYTES);
  });

  it('accepts an already-scoped Locator (for cross-origin frames)', async () => {
    await withRun();
    const locatorBytes = Buffer.from('already-scoped-locator-png');
    const preScoped: ShotTaker = { screenshot: async () => locatorBytes };
    await performCheckpoint(fakePage(), 'cart', { selector: preScoped });
    const written = await fs.readFile(path.join(runDir, 'shots', 'cart.png'));
    expect(written).toEqual(locatorBytes);
  });

  it('writes a .trim marker beside the shot when trim is true', async () => {
    await withRun();
    await performCheckpoint(fakePage(), 'cart', { trim: true });
    await expect(fs.stat(path.join(runDir, 'shots', 'cart.png'))).resolves.toBeDefined();
    const marker = await fs.readFile(path.join(runDir, 'shots', 'cart.trim'), 'utf8');
    expect(marker).toBe('');
  });

  // Counterpart to the trim test above: the marker's ABSENCE must also be
  // read correctly (no stray .trim file when trim was not requested).
  it('writes no .trim marker when trim is not requested', async () => {
    await withRun();
    await performCheckpoint(fakePage(), 'cart');
    await expect(fs.stat(path.join(runDir, 'shots', 'cart.trim'))).rejects.toThrow();
  });

  it('is a no-op outside a run when strict is off', async () => {
    saved = clearHandshakeEnv();
    let called = false;
    const page = fakePage();
    const spiedPage: PageLike = {
      ...page,
      screenshot: async () => {
        called = true;
        return PNG_BYTES;
      },
    };
    await expect(performCheckpoint(spiedPage, 'cart')).resolves.toBeUndefined();
    expect(called).toBe(false);
  });

  it('throws the handshake message in strict mode', async () => {
    saved = clearHandshakeEnv();
    process.env.RETRACE_STRICT = '1';
    await expect(performCheckpoint(fakePage(), 'cart')).rejects.toThrow(/no active run/);
  });

  // R-AE: pin the REJECTION, not just the acceptance — a test that only
  // asserts checkpoint('cart') works passes against no validation at all.
  it('throws when the checkpoint name is not a safe path component', async () => {
    await withRun();
    await expect(performCheckpoint(fakePage(), 'cart/item')).rejects.toThrow(/invalid checkpoint name/);
    await expect(fs.stat(path.join(runDir, 'shots'))).rejects.toThrow();
  });

  // checkpoint() is file-only: there is no HTTP upload route for
  // screenshots, so a markerUrl-only handshake cannot satisfy it. Strict
  // mode must still fail loudly even though requireHandshake() alone would
  // not have thrown (it is satisfied by markerUrl OR runDir).
  it('throws in strict mode even when only RETRACE_MARKER_URL is set', async () => {
    saved = clearHandshakeEnv();
    process.env.RETRACE_STRICT = '1';
    process.env.RETRACE_MARKER_URL = 'http://127.0.0.1:1';
    await expect(performCheckpoint(fakePage(), 'cart')).rejects.toThrow(/no active run/);
  });
});
