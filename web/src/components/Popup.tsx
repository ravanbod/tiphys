import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { CSSProperties, ReactNode, RefObject } from 'react';
import { createPortal } from 'react-dom';

const GAP = 4;
const MAX_HEIGHT = 260;
const EDGE = 8;

/** Where the list goes: under the anchor, or above it when there is more room there. */
function place(anchor: HTMLElement): CSSProperties {
  const r = anchor.getBoundingClientRect();
  const below = window.innerHeight - r.bottom - GAP - EDGE;
  const above = r.top - GAP - EDGE;
  const up = below < Math.min(MAX_HEIGHT, 160) && above > below;
  const maxWidth = Math.min(420, window.innerWidth - 2 * EDGE);
  const minWidth = Math.min(r.width, maxWidth);
  const left = Math.max(EDGE, Math.min(r.left, window.innerWidth - EDGE - minWidth));
  return {
    left, minWidth, maxWidth,
    maxHeight: Math.max(120, Math.min(MAX_HEIGHT, up ? above : below)),
    ...(up ? { bottom: window.innerHeight - r.top + GAP } : { top: r.bottom + GAP }),
  };
}

/**
 * The option list of a Select or ComboBox, rendered at the end of the app's
 * root (`.app`, which holds the colour tokens and the dark theme; the body as
 * a fallback) at a fixed position under (or above) its anchor, so a scrolling
 * table or dialog never cuts it off. It follows the anchor while the page scrolls and closes
 * on a press outside both. Focus stays on the anchor (aria-activedescendant).
 */
export function Popup({
  anchor, onClose, id, label, children,
}: { anchor: RefObject<HTMLElement>; onClose: () => void; id: string; label: string; children: ReactNode }) {
  const [style, setStyle] = useState<CSSProperties | null>(null);
  const list = useRef<HTMLUListElement>(null);
  const close = useRef(onClose);
  close.current = onClose;

  useLayoutEffect(() => {
    const update = () => { if (anchor.current) setStyle(place(anchor.current)); };
    update();
    window.addEventListener('scroll', update, true);
    window.addEventListener('resize', update);
    return () => {
      window.removeEventListener('scroll', update, true);
      window.removeEventListener('resize', update);
    };
  }, [anchor]);

  useEffect(() => {
    const down = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!anchor.current?.contains(t) && !list.current?.contains(t)) close.current();
    };
    document.addEventListener('mousedown', down, true);
    return () => document.removeEventListener('mousedown', down, true);
  }, [anchor]);

  const root = anchor.current?.closest('.app') ?? document.body;
  if (!style) return null;
  return createPortal(
    <ul ref={list} className="cbx-list" id={id} role="listbox" aria-label={label} style={style}
      // Keep focus on the field or button while the mouse picks.
      onMouseDown={(e) => e.preventDefault()}>
      {children}
    </ul>,
    root,
  );
}
