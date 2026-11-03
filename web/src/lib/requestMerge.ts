import type { Action, ScopeView } from '../api';

/** The parts of a grant (GrantView or MyAccessGrant) the merge rule reads. */
export interface MergeableGrant {
  id: number;
  holder: { kind: 'team' | 'user' };
  scope: ScopeView;
  actions: Action[];
  expiresAt: string | null;
  expired: boolean;
}

/** Whether two scopes cover exactly the same targets (workloads as a set). */
export function sameScope(a: ScopeView, b: ScopeView): boolean {
  if (a.kind !== b.kind || (a.argoProject ?? '') !== (b.argoProject ?? '') || (a.environment ?? '') !== (b.environment ?? '')
    || (a.cluster ?? '') !== (b.cluster ?? '') || (a.namespace ?? '') !== (b.namespace ?? '')) return false;
  const set = (s: ScopeView) => new Set((s.workloads ?? []).map((w) => `${w.kind}/${w.name}`));
  const x = set(a);
  const y = set(b);
  return x.size === y.size && [...x].every((k) => y.has(k));
}

const end = (iso: string | null) => (iso === null ? Infinity : Date.parse(iso));

/**
 * The grant approving access on scope until expiresAt (ms, null: permanent)
 * adds to, as application.mergeTarget decides it: the requester's own
 * unexpired grant on exactly that scope that ends no later than the new
 * access, the longest-lasting of them; null when a new grant is created.
 * Pass only the requester's grants.
 */
export function mergeTarget<G extends MergeableGrant>(grants: G[], scope: ScopeView, expiresAt: number | null, now: number): G | null {
  const until = expiresAt ?? Infinity;
  let best: G | null = null;
  for (const g of grants) {
    if (g.holder.kind !== 'user' || g.expired || end(g.expiresAt) <= now || !sameScope(g.scope, scope)) continue;
    if (end(g.expiresAt) > until) continue; // it outlasts the new access
    if (!best || end(g.expiresAt) > end(best.expiresAt)) best = g;
  }
  return best;
}
