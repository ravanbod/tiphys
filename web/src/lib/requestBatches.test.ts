import { describe, expect, it } from 'vitest';
import type { AccessRequest } from '../api';
import { groupKey, groupRequests, groupStatus, statusSummary } from './requestBatches';

const req = (id: number, ns: string, batchId: string | null, status: AccessRequest['status'] = 'pending'): AccessRequest => ({
  id, userId: 1, username: 'sara', scope: { kind: 'namespace', cluster: 'prod-1', namespace: ns }, actions: ['logs'],
  durationHours: null, reason: 'x', status, decidedBy: null, decidedAt: null, decisionNote: null, resultingGrantId: null,
  createdAt: '2026-11-07T10:00:00Z', team: null, batchId,
});

describe('request batches', () => {
  it('groups a batch once, in first-seen order, namespaces sorted', () => {
    const g = groupRequests([req(5, 'zeta', 'b1'), req(4, 'solo', null), req(3, 'alpha', 'b1')]);
    expect(g.map((x) => x.map((r) => r.id))).toEqual([[3, 5], [4]]);
    expect(g.map(groupKey)).toEqual(['b:b1', 'r:4']);
  });
  it('summarizes the statuses', () => {
    const mixed = [req(1, 'a', 'b', 'approved'), req(2, 'b', 'b', 'rejected'), req(3, 'c', 'b', 'approved')];
    expect(statusSummary(mixed)).toBe('Approved 2 · Rejected 1');
    expect(groupStatus(mixed)).toBe('approved');
    expect(statusSummary([req(1, 'a', 'b', 'rejected'), req(2, 'b', 'b', 'rejected')])).toBe('Rejected');
    expect(groupStatus([req(1, 'a', 'b', 'rejected'), req(2, 'b', 'b', 'pending')])).toBe('pending');
  });
});
