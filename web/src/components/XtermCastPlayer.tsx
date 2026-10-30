import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import { parseAsciicast } from '../lib/asciicast';
import { buildCastTimeMap } from '../lib/castTime';
import { fmtCountdown } from '../lib/time';
import type { CastPlayerHandle, CastPlayerProps } from './CastPlayer';
import { Icon } from './Icon';
import { XTerm } from './XTerm';
import type { XTermHandle } from './XTerm';

/** WebAssembly-free cast player: replays output events into xterm.js. Always dark. */
export const XtermCastPlayer = forwardRef<CastPlayerHandle, CastPlayerProps>(function XtermCastPlayer(
  { text, eventTimes, speed, idleLimit = 2, onTime, onError }, ref,
) {
  const term = useRef<XTermHandle>(null);
  const parsed = useMemo(() => parseAsciicast(text), [text]);
  const map = useMemo(() => buildCastTimeMap(eventTimes, idleLimit), [eventTimes, idleLimit]);
  // events in player (idle-compressed) time
  const evs = useMemo(
    () => parsed.events.filter((e) => e.code === 'o' || e.code === 'r').map((e) => ({ ...e, p: map.toPlayer(e.t) })),
    [parsed, map],
  );
  const duration = map.duration || (evs.length ? evs[evs.length - 1].p : 0);
  const [pos, setPos] = useState(0);
  const [playing, setPlaying] = useState(false);
  const posRef = useRef(0);
  const idx = useRef(0);
  const speedRef = useRef(speed);
  speedRef.current = speed;
  const cb = useRef({ onTime, onError });
  cb.current = { onTime, onError };
  const ready = useRef(false);

  const apply = useCallback((from: number, to: number) => {
    let out = '';
    for (; idx.current < evs.length && evs[idx.current].p <= to; idx.current++) {
      const e = evs[idx.current];
      if (idx.current < from) continue;
      if (e.code === 'o') out += e.data;
      else if (e.code === 'r') {
        const m = /^(\d+)x(\d+)$/.exec(e.data);
        if (m) term.current?.resize(Number(m[1]), Number(m[2]));
      }
    }
    if (out) term.current?.write(out);
  }, [evs]);

  const seekPlayer = useCallback((p: number) => {
    const t = Math.max(0, Math.min(duration, p));
    term.current?.reset();
    if (parsed.header) term.current?.resize(parsed.header.width, parsed.header.height);
    idx.current = 0;
    apply(0, t);
    posRef.current = t;
    setPos(t);
    cb.current.onTime?.(map.toOrig(t));
  }, [apply, duration, map, parsed.header]);

  useImperativeHandle(ref, () => ({
    seek: (o) => seekPlayer(map.toPlayer(o)),
    currentTime: () => map.toOrig(posRef.current),
  }));

  const onReady = useCallback(() => {
    ready.current = true;
    if (parsed.header) term.current?.resize(parsed.header.width, parsed.header.height);
    if (evs.length === 0) cb.current.onError?.('This recording has no output.');
  }, [evs.length, parsed.header]);

  useEffect(() => {
    if (!playing) return;
    let raf = 0;
    let last = performance.now();
    const tick = (now: number) => {
      const dt = (now - last) / 1000;
      last = now;
      const to = Math.min(duration, posRef.current + dt * speedRef.current);
      apply(idx.current, to);
      posRef.current = to;
      setPos(to);
      cb.current.onTime?.(map.toOrig(to));
      if (to >= duration) { setPlaying(false); return; }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [playing, apply, duration, map]);

  const toggle = () => {
    if (!playing && posRef.current >= duration) seekPlayer(0);
    setPlaying((p) => !p);
  };

  return (
    <div className="rp-player xcp">
      <div className="xcp-term"><XTerm ref={term} readOnly ariaLabel="Session replay" onReady={onReady} /></div>
      <div className="xcp-bar">
        <button type="button" className="btn tdark sm" onClick={toggle} aria-label={playing ? 'Pause' : 'Play'}><Icon name={playing ? 'pause' : 'play'} />{playing ? 'Pause' : 'Play'}</button>
        <input
          type="range" min={0} max={Math.max(duration, 0.01)} step={0.1} value={pos} aria-label="Seek"
          onChange={(e) => { setPlaying(false); seekPlayer(Number(e.target.value)); }}
        />
        <span className="mono">{fmtCountdown(map.toOrig(pos) * 1000)} / {fmtCountdown(map.toOrig(duration) * 1000)}</span>
      </div>
    </div>
  );
});
