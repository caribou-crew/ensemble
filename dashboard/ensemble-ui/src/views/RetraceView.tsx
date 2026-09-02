// Cross-app retrace review queue, embedded directly from retrace/serve (no
// separate `retrace serve` process — see openspec/changes/retrace-ci-sync).
// A thin navigation shell over the same shared components retrace-ui's own
// App.tsx uses (@ensemble/design-system/components/Retrace*) — this used to
// be a hand-duplicated queue table + detail pane that drifted from
// retrace-ui's own screens every time one of them changed; now both apps
// render the exact same components, parameterized only by basePath
// ("/api/retrace" here vs retrace-ui's "/api"). No keyboard dispatch and no
// accept/reject/rule/redact verbs here — this dashboard is read+sync only.
import { useMemo, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import RetraceQueueList from '@ensemble/design-system/components/RetraceQueueList';
import RetraceQueueFilters from '@ensemble/design-system/components/RetraceQueueFilters';
import RetraceRunsList from '@ensemble/design-system/components/RetraceRunsList';
import RetraceBreadcrumb from '@ensemble/design-system/components/RetraceBreadcrumb';
import RetraceItemScreen from '@ensemble/design-system/components/RetraceItemScreen';
import RetraceSyncPanel from '@ensemble/design-system/components/RetraceSyncPanel';
import {
  createRetraceClient,
  listRetraceInstances,
  retraceMessageOf,
  type QueueFilter,
} from '@ensemble/design-system/retraceClient';
import { formatWhen } from '@ensemble/design-system/retraceWhen';
import { useUrlParam } from '../urlState';
import './RetraceView.css';

const RETRACE_BASE_PATH = '/api/retrace';

function Problem({ message }: { message: string }) {
  return (
    <div className="problem">
      <Badge tone="red">error</Badge>
      <span>{message}</span>
    </div>
  );
}

// InstancePicker is what a multi-repo ensemble.yaml (retrace.instances with
// more than one entry) shows in place of the queue until a repo is chosen —
// single-instance configs (today's norm) never render this at all, so the
// no-picker path stays byte-for-byte what it was before instance support
// existed. See ensemble/config.RetraceConfig.Instances' own doc comment.
function InstancePicker({
  instances,
  onSelect,
}: {
  instances: { key: string; label: string }[];
  onSelect: (key: string) => void;
}) {
  return (
    <div className="retrace-view__instances">
      <p>Choose a repo to review:</p>
      <ul className="retrace-view__instance-list">
        {instances.map((inst) => (
          <li key={inst.key}>
            <button type="button" onClick={() => onSelect(inst.key)}>
              {inst.label}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

export default function RetraceView() {
  const [app, setApp] = useUrlParam('app');
  const [flow, setFlow] = useUrlParam('flow');
  // The open RUN, if any — same three-level model as retrace-ui: none ->
  // queue, app+flow -> that surface's runs list, app+flow+run -> that run's
  // own detail.
  const [run, setRun] = useUrlParam('run');
  const [version, setVersion] = useState(0);
  const [showPassing, setShowPassing] = useState(false);
  const [showSyncPanel, setShowSyncPanel] = useState(false);

  // Which repo (ensemble.yaml's retrace.instances key) is selected, when
  // there's more than one to choose from — see instancesQuery below.
  const [instanceParam, setInstanceParam] = useUrlParam('instance');

  const instancesQuery = useAsync(() => listRetraceInstances(RETRACE_BASE_PATH), []);
  const instances = instancesQuery.data?.instances ?? [];
  // More than one configured instance is the ONLY case that needs a picker
  // at all — zero or one behaves exactly as it did before instance support
  // existed (no ?instance= param on any request, same URLs as always).
  const needsPicker = instances.length > 1;
  const selectedInstance = needsPicker
    ? instances.some((i) => i.key === instanceParam) ? instanceParam : null
    : undefined;

  // Recreated only when the selected instance changes — useAsync's (and the
  // shared components' own) deps arrays close over this by identity, so a
  // value recreated on every render would refetch on every render too. See
  // retrace-ui's App.tsx for the single-instance sibling of this client,
  // pointed at "/api" with no instance concept at all.
  const client = useMemo(
    () => createRetraceClient(RETRACE_BASE_PATH, selectedInstance ?? undefined),
    [selectedInstance],
  );

  const level: 'queue' | 'surface' | 'run' = app && flow && run ? 'run' : app && flow ? 'surface' : 'queue';

  const [sourceParam, setSourceParam] = useUrlParam('source');
  const [appParam, setAppParam] = useUrlParam('buildApp');
  const filter: QueueFilter = {
    source: sourceParam === 'local' || sourceParam === 'ci' ? sourceParam : undefined,
    app: appParam ?? undefined,
  };
  const setFilter = (next: QueueFilter) => {
    setSourceParam(next.source ?? null);
    setAppParam(next.app ?? null);
  };

  // Gated on !needsPicker || selectedInstance: with multiple instances and
  // none chosen yet, `client` carries no ?instance= and the server would
  // answer every one of these with 400 (ambiguous) — so nothing here fires
  // until InstancePicker has resolved that below.
  const ready = !needsPicker || !!selectedInstance;
  const queue = useAsync(
    () => (ready ? client.queue(filter) : Promise.resolve(null)),
    [client, ready, version, filter.source, filter.app],
  );
  const items = queue.data?.items ?? [];
  // The app chips need the FULL set of apps for the current source, not the
  // already app-filtered `items` — see retrace-ui's App.tsx for the same
  // reasoning.
  const appsForChips = useAsync(
    () => (ready && filter.app ? client.queue({ source: filter.source }) : Promise.resolve(null)),
    [client, ready, version, filter.source, filter.app],
  );
  const apps = Array.from(new Set((filter.app ? appsForChips.data?.items : items)?.map((i) => i.app) ?? [])).sort();

  const item = useAsync(async () => {
    if (!ready || level !== 'run' || !app || !flow || !run) return null;
    return (await client.itemAtRun(app, flow, run)).summary;
  }, [client, ready, level, app, flow, run, version]);

  const openSurface = (next: { app: string; flow: string }) => {
    setApp(next.app);
    setFlow(next.flow);
    setRun(null);
  };
  const openRun = (runId: string) => setRun(runId);
  const backToQueue = () => {
    setApp(null);
    setFlow(null);
    setRun(null);
  };
  const backToSurface = () => setRun(null);

  const selectInstance = (key: string) => {
    setInstanceParam(key);
    setApp(null);
    setFlow(null);
    setRun(null);
    setVersion((v) => v + 1);
  };
  const switchInstance = () => {
    setInstanceParam(null);
    setApp(null);
    setFlow(null);
    setRun(null);
  };

  if (instancesQuery.loading) {
    return (
      <div className="retrace-view">
        <p className="loading">
          <Spinner /> loading configured repos…
        </p>
      </div>
    );
  }
  if (instancesQuery.error) {
    return (
      <div className="retrace-view">
        <Problem message={retraceMessageOf(instancesQuery.error, 'failed to load configured repos')} />
      </div>
    );
  }
  if (needsPicker && !selectedInstance) {
    return (
      <div className="retrace-view">
        <InstancePicker instances={instances} onSelect={selectInstance} />
      </div>
    );
  }

  return (
    <div className="retrace-view">
      <div className="retrace-view__toolbar">
        {needsPicker ? (
          <span className="retrace-view__current-instance">
            {instances.find((i) => i.key === selectedInstance)?.label ?? selectedInstance}
            <button type="button" onClick={switchInstance}>
              switch repo
            </button>
          </span>
        ) : null}
        <button type="button" onClick={() => setShowSyncPanel(true)}>
          Browse &amp; sync…
        </button>
      </div>

      {level !== 'queue' ? (
        <RetraceBreadcrumb
          app={app}
          flow={flow}
          runLabel={
            level === 'run' && item.data ? formatWhen(item.data.b.manifest?.finishedAt, item.data.b.runId) : null
          }
          onQueue={backToQueue}
          onSurface={backToSurface}
        />
      ) : null}

      <div className="retrace-view__body">
        {level === 'run' ? (
          item.loading ? (
            <p className="loading">
              <Spinner /> loading {app}/{flow}…
            </p>
          ) : item.error ? (
            <Problem message={retraceMessageOf(item.error, 'failed to load flow detail')} />
          ) : item.data && app && flow && run ? (
            <RetraceItemScreen
              key={`${app}/${flow}/${run}`}
              client={client}
              app={app}
              flow={flow}
              summary={item.data}
              selectedField={null}
              onSelectField={() => {}}
              resolveShotUrl={(a, f, side, name) =>
                client.shotUrlAtRun(a, f, run, side as 'a' | 'b' | 'diff' | 'overlay', name)
              }
              onReveal={() => client.itemAtRun(app, flow, run).then((r) => r.summary.sections)}
              onBack={backToSurface}
            />
          ) : null
        ) : level === 'surface' && app && flow ? (
          <RetraceRunsList client={client} app={app} flow={flow} selectedRun={run} onOpenRun={openRun} />
        ) : queue.loading ? (
          <p className="loading">
            <Spinner /> loading the review queue…
          </p>
        ) : queue.error ? (
          <Problem message={retraceMessageOf(queue.error, 'failed to reach the ensemble API')} />
        ) : queue.data ? (
          <>
            <RetraceQueueFilters apps={apps} filter={filter} onChange={setFilter} />
            <RetraceQueueList
              items={items}
              empty={queue.data.empty}
              selected={null}
              showPassing={showPassing}
              onShowPassingChange={setShowPassing}
              onOpen={openSurface}
            />
          </>
        ) : null}
      </div>

      {showSyncPanel ? (
        <RetraceSyncPanel
          client={client}
          requireRepo={false}
          onClose={() => setShowSyncPanel(false)}
          onSynced={() => setVersion((v) => v + 1)}
        />
      ) : null}
    </div>
  );
}
