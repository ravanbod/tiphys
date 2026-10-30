/** Pure time helpers. All take an explicit `now` / `tz` so they are testable. */

export function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/** 252000 ms -> "04:12"; >= 1 hour -> "1:02:03". Negative clamps to 00:00. */
export function fmtCountdown(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return h > 0 ? `${h}:${pad2(m)}:${pad2(s)}` : `${pad2(m)}:${pad2(s)}`;
}

/** Seconds -> "11:48" / "1:02:03". */
export function fmtDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return '—';
  return fmtCountdown(seconds * 1000);
}

/** Milliseconds from `now` until the ISO instant (can be negative). null input -> null. */
export function msUntil(iso: string | null | undefined, now: number): number | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : t - now;
}

/** "just now", "11 min ago", "2h ago", "3d ago". */
export function fmtRelative(iso: string | null | undefined, now: number): string {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '—';
  const diff = Math.round((now - t) / 1000);
  if (diff < 0) return `in ${fmtSpan(-diff)}`;
  if (diff < 45) return 'just now';
  return `${fmtSpan(diff)} ago`;
}

/** 90 -> "2 min"; 7200 -> "2h"; 200000 -> "2d 7h". */
export function fmtSpan(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 90) return s < 45 ? `${s}s` : '1 min';
  const min = Math.round(s / 60);
  if (min < 60) return `${min} min`;
  // Hours from the rounded minutes, so 59.5 min reads "1h", not "0h 60m".
  const h = Math.floor(min / 60);
  if (h < 24) {
    const m = min - h * 60;
    return m >= 1 && h < 6 ? `${h}h ${m}m` : `${h}h`;
  }
  const d = Math.floor(h / 24);
  const rh = h % 24;
  return rh ? `${d}d ${rh}h` : `${d}d`;
}

/** Pod age style: "3d 12h". */
export function fmtAge(iso: string | null | undefined, now: number): string {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '—';
  return fmtSpan((now - t) / 1000);
}

function parts(d: Date, tz: string | undefined, opts: Intl.DateTimeFormatOptions): Record<string, string> {
  const f = new Intl.DateTimeFormat('en-GB', { timeZone: tz, hourCycle: 'h23', ...opts });
  const out: Record<string, string> = {};
  for (const p of f.formatToParts(d)) out[p.type] = p.value;
  return out;
}

export function fmtTime(iso: string | number | Date | null | undefined, tz?: string, withSeconds = false): string {
  if (iso === null || iso === undefined) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const p = parts(d, tz, { hour: '2-digit', minute: '2-digit', ...(withSeconds ? { second: '2-digit' } : {}) });
  return `${p.hour}:${p.minute}${withSeconds ? `:${p.second}` : ''}`;
}

/** "Oct 20" */
export function fmtDay(iso: string | number | Date | null | undefined, tz?: string): string {
  if (iso === null || iso === undefined) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const p = parts(d, tz, { month: 'short', day: 'numeric' });
  return `${p.month} ${p.day}`;
}

/** "Oct 20, 13:38" */
export function fmtDateTime(iso: string | null | undefined, tz?: string): string {
  if (!iso) return '—';
  return `${fmtDay(iso, tz)}, ${fmtTime(iso, tz)}`;
}

/** "Today 13:12", "Yesterday", else "Oct 18". For lists. */
export function fmtWhen(iso: string | null | undefined, now: number, tz?: string): string {
  if (!iso) return 'Never';
  const dayKey = (t: number) => {
    const p = parts(new Date(t), tz, { year: 'numeric', month: '2-digit', day: '2-digit' });
    return `${p.year}-${p.month}-${p.day}`;
  };
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '—';
  if (dayKey(t) === dayKey(now)) return `Today ${fmtTime(t, tz)}`;
  if (dayKey(t) === dayKey(now - 86400_000)) return `Yesterday ${fmtTime(t, tz)}`;
  return fmtDay(t, tz);
}

/** "Sat 09:00", or "today 09:00" / "tomorrow 09:00" when close. */
export function fmtReopens(iso: string | null | undefined, now: number, tz?: string): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const dayKey = (x: number) => {
    const p = parts(new Date(x), tz, { year: 'numeric', month: '2-digit', day: '2-digit' });
    return `${p.year}-${p.month}-${p.day}`;
  };
  const time = fmtTime(t, tz);
  if (dayKey(t) === dayKey(now)) return `today ${time}`;
  if (dayKey(t) === dayKey(now + 86400_000)) return `tomorrow ${time}`;
  if (t - now < 6 * 86400_000) return `${parts(new Date(t), tz, { weekday: 'short' }).weekday} ${time}`;
  return `${fmtDay(t, tz)} ${time}`;
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(1)} GB`;
}

/** Wall-clock parts of an instant in an IANA zone. dow: 0 = Sunday. */
export function zonedParts(ms: number, tz: string): { y: number; m: number; d: number; h: number; min: number; dow: number } {
  const p = parts(new Date(ms), tz, {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', weekday: 'short',
  });
  const dows = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
  return { y: +p.year, m: +p.month, d: +p.day, h: +p.hour % 24, min: +p.minute, dow: dows.indexOf(p.weekday) };
}

/** The instant at which wall clock reads y-m-d h:min in `tz` (DST-safe to the minute for normal cases). */
export function zonedToInstant(y: number, m: number, d: number, h: number, min: number, tz: string): number {
  const guess = Date.UTC(y, m - 1, d, h, min);
  let t = guess;
  for (let i = 0; i < 3; i++) {
    const z = zonedParts(t, tz);
    const asUtc = Date.UTC(z.y, z.m - 1, z.d, z.h, z.min);
    const diff = guess - asUtc;
    if (diff === 0) break;
    t += diff;
  }
  return t;
}

/** Duration phrase for grants: 1 -> "1 hour", 24 -> "1 day", null -> "Permanent". */
export function fmtHours(h: number | null): string {
  if (h === null) return 'Permanent';
  if (h < 24) return `${h} hour${h === 1 ? '' : 's'}`;
  if (h % 24 === 0) {
    const d = h / 24;
    return `${d} day${d === 1 ? '' : 's'}`;
  }
  return `${h} hours`;
}
