import type { AccessMap, Action, DecisionInfo, GrantView, WorkloadAction, WorkloadKind } from '../api/types';
import { zonedParts, zonedToInstant } from '../lib/time';
import { DAY, HOUR, getDb } from './db';
import type { MockUser } from './db';

export const ACTIONS: Action[] = ['logs', 'exec', 'restart', 'run-job', 'sync'];
export const WORKLOAD_ACTIONS: WorkloadAction[] = ['logs', 'exec', 'restart', 'run-job'];

export function envOf(cluster: string, ns: string): string {
  const d = getDb();
  const o = d.envOverrides.find((x) => x.kind === 'namespace' && x.cluster === cluster && x.namespace === ns);
  if (o) return o.environment;
  const apps = d.apps.filter((a) => a.cluster === cluster && a.namespace === ns);
  for (const a of apps) {
    const po = d.envOverrides.find((x) => x.kind === 'argo_project' && x.argoProject === a.project && (!x.cluster || x.cluster === cluster));
    if (po) return po.environment;
  }
  return d.clusters.find((c) => c.name === cluster)?.environment ?? 'unlabeled';
}

export function projectsOf(cluster: string, ns: string): string[] {
  return getDb().apps.filter((a) => a.cluster === cluster && a.namespace === ns).map((a) => a.project);
}

export function covers(g: GrantView, cluster: string, ns: string, kind: WorkloadKind | null, name: string | null): { match: boolean; project: boolean } {
  const s = g.scope;
  if (s.kind === 'argo_project') return { match: projectsOf(cluster, ns).includes(s.argoProject ?? ''), project: true };
  if (s.kind === 'namespace') return { match: s.cluster === cluster && s.namespace === ns, project: false };
  const inWl = !kind || !name ? s.cluster === cluster && s.namespace === ns : !!s.workloads?.some((w) => w.kind === kind && w.name === name);
  return { match: s.cluster === cluster && s.namespace === ns && inWl, project: false };
}

export function userGrants(u: MockUser): GrantView[] {
  return getDb().grants.filter((g) => (g.holder.kind === 'user' ? g.holder.id === u.id : u.teamIds.includes(g.holder.id)));
}

/** Next Saturday 09:00 Asia/Tehran, as ISO. */
export function nextSaturdayNine(now = Date.now()): string {
  for (let i = 1; i <= 8; i++) {
    const z = zonedParts(now + i * DAY, 'Asia/Tehran');
    if (z.dow === 6) return new Date(zonedToInstant(z.y, z.m, z.d, 9, 0, 'Asia/Tehran')).toISOString();
  }
  return new Date(now + 3 * DAY).toISOString();
}

/** Deterministic "closed" cases so every demo shows them regardless of the real clock. */
function closedBy(cluster: string, ns: string, action: Action): { reason: 'outside_window' | 'holiday'; reopensAt: string } | null {
  if (cluster === 'staging-1' && ns === 'payments-api' && (action === 'exec' || action === 'run-job')) {
    return { reason: 'outside_window', reopensAt: nextSaturdayNine() };
  }
  if (cluster === 'prod-1' && ns === 'payments-worker' && action === 'restart') {
    return { reason: 'holiday', reopensAt: new Date(Date.now() + 20 * HOUR).toISOString() };
  }
  return null;
}

export function decide(u: MockUser, cluster: string, ns: string, kind: WorkloadKind | null, name: string | null, action: Action): DecisionInfo {
  const d = getDb();
  const env = envOf(cluster, ns);
  const base = { environment: env, grantIds: [] as number[], breakGlassId: null, reopensAt: null, closesAt: null };
  if (d.protectedNamespaces.includes(ns)) return { allowed: false, reason: 'protected_namespace', ...base };
  if (u.disabled) return { allowed: false, reason: 'user_disabled', ...base };
  if (u.role === 'auditor') return { allowed: false, reason: 'role', ...base };
  if (u.role === 'admin') return { allowed: true, reason: 'admin', ...base };
  if (kind === 'CronJob' && action === 'restart') return { allowed: false, reason: 'no_grant', ...base };
  if (kind && kind !== 'CronJob' && action === 'run-job') return { allowed: false, reason: 'no_grant', ...base };
  if (kind && action === 'sync') return { allowed: false, reason: 'no_grant', ...base };
  const now = Date.now();
  const mine = userGrants(u).filter((g) => g.actions.includes(action));
  const matched = mine.map((g) => ({ g, c: covers(g, cluster, ns, kind, name) })).filter((x) => x.c.match);
  const live = matched.filter((x) => !x.g.expired && !(x.g.expiresAt && Date.parse(x.g.expiresAt) < now));
  const usable = live.filter((x) => !(x.c.project && d.argo.stale));
  if (usable.length === 0) {
    if (live.length > 0) return { allowed: false, reason: 'argo_stale', ...base };
    if (matched.length > 0) return { allowed: false, reason: 'grant_expired', ...base };
    return { allowed: false, reason: 'no_grant', ...base };
  }
  const grantIds = usable.map((x) => x.g.id);
  const expiries = usable.map((x) => (x.g.expiresAt ? Date.parse(x.g.expiresAt) : Infinity));
  const grantClose = Math.min(...expiries);
  const closed = closedBy(cluster, ns, action);
  const bg = d.breakGlasses.find((b) => b.userId === u.id && b.active && Date.parse(b.expiresAt) > now);
  if (closed) {
    if (bg) {
      const close = Math.min(grantClose, Date.parse(bg.expiresAt));
      return { allowed: true, reason: 'allowed', environment: env, grantIds, breakGlassId: bg.id, reopensAt: null, closesAt: new Date(close).toISOString() };
    }
    return { allowed: false, reason: closed.reason, environment: env, grantIds, breakGlassId: null, reopensAt: closed.reopensAt, closesAt: null };
  }
  return { allowed: true, reason: 'allowed', environment: env, grantIds, breakGlassId: null, reopensAt: null, closesAt: Number.isFinite(grantClose) ? new Date(grantClose).toISOString() : null };
}

export function accessMap(u: MockUser, cluster: string, ns: string, kind: WorkloadKind, name: string): AccessMap {
  const out = {} as AccessMap;
  for (const a of WORKLOAD_ACTIONS) out[a] = decide(u, cluster, ns, kind, name, a);
  return out;
}

/** Actions allowed anywhere in a namespace (for app / namespace summaries). */
export function nsActions(u: MockUser, cluster: string, ns: string): Action[] {
  const d = getDb();
  const def = d.namespaces.find((n) => n.cluster === cluster && n.namespace === ns);
  const kinds = def?.workloads ?? [{ kind: 'Deployment' as WorkloadKind, name: '' }];
  const out = new Set<Action>();
  const open = (d: DecisionInfo) => d.allowed || d.reason === 'outside_window';
  for (const a of ACTIONS) {
    // sync is decided for the namespace (whole Argo CD apps), the rest per workload.
    if (a === 'sync' ? open(decide(u, cluster, ns, null, null, a)) : kinds.some((w) => open(decide(u, cluster, ns, w.kind, w.name || null, a)))) out.add(a);
  }
  return ACTIONS.filter((a) => out.has(a));
}

export function userCanSeeNs(u: MockUser, cluster: string, ns: string): boolean {
  return nsActions(u, cluster, ns).length > 0;
}
