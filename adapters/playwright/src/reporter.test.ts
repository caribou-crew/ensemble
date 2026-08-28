import * as fs from 'node:fs/promises';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import type { FullResult, TestCase, TestResult } from '@playwright/test/reporter';
import RetraceEvidenceReporter from './reporter.js';

async function tmpDir(prefix: string): Promise<string> {
  return fs.mkdtemp(path.join(os.tmpdir(), prefix));
}

// fakeTest/fakeResult supply only what onTestEnd reads (titlePath, attachments)
// — no real Playwright test run needed, the same style fixture.test.ts uses
// for a fake PageLike.
function fakeTest(titlePath: string[]): TestCase {
  return { titlePath: () => titlePath } as unknown as TestCase;
}

function fakeResult(attachments: TestResult['attachments']): TestResult {
  return { attachments } as unknown as TestResult;
}

describe('RetraceEvidenceReporter', () => {
  let runDir = '';
  let videoSrc = '';

  afterEach(async () => {
    if (runDir) await fs.rm(runDir, { recursive: true, force: true });
    if (videoSrc) await fs.rm(videoSrc, { recursive: true, force: true });
    runDir = '';
    videoSrc = '';
  });

  it('attaches a finished video onTestEnd, named from the test title', async () => {
    runDir = await tmpDir('retrace-pw-reporter-run-');
    videoSrc = await tmpDir('retrace-pw-reporter-video-');
    const video = path.join(videoSrc, 'a1b2c3.webm');
    await fs.writeFile(video, 'fake webm bytes');
    process.env.RETRACE_RUN_DIR = runDir;
    try {
      const reporter = new RetraceEvidenceReporter();
      await reporter.onTestEnd(
        fakeTest(['card-views.spec.ts', 'ViewPan: loaded and revealed']),
        fakeResult([{ name: 'video', path: video, contentType: 'video/webm' }]),
      );

      const files = await fs.readdir(path.join(runDir, 'videos'));
      expect(files).toEqual(['ViewPan-loaded-and-revealed.webm']);
    } finally {
      delete process.env.RETRACE_RUN_DIR;
    }
  });

  it('does nothing when a test has no video attachment', async () => {
    runDir = await tmpDir('retrace-pw-reporter-run-');
    process.env.RETRACE_RUN_DIR = runDir;
    try {
      const reporter = new RetraceEvidenceReporter();
      await reporter.onTestEnd(fakeTest(['spec.ts', 'no video']), fakeResult([]));

      await expect(fs.access(path.join(runDir, 'videos'))).rejects.toThrow();
    } finally {
      delete process.env.RETRACE_RUN_DIR;
    }
  });

  it('attaches the HTML report onEnd when reportDir exists', async () => {
    runDir = await tmpDir('retrace-pw-reporter-run-');
    const reportDir = await tmpDir('retrace-pw-reporter-report-');
    await fs.writeFile(path.join(reportDir, 'index.html'), '<html></html>');
    process.env.RETRACE_RUN_DIR = runDir;
    try {
      const reporter = new RetraceEvidenceReporter({ reportDir });
      await reporter.onEnd({} as FullResult);

      await expect(fs.readFile(path.join(runDir, 'report', 'index.html'), 'utf8')).resolves.toBe('<html></html>');
    } finally {
      delete process.env.RETRACE_RUN_DIR;
      await fs.rm(reportDir, { recursive: true, force: true });
    }
  });

  it('does nothing onEnd when reportDir was never written (no HTML reporter configured)', async () => {
    runDir = await tmpDir('retrace-pw-reporter-run-');
    process.env.RETRACE_RUN_DIR = runDir;
    try {
      const reporter = new RetraceEvidenceReporter({ reportDir: path.join(runDir, 'nonexistent-report') });
      await reporter.onEnd({} as FullResult);

      await expect(fs.access(path.join(runDir, 'report'))).rejects.toThrow();
    } finally {
      delete process.env.RETRACE_RUN_DIR;
    }
  });
});
