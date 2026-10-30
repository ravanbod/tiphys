import { Suspense, forwardRef, lazy, useEffect, useState } from 'react';
import { canCompileWasm } from '../lib/wasm';
import { XtermCastPlayer } from './XtermCastPlayer';

export interface CastPlayerHandle {
  /** Seek to a time in the recording's own timeline (seconds). */
  seek(origSeconds: number): void;
  /** Current position in the recording's own timeline (seconds). */
  currentTime(): number;
}

export interface CastPlayerProps {
  /** Raw asciicast v2 text. */
  text: string;
  /** Recording event timestamps (memoized by the caller; used for idle-gap compensation). */
  eventTimes: number[];
  speed: number;
  idleLimit?: number;
  /** Called ~4x per second with the position in recording time. */
  onTime?: (origSeconds: number) => void;
  onError?: (message: string) => void;
}

// asciinema-player instantiates WebAssembly when its module loads, so it is only imported when allowed.
const AsciinemaCastPlayer = lazy(() => import('./AsciinemaCastPlayer').then((m) => ({ default: m.AsciinemaCastPlayer })));

/**
 * asciinema-player needs WebAssembly. Under a CSP without 'wasm-unsafe-eval' compilation is blocked,
 * so fall back to a small xterm.js based player (same handle, no WebAssembly).
 */
export const CastPlayer = forwardRef<CastPlayerHandle, CastPlayerProps>(function CastPlayer(props, ref) {
  const [wasm, setWasm] = useState<boolean | null>(null);
  useEffect(() => {
    let alive = true;
    void canCompileWasm().then((ok) => alive && setWasm(ok));
    return () => { alive = false; };
  }, []);
  if (wasm === null) return <div className="rp-player" aria-busy="true" />;
  return wasm ? (
    <Suspense fallback={<div className="rp-player" aria-busy="true" />}>
      <AsciinemaCastPlayer ref={ref} {...props} />
    </Suspense>
  ) : (
    <XtermCastPlayer ref={ref} {...props} />
  );
});
