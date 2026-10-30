import { describe, expect, it } from 'vitest';
import type { HolidayView, TimeWindowView } from '../api/types';
import {
  buildWeeklyPreview, fmtDays, fmtMinutes, isOpenAt, parseHHMM, ruleScopeMatch,
} from './windowPreview';
import type { PreviewDay } from './windowPreview';

const SAT_WED = [6, 0, 1, 2, 3];

function rule(p: Partial<TimeWindowView>): TimeWindowView {
  return {
    id: 1, name: 'r', scopeKind: 'environment', scopeValue: 'prod', actions: ['exec'], daysOfWeek: SAT_WED,
    startTime: '08:00', endTime: '17:00', timezone: 'Asia/Tehran', effect: 'allow_inside', enabled: true, ...p,
  };
}

// Tue 2026-10-20 13:41 in Asia/Tehran (UTC+3:30, no DST) = 10:11 UTC. The preview week is Sat Oct 17 .. Fri Oct 23.
const NOW = Date.parse('2026-10-20T10:11:00Z');

function summary(d: PreviewDay): string {
  return d.segments.map((s) => `${s.state}:${fmtMinutes(s.fromMin)}-${fmtMinutes(s.toMin)}`).join(' ');
}

const base = { holidays: [] as HolidayView[], environment: 'prod', action: 'exec' as const, now: NOW };

describe('buildWeeklyPreview', () => {
  it('starts the week on Saturday and marks today', () => {
    const p = buildWeeklyPreview({ ...base, rules: [] });
    expect(p.days.map((d) => d.label)).toEqual(['Sat', 'Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri']);
    expect(p.days[0].date).toBe('2026-10-17');
    expect(p.days[3].isToday).toBe(true);
    expect(p.nowDayIndex).toBe(3);
    expect(p.nowMinute).toBe(13 * 60 + 41);
  });

  it('is open all week when there are no rules', () => {
    const p = buildWeeklyPreview({ ...base, rules: [] });
    for (const d of p.days) expect(summary(d)).toBe('open:00:00-24:00');
  });

  it('allow_inside: open only inside; other days closed', () => {
    const p = buildWeeklyPreview({ ...base, rules: [rule({})] });
    expect(summary(p.days[0])).toBe('closed:00:00-08:00 open:08:00-17:00 closed:17:00-24:00'); // Sat
    expect(summary(p.days[5])).toBe('closed:00:00-24:00'); // Thu
    expect(summary(p.days[6])).toBe('closed:00:00-24:00'); // Fri
  });

  it('deny_inside carves a hole into an allow window', () => {
    const rules = [
      rule({ id: 1, name: 'Working hours' }),
      rule({ id: 2, name: 'Change freeze', effect: 'deny_inside', daysOfWeek: [2], startTime: '13:00', endTime: '15:00' }),
    ];
    const tue = buildWeeklyPreview({ ...base, rules }).days[3];
    expect(summary(tue)).toBe('closed:00:00-08:00 open:08:00-13:00 deny:13:00-15:00 open:15:00-17:00 closed:17:00-24:00');
    expect(tue.segments[2].rules).toEqual(['Change freeze']);
    expect(tue.segments[1].rules).toEqual(['Working hours']);
  });

  it('deny_inside alone leaves the rest open', () => {
    const rules = [rule({ effect: 'deny_inside', daysOfWeek: [2], startTime: '13:00', endTime: '15:00', name: 'Freeze' })];
    const tue = buildWeeklyPreview({ ...base, rules }).days[3];
    expect(summary(tue)).toBe('open:00:00-13:00 deny:13:00-15:00 open:15:00-24:00');
  });

  it('handles windows crossing midnight (22:00-06:00), anchored to the start day', () => {
    const rules = [rule({ name: 'Night', daysOfWeek: [6], startTime: '22:00', endTime: '06:00' })]; // Saturday night
    const p = buildWeeklyPreview({ ...base, rules });
    expect(summary(p.days[0])).toBe('closed:00:00-22:00 open:22:00-24:00'); // Sat
    expect(summary(p.days[1])).toBe('open:00:00-06:00 closed:06:00-24:00'); // Sun morning
    expect(summary(p.days[2])).toBe('closed:00:00-24:00');
  });

  it('shows the tail of a window that started on the previous Friday in the first column', () => {
    const rules = [rule({ name: 'Fri night', daysOfWeek: [5], startTime: '22:00', endTime: '06:00' })];
    const p = buildWeeklyPreview({ ...base, rules });
    expect(summary(p.days[0])).toBe('open:00:00-06:00 closed:06:00-24:00');
    expect(summary(p.days[6])).toBe('closed:00:00-22:00 open:22:00-24:00');
  });

  it('deny_inside across midnight', () => {
    const rules = [rule({ effect: 'deny_inside', name: 'Night freeze', daysOfWeek: [2], startTime: '22:00', endTime: '06:00' })];
    const p = buildWeeklyPreview({ ...base, rules });
    expect(summary(p.days[3])).toBe('open:00:00-22:00 deny:22:00-24:00');
    expect(summary(p.days[4])).toBe('deny:00:00-06:00 open:06:00-24:00');
  });

  it('start == end is a full 24h window', () => {
    const rules = [rule({ effect: 'deny_inside', name: 'Thursday', daysOfWeek: [4], startTime: '00:00', endTime: '00:00' })];
    const p = buildWeeklyPreview({ ...base, rules });
    expect(summary(p.days[4])).toBe('open:00:00-24:00'); // Wed
    expect(summary(p.days[5])).toBe('deny:00:00-24:00'); // Thu
    expect(summary(p.days[6])).toBe('open:00:00-24:00'); // Fri
  });

  it('honours the rule timezone', () => {
    // 08:00-09:00 UTC = 11:30-12:30 in Asia/Tehran
    const rules = [rule({ timezone: 'UTC', daysOfWeek: [2], startTime: '08:00', endTime: '09:00' })];
    const tue = buildWeeklyPreview({ ...base, rules }).days[3];
    expect(summary(tue)).toBe('closed:00:00-11:30 open:11:30-12:30 closed:12:30-24:00');
  });

  it('holidays close only the listed environments, for the whole day', () => {
    const holidays: HolidayView[] = [{ id: 1, date: '2026-10-21', title: 'Public holiday', environments: ['prod'] }];
    const rules = [rule({})];
    const prod = buildWeeklyPreview({ ...base, rules, holidays });
    expect(summary(prod.days[4])).toBe('deny:00:00-24:00'); // Wed
    expect(prod.days[4].segments[0].holiday).toBe('Public holiday');
    const staging = buildWeeklyPreview({ ...base, environment: 'staging', rules: [rule({ scopeValue: 'staging' })], holidays });
    expect(summary(staging.days[4])).toBe('closed:00:00-08:00 open:08:00-17:00 closed:17:00-24:00');
  });

  it('a holiday with no environments applies to all', () => {
    const holidays: HolidayView[] = [{ id: 1, date: '2026-10-18', title: 'All', environments: [] }];
    expect(summary(buildWeeklyPreview({ ...base, rules: [], holidays }).days[1])).toBe('deny:00:00-24:00');
  });

  it('ignores disabled rules, other actions and other environments', () => {
    const rules = [
      rule({ enabled: false }),
      rule({ actions: ['logs'] }),
      rule({ scopeValue: 'staging' }),
    ];
    const p = buildWeeklyPreview({ ...base, rules });
    for (const d of p.days) expect(summary(d)).toBe('open:00:00-24:00');
  });

  it('global rules apply; cluster rules apply through the cluster environment', () => {
    const g = buildWeeklyPreview({ ...base, rules: [rule({ scopeKind: 'global', scopeValue: null })] });
    expect(summary(g.days[0])).toBe('closed:00:00-08:00 open:08:00-17:00 closed:17:00-24:00');
    const c = rule({ scopeKind: 'cluster', scopeValue: 'prod-1' });
    const withMap = buildWeeklyPreview({ ...base, rules: [c], clusterEnv: { 'prod-1': 'prod' } });
    expect(summary(withMap.days[0])).toBe('closed:00:00-08:00 open:08:00-17:00 closed:17:00-24:00');
    const other = buildWeeklyPreview({ ...base, rules: [c], clusterEnv: { 'prod-1': 'staging' } });
    expect(summary(other.days[0])).toBe('open:00:00-24:00');
  });

  it('reports team / argo project rules as unresolved instead of guessing', () => {
    const t = rule({ scopeKind: 'team', scopeValue: 'Contractors' });
    const p = buildWeeklyPreview({ ...base, rules: [t] });
    expect(p.unresolvedRules).toHaveLength(1);
    expect(summary(p.days[0])).toBe('open:00:00-24:00');
    expect(ruleScopeMatch(rule({ scopeKind: 'cluster', scopeValue: 'x' }), 'prod', {})).toBe('unresolved');
  });
});

describe('isOpenAt', () => {
  const rules = [
    rule({ id: 1, name: 'Hours' }),
    rule({ id: 2, name: 'Freeze', effect: 'deny_inside', daysOfWeek: [2], startTime: '13:00', endTime: '15:00' }),
  ];
  const o = { rules, holidays: [], environment: 'prod', action: 'exec' as const };
  it('closes right after the window end (17:01 closed, 16:59 open)', () => {
    expect(isOpenAt(o, Date.parse('2026-10-21T13:31:00Z'))).toBe(false); // Wed 17:01 Tehran
    expect(isOpenAt(o, Date.parse('2026-10-21T13:29:00Z'))).toBe(true); // Wed 16:59
    expect(isOpenAt(o, Date.parse('2026-10-20T10:11:00Z'))).toBe(false); // Tue 13:41 freeze
  });
  it('22:00-06:00 at 23:00 and 05:00', () => {
    const night = { ...o, rules: [rule({ daysOfWeek: [0, 1, 2, 3, 4, 5, 6], startTime: '22:00', endTime: '06:00' })] };
    expect(isOpenAt(night, Date.parse('2026-10-20T19:30:00Z'))).toBe(true); // 23:00 Tehran
    expect(isOpenAt(night, Date.parse('2026-10-20T01:30:00Z'))).toBe(true); // 05:00 Tehran
    expect(isOpenAt(night, Date.parse('2026-10-20T09:30:00Z'))).toBe(false); // 13:00
  });
});

describe('helpers', () => {
  it('parseHHMM', () => {
    expect(parseHHMM('08:30')).toBe(510);
    expect(parseHHMM('24:00')).toBe(1440);
    expect(parseHHMM('25:00')).toBeNull();
    expect(parseHHMM('x')).toBeNull();
  });
  it('fmtDays orders from Saturday', () => {
    expect(fmtDays([6, 0, 1, 2, 3])).toBe('Sat – Wed');
    expect(fmtDays([0, 1, 2, 3, 4, 5, 6])).toBe('Every day');
    expect(fmtDays([2])).toBe('Tue');
    expect(fmtDays([1, 6])).toBe('Sat, Mon');
    expect(fmtDays([])).toBe('No days');
  });
});
