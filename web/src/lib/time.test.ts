import { describe, expect, it } from 'vitest';
import { fmtCountdown, fmtDuration, fmtHours, fmtRelative, fmtReopens, fmtSpan, msUntil, zonedParts, zonedToInstant } from './time';

describe('countdown', () => {
  it('formats mm:ss and h:mm:ss', () => {
    expect(fmtCountdown(252_000)).toBe('04:12');
    expect(fmtCountdown(3_723_000)).toBe('1:02:03');
    expect(fmtCountdown(-5)).toBe('00:00');
  });
  it('formats durations', () => {
    expect(fmtDuration(708)).toBe('11:48');
    expect(fmtDuration(null)).toBe('—');
  });
  it('msUntil handles null and invalid', () => {
    expect(msUntil(null, 0)).toBeNull();
    expect(msUntil('nope', 0)).toBeNull();
    expect(msUntil('2026-10-20T10:00:10Z', Date.parse('2026-10-20T10:00:00Z'))).toBe(10_000);
  });
});

describe('relative', () => {
  const now = Date.parse('2026-10-20T12:00:00Z');
  it('describes ago spans', () => {
    expect(fmtRelative('2026-10-20T11:59:50Z', now)).toBe('just now');
    expect(fmtRelative('2026-10-20T11:49:00Z', now)).toBe('11 min ago');
    expect(fmtRelative('2026-10-17T00:00:00Z', now)).toBe('3d 12h ago');
  });
  it('spans', () => {
    expect(fmtSpan(30)).toBe('30s');
    expect(fmtSpan(7200)).toBe('2h');
    expect(fmtSpan(3 * 3600 + 20 * 60)).toBe('3h 20m');
    expect(fmtSpan(3590)).toBe('1h'); // 59.8 min rounds up to the hour, not "0h 60m"
    expect(fmtSpan(2 * 3600 - 10)).toBe('2h');
  });
});

describe('reopens', () => {
  // Wed 2026-10-21 18:01 UTC; window reopens Sat 2026-10-24 09:00 UTC
  const now = Date.parse('2026-10-21T18:01:00Z');
  it('uses weekday within a week', () => {
    expect(fmtReopens('2026-10-24T09:00:00Z', now, 'UTC')).toBe('Sat 09:00');
  });
  it('uses today / tomorrow', () => {
    expect(fmtReopens('2026-10-21T20:00:00Z', now, 'UTC')).toBe('today 20:00');
    expect(fmtReopens('2026-10-22T09:00:00Z', now, 'UTC')).toBe('tomorrow 09:00');
  });
  it('respects the timezone', () => {
    // 05:30 UTC is 09:00 in Asia/Tehran (UTC+3:30, no DST since 2022)
    expect(fmtReopens('2026-10-24T05:30:00Z', now, 'Asia/Tehran')).toBe('Sat 09:00');
  });
});

describe('zones', () => {
  it('round-trips wall clock', () => {
    const t = zonedToInstant(2026, 10, 24, 9, 0, 'Asia/Tehran');
    expect(new Date(t).toISOString()).toBe('2026-10-24T05:30:00.000Z');
    const p = zonedParts(t, 'Asia/Tehran');
    expect([p.h, p.min, p.dow]).toEqual([9, 0, 6]);
  });
});

describe('hours', () => {
  it('labels grant durations', () => {
    expect(fmtHours(1)).toBe('1 hour');
    expect(fmtHours(24)).toBe('1 day');
    expect(fmtHours(168)).toBe('7 days');
    expect(fmtHours(null)).toBe('Permanent');
  });
});
