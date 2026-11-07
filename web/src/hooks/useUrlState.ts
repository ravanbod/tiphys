import { useCallback, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

/** A page's filters: strings, or booleans stored as 1/0. */
export type UrlStateShape = Record<string, string | boolean>;

/** `{ open: true }` holds any boolean, not just `true`. */
export type Widen<D extends UrlStateShape> = { [K in keyof D]: D[K] extends boolean ? boolean : string };

/** The values in the query, the defaults for what it does not hold. */
export function readUrlState<T extends UrlStateShape>(sp: URLSearchParams, defaults: T): T {
  const out = { ...defaults };
  for (const k of Object.keys(defaults) as (keyof T & string)[]) {
    const v = sp.get(k);
    if (v === null) continue;
    out[k] = (typeof defaults[k] === 'boolean' ? v === '1' : v) as T[typeof k];
  }
  return out;
}

/** The query with the patch applied; a value equal to its default is left out. */
export function writeUrlState<T extends UrlStateShape>(sp: URLSearchParams, defaults: T, patch: Partial<T>): URLSearchParams {
  const n = new URLSearchParams(sp);
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined || v === defaults[k]) n.delete(k);
    else n.set(k, typeof v === 'boolean' ? (v ? '1' : '0') : v);
  }
  return n;
}

/**
 * Filters kept in the page's URL (replacing the history entry, not adding
 * one), so they survive going to another page and coming back with the
 * browser's back button. Set several at once with one patch.
 *
 * The router applies URL changes in a transition, which must not drive a
 * text input, so the values are held in state that updates at once; the URL
 * follows, and a URL change this hook did not write (back/forward, a link to
 * the same page) replaces them.
 */
export function useUrlState<D extends UrlStateShape>(defaults: D): [Widen<D>, (patch: Partial<Widen<D>>) => void] {
  type T = Widen<D>;
  const [sp, setSp] = useSearchParams();
  const key = JSON.stringify(defaults);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- defaults are compared by value
  const fromUrl = useMemo(() => readUrlState<T>(sp, defaults as T), [sp, key]);
  const urlJson = JSON.stringify(fromUrl);
  const [local, setLocal] = useState(fromUrl);
  const [seen, setSeen] = useState(urlJson);
  // The latest values, and what this hook wrote that the URL has not shown
  // yet (oldest first), for telling its own URL changes from others.
  const latest = useRef(fromUrl);
  const written = useRef<string[]>([]);
  if (seen !== urlJson) {
    setSeen(urlJson);
    const i = written.current.indexOf(urlJson);
    if (i >= 0) written.current = written.current.slice(i + 1);
    else {
      written.current = [];
      latest.current = fromUrl;
      setLocal(fromUrl);
    }
  }
  const set = useCallback(
    (patch: Partial<T>) => {
      const next = { ...latest.current, ...patch };
      const json = JSON.stringify(next);
      if (json === JSON.stringify(latest.current)) return;
      latest.current = next;
      written.current.push(json);
      setLocal(next);
      // The router hands the updater the query of its last render, not of
      // earlier calls not rendered yet: write all of this hook's values.
      setSp((cur) => writeUrlState<T>(cur, defaults as T, next), { replace: true });
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps -- defaults are compared by value
    [setSp, key],
  );
  return [local, set];
}
