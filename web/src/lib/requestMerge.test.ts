import { describe, expect, it } from 'vitest';
import type { ScopeView } from '../api';
import { mergeTarget, sameScope } from './requestMerge';
import type { MergeableGrant } from './requestMerge';

const now = Date.parse('2026-11-02T12:00:00Z');
const at = (h: number) => new Date(now + h * 3600_000).toISOString();
const ns: ScopeView = { kind: 'namespace', cluster: 'prod-1', namespace: 'shop' };
const wl = (...names: string[]): ScopeView => ({ kind: 'workloads', cluster: 'prod-1', namespace: 'shop', workloads: names.map((name) => ({ kind: 'Deployment', name })) });
const own = (id: number, scope: ScopeView, expiresAt: string | null, expired = false): MergeableGrant => ({ id, holder: { kind: 'user' }, scope, actions: ['logs'], expiresAt, expired });

describe('sameScope', () => {
  it('compares workloads as a set', () => {
    expect(sameScope(wl('a', 'b'), wl('b', 'a'))).toBe(true);
    expect(sameScope(wl('a'), wl('a', 'b'))).toBe(false);
    expect(sameScope(ns, { ...ns, namespace: 'other' })).toBe(false);
  });
});

describe('mergeTarget', () => {
  const grants = [own(1, ns, at(48)), own(2, ns, at(24)), own(3, ns, at(-1)), own(4, ns, null), { ...own(5, ns, null), holder: { kind: 'team' as const } }, own(6, wl('a', 'b'), at(24))];
  const pick = (scope: ScopeView, hours: number | null) => mergeTarget(grants, scope, hours === null ? null : now + hours * 3600_000, now)?.id ?? null;
  it('picks the longest own grant that does not outlast the new access', () => {
    expect(pick(ns, 72)).toBe(1);
    expect(pick(ns, 24)).toBe(2);
    expect(pick(ns, 8)).toBeNull();
    expect(pick(ns, null)).toBe(4);
    expect(pick(wl('b', 'a'), 24)).toBe(6);
    expect(pick(wl('a'), 24)).toBeNull();
  });
  it('merges a team request into the team grant only', () => {
    expect(mergeTarget(grants, ns, null, now, 'team')?.id).toBe(5);
    expect(mergeTarget(grants.filter((g) => g.holder.kind === 'user'), ns, null, now, 'team')).toBeNull();
  });
});
