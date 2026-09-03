import { describe, expect, it } from 'vitest';

// Final review F5, the CLASS rather than the twelve instances.
//
// messageOf.load-sites.test.ts pins the twelve load sites that exist today: each one is
// driven to its error state and asserted to render the friendly fallback and not the raw
// Error#message. Those are behavioural, and they catch things a source check cannot — a site
// wired to the wrong fallback string, or one that renders the error through some other path.
// What they cannot catch is a THIRTEENTH site: a new view, or a new useAsync in an existing
// one, that writes `error.message` on the day it is added. Nothing would fail, because
// nothing would be testing it yet.
//
// So this asserts the invariant the twelve fixes were all instances of: outside messageOf's
// own definition, ensemble-ui's source reads `.message` off an error NOWHERE. messageOf is
// the single place that decides whether a caught value's own message is fit to show a human
// (ApiError: yes — it is our own server's prose; anything else: no — it is
// "Failed to fetch" or a JSON parse position), and that decision does not get made a second
// time somewhere else.
//
// Deliberately zero-tolerance and deliberately not an allowlist of files: a legitimate future
// `.message` read has to fail this test and be added here on purpose, with a reason. That is
// the whole mechanism — an exception that cannot be taken silently.

// This package has no `@types/node`; route the specifiers through non-literal `string`s so
// TypeScript falls back to `Promise<any>` rather than resolving Node builtins. Same technique,
// and the same reason, as testSetup.ts's own `net` import.
const fsModuleName: string = 'node:fs';
const pathModuleName: string = 'node:path';
const fs = (await import(fsModuleName)) as {
  readdirSync: (dir: string, opts: { withFileTypes: true }) => { name: string; isDirectory(): boolean }[];
  readFileSync: (file: string, enc: string) => string;
};
const path = (await import(pathModuleName)) as { join: (...parts: string[]) => string };
const proc = (globalThis as unknown as { process: { cwd(): string } }).process;

/** messageOf's own definition — the one place allowed to read an error's `.message`. */
const DEFINITION = 'src/api/client.ts';

/** `.message` reads that are legitimate because there is no caught error involved at all —
 * messageOf's ApiError-vs-everything-else decision doesn't apply, so it isn't a bypass of it.
 * Matched by file + exact trimmed line content (not line number), so a genuinely NEW
 * `.message` read landing elsewhere in the same file still fails the scan. Add an entry here,
 * with a reason, only for a case like this one — never to quiet an actual error read. */
const ALLOWED: { file: string; line: string; reason: string }[] = [
  {
    file: 'src/views/ServicesView.tsx',
    line: "<Tooltip content={`${WIRING_EXPLAIN}\\n\\n${warnings.map((w) => w.message).join('\\n')}`}>",
    reason:
      "WiringWarning.message is our own control plane's prose (config.WiringWarning's own " +
      '`message` field, structured API data describing a proxy-wiring mismatch) — never a ' +
      "caught Error, so messageOf's ApiError-vs-unknown-failure decision doesn't apply.",
  },
];

function sourceFiles(dir: string, acc: string[] = []): string[] {
  for (const entry of fs.readdirSync(path.join(proc.cwd(), dir), { withFileTypes: true })) {
    const rel = `${dir}/${entry.name}`;
    if (entry.isDirectory()) {
      sourceFiles(rel, acc);
    } else if (/\.tsx?$/.test(entry.name) && !/\.(test|probe)\.tsx?$/.test(entry.name)) {
      acc.push(rel);
    }
  }
  return acc;
}

describe('messageOf is the only place an error message is read', () => {
  it('reads `.message` off an error nowhere outside messageOf itself', () => {
    const offenders: string[] = [];
    const files = sourceFiles('src');

    for (const file of files) {
      if (file === DEFINITION) continue;
      const lines = fs.readFileSync(path.join(proc.cwd(), file), 'utf8').split('\n');
      lines.forEach((line, i) => {
        // Comments discuss `.message` freely — this is about code.
        const code = line.replace(/\/\/.*$/, '');
        if (!/\.message\b/.test(code)) return;
        const trimmed = line.trim();
        if (ALLOWED.some((a) => a.file === file && a.line === trimmed)) return;
        offenders.push(`${file}:${i + 1}  ${line.trim()}`);
      });
    }

    expect(
      files.length,
      'the scan found no source files — the walk is broken, not the invariant satisfied',
    ).toBeGreaterThan(15);

    expect(
      offenders,
      'every one of these must go through messageOf(err, fallback) instead. messageOf shows ' +
        "an ApiError's own message (our server's prose) and the caller's fallback for " +
        'anything else — a raw Error#message puts "Failed to fetch" or a JSON parse offset ' +
        'in front of a human (final review F5). If a read here is genuinely legitimate, add ' +
        'it to this test with a reason rather than deleting the assertion:\n' +
        offenders.join('\n'),
    ).toEqual([]);
  });
});
