import { pad2, zonedParts, zonedToInstant } from './time';

/** Timezone in which admins enter grant expiries (matches the time-window default). */
export const ADMIN_TZ = 'Asia/Tehran';

/** ISO instant -> value for `<input type="datetime-local">` ("2026-11-30T18:00") in `tz`. */
export function isoToLocalInput(iso: string | null | undefined, tz: string = ADMIN_TZ): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const p = zonedParts(t, tz);
  return `${p.y}-${pad2(p.m)}-${pad2(p.d)}T${pad2(p.h)}:${pad2(p.min)}`;
}

/** `datetime-local` value read as wall clock in `tz` -> ISO UTC string, or null if invalid. */
export function localInputToIso(v: string, tz: string = ADMIN_TZ): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(v);
  if (!m) return null;
  const [y, mo, d, h, mi] = m.slice(1).map(Number);
  if (mo < 1 || mo > 12 || d < 1 || d > 31 || h > 23 || mi > 59) return null;
  return new Date(zonedToInstant(y, mo, d, h, mi, tz)).toISOString().replace('.000Z', 'Z');
}
