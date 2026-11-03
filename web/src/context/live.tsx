import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Link, useLocation } from 'react-router-dom';
import { openLive } from '../api';
import type { LiveChange } from '../api';
import { Icon } from '../components/Icon';
import { MAX_VISIBLE_TOASTS, addToast, expireToasts, toastFor } from '../lib/liveToasts';
import type { LiveToast } from '../lib/liveToasts';
import { useToastSlot } from './toast';

/** How many namespaces one stream follows (the server's limit). */
const MAX_NAMESPACES = 10;
const LEAVE_MS = 250;
const NAME = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const CLUSTER = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/;

export type LiveStatus = 'off' | 'connecting' | 'live' | 'down';

interface Sub {
  key: string; // cluster/namespace
  kind?: string;
  name?: string;
  cb: (c: LiveChange | null) => void; // null: reconnected, refetch what may have been missed
}

interface LiveCtx {
  subscribe: (s: Sub) => () => void;
  status: LiveStatus;
  muted: boolean;
  toggleMute: () => void;
}

const Ctx = createContext<LiveCtx>({ subscribe: () => () => {}, status: 'off', muted: false, toggleMute: () => {} });

/**
 * One GET /live stream per tab for every namespace the mounted pages follow
 * (useLive), reopened when that set changes. Changes go to the subscribers;
 * notable ones also become toasts, unless muted on the current page (kept in
 * memory only, so a reload unmutes).
 */
export function LiveProvider({ children }: { children: React.ReactNode }) {
  const subs = useRef(new Map<number, Sub>());
  const nextId = useRef(1);
  const [keys, setKeys] = useState('');
  const [status, setStatus] = useState<LiveStatus>('off');
  const [retry, setRetry] = useState(0);
  const { pathname } = useLocation();
  const [mutedPaths, setMutedPaths] = useState<ReadonlySet<string>>(new Set());
  const muted = mutedPaths.has(pathname);
  const mutedRef = useRef(muted);
  mutedRef.current = muted;
  const [toasts, setToasts] = useState<LiveToast[]>([]);
  const toastId = useRef(1);
  const hovered = useRef(false);

  const recompute = useCallback(() => {
    const all = [...new Set([...subs.current.values()].map((s) => s.key))].sort();
    setKeys(all.slice(0, MAX_NAMESPACES).join(','));
  }, []);

  const subscribe = useCallback((s: Sub) => {
    const [cluster, ns] = s.key.split('/');
    if (!CLUSTER.test(cluster ?? '') || !NAME.test(ns ?? '')) return () => {};
    const id = nextId.current++;
    subs.current.set(id, s);
    recompute();
    return () => {
      subs.current.delete(id);
      recompute();
    };
  }, [recompute]);

  useEffect(() => {
    if (!keys) {
      setStatus('off');
      return;
    }
    let close: (() => void) | null = null;
    let retryTimer = 0;
    // Wait a moment so a page that mounts several followers opens one stream.
    const start = window.setTimeout(() => {
      setStatus('connecting');
      let readies = 0;
      close = openLive(keys.split(','), {
        onReady: () => {
          setStatus('live');
          // After a reconnect, changes may have been missed: let everyone refetch.
          if (readies++ > 0) subs.current.forEach((s) => s.cb(null));
        },
        onChange: (c) => {
          const key = `${c.cluster}/${c.namespace}`;
          let shown = false; // toasts only for what the page shows: on a workload's page, that workload
          subs.current.forEach((s) => {
            if (s.key === key && (!s.kind || (s.kind === c.kind && s.name === c.name))) {
              s.cb(c);
              shown = true;
            }
          });
          const t = shown ? toastFor(c) : null;
          if (t && !mutedRef.current) setToasts((l) => addToast(l, t, Date.now(), toastId.current++));
        },
        onDown: (final) => {
          setStatus('down');
          // A refused stream (e.g. too many tabs) is not retried by the browser.
          if (final) retryTimer = window.setTimeout(() => setRetry((n) => n + 1), 30_000);
        },
      });
    }, 300);
    return () => {
      window.clearTimeout(start);
      window.clearTimeout(retryTimer);
      close?.();
    };
  }, [keys, retry]);

  useEffect(() => {
    if (toasts.length === 0) return;
    const id = window.setInterval(() => {
      if (!hovered.current) setToasts((l) => expireToasts(l, Date.now(), LEAVE_MS));
    }, 200);
    return () => window.clearInterval(id);
  }, [toasts.length]);

  const toggleMute = useCallback(() => {
    const was = mutedRef.current;
    setMutedPaths((m) => {
      const n = new Set(m);
      if (was) n.delete(pathname);
      else n.add(pathname);
      return n;
    });
    if (!was) setToasts([]);
  }, [pathname]);

  const value = useMemo(() => ({ subscribe, status, muted, toggleMute }), [subscribe, status, muted, toggleMute]);
  return (
    <Ctx.Provider value={value}>
      {children}
      <LiveToasts
        toasts={toasts}
        onDismiss={(id) => setToasts((l) => l.filter((t) => t.id !== id))}
        onClear={() => setToasts([])}
        onMute={toggleMute}
        onHover={(h) => {
          hovered.current = h;
          // Leaving the stack gives the toasts their full time again.
          if (!h) setToasts((l) => l.map((t) => (t.leaving ? t : { ...t, at: Date.now() })));
        }}
      />
    </Ctx.Provider>
  );
}

export const useLiveCtx = () => useContext(Ctx);

export interface LiveTarget {
  cluster: string;
  namespace: string;
  kind?: string;
  name?: string;
}

/**
 * Follows a namespace (or one workload of it) while mounted. refresh gets
 * the changes since its last call, gathered until 400 ms pass without one
 * (at most 2 s) and at most every 1.5 s, or an empty list after a
 * reconnect. Returns whether changes are flowing, so pages can poll less
 * while they are.
 */
export function useLive(target: LiveTarget | null, refresh: (changes: LiveChange[]) => void): boolean {
  const { subscribe, status } = useContext(Ctx);
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;
  const key = target ? `${target.cluster}/${target.namespace}` : '';
  const kind = target?.kind;
  const name = target?.name;
  useEffect(() => {
    if (!key) return;
    let pending: LiveChange[] = [];
    let timer = 0;
    let first = 0;
    let last = 0;
    const flush = () => {
      const batch = pending;
      pending = [];
      timer = 0;
      first = 0;
      last = Date.now();
      refreshRef.current(batch);
    };
    const unsubscribe = subscribe({
      key, kind, name: kind ? name : undefined,
      cb: (c) => {
        if (c) pending.push(c);
        const now = Date.now();
        if (!first) first = now;
        window.clearTimeout(timer);
        const quiet = now - first >= 2000 ? 0 : 400;
        timer = window.setTimeout(flush, Math.max(quiet, last + 1500 - now));
      },
    });
    return () => {
      unsubscribe();
      window.clearTimeout(timer);
    };
  }, [subscribe, key, kind, name]);
  return !!key && status === 'live';
}

/** The pulsing "Live" mark in the top bar; it mutes toasts on the current page. */
export function LiveIndicator() {
  const { status, muted, toggleMute } = useContext(Ctx);
  if (status === 'off') return null;
  const label = status === 'live' ? 'Live' : status === 'connecting' ? 'Connecting' : 'Reconnecting';
  const title = status === 'live'
    ? `Changes in the cluster appear as they happen. ${muted ? 'Notifications are muted on this page; click to unmute.' : 'Click to mute notifications on this page.'}`
    : 'Live updates are unavailable; the page refreshes itself every few seconds instead.';
  return (
    <button type="button" className={`live is-${status}`} aria-pressed={muted} onClick={toggleMute} title={title}>
      <span className="live-dot" aria-hidden="true" />
      {label}
      {muted && <Icon name="belloff" size={13} />}
      <span className="sr-only">{muted ? ', notifications muted on this page' : ''}</span>
    </button>
  );
}

function LiveToasts({ toasts, onDismiss, onClear, onMute, onHover }: {
  toasts: LiveToast[]; onDismiss: (id: number) => void; onClear: () => void; onMute: () => void; onHover: (h: boolean) => void;
}) {
  const slot = useToastSlot();
  if (!slot || toasts.length === 0) return null;
  const visible = toasts.slice(-MAX_VISIBLE_TOASTS);
  const more = toasts.length - visible.length;
  return createPortal(
    <div className="ltoasts" onMouseEnter={() => onHover(true)} onMouseLeave={() => onHover(false)}>
      <div className="ltoasts-bar">
        {more > 0 && <span className="ltoasts-more"><span key={more} className="bump">+{more}</span> more</span>}
        <span className="grow" />
        <button type="button" className="linklike" onClick={onMute}><Icon name="belloff" size={12} />Mute on this page</button>
        <button type="button" className="linklike" onClick={onClear}>Clear</button>
      </div>
      {visible.map((t) => (
        <div key={t.id} className={`ltoast ${t.tone}${t.leaving ? ' leaving' : ''}`}>
          <span className={`dot ${t.tone === 'info' ? 'info' : t.tone}`} aria-hidden="true" />
          <div className="ltoast-body">
            <Link className="ltoast-t mono" to={t.to}>{t.title}</Link>
            {t.count > 1 && <span key={t.count} className="ltoast-n bump">×{t.count}</span>}
            {t.detail && <div className="ltoast-d">{t.detail}</div>}
          </div>
          <button type="button" className="ltoast-x" aria-label="Dismiss" onClick={() => onDismiss(t.id)}><Icon name="x" size={12} /></button>
        </div>
      ))}
    </div>,
    slot,
  );
}
