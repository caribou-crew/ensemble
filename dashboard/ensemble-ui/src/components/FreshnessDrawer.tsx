// Right-docked drawer showing what a "Check freshness" click actually did — the button gave
// no visible feedback before this (a spinner, then silence), which looked identical whether
// freshness: wasn't configured at all, no service was eligible, or everything was already
// up to date. Built on the same Drawer shell as LogsDrawer/TraceDrawer.
import Drawer from './Drawer';
import type { FreshnessCheckResult } from '../api/types';
import './FreshnessDrawer.css';

function summarize({ services, configured }: FreshnessCheckResult): string {
  if (!configured) {
    return [
      "Freshness checking isn't configured for this stack — nothing was checked.",
      '',
      'Add a top-level `freshness:` block to ensemble.yaml to enable it, e.g.:',
      '',
      'freshness:',
      '  default_branch: main',
    ].join('\n');
  }

  const eligible = services.filter((s) => s.freshness);
  const skipped = services.filter((s) => !s.freshness);
  const lines: string[] = [];

  if (eligible.length === 0) {
    lines.push('No eligible services — nothing was checked.');
    lines.push('');
    lines.push(
      "A service is only eligible once its `dir:` is its own separate git repository, " +
        "distinct from the repository containing ensemble.yaml.",
    );
  } else {
    const errored = eligible.filter((s) => s.freshness?.error);
    const behind = eligible.filter(
      (s) =>
        !s.freshness?.error &&
        ((s.freshness?.behindBranch ?? 0) > 0 || (s.freshness?.behindDefault ?? 0) > 0),
    );
    const upToDate = eligible.length - errored.length - behind.length;

    lines.push(
      `Checked ${eligible.length} eligible service${eligible.length === 1 ? '' : 's'}: ` +
        `${upToDate} up to date, ${behind.length} behind, ${errored.length} failed.`,
    );
    lines.push('');
    for (const s of eligible) {
      const f = s.freshness!;
      if (f.error) {
        lines.push(`${s.name}: FAILED — ${f.error}`);
      } else if ((f.behindBranch ?? 0) === 0 && (f.behindDefault ?? 0) === 0) {
        lines.push(`${s.name}: up to date (${f.branch})`);
      } else {
        lines.push(
          `${s.name}: ${f.behindBranch} behind its own remote branch (${f.branch}), ` +
            `${f.behindDefault} behind ${f.defaultBranch}`,
        );
      }
    }
  }

  if (skipped.length > 0) {
    lines.push('');
    lines.push(
      `${skipped.length} service${skipped.length === 1 ? '' : 's'} not eligible ` +
        `(not its own separate git repo from ensemble.yaml's): ${skipped.map((s) => s.name).join(', ')}`,
    );
  }

  return lines.join('\n');
}

export default function FreshnessDrawer({
  result,
  onClose,
}: {
  result: FreshnessCheckResult | null;
  onClose: () => void;
}) {
  return (
    <Drawer
      open={result !== null}
      onClose={onClose}
      classPrefix="freshness-drawer"
      ariaLabel="freshness check results"
      header={<span className="freshness-drawer__title">Freshness check</span>}
    >
      {result && <pre className="freshness-drawer__body">{summarize(result)}</pre>}
    </Drawer>
  );
}
