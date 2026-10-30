import { describe, expect, it } from 'vitest';
import type { DecisionExplain, GrantView, TimeWindowView } from '../api/types';
import { buildSteps, verdict, windowCulprits } from './accessCheckSteps';

const grant: GrantView = {
  id: 1, holder: { kind: 'team', id: 1, name: 'Payments' },
  scope: { kind: 'argo_project', argoProject: 'payments' }, actions: ['logs', 'exec'], expiresAt: null, note: '',
  expired: false, createdBy: 'reza', createdAt: '2026-01-01T00:00:00Z', environment: null,
};

function dec(p: Partial<DecisionExplain>): DecisionExplain {
  return {
    allowed: false, reason: 'no_grant', environment: 'prod', grantIds: [], breakGlassId: null, reopensAt: null, closesAt: null,
    cluster: 'prod-1', namespace: 'payments-api', workload: null, action: 'exec', evaluatedAt: '2026-10-20T10:11:00Z',
    userRole: 'user', teams: ['Payments'], matchedGrants: [], ...p,
  };
}

const NOW = Date.parse('2026-10-20T10:11:00Z');
const tones = (d: DecisionExplain) => buildSteps(d, NOW).map((s) => `${s.id}:${s.tone}`);

describe('verdict', () => {
  it('allowed', () => {
    expect(verdict(dec({ allowed: true, reason: 'allowed' }), NOW).chip).toBe('ALLOWED');
    expect(verdict(dec({ allowed: true, reason: 'admin' }), NOW).text).toMatch(/Administrators/);
    expect(verdict(dec({ allowed: true, reason: 'allowed', breakGlassId: 7 }), NOW).text).toMatch(/break-glass #7/);
  });
  it('time denial with a grant mentions break-glass', () => {
    const v = verdict(dec({ reason: 'outside_window', reopensAt: '2026-10-20T11:30:00Z', matchedGrants: [grant] }), NOW);
    expect(v.chip).toBe('DENIED NOW');
    expect(v.breakGlassAvailable).toBe(true);
    expect(v.text).toMatch(/Reopens/);
    expect(v.text).toMatch(/break-glass/);
  });
  it('no_grant is a plain denial', () => {
    const v = verdict(dec({ reason: 'no_grant' }), NOW);
    expect(v.chip).toBe('DENIED');
    expect(v.breakGlassAvailable).toBe(false);
  });
});

describe('buildSteps', () => {
  it('window closed with matching grant', () => {
    const d = dec({ reason: 'outside_window', matchedGrants: [grant], reopensAt: '2026-10-20T11:30:00Z' });
    expect(tones(d)).toEqual([
      'user:ok', 'namespace:ok', 'grant:ok', 'environment:none', 'holiday:ok', 'window:err', 'breakglass:info',
    ]);
  });
  it('no grant skips the time checks', () => {
    expect(tones(dec({ reason: 'no_grant' }))).toEqual([
      'user:ok', 'namespace:ok', 'grant:err', 'environment:none', 'holiday:skip', 'window:skip',
    ]);
  });
  it('protected namespace fails first', () => {
    expect(tones(dec({ reason: 'protected_namespace' }))).toEqual([
      'user:skip', 'namespace:err', 'grant:skip', 'environment:none', 'holiday:skip', 'window:skip',
    ]);
  });
  it('holiday', () => {
    const t = tones(dec({ reason: 'holiday', matchedGrants: [grant] }));
    expect(t).toContain('holiday:err');
    expect(t).toContain('window:skip');
  });
  it('allowed through break-glass shows the override', () => {
    const s = buildSteps(dec({ allowed: true, reason: 'allowed', breakGlassId: 3, matchedGrants: [grant] }), NOW);
    expect(s.find((x) => x.id === 'breakglass')?.detail).toMatch(/#3/);
    expect(s.find((x) => x.id === 'window')?.tone).toBe('none');
  });
  it('admin skips grants and windows', () => {
    const t = tones(dec({ allowed: true, reason: 'admin', userRole: 'admin' }));
    expect(t).toEqual(['user:ok', 'namespace:ok', 'grant:none', 'environment:none', 'holiday:skip', 'window:skip']);
  });
  it('names the freeze rule responsible for the closure', () => {
    const rule: TimeWindowView = {
      id: 2, name: 'Prod change freeze', scopeKind: 'environment', scopeValue: 'prod', actions: ['exec'],
      daysOfWeek: [2], startTime: '13:00', endTime: '15:00', timezone: 'Asia/Tehran', effect: 'deny_inside', enabled: true,
    };
    const d = dec({ reason: 'outside_window', matchedGrants: [grant] }); // 13:41 Tehran, Tue
    expect(windowCulprits(d, [rule], []).deny).toEqual(['Prod change freeze']);
    const w = buildSteps(d, NOW, { rules: [rule] }).find((s) => s.id === 'window');
    expect(w?.detail).toMatch(/Prod change freeze/);
  });
});
