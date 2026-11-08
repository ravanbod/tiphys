import type { BreakGlass, Me } from '../api/types';
import { pad2 } from './time';

/** Whether break-glass can be used at all (started or requested). */
export const breakGlassOn = (me: Me): boolean => me.settings.breakGlassMode !== 'off';

/** Whether break-glass must be requested and approved by an admin. */
export const breakGlassNeedsApproval = (me: Me): boolean => me.settings.breakGlassMode === 'approval';

/** The label of buttons that open the break-glass dialog. */
export const breakGlassLabel = (me: Me): string => (breakGlassNeedsApproval(me) ? 'Request break-glass' : 'Use break-glass');

/** Break-glass durations to pick from, in minutes: common ones up to max, plus max and any extra (e.g. the requested one). */
export function breakGlassDurations(max: number, extra?: number): number[] {
  const opts = [15, 30, 60, 120, 180, 240, 480].filter((m) => m <= max);
  for (const m of [max, extra]) if (m !== undefined && m <= max && !opts.includes(m)) opts.push(m);
  return opts.sort((a, b) => a - b);
}

/** How far ahead a break-glass may be scheduled (the server's MaxBreakGlassLead). */
export const MAX_SCHEDULE_DAYS = 7;

/** Whether the break-glass has not started yet at now. */
export const breakGlassScheduled = (bg: BreakGlass | null | undefined, now: number): boolean =>
  !!bg && !bg.endedAt && Date.parse(bg.startedAt) > now;

/**
 * Changes when the user's break-glass is started, scheduled, begins or ends:
 * the dependency for refetching what it decides.
 */
export const breakGlassKey = (me: Me): string =>
  me.breakGlass ? `${me.breakGlass.id}:${me.breakGlass.active ? 'on' : 'off'}` : '';

/** `datetime-local` value ("2026-11-08T19:00") for a time in the browser's zone. */
export function toLocalInput(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

/** The default start offered for a scheduled break-glass: the next full hour. */
export function nextFullHour(now: number): number {
  const d = new Date(now);
  d.setMinutes(0, 0, 0);
  return d.getTime() + 3600_000;
}

/**
 * Reads a `datetime-local` start (browser time) and checks it lies ahead,
 * within MAX_SCHEDULE_DAYS: the ISO instant, or an error.
 */
export function parseStart(v: string, now: number): { iso: string } | { error: string } {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(v);
  const t = m ? new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]).getTime() : NaN;
  if (Number.isNaN(t)) return { error: 'Choose a date and time.' };
  if (t <= now) return { error: 'The start must lie ahead.' };
  if (t > now + MAX_SCHEDULE_DAYS * 86400_000) return { error: `The start must be within ${MAX_SCHEDULE_DAYS} days.` };
  return { iso: new Date(t).toISOString() };
}
