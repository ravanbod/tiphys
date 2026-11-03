import type { LiveChange } from '../api';
import { workloadUrl } from './routes';

/** A toast about live changes. Repeats of the same kind of change on a workload share one toast. */
export interface LiveToast {
  id: number;
  key: string;
  title: string;
  detail: string;
  tone: 'ok' | 'err' | 'info';
  to: string;
  count: number;
  at: number; // last shown or updated (ms)
  leaving?: boolean;
}

type ToastContent = Pick<LiveToast, 'key' | 'title' | 'detail' | 'tone' | 'to'> & { replace?: boolean };

/** How many live toasts are shown; the others are summed up as "+N more". */
export const MAX_VISIBLE_TOASTS = 3;
/** How many are kept at most (the oldest are dropped). */
const MAX_KEPT = 30;
const TTL = { ok: 6000, info: 6000, err: 10000 };

/** Warnings worth a toast; routine ones (Unhealthy probes, image pulls in progress) only refresh the page. */
const TOAST_WARNINGS = new Set([
  'BackOff', 'Failed', 'FailedScheduling', 'FailedMount', 'FailedAttachVolume', 'FailedCreate', 'FailedCreatePodSandBox',
  'Evicted', 'OOMKilling', 'BackoffLimitExceeded', 'DeadlineExceeded',
]);
const BAD_EXIT = new Set(['OOMKilled', 'Error', 'ContainerCannotRun', 'StartError', 'DeadlineExceeded']);

const clip = (s: string, n = 160) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

/** The toast a change deserves, or null when it only refreshes the page. */
export function toastFor(c: LiveChange): ToastContent | null {
  const base = `${c.cluster}/${c.namespace}/${c.kind}/${c.name}`;
  const to = workloadUrl({ cluster: c.cluster, namespace: c.namespace, kind: c.kind, name: c.name });
  const st = c.status;
  switch (c.type) {
    case 'pod_restarted': {
      const exit = c.exitCode !== undefined && c.exitCode !== null ? ` (exit ${c.exitCode})` : '';
      const bad = BAD_EXIT.has(c.reason ?? '') || (c.exitCode ?? 0) !== 0;
      return {
        key: `${base}:restart:${c.reason ?? ''}`, to, tone: bad ? 'err' : 'info',
        title: `${c.name} restarted`, detail: `${c.pod} · ${c.container}${c.reason ? ` ${c.reason}` : ''}${exit}`,
      };
    }
    case 'rollout_started':
      return { key: `${base}:rollout`, to, tone: 'info', replace: true, title: `${c.name} is rolling out`, detail: st ? `${st.updated}/${st.desired} updated` : '' };
    case 'rollout_complete':
      return { key: `${base}:rollout`, to, tone: 'ok', replace: true, title: `${c.name} rolled out`, detail: st ? `${st.ready}/${st.desired} ready` : '' };
    case 'rollout_failed':
      return { key: `${base}:rollout`, to, tone: 'err', replace: true, title: `${c.name} rollout failed`, detail: clip(st?.message ?? '') };
    case 'event': {
      const e = c.event;
      if (!e) return null;
      if (e.type === 'Warning') {
        if (!TOAST_WARNINGS.has(e.reason)) return null;
        return { key: `${base}:event:${e.reason}`, to, tone: 'err', title: `${c.name}: ${e.reason}`, detail: clip(e.message) };
      }
      if (e.reason === 'SuccessfulCreate') return { key: `${base}:job`, to, tone: 'info', replace: true, title: `${c.name} started a job`, detail: clip(e.message) };
      if (e.reason === 'Completed') return { key: `${base}:job`, to, tone: 'ok', replace: true, title: `${c.name} job completed`, detail: clip(e.message) };
      return null;
    }
  }
  return null;
}

/**
 * Adds a toast to the list (newest last): a toast with the same key is
 * updated and moved to the end, counting repeats unless it is replaced
 * (a rollout or job moving on).
 */
export function addToast(list: LiveToast[], t: ToastContent, now: number, id: number): LiveToast[] {
  const { replace, ...content } = t;
  const old = list.find((x) => x.key === t.key && !x.leaving);
  const rest = list.filter((x) => x !== old);
  const next: LiveToast = old
    ? { ...old, ...content, count: replace ? old.count : old.count + 1, at: now }
    : { ...content, id, count: 1, at: now };
  return [...rest, next].slice(-MAX_KEPT);
}

/** Marks expired toasts as leaving and drops those that left `leaveMs` ago. Returns the same list when nothing changed. */
export function expireToasts(list: LiveToast[], now: number, leaveMs: number): LiveToast[] {
  let changed = false;
  const out: LiveToast[] = [];
  for (const t of list) {
    const end = t.at + TTL[t.tone];
    if (now >= end + leaveMs && t.leaving) {
      changed = true;
      continue;
    }
    if (now >= end && !t.leaving) {
      changed = true;
      out.push({ ...t, leaving: true });
      continue;
    }
    out.push(t);
  }
  return changed ? out : list;
}
