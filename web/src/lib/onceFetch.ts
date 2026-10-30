/**
 * Shares one in-flight (or very recently started) request per key. React StrictMode runs
 * effects twice in development; the recording read is audited server-side, so the second
 * run must reuse the first request instead of issuing another.
 */
const entries = new Map<string, { p: Promise<unknown>; at: number }>();
const WINDOW_MS = 3000;

export function fetchOnce<T>(key: string, fetcher: () => Promise<T>, opts: { force?: boolean; now?: () => number } = {}): Promise<T> {
  const now = (opts.now ?? Date.now)();
  const hit = entries.get(key);
  if (!opts.force && hit && now - hit.at < WINDOW_MS) return hit.p as Promise<T>;
  const p = fetcher();
  entries.set(key, { p, at: now });
  // a failed request must never be reused
  p.catch(() => {
    if (entries.get(key)?.p === p) entries.delete(key);
  });
  return p;
}

export function resetFetchOnce(): void {
  entries.clear();
}
