import { describe, expect, it } from 'vitest';
import type { DecisionInfo } from '../api/types';
import { closesInSeconds, closesSoonLabel, explainDecision, grantedOrClosed, reopensLabel } from './decision';

const base: DecisionInfo = {
  allowed: false, reason: 'no_grant', environment: 'prod', grantIds: [], breakGlassId: null, reopensAt: null, closesAt: null,
};
const now = Date.parse('2026-10-21T18:01:00Z');

describe('explainDecision', () => {
  it('returns null when allowed', () => {
    expect(explainDecision({ ...base, allowed: true, reason: 'allowed' }, { action: 'exec', now })).toBeNull();
    expect(explainDecision(undefined, { action: 'exec' })).toBeNull();
  });
  it('no_grant offers a request', () => {
    const e = explainDecision(base, { action: 'exec', target: 'payments-api', now })!;
    expect(e.text).toBe("You don't have exec access to payments-api.");
    expect(e.cta).toBe('request');
  });
  it('outside_window shows reopen time and offers break-glass', () => {
    const e = explainDecision(
      { ...base, reason: 'outside_window', reopensAt: '2026-10-24T09:00:00Z' },
      { action: 'exec', now, tz: 'UTC' },
    )!;
    expect(e.text).toBe('Exec in prod is closed until Sat 09:00.');
    expect(e.cta).toBe('breakglass');
  });
  it('no break-glass CTA for non-users', () => {
    const e = explainDecision({ ...base, reason: 'holiday' }, { action: 'restart', now, canBreakGlass: false })!;
    expect(e.text).toBe('Closed today: holiday.');
    expect(e.cta).toBe('none');
  });
  it('terminal reasons have no CTA', () => {
    for (const reason of ['protected_namespace', 'user_disabled', 'role', 'argo_stale'] as const) {
      expect(explainDecision({ ...base, reason }, { action: 'logs', now })!.cta).toBe('none');
    }
  });
  it('grant_expired offers request', () => {
    expect(explainDecision({ ...base, reason: 'grant_expired' }, { action: 'exec', now })!.cta).toBe('request');
  });
});

describe('reopens / closes', () => {
  it('reopensLabel', () => {
    expect(reopensLabel({ ...base, reason: 'outside_window', reopensAt: '2026-10-24T09:00:00Z' }, now, 'UTC')).toBe('Reopens Sat 09:00');
    expect(reopensLabel(base, now)).toBe('');
  });
  it('closesIn counts down only for allowed decisions', () => {
    const allowed: DecisionInfo = { ...base, allowed: true, reason: 'allowed', closesAt: '2026-10-21T18:06:00Z' };
    expect(closesInSeconds(allowed, now)).toBe(300);
    expect(closesSoonLabel(allowed, now)).toBe('Closes in 5 min');
    expect(closesSoonLabel({ ...allowed, closesAt: '2026-10-30T00:00:00Z' }, now)).toBe('');
    expect(closesInSeconds(base, now)).toBeNull();
  });
});

describe('grantedOrClosed', () => {
  it('is true when allowed or only a window or holiday denies', () => {
    const d = (allowed: boolean, reason: DecisionInfo['reason']): DecisionInfo => ({ ...base, allowed, reason });
    expect(grantedOrClosed(d(true, 'allowed'))).toBe(true);
    expect(grantedOrClosed(d(false, 'outside_window'))).toBe(true);
    expect(grantedOrClosed(d(false, 'holiday'))).toBe(true);
    expect(grantedOrClosed(d(false, 'no_grant'))).toBe(false);
    expect(grantedOrClosed(d(false, 'grant_expired'))).toBe(false);
    expect(grantedOrClosed(undefined)).toBe(false);
  });
});
