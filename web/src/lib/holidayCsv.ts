import type { HolidayInput } from '../api/types';

export interface HolidayCsvResult {
  rows: HolidayInput[];
  errors: { line: number; message: string }[];
}

function validDate(s: string): boolean {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (!m) return false;
  const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]));
  return d.getUTCFullYear() === +m[1] && d.getUTCMonth() === +m[2] - 1 && d.getUTCDate() === +m[3];
}

/**
 * Parses pasted CSV lines `date,title,env1|env2` (YYYY-MM-DD). The environments column is optional (empty = all).
 * A header line starting with "date" is skipped. Titles may be wrapped in double quotes and contain commas.
 */
export function parseHolidayCsv(text: string): HolidayCsvResult {
  const rows: HolidayInput[] = [];
  const errors: { line: number; message: string }[] = [];
  text.split(/\r?\n/).forEach((raw, i) => {
    const line = raw.trim();
    if (!line) return;
    if (i === 0 && /^date\s*,/i.test(line)) return;
    const cells: string[] = [];
    let cur = '';
    let q = false;
    for (let k = 0; k < line.length; k++) {
      const c = line[k];
      if (c === '"') {
        if (q && line[k + 1] === '"') {
          cur += '"';
          k++;
        } else q = !q;
      } else if (c === ',' && !q) {
        cells.push(cur.trim());
        cur = '';
      } else cur += c;
    }
    cells.push(cur.trim());
    const [date, title = '', envs = ''] = cells;
    if (!validDate(date)) {
      errors.push({ line: i + 1, message: `"${date}" is not a date (use YYYY-MM-DD)` });
      return;
    }
    if (!title) {
      errors.push({ line: i + 1, message: 'Title is missing' });
      return;
    }
    const environments = envs.split('|').map((e) => e.trim()).filter(Boolean);
    rows.push({ date, title, environments });
  });
  return { rows, errors };
}
