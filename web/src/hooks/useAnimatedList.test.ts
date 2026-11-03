// @vitest-environment jsdom
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useAnimatedList } from './useAnimatedList';
import type { AnimatedRow } from './useAnimatedList';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface Item { name: string; v: number }

let renders = 0;
let last: AnimatedRow<Item>[] = [];
function Probe({ items, scope }: { items: Item[] | undefined; scope: string }) {
  renders++;
  // A new array on every render, as filtered lists are.
  last = useAnimatedList(items ? [...items] : undefined, (i) => i.name, (i) => String(i.v), scope);
  return null;
}

function mount() {
  const root = createRoot(document.createElement('div'));
  const render = (items: Item[] | undefined, scope = 's') => act(() => root.render(createElement(Probe, { items, scope })));
  return { render, unmount: () => act(() => root.unmount()) };
}
const anims = () => Object.fromEntries(last.map((r) => [r.key, r.anim]));

describe('useAnimatedList', () => {
  afterEach(() => vi.useRealTimers());

  it('marks new, changed and removed rows after the first data, then settles', () => {
    vi.useFakeTimers();
    const { render, unmount } = mount();
    render(undefined);
    render([{ name: 'a', v: 1 }, { name: 'b', v: 1 }]);
    expect(anims()).toEqual({ a: '', b: '' });

    render([{ name: 'a', v: 2 }, { name: 'c', v: 1 }]);
    expect(last.map((r) => r.key)).toEqual(['a', 'b', 'c']); // b is still shown, where it was
    expect(anims()).toEqual({ a: 'changed', b: 'leave', c: 'enter' });

    act(() => vi.advanceTimersByTime(500));
    expect(anims()).toEqual({ a: 'changed', c: 'enter' });
    act(() => vi.advanceTimersByTime(2000));
    expect(anims()).toEqual({ a: '', c: '' });
    unmount();
  });

  it('does not animate a new scope and does not loop on new arrays', () => {
    const { render, unmount } = mount();
    render([{ name: 'a', v: 1 }]);
    render([{ name: 'x', v: 1 }], 'other');
    expect(anims()).toEqual({ x: '' });
    renders = 0;
    render([{ name: 'x', v: 1 }], 'other');
    render([{ name: 'x', v: 1 }], 'other');
    expect(renders).toBe(2);
    unmount();
  });

  it('keeps repeated keys apart', () => {
    const { render, unmount } = mount();
    render([{ name: 'a', v: 1 }, { name: 'a', v: 2 }]);
    expect(last.map((r) => r.key)).toEqual(['a', 'a#2']);
    unmount();
  });
});
