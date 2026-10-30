import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';

export interface XTermHandle {
  write(data: Uint8Array | string): void;
  writeln(text: string): void;
  clear(): void;
  /** Full reset (screen, scrollback and modes). */
  reset(): void;
  focus(): void;
  size(): { cols: number; rows: number };
  /** Pin the terminal to a fixed size (used for read-only watchers of someone else's shell). */
  resize(cols: number, rows: number): void;
}

export interface XTermProps {
  /** Keystrokes / paste as a string (send as UTF-8 bytes). */
  onData?: (data: string) => void;
  onResize?: (cols: number, rows: number) => void;
  /** Called once after the first fit, with the measured size. */
  onReady?: (size: { cols: number; rows: number }) => void;
  readOnly?: boolean;
  ariaLabel?: string;
}

/** xterm.js host. Always dark, regardless of app theme. */
export const XTerm = forwardRef<XTermHandle, XTermProps>(function XTerm(
  { onData, onResize, onReady, readOnly, ariaLabel = 'Terminal' }, ref,
) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const fixed = useRef(false);
  const cb = useRef({ onData, onResize, onReady });
  cb.current = { onData, onResize, onReady };

  useImperativeHandle(ref, () => ({
    write: (d) => term.current?.write(d),
    writeln: (t) => term.current?.writeln(t),
    clear: () => term.current?.clear(),
    reset: () => term.current?.reset(),
    focus: () => term.current?.focus(),
    size: () => ({ cols: term.current?.cols ?? 80, rows: term.current?.rows ?? 24 }),
    resize: (cols, rows) => {
      fixed.current = true;
      term.current?.resize(cols, rows);
    },
  }));

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const t = new Terminal({
      fontFamily: "'IBM Plex Mono', ui-monospace, monospace",
      fontSize: 13,
      lineHeight: 1.25,
      cursorBlink: !readOnly,
      disableStdin: !!readOnly,
      scrollback: 5000,
      convertEol: false,
      theme: {
        background: '#0A0D12', foreground: '#D5DDE8', cursor: '#D5DDE8', selectionBackground: '#2F3B4F',
        black: '#0A0D12', red: '#FF8A80', green: '#7EE0A6', yellow: '#F5C26B', blue: '#6FA8FF',
        magenta: '#C792EA', cyan: '#7FDBCA', white: '#D5DDE8', brightBlack: '#7F8CA0',
      },
    });
    const f = new FitAddon();
    t.loadAddon(f);
    t.open(el);
    const ta = el.querySelector('textarea');
    ta?.setAttribute('aria-label', ariaLabel);
    term.current = t;
    fit.current = f;

    let disposed = false;
    const doFit = () => {
      if (disposed || fixed.current) return;
      try {
        f.fit();
      } catch {
        /* container not measurable yet */
      }
    };
    // Wait for fonts so measurements are correct.
    const start = () => {
      if (disposed) return;
      doFit();
      cb.current.onReady?.({ cols: t.cols, rows: t.rows });
    };
    if (document.fonts?.ready) void document.fonts.ready.then(start);
    else start();

    const d1 = t.onData((d) => cb.current.onData?.(d));
    const d2 = t.onResize(({ cols, rows }) => cb.current.onResize?.(cols, rows));
    const ro = new ResizeObserver(() => doFit());
    ro.observe(el);
    return () => {
      disposed = true;
      ro.disconnect();
      d1.dispose();
      d2.dispose();
      t.dispose();
      term.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readOnly]);

  return <div className="termhost" ref={host} />;
});
