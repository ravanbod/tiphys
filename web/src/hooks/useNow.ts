import { useEffect, useState } from 'react';

/** Re-renders every `ms` and returns Date.now(). Pass ms=0 to disable ticking. */
export function useNow(ms = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!ms) return;
    const id = window.setInterval(() => setNow(Date.now()), ms);
    return () => window.clearInterval(id);
  }, [ms]);
  return now;
}
