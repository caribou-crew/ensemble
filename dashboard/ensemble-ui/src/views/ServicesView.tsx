// A flat, whole-stack view of every service: variant, native/container
// placement, ports, health, memory, and uptime, with row-level lifecycle
// controls (start/restart, stop, flip, change variant). TopologyView's
// ServicePanel already covers this per-node in a graph context; this view
// is the "just show me the list" counterpart the graph doesn't serve well
// once a stack has more than a handful of services.
import { useCallback, useEffect, useState } from 'react';
import { Badge, Spinner, Tooltip } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import type {
  FreshnessCheckResult,
  FreshnessState,
  GatewayStatus,
  ServiceState,
  Topology,
  TopologyNode,
  WiringWarning,
} from '../api/types';
import FreshnessDrawer from '../components/FreshnessDrawer';
import InlineError from '../components/InlineError';
import LogsDrawer from '../components/LogsDrawer';
import { usePendingRefresh } from '../usePendingRefresh';
import './ServicesView.css';

const POLL_MS = 5000;

function statusTone(status: string): 'green' | 'red' | 'amber' | 'neutral' {
  switch (status) {
    case 'healthy':
      return 'green';
    case 'unhealthy':
    case 'failed':
    case 'crashed':
      return 'red';
    case 'stopped':
    case 'exited':
      return 'neutral';
    default:
      return 'amber';
  }
}

/** Badge text for the status cell — the exited/crashed states carry how the process ended
    ("crashed (exit 1)"), so the supervision detail is visible without opening anything. */
function statusLabel(s: ServiceState): string {
  if (s.exitCode !== undefined) return `${s.status} (exit ${s.exitCode})`;
  if (s.signal) return `${s.status} (${s.signal})`;
  return s.status;
}

/** "service" (the default, unlabeled) is deliberately unremarkable; anything the user
    actually configured (`kind: stub`, `kind: mock`, ...) gets called out. */
function kindTone(kind: string | undefined): 'amber' | 'neutral' {
  return kind ? 'amber' : 'neutral';
}

/** Explains the amber-vs-gray kind badge on hover — the color alone doesn't say whether it
    means "this is fake" or just "this is labeled". */
function kindTooltip(kind: string | undefined): string {
  return kind
    ? `kind: ${kind}\n\nLabeled via this service's \`kind:\` config — a hint that it isn't a fully real backing (e.g. a stub or mock standing in for one).`
    : 'kind: service\n\nThe default, unlabeled kind — a real backing running its own code, not a stub or mock.';
}

const PLACEMENT_EXPLAIN: Record<string, string> = {
  native: 'native\n\nRuns as a directly supervised OS process on this machine.',
  docker: 'docker\n\nRuns as a docker container.',
  passthrough: 'passthrough\n\nNo local backing — requests forward straight to a real upstream.',
};

const STATUS_EXPLAIN: Record<string, string> = {
  healthy: 'Started and currently passing its health check.',
  unhealthy: 'Running, but its health check is currently failing.',
  starting: 'Process launched; waiting on its health check to pass.',
  building: "Running its configured `build:` step before starting.",
  stopped: 'Stopped by an explicit Stop — will not auto-restart.',
  failed: 'Never came up healthy — the start itself failed.',
  exited: 'Ran, then exited on its own with a clean (zero) exit code.',
  crashed: 'Ran, then exited on its own with a non-zero exit code or a signal.',
};

/** Status badge tooltip: the general meaning of the status word, plus lastErr (a crash's log
    tail) when there is one — so the reason is one hover away without opening the log drawer. */
function statusTooltip(s: ServiceState): string {
  const base = STATUS_EXPLAIN[s.status] ?? s.status;
  return s.lastErr ? `${base}\n\n${s.lastErr}` : base;
}

/** Hover card for a service's name: everything identifying, in one place, including the
    working directory, build/run commands, and resolved env — otherwise not shown
    anywhere in this table. Build/run matter beyond the obvious "how does this start":
    most services are `tools/services/scripts/*.sh` wrappers, and dir alone only names
    where the wrapper lives — the actual checkout it cds into (a different repo, in the
    common case) is named nowhere else in the dashboard. */
function nameTooltip(s: ServiceState): string {
  const envLines = s.env
    ? Object.entries(s.env)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([k, v]) => `  ${k}=${v}`)
    : [];
  return [
    s.variant ? `variant: ${s.variant}` : null,
    `kind: ${s.kind || 'service'}`,
    `placement: ${s.placement}`,
    s.dir ? `dir: ${s.dir}` : null,
    s.build ? `build: ${s.build}` : null,
    s.run ? `run: ${s.run}` : null,
    envLines.length ? ['env:', ...envLines].join('\n') : null,
  ]
    .filter((line): line is string => line !== null)
    .join('\n');
}

const WIRING_EXPLAIN =
  "Wiring warning\n\nAn env var on this service references another service's real port directly " +
  "instead of its proxy port — traffic sent there bypasses interception, so that hop won't be " +
  'captured for tracing/replay.';

function formatRSS(kb: number | undefined): string {
  if (!kb) return '—';
  if (kb < 1024) return `${kb} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  return `${(mb / 1024).toFixed(2)} GB`;
}

/** Renders one service's freshness state: nothing for a service that was never eligible
    (no `freshness:` configured, or ineligible Dir); an "unknown" badge for one that's never
    been successfully checked or whose last check failed (never a false "clean" or a false
    "behind" from a stale/failed read); amber badge(s) naming exactly which counts are
    nonzero; nothing (same as "never eligible") for a genuinely clean, checked service — so a
    row with no news to report stays visually quiet. */
function FreshnessCell({ freshness }: { freshness: FreshnessState | undefined }) {
  if (!freshness) {
    return <span className="services-table__dash">—</span>;
  }
  if (!freshness.checkedAt || freshness.error) {
    return (
      <Tooltip
        content={`unknown\n\n${freshness.error || 'This service has never been successfully checked for git freshness.'}`}
      >
        <Badge tone="neutral">unknown</Badge>
      </Tooltip>
    );
  }
  if (freshness.behindBranch === 0 && freshness.behindDefault === 0) {
    return <span className="services-table__dash">—</span>;
  }
  const checkedAt = `checked ${new Date(freshness.checkedAt).toLocaleString()}`;
  return (
    <span className="services-table__freshness">
      {freshness.behindBranch > 0 && (
        <Tooltip
          content={`${freshness.behindBranch} commit${freshness.behindBranch === 1 ? '' : 's'} behind this service's own remote branch (${freshness.branch})\n\n${checkedAt}`}
        >
          <Badge tone="amber">↓{freshness.behindBranch}</Badge>
        </Tooltip>
      )}
      {freshness.behindDefault > 0 && (
        <Tooltip
          content={`${freshness.behindDefault} commit${freshness.behindDefault === 1 ? '' : 's'} behind the configured default branch (${freshness.defaultBranch})\n\n${checkedAt}`}
        >
          <Badge tone="amber">
            {freshness.defaultBranch} ↓{freshness.behindDefault}
          </Badge>
        </Tooltip>
      )}
    </span>
  );
}

function formatUptime(startedAt: string | undefined): string {
  if (!startedAt) return '—';
  const ms = Date.now() - new Date(startedAt).getTime();
  if (ms < 0) return '—';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

type SortKey = 'name' | 'status' | 'placement' | 'kind' | 'variant' | 'port' | 'proxyPort' | 'rss' | 'uptime';
type SortDir = 'asc' | 'desc';
interface SortState {
  key: SortKey;
  dir: SortDir;
}

const COLUMNS: { key: SortKey; label: string; numeric?: boolean }[] = [
  { key: 'name', label: 'name' },
  { key: 'status', label: 'status' },
  { key: 'placement', label: 'placement' },
  { key: 'kind', label: 'kind' },
  { key: 'variant', label: 'variant' },
  { key: 'port', label: 'port', numeric: true },
  { key: 'proxyPort', label: 'proxy', numeric: true },
  { key: 'rss', label: 'rss', numeric: true },
  { key: 'uptime', label: 'uptime', numeric: true },
];

/** A missing value sorts as "least" in either direction, so e.g. a stopped service (no
    uptime/rss) always lands at the ends of the list rather than jumping around when the
    sort direction flips. */
function sortValue(s: ServiceState, key: SortKey): string | number {
  switch (key) {
    case 'name':
      return s.name;
    case 'status':
      return s.status;
    case 'placement':
      return s.placement;
    case 'kind':
      return s.kind || 'service';
    case 'variant':
      return s.variant ?? '';
    case 'port':
      return s.port ?? -1;
    case 'proxyPort':
      return s.proxyPort ?? -1;
    case 'rss':
      return s.rssKB ?? -1;
    case 'uptime':
      return s.startedAt ? new Date(s.startedAt).getTime() : -1;
  }
}

function sortServices(services: ServiceState[], sort: SortState | null): ServiceState[] {
  if (!sort) return services;
  const { key, dir } = sort;
  const sign = dir === 'asc' ? 1 : -1;
  return [...services].sort((a, b) => {
    const va = sortValue(a, key);
    const vb = sortValue(b, key);
    const cmp = typeof va === 'number' && typeof vb === 'number' ? va - vb : String(va).localeCompare(String(vb));
    return cmp * sign;
  });
}

interface ServicesSnapshot {
  services: ServiceState[];
  topology: Topology;
  warnings: WiringWarning[];
  gateways: GatewayStatus[];
}

/** refresh() runs both from the poll interval and out-of-band after a row mutation, so an
    older in-flight call resolving after a newer one must not clobber it — that generation
    guard is now useAsync's (keyed on `tick`), not a hand-rolled ref. `snapshot` exists only to
    keep the table on screen between polls: useAsync clears its data the instant `tick`
    changes (by design — see its own doc comment), which is right for "a different resource"
    but would otherwise flash the whole table back to a loading spinner every 5s. */
function useServicesPoll() {
  const [tick, setTick] = useState(0);
  const { data, error, loading } = useAsync<ServicesSnapshot>(async () => {
    const [s, t, w, g] = await Promise.all([
      api.status(true),
      api.topology(),
      api.wiringWarnings(),
      api.gatewayStatus(),
    ]);
    return { services: s, topology: t, warnings: w, gateways: g };
  }, [tick]);

  const [snapshot, setSnapshot] = useState<ServicesSnapshot | null>(null);
  useEffect(() => {
    if (data !== null) setSnapshot(data);
  }, [data]);

  // Sticky error, mirroring the sticky `snapshot` above: useAsync clears BOTH `data` and
  // `error` to null the instant a new poll starts (tick bumps), so reading `error` straight
  // off the hook flashed the "offline" banner back to the stale-but-good table for the
  // whole duration of every in-flight poll while the backend was down (final review F2).
  // Cleared only on an actual successful load, never merely because the next poll started —
  // matching pre-migration's `setError(null)` on the success path only.
  const [staleError, setStaleError] = useState<string | null>(null);
  useEffect(() => {
    if (error) setStaleError(messageOf(error, 'failed to reach the ensemble API'));
    else if (data !== null) setStaleError(null);
  }, [error, data]);

  useEffect(() => {
    const id = window.setInterval(() => setTick((t) => t + 1), POLL_MS);
    return () => window.clearInterval(id);
  }, []);

  // Bumping `tick` alone resolves as soon as the state update is scheduled, not once the
  // triggered reload actually lands — callers that `await refresh()` (below) need the
  // promise to settle only once the NEW data (or a new error) is actually on screen, or
  // their `busy` flag clears while the row still shows the pre-action state (final review
  // F7). Every ServiceRow owns its own `busy` and disables only its OWN buttons, so two
  // rows are concurrently actionable and two refreshes can be waiting at once — see
  // usePendingRefresh for why ALL of them are resolved rather than only the newest
  // (re-review N1).
  const bumpTick = useCallback(() => setTick((t) => t + 1), []);
  const refresh = usePendingRefresh(loading, bumpTick);

  return {
    services: snapshot?.services ?? null,
    topology: snapshot?.topology ?? null,
    warnings: snapshot?.warnings ?? [],
    gateways: snapshot?.gateways ?? [],
    error: staleError,
    refresh,
  };
}

type Action = 'start' | 'restart' | 'stop' | 'flip' | 'variant';

/** Flip's control shape depends on how many placements a service declares: nothing to flip
    to (0 or 1), a single "Flip to X" button (exactly 2 — the original native/docker case,
    generalized to whichever two placements are declared), or a target-picking select (3,
    once passthrough joins native/docker) since "the other one" stops being well-defined. */
function FlipControl({
  placement,
  placements,
  busy,
  disabled,
  onFlip,
  ariaLabel,
}: {
  placement: string;
  placements: string[];
  busy: boolean;
  disabled: boolean;
  onFlip: (target: string) => void;
  ariaLabel?: string;
}) {
  const others = placements.filter((p) => p !== placement);
  if (others.length === 0) {
    return <span className="services-table__dash">—</span>;
  }
  if (others.length === 1) {
    return (
      <button type="button" aria-label={ariaLabel} disabled={disabled} onClick={() => onFlip(others[0])}>
        {busy ? <Spinner /> : `Flip to ${others[0]}`}
      </button>
    );
  }
  return (
    <select
      aria-label={ariaLabel}
      value=""
      disabled={disabled}
      onChange={(e) => {
        if (e.target.value) onFlip(e.target.value);
      }}
    >
      <option value="">{busy ? 'Flipping…' : 'Flip to…'}</option>
      {others.map((p) => (
        <option key={p} value={p}>
          {p}
        </option>
      ))}
    </select>
  );
}

function ServiceRow({
  state,
  variants,
  placements,
  warnings,
  onAction,
  onOpenLogs,
}: {
  state: ServiceState;
  variants: string[];
  placements: string[];
  warnings: WiringWarning[];
  onAction: (action: Action, extra?: string) => Promise<void>;
  onOpenLogs: (name: string) => void;
}) {
  const [busy, setBusy] = useState<Action | null>(null);
  const [rowError, setRowError] = useState<string | null>(null);

  async function run(action: Action, extra?: string) {
    setBusy(action);
    setRowError(null);
    try {
      await onAction(action, extra);
    } catch (err) {
      setRowError(messageOf(err, `${action} failed`));
    } finally {
      setBusy(null);
    }
  }

  // exited/crashed are "not running" the same way stopped/failed are — the process is gone
  // and the only lifecycle action that makes sense is a fresh start.
  const stopped = ['stopped', 'failed', 'exited', 'crashed'].includes(state.status);

  return (
    <tr className="services-table__row">
      <td className="services-table__name">
        <Tooltip content={nameTooltip(state)} side="right">
          <span className="services-table__name-label">{state.name}</span>
        </Tooltip>
        {warnings.length > 0 && (
          <span className="services-table__wiring-warning">
            <Tooltip content={`${WIRING_EXPLAIN}\n\n${warnings.map((w) => w.message).join('\n')}`}>
              <Badge tone="red">wiring</Badge>
            </Tooltip>
          </span>
        )}
      </td>
      <td>
        <Tooltip content={statusTooltip(state)}>
          <Badge tone={statusTone(state.status)}>{statusLabel(state)}</Badge>
        </Tooltip>
      </td>
      <td>
        <Tooltip content={PLACEMENT_EXPLAIN[state.placement]}>
          <Badge tone="neutral">{state.placement}</Badge>
        </Tooltip>
      </td>
      <td>
        <Tooltip content={kindTooltip(state.kind)}>
          <Badge tone={kindTone(state.kind)}>{state.kind || 'service'}</Badge>
        </Tooltip>
      </td>
      <td className="services-table__variant">
        {variants.length > 0 ? (
          <select
            value={state.variant ?? ''}
            disabled={busy !== null}
            onChange={(e) => void run('variant', e.target.value)}
          >
            {variants.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        ) : (
          <span className="services-table__dash">—</span>
        )}
      </td>
      <td className="services-table__num">{state.port ?? '—'}</td>
      <td className="services-table__num">{state.proxyPort ?? '—'}</td>
      <td className="services-table__num">{formatRSS(state.rssKB)}</td>
      <td className="services-table__num">{formatUptime(state.startedAt)}</td>
      <td>
        <FreshnessCell freshness={state.freshness} />
      </td>
      <td className="services-table__actions">
        {stopped ? (
          <button type="button" disabled={busy !== null} onClick={() => void run('start')}>
            {busy === 'start' ? <Spinner /> : 'Start'}
          </button>
        ) : (
          <>
            <button type="button" disabled={busy !== null} onClick={() => void run('restart')}>
              {busy === 'restart' ? <Spinner /> : 'Restart'}
            </button>
            <button type="button" disabled={busy !== null} onClick={() => void run('stop')}>
              {busy === 'stop' ? <Spinner /> : 'Stop'}
            </button>
          </>
        )}
        <FlipControl
          placement={state.placement}
          placements={placements}
          busy={busy === 'flip'}
          disabled={busy !== null}
          onFlip={(target) => void run('flip', target)}
        />
        <button type="button" onClick={() => onOpenLogs(state.name)}>
          Logs
        </button>
        {rowError && <InlineError message={rowError} className="services-table__row-error" />}
      </td>
    </tr>
  );
}

/** A cfg.Stubs entry, rendered from Topology's existing "stub" category — stubs never get a
    ServiceState (they aren't orchestrator-supervised lifecycle nodes the way services are),
    so this row has no placement/variant/port/rss/uptime/actions, just a name and status. */
function StubRow({ node }: { node: TopologyNode }) {
  return (
    <tr className="services-table__row">
      <td className="services-table__name">{node.name}</td>
      <td>
        <Badge tone={statusTone(node.status)}>{node.status}</Badge>
      </td>
      <td>
        <span className="services-table__dash">—</span>
      </td>
      <td>
        <Tooltip content={"stub\n\nA `stubs:` entry — a canned/mocked responder, not an orchestrator-supervised service. No lifecycle actions, ports, or variants of its own."}>
          <Badge tone="amber">stub</Badge>
        </Tooltip>
      </td>
      <td className="services-table__variant">
        <span className="services-table__dash">—</span>
      </td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td>
        <span className="services-table__dash">—</span>
      </td>
      <td className="services-table__actions" />
    </tr>
  );
}

/** A cfg.Gateways entry, rendered from Topology's existing "gateway" category — gateways are
    static listeners the proxy binds at Up, not orchestrator-supervised nodes, so like stubs
    they never get a ServiceState. Unlike a stub, a gateway CAN carry an action: flipping
    between routing locally and forwarding to one of its declared upstreams (FlipGateway),
    reusing the same FlipControl a 3-placement service uses. */
function GatewayRow({
  node,
  activeTarget,
  onFlip,
}: {
  node: TopologyNode;
  activeTarget: string;
  onFlip: (target: string) => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [rowError, setRowError] = useState<string | null>(null);

  async function handleFlip(target: string) {
    setBusy(true);
    setRowError(null);
    try {
      await onFlip(target);
    } catch (err) {
      setRowError(messageOf(err, 'flip failed'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <tr className="services-table__row">
      <td className="services-table__name">{node.name}</td>
      <td>
        <Badge tone={statusTone(node.status)}>{node.status}</Badge>
      </td>
      <td>
        <Tooltip content={"Current flip target\n\n\"local\" routes to this gateway's own local handling; anything else names one of its declared upstreams."}>
          <Badge tone="neutral">{activeTarget}</Badge>
        </Tooltip>
      </td>
      <td>
        <Tooltip content={"gateway\n\nA `gateways:` entry — a static proxy listener that routes locally or forwards to a declared upstream (flip below). Not an orchestrator-supervised service."}>
          <Badge tone="amber">gateway</Badge>
        </Tooltip>
      </td>
      <td className="services-table__variant">
        <span className="services-table__dash">—</span>
      </td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td className="services-table__num">—</td>
      <td>
        <span className="services-table__dash">—</span>
      </td>
      <td className="services-table__actions">
        <FlipControl
          placement={activeTarget}
          placements={['local', ...(node.upstreams ?? [])]}
          busy={busy}
          disabled={busy}
          onFlip={(target) => void handleFlip(target)}
          ariaLabel={`Flip gateway ${node.name}`}
        />
        {rowError && <InlineError message={rowError} className="services-table__row-error" />}
      </td>
    </tr>
  );
}

export default function ServicesView() {
  const { services, topology, warnings, gateways, error, refresh } = useServicesPoll();
  const [sort, setSort] = useState<SortState | null>(null);
  const [checkingFreshness, setCheckingFreshness] = useState(false);
  const [freshnessError, setFreshnessError] = useState<string | null>(null);
  const [freshnessResult, setFreshnessResult] = useState<FreshnessCheckResult | null>(null);
  const [logsFor, setLogsFor] = useState<string | null>(null);

  async function handleFreshnessCheck() {
    setCheckingFreshness(true);
    setFreshnessError(null);
    try {
      const result = await api.freshnessCheck();
      setFreshnessResult(result);
      await refresh();
    } catch (err) {
      setFreshnessError(messageOf(err, 'freshness check failed'));
    } finally {
      setCheckingFreshness(false);
    }
  }

  const toggleSort = useCallback((key: SortKey) => {
    setSort((prev) =>
      prev?.key === key ? { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: 'asc' },
    );
  }, []);

  async function handleAction(name: string, action: Action, extra?: string) {
    switch (action) {
      case 'start':
      case 'restart':
        await api.restart(name);
        break;
      case 'stop':
        await api.stop(name);
        break;
      case 'flip':
        await api.flip(name, extra as 'native' | 'docker' | 'passthrough' | undefined);
        break;
      case 'variant':
        if (extra !== undefined) await api.setVariant(name, extra);
        break;
    }
    await refresh();
  }

  async function handleGatewayFlip(name: string, target: string) {
    await api.flipGateway(name, target);
    await refresh();
  }

  if (error) {
    return (
      <div className="services-view services-view--error">
        <Badge tone="red">offline</Badge>
        <span>{error}</span>
      </div>
    );
  }

  if (!services) {
    return (
      <div className="services-view services-view--loading">
        <Spinner />
        <span>loading services…</span>
      </div>
    );
  }

  const variantsByName = new Map(
    (topology?.nodes ?? []).map((n) => [n.name, n.variants ?? []]),
  );
  const placementsByName = new Map(
    (topology?.nodes ?? []).map((n) => [n.name, n.placements ?? []]),
  );
  const warningsByService = new Map<string, WiringWarning[]>();
  for (const w of warnings) {
    const list = warningsByService.get(w.service);
    if (list) list.push(w);
    else warningsByService.set(w.service, [w]);
  }
  const sorted = sortServices(services, sort);
  const stubNodes = (topology?.nodes ?? [])
    .filter((n) => n.category === 'stub')
    .sort((a, b) => a.name.localeCompare(b.name));
  const gatewayNodes = (topology?.nodes ?? [])
    .filter((n) => n.category === 'gateway')
    .sort((a, b) => a.name.localeCompare(b.name));
  const gatewayTargetByName = new Map(gateways.map((g) => [g.name, g.activeTarget]));

  return (
    <div className="services-view">
      <div className="services-view__toolbar">
        <Tooltip
          side="bottom"
          content={
            "Runs `git fetch` for every service whose `dir:` is its own separate git repository " +
            "(distinct from the repo containing ensemble.yaml), then compares HEAD to its own " +
            "remote branch and to the configured default branch. Results open in a drawer here " +
            "— a no-op if `freshness:` isn't configured in ensemble.yaml."
          }
        >
          <button
            type="button"
            className="services-view__freshness-btn"
            disabled={checkingFreshness}
            onClick={() => void handleFreshnessCheck()}
          >
            {checkingFreshness ? (
              <Spinner />
            ) : (
              <svg
                className="services-view__freshness-icon"
                viewBox="0 0 16 16"
                fill="none"
                aria-hidden="true"
              >
                <path
                  d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9M13.5 2.3v3.2h-3.2"
                  stroke="currentColor"
                  strokeWidth="1.4"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              </svg>
            )}
            Check freshness
          </button>
        </Tooltip>
        {freshnessError && <InlineError message={freshnessError} />}
      </div>
      <table className="services-table">
        <thead>
          <tr>
            {COLUMNS.map((col) => (
              <th
                key={col.key}
                className={col.numeric ? 'services-table__num' : undefined}
                aria-sort={sort?.key === col.key ? (sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}
              >
                <button type="button" className="services-table__sort" onClick={() => toggleSort(col.key)}>
                  {col.label}
                  <span className="services-table__sort-indicator">
                    {sort?.key === col.key ? (sort.dir === 'asc' ? '▲' : '▼') : ''}
                  </span>
                </button>
              </th>
            ))}
            <th>freshness</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {sorted.length === 0 && stubNodes.length === 0 && gatewayNodes.length === 0 && (
            <tr>
              <td colSpan={11} className="services-table__empty">
                no services configured
              </td>
            </tr>
          )}
          {sorted.map((s) => (
            <ServiceRow
              key={s.name}
              state={s}
              variants={variantsByName.get(s.name) ?? []}
              placements={placementsByName.get(s.name) ?? []}
              warnings={warningsByService.get(s.name) ?? []}
              onAction={(action, extra) => handleAction(s.name, action, extra)}
              onOpenLogs={setLogsFor}
            />
          ))}
          {stubNodes.map((n) => (
            <StubRow key={n.name} node={n} />
          ))}
          {gatewayNodes.map((n) => (
            <GatewayRow
              key={n.name}
              node={n}
              activeTarget={gatewayTargetByName.get(n.name) ?? 'local'}
              onFlip={(target) => handleGatewayFlip(n.name, target)}
            />
          ))}
        </tbody>
      </table>
      <LogsDrawer name={logsFor} onClose={() => setLogsFor(null)} />
      <FreshnessDrawer result={freshnessResult} onClose={() => setFreshnessResult(null)} />
    </div>
  );
}
