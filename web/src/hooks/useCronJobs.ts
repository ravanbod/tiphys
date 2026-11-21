import { api } from '../api';
import { useAuth } from '../context/auth';
import { useLive } from '../context/live';
import { breakGlassKey } from '../lib/breakGlass';
import { useApi } from './useApi';

/** The namespace's CronJobs with their jobs, reloaded when one of them changes. */
export function useCronJobs(cluster: string, namespace: string, opts: { only?: string; enabled?: boolean } = {}) {
  const { me } = useAuth();
  const { only, enabled = true } = opts;
  const live = useLive(enabled ? { cluster, namespace, ...(only ? { kind: 'CronJob', name: only } : {}) } : null, (changes) => {
    if (changes.length === 0 || changes.some((c) => c.kind === 'CronJob')) list.reload();
  });
  const list = useApi(() => api.cronJobs(cluster, namespace), [cluster, namespace, breakGlassKey(me)], { enabled, poll: live ? undefined : 30000 });
  return list;
}
