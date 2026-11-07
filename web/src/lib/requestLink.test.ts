import { describe, expect, it } from 'vitest';
import { requestPrefill, requestUrl } from './requestLink';

const parse = (url: string) => requestPrefill(new URLSearchParams(url.split('?')[1]));

describe('request links', () => {
  it('without a target only opens the form', () => {
    expect(requestUrl()).toBe('/access?request=1');
    expect(parse(requestUrl())).toBeNull();
  });
  it('round-trips a namespace action', () => {
    const p = { cluster: 'prod-1', namespace: 'shop-prod', action: 'sync' as const };
    expect(parse(requestUrl(p))).toEqual(p);
  });
  it('round-trips a workload action; sync and rollback never name a workload', () => {
    const w = { kind: 'CronJob' as const, name: 'nightly' };
    expect(parse(requestUrl({ cluster: 'c', namespace: 'n', action: 'run-job', workload: w }))).toEqual({ cluster: 'c', namespace: 'n', action: 'run-job', workload: w });
    expect(parse(requestUrl({ cluster: 'c', namespace: 'n', action: 'sync', workload: w }))?.workload).toBeUndefined();
    expect(parse(requestUrl({ cluster: 'c', namespace: 'n', action: 'rollback', workload: w }))).toEqual({ cluster: 'c', namespace: 'n', action: 'rollback' });
  });
  it('drops what it does not understand', () => {
    expect(parse('/access?request=1&cluster=c&namespace=n&action=delete')).toBeNull();
    expect(parse('/access?request=1&cluster=c&namespace=n&action=exec&kind=Pod&name=x')).toEqual({ cluster: 'c', namespace: 'n', action: 'exec' });
  });
});
