import { describe, expect, it } from 'vitest';
import { exactOption, filterOptions, matchesQuery } from './comboFilter';

const opts = [
  { value: 'notifications-api' },
  { value: 'payments-api' },
  { value: 'payments-worker' },
  { value: 'team:1', label: 'Payments', hint: 'Team' },
  { value: 'user:2', label: 'Sara Moradi', hint: 'sara.moradi' },
];
const values = (q: string) => filterOptions(opts, q).map((o) => o.value);

describe('combo box filtering', () => {
  it('keeps everything for an empty query', () => {
    expect(values('  ')).toHaveLength(opts.length);
  });
  it('matches every word anywhere, label prefixes first', () => {
    expect(values('api')).toEqual(['notifications-api', 'payments-api']);
    expect(values('pay')).toEqual(['payments-api', 'payments-worker', 'team:1']);
    expect(values('payments team')).toEqual(['team:1']);
    expect(values('sara.moradi')).toEqual(['user:2']);
    expect(values('nothing')).toEqual([]);
  });
  it('finds the option typed exactly', () => {
    expect(exactOption(opts, 'Payments-API ')?.value).toBe('payments-api');
    expect(exactOption(opts, 'sara moradi')?.value).toBe('user:2');
    expect(exactOption(opts, 'pay')).toBeUndefined();
  });
});

describe('matchesQuery', () => {
  it('needs every word, any case', () => {
    expect(matchesQuery('Deployment payments-api', 'pay DEPLOY')).toBe(true);
    expect(matchesQuery('Deployment payments-api', 'pay cron')).toBe(false);
    expect(matchesQuery('anything', ' ')).toBe(true);
  });
});
