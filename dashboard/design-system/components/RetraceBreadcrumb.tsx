import './RetraceBreadcrumb.css';

/**
 * The trail: <rootLabel> / <app/flow> / <run>. Every segment EXCEPT the
 * current one is a link that navigates up to that level — inline
 * back/forward without a separate button. The last segment is the page you
 * are on and is rendered as plain text.
 *
 * Levels are derived from which values are present: no app -> root level;
 * app+flow, no run -> surface level; app+flow+run -> run level. Meant to sit
 * in a persistent header, so it always renders, including at the root level
 * — the "← Back" control only appears once there's somewhere to go back to.
 */
export default function RetraceBreadcrumb({
  app,
  flow,
  runLabel,
  onQueue,
  onSurface,
  rootLabel = 'queue',
}: {
  app: string | null;
  flow: string | null;
  /** A readable label for the open run (its timestamp), or null before a run is open. */
  runLabel: string | null;
  onQueue: () => void;
  onSurface: () => void;
  /** Label for the root segment. Defaults to "queue". */
  rootLabel?: string;
}) {
  const atSurface = Boolean(app && flow && !runLabel);
  const atRun = Boolean(app && flow && runLabel);

  // Where "up one level" goes: a run steps back to its surface's runs list, a
  // surface steps back to the queue.
  const onBack = atRun ? onSurface : onQueue;

  return (
    <div className="breadcrumb-bar">
      {atSurface || atRun ? (
        <button type="button" className="breadcrumb__back" onClick={onBack}>
          ← Back
        </button>
      ) : null}
      <nav className="breadcrumb" aria-label="breadcrumb">
        {atSurface || atRun ? (
          <button type="button" className="breadcrumb__link" onClick={onQueue}>
            {rootLabel}
          </button>
        ) : (
          <span className="breadcrumb__current">{rootLabel}</span>
        )}

        {app && flow ? (
          <>
            <span className="breadcrumb__sep">/</span>
            {atRun ? (
              <button type="button" className="breadcrumb__link" onClick={onSurface}>
                {app}/{flow}
              </button>
            ) : (
              <span className="breadcrumb__current">
                {app}/{flow}
              </span>
            )}
          </>
        ) : null}

        {atRun ? (
          <>
            <span className="breadcrumb__sep">/</span>
            <span className="breadcrumb__current">{runLabel}</span>
          </>
        ) : null}
      </nav>
    </div>
  );
}
