import { describe, expect, it } from 'vitest';
import { describeCron } from './cron';

describe('describeCron', () => {
  it('describes the common shapes', () => {
    expect(describeCron('*/15 * * * *')).toBe('Every 15 minutes');
    expect(describeCron('0 2 * * *')).toBe('Every day at 02:00');
    expect(describeCron('0 */6 * * *')).toBe('Every 6 hours');
    expect(describeCron('0 3 * * 0')).toBe('At 03:00 on Sunday');
    expect(describeCron('30 * * * *')).toBe('Every hour at minute 30');
  });
  it('describes day ranges, lists and names', () => {
    expect(describeCron('0 9 * * 1-5')).toBe('At 09:00 on weekdays');
    expect(describeCron('0 9 * * MON-FRI')).toBe('At 09:00 on weekdays');
    expect(describeCron('0 9 * * 1-4')).toBe('At 09:00 on Monday to Thursday');
    expect(describeCron('0 9 * * 1,3,5')).toBe('At 09:00 on Monday, Wednesday and Friday');
    expect(describeCron('0 9 * * 6,0')).toBe('At 09:00 on weekends');
    expect(describeCron('0 3 * * 7')).toBe('At 03:00 on Sunday');
    expect(describeCron('0 9,17 * * *')).toBe('Every day at 09:00 and 17:00');
    expect(describeCron('15 4 1 * *')).toBe('At 04:15 on day 1 of every month');
  });
  it('describes descriptors', () => {
    expect(describeCron('@daily')).toBe('Every day at 00:00');
    expect(describeCron('@hourly')).toBe('Every hour');
    expect(describeCron('@weekly')).toBe('Every Sunday at 00:00');
  });
  it('returns empty for anything else', () => {
    expect(describeCron('1-5 * * * *')).toBe('');
    expect(describeCron('bogus')).toBe('');
    expect(describeCron('@every 5m')).toBe('');
    expect(describeCron('0 0 1 1 *')).toBe('');
    expect(describeCron('0 0 1 * 1')).toBe('');
    expect(describeCron('0 9 * * 8')).toBe('');
    expect(describeCron('*/5 * * * 1-5')).toBe('');
  });
});
