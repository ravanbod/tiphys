import { createContext, useCallback, useContext, useRef, useState } from 'react';

interface Toast {
  id: number;
  text: string;
  tone: 'ok' | 'err';
}
const Ctx = createContext<(text: string, tone?: 'ok' | 'err') => void>(() => {});

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<Toast[]>([]);
  const idRef = useRef(1);
  const push = useCallback((text: string, tone: 'ok' | 'err' = 'ok') => {
    const id = idRef.current++;
    setItems((x) => [...x, { id, text, tone }]);
    window.setTimeout(() => setItems((x) => x.filter((t) => t.id !== id)), tone === 'err' ? 8000 : 4000);
  }, []);
  return (
    <Ctx.Provider value={push}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={`toast ${t.tone}`}>
            {t.text}
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}
export const useToast = () => useContext(Ctx);
