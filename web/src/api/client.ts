import { toApiError } from './errors';
import { loginUrl } from './query';

const BASE = '/api/v1';

let csrfToken = '';
export function setCsrfToken(t: string) {
  csrfToken = t;
}
export function getCsrfToken() {
  return csrfToken;
}

let redirecting = false;
function redirectToLogin(): Promise<never> {
  if (!redirecting) {
    redirecting = true;
    const next = window.location.pathname + window.location.search;
    // The login page is needed only when it offers local accounts; otherwise
    // go straight to Keycloak as before.
    fetch('/auth/options', { credentials: 'same-origin', headers: { Accept: 'application/json' } })
      .then(async (r) => r.ok && ((await r.json()) as { localLogin?: boolean }).localLogin === true)
      .catch(() => false)
      .then((local) => window.location.assign(loginUrl(next, local)));
  }
  return new Promise<never>(() => {});
}

export interface RequestOptions {
  body?: unknown;
  signal?: AbortSignal;
  /** Do not redirect to login on 401 (used by the login page). */
  noRedirect?: boolean;
  /** Absolute path (skips /api/v1 prefix), e.g. /auth/logout. */
  raw?: boolean;
}

export async function request<T>(method: string, path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.body);
  }
  if (method !== 'GET' && method !== 'HEAD') headers['X-CSRF-Token'] = csrfToken;
  const res = await fetch(opts.raw ? path : BASE + path, {
    method,
    headers,
    body,
    credentials: 'same-origin',
    signal: opts.signal,
  });
  if (res.status === 401 && !opts.noRedirect) return redirectToLogin();
  if (!res.ok) throw toApiError(res.status, await res.text().catch(() => ''));
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  if (!text) return undefined as T;
  const ct = res.headers.get('Content-Type') ?? '';
  if (!ct.includes('json')) return text as unknown as T;
  return JSON.parse(text) as T;
}

/** Fetches a text body (e.g. the asciicast recording). */
export async function requestText(path: string, signal?: AbortSignal): Promise<string> {
  const res = await fetch(BASE + path, { credentials: 'same-origin', signal, headers: { Accept: '*/*' } });
  if (res.status === 401) return redirectToLogin();
  if (!res.ok) throw toApiError(res.status, await res.text().catch(() => ''));
  return res.text();
}

export const get = <T>(path: string, signal?: AbortSignal) => request<T>('GET', path, { signal });
export const post = <T>(path: string, body?: unknown) => request<T>('POST', path, { body: body ?? {} });
export const put = <T>(path: string, body?: unknown) => request<T>('PUT', path, { body });
export const patch = <T>(path: string, body?: unknown) => request<T>('PATCH', path, { body });
export const del = <T>(path: string) => request<T>('DELETE', path);
