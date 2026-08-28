import * as fs from 'node:fs/promises';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { attachEvidence } from './index.js';

async function runDir(): Promise<string> {
  return fs.mkdtemp(path.join(os.tmpdir(), 'retrace-js-evidence-'));
}

describe('attachEvidence', () => {
  let dir = '';
  let srcDir = '';

  afterEach(async () => {
    if (dir) await fs.rm(dir, { recursive: true, force: true });
    if (srcDir) await fs.rm(srcDir, { recursive: true, force: true });
    dir = '';
    srcDir = '';
  });

  it('copies a video into videos/ under the run directory', async () => {
    dir = await runDir();
    srcDir = await runDir();
    const src = path.join(srcDir, 'recording.webm');
    await fs.writeFile(src, 'fake webm bytes');

    await attachEvidence('video', src, undefined, { RETRACE_RUN_DIR: dir });

    const written = await fs.readFile(path.join(dir, 'videos', 'recording.webm'), 'utf8');
    expect(written).toBe('fake webm bytes');
  });

  it('uses the explicit name over the source basename', async () => {
    dir = await runDir();
    srcDir = await runDir();
    const src = path.join(srcDir, 'video-a1b2.webm');
    await fs.writeFile(src, 'fake webm bytes');

    await attachEvidence('video', src, 'ViewPan.webm', { RETRACE_RUN_DIR: dir });

    await expect(fs.readFile(path.join(dir, 'videos', 'ViewPan.webm'), 'utf8')).resolves.toBe('fake webm bytes');
    await expect(fs.access(path.join(dir, 'videos', 'video-a1b2.webm'))).rejects.toThrow();
  });

  it("copies a report directory's CONTENTS into report/, not a nested subdirectory", async () => {
    dir = await runDir();
    srcDir = await runDir();
    await fs.mkdir(path.join(srcDir, 'assets'), { recursive: true });
    await fs.writeFile(path.join(srcDir, 'index.html'), '<html></html>');
    await fs.writeFile(path.join(srcDir, 'assets', 'app.js'), 'console.log(1)');

    await attachEvidence('report', srcDir, undefined, { RETRACE_RUN_DIR: dir });

    await expect(fs.readFile(path.join(dir, 'report', 'index.html'), 'utf8')).resolves.toBe('<html></html>');
    await expect(fs.readFile(path.join(dir, 'report', 'assets', 'app.js'), 'utf8')).resolves.toBe('console.log(1)');
  });

  it('is a silent no-op outside a run, even in strict mode', async () => {
    await expect(
      attachEvidence('video', '/nonexistent/path.webm', undefined, { RETRACE_STRICT: '0' }),
    ).resolves.toBeUndefined();
  });

  it('throws MISSING_HANDSHAKE_MESSAGE in strict mode with no run directory and no marker URL', async () => {
    await expect(
      attachEvidence('video', '/nonexistent/path.webm', undefined, { RETRACE_STRICT: '1' }),
    ).rejects.toThrow(/no active run/);
  });

  it('throws in strict mode even when a marker URL is set (evidence is file-only, like checkpoint() — a marker-only handshake cannot satisfy it)', async () => {
    await expect(
      attachEvidence('video', '/nonexistent/path.webm', undefined, {
        RETRACE_MARKER_URL: 'http://127.0.0.1:1',
        RETRACE_STRICT: '1',
      }),
    ).rejects.toThrow(/no active run/);
  });

  it('is a no-op when only a marker URL is set and strict mode is off', async () => {
    await expect(
      attachEvidence('video', '/nonexistent/path.webm', undefined, {
        RETRACE_MARKER_URL: 'http://127.0.0.1:1',
      }),
    ).resolves.toBeUndefined();
  });
});
