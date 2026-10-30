import { describe, expect, it } from 'vitest';
import { isoToLocalInput, localInputToIso } from './adminDatetime';

describe('admin datetime', () => {
  it('round-trips in Asia/Tehran (UTC+3:30)', () => {
    expect(localInputToIso('2026-11-30T18:00')).toBe('2026-11-30T14:30:00Z');
    expect(isoToLocalInput('2026-11-30T14:30:00Z')).toBe('2026-11-30T18:00');
  });
  it('rejects garbage and empty', () => {
    expect(localInputToIso('')).toBeNull();
    expect(localInputToIso('2026-13-01T10:00')).toBeNull();
    expect(isoToLocalInput(null)).toBe('');
    expect(isoToLocalInput('nope')).toBe('');
  });
});
