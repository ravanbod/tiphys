// @vitest-environment jsdom
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { LiveChange } from '../api';
import { LiveIndicator, LiveProvider, useLive } from './live';
import { ToastProvider } from './toast';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** A stand-in EventSource the test drives. */
class FakeES {
  static last: FakeES | null = null;
  static CLOSED = 2;
  readyState = 1;
  onerror: (() => void) | null = null;
  ls: Record<string, ((e: MessageEvent) => void)[]> = {};
  constructor(public url: string) { FakeES.last = this; }
  addEventListener(t: string, fn: (e: MessageEvent) => void) { (this.ls[t] ??= []).push(fn); }
  close() { this.readyState = 2; }
  emit(t: string, data: unknown) { for (const f of this.ls[t] ?? []) f(new MessageEvent(t, { data: JSON.stringify(data) })); }
}

const restart = (name: string): LiveChange => ({ cluster: 'prod-1', namespace: 'shop', kind: 'Deployment', name, type: 'pod_restarted', pod: `${name}-a`, container: 'app', reason: 'OOMKilled', exitCode: 137 });

let refreshed: LiveChange[][] = [];
function Page({ ns, name }: { ns: string; name?: string }) {
  useLive({ cluster: 'prod-1', namespace: ns, ...(name ? { kind: 'Deployment', name } : {}) }, (c) => refreshed.push(c));
  return createElement(LiveIndicator);
}

function mount(ns = 'shop', name?: string) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const root = createRoot(el);
  act(() => root.render(createElement(MemoryRouter, null, createElement(ToastProvider, null, createElement(LiveProvider, null, createElement(Page, { ns, name }))))));
  act(() => vi.advanceTimersByTime(400)); // the stream opens after a short wait
  return { el, unmount: () => act(() => root.unmount()) };
}

describe('LiveProvider', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeES);
    refreshed = [];
    FakeES.last = null;
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    document.body.innerHTML = '';
  });

  it('follows the page namespace and hands it the changes in batches', () => {
    const { el, unmount } = mount();
    expect(FakeES.last?.url).toBe('/api/v1/live?ns=prod-1%2Fshop');
    act(() => FakeES.last!.emit('ready', { namespaces: [{ cluster: 'prod-1', namespace: 'shop' }] }));
    expect(el.querySelector('.live')?.textContent).toContain('Live');
    act(() => {
      FakeES.last!.emit('change', restart('api'));
      FakeES.last!.emit('change', { ...restart('other'), namespace: 'elsewhere' });
      FakeES.last!.emit('change', restart('worker'));
    });
    expect(refreshed).toEqual([]);
    act(() => vi.advanceTimersByTime(450));
    expect(refreshed.map((b) => b.map((c) => c.name))).toEqual([['api', 'worker']]);
    unmount();
  });

  it('shows at most three toasts, folds repeats and mutes per page', () => {
    const { unmount } = mount();
    act(() => FakeES.last!.emit('ready', { namespaces: [] }));
    act(() => {
      for (const n of ['a', 'b', 'c', 'd', 'e', 'a']) FakeES.last!.emit('change', restart(n));
    });
    const toasts = () => [...document.querySelectorAll('.ltoast .ltoast-t')].map((t) => t.textContent);
    expect(toasts()).toEqual(['d restarted', 'e restarted', 'a restarted']);
    expect(document.querySelector('.ltoast-n')?.textContent).toBe('×2');
    expect(document.querySelector('.ltoasts-more')?.textContent).toBe('+2 more');

    act(() => (document.querySelector('.ltoasts-bar button') as HTMLButtonElement).click()); // Mute on this page
    expect(toasts()).toEqual([]);
    expect(document.querySelector('.live')?.getAttribute('aria-pressed')).toBe('true');
    act(() => FakeES.last!.emit('change', restart('f')));
    expect(toasts()).toEqual([]);
    act(() => (document.querySelector('.live') as HTMLButtonElement).click()); // unmute
    act(() => FakeES.last!.emit('change', restart('g')));
    expect(toasts()).toEqual(['g restarted']);
    unmount();
  });

  it('on a workload page, only that workload refreshes and toasts', () => {
    const { unmount } = mount('shop', 'api');
    act(() => FakeES.last!.emit('ready', { namespaces: [] }));
    act(() => {
      FakeES.last!.emit('change', restart('worker'));
      FakeES.last!.emit('change', restart('api'));
    });
    act(() => vi.advanceTimersByTime(450));
    expect(refreshed.map((b) => b.map((c) => c.name))).toEqual([['api']]);
    expect([...document.querySelectorAll('.ltoast .ltoast-t')].map((t) => t.textContent)).toEqual(['api restarted']);
    unmount();
  });

  it('lets pages refetch after a reconnect', () => {
    const { unmount } = mount();
    act(() => FakeES.last!.emit('ready', { namespaces: [] }));
    act(() => FakeES.last!.onerror?.());
    expect(document.querySelector('.live')?.textContent).toContain('Reconnecting');
    act(() => FakeES.last!.emit('ready', { namespaces: [] }));
    act(() => vi.advanceTimersByTime(450));
    expect(refreshed).toEqual([[]]);
    unmount();
  });

  it('does not follow invalid names', () => {
    const { unmount } = mount('Bad_NS');
    expect(FakeES.last).toBeNull();
    unmount();
  });
});
