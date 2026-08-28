import * as fs from 'node:fs/promises';
import type { FullResult, Reporter, TestCase, TestResult } from '@playwright/test/reporter';
import { attachEvidence } from '@caribou-crew/retrace-js';

export interface RetraceEvidenceReporterOptions {
  // Where Playwright's own HTML reporter wrote its output. Defaults to
  // Playwright's own default ('playwright-report') — a project that hasn't
  // changed `reporter: [['html', { outputFolder: ... }]]` needs no option
  // here at all.
  reportDir?: string;
}

const DEFAULT_REPORT_DIR = 'playwright-report';

// sanitizeVideoName turns a Playwright test's title path into a filesystem-
// safe, human-legible video filename — "ViewPan: loaded and revealed"
// (nested under its spec file) becomes "ViewPan-loaded-and-revealed.webm",
// not the video attachment's own opaque hash-named path.
function sanitizeVideoName(test: TestCase): string {
  const raw = test
    .titlePath()
    .filter(Boolean)
    .slice(1) // drop the spec file segment — the checkpoint/test name alone is what a reviewer recognises
    .join(' ');
  const safe = raw.replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '');
  return `${safe || 'test'}.webm`;
}

// RetraceEvidenceReporter is a Playwright reporter, registered in a
// project's playwright.config.ts `reporter: [...]` list — see
// docs/retrace-ci-example.yml. It requires no CI script changes: every
// finished test's video (if Playwright captured one) and the HTML report's
// own output directory (if Playwright's html reporter also ran) are copied
// into the active retrace run's videos/ and report/ subdirectories, which
// `retrace sync` already carries to a developer's machine for free.
export default class RetraceEvidenceReporter implements Reporter {
  private reportDir: string;

  constructor(options: RetraceEvidenceReporterOptions = {}) {
    this.reportDir = options.reportDir ?? DEFAULT_REPORT_DIR;
  }

  async onTestEnd(test: TestCase, result: TestResult): Promise<void> {
    const video = result.attachments.find((a) => a.name === 'video' && a.path);
    if (!video?.path) return;
    await attachEvidence('video', video.path, sanitizeVideoName(test));
  }

  async onEnd(_result: FullResult): Promise<void> {
    try {
      await fs.access(this.reportDir);
    } catch {
      return; // no HTML report was generated — nothing to attach
    }
    await attachEvidence('report', this.reportDir);
  }
}
