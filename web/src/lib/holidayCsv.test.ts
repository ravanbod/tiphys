import { describe, expect, it } from 'vitest';
import { parseHolidayCsv } from './holidayCsv';

describe('parseHolidayCsv', () => {
  it('parses rows, header, quotes and env lists', () => {
    const r = parseHolidayCsv('date,title,environments\n2026-10-28,Public holiday,prod\n2026-11-11,"Day off, company",prod|staging\n2026-12-21,Winter\n');
    expect(r.errors).toEqual([]);
    expect(r.rows).toEqual([
      { date: '2026-10-28', title: 'Public holiday', environments: ['prod'] },
      { date: '2026-11-11', title: 'Day off, company', environments: ['prod', 'staging'] },
      { date: '2026-12-21', title: 'Winter', environments: [] },
    ]);
  });
  it('reports bad lines with line numbers', () => {
    const r = parseHolidayCsv('2026-02-30,Nope\nfoo,Bar\n2026-01-01,\n2026-01-02,Ok');
    expect(r.errors.map((e) => e.line)).toEqual([1, 2, 3]);
    expect(r.rows).toHaveLength(1);
  });
});
