import { useEffect, useRef, useState } from 'react';
import type { K8sEvent } from '../api';

export type RowAnim = '' | 'enter' | 'changed' | 'leave';

export interface AnimatedRow<T> {
  item: T;
  key: string;
  anim: RowAnim;
}

const MARK_MS = 1600;
const LEAVE_MS = 400;

/** The keys of a list; repeated keys get "#2", "#3"... so they stay unique. */
function keysOf<T>(items: T[], key: (t: T) => string): string[] {
  const seen = new Map<string, number>();
  return items.map((i) => {
    const k = key(i);
    const n = (seen.get(k) ?? 0) + 1;
    seen.set(k, n);
    return n === 1 ? k : `${k}#${n}`;
  });
}

interface Mark<T> {
  anim: Exclude<RowAnim, ''>;
  until: number;
  item?: T; // a leaving row, still shown
  index?: number; // where it was
}

/**
 * Compares each new version of a list with the previous one and marks rows
 * that appeared (enter), changed (their sig differs) or disappeared (leave,
 * shown a moment longer), so tables can animate refreshes. The first data
 * and a new scope (another page or filter) are not animated. Lists are
 * compared by content, so items may be a new array on every render.
 */
export function useAnimatedList<T>(items: T[] | undefined, key: (t: T) => string, sig: (t: T) => string, scope: string): AnimatedRow<T>[] {
  const prev = useRef<{ scope: string; sigs: Map<string, string>; list: T[] } | null>(null);
  const [marks, setMarks] = useState<ReadonlyMap<string, Mark<T>>>(new Map());
  const ids = keysOf(items ?? [], key);
  const sigList = (items ?? []).map(sig);
  const content = items ? ids.map((id, n) => `${id}\u0001${sigList[n]}`).join('\u0002') : null;
  const latest = useRef({ items, key, ids, sigList });
  latest.current = { items, key, ids, sigList };

  useEffect(() => {
    const { items: list, key: k, ids: cur, sigList: curSigs } = latest.current;
    if (!list) return;
    const p = prev.current;
    const sigs = new Map(cur.map((id, n) => [id, curSigs[n]]));
    prev.current = { scope, sigs, list };
    if (!p || p.scope !== scope) {
      setMarks(new Map());
      return;
    }
    const now = Date.now();
    setMarks((old) => {
      const next = new Map([...old].filter(([, m]) => m.until > now));
      const before = next.size === old.size ? old : null;
      for (const [id, sg] of sigs) {
        if (!p.sigs.has(id)) next.set(id, { anim: 'enter', until: now + MARK_MS });
        else if (p.sigs.get(id) !== sg) next.set(id, { anim: 'changed', until: now + MARK_MS });
        else if (next.get(id)?.anim === 'leave') next.delete(id);
      }
      const prevIds = keysOf(p.list, k);
      p.list.forEach((item, index) => {
        const id = prevIds[index];
        if (!sigs.has(id)) next.set(id, { anim: 'leave', until: now + LEAVE_MS, item, index });
      });
      return before && next.size === before.size && [...next].every(([id, m]) => before.get(id) === m) ? before : next;
    });
  }, [content, scope]);

  useEffect(() => {
    if (marks.size === 0) return;
    const soonest = Math.min(...[...marks.values()].map((m) => m.until));
    const t = window.setTimeout(() => {
      const now = Date.now();
      setMarks((old) => new Map([...old].filter(([, m]) => m.until > now)));
    }, Math.max(0, soonest - Date.now()) + 20);
    return () => window.clearTimeout(t);
  }, [marks]);

  const rows: AnimatedRow<T>[] = (items ?? []).map((item, n) => {
    const id = ids[n];
    const m = marks.get(id);
    return { item, key: id, anim: m && m.anim !== 'leave' ? m.anim : '' };
  });
  for (const [id, m] of marks) {
    if (m.anim === 'leave' && m.item !== undefined && !rows.some((r) => r.key === id)) {
      rows.splice(Math.min(m.index ?? rows.length, rows.length), 0, { item: m.item, key: id, anim: 'leave' });
    }
  }
  return rows;
}

/** The class a table row gets for its animation. */
export const rowClass = (anim: RowAnim, base = '') => [base, anim ? `live-${anim}` : ''].filter(Boolean).join(' ') || undefined;

/** Identity and content of a Kubernetes event row. */
export const eventKey = (e: K8sEvent) => `${e.type}|${e.reason}|${e.object}|${e.firstSeen}`;
export const eventSig = (e: K8sEvent) => `${e.count}|${e.lastSeen}|${e.message}`;
