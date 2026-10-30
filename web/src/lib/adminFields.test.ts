import { describe, expect, it } from 'vitest';
import { fieldError, unmatchedFields } from './adminFields';

describe('field errors', () => {
  const f = { actions: 'pick one', 'scope.cluster': 'unknown cluster', weird: 'x' };
  it('finds exact and nested keys', () => {
    expect(fieldError(f, 'actions')).toBe('pick one');
    expect(fieldError(f, 'cluster', 'scope.cluster')).toBe('unknown cluster');
    expect(fieldError(f, 'scope')).toBe('unknown cluster');
    expect(fieldError(f, 'note')).toBeUndefined();
  });
  it('lists unmatched', () => {
    expect(unmatchedFields(f, ['actions', 'scope'])).toEqual([['weird', 'x']]);
  });
});
