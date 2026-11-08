import { describe, expect, it } from 'vitest';
import type { BreakGlass, Me } from '../api/types';
import { breakGlassKey, breakGlassScheduled, nextFullHour, parseStart, toLocalInput } from './breakGlass';

const now = new Date(2026, 10, 8, 14, 20).getTime(); // local time

const bg = (over: Partial<BreakGlass> = {}): BreakGlass => ({
  id: 3, userId: 1, username: 'sara', reason: 'r', startedAt: new Date(now + 3600_000).toISOString(),
  expiresAt: new Date(now + 7200_000).toISOString(), endedAt: null, endedBy: null, active: false, scheduled: true, ...over,
});

describe('scheduled break-glass', () => {
  it('is scheduled until it starts, and never once cancelled', () => {
    expect(breakGlassScheduled(bg(), now)).toBe(true);
    expect(breakGlassScheduled(bg(), now + 3600_000)).toBe(false);
    expect(breakGlassScheduled(bg({ endedAt: new Date(now).toISOString() }), now)).toBe(false);
    expect(breakGlassScheduled(null, now)).toBe(false);
  });

  it('changes its key when it begins', () => {
    const me = (b: BreakGlass | null) => ({ breakGlass: b }) as Me;
    expect(breakGlassKey(me(null))).toBe('');
    expect(breakGlassKey(me(bg()))).not.toBe(breakGlassKey(me(bg({ active: true, scheduled: false }))));
  });

  it('offers the next full hour, in browser time', () => {
    expect(toLocalInput(nextFullHour(now))).toBe('2026-11-08T15:00');
  });

  it('accepts only starts ahead and within seven days', () => {
    expect(parseStart('2026-11-08T19:00', now)).toEqual({ iso: new Date(2026, 10, 8, 19, 0).toISOString() });
    expect(parseStart('2026-11-08T14:00', now)).toEqual({ error: 'The start must lie ahead.' });
    expect(parseStart('2026-11-16T14:00', now)).toEqual({ error: 'The start must be within 7 days.' });
    expect(parseStart('', now)).toEqual({ error: 'Choose a date and time.' });
  });
});
