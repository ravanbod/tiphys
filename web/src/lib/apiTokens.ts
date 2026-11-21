import type { ApiPermission } from '../api';

export type PermissionColumn = 'read' | 'create' | 'update' | 'delete';

/** A row of the permission grid: its permission per column; a missing column does not apply. */
export interface PermissionRow {
  label: string;
  cells: Partial<Record<PermissionColumn, ApiPermission>>;
}

export const PERMISSION_ROWS: PermissionRow[] = [
  {
    label: 'Time windows',
    cells: { read: 'time_windows:read', create: 'time_windows:create', update: 'time_windows:update', delete: 'time_windows:delete' },
  },
  {
    label: 'Holidays',
    cells: { read: 'holidays:read', create: 'holidays:create', update: 'holidays:update', delete: 'holidays:delete' },
  },
  { label: 'Settings', cells: { read: 'settings:read' } },
  { label: 'Settings: break-glass', cells: { update: 'settings.break_glass:write' } },
  { label: 'Settings: session limits', cells: { update: 'settings.exec:write' } },
  { label: 'Settings: retention', cells: { update: 'settings.retention:write' } },
  { label: 'Settings: notifications', cells: { update: 'settings.notifications:write' } },
];

/** Every permission, in the server's catalog order. */
export const ALL_PERMISSIONS: ApiPermission[] = [
  'time_windows:read', 'time_windows:create', 'time_windows:update', 'time_windows:delete',
  'holidays:read', 'holidays:create', 'holidays:update', 'holidays:delete',
  'settings:read', 'settings.break_glass:write', 'settings.exec:write', 'settings.retention:write', 'settings.notifications:write',
];

/** Short names for chips. */
export const PERMISSION_LABEL: Record<ApiPermission, string> = {
  'time_windows:read': 'Windows: read',
  'time_windows:create': 'Windows: create',
  'time_windows:update': 'Windows: update',
  'time_windows:delete': 'Windows: delete',
  'holidays:read': 'Holidays: read',
  'holidays:create': 'Holidays: create',
  'holidays:update': 'Holidays: update',
  'holidays:delete': 'Holidays: delete',
  'settings:read': 'Settings: read',
  'settings.break_glass:write': 'Break-glass: update',
  'settings.exec:write': 'Session limits: update',
  'settings.retention:write': 'Retention: update',
  'settings.notifications:write': 'Notifications: update',
};

/** The read a permission needs: time_windows:delete needs time_windows:read, settings.exec:write settings:read. */
export function readOf(p: ApiPermission): ApiPermission {
  return `${p.split(/[.:]/)[0]}:read` as ApiPermission;
}

/**
 * Turns one permission on or off. A write needs its read, so ticking a write
 * ticks the read, and unticking a read unticks the writes that need it. The
 * result is in catalog order.
 */
export function togglePermission(current: ApiPermission[], p: ApiPermission, on: boolean): ApiPermission[] {
  const set = new Set(current);
  if (on) {
    set.add(p);
    set.add(readOf(p));
  } else {
    set.delete(p);
    if (readOf(p) === p) ALL_PERMISSIONS.filter((x) => readOf(x) === p).forEach((x) => set.delete(x));
  }
  return ALL_PERMISSIONS.filter((x) => set.has(x));
}

/** The expiry choices of the new-token dialog, in days; null is never. */
export const EXPIRY_CHOICES: { days: number | null; label: string }[] = [
  { days: 30, label: '30 days' },
  { days: 90, label: '90 days' },
  { days: 365, label: '1 year' },
  { days: null, label: 'Never' },
];

export function expiryFrom(days: number | null, now: number): string | null {
  return days === null ? null : new Date(now + days * 24 * 3600 * 1000).toISOString();
}

/** A sample body per write, in the order the curl example prefers them. */
const WRITE_EXAMPLES: { perm: ApiPermission; method: string; path: string; body?: unknown }[] = [
  { perm: 'settings.break_glass:write', method: 'PATCH', path: '/admin/settings', body: { breakGlassMode: 'approval' } },
  { perm: 'settings.exec:write', method: 'PATCH', path: '/admin/settings', body: { execIdleTimeoutMinutes: 30 } },
  { perm: 'settings.retention:write', method: 'PATCH', path: '/admin/settings', body: { retentionAuditDays: 365 } },
  { perm: 'settings.notifications:write', method: 'PATCH', path: '/admin/settings', body: { rocket: { events: { admin_changes: true } } } },
  { perm: 'holidays:create', method: 'POST', path: '/admin/holidays', body: { date: '2026-12-31', title: 'New Year freeze', environments: ['prod'] } },
  {
    perm: 'time_windows:create', method: 'POST', path: '/admin/time-windows',
    body: { name: 'Release freeze', scopeKind: 'environment', scopeValue: 'prod', actions: ['exec', 'restart'], daysOfWeek: [5],
      startTime: '00:00', endTime: '00:00', timezone: 'Asia/Tehran', effect: 'deny_inside', enabled: true },
  },
  { perm: 'time_windows:update', method: 'PATCH', path: '/admin/time-windows/{id}', body: { enabled: false } },
  { perm: 'holidays:update', method: 'PATCH', path: '/admin/holidays/{id}', body: { title: 'New Year freeze' } },
  { perm: 'time_windows:delete', method: 'DELETE', path: '/admin/time-windows/{id}' },
  { perm: 'holidays:delete', method: 'DELETE', path: '/admin/holidays/{id}' },
];

const READ_PATHS: Partial<Record<ApiPermission, string>> = {
  'time_windows:read': '/admin/time-windows',
  'holidays:read': '/admin/holidays',
  'settings:read': '/admin/settings',
};

/**
 * Example calls for a new token: a read it may make and, when it may write,
 * one write. The token is read from $TIPHYS_TOKEN, so it stays out of the
 * example (and of shell history when the example is pasted).
 */
export function curlExample(perms: ApiPermission[], origin: string): string {
  const auth = '-H "Authorization: Bearer $TIPHYS_TOKEN"';
  const out: string[] = [];
  const read = ALL_PERMISSIONS.find((p) => perms.includes(p) && READ_PATHS[p]);
  if (read) out.push(`curl ${auth} \\\n  ${origin}/api/v1${READ_PATHS[read]}`);
  const write = WRITE_EXAMPLES.find((w) => perms.includes(w.perm));
  if (write) {
    const lines = [`curl -X ${write.method} ${auth}`];
    if (write.body !== undefined) lines.push(`-H 'Content-Type: application/json'`, `-d '${JSON.stringify(write.body)}'`);
    lines.push(`${origin}/api/v1${write.path}`);
    out.push(lines.join(' \\\n  '));
  }
  return out.join('\n\n');
}
