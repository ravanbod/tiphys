import { describe, expect, it } from 'vitest';
import type { AuditEvent } from '../api/types';
import { eventTitle, podMiddleEllipsis, podSuffix, reasonText, targetFull, targetLine } from './auditLabels';

const base: AuditEvent = {
  id: 1, at: '2026-10-20T10:00:00Z', userId: 1, username: 'sara', eventType: 'exec.start', action: 'exec',
  cluster: 'prod-1', namespace: 'payments', workloadKind: 'Deployment', workloadName: 'payments-api',
  pod: 'payments-api-7d9c6b5f4-2kq8x', container: 'app', environment: 'prod', result: 'succeeded', reason: null,
  breakGlassId: null, sessionId: 's1', details: null, sourceIp: null, userAgent: null,
};

describe('auditLabels', () => {
  it('titles', () => {
    expect(eventTitle(base)).toBe('Exec started');
    expect(eventTitle({ eventType: 'weird.thing_x', details: null })).toBe('Weird thing x');
    expect(eventTitle({ eventType: 'admin.change', details: { entity: 'grant', operation: 'update' } })).toBe('Grant updated');
    expect(eventTitle({ eventType: 'admin.change', details: null })).toBe('Admin change');
  });
  it('pod helpers', () => {
    expect(podSuffix('payments-api-7d9c6b5f4-2kq8x')).toBe('…2kq8x');
    expect(podSuffix('single')).toBe('single');
    expect(podMiddleEllipsis('payments-api-7d9c6b5f4-2kq8x')).toBe('payments-api-…2kq8x');
    expect(podMiddleEllipsis('short-pod')).toBe('short-pod');
  });
  it('target lines', () => {
    expect(targetLine(base)).toBe('prod-1 / payments-api / …2kq8x');
    expect(targetLine({ ...base, pod: null, workloadName: null, namespace: null, cluster: null, details: { entity: 'time_window', id: 3 } })).toBe('time window 3');
    expect(targetLine({ ...base, cluster: null, workloadName: null, pod: null, namespace: null })).toBeNull();
    expect(targetFull(base)).toBe('prod-1 / payments / deployment/payments-api / payments-api-7d9c6b5f4-2kq8x');
  });
  it('reasons', () => {
    expect(reasonText('outside_window')).toBe('outside time window');
    expect(reasonText('custom_reason')).toBe('custom reason');
    expect(reasonText(null)).toBe('');
  });
});
