import type { Me } from '../api/types';

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
