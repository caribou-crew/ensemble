// Generic Datadog-style right-docked drawer: dims the page behind a backdrop while a
// fixed-width panel slides in from the right edge, closable via backdrop click or Escape.
// Extracted from TraceDrawer so LogsDrawer (Services tab) gets the same chrome for free.
import { useEffect, type ReactNode } from 'react';
import './Drawer.css';

export default function Drawer({
  open,
  onClose,
  classPrefix,
  ariaLabel,
  header,
  children,
}: {
  open: boolean;
  onClose: () => void;
  /** Every generated element also carries `<classPrefix>`/`<classPrefix>__panel`/etc, so a
   * caller's existing stylesheet and tests (querySelector('.trace-drawer__close')) keep
   * working unchanged after adopting this shared shell. */
  classPrefix: string;
  ariaLabel: string;
  header: ReactNode;
  children: ReactNode;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className={`drawer ${classPrefix}`}>
      <button
        type="button"
        className={`drawer__backdrop ${classPrefix}__backdrop`}
        onClick={onClose}
        aria-label="close"
      />
      <div className={`drawer__panel ${classPrefix}__panel`} role="dialog" aria-label={ariaLabel}>
        <div className={`drawer__header ${classPrefix}__header`}>
          {header}
          <button
            type="button"
            className={`drawer__close ${classPrefix}__close`}
            onClick={onClose}
            aria-label="close"
          >
            ×
          </button>
        </div>
        <div className={`drawer__body ${classPrefix}__body`}>{children}</div>
      </div>
    </div>
  );
}
