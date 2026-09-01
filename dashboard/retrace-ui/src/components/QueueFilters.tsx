import type { QueueFilter } from '../api/client';
import './QueueFilters.css';

function Chip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={`filter-chip${active ? ' filter-chip--active' : ''}`}
      aria-pressed={active}
      onClick={onClick}
    >
      {label}
    </button>
  );
}

/**
 * The queue filter bar: two chip groups whose selection is the QueueFilter
 * App holds in the URL. Selecting a chip that is already active clears it
 * (back to "all"), so the bar is a toggle rather than a mode you can get
 * stuck in — the "all" chip is the always-available escape either way.
 *
 * The app chips are DERIVED from `apps` — the distinct app keys actually
 * present in the current (unfiltered-by-app) queue response — rather than a
 * hardcoded list. An app key's own naming convention belongs to the
 * project's retrace config, not to this dashboard, so the only thing this
 * bar can safely offer is "whatever is actually here."
 */
export default function QueueFilters({
  apps,
  filter,
  onChange,
}: {
  apps: string[];
  filter: QueueFilter;
  onChange: (next: QueueFilter) => void;
}) {
  const setSource = (value: QueueFilter['source']) =>
    onChange({ ...filter, source: filter.source === value ? undefined : value });
  const setApp = (app: string) => onChange({ ...filter, app: filter.app === app ? undefined : app });

  return (
    <div className="queue-filters" role="group" aria-label="filter the review queue">
      <div className="queue-filters__group" role="group" aria-label="source">
        <span className="queue-filters__label">source</span>
        <Chip label="all" active={!filter.source} onClick={() => onChange({ ...filter, source: undefined })} />
        <Chip label="local" active={filter.source === 'local'} onClick={() => setSource('local')} />
        <Chip label="CI" active={filter.source === 'ci'} onClick={() => setSource('ci')} />
      </div>
      {apps.length > 1 ? (
        <div className="queue-filters__group" role="group" aria-label="app">
          <span className="queue-filters__label">app</span>
          <Chip label="all" active={!filter.app} onClick={() => onChange({ ...filter, app: undefined })} />
          {apps.map((app) => (
            <Chip key={app} label={app} active={filter.app === app} onClick={() => setApp(app)} />
          ))}
        </div>
      ) : null}
    </div>
  );
}
