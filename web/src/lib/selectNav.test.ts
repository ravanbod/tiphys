import { describe, expect, it } from 'vitest';
import { edgeIndex, moveIndex, typeaheadIndex } from './selectNav';

const off = (...idx: number[]) => Array.from({ length: 6 }, (_, i) => idx.includes(i));

describe('select keyboard movement', () => {
  it('moves by steps, skips disabled options and stops at the ends', () => {
    expect(moveIndex(off(), 0, 1)).toBe(1);
    expect(moveIndex(off(1), 0, 1)).toBe(2);
    expect(moveIndex(off(), 5, 1)).toBe(5);
    expect(moveIndex(off(), 0, -1)).toBe(0);
    expect(moveIndex(off(), 1, 10)).toBe(5);
    expect(moveIndex(off(5), 3, 10)).toBe(4);
    expect(moveIndex(off(0), 2, -10)).toBe(1);
    expect(moveIndex([], 0, 1)).toBe(-1);
  });
  it('finds the enabled ends', () => {
    expect(edgeIndex(off(0))).toBe(1);
    expect(edgeIndex(off(5), true)).toBe(4);
    expect(edgeIndex([true, true])).toBe(-1);
  });
  it('jumps to a typed prefix, wrapping, and cycles on a repeated letter', () => {
    const labels = ['Any env', 'prod', 'preview', 'staging', 'Prod-eu', 'dev'];
    const none = off();
    expect(typeaheadIndex(labels, none, 0, 'p')).toBe(1);
    expect(typeaheadIndex(labels, none, 1, 'p')).toBe(2);
    expect(typeaheadIndex(labels, none, 1, 'pp')).toBe(2);
    expect(typeaheadIndex(labels, none, 4, 'p')).toBe(1);
    expect(typeaheadIndex(labels, none, 1, 'pro')).toBe(1);
    expect(typeaheadIndex(labels, none, 1, 'prod-')).toBe(4);
    expect(typeaheadIndex(labels, off(1), 0, 'prod')).toBe(4);
    expect(typeaheadIndex(labels, none, 3, 'x')).toBe(3);
  });
});
