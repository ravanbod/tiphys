import type { AccessRequest } from '../api';

/**
 * Requests as people see them: a single request alone, a batch (shared
 * batchId) as one group, its namespaces in name order. Groups keep the order
 * in which their first request appears.
 */
export function groupRequests(reqs: AccessRequest[]): AccessRequest[][] {
  const out: AccessRequest[][] = [];
  const byBatch = new Map<string, AccessRequest[]>();
  for (const r of reqs) {
    if (!r.batchId) {
      out.push([r]);
      continue;
    }
    let g = byBatch.get(r.batchId);
    if (!g) {
      g = [];
      byBatch.set(r.batchId, g);
      out.push(g);
    }
    g.push(r);
  }
  for (const g of byBatch.values()) g.sort((a, b) => (a.scope.namespace ?? '').localeCompare(b.scope.namespace ?? ''));
  return out;
}

/** A stable key for a group: its batch, else its request. */
export const groupKey = (g: AccessRequest[]) => (g[0].batchId ? `b:${g[0].batchId}` : `r:${g[0].id}`);

/** Pending while any request of the group is (a whole batch is decided at once). */
export const groupPending = (g: AccessRequest[]) => g.some((r) => r.status === 'pending');

const LABEL: Record<AccessRequest['status'], string> = { pending: 'Pending', approved: 'Approved', rejected: 'Rejected', expired: 'Expired' };

/** "Approved 3 · Rejected 2", or a single status's label when all agree. */
export function statusSummary(g: AccessRequest[]): string {
  const counts = new Map<AccessRequest['status'], number>();
  for (const r of g) counts.set(r.status, (counts.get(r.status) ?? 0) + 1);
  if (counts.size === 1) return LABEL[g[0].status];
  return (['approved', 'rejected', 'pending', 'expired'] as const)
    .filter((s) => counts.has(s)).map((s) => `${LABEL[s]} ${counts.get(s)}`).join(' · ');
}

/** The status a group's chip shows: approved when any was, else the shared one. */
export function groupStatus(g: AccessRequest[]): AccessRequest['status'] {
  if (groupPending(g)) return 'pending';
  return g.some((r) => r.status === 'approved') ? 'approved' : g[0].status;
}
