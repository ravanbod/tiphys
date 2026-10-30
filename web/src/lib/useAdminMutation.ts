import { useCallback, useState } from 'react';
import { useToast } from '../context/toast';

export type MutationResult<T> = { ok: true; value: T } | { ok: false };

/** Busy flag + error + toast-on-success wrapper for one admin mutation at a time. */
export function useAdminMutation() {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = useCallback(
    async <T,>(fn: () => Promise<T>, okMessage?: string): Promise<MutationResult<T>> => {
      setBusy(true);
      setError(null);
      try {
        const value = await fn();
        if (okMessage) toast(okMessage);
        return { ok: true, value };
      } catch (e) {
        setError(e);
        return { ok: false };
      } finally {
        setBusy(false);
      }
    },
    [toast],
  );
  const clear = useCallback(() => setError(null), []);
  return { busy, error, run, clear };
}
