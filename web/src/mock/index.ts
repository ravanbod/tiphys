import { getDb } from './db';
import { dispatch } from './handlers';
import { makePatchedEventSource, makePatchedWebSocket } from './sockets';

/**
 * In-browser mock of the Tiphys API (VITE_MOCK=1). Patches fetch, WebSocket and EventSource for
 * /api/v1/* and /auth/*. Persona via ?as=user|admin|auditor (remembered in localStorage);
 * ?stale=1 simulates a stale Argo cache; ?recordingDown=1 simulates MinIO being down.
 */
export function installMock() {
  const params = new URLSearchParams(window.location.search);
  const store = (k: string, v: string | null) => {
    try {
      if (v === null) window.localStorage.removeItem(k);
      else window.localStorage.setItem(k, v);
    } catch { /* ignore */ }
  };
  const as = params.get('as');
  if (as === 'user' || as === 'admin' || as === 'auditor') store('tiphys.mockRole', as);
  if (params.has('recordingDown')) store('tiphys.mockRecordingDown', params.get('recordingDown') === '1' ? '1' : null);
  const d = getDb();
  if (params.has('stale')) store('tiphys.mockStale', params.get('stale') === '1' ? '1' : null);
  d.argo.stale = window.localStorage.getItem('tiphys.mockStale') === '1';

  const realFetch = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const req = input instanceof Request ? input : null;
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, window.location.href);
    if (url.origin !== window.location.origin || !(url.pathname.startsWith('/api/v1/') || url.pathname.startsWith('/auth/'))) return realFetch(input, init);
    const method = (init?.method ?? req?.method ?? 'GET').toUpperCase();
    const headers = new Headers(init?.headers ?? req?.headers);
    let body: Record<string, unknown> = {};
    const rawBody = init?.body ?? undefined;
    if (typeof rawBody === 'string' && rawBody) {
      try { body = JSON.parse(rawBody) as Record<string, unknown>; } catch { body = {}; }
    }
    await new Promise((r) => setTimeout(r, 60 + Math.random() * 60));
    if (method !== 'GET' && method !== 'HEAD' && headers.get('X-CSRF-Token') !== 'mock-csrf-token') {
      return new Response(JSON.stringify({ error: { code: 'forbidden_role', message: 'Missing or invalid CSRF token' } }), { status: 403, headers: { 'Content-Type': 'application/json' } });
    }
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const res = dispatch(method, path, url.searchParams, body);
    if (res.data && typeof res.data === 'object' && '__raw' in (res.data as object)) {
      const r = res.data as { __raw: string; __type: string };
      return new Response(r.__raw, { status: res.status, headers: { 'Content-Type': r.__type } });
    }
    if (res.status === 204) return new Response(null, { status: 204 });
    return new Response(JSON.stringify(res.data), { status: res.status, headers: { 'Content-Type': 'application/json' } });
  };
  window.WebSocket = makePatchedWebSocket(window.WebSocket);
  window.EventSource = makePatchedEventSource(window.EventSource);
  console.info('[tiphys] mock API active (persona: ' + (window.localStorage.getItem('tiphys.mockRole') ?? 'user') + ')');
}
