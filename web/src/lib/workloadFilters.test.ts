import { describe, expect, it } from 'vitest';
import { filtersOf, restoredQuery } from './workloadFilters';

const q = (s: string) => new URLSearchParams(s);

describe('workload filters', () => {
  it('keeps only the filter parameters', () => {
    expect(filtersOf(q('expand=x&env=prod&q=pay&foo=1'))).toBe('q=pay&env=prod');
    expect(filtersOf(q('expand=x&env='))).toBe('');
  });
  it('restores saved filters on a bare URL', () => {
    expect(restoredQuery(q(''), 'q=pay&env=prod')?.toString()).toBe('q=pay&env=prod');
  });
  it('leaves a URL with its own filters or an expand alone', () => {
    expect(restoredQuery(q('cluster=prod-1'), 'q=pay')).toBeNull();
    expect(restoredQuery(q('expand=prod-1/ns/app'), 'q=pay')).toBeNull();
    expect(restoredQuery(q(''), '')).toBeNull();
    expect(restoredQuery(q(''), 'foo=1')).toBeNull();
  });
});
