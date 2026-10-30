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
  it('returns empty for anything else', () => {
    expect(describeCron('1-5 * * * *')).toBe('');
    expect(describeCron('bogus')).toBe('');
    expect(describeCron('0 0 1 * *')).toBe('');
  });
});
