import { createContext, useContext, useEffect, useState } from 'react';

export interface ShellCtx {
  cluster?: string;
  environment?: string;
}
const Ctx = createContext<{ ctx: ShellCtx; set: (c: ShellCtx) => void }>({ ctx: {}, set: () => {} });

export function ShellContextProvider({ children }: { children: React.ReactNode }) {
  const [ctx, set] = useState<ShellCtx>({});
  return <Ctx.Provider value={{ ctx, set }}>{children}</Ctx.Provider>;
}
export const useShellCtx = () => useContext(Ctx).ctx;

/** Pages call this to show "cluster · env" in the top bar. */
export function useSetShellCtx(cluster?: string, environment?: string) {
  const { set } = useContext(Ctx);
  useEffect(() => {
    set({ cluster, environment });
    return () => set({});
  }, [cluster, environment, set]);
}
