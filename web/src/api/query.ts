/** Builds a query string from defined values; booleans become "true"/"false". */
export function qs(params: object | undefined): string {
  if (!params) return '';
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params as Record<string, unknown>)) {
    if (v === undefined || v === null || v === '') continue;
    u.set(k, String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : '';
}

export function seg(s: string | number): string {
  return encodeURIComponent(String(s));
}

/** Lowercase plural path segment for a workload kind. */
export function kindSegment(kind: string): string {
  const map: Record<string, string> = {
    Deployment: 'deployments',
    StatefulSet: 'statefulsets',
    DaemonSet: 'daemonsets',
    Rollout: 'rollouts',
    CronJob: 'cronjobs',
  };
  return map[kind] ?? kind.toLowerCase() + 's';
}

export function kindFromSegment(s: string): string {
  const map: Record<string, string> = {
    deployments: 'Deployment',
    statefulsets: 'StatefulSet',
    daemonsets: 'DaemonSet',
    rollouts: 'Rollout',
    cronjobs: 'CronJob',
  };
  return map[s] ?? s;
}

/** Only same-origin absolute paths are allowed as `next`. */
export function safeNext(path: string): string {
  if (!path.startsWith('/') || path.startsWith('//') || path.includes('\\')) return '/';
  return path;
}

/**
 * Where to sign in: straight to Keycloak, or the login page when it also
 * offers local accounts (LOCAL_USERS_ENABLED).
 */
export function loginUrl(path: string, localLogin = false): string {
  return `${localLogin ? '/login' : '/auth/login'}?next=${encodeURIComponent(safeNext(path))}`;
}
