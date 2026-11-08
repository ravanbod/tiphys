import { describe, expect, it } from 'vitest';
import type { AccessRequest } from '../api';
import { NO_REQUEST_FILTERS, activeRequestFilters, cleanRequestFilters, matchesRequest } from './requestFilters';

const req = (p: Partial<AccessRequest> = {}): AccessRequest => ({
  id: 1, userId: 2, username: 'sara.moradi',
  scope: { kind: 'workloads', cluster: 'prod-1', namespace: 'payments-api', workloads: [{ kind: 'Deployment', name: 'api' }] },
  actions: ['logs', 'exec'], durationHours: 8, reason: 'Incident INC-42', status: 'pending',
  decidedBy: null, decidedAt: null, decisionNote: null, resultingGrantId: null, createdAt: '2026-11-07T10:00:00Z', team: null, batchId: null,
  ...p,
});
const f = (p: Partial<typeof NO_REQUEST_FILTERS>) => ({ ...NO_REQUEST_FILTERS, ...p });

describe('request filters', () => {
  it('passes everything without filters', () => {
    expect(matchesRequest(req(), NO_REQUEST_FILTERS, null, 'Sara Moradi')).toBe(true);
  });
  it('filters by cluster, environment and action', () => {
    expect(matchesRequest(req(), f({ cluster: 'prod-1' }), 'prod', '')).toBe(true);
    expect(matchesRequest(req(), f({ cluster: 'prod-2' }), 'prod', '')).toBe(false);
    expect(matchesRequest(req({ scope: { kind: 'argo_project', argoProject: 'payments' } }), f({ cluster: 'prod-1' }), null, '')).toBe(false);
    expect(matchesRequest(req(), f({ env: 'prod' }), 'prod', '')).toBe(true);
    expect(matchesRequest(req(), f({ env: 'prod' }), null, '')).toBe(false);
    expect(matchesRequest(req(), f({ action: 'exec' }), null, '')).toBe(true);
    expect(matchesRequest(req(), f({ action: 'restart' }), null, '')).toBe(false);
  });
  it('filters by who it is for', () => {
    const team = req({ team: { id: 1, name: 'Payments' } });
    expect(matchesRequest(team, f({ for: 'team' }), null, '')).toBe(true);
    expect(matchesRequest(team, f({ for: 'user' }), null, '')).toBe(false);
    expect(matchesRequest(req(), f({ for: 'team' }), null, '')).toBe(false);
  });
  it('searches every word in names, scope and reason', () => {
    const team = req({ team: { id: 1, name: 'Payments' } });
    expect(matchesRequest(req(), f({ q: 'sara' }), null, 'Sara Moradi')).toBe(true);
    expect(matchesRequest(req(), f({ q: 'Moradi inc-42' }), null, 'Sara Moradi')).toBe(true);
    expect(matchesRequest(req(), f({ q: 'api payments-api' }), null, '')).toBe(true);
    expect(matchesRequest(team, f({ q: 'payments sara' }), null, '')).toBe(true);
    expect(matchesRequest(req(), f({ q: 'kian' }), null, 'Sara Moradi')).toBe(false);
  });
  it('cleans saved filters and counts the active ones', () => {
    expect(cleanRequestFilters(null)).toEqual(NO_REQUEST_FILTERS);
    expect(cleanRequestFilters({ q: 'x', cluster: 3, for: 'admin', extra: 1 })).toEqual(f({ q: 'x' }));
    expect(activeRequestFilters(f({ q: '  ', cluster: 'prod-1', for: 'team' }))).toBe(2);
  });
});
