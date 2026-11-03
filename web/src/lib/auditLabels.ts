import type { AuditEvent } from '../api/types';

const TITLES: Record<string, string> = {
  login: 'Login',
  logout: 'Logout',
  'decision.denied': 'Access denied',
  'logs.view': 'Logs viewed',
  'logs.download': 'Log downloaded',
  'exec.start': 'Exec started',
  'exec.end': 'Exec ended',
  'exec.watch': 'Session watched live',
  'exec.terminate': 'Session terminated',
  'exec.replay': 'Recording replayed',
  restart: 'Restart',
  'run-job': 'CronJob run',
  'argo.sync': 'Argo CD sync',
  'argo.diff': 'Argo CD diff viewed',
  'break_glass.start': 'Break-glass started',
  'break_glass.end': 'Break-glass ended',
  'break_glass.request': 'Break-glass requested',
  'break_glass.reject': 'Break-glass request rejected',
  'break_glass.cancel': 'Break-glass request cancelled',
  'access_request.create': 'Access request created',
  'access_request.decide': 'Access request decided',
  'admin.change': 'Admin change',
  'notification.failed': 'Notification failed',
  'kubeaccess.refused': 'Cluster access refused',
  'session.revoked': 'Session revoked',
};

export const KNOWN_EVENT_TYPES = Object.keys(TITLES);

function cap(s: string): string {
  const t = s.replace(/[_.-]+/g, ' ').trim();
  return t.charAt(0).toUpperCase() + t.slice(1);
}

const PAST: Record<string, string> = { create: 'created', update: 'updated', delete: 'deleted', set: 'changed' };

/** Short human title for an audit event. */
export function eventTitle(e: Pick<AuditEvent, 'eventType' | 'details'>): string {
  if (e.eventType === 'admin.change') {
    const entity = e.details && typeof e.details.entity === 'string' ? e.details.entity : '';
    const op = e.details && typeof e.details.operation === 'string' ? e.details.operation : '';
    if (entity) return `${cap(entity)} ${op ? (PAST[op] ?? op.replace(/_/g, ' ')) : 'changed'}`;
  }
  return TITLES[e.eventType] ?? cap(e.eventType);
}

/** "payments-api-7d9c6b5f4-2kq8x" -> "…2kq8x". */
export function podSuffix(pod: string): string {
  const i = pod.lastIndexOf('-');
  return i > 0 && i < pod.length - 1 ? `…${pod.slice(i + 1)}` : pod;
}

/** "payments-api-7d9c6b5f4-2kq8x" -> "payments-api-…2kq8x" (for narrow table cells). */
export function podMiddleEllipsis(pod: string): string {
  if (pod.length <= 22) return pod;
  const parts = pod.split('-');
  if (parts.length < 3) return pod;
  return `${parts.slice(0, -2).join('-')}-…${parts[parts.length - 1]}`;
}

/** "prod-1 / payments-api / …2kq8x"; falls back to details for admin changes. null when nothing to show. */
export function targetLine(e: AuditEvent): string | null {
  const parts: string[] = [];
  if (e.cluster) parts.push(e.cluster);
  if (e.workloadName) parts.push(e.workloadName);
  else if (e.namespace) parts.push(e.namespace);
  if (e.pod) parts.push(podSuffix(e.pod));
  if (parts.length) return parts.join(' / ');
  const d = e.details;
  if (d && typeof d.entity === 'string') {
    const id = d.id !== undefined && d.id !== null ? ` ${String(d.id)}` : '';
    return `${d.entity.replace(/_/g, ' ')}${id}`;
  }
  return null;
}

/** Full target with the whole pod name, for the detail view. */
export function targetFull(e: AuditEvent): string | null {
  const parts: string[] = [];
  if (e.cluster) parts.push(e.cluster);
  if (e.namespace) parts.push(e.namespace);
  if (e.workloadName) parts.push(e.workloadKind ? `${e.workloadKind.toLowerCase()}/${e.workloadName}` : e.workloadName);
  if (e.pod) parts.push(e.pod);
  return parts.length ? parts.join(' / ') : null;
}

const REASONS: Record<string, string> = {
  protected_namespace: 'protected namespace',
  user_disabled: 'user disabled',
  role: 'role not allowed',
  no_grant: 'no grant',
  grant_expired: 'grant expired',
  outside_window: 'outside time window',
  holiday: 'holiday',
  argo_stale: 'Argo data stale',
};

/** Reason text for the Result column ("outside time window"). */
export function reasonText(reason: string | null | undefined): string {
  if (!reason) return '';
  return REASONS[reason] ?? reason.replace(/_/g, ' ');
}
