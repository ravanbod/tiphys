import type { Action, AppAction, GrantInput, GrantView, ScopeView, WorkloadKind } from '../api/types';
import { ADMIN_TZ, isoToLocalInput, localInputToIso } from './adminDatetime';
import { ARGO_UI } from './features';

export interface GrantForm {
  id: number | null;
  holderKind: 'team' | 'user';
  holderId: number | null;
  scopeKind: ScopeView['kind'];
  argoProject: string;
  environment: string;
  cluster: string;
  namespace: string;
  /** More namespaces for a new namespace grant: one grant each, created together. */
  extraNamespaces: string[];
  /** `Kind/name` keys. */
  workloads: string[];
  actions: Action[];
  expiryMode: 'none' | 'date';
  /** `datetime-local` value in ADMIN_TZ. */
  expiry: string;
  note: string;
}

/** Sync and rollback act on whole Argo CD applications, which a workloads scope cannot cover. */
export const APP_SCOPE_HINT = 'Sync and rollback need a namespace, cluster, environment or Argo project scope: they apply to whole Argo CD applications.';

export const ACTIONS: Action[] = ['logs', 'exec', 'restart', 'run-job', 'sync', 'rollback'];

/** Whether the action acts on a whole Argo CD application (sync, rollback). */
export function isAppAction(a: Action): a is AppAction {
  return a === 'sync' || a === 'rollback';
}

/** How an action is named on the grant, request and time window checkboxes. */
export function actionOptionLabel(a: Action): string {
  return a === 'sync' ? 'ArgoCD Sync' : a === 'rollback' ? 'ArgoCD Rollback' : a;
}

export function emptyGrantForm(): GrantForm {
  return {
    id: null, holderKind: 'user', holderId: null, scopeKind: ARGO_UI ? 'argo_project' : 'namespace', argoProject: '', environment: '', cluster: '', namespace: '',
    extraNamespaces: [], workloads: [], actions: ['logs'], expiryMode: 'none', expiry: '', note: '',
  };
}

export const wlKey = (w: { kind: string; name: string }): string => `${w.kind}/${w.name}`;
export function parseWlKey(k: string): { kind: WorkloadKind; name: string } {
  const i = k.indexOf('/');
  return { kind: k.slice(0, i) as WorkloadKind, name: k.slice(i + 1) };
}

export function formFromGrant(g: GrantView): GrantForm {
  return {
    id: g.id,
    holderKind: g.holder.kind,
    holderId: g.holder.id,
    scopeKind: g.scope.kind,
    argoProject: g.scope.argoProject ?? '',
    environment: g.scope.environment ?? '',
    cluster: g.scope.cluster ?? '',
    namespace: g.scope.namespace ?? '',
    extraNamespaces: [],
    workloads: (g.scope.workloads ?? []).map(wlKey),
    actions: g.actions,
    expiryMode: g.expiresAt ? 'date' : 'none',
    expiry: isoToLocalInput(g.expiresAt, ADMIN_TZ),
    note: g.note,
  };
}

/** The server's cap on namespaces created at once (application.MaxGrantBatch). */
export const MAX_GRANT_BATCH = 50;

/** The namespaces a new namespace grant is created on: the chosen one first, then the extra ones. */
export function formNamespaces(f: GrantForm): string[] {
  if (f.scopeKind !== 'namespace' || f.id !== null) return f.namespace ? [f.namespace] : [];
  return [...new Set([f.namespace, ...f.extraNamespaces].filter(Boolean))];
}

/** Local validation; keys mirror the server's `details.fields` names. */
export function validateGrantForm(f: GrantForm, now: number): Record<string, string> {
  const e: Record<string, string> = {};
  if (f.holderId === null) e.holder = `Choose a ${f.holderKind}.`;
  if (f.scopeKind === 'argo_project') {
    if (!f.argoProject) e.argoProject = 'Choose an Argo project.';
  } else if (f.scopeKind === 'environment') {
    if (!f.environment) e.environment = 'Choose an environment.';
  } else if (f.scopeKind === 'cluster') {
    if (!f.cluster) e.cluster = 'Choose a cluster.';
  } else {
    if (!f.cluster) e.cluster = 'Choose a cluster.';
    if (!f.namespace) e.namespace = 'Choose a namespace.';
    if (formNamespaces(f).length > MAX_GRANT_BATCH) e.namespaces = `At most ${MAX_GRANT_BATCH} namespaces at once.`;
    if (f.scopeKind === 'workloads' && f.workloads.length === 0) e.workloads = 'Select at least one workload.';
  }
  if (f.actions.length === 0) e.actions = 'Select at least one action.';
  else if (f.scopeKind === 'workloads' && f.actions.some(isAppAction)) e.actions = APP_SCOPE_HINT;
  if (f.expiryMode === 'date') {
    const iso = localInputToIso(f.expiry);
    if (!iso) e.expiresAt = 'Enter a valid date and time.';
    else if (Date.parse(iso) <= now) e.expiresAt = 'Expiry must be in the future.';
  }
  return e;
}

/** Assumes `validateGrantForm` passed. */
export function grantInputFromForm(f: GrantForm): GrantInput {
  const scope: ScopeView =
    f.scopeKind === 'argo_project'
      ? { kind: 'argo_project', argoProject: f.argoProject }
      : f.scopeKind === 'environment'
        ? { kind: 'environment', environment: f.environment }
        : f.scopeKind === 'cluster'
          ? { kind: 'cluster', cluster: f.cluster }
          : f.scopeKind === 'namespace'
            ? { kind: 'namespace', cluster: f.cluster, namespace: f.namespace }
            : { kind: 'workloads', cluster: f.cluster, namespace: f.namespace, workloads: f.workloads.map(parseWlKey) };
  return {
    holder: { kind: f.holderKind, id: f.holderId as number },
    scope,
    actions: ACTIONS.filter((a) => f.actions.includes(a)),
    expiresAt: f.expiryMode === 'date' ? localInputToIso(f.expiry) : null,
    note: f.note.trim(),
  };
}

const VERB: Record<Action, string> = { logs: 'read logs', exec: 'exec', restart: 'restart', 'run-job': 'run jobs', sync: 'sync Argo CD apps', rollback: 'roll back Argo CD apps' };

/** "read logs, exec and restart" */
export function actionPhrase(actions: Action[]): string {
  const v = ACTIONS.filter((a) => actions.includes(a)).map((a) => VERB[a]);
  if (v.length === 0) return '…';
  if (v.length === 1) return v[0];
  return `${v.slice(0, -1).join(', ')} and ${v[v.length - 1]}`;
}
