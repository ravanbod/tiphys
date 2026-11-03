import { describe, expect, it } from 'vitest';
import type { LiveChange } from '../api';
import { addToast, expireToasts, toastFor } from './liveToasts';
import type { LiveToast } from './liveToasts';

const at = { cluster: 'prod-1', namespace: 'shop', kind: 'Deployment' as const, name: 'api' };
const restart = (reason: string, exitCode?: number): LiveChange => ({ ...at, type: 'pod_restarted', pod: 'api-1-a', container: 'app', reason, exitCode });
const event = (type: 'Normal' | 'Warning', reason: string): LiveChange => ({
  ...at, type: 'event', event: { type, reason, message: `${reason} happened`, object: 'Pod/api-1-a', count: 1, firstSeen: '', lastSeen: '' },
});
const status = { desired: 3, updated: 1, ready: 2, available: 2, complete: false, failed: false, message: '' };

describe('toastFor', () => {
  it('names the pod, container and reason of a restart', () => {
    const t = toastFor(restart('OOMKilled', 137));
    expect(t).toMatchObject({ title: 'api restarted', detail: 'api-1-a · app OOMKilled (exit 137)', tone: 'err', to: '/c/prod-1/n/shop/w/deployments/api' });
    expect(toastFor(restart('Completed', 0))?.tone).toBe('info');
  });
  it('shows rollouts and notable events only', () => {
    expect(toastFor({ ...at, type: 'rollout_started', status })?.detail).toBe('1/3 updated');
    expect(toastFor({ ...at, type: 'rollout_complete', status: { ...status, ready: 3, complete: true } })).toMatchObject({ title: 'api rolled out', tone: 'ok' });
    expect(toastFor(event('Warning', 'BackOff'))).toMatchObject({ title: 'api: BackOff', tone: 'err' });
    expect(toastFor(event('Warning', 'Unhealthy'))).toBeNull();
    expect(toastFor(event('Normal', 'Pulled'))).toBeNull();
    for (const type of ['pod_added', 'pod_removed', 'pod_updated', 'workload'] as const) expect(toastFor({ ...at, type })).toBeNull();
  });
});

describe('addToast', () => {
  const add = (l: LiveToast[], c: LiveChange, now: number, id: number) => addToast(l, toastFor(c)!, now, id);

  it('counts repeats on one toast and moves it to the newest place', () => {
    let l = add([], restart('OOMKilled'), 1, 1);
    l = add(l, event('Warning', 'BackOff'), 2, 2);
    l = add(l, restart('OOMKilled'), 3, 3);
    expect(l.map((t) => [t.id, t.count])).toEqual([[2, 1], [1, 2]]);
    expect(l[1].at).toBe(3);
  });
  it('replaces a rollout toast as it moves on instead of counting', () => {
    let l = add([], { ...at, type: 'rollout_started', status }, 1, 1);
    l = add(l, { ...at, type: 'rollout_complete', status: { ...status, complete: true } }, 2, 2);
    expect(l).toHaveLength(1);
    expect(l[0]).toMatchObject({ id: 1, count: 1, tone: 'ok', title: 'api rolled out' });
  });
  it('keeps a bounded number of toasts', () => {
    let l: LiveToast[] = [];
    for (let i = 0; i < 50; i++) l = add(l, { ...at, name: `w${i}`, type: 'pod_restarted', pod: 'p', container: 'c', reason: 'Error' }, i, i);
    expect(l).toHaveLength(30);
    expect(l[29].title).toBe('w49 restarted');
  });
});

describe('expireToasts', () => {
  it('lets toasts leave after their time, errors later', () => {
    let l = add2();
    expect(expireToasts(l, 5000, 250)).toBe(l);
    l = expireToasts(l, 6000, 250);
    expect(l.map((t) => !!t.leaving)).toEqual([true, false]);
    l = expireToasts(l, 6300, 250);
    expect(l).toHaveLength(1);
    l = expireToasts(l, 10000, 250);
    expect(l[0].leaving).toBe(true);
  });
});

function add2(): LiveToast[] {
  const l = addToast([], toastFor({ ...at, type: 'rollout_complete', status })!, 0, 1);
  return addToast(l, toastFor(restart('OOMKilled'))!, 0, 2);
}
