import { describe, expect, it } from 'vitest';
import type { GrantView } from '../api/types';
import { actionPhrase, emptyGrantForm, formFromGrant, grantInputFromForm, validateGrantForm } from './grantForm';

const NOW = Date.parse('2026-10-20T10:00:00Z');

describe('grant form', () => {
  it('validates required fields per scope kind', () => {
    const f = { ...emptyGrantForm(), scopeKind: 'argo_project' as const };
    expect(Object.keys(validateGrantForm(f, NOW)).sort()).toEqual(['argoProject', 'holder']);
    const w = { ...f, holderId: 1, scopeKind: 'workloads' as const, cluster: 'c', namespace: 'n' };
    expect(validateGrantForm(w, NOW)).toEqual({ workloads: 'Select at least one workload.' });
    expect(validateGrantForm({ ...w, workloads: ['Deployment/a'], actions: [] }, NOW).actions).toBeTruthy();
  });
  it('rejects past or malformed expiry', () => {
    const f = { ...emptyGrantForm(), holderId: 1, scopeKind: 'argo_project' as const, argoProject: 'p', expiryMode: 'date' as const, expiry: '2026-10-01T10:00' };
    expect(validateGrantForm(f, NOW).expiresAt).toMatch(/future/);
    expect(validateGrantForm({ ...f, expiry: '' }, NOW).expiresAt).toMatch(/valid/);
    expect(validateGrantForm({ ...f, expiry: '2026-11-30T18:00' }, NOW)).toEqual({});
  });
  it('builds the API input and round-trips a grant', () => {
    const g: GrantView = {
      id: 5, holder: { kind: 'user', id: 3, name: 'Sara' },
      scope: { kind: 'workloads', cluster: 'prod-1', namespace: 'payments', workloads: [{ kind: 'Deployment', name: 'payments-api' }] },
      actions: ['exec', 'logs'], expiresAt: '2026-11-30T14:30:00Z', note: ' hi ', expired: false, createdBy: 'x', createdAt: '', environment: null,
    };
    const f = formFromGrant(g);
    expect(f.expiry).toBe('2026-11-30T18:00');
    expect(grantInputFromForm(f)).toEqual({
      holder: { kind: 'user', id: 3 }, scope: g.scope, actions: ['logs', 'exec'], expiresAt: '2026-11-30T14:30:00Z', note: 'hi',
    });
  });
  it('phrases actions', () => {
    expect(actionPhrase(['exec', 'logs'])).toBe('read logs and exec');
    expect(actionPhrase(['logs', 'exec', 'restart'])).toBe('read logs, exec and restart');
    expect(actionPhrase([])).toBe('…');
  });
});
