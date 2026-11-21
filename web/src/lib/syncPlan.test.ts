import { describe, expect, it } from 'vitest';
import type { ArgoPlanResource, ArgoSyncPlan } from '../api/types';
import { countsText, initialForm, otherOptions, planCounts, prunePolicy, refKey, retryValid, syncInput, toPrune, toggleOption, withPrunePolicy } from './syncPlan';

const res = (kind: string, name: string, state: ArgoPlanResource['state'], prunable = state === 'extra'): ArgoPlanResource => ({
  group: '', kind, namespace: 'payments', name, state, diff: '', hidden: false, truncated: false, prunable,
});
const plan = (over: Partial<ArgoSyncPlan> = {}): ArgoSyncPlan => ({
  revision: null, targetRevision: 'main', autoSync: false,
  decision: { allowed: true, reason: 'allowed', environment: 'prod', grantIds: [1], breakGlassId: null, reopensAt: null, closesAt: null },
  decisionAction: 'sync', decisionNamespace: 'payments', adminOnlyResources: [], truncated: false,
  resources: [res('Deployment', 'api', 'changed'), res('ConfigMap', 'new', 'missing'), res('ConfigMap', 'old', 'extra'),
    res('ConfigMap', 'kept', 'extra', false), res('Service', 'api', 'synced')],
  defaults: { syncOptions: ['CreateNamespace=true', 'SkipDryRunOnMissingResource=true'], retry: null },
  ...over,
});

describe('sync plan', () => {
  it('sends nothing for a plain sync with the app\'s own settings', () => {
    const p = plan();
    expect(syncInput(p, initialForm(p))).toEqual({});
  });

  it('sends what differs', () => {
    const p = plan({ revision: 'v1.2.0' });
    const f = initialForm(p);
    f.prune = true;
    f.force = true;
    f.options = toggleOption(f.options, 'PruneLast=true');
    f.selected = [refKey(p.resources[0]), refKey(p.resources[2])];
    f.retryTouched = true;
    f.retry.on = true;
    expect(syncInput(p, f)).toEqual({
      revision: 'v1.2.0', prune: true, force: true,
      syncOptions: ['CreateNamespace=true', 'SkipDryRunOnMissingResource=true', 'PruneLast=true'],
      retry: { limit: 2, backoff: { duration: '5s', factor: 2, maxDuration: '3m' } },
      resources: [{ group: '', kind: 'Deployment', namespace: 'payments', name: 'api' }, { group: '', kind: 'ConfigMap', namespace: 'payments', name: 'old' }],
    });
    f.retry.on = false;
    expect(syncInput(p, f).retry).toEqual({ limit: 0, backoff: null });
  });

  it('starts from the app\'s retry', () => {
    const f = initialForm(plan({ defaults: { syncOptions: [], retry: { limit: 5, backoff: { duration: '10s', factor: 3, maxDuration: '' } } } }));
    expect(f.retry).toEqual({ on: true, limit: 5, duration: '10s', factor: 3, maxDuration: '3m' });
  });

  it('prunes only what is selected and prunable', () => {
    const p = plan();
    const f = initialForm(p);
    expect(toPrune(p, f)).toEqual([]);
    expect(countsText(planCounts(p, f), false)).toBe('1 changed · 1 created · 1 in sync · 2 left in the cluster');
    f.prune = true;
    expect(toPrune(p, f).map((r) => r.name)).toEqual(['old']);
    expect(countsText(planCounts(p, f), false)).toBe('1 changed · 1 created · 1 in sync · 1 pruned · 1 left in the cluster');
    f.selected = [refKey(p.resources[0])];
    expect(toPrune(p, f)).toEqual([]);
    expect(countsText(planCounts(p, f), false)).toBe('1 changed');
    f.selected = [];
    expect(countsText(planCounts(p, f), false)).toBe('Nothing selected');
  });

  it('keeps one prune propagation policy and the app\'s other options', () => {
    let o = withPrunePolicy(['CreateNamespace=true', 'Foo=bar'], 'orphan');
    expect(prunePolicy(o)).toBe('orphan');
    o = withPrunePolicy(o, 'background');
    expect(o).toEqual(['CreateNamespace=true', 'Foo=bar', 'PrunePropagationPolicy=background']);
    expect(otherOptions(o)).toEqual(['Foo=bar']);
    expect(withPrunePolicy(o, '')).toEqual(['CreateNamespace=true', 'Foo=bar']);
  });

  it('checks the retry', () => {
    const r = { on: true, limit: 2, duration: '5s', factor: 2, maxDuration: '3m' };
    expect(retryValid(r)).toBe(true);
    expect(retryValid({ ...r, limit: 11 })).toBe(false);
    expect(retryValid({ ...r, duration: '5 s' })).toBe(false);
    expect(retryValid({ ...r, on: false, limit: 0 })).toBe(true);
  });
});
