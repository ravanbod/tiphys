import { useCallback, useState } from 'react';

export function readStorage<T>(key: string, fallback: T): T {
  try {
    const v = window.localStorage.getItem(key);
    return v === null ? fallback : (JSON.parse(v) as T);
  } catch {
    return fallback;
  }
}
export function writeStorage(key: string, value: unknown) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* storage may be unavailable */
  }
}

export function useLocalStorage<T>(key: string, fallback: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => readStorage(key, fallback));
  const set = useCallback(
    (n: T) => {
      setV(n);
      writeStorage(key, n);
    },
    [key],
  );
  return [v, set];
}
