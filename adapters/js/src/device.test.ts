import * as fs from 'node:fs/promises';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DEVICE_FILE, recordDevice } from './index.js';
import type { DeviceRecord } from './index.js';

async function runDir(): Promise<string> {
  return fs.mkdtemp(path.join(os.tmpdir(), 'retrace-js-device-'));
}

async function readDevice(dir: string): Promise<DeviceRecord> {
  return JSON.parse(await fs.readFile(path.join(dir, DEVICE_FILE), 'utf8')) as DeviceRecord;
}

describe('recordDevice', () => {
  let dir = '';

  afterEach(async () => {
    vi.restoreAllMocks();
    if (dir) await fs.rm(dir, { recursive: true, force: true });
    dir = '';
  });

  it('writes the screen where the Go side looks for it', async () => {
    // The path is the contract with retrace/capture.DeviceFile: the run
    // directory, beside shots/ and not inside it. A file one level off is
    // indistinguishable from one that was never written.
    dir = await runDir();
    await recordDevice({ kind: 'browser', id: 'chromium', width: 390, height: 844 }, { RETRACE_RUN_DIR: dir });

    expect(await readDevice(dir)).toEqual({ kind: 'browser', id: 'chromium', width: 390, height: 844 });
  });

  it('keeps the first screen and warns rather than overwriting', async () => {
    // Letting the last writer win would make the recorded geometry depend on
    // which parallel worker finished last, so two runs of the same unchanged
    // suite could disagree and be refused as a geometry mismatch.
    dir = await runDir();
    const stderr = vi.spyOn(process.stderr, 'write').mockReturnValue(true);

    await recordDevice({ kind: 'browser', width: 390, height: 844 }, { RETRACE_RUN_DIR: dir });
    await recordDevice({ kind: 'browser', width: 1280, height: 720 }, { RETRACE_RUN_DIR: dir });

    expect(await readDevice(dir)).toMatchObject({ width: 390, height: 844 });
    expect(stderr).toHaveBeenCalledTimes(1);
    const warning = String(stderr.mock.calls[0][0]);
    expect(warning).toContain('390x844');
    expect(warning).toContain('1280x720');
  });

  it('says nothing when a repeat write agrees with the first', async () => {
    // The playwright fixture calls this on EVERY checkpoint, so agreeing
    // repeats are the normal case. A warning per checkpoint would train
    // people to ignore the one that matters.
    dir = await runDir();
    const stderr = vi.spyOn(process.stderr, 'write').mockReturnValue(true);

    await recordDevice({ kind: 'browser', width: 390, height: 844 }, { RETRACE_RUN_DIR: dir });
    await recordDevice({ kind: 'browser', width: 390, height: 844 }, { RETRACE_RUN_DIR: dir });

    expect(stderr).not.toHaveBeenCalled();
  });

  it('refuses a screen with no size', async () => {
    // runs.validateDevice rejects these too, but at manifest-write time —
    // the end of the run, naming neither the adapter nor the test.
    dir = await runDir();
    for (const bad of [
      { kind: 'browser', width: 0, height: 844 },
      { kind: 'browser', width: 390, height: 0 },
      { kind: 'browser', width: -1, height: 844 },
      { kind: 'browser', width: 390.5, height: 844 },
    ] as DeviceRecord[]) {
      await expect(recordDevice(bad, { RETRACE_RUN_DIR: dir })).rejects.toThrow(/positive integers/);
    }
    await expect(fs.readFile(path.join(dir, DEVICE_FILE))).rejects.toThrow();
  });

  it('is a silent no-op outside a run, even in strict mode', async () => {
    // Nobody asked for this file. Strict mode promises that the markers the
    // caller wrote are recorded; failing a run over bookkeeping the adapter
    // added on its own would be a worse bargain than the geometry is worth.
    await expect(
      recordDevice({ kind: 'browser', width: 390, height: 844 }, { RETRACE_STRICT: '1' }),
    ).resolves.toBeUndefined();
  });

  it('validates the size before it looks for a run', async () => {
    // Otherwise a 0x0 bug is invisible until someone runs the suite under
    // retrace, which is the run it then breaks.
    await expect(recordDevice({ kind: 'browser', width: 0, height: 0 }, {})).rejects.toThrow(/positive integers/);
  });
});
