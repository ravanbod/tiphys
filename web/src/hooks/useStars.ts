import { useCallback, useEffect, useState } from 'react';
import { readStorage, writeStorage } from './useLocalStorage';

export interface Star {
  key: string; // cluster/namespace/app
  name: string;
  cluster: string;
  namespace: string;
  environment: string;
}
const KEY = 'tiphys.stars';
const EVT = 'tiphys:stars';

export function starKey(cluster: string, ns: string, name: string) {
  return `${cluster}/${ns}/${name}`;
}

export function useStars() {
  const [stars, setStars] = useState<Star[]>(() => readStorage<Star[]>(KEY, []));
  useEffect(() => {
    const h = () => setStars(readStorage<Star[]>(KEY, []));
    window.addEventListener(EVT, h);
    window.addEventListener('storage', h);
    return () => {
      window.removeEventListener(EVT, h);
      window.removeEventListener('storage', h);
    };
  }, []);
  const toggle = useCallback((s: Star) => {
    const cur = readStorage<Star[]>(KEY, []);
    const next = cur.some((x) => x.key === s.key) ? cur.filter((x) => x.key !== s.key) : [...cur, s];
    writeStorage(KEY, next);
    window.dispatchEvent(new Event(EVT));
  }, []);
  return { stars, toggle, isStarred: (key: string) => stars.some((s) => s.key === key) };
}
