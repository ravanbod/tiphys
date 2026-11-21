const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const DAY_NAMES = ['SUN', 'MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT'];
const p2 = (n: string | number) => String(n).padStart(2, '0');
const num = /^\d+$/;

const DESCRIPTORS: Record<string, string> = {
  '@yearly': 'Every year on January 1 at 00:00',
  '@annually': 'Every year on January 1 at 00:00',
  '@monthly': 'On day 1 of every month at 00:00',
  '@weekly': 'Every Sunday at 00:00',
  '@daily': 'Every day at 00:00',
  '@midnight': 'Every day at 00:00',
  '@hourly': 'Every hour',
};

/** "a, b and c". */
function and(items: string[]): string {
  return items.length < 2 ? items.join('') : `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`;
}

/** A day-of-week number or name (0 and 7 are Sunday); -1 when unknown. */
function day(s: string): number {
  if (num.test(s)) return Number(s) <= 7 ? Number(s) % 7 : -1;
  return DAY_NAMES.indexOf(s.toUpperCase());
}

/** "weekdays", "Monday to Thursday", "Monday and Friday"; '' when unknown. */
function days(dow: string): string {
  const range = /^(\w+)-(\w+)$/.exec(dow);
  if (range) {
    const [a, b] = [day(range[1]), day(range[2])];
    if (a < 0 || b < 0) return '';
    if (a === 1 && b === 5) return 'weekdays';
    return `${DAYS[a]} to ${DAYS[b]}`;
  }
  const list = dow.split(',').map(day);
  if (list.some((d) => d < 0)) return '';
  if (list.length === 2 && list.includes(0) && list.includes(6)) return 'weekends';
  return and(list.map((d) => DAYS[d]));
}

/** Plain-English description of the common cron shapes; '' when unknown. */
export function describeCron(expr: string): string {
  const e = expr.trim();
  if (e.startsWith('@')) return DESCRIPTORS[e.toLowerCase()] ?? '';
  const f = e.split(/\s+/);
  if (f.length !== 5) return '';
  const [min, hour, dom, mon, dow] = f;
  if (mon !== '*' || (dom !== '*' && dow !== '*')) return '';
  const on = dow === '*' ? '' : days(dow);
  if (dow !== '*' && !on) return '';

  if (dom === '*' && dow === '*') {
    let m = /^\*\/(\d+)$/.exec(min);
    if (m && hour === '*') return `Every ${m[1]} minutes`;
    if (min === '*' && hour === '*') return 'Every minute';
    m = /^\*\/(\d+)$/.exec(hour);
    if (m && num.test(min)) return `Every ${m[1]} hours${min === '0' ? '' : ` at minute ${min}`}`;
    if (num.test(min) && hour === '*') return `Every hour at minute ${min}`;
  }
  const hours = hour.split(',');
  if (!num.test(min) || !hours.every((h) => num.test(h))) return '';
  const at = and(hours.map((h) => `${p2(h)}:${p2(min)}`));
  if (num.test(dom)) return `At ${at} on day ${dom} of every month`;
  if (on) return `At ${at} on ${on}`;
  return `Every day at ${at}`;
}
