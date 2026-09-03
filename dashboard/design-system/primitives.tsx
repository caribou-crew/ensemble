import { useState, type ReactNode } from 'react';
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

/** Hover/focus popover, CSS-positioned (no portal, no measurement) — the trigger and bubble
 * share one relatively-positioned wrapper, so it only works well for triggers that aren't
 * themselves near a scroll/clip boundary. `content` falsy renders just the trigger, so
 * callers don't need their own conditional. The bubble mounts only while open rather than
 * always-rendered-but-hidden — otherwise its text silently joins the trigger's textContent
 * (breaking anything that reads cell text, sighted copy-paste included). */
export function Tooltip({
  content,
  side = 'top',
  children,
}: {
  content: ReactNode;
  side?: 'top' | 'bottom';
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  if (!content) return <>{children}</>;
  return (
    <span
      className={`ds-tooltip ds-tooltip--${side}`}
      tabIndex={0}
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
      onFocus={() => setOpen(true)}
      onBlur={() => setOpen(false)}
    >
      {children}
      {open && (
        <span className="ds-tooltip__bubble" role="tooltip">
          {content}
        </span>
      )}
    </span>
  );
}
