import type { AccessRequest } from '../api';

/**
 * Filters of the admin's Access requests list. They are applied in the
 * browser to the loaded list and remembered per admin (localStorage), so they
 * are still set on the next visit, also in a new tab.
 */
export interface RequestFilters {
  /** Free text: requester, team, scope names, cluster, reason. */
  q: string;
  cluster: string;
  env: string;
  action: string;
  /** Who it is for: '' anyone, 'user' the requester, 'team' a manager's team. */
  for: '' | 'user' | 'team';
}

export const NO_REQUEST_FILTERS: RequestFilters = { q: '', cluster: '', env: '', action: '', for: '' };

export const requestFiltersKey = (userId: number) => `tiphys.requestFilters.${userId}`;

/** Saved filters, with anything unknown or malformed dropped. */
export function cleanRequestFilters(v: unknown): RequestFilters {
  const o = (v && typeof v === 'object' ? v : {}) as Record<string, unknown>;
  const s = (k: string) => (typeof o[k] === 'string' ? (o[k] as string) : '');
  const f = s('for');
  return { q: s('q'), cluster: s('cluster'), env: s('env'), action: s('action'), for: f === 'user' || f === 'team' ? f : '' };
}

export function activeRequestFilters(f: RequestFilters): number {
  return [f.q.trim(), f.cluster, f.env, f.action, f.for].filter(Boolean).length;
}

/** Whether the request passes the filters; `env` is the scope's environment, `name` the requester's display name. */
export function matchesRequest(r: AccessRequest, f: RequestFilters, env: string | null, name: string): boolean {
  if (f.cluster && r.scope.cluster !== f.cluster) return false;
  if (f.env && env !== f.env) return false;
  if (f.action && !r.actions.includes(f.action as AccessRequest['actions'][number])) return false;
  if (f.for === 'team' && !r.team) return false;
  if (f.for === 'user' && r.team) return false;
  const words = f.q.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const s = r.scope;
  const text = [
    r.username, name, r.team?.name, s.argoProject, s.cluster, s.namespace, s.environment,
    ...(s.workloads ?? []).map((w) => w.name), r.reason,
  ].filter(Boolean).join(' ').toLowerCase();
  return words.every((w) => text.includes(w));
}
