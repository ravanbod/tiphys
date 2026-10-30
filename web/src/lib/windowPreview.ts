/**
 * Pure logic for the weekly time-window preview (admin "Time windows" page).
 *
 * Semantics mirrored from spec.md ("Time windows and holidays"):
 *  - a rule applies to an (environment, action) when it is enabled, its scope matches and its actions include the action;
 *  - closed if any applicable `allow_inside` rule exists and now is inside none of them,
 *    or now is inside an applicable `deny_inside` rule,
 *    or the day (in the holiday timezone) is a holiday for the environment;
 *  - windows may cross midnight (22:00-06:00). A window belongs to the day it STARTS on: with days=[Sat],
 *    22:00-06:00 is open Sat 22:00 -> Sun 06:00 (and not Sat 00:00-06:00).
 *
 * Intervals are computed per rule in the rule's own timezone, so the work is O(days x rules), not O(minutes).
 */
import type { Action, HolidayView, TimeWindowView } from '../api/types';
import { zonedParts, zonedToInstant } from './time';

export const DEFAULT_TZ = 'Asia/Tehran';
const DAY_MS = 86400_000;

/** Weekday order used by the UI: the week starts on Saturday. Values are JS/API day numbers (0 = Sunday). */
export const WEEK_ORDER = [6, 0, 1, 2, 3, 4, 5] as const;
export const DAY_SHORT = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'] as const;

export type PreviewState = 'open' | 'deny' | 'closed';

export interface PreviewSegment {
  /** Minutes from the start of the displayed day (0..1440). */
  fromMin: number;
  toMin: number;
  state: PreviewState;
  /** Why: names of rules inside which this span is (deny rules for `deny`, allow rules for `open`), holiday title, or ''. */
  rules: string[];
  holiday: string | null;
}

export interface PreviewDay {
  /** YYYY-MM-DD in the display timezone. */
  date: string;
  dow: number;
  dayOfMonth: number;
  label: string;
  isToday: boolean;
  segments: PreviewSegment[];
}

export interface WeeklyPreview {
  days: PreviewDay[];
  /** Index (0..6) of today's column or -1 when `now` is outside the week (never, today). */
  nowDayIndex: number;
  nowMinute: number;
  /** Enabled rules that match the action but whose scope (argo project / team / unknown cluster) cannot be resolved from an environment. */
  unresolvedRules: TimeWindowView[];
}

export interface PreviewOptions {
  rules: TimeWindowView[];
  holidays: HolidayView[];
  environment: string;
  action: Action;
  now: number;
  /** Timezone of the grid (default Asia/Tehran). */
  tz?: string;
  /** Cluster name -> environment, to include `cluster` scoped rules. */
  clusterEnv?: Record<string, string>;
  /** Timezone used to decide which instants fall on a holiday (default Asia/Tehran). */
  holidayTz?: string;
}

export function parseHHMM(s: string): number | null {
  const m = /^(\d{1,2}):(\d{2})(?::\d{2})?$/.exec(s.trim());
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (h > 24 || min > 59 || (h === 24 && min !== 0)) return null;
  return h * 60 + min;
}

export function isValidTimezone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat('en-GB', { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

/** 'applies' | 'unresolved' | 'no'. `unresolved` = would apply to some targets of that action but cannot be placed in one environment. */
export function ruleScopeMatch(
  r: TimeWindowView,
  environment: string,
  clusterEnv: Record<string, string> = {},
): 'applies' | 'unresolved' | 'no' {
  switch (r.scopeKind) {
    case 'global':
      return 'applies';
    case 'environment':
      return r.scopeValue === environment ? 'applies' : 'no';
    case 'cluster': {
      const env = r.scopeValue ? clusterEnv[r.scopeValue] : undefined;
      if (env === undefined) return 'unresolved';
      return env === environment ? 'applies' : 'no';
    }
    default:
      return 'unresolved'; // argo_project, team
  }
}

interface Interval {
  from: number;
  to: number;
  rule: TimeWindowView;
}

/** Instants in [from, to) at which an enabled rule is "inside", in the rule's own timezone. */
export function ruleIntervals(r: TimeWindowView, from: number, to: number): Interval[] {
  const tz = isValidTimezone(r.timezone) ? r.timezone : DEFAULT_TZ;
  const start = parseHHMM(r.startTime);
  const end = parseHHMM(r.endTime);
  if (start === null || end === null) return [];
  const out: Interval[] = [];
  // Walk the rule's local dates from one day before `from` to one day after `to` (midnight-crossing windows start the day before).
  const first = zonedParts(from - DAY_MS, tz);
  const last = zonedParts(to + DAY_MS, tz);
  const lastKey = Date.UTC(last.y, last.m - 1, last.d);
  for (let t = Date.UTC(first.y, first.m - 1, first.d); t <= lastKey; t += DAY_MS) {
    const d = new Date(t);
    if (!r.daysOfWeek.includes(d.getUTCDay())) continue;
    const y = d.getUTCFullYear();
    const m = d.getUTCMonth() + 1;
    const day = d.getUTCDate();
    const endMin = end <= start ? end + 1440 : end; // crosses midnight (24h when start == end)
    const a = zonedToInstant(y, m, day, Math.floor(start / 60), start % 60, tz);
    const b = zonedToInstant(y, m, day + Math.floor(endMin / 1440), Math.floor((endMin % 1440) / 60), endMin % 60, tz);
    if (b > from && a < to) out.push({ from: a, to: b, rule: r });
  }
  return out;
}

interface HolidayInterval {
  from: number;
  to: number;
  title: string;
}

export function holidayIntervals(
  holidays: HolidayView[],
  environment: string,
  tz: string,
): HolidayInterval[] {
  const out: HolidayInterval[] = [];
  for (const h of holidays) {
    if (h.environments.length > 0 && !h.environments.includes(environment)) continue;
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(h.date);
    if (!m) continue;
    const y = Number(m[1]);
    const mo = Number(m[2]);
    const d = Number(m[3]);
    out.push({
      from: zonedToInstant(y, mo, d, 0, 0, tz),
      to: zonedToInstant(y, mo, d + 1, 0, 0, tz),
      title: h.title,
    });
  }
  return out;
}

/** Everything needed to decide the state of any instant in [from, to). */
export interface PreviewModel {
  allow: Interval[];
  deny: Interval[];
  holidays: HolidayInterval[];
  hasAllow: boolean;
  unresolved: TimeWindowView[];
}

export function buildModel(o: Omit<PreviewOptions, 'now'>, from: number, to: number): PreviewModel {
  const applicable: TimeWindowView[] = [];
  const unresolved: TimeWindowView[] = [];
  for (const r of o.rules) {
    if (!r.enabled || !r.actions.includes(o.action)) continue;
    const m = ruleScopeMatch(r, o.environment, o.clusterEnv);
    if (m === 'applies') applicable.push(r);
    else if (m === 'unresolved') unresolved.push(r);
  }
  const allow: Interval[] = [];
  const deny: Interval[] = [];
  for (const r of applicable) {
    const iv = ruleIntervals(r, from, to);
    (r.effect === 'allow_inside' ? allow : deny).push(...iv);
  }
  return {
    allow,
    deny,
    holidays: holidayIntervals(o.holidays, o.environment, o.holidayTz ?? DEFAULT_TZ),
    hasAllow: applicable.some((r) => r.effect === 'allow_inside'),
    unresolved,
  };
}

function inside(iv: { from: number; to: number }, t: number): boolean {
  return t >= iv.from && t < iv.to;
}

export interface InstantState {
  state: PreviewState;
  rules: string[];
  holiday: string | null;
}

export function stateAt(m: PreviewModel, t: number): InstantState {
  const denyHit = m.deny.filter((iv) => inside(iv, t));
  const holidayHit = m.holidays.find((iv) => inside(iv, t));
  if (denyHit.length > 0 || holidayHit) {
    return {
      state: 'deny',
      rules: Array.from(new Set(denyHit.map((iv) => iv.rule.name))),
      holiday: holidayHit?.title ?? null,
    };
  }
  const allowHit = m.allow.filter((iv) => inside(iv, t));
  if (m.hasAllow && allowHit.length === 0) return { state: 'closed', rules: [], holiday: null };
  return { state: 'open', rules: Array.from(new Set(allowHit.map((iv) => iv.rule.name))), holiday: null };
}

/** Convenience for tests and callers: is the action open at this instant? */
export function isOpenAt(o: Omit<PreviewOptions, 'now'>, t: number): boolean {
  return stateAt(buildModel(o, t - DAY_MS, t + DAY_MS), t).state === 'open';
}

function sameDetail(a: PreviewSegment, b: InstantState): boolean {
  return (
    a.state === b.state &&
    a.holiday === b.holiday &&
    a.rules.length === b.rules.length &&
    a.rules.every((x, i) => x === b.rules[i])
  );
}

/** Builds the 7 day columns (Saturday first) of the week containing `now`, with merged state segments. */
export function buildWeeklyPreview(o: PreviewOptions): WeeklyPreview {
  const tz = o.tz ?? DEFAULT_TZ;
  const nowP = zonedParts(o.now, tz);
  const sinceSat = (nowP.dow + 1) % 7;
  const base = Date.UTC(nowP.y, nowP.m - 1, nowP.d) - sinceSat * DAY_MS;
  const weekStart = zonedToInstant(
    new Date(base).getUTCFullYear(),
    new Date(base).getUTCMonth() + 1,
    new Date(base).getUTCDate(),
    0,
    0,
    tz,
  );
  const weekEnd = weekStart + 8 * DAY_MS;
  const model = buildModel(o, weekStart - DAY_MS, weekEnd);

  const days: PreviewDay[] = [];
  for (let i = 0; i < 7; i++) {
    const dd = new Date(base + i * DAY_MS);
    const y = dd.getUTCFullYear();
    const mo = dd.getUTCMonth() + 1;
    const d = dd.getUTCDate();
    const dayStart = zonedToInstant(y, mo, d, 0, 0, tz);
    const dayEnd = zonedToInstant(y, mo, d + 1, 0, 0, tz);
    const cuts = new Set<number>([dayStart, dayEnd]);
    const addCuts = (ivs: { from: number; to: number }[]) => {
      for (const iv of ivs) {
        if (iv.from > dayStart && iv.from < dayEnd) cuts.add(iv.from);
        if (iv.to > dayStart && iv.to < dayEnd) cuts.add(iv.to);
      }
    };
    addCuts(model.allow);
    addCuts(model.deny);
    addCuts(model.holidays);
    const sorted = Array.from(cuts).sort((a, b) => a - b);
    const segments: PreviewSegment[] = [];
    for (let k = 0; k + 1 < sorted.length; k++) {
      const s = stateAt(model, sorted[k]);
      const fromMin = Math.round((sorted[k] - dayStart) / 60000);
      const toMin = Math.min(1440, Math.round((sorted[k + 1] - dayStart) / 60000));
      const prev = segments[segments.length - 1];
      if (prev && sameDetail(prev, s)) prev.toMin = toMin;
      else segments.push({ fromMin, toMin, state: s.state, rules: s.rules, holiday: s.holiday });
    }
    days.push({
      date: `${y}-${String(mo).padStart(2, '0')}-${String(d).padStart(2, '0')}`,
      dow: dd.getUTCDay(),
      dayOfMonth: d,
      label: DAY_SHORT[dd.getUTCDay()],
      isToday: i === sinceSat,
      segments,
    });
  }
  return { days, nowDayIndex: sinceSat, nowMinute: nowP.h * 60 + nowP.min, unresolvedRules: model.unresolved };
}

/** 0 -> "00:00", 1020 -> "17:00", 1440 -> "24:00". */
export function fmtMinutes(min: number): string {
  const h = Math.floor(min / 60);
  const m = min % 60;
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
}

/** Days in Saturday-first order: "Sat – Wed", "Every day", "Sat, Mon, Thu". */
export function fmtDays(days: number[]): string {
  const set = new Set(days);
  if (set.size === 0) return 'No days';
  if (set.size === 7) return 'Every day';
  const flags = WEEK_ORDER.map((d) => set.has(d));
  // contiguous run in week order (no wrap-around), length >= 3
  const first = flags.indexOf(true);
  let last = first;
  while (last + 1 < 7 && flags[last + 1]) last++;
  const contiguous = flags.filter(Boolean).length === last - first + 1;
  if (contiguous && last - first >= 2) return `${DAY_SHORT[WEEK_ORDER[first]]} – ${DAY_SHORT[WEEK_ORDER[last]]}`;
  return WEEK_ORDER.filter((d) => set.has(d)).map((d) => DAY_SHORT[d]).join(', ');
}
