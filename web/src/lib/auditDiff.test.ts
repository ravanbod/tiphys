import { describe, expect, it } from 'vitest';
import { adminDiff, diffObjects, fmtDiffValue } from './auditDiff';

describe('fmtDiffValue', () => {
  it('formats scalars, arrays and objects', () => {
    expect(fmtDiffValue('x')).toBe('x');
    expect(fmtDiffValue(3)).toBe('3');
    expect(fmtDiffValue(null)).toBe('null');
    expect(fmtDiffValue(['logs', 'exec'])).toBe('logs, exec');
    expect(fmtDiffValue([])).toBe('(none)');
    expect(fmtDiffValue({ a: 1 })).toBe('{"a":1}');
  });
});

describe('diffObjects', () => {
  it('emits minus/plus for changed keys only', () => {
    const lines = diffObjects(
      { actions: ['logs', 'exec'], note: 'same', expiresAt: null },
      { actions: ['logs', 'exec', 'restart'], note: 'same', expiresAt: null },
    );
    expect(lines.map((l) => l.text)).toEqual(['- actions: logs, exec', '+ actions: logs, exec, restart']);
    expect(lines.map((l) => l.kind)).toEqual(['minus', 'plus']);
  });
  it('handles added and removed keys', () => {
    const lines = diffObjects({ a: 1 }, { b: 2 });
    expect(lines.map((l) => l.text)).toEqual(['- a: 1', '+ b: 2']);
  });
  it('treats null before (create) and null after (delete)', () => {
    expect(diffObjects(null, { name: 'x' }).map((l) => l.text)).toEqual(['+ name: x']);
    expect(diffObjects({ name: 'x' }, null).map((l) => l.text)).toEqual(['- name: x']);
  });
  it('falls back to whole values for scalars', () => {
    expect(diffObjects('2026-01-01', undefined).map((l) => l.text)).toEqual(['- 2026-01-01']);
  });
});

describe('adminDiff', () => {
  it('returns null without before/after', () => {
    expect(adminDiff(null)).toBeNull();
    expect(adminDiff({ entity: 'grant' })).toBeNull();
  });
  it('diffs before/after from details', () => {
    const d = adminDiff({ entity: 'grant', before: { a: 1 }, after: { a: 2 } });
    expect(d?.map((l) => l.text)).toEqual(['- a: 1', '+ a: 2']);
  });
});
