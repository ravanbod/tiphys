/**
 * Settings → Argo CD sync: cluster-scoped kinds ("Kind.group") whose
 * resources do not leave syncing an app to admins. Mirrors
 * domain.CheckSyncClusterKind; the server checks again.
 */
export const MAX_SYNC_CLUSTER_KINDS = 50;

const NEVER_GROUPS = ['rbac.authorization.k8s.io', 'admissionregistration.k8s.io', 'apiextensions.k8s.io', 'apiregistration.k8s.io'];
const NEVER_KINDS = ['StorageClass.storage.k8s.io', 'PriorityClass.scheduling.k8s.io'];
const KIND = /^[A-Z][A-Za-z0-9]*$/;
const GROUP = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$/;

/** Why an entry cannot be listed, or '' when it can. */
export function checkSyncClusterKind(entry: string): string {
  const dot = entry.indexOf('.');
  const kind = dot < 0 ? entry : entry.slice(0, dot);
  const group = dot < 0 ? '' : entry.slice(dot + 1);
  if (!KIND.test(kind) || !GROUP.test(group)) {
    return KIND.test(entry) ? 'core kinds cannot be allowed' : 'must be Kind.group, e.g. ClusterInput.fluentbit.fluent.io';
  }
  if (NEVER_GROUPS.includes(group)) return `kinds of ${group} cannot be allowed`;
  if (NEVER_KINDS.includes(entry)) return `${entry} cannot be allowed`;
  return '';
}

/** The entries of the text box: one per line (or comma), blanks dropped. */
export function parseSyncClusterKinds(text: string): string[] {
  return text.split(/[\n,]/).map((s) => s.trim()).filter(Boolean);
}

/** The first problem in the list, or '' if none (as the server words it). */
export function syncClusterKindsError(kinds: string[]): string {
  if (kinds.length > MAX_SYNC_CLUSTER_KINDS) return `at most ${MAX_SYNC_CLUSTER_KINDS} kinds`;
  const seen = new Set<string>();
  for (const k of kinds) {
    const msg = checkSyncClusterKind(k);
    if (msg) return `${k}: ${msg}`;
    if (seen.has(k)) return `${k}: listed twice`;
    seen.add(k);
  }
  return '';
}
