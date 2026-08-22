import * as fs from 'node:fs/promises';
import * as path from 'node:path';
import { test as base } from '@playwright/test';
import type { Locator, TestType } from '@playwright/test';
import {
  MISSING_HANDSHAKE_MESSAGE,
  endGroup as jsEndGroup,
  group as jsGroup,
  requireHandshake,
  shotsDir,
  validateName,
} from '@caribou-crew/retrace-js';

// ShotTaker is the minimal surface fixture logic needs from either a
// Playwright Page or Locator — screenshot() alone. Kept separate from
// PageLike below because a Page also needs .locator(), a Locator does not.
export interface ShotTaker {
  screenshot(): Promise<Buffer>;
}

// PageLike is the minimal surface performCheckpoint needs from a real
// Playwright Page, so the package's own tests can exercise it with a fake
// object — `{ screenshot, viewportSize, locator }` — and need no browser.
export interface PageLike extends ShotTaker {
  locator(selector: string): ShotTaker;
}

export interface RetraceFixture {
  checkpoint(name: string, options?: { selector?: string | Locator; trim?: boolean }): Promise<void>;
  group(name: string): Promise<void>;
  endGroup(): Promise<void>;
}

// performCheckpoint is the fixture's testable core, independent of
// @playwright/test's own fixture wiring (test.extend), so vitest can call it
// directly against a fake page with no browser involved.
//
// trim (uniform-border cropping) is deliberately NOT implemented here,
// unlike the ported the JS prototype file it descends from: the Go binary owns
// pixel work (retrace/pixel.TrimUniformBorder, at compare time), and
// duplicating it in TS would be a second implementation to keep in sync.
// `trim: true` writes an empty `<name>.trim` marker file beside the shot —
// that is this adapter's entire contribution to trimming.
export async function performCheckpoint(
  page: PageLike,
  name: string,
  options?: { selector?: string | ShotTaker; trim?: boolean },
): Promise<void> {
  // R-AE: reject by throwing, never by skipping, and do it BEFORE checking
  // whether a run is even active — strict mode governs whether there is a
  // run to write to, not whether the name the caller typed is writable.
  validateName('checkpoint', name);

  const h = requireHandshake();
  if (!h.runDir) {
    // checkpoint() is file-only — there is no HTTP upload path for
    // screenshots (retrace/capture/markers.go's door has no such route) —
    // so a markerUrl-only handshake cannot satisfy it either. Strict mode
    // must still fail loudly here even though requireHandshake() itself
    // did not throw (it only requires ONE of runDir/markerUrl, and this
    // capability needs runDir specifically).
    if (h.strict) throw new Error(MISSING_HANDSHAKE_MESSAGE);
    return; // no-op outside a run
  }

  const dir = shotsDir()!; // h.runDir is set, so shotsDir() cannot be null
  await fs.mkdir(dir, { recursive: true });

  const target: ShotTaker =
    options?.selector === undefined
      ? page
      : typeof options.selector === 'string'
        ? page.locator(options.selector)
        : options.selector; // an already-scoped Locator, e.g. for cross-origin frames

  const buf = await target.screenshot();
  await fs.writeFile(path.join(dir, `${name}.png`), buf);
  if (options?.trim) {
    await fs.writeFile(path.join(dir, `${name}.trim`), '');
  }
}

// Exported (not just used internally by test.extend below) so the package's
// own tests can exercise the fixture object directly with a fake PageLike —
// proving `group`/`endGroup` actually delegate to @caribou-crew/retrace-js
// and that `checkpoint`'s options reach performCheckpoint — without needing
// a real Playwright browser/page (F-4, task-17-review.md: test.extend's own
// wiring was previously untested; only performCheckpoint in isolation was).
export function createRetraceFixture(page: PageLike): RetraceFixture {
  return {
    checkpoint: (name, options) => performCheckpoint(page, name, options),
    group: (name) => jsGroup(name),
    endGroup: () => jsEndGroup(),
  };
}

export const test: TestType<{ retrace: RetraceFixture }, object> = base.extend<{ retrace: RetraceFixture }>({
  retrace: async ({ page }, use) => {
    await use(createRetraceFixture(page));
  },
});
