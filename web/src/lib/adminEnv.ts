import type { ClusterView, EnvironmentOverride, ScopeView } from '../api/types';

/**
 * Best-effort environment of a scope for display (namespace override -> cluster label; Argo project scopes use a project override).
 * The portal itself decides the real one; the API does not return an environment on access requests.
 */
export function scopeEnvironment(
  scope: ScopeView,
  clusters: ClusterView[] | undefined,
  overrides: EnvironmentOverride[] | undefined,
): string | null {
  if (scope.kind === 'argo_project') {
    return overrides?.find((o) => o.kind === 'argo_project' && o.argoProject === scope.argoProject)?.environment ?? null;
  }
  const ns = overrides?.find(
    (o) => o.kind === 'namespace' && o.cluster === scope.cluster && o.namespace === scope.namespace,
  );
  if (ns) return ns.environment;
  return clusters?.find((c) => c.name === scope.cluster)?.environment ?? null;
}
