import type { ScopeView } from '../api/types';

export const SCOPE_KIND_LABEL: Record<ScopeView['kind'], string> = {
  argo_project: 'Argo project',
  namespace: 'Namespace',
  workloads: 'Workloads',
};

/** Main identifier of a scope: project name, namespace, or "N in namespace". */
export function scopeName(s: ScopeView): string {
  if (s.kind === 'argo_project') return s.argoProject ?? '';
  if (s.kind === 'namespace') return s.namespace ?? '';
  const n = s.workloads?.length ?? 0;
  return n === 1 ? (s.workloads?.[0].name ?? '') : `${n} in ${s.namespace ?? ''}`;
}

/** Cluster text for a scope ("all clusters" for Argo project scopes). */
export function scopeCluster(s: ScopeView): string {
  return s.kind === 'argo_project' ? 'all clusters' : (s.cluster ?? '');
}

/** `[Argo project] payments` as in the grant tables. */
export function ScopeCell({ scope, struck }: { scope: ScopeView; struck?: boolean }) {
  return (
    <span>
      <span className="tag">{SCOPE_KIND_LABEL[scope.kind]}</span>{' '}
      <b className="mono" style={{ marginLeft: 4, fontWeight: 500, textDecoration: struck ? 'line-through' : undefined }}>{scopeName(scope)}</b>
    </span>
  );
}

/** Plain-language sentence part, e.g. "Argo project payments (all clusters)". */
export function describeScope(s: ScopeView): string {
  if (s.kind === 'argo_project') return `Argo project ${s.argoProject} (all clusters)`;
  if (s.kind === 'namespace') return `namespace ${s.namespace} on cluster ${s.cluster}`;
  const names = (s.workloads ?? []).map((w) => w.name).join(', ');
  return `${s.workloads?.length ?? 0} workload(s) (${names}) in namespace ${s.namespace} on cluster ${s.cluster}`;
}
