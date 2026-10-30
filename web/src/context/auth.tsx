import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { api, setCsrfToken } from '../api';
import type { Me, Role } from '../api';
import { ErrorBanner } from '../components/Feedback';

interface AuthCtx {
  me: Me;
  role: Role;
  isAdmin: boolean;
  isAuditor: boolean;
  isUser: boolean;
  reload: () => Promise<void>;
}
const Ctx = createContext<AuthCtx | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  const [error, setError] = useState<unknown>(null);

  const load = useCallback(async () => {
    const m = await api.me();
    setCsrfToken(m.csrfToken);
    setMe(m);
  }, []);

  useEffect(() => {
    load().catch(setError);
  }, [load]);

  const value = useMemo<AuthCtx | null>(
    () =>
      me && {
        me,
        role: me.role,
        isAdmin: me.role === 'admin',
        isAuditor: me.role === 'auditor',
        isUser: me.role === 'user',
        reload: load,
      },
    [me, load],
  );

  if (error) {
    return (
      <div style={{ padding: 24, maxWidth: 520, margin: '10vh auto' }}>
        <ErrorBanner error={error} title="Could not load your session" onRetry={() => window.location.reload()} />
      </div>
    );
  }
  if (!value) return <div className="boot" aria-busy="true">Loading…</div>;
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth(): AuthCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error('useAuth outside AuthProvider');
  return v;
}
