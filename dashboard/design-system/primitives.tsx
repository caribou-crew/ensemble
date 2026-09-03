import { useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react';
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

/** Hover/focus popover, CSS-positioned (no portal) — the trigger and bubble share one
 * relatively-positioned wrapper, so it only works well for triggers that aren't themselves
 * near a scroll/clip boundary. `content` falsy renders just the trigger, so callers don't
 * need their own conditional. The bubble mounts only while open rather than
 * always-rendered-but-hidden — otherwise its text silently joins the trigger's textContent
 * (breaking anything that reads cell text, sighted copy-paste included).
 *
 * Horizontally centered on the trigger by default, but a trigger near the left/right edge of
 * the viewport (the leftmost column of a wide table, say) would center a bubble half off
 * screen — clamped back on screen via `--tooltip-shift`, measured post-mount and applied
 * before paint (useLayoutEffect) so there's no visible jump. */
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
  const [shift, setShift] = useState(0);
  const bubbleRef = useRef<HTMLSpanElement>(null);

  useLayoutEffect(() => {
    if (!open) return;
    const el = bubbleRef.current;
    if (!el) return;
    const margin = 8;
    const rect = el.getBoundingClientRect();
    if (rect.left < margin) {
      setShift(margin - rect.left);
    } else if (rect.right > window.innerWidth - margin) {
      setShift(window.innerWidth - margin - rect.right);
    } else {
      setShift(0);
    }
  }, [open]);

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
        <span
          ref={bubbleRef}
          className="ds-tooltip__bubble"
          role="tooltip"
          style={{ '--tooltip-shift': `${shift}px` } as CSSProperties}
        >
          {content}
        </span>
      )}
    </span>
  );
}
