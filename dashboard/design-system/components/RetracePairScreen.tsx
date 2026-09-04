import { Spinner } from '../primitives';
import { useAsync } from '../useAsync';
import type { RetraceClient } from '../retraceClient';
import RetraceItemScreen from './RetraceItemScreen';

/**
 * The cross-app compare detail view: fetches one persisted pairing
 * (retrace/pairs) and renders it through the SAME RetraceItemScreen the
 * same-app queue uses — a cross-app Summary is the identical shape, just
 * computed and persisted by the CLI rather than by this server. Read-only:
 * no accept/reject/rule/redact wiring, because those verbs mutate a
 * reference this view has no reference to promote or reject.
 */
export default function RetracePairScreen({
  client,
  appB,
  flowB,
  runB,
  pairId,
  onBack,
}: {
  client: RetraceClient;
  appB: string;
  flowB: string;
  runB: string;
  pairId: string;
  onBack?: () => void;
}) {
  const { data, loading, error } = useAsync(
    () => client.pair(appB, flowB, runB, pairId).then((r) => r.summary),
    [client, appB, flowB, runB, pairId],
  );

  if (loading) {
    return (
      <p className="loading">
        <Spinner /> loading {appB}/{flowB}…
      </p>
    );
  }
  if (error) {
    return <p className="pairs__error">{error.message}</p>;
  }
  if (!data) return null;

  return (
    <RetraceItemScreen
      client={client}
      app={`${data.a.manifest.app || '?'} → ${data.b.manifest.app || '?'}`}
      flow={flowB}
      summary={data}
      selectedField={null}
      onSelectField={() => {}}
      resolveShotUrl={(_a, _f, side, name) =>
        client.pairShotUrl(appB, flowB, runB, pairId, side as 'a' | 'b' | 'diff' | 'overlay', name)
      }
      onReveal={() => client.pair(appB, flowB, runB, pairId).then((r) => r.summary.sections)}
      onBack={onBack}
    />
  );
}
