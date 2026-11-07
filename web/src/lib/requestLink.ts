import type { Action, WorkloadKind } from '../api/types';
import { isAppAction } from './grantForm';

/** What a "Request access" link fills the request form with. */
export interface RequestPrefill {
  cluster: string;
  namespace: string;
  action: Action;
  /** For an action on one workload; ignored for sync and rollback, which cover whole apps. */
  workload?: { kind: WorkloadKind; name: string };
}

const ACTIONS: Action[] = ['logs', 'exec', 'restart', 'run-job', 'sync', 'rollback'];
const KINDS: WorkloadKind[] = ['Deployment', 'StatefulSet', 'DaemonSet', 'Rollout', 'CronJob'];

/** The My access page with the request form open, filled with `p` if given. */
export function requestUrl(p?: RequestPrefill): string {
  const q = new URLSearchParams({ request: '1' });
  if (p) {
    q.set('cluster', p.cluster);
    q.set('namespace', p.namespace);
    q.set('action', p.action);
    if (p.workload && !isAppAction(p.action)) {
      q.set('kind', p.workload.kind);
      q.set('name', p.workload.name);
    }
  }
  return `/access?${q}`;
}

/** The prefill a request link carries, or null when it carries none (or a bad one). */
export function requestPrefill(sp: URLSearchParams): RequestPrefill | null {
  const cluster = sp.get('cluster') ?? '';
  const namespace = sp.get('namespace') ?? '';
  const action = sp.get('action') as Action;
  if (!cluster || !namespace || !ACTIONS.includes(action)) return null;
  const kind = sp.get('kind') as WorkloadKind;
  const name = sp.get('name') ?? '';
  return KINDS.includes(kind) && name && !isAppAction(action) ? { cluster, namespace, action, workload: { kind, name } } : { cluster, namespace, action };
}
