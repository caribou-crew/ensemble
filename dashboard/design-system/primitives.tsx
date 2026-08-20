import type { ReactNode } from 'react';
import './primitives.css';

/** Tone maps 1:1 onto the design tokens' status/accent colors. */
export type BadgeTone = 'neutral' | 'accent' | 'green' | 'amber' | 'red' | 'blue';

export function Badge({ tone = 'neutral', children }: { tone?: BadgeTone; children: ReactNode }) {
  return <span className={`ds-badge ds-badge--${tone}`}>{children}</span>;
}

export interface TabItem {
  id: string;
  label: string;
}

export function Tabs({
  items,
  active,
  onSelect,
}: {
  items: TabItem[];
  active: string;
  onSelect: (id: string) => void;
}) {
  return (
    <div className="ds-tabs" role="tablist">
      {items.map((item) => (
        <button
          key={item.id}
          type="button"
          role="tab"
          aria-selected={item.id === active}
          className="ds-tabs__tab"
          onClick={() => onSelect(item.id)}
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}

export function Spinner() {
  return <span className="ds-spinner" role="status" aria-label="Loading" />;
}
