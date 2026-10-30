import { describe, expect, it } from 'vitest';
import { buildCastTimeMap, indexAtOrBefore } from './castTime';

describe('buildCastTimeMap', () => {
  // gaps: 1, 10 (-> 2), 1, 20 (-> 2)
  const times = [1, 11, 12, 32];
  const m = buildCastTimeMap(times, 2);
  it('compresses idle gaps like the player', () => {
    expect(m.toPlayer(1)).toBe(1);
    expect(m.toPlayer(11)).toBe(3);
    expect(m.toPlayer(12)).toBe(4);
    expect(m.toPlayer(32)).toBe(6);
    expect(m.duration).toBe(6);
  });
  it('interpolates between events within the limit', () => {
    expect(m.toPlayer(1.5)).toBeCloseTo(1.5);
    expect(m.toPlayer(5)).toBeCloseTo(3); // inside a long idle gap: capped at limit after event 1 => 1 + 2
  });
  it('maps back', () => {
    expect(m.toOrig(3)).toBe(11);
    expect(m.toOrig(4)).toBe(12);
    expect(m.toOrig(6)).toBe(32);
    expect(m.toOrig(0.5)).toBeCloseTo(0.5);
  });
  it('is the identity without a limit', () => {
    const id = buildCastTimeMap(times, undefined);
    expect(id.toPlayer(11)).toBe(11);
    expect(id.toOrig(32)).toBe(32);
  });
  it('handles empty input', () => {
    const e = buildCastTimeMap([], 2);
    expect(e.duration).toBe(0);
    expect(e.toPlayer(5)).toBe(2);
  });
});

describe('indexAtOrBefore', () => {
  it('finds the last item at or before t', () => {
    expect(indexAtOrBefore([1, 3, 5], 0)).toBe(-1);
    expect(indexAtOrBefore([1, 3, 5], 3)).toBe(1);
    expect(indexAtOrBefore([1, 3, 5], 4.9)).toBe(1);
    expect(indexAtOrBefore([1, 3, 5], 99)).toBe(2);
    expect(indexAtOrBefore([], 1)).toBe(-1);
  });
});
