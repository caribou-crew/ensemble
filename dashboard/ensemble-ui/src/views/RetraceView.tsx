import { Spinner } from '@ensemble/design-system';
import { createRetraceClient, retraceMessageOf } from '@ensemble/design-system/retraceClient';
import { useAsync } from '@ensemble/design-system/useAsync';
import RetraceSuites, { SuiteRunEvidenceNotice } from '@ensemble/design-system/components/RetraceSuites';
import RetraceItemScreen from '@ensemble/design-system/components/RetraceItemScreen';
import RetracePairScreen from '@ensemble/design-system/components/RetracePairScreen';
import type { SuiteEvidence, SuiteSelection } from '@ensemble/design-system/suiteTypes';
import { useUrlParam } from '../urlState';
import './RetraceView.css';

const client = createRetraceClient('/api/retrace');

export default function RetraceView() {
  const [suiteId, setSuiteId] = useUrlParam('suite');
  const [buildId, setBuildId] = useUrlParam('suiteBuild');
  const [featureId, setFeatureId] = useUrlParam('suiteFeature');
  const [platform, setPlatform] = useUrlParam('suitePlatform');
  const [app, setApp] = useUrlParam('retraceApp');
  const [flow, setFlow] = useUrlParam('retraceFlow');
  const [runId, setRunId] = useUrlParam('retraceRun');
  const [pairId, setPairId] = useUrlParam('retracePair');
  const selection: SuiteSelection = {
    suiteId: suiteId ?? undefined,
    buildId: buildId ?? undefined,
    featureId: featureId ?? undefined,
    platform: platform === 'web' || platform === 'ios' || platform === 'android' ? platform : undefined,
  };
  const select = (next: SuiteSelection) => {
    setSuiteId(next.suiteId ?? null);
    setBuildId(next.buildId ?? null);
    setFeatureId(next.featureId ?? null);
    setPlatform(next.platform ?? null);
  };
  const openEvidence = (e: SuiteEvidence) => {
    setApp(e.app);
    setFlow(e.flow);
    setRunId(e.runId);
    setPairId(e.pairId ?? null);
  };
  const back = () => {
    setApp(null);
    setFlow(null);
    setRunId(null);
    setPairId(null);
  };
  const run = useAsync(
    () => app && flow && runId && !pairId ? client.itemAtRun(app, flow, runId, true) : Promise.resolve(null),
    [client, app, flow, runId, pairId],
  );

  if (app && flow && runId) {
    return (
      <div className="retrace-view__body">
        <div className="retrace-view__toolbar">
          <button type="button" onClick={back}>← Comparison suites</button>
        </div>
        {!pairId ? <SuiteRunEvidenceNotice /> : null}
        {pairId ? (
          <RetracePairScreen
            showLatestEvidence={false}
            client={client}
            appB={app}
            flowB={flow}
            runB={runId}
            pairId={pairId}
            backLabel="comparison suites"
            onBack={back}
          />
        ) : run.loading ? (
          <p><Spinner /> Loading run evidence…</p>
        ) : run.error ? (
          <p role="alert">{retraceMessageOf(run.error, 'Unable to load linked run evidence')}</p>
        ) : run.data ? (
          <RetraceItemScreen
            showLatestEvidence={false}
            client={client}
            app={app}
            flow={flow}
            summary={run.data.summary}
            selectedField={null}
            onSelectField={() => {}}
            onBack={back}
            backLabel="comparison suites"
            resolveShotUrl={(a, f, side, name) =>
              client.shotUrlAtRun(a, f, runId, side as 'a' | 'b' | 'diff' | 'overlay', name, true)
            }
            onReveal={() => client.itemAtRun(app, flow, runId, true).then(r => r.summary.sections)}
          />
        ) : null}
      </div>
    );
  }
  return (
    <div className="retrace-view">
      <RetraceSuites client={client} selection={selection} onSelect={select} onOpenEvidence={openEvidence} />
    </div>
  );
}
