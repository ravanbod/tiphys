import { describe, expect, it } from 'vitest';
import {
  EMPTY_AUDIT_FILTERS, filtersFromParams, filtersToParams, isDefaultFilters, moreFilterCount, serverFilterKey, toApiFilters,
} from './auditFilters';

const NOW = Date.parse('2026-10-20T12:00:00Z');

describe('filtersFromParams / filtersToParams', () => {
  it('defaults to last 24h with nothing in the URL', () => {
    const f = filtersFromParams(new URLSearchParams(''));
    expect(f).toEqual(EMPTY_AUDIT_FILTERS);
    expect(filtersToParams(f).toString()).toBe('');
    expect(isDefaultFilters(f)).toBe(true);
  });
  it('round-trips filters', () => {
    const p = new URLSearchParams('range=7d&user=sara&environment=prod&bg=1&q=exec&namespace=pay');
    const f = filtersFromParams(p);
    expect(f.range).toBe('7d');
    expect(f.user).toBe('sara');
    expect(f.breakGlassOnly).toBe(true);
    expect(f.q).toBe('exec');
    const back = filtersFromParams(filtersToParams(f));
    expect(back).toEqual(f);
  });
  it('from/to imply custom range and are dropped for presets', () => {
    const f = filtersFromParams(new URLSearchParams('from=2026-10-01T08:00&to=2026-10-02T08:00'));
    expect(f.range).toBe('custom');
    expect(filtersToParams(f).get('from')).toBe('2026-10-01T08:00');
    expect(filtersToParams({ ...f, range: '1h' }).has('from')).toBe(false);
  });
  it('ignores an unknown range', () => {
    expect(filtersFromParams(new URLSearchParams('range=bogus')).range).toBe('24h');
  });
});

describe('toApiFilters', () => {
  it('anchors relative ranges at now', () => {
    expect(toApiFilters({ ...EMPTY_AUDIT_FILTERS, range: '1h' }, NOW).from).toBe('2026-10-20T11:00:00.000Z');
    expect(toApiFilters(EMPTY_AUDIT_FILTERS, NOW).from).toBe('2026-10-19T12:00:00.000Z');
    expect(toApiFilters({ ...EMPTY_AUDIT_FILTERS, range: '30d' }, NOW).from).toBe('2026-09-20T12:00:00.000Z');
  });
  it('converts custom range to ISO and drops bad dates', () => {
    const f = { ...EMPTY_AUDIT_FILTERS, range: 'custom' as const, from: '2026-10-01T08:00', to: 'garbage' };
    const a = toApiFilters(f, NOW);
    expect(a.from).toBe(new Date('2026-10-01T08:00').toISOString());
    expect(a.to).toBeUndefined();
  });
  it('maps text filters, trims, and adds paging', () => {
    const a = toApiFilters({ ...EMPTY_AUDIT_FILTERS, user: ' sara ', result: 'denied', q: 'x', breakGlassOnly: true }, NOW, { limit: 50, cursor: 'c1' });
    expect(a).toMatchObject({ user: 'sara', result: 'denied', q: 'x', limit: 50, cursor: 'c1' });
    expect(a.breakGlassId).toBeUndefined();
    expect(a.breakGlass).toBe(true);
    expect(a.action).toBeUndefined();
    expect(toApiFilters(EMPTY_AUDIT_FILTERS, NOW).breakGlass).toBeUndefined();
  });
});

describe('helpers', () => {
  it('counts hidden filters', () => {
    expect(moreFilterCount({ ...EMPTY_AUDIT_FILTERS, cluster: 'a', namespace: 'b' })).toBe(2);
    expect(moreFilterCount(EMPTY_AUDIT_FILTERS)).toBe(0);
  });
  it('server key changes with every filter, break-glass too (the server applies it)', () => {
    expect(serverFilterKey({ ...EMPTY_AUDIT_FILTERS, breakGlassOnly: true })).not.toBe(serverFilterKey(EMPTY_AUDIT_FILTERS));
    expect(serverFilterKey({ ...EMPTY_AUDIT_FILTERS, user: 'x' })).not.toBe(serverFilterKey(EMPTY_AUDIT_FILTERS));
  });
});
