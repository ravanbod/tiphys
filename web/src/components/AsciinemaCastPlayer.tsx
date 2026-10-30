import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react';
import * as AsciinemaPlayer from 'asciinema-player';
import type { Player } from 'asciinema-player';
import 'asciinema-player/dist/bundle/asciinema-player.css';
import { buildCastTimeMap } from '../lib/castTime';
import type { CastTimeMap } from '../lib/castTime';
import type { CastPlayerHandle, CastPlayerProps } from './CastPlayer';

const POLL_MS = 250;

/**
 * Wraps asciinema-player. Changing `speed` re-creates the player at the current position
 * (the player has no speed setter). Always dark.
 */
export const AsciinemaCastPlayer = forwardRef<CastPlayerHandle, CastPlayerProps>(function AsciinemaCastPlayer(
  { text, eventTimes, speed, idleLimit = 2, onTime, onError }, ref,
) {
  const host = useRef<HTMLDivElement>(null);
  const player = useRef<Player | null>(null);
  const map = useRef<CastTimeMap>(buildCastTimeMap([], idleLimit));
  const resume = useRef({ orig: 0, playing: false });
  const cb = useRef({ onTime, onError });
  cb.current = { onTime, onError };

  useImperativeHandle(ref, () => ({
    seek(o) {
      const p = player.current;
      if (!p) return;
      void Promise.resolve(p.seek(map.current.toPlayer(o))).catch(() => {});
    },
    currentTime() {
      const p = player.current;
      return p ? map.current.toOrig(p.getCurrentTime()) : 0;
    },
  }));

  // A different recording starts from the beginning.
  useEffect(() => {
    resume.current = { orig: 0, playing: false };
  }, [text]);

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    map.current = buildCastTimeMap(eventTimes, idleLimit);
    const { orig, playing } = resume.current;
    let p: Player;
    try {
      p = AsciinemaPlayer.create({ data: text, parser: 'asciicast' }, el, {
        fit: 'width',
        theme: 'asciinema',
        controls: true,
        idleTimeLimit: idleLimit,
        startAt: orig,
        speed,
        autoPlay: playing,
        preload: true,
        terminalFontFamily: "'IBM Plex Mono', ui-monospace, monospace",
      });
    } catch (e) {
      cb.current.onError?.(e instanceof Error ? e.message : 'The recording could not be played.');
      return;
    }
    player.current = p;
    let isPlaying = playing;
    p.addEventListener('play', () => { isPlaying = true; });
    p.addEventListener('pause', () => { isPlaying = false; });
    p.addEventListener('ended', () => { isPlaying = false; });
    const timer = window.setInterval(() => {
      try {
        cb.current.onTime?.(map.current.toOrig(p.getCurrentTime()));
      } catch {
        /* player not ready yet */
      }
    }, POLL_MS);
    return () => {
      window.clearInterval(timer);
      try {
        resume.current = { orig: map.current.toOrig(p.getCurrentTime()), playing: isPlaying };
      } catch {
        /* keep the previous position */
      }
      try {
        p.dispose();
      } catch {
        /* already disposed */
      }
      if (player.current === p) player.current = null;
    };
    // eventTimes/idleLimit only change together with `text`
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text, speed, idleLimit]);

  return <div ref={host} className="rp-player" />;
});

