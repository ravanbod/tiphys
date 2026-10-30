const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const p2 = (n: string | number) => String(n).padStart(2, '0');

/** Plain-English description of the common cron shapes; '' when unknown. */
export function describeCron(expr: string): string {
  const f = expr.trim().split(/\s+/);
  if (f.length !== 5) return '';
  const [min, hour, dom, mon, dow] = f;
  const num = /^\d+$/;
  if (dom !== '*' || mon !== '*') return '';
  let m = /^\*\/(\d+)$/.exec(min);
  if (m && hour === '*' && dow === '*') return `Every ${m[1]} minutes`;
  if (min === '*' && hour === '*' && dow === '*') return 'Every minute';
  m = /^\*\/(\d+)$/.exec(hour);
  if (m && num.test(min) && dow === '*') return `Every ${m[1]} hours${min === '0' ? '' : ` at minute ${min}`}`;
  if (num.test(min) && hour === '*' && dow === '*') return `Every hour at minute ${min}`;
  if (num.test(min) && num.test(hour)) {
    const t = `${p2(hour)}:${p2(min)}`;
    if (dow === '*') return `Every day at ${t}`;
    if (num.test(dow)) return `At ${t} on ${DAYS[Number(dow) % 7]}`;
  }
  return '';
}
