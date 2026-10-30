import { beforeEach, describe, expect, it } from 'vitest';
import { fetchOnce, resetFetchOnce } from './onceFetch';

describe('fetchOnce', () => {
  beforeEach(() => resetFetchOnce());

  it('shares the request between immediate callers', async () => {
    let calls = 0;
    const f = () => Promise.resolve(++calls);
    const [a, b] = await Promise.all([fetchOnce('k', f), fetchOnce('k', f)]);
    expect(a).toBe(1);
    expect(b).toBe(1);
    expect(calls).toBe(1);
  });
  it('fetches again after the window or when forced', async () => {
    let calls = 0;
    const f = () => Promise.resolve(++calls);
    await fetchOnce('k', f, { now: () => 0 });
    await fetchOnce('k', f, { now: () => 5000 });
    expect(calls).toBe(2);
    await fetchOnce('k', f, { now: () => 5001, force: true });
    expect(calls).toBe(3);
  });
  it('does not reuse failures', async () => {
    let calls = 0;
    const f = () => (++calls === 1 ? Promise.reject(new Error('x')) : Promise.resolve('ok'));
    await expect(fetchOnce('k', f)).rejects.toThrow('x');
    await Promise.resolve();
    await expect(fetchOnce('k', f)).resolves.toBe('ok');
    expect(calls).toBe(2);
  });
  it('keys are independent', async () => {
    let calls = 0;
    const f = () => Promise.resolve(++calls);
    await fetchOnce('a', f);
    await fetchOnce('b', f);
    expect(calls).toBe(2);
  });
});
