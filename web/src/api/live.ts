import type { LiveChange, LiveReady } from './types';

export interface LiveHandlers {
  onReady: (r: LiveReady) => void;
  onChange: (c: LiveChange) => void;
  /** The stream dropped; EventSource reconnects on its own unless it was refused. */
  onDown: (final: boolean) => void;
}

/** Follows namespaces ("cluster/namespace") through GET /live. Returns a close function. */
export function openLive(namespaces: string[], h: LiveHandlers): () => void {
  const q = namespaces.map((n) => `ns=${encodeURIComponent(n)}`).join('&');
  const es = new EventSource(`/api/v1/live?${q}`);
  const parse = <T,>(ev: Event): T | null => {
    try {
      return JSON.parse((ev as MessageEvent<string>).data) as T;
    } catch {
      return null;
    }
  };
  es.addEventListener('ready', (ev) => {
    const r = parse<LiveReady>(ev);
    if (r) h.onReady(r);
  });
  es.addEventListener('change', (ev) => {
    const c = parse<LiveChange>(ev);
    if (c) h.onChange(c);
  });
  // The server ends the stream after a while (or when access ends); the browser reconnects.
  es.onerror = () => h.onDown(es.readyState === EventSource.CLOSED);
  return () => es.close();
}
