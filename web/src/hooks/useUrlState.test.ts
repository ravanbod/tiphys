import { describe, expect, it } from 'vitest';
import { readUrlState, writeUrlState } from './useUrlState';

const defaults = { q: '', kind: '', open: true, bg: false };

describe('url state', () => {
  it('reads values and falls back to defaults', () => {
    expect(readUrlState(new URLSearchParams('q=pay&open=0&tab=events'), defaults)).toEqual({ q: 'pay', kind: '', open: false, bg: false });
    expect(readUrlState(new URLSearchParams('bg=1'), defaults).bg).toBe(true);
  });
  it('writes a patch, leaving out defaults and keeping other parameters', () => {
    const sp = new URLSearchParams('tab=events&q=old&open=0');
    expect(writeUrlState(sp, defaults, { q: 'new', open: true, bg: true }).toString()).toBe('tab=events&q=new&bg=1');
    expect(writeUrlState(sp, defaults, { q: '' }).toString()).toBe('tab=events&open=0');
  });
});
