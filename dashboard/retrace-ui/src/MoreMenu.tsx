import { useEffect, useRef, useState } from 'react';

export interface MenuItem {
  label: string;
  hint: string;
  onSelect: () => void;
}

export default function MoreMenu({ items }: { items: MenuItem[] }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      e.stopPropagation();
      setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey, true);
    };
  }, [open]);

  return (
    <div className="more-menu" ref={ref}>
      <button type="button" className="more-menu__trigger" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((v) => !v)} title="more tools">
        ⋯
      </button>
      {open ? (
        <div className="more-menu__list" role="menu">
          {items.map((it) => (
            <button
              key={it.label}
              type="button"
              role="menuitem"
              className="more-menu__item"
              onClick={() => {
                setOpen(false);
                it.onSelect();
              }}
            >
              <span className="more-menu__label">{it.label}</span>
              <span className="more-menu__hint">{it.hint}</span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
