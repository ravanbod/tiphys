import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { readStorage, writeStorage } from '../hooks/useLocalStorage';

export type ThemePref = 'system' | 'light' | 'dark';
interface ThemeCtx {
  pref: ThemePref;
  dark: boolean;
  setPref: (p: ThemePref) => void;
  toggle: () => void;
}
const Ctx = createContext<ThemeCtx>({ pref: 'system', dark: false, setPref: () => {}, toggle: () => {} });
const KEY = 'tiphys.theme';

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [pref, setPrefState] = useState<ThemePref>(() => readStorage<ThemePref>(KEY, 'system'));
  const [sysDark, setSysDark] = useState(() => window.matchMedia('(prefers-color-scheme: dark)').matches);
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const h = () => setSysDark(mq.matches);
    mq.addEventListener('change', h);
    return () => mq.removeEventListener('change', h);
  }, []);
  const dark = pref === 'dark' || (pref === 'system' && sysDark);
  useEffect(() => {
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light';
    document.documentElement.classList.toggle('dark', dark);
  }, [dark]);
  const setPref = useCallback((p: ThemePref) => {
    setPrefState(p);
    writeStorage(KEY, p);
  }, []);
  const value = useMemo(() => ({ pref, dark, setPref, toggle: () => setPref(dark ? 'light' : 'dark') }), [pref, dark, setPref]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
export const useTheme = () => useContext(Ctx);
