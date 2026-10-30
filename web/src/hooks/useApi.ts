import { useCallback, useEffect, useRef, useState } from 'react';

export interface ApiState<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
  reload: () => void;
  setData: (v: T | undefined | ((prev: T | undefined) => T | undefined)) => void;
}

/** Small data-loading hook: runs `fn` when `deps` change, optionally polls. */
export function useApi<T>(
  fn: (signal: AbortSignal) => Promise<T>,
  deps: React.DependencyList,
  opts: { poll?: number; enabled?: boolean } = {},
): ApiState<T> {
  const { poll, enabled = true } = opts;
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(enabled);
  const [tick, setTick] = useState(0);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      return;
    }
    const ctrl = new AbortController();
    let alive = true;
    const run = (initial: boolean) => {
      if (initial) setLoading(true);
      fnRef
        .current(ctrl.signal)
        .then((v) => {
          if (!alive) return;
          setData(v);
          setError(null);
        })
        .catch((e: unknown) => {
          if (!alive || (e instanceof DOMException && e.name === 'AbortError')) return;
          setError(e);
        })
        .finally(() => {
          if (alive) setLoading(false);
        });
    };
    run(true);
    const id = poll ? window.setInterval(() => run(false), poll) : undefined;
    return () => {
      alive = false;
      ctrl.abort();
      if (id) window.clearInterval(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick, enabled, poll]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, loading, reload, setData };
}
